package client

import (
	"database/sql"
	"time"
)

// 发送队列（outbox）解决的是“网络断了但用户已经点了发送”：
// 消息先落库，再慢慢发；进程重启、断网、服务端 5xx 都不丢。
//
// 为什么未确认的消息不放 messages 表：messages 的主键是 (conversation_id, seq)，
// 而 seq 只有服务端能分配。硬塞 seq=0 会让 PORM 顺序、LastSeq、翻页全部失真。
// 所以 outbox 单独一张表，读的时候再合并成“发送中”的消息给 UI。

const messageTypeText = 1

// OutboxSending / OutboxFailed 是本地消息状态；服务端确认后状态由服务端给。
const (
	OutboxSending = "sending"
	OutboxFailed  = "failed"
)

// OutboxMessage 是已落库、尚未被服务端确认的消息。
type OutboxMessage struct {
	ClientMsgID    string `json:"client_msg_id"`
	ConversationID uint64 `json:"conversation_id"`
	SenderID       uint64 `json:"sender_id"`
	Body           string `json:"body"`
	CreatedAt      string `json:"created_at"`
	Status         string `json:"status"`
	Attempts       int32  `json:"attempts"`
	NextAttemptAt  int64  `json:"next_attempt_at"` // unix 毫秒
	LastError      string `json:"last_error"`
}

// Message 把队列表项转成 UI 用的消息模型：seq=0、没有 msg_id，靠 client_msg_id 识别。
func (o *OutboxMessage) Message() *Message {
	if o == nil {
		return nil
	}
	return &Message{
		ConversationID: o.ConversationID,
		ClientMsgID:    o.ClientMsgID,
		SenderID:       o.SenderID,
		Type:           messageTypeText,
		Body:           o.Body,
		CreatedAt:      o.CreatedAt,
		State:          o.Status,
		Error:          o.LastError,
	}
}

// EnqueueOutbox 把消息放进发送队列。
func (s *Store) EnqueueOutbox(item *OutboxMessage) error {
	if item == nil {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO outbox
			(client_msg_id, conversation_id, sender_id, body, created_at, status, attempts, next_attempt_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ClientMsgID, item.ConversationID, item.SenderID, item.Body, item.CreatedAt,
		OutboxSending, item.Attempts, item.NextAttemptAt, item.LastError)
	return err
}

// DueOutbox 返回到期可发送的队列项，按入队顺序。
//
// 排序 tiebreak 用 rowid 而不是 client_msg_id：同一毫秒内连点两条时，
// 用随机 UUID 排序会让它们随机换位，进而拿反 seq。
func (s *Store) DueOutbox(nowMS int64, limit int) []*OutboxMessage {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT client_msg_id, conversation_id, sender_id, body, created_at, status, attempts, next_attempt_at, last_error
		FROM outbox
		WHERE status = ? AND next_attempt_at <= ?
		ORDER BY created_at ASC, rowid ASC
		LIMIT ?`, OutboxSending, nowMS, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanOutbox(rows)
}

// OutboxOf 返回某会话的全部队列项（含发送失败的），用于合并进消息列表。
func (s *Store) OutboxOf(conversationID uint64) []*OutboxMessage {
	rows, err := s.db.Query(`
		SELECT client_msg_id, conversation_id, sender_id, body, created_at, status, attempts, next_attempt_at, last_error
		FROM outbox WHERE conversation_id = ?
		ORDER BY created_at ASC, rowid ASC`, conversationID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanOutbox(rows)
}

// OutboxNextAttempt 返回队列里最早的下次发送时间（只算可重试的项）。
// 没有队列项时 ok=false。
func (s *Store) OutboxNextAttempt() (int64, bool) {
	var next sql.NullInt64
	if err := s.db.QueryRow(
		`SELECT MIN(next_attempt_at) FROM outbox WHERE status = ?`, OutboxSending,
	).Scan(&next); err != nil || !next.Valid {
		return 0, false
	}
	return next.Int64, true
}

// RetryOutbox 记录一次可重试失败：累加次数、推迟下次尝试、保存最近错误。
func (s *Store) RetryOutbox(clientMsgID string, attempts int32, nextAttemptAt int64, lastError string) {
	_, _ = s.db.Exec(`
		UPDATE outbox SET attempts = ?, next_attempt_at = ?, last_error = ?
		WHERE client_msg_id = ?`, attempts, nextAttemptAt, lastError, clientMsgID)
}

// FailOutbox 标记不可重试的失败（4xx：参数/权限/不存在），保留给 UI 展示。
func (s *Store) FailOutbox(clientMsgID, lastError string) {
	_, _ = s.db.Exec(`
		UPDATE outbox SET status = ?, last_error = ? WHERE client_msg_id = ?`,
		OutboxFailed, lastError, clientMsgID)
}

// ResolveOutbox 用服务端确认的消息替换队列项：写入 messages 并删除队列行。
// 两步必须同事务，否则崩溃可能留下“既在队列又已入库”的重复消息。
func (s *Store) ResolveOutbox(clientMsgID string, msg *Message) {
	if msg == nil {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := addMessage(tx, msg); err != nil {
		return
	}
	if _, err := tx.Exec(`DELETE FROM outbox WHERE client_msg_id = ?`, clientMsgID); err != nil {
		return
	}
	_ = tx.Commit()
}

func scanOutbox(rows *sql.Rows) []*OutboxMessage {
	out := make([]*OutboxMessage, 0, 8)
	for rows.Next() {
		item := &OutboxMessage{}
		if err := rows.Scan(&item.ClientMsgID, &item.ConversationID, &item.SenderID, &item.Body,
			&item.CreatedAt, &item.Status, &item.Attempts, &item.NextAttemptAt, &item.LastError); err != nil {
			return nil
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

// outboxCreatedAt 统一本地消息时间格式，与前端 new Date() 兼容。
func outboxCreatedAt(t time.Time) string {
	return t.Truncate(time.Millisecond).UTC().Format(time.RFC3339Nano)
}
