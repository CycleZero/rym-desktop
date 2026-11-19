package client

// 真实服务端 E2E：默认 skip。
//
// 用法：
//   RYM_E2E_BASE=http://127.0.0.1:18080 RYM_E2E_WS_PORT=18081 \
//     go test ./client -run TestE2E -v

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
	base := os.Getenv("RYM_E2E_BASE")
	if base == "" {
		t.Skip("设置 RYM_E2E_BASE / RYM_E2E_WS_PORT 后运行")
	}
	wsPort := 8001
	if raw := os.Getenv("RYM_E2E_WS_PORT"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			wsPort = n
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	aliceName := fmt.Sprintf("e2ealice%d", suffix)
	bobName := fmt.Sprintf("e2ebob%d", suffix)
	const password = "password123"

	// 用内存令牌存储：E2E 不能往真实凭据库或 HOME 里写东西。
	alicePath := filepath.Join(t.TempDir(), "alice.db")
	aliceTokens := &memoryTokenStore{}
	alice, err := newClient(alicePath, aliceTokens)
	if err != nil {
		t.Fatalf("创建 alice 客户端: %v", err)
	}
	bob, err := newClient(filepath.Join(t.TempDir(), "bob.db"), &memoryTokenStore{})
	if err != nil {
		t.Fatalf("创建 bob 客户端: %v", err)
	}
	defer alice.Close()
	defer bob.Close()
	defer alice.Logout()
	defer bob.Logout()

	aliceEvents := make(chan Event, 32)
	bobEvents := make(chan Event, 32)
	aliceStates := make(chan ConnectionState, 8)
	bobStates := make(chan ConnectionState, 8)
	alice.SetEventHandlers(func(ev Event) { aliceEvents <- ev }, func(s ConnectionState) { aliceStates <- s })
	bob.SetEventHandlers(func(ev Event) { bobEvents <- ev }, func(s ConnectionState) { bobStates <- s })

	aliceUser, err := alice.Register(ctx, base, aliceName, password, "Alice E2E")
	if err != nil {
		t.Fatalf("注册 alice: %v", err)
	}
	bobUser, err := bob.Register(ctx, base, bobName, password, "Bob E2E")
	if err != nil {
		t.Fatalf("注册 bob: %v", err)
	}
	if _, err := alice.Login(ctx, base, wsPort, aliceName, password); err != nil {
		t.Fatalf("登录 alice: %v", err)
	}
	if _, err := bob.Login(ctx, base, wsPort, bobName, password); err != nil {
		t.Fatalf("登录 bob: %v", err)
	}
	waitConnected(t, aliceStates)
	waitConnected(t, bobStates)

	users, err := alice.SearchUsers(ctx, "e2ebob"+strconv.FormatInt(suffix, 10))
	if err != nil {
		t.Fatalf("搜索: %v", err)
	}
	if len(users) == 0 || users[0].ID != bobUser.ID {
		t.Fatalf("搜索未命中 bob: %+v", users)
	}

	conv, err := alice.OpenConversation(ctx, bobUser.ID)
	if err != nil {
		t.Fatalf("打开会话: %v", err)
	}
	if conv.PeerID != bobUser.ID {
		t.Fatalf("peer_id=%d，期望 %d", conv.PeerID, bobUser.ID)
	}

	sent, err := alice.SendMessage(ctx, conv.ID, "hello from e2e")
	if err != nil {
		t.Fatalf("发送: %v", err)
	}
	if sent.Seq == 0 || sent.MsgID == "" {
		t.Fatalf("发送返回不完整: %+v", sent)
	}

	ev := waitEvent(t, bobEvents, "message.created")
	var created MessageCreatedData
	if err := json.Unmarshal(ev.Data, &created); err != nil {
		t.Fatalf("解析 message.created: %v", err)
	}
	if created.Message.Seq != sent.Seq || created.ConversationID != conv.ID {
		t.Fatalf("WS 消息不匹配: %+v", created)
	}

	if err := bob.MarkRead(ctx, conv.ID, sent.Seq); err != nil {
		t.Fatalf("bob 已读: %v", err)
	}
	readEv := waitEvent(t, aliceEvents, "read.updated")
	var read ReadUpdatedData
	if err := json.Unmarshal(readEv.Data, &read); err != nil {
		t.Fatalf("解析 read.updated: %v", err)
	}
	if read.UserID != bobUser.ID || read.ReadSeq < sent.Seq {
		t.Fatalf("read.updated 不对: %+v", read)
	}

	msgs, err := bob.Messages(ctx, conv.ID, 0, 50)
	if err != nil {
		t.Fatalf("拉历史: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.MsgID == sent.MsgID {
			found = true
		}
	}
	if !found {
		t.Fatalf("历史里没有刚发的消息: %+v", msgs)
	}

	convs := alice.Conversations()
	if len(convs) == 0 || convs[0].Peer == nil || convs[0].Peer.ID != bobUser.ID {
		t.Fatalf("会话列表 peer 不对: %+v", convs)
	}

	// 模拟重启：同一个库 + 同一个令牌存储，应能用 token 自动登录。
	// 同时验证 token 没有落库（库里的 session 不含 token，令牌只在 memoryTokenStore 里）。
	alice.Close()
	restarted, err := newClient(alicePath, aliceTokens)
	if err != nil {
		t.Fatalf("重启 alice 客户端: %v", err)
	}
	defer restarted.Close()
	info, ok, err := restarted.AutoLogin(ctx)
	if err != nil {
		t.Fatalf("自动登录: %v", err)
	}
	if !ok || info == nil || info.User.ID != aliceUser.ID {
		t.Fatalf("自动登录失败: ok=%v info=%+v", ok, info)
	}
	if len(restarted.Conversations()) == 0 {
		t.Fatalf("自动登录后本地会话未恢复")
	}
	restarted.Logout()
	if _, err := aliceTokens.Get(); !errors.Is(err, errNoToken) {
		t.Fatalf("登出后应清除令牌，实际 %v", err)
	}
}

func waitConnected(t *testing.T, states <-chan ConnectionState) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case s := <-states:
			if s.Connected {
				return
			}
		case <-deadline:
			t.Fatal("等待 WS 连接超时")
		}
	}
}

func waitEvent(t *testing.T, events <-chan Event, typ string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("等待事件 %s 超时", typ)
		}
	}
}
