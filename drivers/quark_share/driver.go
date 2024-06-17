package quarkShare

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"time"
	"fmt"
	"math/rand"
	"strconv"
	"encoding/json"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type QuarkShare struct {
	model.Storage
	Addition
	config driver.Config
	conf   Conf
	stoken string
}

func (d *QuarkShare) Config() driver.Config {
	return d.config
}

func (d *QuarkShare) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *QuarkShare) Init(ctx context.Context) error {
	_, err := d.request("/config", http.MethodGet, nil, nil)
	d.getStoken()
	return err
}

func (d *QuarkShare) Drop(ctx context.Context) error {
	return nil
}

func (d *QuarkShare) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.GetFiles(dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *QuarkShare) save(file model.Obj) (string) {
    data := base.Json{
		"fid_list": file.GetID(),
		"fid_token_list": file.(*Object).FidToken,
		"to_pdir_fid": "0",
        "pwd_id": d.Addition.ShareId,
        "stoken": d.stoken,
        "pdir_fid": "0",
        "scene": "link",
	}
	fmt.Println("提交数据：", string(json.Marshal(data)))
	query := map[string]string{
	    "uc_param_str":     "",
        "__dt": strconv.FormatInt(int64(rand.Float64()*(5-1)+1) * 60 * 1000, 10),
        "__t":  strconv.FormatInt(time.Now().UnixNano()/1000000, 10),
	}
    rsp, _ := d.request("/share/sharepage/save", http.MethodPost, func(req *resty.Request) {
		req.SetQueryParams(query)
		req.SetBody(data)
	}, nil)
	taskId := utils.Json.Get(rsp, "data", "task_id").ToString()
	fmt.Println("转存任务原始响应：", string(rsp))
	
	retry := int64(0)
	query = map[string]string{
	   "uc_param_str":     "",
	   "task_id": taskId,
        "__dt": strconv.FormatInt(int64(rand.Float64()*(5-1)+1) * 60 * 1000, 10),
        "__t":  strconv.FormatInt(time.Now().UnixNano()/1000000, 10),
	}
	for {
	    query["retry_index"] = strconv.FormatInt(retry, 10)
	    fmt.Println("查询参数：", query)
	    rsp, _ := d.request("/task", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(query)
		}, nil)
		fmt.Println("转存进度原始响应：", string(rsp))
		fid := utils.Json.Get(rsp, "data", "save_as_top_fids")[0].ToString()
		if fid != "" {
		    return fid
		}
	    time.Sleep(2 * time.Second)
	    retry++
	    if retry > 3 {
	        break
	    }
	}
	return ""
}

func (d *QuarkShare) link(fid string) (*model.Link, error) {
    data := base.Json{
		"fids": fid,
	}
	var resp DownResp
	ua := d.conf.ua
	_, err := d.request("/file/download", http.MethodPost, func(req *resty.Request) {
		req.SetHeader("User-Agent", ua).
			SetBody(data)
	}, &resp)
	if err != nil {
		return nil, err
	}

	return &model.Link{
		URL: resp.Data[0].DownloadUrl,
		Header: http.Header{
			"Cookie":     []string{d.Cookie},
			"Referer":    []string{d.conf.referer},
			"User-Agent": []string{ua},
		},
		Concurrency: 2,
		PartSize:    10 * utils.MB,
	}, nil
}

func (d *QuarkShare) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
    d.save(file)
    
    
    return d.link(file.GetID())
}

func (d *QuarkShare) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	data := base.Json{
		"dir_init_lock": false,
		"dir_path":      "",
		"file_name":     dirName,
		"pdir_fid":      parentDir.GetID(),
	}
	_, err := d.request("/file", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	if err == nil {
		time.Sleep(time.Second)
	}
	return err
}

func (d *QuarkShare) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	data := base.Json{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{srcObj.GetID()},
		"to_pdir_fid":  dstDir.GetID(),
	}
	_, err := d.request("/file/move", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *QuarkShare) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	data := base.Json{
		"fid":       srcObj.GetID(),
		"file_name": newName,
	}
	_, err := d.request("/file/rename", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *QuarkShare) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errs.NotSupport
}

func (d *QuarkShare) Remove(ctx context.Context, obj model.Obj) error {
	data := base.Json{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{obj.GetID()},
	}
	_, err := d.request("/file/delete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *QuarkShare) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	tempFile, err := stream.CacheFullInTempFile()
	if err != nil {
		return err
	}
	defer func() {
		_ = tempFile.Close()
	}()
	m := md5.New()
	_, err = utils.CopyWithBuffer(m, tempFile)
	if err != nil {
		return err
	}
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	md5Str := hex.EncodeToString(m.Sum(nil))
	s := sha1.New()
	_, err = utils.CopyWithBuffer(s, tempFile)
	if err != nil {
		return err
	}
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	sha1Str := hex.EncodeToString(s.Sum(nil))
	// pre
	pre, err := d.upPre(stream, dstDir.GetID())
	if err != nil {
		return err
	}
	log.Debugln("hash: ", md5Str, sha1Str)
	// hash
	finish, err := d.upHash(md5Str, sha1Str, pre.Data.TaskId)
	if err != nil {
		return err
	}
	if finish {
		return nil
	}
	// part up
	partSize := pre.Metadata.PartSize
	var bytes []byte
	md5s := make([]string, 0)
	defaultBytes := make([]byte, partSize)
	total := stream.GetSize()
	left := total
	partNumber := 1
	for left > 0 {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		if left > int64(partSize) {
			bytes = defaultBytes
		} else {
			bytes = make([]byte, left)
		}
		_, err := io.ReadFull(tempFile, bytes)
		if err != nil {
			return err
		}
		left -= int64(len(bytes))
		log.Debugf("left: %d", left)
		m, err := d.upPart(ctx, pre, stream.GetMimetype(), partNumber, bytes)
		//m, err := driver.UpPart(pre, file.GetMIMEType(), partNumber, bytes, account, md5Str, sha1Str)
		if err != nil {
			return err
		}
		if m == "finish" {
			return nil
		}
		md5s = append(md5s, m)
		partNumber++
		up(100 * float64(total-left) / float64(total))
	}
	err = d.upCommit(pre, md5s)
	if err != nil {
		return err
	}
	return d.upFinish(pre)
}

var _ driver.Driver = (*QuarkShare)(nil)
