package strm_list

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/model"
	_ "gorm.io/driver/sqlite"
)

type StrmList struct {
	model.Storage
	Addition
	db *sql.DB
}

func (d *StrmList) Config() driver.Config {
	return config
}

func (d *StrmList) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *StrmList) Init(ctx context.Context) error {
	// 确保数据库目录存在
	dir := strings.ReplaceAll(d.DbPath, "strm.db", "")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var err error
	d.db, err = sql.Open("sqlite", d.DbPath+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			id INTEGER PRIMARY KEY,
			name TEXT,
			parent_id INTEGER,
			is_dir BOOLEAN,
			content TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_parent_name ON nodes(parent_id, name);
	`)
	if err != nil {
		return err
	}

	var count int
	_ = d.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count == 0 {
		return d.importTxt()
	}

	return nil
}

func (d *StrmList) Drop(ctx context.Context) error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

func (d *StrmList) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	nodeID, _, _, err := d.findNodeByPath(dir.GetPath())
	if err != nil {
		return nil, err
	}

	rows, err := d.db.Query("SELECT name, is_dir, content FROM nodes WHERE parent_id = ?", nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.Obj
	for rows.Next() {
		var name string
		var isDir bool
		var content string
		_ = rows.Scan(&name, &isDir, &content)

		files = append(files, &model.Object{
			Name:     name,
			Size:     int64(len(content)),
			Modified: time.Now(),
			IsFolder: isDir,
		})
	}
	return files, nil
}

func (d *StrmList) Get(ctx context.Context, path string) (model.Obj, error) {
	_, isDir, content, err := d.findNodeByPath(path)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	name := parts[len(parts)-1]
	if path == "/" || path == "" {
		name = "root"
	}

	return &model.Object{
		Name:     name,
		Size:     int64(len(content)),
		IsFolder: isDir,
		Modified: time.Now(),
	}, nil
}

func (d *StrmList) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	_, _, content, err := d.findNodeByPath(file.GetPath())
	if err != nil {
		return nil, err
	}
	
	// AList V3 的 model.Link 不支持 String 字段，必须通过 MFile 返回数据流
	return &model.Link{
		MFile: model.NewNopMFile(bytes.NewReader([]byte(content))),
		Header: http.Header{
			"Content-Type": []string{"text/plain; charset=utf-8"},
		},
	}, nil
}

var _ driver.Driver = (*StrmList)(nil)