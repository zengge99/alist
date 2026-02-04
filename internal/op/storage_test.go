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
	log "github.com/sirupsen/logrus"
)

// 1. 定义配置项 (Addition)
type Addition struct {
	driver.RootPath
	TxtPath string `json:"txt_path" config:"strm.txt 路径" default:"/index/strm/strm.txt" help:"strm.txt 的绝对路径"`
	DbPath  string `json:"db_path" config:"数据库存放路径" default:"/opt/alist/strm/strm.db" help:"SQLite 数据库文件的绝对路径"`
}

// 2. 驱动配置元数据
var config = driver.Config{
	Name:        "StrmList",
	OnlyLocal:   true,
	LocalSort:   true,
	NoCache:     false,
	DefaultRoot: "/",
}

// 3. 驱动结构体
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

// 4. 生命周期：初始化 (Init)
func (d *StrmList) Init(ctx context.Context) error {
	// 关键检查：如果 DbPath 为空（说明用户还没在后台配置并保存存储），
	// 必须直接返回 nil，否则 sql.Open 或 Mkdir 会 Panic，导致 AList 首页报 Undefined。
	if d.DbPath == "" || d.TxtPath == "" {
		return nil
	}

	// 确保数据库存放目录存在
	dbDir := filepath.Dir(d.DbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return err
	}

	// 使用 AList 内置集成的 sqlite 驱动名
	var err error
	d.db, err = sql.Open("sqlite", d.DbPath+"?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)")
	if err != nil {
		return fmt.Errorf("strm_list: failed to open sqlite: %v", err)
	}

	// 初始化表结构和索引
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

	// 检查数据量，若为空则触发异步导入
	var count int
	_ = d.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count <= 1 {
		go d.importTxtTask()
	}

	return nil
}

// 释放资源
func (d *StrmList) Drop(ctx context.Context) error {
	if d.db != nil {
		err := d.db.Close()
		d.db = nil
		return err
	}
	return nil
}

// 5. 核心：获取目录列表 (List)
func (d *StrmList) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if d.db == nil {
		return nil, fmt.Errorf("strm_list: 数据库未就绪")
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

	var objs []model.Obj
	now := time.Now()
	for rows.Next() {
		var n string
		var isDir bool
		var cnt string
		_ = rows.Scan(&n, &isDir, &cnt)

		objs = append(objs, &model.Object{
			Name:     n,
			Size:     int64(len(cnt)),
			Modified: now,
			IsFolder: isDir,
		})
	}
	return objs, nil
}

// 6. 核心：获取单个文件/目录信息 (Get)
func (d *StrmList) Get(ctx context.Context, path string) (model.Obj, error) {
	if d.db == nil {
		return nil, fmt.Errorf("strm_list: 数据库未就绪")
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

// 7. 核心：返回文件内容 (Link)
// 对于 .strm 文件，它直接返回文件内记录的 URL
func (d *StrmList) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if d.db == nil {
		return nil, fmt.Errorf("strm_list: 数据库未就绪")
	}

	_, _, content, err := d.findNodeByPath(file.GetPath())
	if err != nil {
		return nil, err
	}

	// AList V3 标准做法：将字符串包装成 Reader 通过 MFile 返回
	return &model.Link{
		MFile: model.NewNopMFile(bytes.NewReader([]byte(content))),
		Header: http.Header{
			"Content-Type": []string{"text/plain; charset=utf-8"},
		},
	}, nil
}

// --- 内部辅助方法 ---

// 路径查找逻辑：逐级从 DB 查询 ID
func (d *StrmList) findNodeByPath(path string) (id int64, isDir bool, content string, err error) {
	path = strings.Trim(path, "/")
	if path == "" || path == "." {
		return 0, true, "", nil // 根目录
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

// 异步导入任务
func (d *StrmList) importTxtTask() {
	log.Infof("[StrmList] 任务启动: 准备从 %s 导入数据", d.TxtPath)
	file, err := os.Open(d.TxtPath)
	if err != nil {
		log.Errorf("[StrmList] 无法打开 TXT 文件: %v", err)
		return
	}
	defer file.Close()

	tx, _ := d.db.Begin()
	// 初始化根节点
	_, _ = tx.Exec("INSERT OR IGNORE INTO nodes (id, name, parent_id, is_dir) VALUES (0, '', -1, 1)")
	
	stmt, _ := tx.Prepare("INSERT INTO nodes (name, parent_id, is_dir, content) VALUES (?, ?, ?, ?)")
	defer stmt.Close()

	dirCache := map[string]int64{"": 0} // 路径缓存，避免重复创建目录
	scanner := bufio.NewScanner(file)
	// 设置缓冲区大小，应对超长行
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "#", 2)
		if len(parts) < 2 {
			continue
		}

		rawPath := strings.Trim(parts[0], "/")
		content := parts[1]
		pathParts := strings.Split(rawPath, "/")

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

		// 插入 strm 文件
		_, _ = stmt.Exec(pathParts[len(pathParts)-1], currParent, 0, content)
		count++
		if count%100000 == 0 {
			log.Infof("[StrmList] 导入中: 已处理 %d 条...", count)
		}
	}

	_ = tx.Commit()
	log.Infof("[StrmList] 导入完成，共计有效记录: %d 条", count)
}

// 8. 注册驱动 (使用标准 op.RegisterDriver)
func init() {
	op.RegisterDriver(func() driver.Driver {
		return &StrmList{}
	})
}

var _ driver.Driver = (*StrmList)(nil)