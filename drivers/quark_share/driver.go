package quarkShare

import (
	"context"
	//"crypto/md5"
	//"crypto/sha1"
	//"encoding/hex"
	//"io"
	"net/http"
	"time"

	"github.com/alist-org/alist/v3/drivers/base"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/go-resty/resty/v2"
	//log "github.com/sirupsen/logrus"
)

type QuarkShare struct {
	model.Storage
	Addition
	config driver.Config
	conf   Conf
	stoken string
	linkMap map[string]*model.Link
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
	d.linkMap = make(map[string]*model.Link)
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

func (d *QuarkShare) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if cacheLink, ok := d.linkMap[file.GetID()]; ok {
		return cacheLink, nil
	}
    fid := d.save(file)
	link, err := d.previewLink(file, fid)
	if err == nil {
		d.linkMap[file.GetID()] = link
	}
	d.delete(fid)
    return link, err
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

var _ driver.Driver = (*QuarkShare)(nil)
