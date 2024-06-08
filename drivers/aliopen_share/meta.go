package aliyundrive_share2open

import (
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/op"
)

type Addition struct {
	//RefreshToken_open   string `json:"refresh_token_open" required:"true"`
	RefreshToken string `json:"RefreshToken" required:"true"`
	RefreshTokenOpen string `json:"RefreshTokenOpen" required:"true"`
	TempTransferFolderID string `json:"TempTransferFolderID" default:"root"`
	ShareId      string `json:"share_id" required:"true"`
	SharePwd     string `json:"share_pwd"`
	driver.RootID
	OrderBy        string `json:"order_by" type:"select" options:"name,size,updated_at,created_at"`
	OrderDirection string `json:"order_direction" type:"select" options:"ASC,DESC"`
	OauthTokenURL  string `json:"oauth_token_url" default:"https://api.nn.ci/alist/ali_open/token"`
	ClientID       string `json:"client_id" required:"false" help:"Keep it empty if you don't have one"`
	ClientSecret   string `json:"client_secret" required:"false" help:"Keep it empty if you don't have one"`
	
	//115参数
	Cookie       string  `json:"cookie" type:"text" help:"115 cookie required"`
	PageSize     int64   `json:"page_size" type:"number" default:"56" help:"list api per page size of 115 driver"`
	LimitRate    float64 `json:"limit_rate" type:"number" default:"2" help:"limit all api request rate (1r/[limit_rate]s)"`
}

var config = driver.Config{
	Name:        "AliyundriveShare2Open",
	LocalSort:   false,
	OnlyProxy:   false,
	NoUpload:    true,
	OnlyLocal:   false,
	NoCache:     false,
	NeedMs:      false,
	DefaultRoot: "root",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &AliyundriveShare2Open{
			base: "https://openapi.alipan.com",
		}
	})
}
