package client

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite" // 纯 Go 驱动：不需要 cgo，交叉编译和 Windows 打包都简单
)

// schemaVersion 是本地库的表结构版本，用 SQLite 的 PRAGMA user_version 记录。
// 升级表结构时：加一个 if 分支写增量 SQL，并把这个常量 +1；不要改已发布的建表语句。
//
//	v1 基线：settings / conversations / messages
//	v2 发送队列：outbox
const schemaVersion = 2

// Store 是本地会话/消息缓存（SQLite）。
//
// 它是**缓存**而不是事实来源：写失败不阻断 UI，数据可以从服务端按游标重新拉回来。
// 唯一的例外是登录态（settings.session），写失败只意味着下次要重新登录。
//
// 并发模型：SQLite 只有单写者，这里直接 SetMaxOpenConns(1) 让所有读写串行化。
// 桌面端的量级（每秒几次操作）下这是最简单且不会出现 "database is locked" 的做法；
// 代价是慢查询会挡住其它访问——真到了那一步再拆读写连接池，而不是现在做。
type Store struct {
	db *sql.DB
}

// NewStore 打开（或创建）本地库。
//
// path 是数据库文件路径；同目录下的旧版 store.json 会被一次性导入（见 importLegacy）。
func NewStore(path string) (*Store, error) {
	// 目录 0700：库里有 token，不能让同机其它用户读到。
	if err := EnsureDir(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	if err := s.init(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init(path string) error {
	// sql.Open 是惰性的，先建一次连接（顺带创建库文件）再改权限。
	if err := s.db.Ping(); err != nil {
		return err
	}
	// SQLite 按 umask 建库文件（通常 0644）。库里有 token，这里显式收紧到 0600；
	// -wal/-shm 由 SQLite 按主库文件的权限创建，所以必须先 chmod 再写。
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}

	// WAL：读写不互相阻塞，崩溃恢复也更稳。
	// synchronous=NORMAL 在 WAL 下只保证进程崩溃不丢数据，掉电可能丢最后几个事务——
	// 对缓存可接受：消息能按 after_seq 补洞，会话摘要能从服务端重拉。
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := s.db.Exec(pragma); err != nil {
			return fmt.Errorf("%s: %w", pragma, err)
		}
	}

	if err := s.migrate(); err != nil {
		return err
	}
	return s.importLegacy(filepath.Join(filepath.Dir(path), legacyStoreFile))
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= schemaVersion {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// v1 基线表。
	if version < 1 {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS settings (
				key   TEXT PRIMARY KEY,
				value TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS conversations (
				id              INTEGER PRIMARY KEY,
				type            INTEGER NOT NULL DEFAULT 0,
				last_seq        INTEGER NOT NULL DEFAULT 0,
				read_seq        INTEGER NOT NULL DEFAULT 0,
				unread_count    INTEGER NOT NULL DEFAULT 0,
				version         INTEGER NOT NULL DEFAULT 0,
				peer_read_seq   INTEGER NOT NULL DEFAULT 0,
				peer_id         INTEGER NOT NULL DEFAULT 0,
				peer_json       TEXT    NOT NULL DEFAULT '',
				last_msg_seq    INTEGER NOT NULL DEFAULT 0,
				last_msg_json   TEXT    NOT NULL DEFAULT '',
				pinned          INTEGER NOT NULL DEFAULT 0,
				muted           INTEGER NOT NULL DEFAULT 0,
				joined_at       TEXT    NOT NULL DEFAULT ''
			)`,
			// 主键 (conversation_id, seq) 是顺序与唯一性的闸门；
			// (conversation_id, msg_id) 让重发的消息幂等。
			`CREATE TABLE IF NOT EXISTS messages (
				conversation_id INTEGER NOT NULL,
				seq             INTEGER NOT NULL,
				msg_id          TEXT    NOT NULL,
				sender_id       INTEGER NOT NULL,
				client_msg_id   TEXT    NOT NULL DEFAULT '',
				type            INTEGER NOT NULL DEFAULT 0,
				body            TEXT    NOT NULL DEFAULT '',
				status          INTEGER NOT NULL DEFAULT 0,
				reply_to        TEXT    NOT NULL DEFAULT '',
				version         INTEGER NOT NULL DEFAULT 0,
				created_at      TEXT    NOT NULL DEFAULT '',
				PRIMARY KEY (conversation_id, seq)
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS uk_messages_msg_id
				ON messages (conversation_id, msg_id)`,
			`CREATE INDEX IF NOT EXISTS idx_messages_client_msg_id
				ON messages (conversation_id, sender_id, client_msg_id)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
	}

	// v2 发送队列：没被服务端确认的消息不进 messages（没有 seq），单独排队。
	if version < 2 {
		for _, stmt := range []string{
			`CREATE TABLE IF NOT EXISTS outbox (
				client_msg_id   TEXT PRIMARY KEY,
				conversation_id INTEGER NOT NULL,
				sender_id       INTEGER NOT NULL DEFAULT 0,
				body            TEXT    NOT NULL,
				created_at      TEXT    NOT NULL,
				status          TEXT    NOT NULL DEFAULT 'sending',
				attempts        INTEGER NOT NULL DEFAULT 0,
				next_attempt_at INTEGER NOT NULL DEFAULT 0,
				last_error      TEXT    NOT NULL DEFAULT ''
			)`,
			`CREATE INDEX IF NOT EXISTS idx_outbox_due
				ON outbox (status, next_attempt_at, created_at)`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return err
			}
		}
	}

	// PRAGMA 不支持参数绑定，版本号是编译期常量，直接拼。
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close 关闭数据库（应用退出时用；之后 Store 不可再用）。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
