package strm_list

import (
	"database/sql"
	"os"
	"strings"

	"github.com/alist-org/alist/v3/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// importTxt 高效导入百万级数据
func (d *StrmList) importTxt() error {
	log.Infof("[StrmList] 开始解析文本并构建索引: %s", d.TxtPath)
	file, err := os.Open(d.TxtPath)
	if err != nil {
		return err
	}
	defer file.Close()

	tx, _ := d.db.Begin()
	// 插入根节点 (ID=0)
	_, _ = tx.Exec("INSERT OR IGNORE INTO nodes (id, name, parent_id, is_dir) VALUES (0, '', -1, 1)")

	stmt, _ := tx.Prepare("INSERT INTO nodes (name, parent_id, is_dir, content) VALUES (?, ?, ?, ?)")
	defer stmt.Close()

	dirCache := map[string]int64{"": 0}
	scanner := utils.GetScanner(file)
	count := 0

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "#", 2)
		if len(parts) < 2 {
			continue
		}

		fullPath := strings.Trim(parts[0], "/")
		content := parts[1]
		pathParts := strings.Split(fullPath, "/")

		// 分层处理路径中的目录部分
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

		// 插入文件节点
		_, _ = stmt.Exec(pathParts[len(pathParts)-1], currParent, 0, content)
		count++
		if count%100000 == 0 {
			log.Infof("[StrmList] 已导入 %d 条记录...", count)
		}
	}

	err = tx.Commit()
	log.Infof("[StrmList] 导入完成，总计 %d 条", count)
	return err
}

// findNodeByPath 核心查询逻辑：通过路径逐级在数据库查找
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