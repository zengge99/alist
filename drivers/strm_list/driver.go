package strm_list

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	// 直接使用 AList 已经包含的驱动
	_ "github.com/glebarez/go-sqlite"
	log "github.com/sirupsen/logrus"
)

// 1. 配置定义：确保 json 和 config 标签对齐
type Addition struct {
	driver.RootPath
	TxtPath string `json:"txt_path" config:"strm.txt 路径" default:"/index/strm/strm.txt"`
	DbPath  string `json:"db_path" config:"数据库存放路径" default:"/opt/alist/strm/strm.db"`
}

var config = driver.Config{
	Name:        "StrmList",
	OnlyLocal:   true,
	LocalSort:   true,
	NoCache:     false,
	DefaultRoot: "/",
}

// 2. 驱动结构体
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

// 3. 生命周期：Init
func (d *StrmList) Init(ctx context.Context) error {
	// 安全校验：如果用户还没配置存储，直接返回，不触发数据库操作
	if d.DbPath == "" || d.TxtPath == "" {
		return nil
	}

	// 确保目录存在
	dbDir := filepath.Dir(d.DbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return err
	}

	// 打开数据库
	// 使用 "sqlite" 驱动名，对齐 AList 内部使用的 glebarez 驱动
	var err error
	d.db, err = sql.Open("sqlite", d.DbPath+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		return fmt.Errorf("failed to open sqlite: %v", err)
	}

	// 初始化表
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

	// 检查是否需要导入数据
	var count int
	_ = d.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count <= 1 {
		go d.importTxt() // 异步执行导入，避免 AList 首页请求超时
	}

	return nil
}

func (d *StrmList) Drop(ctx context.Context) error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// 4. 目录列表处理
func (d *StrmList) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if d.db == nil {
		return nil, fmt.Errorf("database not initialized")
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
	now := time.Now()
	for rows.Next() {
		var name string
		var isDir bool
		var content string
		_ = rows.Scan(&name, &isDir, &content)

		files = append(files, &model.Object{
			Name:     name,
			Size:     int64(len(content)),
			Modified: now,
			IsFolder: isDir,
		})
	}
	return files, nil
}

func (d *StrmList) Get(ctx context.Context, path string) (model.Obj, error) {
	if d.db == nil {
		return nil, fmt.Errorf("database not initialized")
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

// 5. 文件内容读取 (WebDAV 访问 strm 内容的关键)
func (d *StrmList) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if d.db == nil {
		return nil, fmt.Errorf("database not initialized")
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

// 6. 内部数据导入逻辑
func (d *StrmList) importTxt() {
	log.Infof("[StrmList] 开始导入数据: %s", d.TxtPath)
	file, err := os.Open(d.TxtPath)
	if err != nil {
		log.Errorf("[StrmList] 无法打开文件: %v", err)
		return
	}
	defer file.Close()

	tx, _ := d.db.Begin()
	// 根节点处理
	_, _ = tx.Exec("INSERT OR IGNORE INTO nodes (id, name, parent_id, is_dir) VALUES (0, '', -1, 1)")
	stmt, _ := tx.Prepare("INSERT INTO nodes (name, parent_id, is_dir, content) VALUES (?, ?, ?, ?)")
	defer stmt.Close()

	dirCache := map[string]int64{"": 0}
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 允许单行 10MB，防止特长 URL

	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "#", 2)
		if len(parts) < 2 {
			continue
		}

		pathStr := strings.Trim(parts[0], "/")
		content := parts[1]
		pathParts := strings.Split(pathStr, "/")

		// 处理目录层级
		var currParent int64 = 0
		currPathAcc := ""
		for _, part := range pathParts[:len(pathParts)-1] {
			if currPathAcc == "" {
				currPathAcc = part
			} else {
				currPathAcc += "/" + part
			}

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

		// 插入文件
		_, _ = stmt.Exec(pathParts[len(pathParts)-1], currParent, 0, content)
		count++
		if count%100000 == 0 {
			log.Infof("[StrmList] 已解析并存储 %d 条记录...", count)
		}
	}

	_ = tx.Commit()
	log.Infof("[StrmList] 数据导入完成，总计: %d 条", count)
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
		if err != nil {
			return 0, false, "", err
		}
		currentParent = id
	}
	return id, isDir, content, nil
}

// 7. 注册驱动
func init() {
	op.RegisterDriver(func() driver.Driver {
		return &StrmList{}
	})
}