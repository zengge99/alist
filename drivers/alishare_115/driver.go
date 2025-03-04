package alishare_pan115

import (
	"context"
	"net/http"
	"time"
	"fmt"
	"encoding/json"
	"github.com/tidwall/gjson"

	//"bytes"
	"crypto/tls"
	"io"
	"net/url"
	"strconv"
	"strings"
	
	"github.com/alist-org/alist/v3/drivers/base"
    "github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	//"github.com/alist-org/alist/v3/internal/stream"
	"github.com/alist-org/alist/v3/pkg/cron"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	//crypto "github.com/gaoyb7/115drive-webdav/115"
	"github.com/orzogc/fake115uploader/cipher"
	"github.com/pkg/errors"
	//"github.com/alist-org/alist/v3/pkg/http_range"
	//"golang.org/x/time/rate"

	"crypto/sha1"
	"crypto/md5"
	"bufio"
	"encoding/hex"
    "os"
)

//var UserAgent = driver115.UA115Desktop
//var UserAgent = "Mozilla/5.0 115disk/30.5.1"
var UserAgent = "Mozilla/5.0 115Browser/27.0.3.7"

const (
	//appVer = "30.5.1"
	appVer = "27.0.3.7"
)

type AliyundriveShare2Pan115 struct {
	model.Storage
	Addition
	AccessToken string
	ShareToken  string
//	DriveId     string
	cron        *cron.Cron
	cron1	    *cron.Cron
	cron2	    *cron.Cron
	cron3	    *cron.Cron	
	base             string
    	MyAliDriveId     string
	backup_drive_id	 string
	resource_drive_id	string
	AccessTokenOpen  string
	CopyFiles        map[string]string
	DownloadUrl_dict map[string]string
	FileHash_dict map[string]string
	FileSize_dict map[string]int64
	FileID_Link		 map[string]string
	FileID_Link_model		 map[string]*model.Link
	client  *driver115.Pan115Client
	pan115LoginStatus	bool
	pickCodeMap map[string]string
}

const md5Salt = "Qclm8MGWUv59TnrR0XPg"
func (d *AliyundriveShare2Pan115) Generate115Token(fileID, preID, timeStamp, fileSize, signKey, signVal string) string {
	userID := strconv.FormatInt(d.client.UserID, 10)
	userIDMd5 := md5.Sum([]byte(userID))
	tokenMd5 := md5.Sum([]byte(md5Salt + fileID + fileSize + signKey + signVal + userID + timeStamp + hex.EncodeToString(userIDMd5[:]) + appVer))
	return hex.EncodeToString(tokenMd5[:])
}

func (d *AliyundriveShare2Pan115) Config() driver.Config {
	return config
}

func (d *AliyundriveShare2Pan115) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *AliyundriveShare2Pan115) Init(ctx context.Context) error {
	err := d.refreshToken()
	if err != nil {
		return err
	}
	err2 := d.getShareToken()
	if err2 != nil {
		return err2
	}

    // d.OauthTokenURL = conf.Conf.Opentoken_auth_url
	err = d.refreshTokenOpen()

	var siteMap map[string]string
    var downloadurlmap map[string]string
	var fileid_link map[string]string
	var filehash_map map[string]string
	var filesize_map map[string]int64
	var fileid_link_model map[string]*model.Link
    downloadurlmap = make(map[string]string)
	fileid_link = make(map[string]string)
	fileid_link_model = make(map[string]*model.Link)
	siteMap = make(map[string]string)
	filehash_map = make(map[string]string)
	filesize_map = make(map[string]int64)
	d.CopyFiles = siteMap
	d.DownloadUrl_dict = downloadurlmap
	d.FileHash_dict = filehash_map
	d.FileSize_dict = filesize_map
	d.FileID_Link = fileid_link
	d.FileID_Link_model = fileid_link_model

	//res, err := d.request("https://api.aliyundrive.com/v2/user/get", http.MethodPost, nil)
	res, err := d.requestOpen("/adrive/v1.0/user/getDriveInfo", http.MethodPost, func(req *resty.Request){})
	if err != nil {
		return err
	}
	d.MyAliDriveId = utils.Json.Get(res, "default_drive_id").ToString()
	d.backup_drive_id = utils.Json.Get(res, "backup_drive_id").ToString()
	d.resource_drive_id = utils.Json.Get(res, "resource_drive_id").ToString()
	if d.resource_drive_id != "" {
		d.MyAliDriveId = d.resource_drive_id 
		//fmt.Println("资源库ID:", d.resource_drive_id)
	}

	d.cron = cron.NewCron(time.Hour * 2)
	d.cron.Do(func() {
		err := d.refreshToken()
		if err != nil {
			log.Errorf("%+v", err)
		}
	})

    d.cron2 = cron.NewCron(time.Minute * 13)
    d.cron2.Do(func() {
	if len(d.FileID_Link) > 0 {
		fmt.Println(time.Now().Format("01-02-2006 15:04:05")," 清空缓存下载链接: ", d.MountPath) //d.ShareId) //d.MyAliDriveId)
		d.DownloadUrl_dict = make(map[string]string)
		d.FileID_Link = make(map[string]string)
		d.FileID_Link_model = make(map[string]*model.Link)
		d.CopyFiles = make(map[string]string)
		d.FileHash_dict = make(map[string]string)
		d.FileSize_dict = make(map[string]int64)
	}
    })
	d.pan115LoginStatus = false
	d.pickCodeMap = make(map[string]string)
	return d.preLogin()
}

func (d *AliyundriveShare2Pan115) Drop(ctx context.Context) error {
	if d.cron != nil { d.cron.Stop() }	
	if d.cron2 != nil { d.cron2.Stop() }		
	if d.cron3 != nil { d.cron3.Stop() }		
	d.MyAliDriveId = ""
	return nil
}

func (d *AliyundriveShare2Pan115) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
    count := 0
	for {
		files, err := d.getFiles(dir.GetID())
		if err != nil {
			//if count > conf.Conf.Retry_count {
			if count > 4 {
				fmt.Println("获取目录列表失败，结束重试",d.MountPath,": ",dir.GetName())
				return nil, err
			}
			count += 1
			fmt.Println("获取目录列表失败，重试第",count,"次 ",d.MountPath,": ",dir.GetName())
			time.Sleep(2 * time.Second)
		}	

		if err == nil {
			return utils.SliceConvert(files, func(src File) (model.Obj, error) {
				return fileToObj(src), nil
			})
		}	
	}
}

func calculateSHA1Range(url string, start int64, end int64) (string, error) {
    client := &http.Client{}
    req, err := http.NewRequest("GET", url, nil)
    if err != nil {
        return "", err
    }

    rangeHeader := fmt.Sprintf("bytes=%d-%d", start, end)
    req.Header.Set("Range", rangeHeader)

    resp, err := client.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()

    var buf []byte
    readBytes := end - start + 1
    buf = make([]byte, readBytes)

    _, err = io.ReadAtLeast(resp.Body, buf, int(readBytes))
    if err != nil {
        return "", err
    }

    hash := sha1.New()
    hash.Write(buf)
    sha1Hash := hash.Sum(nil)

    // Convert the SHA1 hash to uppercase
    sha1HashUpper := fmt.Sprintf("%X", sha1Hash)

    return sha1HashUpper, nil
}

func (d *AliyundriveShare2Pan115) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	file_id :=  file.GetID()
	file_name := file.GetName()

	//不放Init里面，避免加载速度变慢
	if !d.pan115LoginStatus {
		if err:= d.login(); err == nil {
			d.pan115LoginStatus = true
		}
	}

	if cachePickCode, ok := d.pickCodeMap[file_id]; ok {
	    var userAgent = args.Header.Get("User-Agent")
	    downloadInfo, err := d.client.DownloadWithUA(cachePickCode, userAgent)
	    if err == nil {
		    fmt.Println("重新获取115已有文件新链接：", downloadInfo.Url.Url)
	        link := &model.Link{
		        URL:    downloadInfo.Url.Url,
		        Header: downloadInfo.Header,
	        }
	        return link, nil
	    }
	}

	new_file_id, err := d.Copy2Myali( ctx , d.MyAliDriveId, file_id, file_name)
	if err != nil || new_file_id == "" {
		return &model.Link{
			Header: http.Header{
				"Referer": []string{"https://www.aliyundrive.com/"},
			},
			URL: "http:/GetmyLink/img.xiaoya.pro/abnormal.png",
		}, nil
	} 

	//time.Sleep(2 * 1000 * time.Millisecond)
	DownloadUrl, ContentHash, fileSize, err := d.GetmyLink(ctx, new_file_id, file_name)
	if err != nil {
		fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"获取转存后的直链失败！！！",err)
	}
	if err == nil {
		d.FileID_Link[file_id] = DownloadUrl
	}	

	link := &model.Link{
		Header: http.Header{
			"Referer": []string{"https://www.aliyundrive.com/"},
		},
		URL: DownloadUrl,
	}

	//甲骨文等海外环境，115 API可能临时被block导致超时，5秒未响应则强制返回阿里直链
	fullHash := "123"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) 
    defer cancel() 
    done := make(chan struct{})
    go func() {
		defer close(done)
		if ok, err := d.client.UploadAvailable(); err != nil || !ok {
			fmt.Println("[Debug] UploadAvailable failed",err)
			return
		}
		
        	//preHash := "2EF7BDE608CE5404E97D5F042F95F89F1C232871"
		preHash, _ := calculateSHA1Range(link.URL, 0, 128 * 1024 - 1)
	    	fullHash = ContentHash

		var fastInfo *driver115.UploadInitResp
		now := time.Now()
		timestamp := now.Format("20060102_150405")
		lastDotIndex := strings.LastIndex(file_name, ".")
		if lastDotIndex != -1 {
		    name := file_name[:lastDotIndex]
		    ext := file_name[lastDotIndex:]
		    file_name = fmt.Sprintf("%s_%s%s", name, timestamp, ext)
		} else {
		    file_name = fmt.Sprintf("%s_%s", file_name, timestamp)
		}
		if fastInfo, err = d.rapidUpload(fileSize, file_name, d.DirId, preHash, fullHash, link.URL); err != nil {
			fmt.Println("[Debug] rapidUpload failed",err)
			//time.Sleep(2000 * time.Millisecond)
			return
		}

		success := false
	    	var err1 error
		for i := 0; i < 5; i++ {
			var userAgent = args.Header.Get("User-Agent")
			downloadInfo, err := d.client.DownloadWithUA(fastInfo.PickCode, userAgent)
			if err != nil {
				err1 = err
				time.Sleep(200 * time.Millisecond)
				continue
			}
			success = true
			d.pickCodeMap[file_id] = fastInfo.PickCode
			fmt.Println("获取到115下载新链接：", downloadInfo.Url.Url)
			link = &model.Link{
				URL:    downloadInfo.Url.Url,
				Header: downloadInfo.Header,
			}
			break
		}
		if !success {
			fmt.Println("[Debug] DownloadWithUA failed", err1)
			return
		}
    }()

    select {
		case <-ctx.Done():
			fmt.Println("[Debug]访问115 API超时，可能是网络问题")
		case <-done:
    }

	go func() {
        time.Sleep(2 * time.Second)
		if files, err := d.client.List(d.DirId); err == nil && d.PurgePan115Temp {
			for i := 0; i < len(*files); i++ {
				file := (*files)[i]
				if file.Name == file_name && strings.ToUpper(file.Sha1) == fullHash{
					d.client.Delete(file.FileID)
				}
			}
		}
		if d.PurgeAliTemp{
			d.Purge_temp_folder(ctx)
		}
    }()

	d.FileID_Link_model[file_id] = link

	return link, nil
}

func (d *AliyundriveShare2Pan115) Copy2Myali(ctx context.Context, src_driveid string, file_id string, file_name string) (string, error) {

	Newfile_id, ok := d.CopyFiles[file_id]  // 如果键不存在，ok 的值为 false，v2 的值为该类型的零值
	if ok {
		fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"文件已转存: ",file_name)
		return Newfile_id, nil
	}
    targetUrl := "https://api.aliyundrive.com/adrive/v2/batch"
	jsonData := map[string]interface{}{
		"resource": "file",
		"requests": []interface{}{
			map[string]interface{}{
				"method": "POST",
				"url": "/file/copy",
				"id": "0",
				"headers": map[string]interface{}{"Content-Type": "application/json"},
				"body": map[string]interface{}{
				"file_id": file_id,
				"share_id": d.ShareId,
				"auto_rename": true,
				"to_parent_file_id": d.TempTransferFolderID,
				"to_drive_id": d.MyAliDriveId,
				},
			},},
	}
	r, err := d.request(targetUrl, http.MethodPost, func(req *resty.Request) {
		req.SetBody(jsonData)
	})
	if err != nil {
		fmt.Println("转存失败: ",string(r),err)
		return "", err
	}

	var responses map[string]interface{}
	json.Unmarshal([]byte(r), &responses)

	respon := responses["responses"].([]interface{})[0]
	Newfile_id, _ = respon.(map[string]interface{})["body"].(map[string]interface{})["file_id"].(string)

	if Newfile_id != "" {
		d.CopyFiles[file_id] = Newfile_id
		fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"新增转存记录, 挂载路径: ",d.MountPath," 文件: ",file_name)	
	}
	
	if Newfile_id == "" {
		NNewfile_id := utils.Json.Get(r, "file_id").ToString()
		if NNewfile_id != "" {
			d.CopyFiles[file_id] = NNewfile_id
			fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"新增转存记录, file_id(x): ",NNewfile_id," 文件: ",file_name)	
		}
		if NNewfile_id == "" {
            r, err := d.request(targetUrl, http.MethodPost, func(req *resty.Request) {req.SetBody(jsonData)})
            if err != nil {
                fmt.Println("转存失败: ",string(r),err)
                return "", err
            }
            var responses map[string]interface{}
            json.Unmarshal([]byte(r), &responses)
            respon := responses["responses"].([]interface{})[0]
            Newfile_id, _ = respon.(map[string]interface{})["body"].(map[string]interface{})["file_id"].(string)
            if Newfile_id != "" {
                d.CopyFiles[file_id] = Newfile_id
                fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"新增转存记录, 挂载路径: ",d.MountPath," 文件: ",file_name) 
			}
            if Newfile_id == "" {
				fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"获取新file id失败: ",err)
				return "", err
			}
		}
	}
	
	return Newfile_id, nil

}

func (d *AliyundriveShare2Pan115) GetmyLink(ctx context.Context, file_id string, file_name string) (string, string, int64, error) {
	existed_download_url, ok := d.DownloadUrl_dict[file_id]
	existed_file_hash, _ := d.FileHash_dict[file_id];
	existed_file_size, _ := d.FileSize_dict[file_id];
	if ok {
		fmt.Println(time.Now().Format("01-02-2006 15:04:05"),"下载链接已存在: ",file_name)
		return existed_download_url, existed_file_hash, existed_file_size, nil
	}

    count := 1
    for {
        res, err := d.requestOpen("/adrive/v1.0/openFile/getDownloadUrl", http.MethodPost, func(req *resty.Request) {
			req.SetBody(base.Json{
					"drive_id":   d.MyAliDriveId,
					"file_id":    file_id,
					"expire_sec": 14300,
			})
		})
		
        if err != nil {
            if count > 2 {
                  return "http://img.xiaoya.pro/abnormal.png", "", 0, err
            }
			fmt.Println("获取下载链接失败第",count,"次 ",file_name)
            count += 1
            time.Sleep(1 * 1000 * time.Millisecond)
        }

        if err == nil {
            d.DownloadUrl_dict[file_id] = utils.Json.Get(res, "url").ToString()
			d.FileHash_dict[file_id] = strings.ToUpper(utils.Json.Get(res, "content_hash").ToString())
			d.FileSize_dict[file_id] = utils.Json.Get(res, "size").ToInt64()
			fmt.Println("文件: ",file_name,"  新增下载直链: ", d.DownloadUrl_dict[file_id]," SHA1", d.FileHash_dict[file_id])
			fmt.Println(time.Now().Format("01-02-2006 15:04:05")," 已成功缓存了",len(d.DownloadUrl_dict),"个文件")
			return d.DownloadUrl_dict[file_id], d.FileHash_dict[file_id], d.FileSize_dict[file_id], nil
		}	
    }	
}

func (d *AliyundriveShare2Pan115) Remove(ctx context.Context, file_id string) error {
	_, err := d.requestOpen("/adrive/v1.0/openFile/delete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"file_id": file_id,
			"drive_id":  d.MyAliDriveId,
		})
	})
	return err
}

func (d *AliyundriveShare2Pan115) Purge_temp_folder(ctx context.Context) error {
	res, err := d.requestOpen("/adrive/v1.0/openFile/list", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"parent_file_id": d.TempTransferFolderID,
			"drive_id":  d.MyAliDriveId,
		})
	})
	
	if err != nil {	return err }

	delete_files := gjson.GetBytes(res, "items.#.file_id")
	if len(delete_files.Array()) > 0 {
		fmt.Println(delete_files.Array())
		for _,b := range delete_files.Array() {
			err := d.Remove(ctx, b.String()) 
			if err != nil {
				//fmt.Printf("\033[0;32m%s%s%s\033[0m\n",time.Now().Format("01-02-2006 15:04:05"),"转存文件删除失败：",err)
			}	
			if err == nil {
				fmt.Printf("\033[0;32m%s%s%s\033[0m\n",time.Now().Format("01-02-2006 15:04:05"),"转存文件成功删除：",b.String())
			}
		}
	}
	return nil
}

func (d *AliyundriveShare2Pan115) login() error {
	var err error

	cr := &driver115.Credential{}
	if d.Addition.Cookie == "" {
		return errors.New("missing cookie")
	}

	if err = cr.FromCookie(d.Addition.Cookie); err != nil {
		fmt.Println("通过cookie登陆失败：", d.Addition.Cookie)
		return errors.Wrap(err, "failed to login by cookies")
	}
	d.client.ImportCredential(cr)
	
	if userInfo, err := d.client.GetUser(); err == nil {
	    if userInfo.Vip == 0 {
	        fmt.Println("非115会员不能使用该功能！")
	        //重新生成一个没有登陆的客户端,免得其他代码到处修改
	        opts := []driver115.Option{
	            driver115.UA(UserAgent),
	            func(c *driver115.Pan115Client) {
	                c.Client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})
	                
	            },
	        }
	        d.client = driver115.New(opts...)
	    }
	}
	
	return d.client.LoginCheck()
}

func (d *AliyundriveShare2Pan115) preLogin() error {
	opts := []driver115.Option{
		driver115.UA(UserAgent),
		func(c *driver115.Pan115Client) {
			c.Client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})
		},
	}
	d.client = driver115.New(opts...)
	if(base.Pan115Cookie != ""){
		d.Addition.Cookie = base.Pan115Cookie
	}
	if strings.Contains(d.Addition.Cookie, "=") {
		return nil
	}

	s := &driver115.QRCodeSession{
		UID: d.Addition.Cookie,
	}
	cr, err := d.client.QRCodeLoginWithApp(s, driver115.LoginApp("linux"))
	if err != nil {
		fmt.Println("通过QR码登陆失败：", d.Addition.Cookie)
		return errors.Wrap(err, "failed to login by qrcode")
	}
	d.Addition.Cookie = fmt.Sprintf("UID=%s;CID=%s;SEID=%s", cr.UID, cr.CID, cr.SEID)
	base.Pan115Cookie = d.Addition.Cookie 
	fmt.Println("通过QR码获取到cookie：", d.Addition.Cookie)
	
	file, err := os.OpenFile("/data/ali2115.txt", os.O_RDWR, 0644)
	defer func() {
        if err == nil {
            file.Close()
        }
    }()
    
    if err != nil {
        fmt.Println("打开小雅115配置文件失败：", err)
        return nil
    }
    
	scanner := bufio.NewScanner(file)
	var newConfigContent string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "cookie=") {
			line = "cookie=\"" + d.Addition.Cookie + "\""
		}
		newConfigContent += line + "\n"
	}
	file.Seek(0, 0)
	fmt.Println("写入新配置：", newConfigContent)
	os.WriteFile("/data/ali2115.txt", []byte(newConfigContent), 0644)
    
	return nil
}

func UploadDigestRange(linkUrl string, rangeSpec string) (result string, err error) {
	var start, end int64
	if _, err = fmt.Sscanf(rangeSpec, "%d-%d", &start, &end); err != nil {
		return
	}
	return calculateSHA1Range(linkUrl, start, end)
}

func (d *AliyundriveShare2Pan115) rapidUpload(fileSize int64, fileName, dirID, preID, fileID string, linkUrl string) (*driver115.UploadInitResp, error) {
	var (
		ecdhCipher   *cipher.EcdhCipher
		encrypted    []byte
		decrypted    []byte
		encodedToken string
		err          error
		target       = "U_1_" + dirID
		bodyBytes    []byte
		result       = driver115.UploadInitResp{}
		fileSizeStr  = strconv.FormatInt(fileSize, 10)
	)
	if ecdhCipher, err = cipher.NewEcdhCipher(); err != nil {
		return nil, err
	}

	userID := strconv.FormatInt(d.client.UserID, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", userID)
	form.Set("filename", fileName)
	form.Set("filesize", fileSizeStr)
	form.Set("fileid", fileID)
	form.Set("target", target)
	form.Set("sig", d.client.GenerateSignature(fileID, target))

	signKey, signVal := "", ""
	for retry := true; retry; {
		//t := driver115.Now()
		t := driver115.NowMilli()

		if encodedToken, err = ecdhCipher.EncodeToken(t.ToInt64()); err != nil {
			return nil, err
		}

		params := map[string]string{
			"k_ec": encodedToken,
		}

		form.Set("t", t.String())
		//form.Set("token", d.client.GenerateToken(fileID, preID, t.String(), fileSizeStr, signKey, signVal))
		form.Set("token", d.Generate115Token(fileID, preID, t.String(), fileSizeStr, signKey, signVal))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		if encrypted, err = ecdhCipher.Encrypt([]byte(form.Encode())); err != nil {
			return nil, err
		}

		req := d.client.NewRequest().
			SetQueryParams(params).
			SetBody(encrypted).
			SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
			SetDoNotParseResponse(true)
		resp, err := req.Post(driver115.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		data := resp.RawBody()
		defer data.Close()
		if bodyBytes, err = io.ReadAll(data); err != nil {
			return nil, err
		}
		if decrypted, err = ecdhCipher.Decrypt(bodyBytes); err != nil {
			return nil, err
		}

		if err = driver115.CheckErr(json.Unmarshal(decrypted, &result), &result, resp); err != nil {
			return nil, err
		}
		if result.Status == 7 {
			// Update signKey & signVal
			signKey = result.SignKey
			signVal, err = UploadDigestRange(linkUrl, result.SignCheck)
			if err != nil {
				return nil, err
			}
		} else {
			retry = false
		}
		result.SHA1 = fileID
	}

	return &result, nil
}

func (d *AliyundriveShare2Pan115) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	var resp base.Json
	var uri string
	new_file_id, _ := d.Copy2Myali(ctx , d.MyAliDriveId, args.Obj.GetID(), args.Obj.GetName())
	data := base.Json{
		"drive_id": d.MyAliDriveId,
		"share_id": d.ShareId,
		"file_id":  new_file_id,
	}
	switch args.Method {
	case "video_preview":
		uri = "/adrive/v1.0/openFile/getVideoPreviewPlayInfo"
		data["category"] = "live_transcoding"
		data["url_expire_sec"] = 14400
	default:
		return nil, errs.NotSupport
	}
	_, err := d.requestOpen(uri, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetResult(&resp)
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

var _ driver.Driver = (*AliyundriveShare2Pan115)(nil)
