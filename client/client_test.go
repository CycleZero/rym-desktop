package client

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTextBodyAndWSURL(t *testing.T) {
	if got := textBody(`a"b`); got != `{"text":"a\"b"}` {
		t.Fatalf("textBody = %s", got)
	}
	if got := wsURL("http://192.168.0.136:18080/", 8001); got != "ws://192.168.0.136:8001/v1/im/ws" {
		t.Fatalf("wsURL = %s", got)
	}
	if got := wsURL("https://chat.example.com", 443); got != "wss://chat.example.com:443/v1/im/ws" {
		t.Fatalf("wsURL = %s", got)
	}
}

func TestStoreAddDedupeAndRead(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "store.json"))
	msg := &Message{MsgID: "m1", ConversationID: 7, Seq: 1, SenderID: 2, Body: `{"text":"hi"}`}
	store.AddMessage(msg)
	store.AddMessage(msg) // 去重

	if got := len(store.Messages(7)); got != 1 {
		t.Fatalf("消息应去重为 1 条，实际 %d", got)
	}
	if got := store.LastSeq(7); got != 1 {
		t.Fatalf("LastSeq = %d", got)
	}
	store.SetRead(7, 1)
	conv := store.Conversation(7)
	if conv == nil || conv.ReadSeq != 1 || conv.Unread != 0 {
		t.Fatalf("已读状态不对：%+v", conv)
	}
	if !strings.Contains(store.Conversations()[0].LastMessage.Body, "hi") {
		t.Fatalf("会话最后消息不对：%+v", store.Conversations()[0])
	}
}
