package strm_list

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/pkg/utils"
	_ "modernc.org/sqlite" // AList 默认使用的纯 Go SQLite 驱动
)

type StrmList struct {
	driver.DefaultDriver
	Config
	db *sql.DB
}

func (d *StrmList) GetConfig() driver.DriverConfig {
	return driver.DriverConfig{
		Name:          "StrmList",
		Local:         true,
		OnlyRaw:       true,
		NoCache:       false,
		NoSort:        false,
		NoUpdate:      true, // 只读
		NoUpload:      true, // 只读
		NoSearch:      false,
	}
}

func (d *StrmList) Init(ctx context.Context) error {
	// 确保数据库目录存在
	if err := os.MkdirAll(d.DbDir, 0755); err != nil {
		return err
	}

	dbPath := filepath.Join(d.DbDir, "strm.db")
	// 针对百万数据优化连接参数
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)&_pragma=cache_size(-200000)", dbPath)
	
	var err error
	d.db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}

	// 1. 初始化表结构
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

	// 2. 检查是否为空，为空则导入
	var count int
	_ = d.db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	if count == 0 {
		return d.importData()
	}
	return nil
}

func (d *StrmList) importData() error {
	f, err := os.Open(d.TxtPath)
	if err != nil {
		return fmt.Errorf("找不到 txt 文件: %v", err)
	}
	defer f.Close()

	tx, _ := d.db.Begin()
	// 插入根节点
	_, _ = tx.Exec("INSERT INTO nodes (id, name, parent_id, is_dir) VALUES (0, '', -1, 1)")
	
	stmt, _ := tx.Prepare("INSERT INTO nodes (name, parent_id, is_dir, content) VALUES (?, ?, ?, ?)")
	defer stmt.Close()

	dirCache := map[string]int64{"": 0}
	scanner := utils.GetScanner(f)
	
	fmt.Println("StrmList: 开始导入百万级数据...")
	lineCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "#", 2)
		if len(parts) < 2 { continue }

		fullPath := strings.Trim(parts[0], "/")
		content := parts[1]
		pathParts := strings.Split(fullPath, "/")

		var currParent int64 = 0
		currPathAcc := ""

		// 分层处理目录
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

		// 插入 strm 文件
		_, _ = stmt.Exec(pathParts[len(pathParts)-1], currParent, 0, content)
		lineCount++
		if lineCount%100000 == 0 {
			fmt.Printf("StrmList: 已处理 %d 条...\n", lineCount)
		}
	}
	return tx.Commit()
}

func (d *StrmList) findNode(path string) (id int64, isDir bool, content string, name string, err error) {
	path = strings.Trim(strings.TrimPrefix(path, d.RootPath), "/")
	if path == "" || path == "." {
		return 0, true, "", "", nil
	}

	parts := strings.Split(path, "/")
	var currentParent int64 = 0
	for _, part := range parts {
		err = d.db.QueryRow("SELECT id, is_dir, content, name FROM nodes WHERE parent_id = ? AND name = ?", currentParent, part).
			Scan(&id, &isDir, &content, &name)
		if err != nil {
			return 0, false, "", "", os.ErrNotExist
		}
		currentParent = id
	}
	return
}

func (d *StrmList) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	id, _, _, _, err := d.findNode(dir.GetPath())
	if err != nil { return nil, err }

	rows, err := d.db.Query("SELECT name, is_dir, content FROM nodes WHERE parent_id = ?", id)
	if err != nil { return nil, err }
	defer rows.Close()

	var res []model.Obj
	for rows.Next() {
		var n string
		var isDir bool
		var cnt string
		_ = rows.Scan(&n, &isDir, &cnt)
		res = append(res, &model.Object{
			Name:     n,
			Size:     int64(len(cnt)),
			IsFolder: isDir,
			Modified: dir.GetModified(),
		})
	}
	return res, nil
}

func (d *StrmList) Get(ctx context.Context, path string) (model.Obj, error) {
	_, isDir, content, name, err := d.findNode(path)
	if err != nil { return nil, err }
	return &model.Object{
		Name:     name,
		Size:     int64(len(content)),
		IsFolder: isDir,
	}, nil
}

func (d *StrmList) Open(ctx context.Context, path string, args model.OpenArgs) (model.FileIDReader, error) {
	_, _, content, _, err := d.findNode(path)
	if err != nil { return nil, err }
	// 返回 strm 内容（即 URL 字符串）
	return model.NewFileIDReader(io.NopCloser(strings.NewReader(content))), nil
}

func init() {
	driver.RegisterDriver(func() driver.Driver {
		return &StrmList{}
	})
}