package strm_list

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	log "github.com/sirupsen/logrus"
	_ "gorm.io/driver/sqlite"
)

// 1. 配置定义
type Addition struct {
	driver.RootPath
	TxtPath string `json:"txt_path" config:"strm.txt 路径" default:"/index/strm/strm.txt"`
	DbPath  string `json:"db_path" config:"数据库路径" default:"/opt/alist/strm/strm.db"`
}

var config = driver.Config{
	Name:        "StrmList",
	OnlyLocal:   true,
	LocalSort:   true,
	NoCache:     false,
	DefaultRoot: "/",
}

// 2. 驱动结构
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

// 3. 生命周期逻辑
func (d *StrmList) Init(ctx context.Context) error {
	// AList 在启动时会调用所有驱动的 Init，如果此时路径还没配置，必须直接返回
	if d.DbPath == "" || d.TxtPath == "" {
		return nil
	}

	// 确保 DB 文件夹存在
	dir := filepath.Dir(d.DbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var err error
	// 纯 Go sqlite 驱动名称通常是 "sqlite"
	d.db, err = sql.Open("sqlite", d.DbPath+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		log.Errorf("[StrmList] 数据库打开失败: %v", err)
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

	// 检查是否需要导入
	var count int
	_ = d.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count <= 1 { // 只有根节点或为空
		go d.importTxt() // 异步导入，防止 AList 启动超时
	}

	return nil
}

func (d *StrmList) Drop(ctx context.Context) error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// 4. 核心文件系统逻辑
func (d *StrmList) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if d.db == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
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
	if d.db == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
	_, isDir, content, err := d.findNodeByPath(path)
	if err != nil {
		return nil, err
	}

	return &model.Object{
		Name:     filepath.Base(path),
		Size:     int64(len(content)),
		IsFolder: isDir,
		Modified: time.Now(),
	}, nil
}

func (d *StrmList) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if d.db == nil {
		return nil, fmt.Errorf("数据库未就绪")
	}
	_, _, content, err := d.findNodeByPath(file.GetPath())
	if err != nil {
		return nil, err
	}
	
	return &model.Link{
		MFile: model.NewNopMFile(bytes.NewReader([]byte(content))),
		Header: http.Header{
			"Content-Type": []string{"text/plain; charset=utf-8"},
		},
	}, nil
}

// 5. 内部工具方法 (合并自之前的 util.go)
func (d *StrmList) importTxt() {
	log.Infof("[StrmList] 任务启动: 正在从 %s 导入数据", d.TxtPath)
	file, err := os.Open(d.TxtPath)
	if err != nil {
		log.Errorf("[StrmList] 无法读取 TXT: %v", err)
		return
	}
	defer file.Close()

	tx, _ := d.db.Begin()
	_, _ = tx.Exec("INSERT OR IGNORE INTO nodes (id, name, parent_id, is_dir) VALUES (0, '', -1, 1)")
	stmt, _ := tx.Prepare("INSERT INTO nodes (name, parent_id, is_dir, content) VALUES (?, ?, ?, ?)")

	dirCache := map[string]int64{"": 0}
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "#", 2)
		if len(parts) < 2 { continue }
		fullPath := strings.Trim(parts[0], "/")
		pathParts := strings.Split(fullPath, "/")

		var currParent int64 = 0
		currPathAcc := ""
		for _, part := range pathParts[:len(pathParts)-1] {
			if currPathAcc == "" { currPathAcc = part } else { currPathAcc += "/" + part }
			if id, ok := dirCache[currPathAcc]; ok {
				currParent = id
			} else {
				res, err := tx.Exec("INSERT INTO nodes (name, parent_id, is_dir) VALUES (?, ?, 1)", part, currParent)
				if err == nil {
					currParent, _ = res.LastInsertId()
					dirCache[currPathAcc] = currParent
				}
			}
		}
		_, _ = stmt.Exec(pathParts[len(pathParts)-1], currParent, 0, parts[1])
		count++
	}
	_ = tx.Commit()
	log.Infof("[StrmList] 导入成功: %d 条记录", count)
}

func (d *StrmList) findNodeByPath(path string) (id int64, isDir bool, content string, err error) {
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return 0, true, "", nil
	}
	parts := strings.Split(path, "/")
	var currentParent int64 = 0
	for _, part := range parts {
		err = d.db.QueryRow("SELECT id, is_dir, content FROM nodes WHERE parent_id = ? AND name = ?", currentParent, part).
			Scan(&id, &isDir, &content)
		if err != nil { return 0, false, "", err }
		currentParent = id
	}
	return id, isDir, content, nil
}

// 6. 注册驱动 (使用 op.RegisterDriver，这是 AList V3 最标准的做法)
func init() {
	op.RegisterDriver(func() driver.Driver {
		return &StrmList{}
	})
}