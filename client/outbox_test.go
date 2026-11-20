package client

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// newTestClient 返回一个“已登录”的客户端：只填 http/session 的壳，
// 网络发送替换成假实现，避免测试连服务器。
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := newClient(filepath.Join(t.TempDir(), "rym.db"), &memoryTokenStore{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	t.Cleanup(c.Close)

	c.mu.Lock()
	c.http = &httpClient{}
	c.session = &Session{User: User{ID: 1, Username: "alice"}, Token: "t"}
	c.mu.Unlock()
	return c
}

func enqueueTestMessage(t *testing.T, c *Client, conversationID uint64, text string) *OutboxMessage {
	t.Helper()
	now := time.Now().Truncate(time.Millisecond)
	item := &OutboxMessage{
		ClientMsgID:    uuid.NewString(),
		ConversationID: conversationID,
		SenderID:       1,
		Body:           textBody(text),
		CreatedAt:      outboxCreatedAt(now),
		Status:         OutboxSending,
		NextAttemptAt:  now.UnixMilli(),
	}
	if err := c.store.EnqueueOutbox(item); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	return item
}

// confirmedMessage 造一个服务端确认后的消息。
func confirmedMessage(item *OutboxMessage, seq int64) *Message {
	return &Message{
		MsgID:          "srv-" + item.ClientMsgID,
		ConversationID: item.ConversationID,
		Seq:            seq,
		SenderID:       item.SenderID,
		ClientMsgID:    item.ClientMsgID,
		Type:           messageTypeText,
		Body:           item.Body,
	}
}

type eventSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *eventSink) add(ev Event) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *eventSink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, ev := range s.events {
		out = append(out, ev.Type)
	}
	return out
}

// 正常路径：队列按入队顺序发出、写入 messages、清空队列、推 message.sent。
func TestClientFlushResolvesQueue(t *testing.T) {
	c := newTestClient(t)
	sink := &eventSink{}
	c.SetEventHandlers(sink.add, nil)

	var sent []string
	c.sendPending = func(item *OutboxMessage) (*Message, error) {
		sent = append(sent, item.ClientMsgID)
		return confirmedMessage(item, int64(len(sent))), nil
	}

	first := enqueueTestMessage(t, c, 7, "a")
	second := enqueueTestMessage(t, c, 7, "b")
	c.flushOutbox()

	if len(sent) != 2 || sent[0] != first.ClientMsgID || sent[1] != second.ClientMsgID {
		t.Fatalf("发送顺序不对：%v", sent)
	}
	if left := c.store.OutboxOf(7); len(left) != 0 {
		t.Fatalf("队列应清空，剩余 %d", len(left))
	}
	msgs := c.store.Messages(7)
	if len(msgs) != 2 {
		t.Fatalf("消息数 = %d", len(msgs))
	}
	if msgs[0].Seq != 1 || msgs[1].Seq != 2 || msgs[0].State != "" {
		t.Fatalf("确认后的消息不对：%+v %+v", msgs[0], msgs[1])
	}
	if got := sink.types(); len(got) != 2 || got[0] != "message.sent" || got[1] != "message.sent" {
		t.Fatalf("事件不对：%v", got)
	}
}

// 网络错误 → 指数退避后重试，复用同一个 client_msg_id，最终成功。
func TestClientFlushRetriesThenSucceeds(t *testing.T) {
	c := newTestClient(t)
	c.outboxBase = 5 * time.Millisecond
	c.outboxMax = 10 * time.Millisecond

	attempts := 0
	c.sendPending = func(item *OutboxMessage) (*Message, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("dial tcp 127.0.0.1:8000: connect: connection refused")
		}
		return confirmedMessage(item, 1), nil
	}

	item := enqueueTestMessage(t, c, 7, "hello")
	c.flushOutbox()

	if attempts != 2 {
		t.Fatalf("应重试一次，实际尝试 %d 次", attempts)
	}
	if left := c.store.OutboxOf(7); len(left) != 0 {
		t.Fatalf("重试成功后队列应清空：%+v", left)
	}
	msgs := c.store.Messages(7)
	if len(msgs) != 1 || msgs[0].ClientMsgID != item.ClientMsgID {
		t.Fatalf("消息未落库：%+v", msgs)
	}
}

// 4xx（权限/参数）不重试：标记 failed，保留在队列里给 UI 展示。
func TestClientFlushMarksFailedOn4xx(t *testing.T) {
	c := newTestClient(t)
	sink := &eventSink{}
	c.SetEventHandlers(sink.add, nil)

	calls := 0
	c.sendPending = func(*OutboxMessage) (*Message, error) {
		calls++
		return nil, &APIError{Status: 403, Code: 40300, Message: "非会话成员"}
	}

	item := enqueueTestMessage(t, c, 7, "hello")
	c.flushOutbox()

	if calls != 1 {
		t.Fatalf("4xx 不应重试，实际调用 %d 次", calls)
	}
	left := c.store.OutboxOf(7)
	if len(left) != 1 || left[0].Status != OutboxFailed || left[0].LastError == "" {
		t.Fatalf("应标记 failed：%+v", left)
	}
	if due := c.store.DueOutbox(time.Now().Add(time.Hour).UnixMilli(), 10); len(due) != 0 {
		t.Fatalf("failed 不应再被调度：%+v", due)
	}
	msgs := c.store.Messages(7)
	if len(msgs) != 1 || msgs[0].State != OutboxFailed || msgs[0].ClientMsgID != item.ClientMsgID {
		t.Fatalf("失败的本地消息应出现在列表里：%+v", msgs)
	}
	if got := sink.types(); len(got) != 1 || got[0] != "message.failed" {
		t.Fatalf("事件不对：%v", got)
	}
}

// 401：保留队列（不标 failed、不重试），等重新登录后再发。
func TestClientFlushStopsOnUnauthorized(t *testing.T) {
	c := newTestClient(t)
	calls := 0
	c.sendPending = func(*OutboxMessage) (*Message, error) {
		calls++
		return nil, ErrUnauthorized
	}

	enqueueTestMessage(t, c, 7, "hello")
	c.flushOutbox()

	if calls != 1 {
		t.Fatalf("401 后应停止，实际调用 %d 次", calls)
	}
	left := c.store.OutboxOf(7)
	if len(left) != 1 || left[0].Status != OutboxSending {
		t.Fatalf("401 后消息应保持 sending：%+v", left)
	}
}

// WS 重连成功会触发补发。
func TestClientFlushesOnReconnect(t *testing.T) {
	c := newTestClient(t)
	done := make(chan struct{}, 1)
	c.sendPending = func(item *OutboxMessage) (*Message, error) {
		select {
		case done <- struct{}{}:
		default:
		}
		return confirmedMessage(item, 1), nil
	}

	enqueueTestMessage(t, c, 7, "hello")
	c.emitState(ConnectionState{Connected: true})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("重连后没有触发补发")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.store.OutboxOf(7)) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("补发未完成：%+v", c.store.OutboxOf(7))
}

// 服务端回推自己发的消息（带 client_msg_id）等同于确认，队列项要清掉。
func TestStoreEchoClearsOutbox(t *testing.T) {
	store := newTestStore(t)
	item := &OutboxMessage{
		ClientMsgID: "c1", ConversationID: 7, SenderID: 1,
		Body: `{"text":"hi"}`, CreatedAt: outboxCreatedAt(time.Now()), Status: OutboxSending,
	}
	if err := store.EnqueueOutbox(item); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}

	store.AddMessage(&Message{MsgID: "m1", ConversationID: 7, Seq: 1, SenderID: 1,
		ClientMsgID: "c1", Body: `{"text":"hi"}`})

	if left := store.OutboxOf(7); len(left) != 0 {
		t.Fatalf("收到回推后队列应清空：%+v", left)
	}
	if msgs := store.Messages(7); len(msgs) != 1 || msgs[0].State != "" {
		t.Fatalf("消息列表不对：%+v", msgs)
	}
}

// 队列项在重启后仍然存在，并且退避时间未到不会被提前发送。
func TestOutboxSurvivesRestartAndRespectsBackoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rym.db")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	now := time.Now().Truncate(time.Millisecond)
	if err := store.EnqueueOutbox(&OutboxMessage{
		ClientMsgID: "c1", ConversationID: 7, SenderID: 1, Body: `{"text":"hi"}`,
		CreatedAt: outboxCreatedAt(now), Status: OutboxSending,
		NextAttemptAt: now.Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err = NewStore(path)
	if err != nil {
		t.Fatalf("重新打开: %v", err)
	}
	defer func() { _ = store.Close() }()

	if due := store.DueOutbox(now.UnixMilli(), 10); len(due) != 0 {
		t.Fatalf("未到退避时间不应发送：%+v", due)
	}
	due := store.DueOutbox(now.Add(2*time.Hour).UnixMilli(), 10)
	if len(due) != 1 || due[0].ClientMsgID != "c1" {
		t.Fatalf("重启后队列丢失：%+v", due)
	}
	next, ok := store.OutboxNextAttempt()
	if !ok || next != now.Add(time.Hour).UnixMilli() {
		t.Fatalf("OutboxNextAttempt = %d, %v", next, ok)
	}
}

// 未确认的消息接在已确认消息后面，并带上 state，方便 UI 显示“发送中”。
func TestStoreMessagesIncludePending(t *testing.T) {
	store := newTestStore(t)
	store.AddMessage(&Message{MsgID: "m1", ConversationID: 7, Seq: 1, SenderID: 2, Body: `{"text":"old"}`})
	if err := store.EnqueueOutbox(&OutboxMessage{
		ClientMsgID: "c1", ConversationID: 7, SenderID: 1, Body: `{"text":"new"}`,
		CreatedAt: outboxCreatedAt(time.Now()), Status: OutboxSending,
	}); err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}

	msgs := store.Messages(7)
	if len(msgs) != 2 {
		t.Fatalf("消息数 = %d", len(msgs))
	}
	pending := msgs[1]
	if pending.ClientMsgID != "c1" || pending.Seq != 0 || pending.State != OutboxSending || pending.MsgID != "" {
		t.Fatalf("pending 消息字段不对：%+v", pending)
	}

	// 事件 payload 能被前端解析（字段名是契约）。
	data, err := json.Marshal(MessageSentData{ConversationID: 7, ClientMsgID: "c1", Message: msgs[0]})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"conversation_id", "client_msg_id", "message"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("payload 缺少 %q：%s", key, data)
		}
	}
}
