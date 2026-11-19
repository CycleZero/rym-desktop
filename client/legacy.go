package client

import (
	"encoding/json"
	"os"
)

// legacyStoreFile 是 MVP 阶段单个 JSON 缓存的文件名（与数据库同目录）。
//
// 迁移策略：新库为空且旧文件存在时一次性导入，成功后把旧文件改名成
// store.json.imported（留证，不删除）。旧文件损坏或导入失败就跳过——
// 缓存可以从服务端重建，不能为了迁移让应用起不来。
const legacyStoreFile = "store.json"

func (s *Store) importLegacy(path string) error {
	var existing int
	if err := s.db.QueryRow(
		`SELECT (SELECT COUNT(*) FROM settings) + (SELECT COUNT(*) FROM conversations)`,
	).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil // 库已初始化；旧文件是上次没清掉的残留，不重复导入
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // 没有旧文件：正常路径
	}
	var data StoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil // 旧文件损坏：放弃导入，不影响启动
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// 先灌消息（走与线上相同的写入路径，顺带把会话行建出来），
	// 再用会话摘要覆盖，保证 last_seq 不会低于实际最大 seq。
	for _, list := range data.Messages {
		for _, msg := range list {
			if _, err := addMessage(tx, msg); err != nil {
				return err
			}
		}
	}
	for _, conv := range data.Conversations {
		if err := upsertConversation(tx, conv); err != nil {
			return err
		}
	}
	if data.Session != nil {
		sessionJSON, err := json.Marshal(data.Session)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			sessionKey, string(sessionJSON)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	_ = os.Rename(path, path+".imported")
	return nil
}
