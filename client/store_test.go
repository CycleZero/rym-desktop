package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rym.db")

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.SetSession(&Session{User: User{ID: 1, Username: "alice"}, Token: "t", ServerURL: "http://x", WSPort: 8001})
	store.AddMessage(&Message{MsgID: "m1", ConversationID: 7, Seq: 1, SenderID: 2, Body: `{"text":"hi"}`})
	store.AddMessage(&Message{MsgID: "m2", ConversationID: 7, Seq: 2, SenderID: 1, Body: `{"text":"yo"}`})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err = NewStore(path)
	if err != nil {
		t.Fatalf("重新打开: %v", err)
	}
	defer func() { _ = store.Close() }()

	session := store.Session()
	if session == nil || session.Token != "t" || session.User.Username != "alice" {
		t.Fatalf("登录态未持久化：%+v", session)
	}
	if got := len(store.Messages(7)); got != 2 {
		t.Fatalf("消息应持久化 2 条，实际 %d", got)
	}
	if got := store.LastSeq(7); got != 2 {
		t.Fatalf("LastSeq = %d", got)
	}
	if conv := store.Conversation(7); conv == nil || conv.LastMessage == nil || conv.LastMessage.MsgID != "m2" {
		t.Fatalf("会话摘要未持久化：%+v", conv)
	}
}

// 乱序插入也要按 seq 升序返回；同 msg_id 只留一条。
func TestStoreMessagesOrderedAndDeduped(t *testing.T) {
	store := newTestStore(t)

	store.AddMessage(&Message{MsgID: "m2", ConversationID: 7, Seq: 2})
	store.AddMessage(&Message{MsgID: "m1", ConversationID: 7, Seq: 1})
	store.AddMessage(&Message{MsgID: "m2", ConversationID: 7, Seq: 2}) // 完全重复
	store.AddMessage(&Message{MsgID: "m2", ConversationID: 7, Seq: 9}) // 同 msg_id 不同 seq
	store.AddMessage(&Message{MsgID: "m3", ConversationID: 8, Seq: 1}) // 其它会话

	list := store.Messages(7)
	if len(list) != 2 {
		t.Fatalf("消息数 = %d，期望 2：%+v", len(list), list)
	}
	if list[0].Seq != 1 || list[1].Seq != 2 {
		t.Fatalf("消息未按 seq 升序：%d %d", list[0].Seq, list[1].Seq)
	}
	if got := store.LastSeq(7); got != 2 {
		t.Fatalf("LastSeq = %d，期望 2（同 msg_id 的新 seq 不应写入）", got)
	}
	if got := len(store.Messages(8)); got != 1 {
		t.Fatalf("会话隔离失败，conv8 消息数 = %d", got)
	}
}

// 已读只增不减：乱序上报（先 100 再 90）必须保持 100。
func TestStoreReadWatermarkMonotonic(t *testing.T) {
	store := newTestStore(t)
	store.UpsertConversations([]*Conversation{{ID: 7, LastSeq: 100}})

	store.SetRead(7, 100)
	conv := store.Conversation(7)
	if conv.ReadSeq != 100 || conv.Unread != 0 {
		t.Fatalf("推进已读后：read=%d unread=%d", conv.ReadSeq, conv.Unread)
	}

	store.SetRead(7, 90)
	conv = store.Conversation(7)
	if conv.ReadSeq != 100 {
		t.Fatalf("已读被回退到 %d", conv.ReadSeq)
	}
	if conv.Unread != 0 {
		t.Fatalf("未读应保持 0，实际 %d", conv.Unread)
	}

	store.SetPeerRead(7, 60)
	store.SetPeerRead(7, 10)
	if conv = store.Conversation(7); conv.PeerReadSeq != 60 {
		t.Fatalf("对端已读被回退到 %d", conv.PeerReadSeq)
	}

	store.IncrementUnread(7)
	if conv = store.Conversation(7); conv.Unread != 1 {
		t.Fatalf("IncrementUnread 后未读 = %d", conv.Unread)
	}
	store.MarkUnreadZero(7)
	if conv = store.Conversation(7); conv.Unread != 0 {
		t.Fatalf("MarkUnreadZero 后未读 = %d", conv.Unread)
	}
}

// 服务端摘要乱序到达：水位取 max，未读/版本等展示字段以最新一份为准；
// 请求里没带 peer / last_message 时不能把已有内容清掉。
func TestStoreUpsertWatermarksMonotonic(t *testing.T) {
	store := newTestStore(t)
	store.UpsertConversations([]*Conversation{{
		ID:          7,
		LastSeq:     5,
		ReadSeq:     3,
		PeerReadSeq: 2,
		Unread:      2,
		Version:     10,
		PeerID:      9,
		Peer:        &User{ID: 9, Nickname: "Bob"},
		LastMessage: &Message{MsgID: "m5", Seq: 5, CreatedAt: "2026-01-01T00:00:05Z"},
		Pinned:      true,
		JoinedAt:    "2026-01-01T00:00:00Z",
	}})

	store.UpsertConversations([]*Conversation{{
		ID:          7,
		LastSeq:     3, // 回退：应被 max 挡住
		ReadSeq:     1,
		PeerReadSeq: 1,
		Unread:      9,
		Version:     8,
	}})

	conv := store.Conversation(7)
	if conv.LastSeq != 5 || conv.ReadSeq != 3 || conv.PeerReadSeq != 2 {
		t.Fatalf("水位被回退：last=%d read=%d peer=%d", conv.LastSeq, conv.ReadSeq, conv.PeerReadSeq)
	}
	if conv.Unread != 9 || conv.Version != 8 {
		t.Fatalf("展示字段应以最新一份为准：unread=%d version=%d", conv.Unread, conv.Version)
	}
	// pinned/muted 是展示字段，跟 unread/version 一样以最新一份为准（含零值），
	// 只有水位（last_seq/read_seq/peer_read_seq）取 max。
	if conv.Pinned {
		t.Fatalf("pinned 应以最新一份为准（第二份没带，即 false）")
	}
	if conv.Peer == nil || conv.Peer.Nickname != "Bob" {
		t.Fatalf("peer 被清空：%+v", conv.Peer)
	}
	if conv.LastMessage == nil || conv.LastMessage.MsgID != "m5" {
		t.Fatalf("last_message 被清空：%+v", conv.LastMessage)
	}
}

func TestStoreClear(t *testing.T) {
	store := newTestStore(t)
	store.SetSession(&Session{Token: "t"})
	store.AddMessage(&Message{MsgID: "m1", ConversationID: 7, Seq: 1})

	store.Clear()

	if store.Session() != nil {
		t.Fatalf("Clear 后仍有登录态")
	}
	if got := len(store.Conversations()); got != 0 {
		t.Fatalf("Clear 后会话数 = %d", got)
	}
	if got := len(store.Messages(7)); got != 0 {
		t.Fatalf("Clear 后消息数 = %d", got)
	}
	if got := store.LastSeq(7); got != 0 {
		t.Fatalf("Clear 后 LastSeq = %d", got)
	}
}

// 旧版 store.json 在首次打开时导入，然后改名留证。
func TestStoreImportsLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacy := StoreData{
		Session: &Session{User: User{ID: 1, Username: "alice"}, Token: "t", ServerURL: "http://x", WSPort: 8001},
		Conversations: map[uint64]*Conversation{
			7: {
				ID:          7,
				LastSeq:     2,
				ReadSeq:     1,
				PeerID:      9,
				Peer:        &User{ID: 9, Nickname: "Bob"},
				LastMessage: &Message{MsgID: "m2", ConversationID: 7, Seq: 2, CreatedAt: "2026-01-01T00:00:02Z"},
			},
		},
		Messages: map[uint64][]*Message{
			7: {
				{MsgID: "m1", ConversationID: 7, Seq: 1, Body: `{"text":"hi"}`},
				{MsgID: "m2", ConversationID: 7, Seq: 2, Body: `{"text":"yo"}`},
			},
		},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	legacyPath := filepath.Join(dir, legacyStoreFile)
	if err := os.WriteFile(legacyPath, raw, 0o600); err != nil {
		t.Fatalf("写旧缓存: %v", err)
	}

	store, err := NewStore(filepath.Join(dir, "rym.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if session := store.Session(); session == nil || session.Token != "t" {
		t.Fatalf("登录态未导入：%+v", session)
	}
	if got := len(store.Messages(7)); got != 2 {
		t.Fatalf("消息未导入，数量 %d", got)
	}
	conv := store.Conversation(7)
	if conv == nil || conv.LastSeq != 2 || conv.Peer == nil || conv.LastMessage == nil {
		t.Fatalf("会话未导入：%+v", conv)
	}

	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("旧文件应被改名，实际仍存在（err=%v）", err)
	}
	if _, err := os.Stat(legacyPath + ".imported"); err != nil {
		t.Fatalf("旧文件未留证：%v", err)
	}
}

// 库已有数据时不再导入旧文件（避免重复导入覆盖当前状态）。
func TestStoreSkipsLegacyImportWhenNotEmpty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rym.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.AddMessage(&Message{MsgID: "new", ConversationID: 7, Seq: 1})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	legacy := StoreData{Messages: map[uint64][]*Message{
		7: {{MsgID: "old", ConversationID: 7, Seq: 1}},
	}}
	raw, _ := json.Marshal(legacy)
	legacyPath := filepath.Join(dir, legacyStoreFile)
	if err := os.WriteFile(legacyPath, raw, 0o600); err != nil {
		t.Fatalf("写旧缓存: %v", err)
	}

	store, err = NewStore(dbPath)
	if err != nil {
		t.Fatalf("重新打开: %v", err)
	}
	defer func() { _ = store.Close() }()

	list := store.Messages(7)
	if len(list) != 1 || list[0].MsgID != "new" {
		t.Fatalf("不应导入旧数据：%+v", list)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("跳过导入时不应动旧文件：%v", err)
	}
}
