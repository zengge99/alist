package strm_list

import (
	"github.com/alist-org/alist/v3/internal/driver"
)

type Config struct {
	driver.DefaultConfig
	// 设置默认值
	TxtPath string `json:"txt_path" config:"strm.txt 路径" default:"/index/strm/strm.txt"`
	DbDir   string `json:"db_dir" config:"数据库存放目录" default:"/opt/alist/strm/"`
}

func (c *Config) GetConfig() driver.DefaultConfig {
	return c.DefaultConfig
}