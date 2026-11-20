package client

import (
	"database/sql"
	"encoding/json"
	"sort"
)

const sessionKey = "session"

// conversationColumns 只写一次，保证 SELECT 与 scanConversation 的顺序不会漂移。
const conversationColumns = `id, type, last_seq, read_seq, unread_count, version,
	peer_read_seq, peer_id, peer_json, last_msg_seq, last_msg_json, pinned, muted, joined_at`

// Session 返回当前登录态副本。
func (s *Store) Session() *Session {
	var raw string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, sessionKey).Scan(&raw); err != nil {
		return nil // 没有登录态或读失败：都当作未登录
	}
	var session Session
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return nil
	}
	return &session
}

// SetSession 持久化登录态（用户与服务器地址）。
//
// token 不落库：它走 tokenStore（系统凭据库 / 0600 兜底文件）。
// 库文件权限再严也不该存凭据明文——备份、同步、截图都可能把它带出去。
func (s *Store) SetSession(session *Session) {
	if session == nil {
		_, _ = s.db.Exec(`DELETE FROM settings WHERE key = ?`, sessionKey)
		return
	}
	persisted := *session
	persisted.Token = ""
	raw, err := json.Marshal(&persisted)
	if err != nil {
		return
	}
	_, _ = s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, sessionKey, string(raw))
}

// Clear 清空登录态与所有缓存（登出）。
func (s *Store) Clear() {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM settings`,
		`DELETE FROM conversations`,
		`DELETE FROM messages`,
		`DELETE FROM outbox`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

// Conversations 返回按最后消息时间倒序的会话副本。
func (s *Store) Conversations() []*Conversation {
	rows, err := s.db.Query(`SELECT ` + conversationColumns + ` FROM conversations`)
	if err != nil {
		return []*Conversation{}
	}
	defer rows.Close()

	out := make([]*Conversation, 0, 16)
	for rows.Next() {
		conv, err := scanConversation(rows)
		if err != nil {
			return []*Conversation{}
		}
		out = append(out, conv)
	}
	if err := rows.Err(); err != nil {
		return []*Conversation{}
	}
	// 排序放在 Go 侧：只有几十个会话，没必要为它多维护一列排序列。
	sort.Slice(out, func(i, j int) bool {
		ti, tj := "", ""
		if out[i].LastMessage != nil {
			ti = out[i].LastMessage.CreatedAt
		}
		if out[j].LastMessage != nil {
			tj = out[j].LastMessage.CreatedAt
		}
		if ti != tj {
			return ti > tj
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Conversation 返回单个会话副本。
func (s *Store) Conversation(id uint64) *Conversation {
	conv, err := scanConversation(s.db.QueryRow(
		`SELECT `+conversationColumns+` FROM conversations WHERE id = ?`, id))
	if err != nil {
		return nil
	}
	return conv
}

// UpsertConversations 合并服务端会话摘要。
//
// 水位（last_seq / read_seq / peer_read_seq）只增不减：服务端响应可能乱序到达，
// 回退水位会让客户端重新“发现”已读消息或产生假的漏消息。
func (s *Store) UpsertConversations(list []*Conversation) {
	if len(list) == 0 {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, conv := range list {
		if conv == nil {
			continue
		}
		if err := upsertConversation(tx, conv); err != nil {
			return
		}
	}
	_ = tx.Commit()
}

func upsertConversation(tx *sql.Tx, conv *Conversation) error {
	// 注意：nil 指针装进 any 之后不等于 nil，json.Marshal 会得到 "null"。
	// 这里显式判空，'' 才表示“本次没有带这个字段，保留旧值”。
	var peerJSON, lastMsgJSON string
	var err error
	if conv.Peer != nil {
		if peerJSON, err = marshalJSON(conv.Peer); err != nil {
			return err
		}
	}
	lastMsgSeq := int64(0)
	if conv.LastMessage != nil {
		if lastMsgJSON, err = marshalJSON(conv.LastMessage); err != nil {
			return err
		}
		lastMsgSeq = conv.LastMessage.Seq
	}

	_, err = tx.Exec(`
		INSERT INTO conversations (`+conversationColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			type          = excluded.type,
			last_seq      = max(conversations.last_seq, excluded.last_seq),
			read_seq      = max(conversations.read_seq, excluded.read_seq),
			unread_count  = excluded.unread_count,
			version       = excluded.version,
			peer_read_seq = max(conversations.peer_read_seq, excluded.peer_read_seq),
			peer_id       = excluded.peer_id,
			peer_json     = CASE WHEN excluded.peer_json = '' THEN conversations.peer_json ELSE excluded.peer_json END,
			last_msg_seq  = CASE WHEN excluded.last_msg_json = '' THEN conversations.last_msg_seq ELSE excluded.last_msg_seq END,
			last_msg_json = CASE WHEN excluded.last_msg_json = '' THEN conversations.last_msg_json ELSE excluded.last_msg_json END,
			pinned        = excluded.pinned,
			muted         = excluded.muted,
			joined_at     = excluded.joined_at`,
		conv.ID, conv.Type, conv.LastSeq, conv.ReadSeq, conv.Unread, conv.Version,
		conv.PeerReadSeq, conv.PeerID, peerJSON, lastMsgSeq, lastMsgJSON,
		boolInt(conv.Pinned), boolInt(conv.Muted), conv.JoinedAt,
	)
	return err
}

// Messages 返回会话消息副本（服务端消息按 seq 升序，后面接未确认的本地消息）。
func (s *Store) Messages(conversationID uint64) []*Message {
	rows, err := s.db.Query(`SELECT seq, msg_id, sender_id, client_msg_id, type, body,
		status, reply_to, version, created_at
		FROM messages WHERE conversation_id = ? ORDER BY seq ASC`, conversationID)
	if err != nil {
		return []*Message{}
	}

	out := make([]*Message, 0, 64)
	for rows.Next() {
		var (
			msg        = &Message{ConversationID: conversationID}
			msgType    int64
			msgVersion int64
			status     int64
		)
		if err := rows.Scan(&msg.Seq, &msg.MsgID, &msg.SenderID, &msg.ClientMsgID,
			&msgType, &msg.Body, &status, &msg.ReplyTo, &msgVersion, &msg.CreatedAt); err != nil {
			return []*Message{}
		}
		msg.Type = int32(msgType)
		msg.Status = int32(status)
		msg.Version = int32(msgVersion)
		out = append(out, msg)
	}
	// 必须先关掉 rows：连接池只有 1 条连接，下面查 outbox 会等这条连接。
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return []*Message{}
	}

	// 未确认的消息没有 seq，统一排在已确认消息之后（确认后会自动落到正确位置）。
	for _, item := range s.OutboxOf(conversationID) {
		out = append(out, item.Message())
	}
	return out
}

// AddMessage 去重后插入消息，并更新会话最后一条/本地最大 seq。
func (s *Store) AddMessage(msg *Message) {
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
	_ = tx.Commit()
}

// addMessage 插入一条消息并维护会话摘要；返回是否真的写入了新行。
//
// 幂等靠唯一索引，不靠先查后插：WS 重连和 HTTP 补洞可能同时送达同一条消息。
func addMessage(tx *sql.Tx, msg *Message) (bool, error) {
	res, err := tx.Exec(`
		INSERT OR IGNORE INTO messages
			(conversation_id, seq, msg_id, sender_id, client_msg_id, type, body, status, reply_to, version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ConversationID, msg.Seq, msg.MsgID, msg.SenderID, msg.ClientMsgID,
		msg.Type, msg.Body, msg.Status, msg.ReplyTo, msg.Version, msg.CreatedAt)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil // 同 msg_id 或同 seq 已存在
	}

	// 服务端回推自己发的消息时，按 client_msg_id 把队列项清掉：
	// HTTP 响应可能丢，WS 这条就是确认信号。
	if msg.ClientMsgID != "" {
		if _, err := tx.Exec(`DELETE FROM outbox WHERE conversation_id = ? AND client_msg_id = ?`,
			msg.ConversationID, msg.ClientMsgID); err != nil {
			return false, err
		}
	}

	lastMsgJSON, err := marshalJSON(msg)
	if err != nil {
		return false, err
	}
	// 会话可能在收到第一条消息前还不存在（WS 先于会话列表到达），一起 upsert。
	_, err = tx.Exec(`
		INSERT INTO conversations (id, last_seq, last_msg_seq, last_msg_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_seq      = max(conversations.last_seq, excluded.last_seq),
			last_msg_seq  = CASE WHEN conversations.last_msg_seq <= excluded.last_msg_seq
			                     THEN excluded.last_msg_seq ELSE conversations.last_msg_seq END,
			last_msg_json = CASE WHEN conversations.last_msg_seq <= excluded.last_msg_seq
			                     THEN excluded.last_msg_json ELSE conversations.last_msg_json END`,
		msg.ConversationID, msg.Seq, msg.Seq, lastMsgJSON)
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkUnreadZero 清空本地未读（前端已读到最新）。
func (s *Store) MarkUnreadZero(conversationID uint64) {
	_, _ = s.db.Exec(`UPDATE conversations SET unread_count = 0 WHERE id = ?`, conversationID)
}

// SetRead 更新已读位与未读（近似值，服务端会话列表可校准）。
//
// 已读只增不减：乱序上报（先 100 再 90）不能把水位退回去。
func (s *Store) SetRead(conversationID uint64, seq int64) {
	_, _ = s.db.Exec(`
		UPDATE conversations SET
			read_seq = ?,
			unread_count = CASE WHEN ? >= last_seq THEN 0 ELSE last_seq - ? END
		WHERE id = ? AND read_seq < ?`,
		seq, seq, seq, conversationID, seq)
}

// SetPeerRead 更新对端已读位。
func (s *Store) SetPeerRead(conversationID uint64, seq int64) {
	_, _ = s.db.Exec(`UPDATE conversations SET peer_read_seq = ?
		WHERE id = ? AND peer_read_seq < ?`, seq, conversationID, seq)
}

func (s *Store) IncrementUnread(conversationID uint64) {
	_, _ = s.db.Exec(`UPDATE conversations SET unread_count = unread_count + 1 WHERE id = ?`, conversationID)
}

// LastSeq 返回本地已连续收到（或拉取到）的最大 seq。
func (s *Store) LastSeq(conversationID uint64) int64 {
	var seq int64
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) FROM messages WHERE conversation_id = ?`,
		conversationID).Scan(&seq); err != nil {
		return 0
	}
	return seq
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConversation(row rowScanner) (*Conversation, error) {
	var (
		conv                  = &Conversation{}
		peerJSON, lastMsgJSON string
		pinned, muted         int64
		convType              int64
		lastMsgSeq            int64 // 只用于写入时判断“哪条是最新”，读出来不需要
	)
	if err := row.Scan(&conv.ID, &convType, &conv.LastSeq, &conv.ReadSeq, &conv.Unread,
		&conv.Version, &conv.PeerReadSeq, &conv.PeerID, &peerJSON, &lastMsgSeq, &lastMsgJSON,
		&pinned, &muted, &conv.JoinedAt); err != nil {
		return nil, err
	}
	conv.Type = int32(convType)
	conv.Pinned = pinned != 0
	conv.Muted = muted != 0
	if peerJSON != "" {
		var peer User
		if err := json.Unmarshal([]byte(peerJSON), &peer); err == nil {
			conv.Peer = &peer
		}
	}
	if lastMsgJSON != "" {
		var msg Message
		if err := json.Unmarshal([]byte(lastMsgJSON), &msg); err == nil {
			conv.LastMessage = &msg
		}
	}
	return conv, nil
}

func marshalJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
