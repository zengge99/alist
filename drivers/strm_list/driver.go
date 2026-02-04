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
	// 使用 AList 默认带有的纯 Go SQLite 驱动包
	_ "modernc.org/sqlite"
	log "github.com/sirupsen/logrus"
)

// 1. 配置定义
type Addition struct {
	driver.RootPath
	// 使用 help 标签提供 UI 描述，default 提供默认值
	TxtPath string `json:"txt_path" help:"strm.txt 文件的绝对路径" default:"/index/strm/strm.txt"`
	DbPath  string `json:"db_path" help:"SQLite 数据库存放的绝对路径" default:"/opt/alist/strm/strm.db"`
}

var config = driver.Config{
	Name:        "StrmList",
	OnlyLocal:   true,
	LocalSort:   true,
	NoCache:     false,
	DefaultRoot: "/",
}

// 2. 驱动结构体实现
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

// 3. 驱动初始化
func (d *StrmList) Init(ctx context.Context) error {
	// 防御性检查：如果存储还没配置（DbPath为空），直接返回，不触发数据库逻辑
	// 这是解决首页“Undefined”错误的关键，防止 API 反射阶段 Panic
	if d.DbPath == "" {
		return nil
	}

	dbDir := filepath.Dir(d.DbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return err
	}

	var err error
	// modernc.org/sqlite 注册的驱动名称通常是 "sqlite"
	d.db, err = sql.Open("sqlite", d.DbPath+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		log.Errorf("[StrmList] 数据库连接失败: %v", err)
		return err
	}

	// 快速创建表和索引
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

	// 异步导入数据，不阻塞 AList 启动
	var count int
	_ = d.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count <= 1 {
		go d.importTxt()
	}

	return nil
}

func (d *StrmList) Drop(ctx context.Context) error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// 4. 文件列表处理
func (d *StrmList) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if d.db == nil {
		return nil, fmt.Errorf("数据库未就绪，请检查配置")
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

// 5. 链接获取逻辑 (WebDAV 获取 strm 内容)
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

// 6. 辅助方法
func (d *StrmList) importTxt() {
	if d.TxtPath == "" {
		return
	}
	log.Infof("[StrmList] 准备导入: %s", d.TxtPath)
	file, err := os.Open(d.TxtPath)
	if err != nil {
		log.Errorf("[StrmList] 无法打开 TXT: %v", err)
		return
	}
	defer file.Close()

	tx, _ := d.db.Begin()
	_, _ = tx.Exec("INSERT OR IGNORE INTO nodes (id, name, parent_id, is_dir) VALUES (0, '', -1, 1)")
	stmt, _ := tx.Prepare("INSERT INTO nodes (name, parent_id, is_dir, content) VALUES (?, ?, ?, ?)")
	defer stmt.Close()

	dirCache := map[string]int64{"": 0}
	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "#", 2)
		if len(parts) < 2 { continue }

		pathStr := strings.Trim(parts[0], "/")
		content := parts[1]
		pathParts := strings.Split(pathStr, "/")

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
		_, _ = stmt.Exec(pathParts[len(pathParts)-1], currParent, 0, content)
		count++
		if count%100000 == 0 {
			log.Infof("[StrmList] 导入进度: %d 条...", count)
		}
	}
	_ = tx.Commit()
	log.Infof("[StrmList] 百万级数据导入完成，共 %d 条", count)
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

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &StrmList{}
	})
}