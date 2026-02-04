package strm_list

import (
	"bufio"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

func (d *StrmList) importTxt() error {
	log.Infof("[StrmList] 开始解析文本: %s", d.TxtPath)
	file, err := os.Open(d.TxtPath)
	if err != nil {
		return err
	}
	defer file.Close()

	tx, _ := d.db.Begin()
	_, _ = tx.Exec("INSERT OR IGNORE INTO nodes (id, name, parent_id, is_dir) VALUES (0, '', -1, 1)")

	stmt, _ := tx.Prepare("INSERT INTO nodes (name, parent_id, is_dir, content) VALUES (?, ?, ?, ?)")
	defer stmt.Close()

	dirCache := map[string]int64{"": 0}
	
	// 使用标准库 bufio 替代 AList utils 中不确定的 Scanner 函数
	scanner := bufio.NewScanner(file)
	// 如果一行内容非常长（比如超长URL），可以增加缓冲区
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

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

		_, _ = stmt.Exec(pathParts[len(pathParts)-1], currParent, 0, content)
		count++
		if count%100000 == 0 {
			log.Infof("[StrmList] 已解析并存入数据库 %d 条...", count)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Errorf("[StrmList] 扫描文本失败: %v", err)
	}

	err = tx.Commit()
	log.Infof("[StrmList] 导入完成，总计有效记录: %d 条", count)
	return err
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