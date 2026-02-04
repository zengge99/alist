package strm_list

import (
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/op"
)

type Addition struct {
	driver.RootPath
	TxtPath string `json:"txt_path" default:"/index/strm/strm.txt" help:"strm.txt 文件的绝对路径"`
	DbPath  string `json:"db_path" default:"/opt/alist/strm/strm.db" help:"SQLite 数据库存放的绝对路径"`
}

var config = driver.Config{
	Name:        "StrmList",
	OnlyLocal:   true,
	LocalSort:   true,
	NoCache:     false,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &StrmList{}
	})
}