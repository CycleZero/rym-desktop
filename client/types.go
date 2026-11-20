package client

import "encoding/json"

// User 是服务端公开用户资料。
type User struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Status   int32  `json:"status"`
}

// Session 是本地持久化的登录态（Token 不返回给前端绑定）。
type Session struct {
	User      User   `json:"user"`
	Token     string `json:"token,omitempty"`
	ServerURL string `json:"server_url"`
	WSPort    int    `json:"ws_port"`
}

// SessionInfo 是暴露给前端的登录信息（不含 token）。
type SessionInfo struct {
	User      User   `json:"user"`
	ServerURL string `json:"server_url"`
	WSPort    int    `json:"ws_port"`
}

// Message 是客户端消息模型。
type Message struct {
	MsgID          string `json:"msg_id"`
	ConversationID uint64 `json:"conversation_id"`
	Seq            int64  `json:"seq"`
	SenderID       uint64 `json:"sender_id"`
	ClientMsgID    string `json:"client_msg_id"`
	Type           int32  `json:"type"`
	Body           string `json:"body"`
	Status         int32  `json:"status"`
	ReplyTo        string `json:"reply_to"`
	Version        int32  `json:"version"`
	CreatedAt      string `json:"created_at"`
	// State / Error 只对本地未确认消息有值（sending / failed）；服务端消息为空。
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// Conversation 是会话摘要。
type Conversation struct {
	ID          uint64   `json:"id"`
	Type        int32    `json:"type"`
	LastSeq     int64    `json:"last_seq"`
	ReadSeq     int64    `json:"read_seq"`
	Unread      int64    `json:"unread_count"`
	Version     int64    `json:"version"`
	PeerReadSeq int64    `json:"peer_read_seq"`
	PeerID      uint64   `json:"peer_id"`
	Peer        *User    `json:"peer,omitempty"`
	LastMessage *Message `json:"last_message,omitempty"`
	Pinned      bool     `json:"pinned"`
	Muted       bool     `json:"muted"`
	JoinedAt    string   `json:"joined_at"`
}

// Event 是 WS 推送事件。
type Event struct {
	V    int             `json:"v"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// MessageCreatedData / ReadUpdatedData 是事件 payload。
type MessageCreatedData struct {
	ConversationID uint64  `json:"conversation_id"`
	Message        Message `json:"message"`
}

// MessageSentData 是本地发送结果的推送（message.sent / message.failed）。
type MessageSentData struct {
	ConversationID uint64   `json:"conversation_id"`
	ClientMsgID    string   `json:"client_msg_id"`
	Message        *Message `json:"message,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type ReadUpdatedData struct {
	ConversationID uint64 `json:"conversation_id"`
	UserID         uint64 `json:"user_id"`
	ReadSeq        int64  `json:"read_seq"`
}

// ConnectionState 是 WS 连接状态（推给前端）。
type ConnectionState struct {
	Connected bool   `json:"connected"`
	Reason    string `json:"reason,omitempty"`
}

// StoreData 是旧版本地 JSON 缓存的结构，只用于首次导入 SQLite（见 legacy.go）。
// 新代码不要再往里加字段。
type StoreData struct {
	Session       *Session                 `json:"session,omitempty"`
	Conversations map[uint64]*Conversation `json:"conversations,omitempty"`
	Messages      map[uint64][]*Message    `json:"messages,omitempty"`
}
