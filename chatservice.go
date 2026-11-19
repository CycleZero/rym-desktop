package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CycleZero/rym-desktop/client"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// ChatService 是 Wails 与客户端核心之间的薄适配层。
//
// 前端不接触 token、不直接发 HTTP/WS；所有网络与缓存都在 Go 核心。
type ChatService struct {
	c        *client.Client
	notifier *notifications.NotificationService
}

func NewChatService(notifier *notifications.NotificationService) *ChatService {
	s := &ChatService{c: client.New(""), notifier: notifier}
	s.c.SetEventHandlers(s.emitEvent, s.emitState)
	return s
}

// ServiceName 让 Wails 把它当作普通服务管理生命周期。
func (s *ChatService) ServiceName() string { return "ChatService" }

// ServiceShutdown 应用退出时只断开 WS，保留本地缓存。
func (s *ChatService) ServiceShutdown() error {
	s.c.Close()
	return nil
}

func (s *ChatService) emitEvent(ev client.Event) {
	s.notify(ev)

	var data any
	_ = json.Unmarshal(ev.Data, &data)
	if app := application.Get(); app != nil {
		app.Event.Emit("im:event", map[string]any{
			"v": ev.V, "type": ev.Type, "data": data,
		})
	}
}

// notify 在窗口不可见/失焦时发系统通知。
func (s *ChatService) notify(ev client.Event) {
	if s.notifier == nil || ev.Type != "message.created" {
		return
	}
	var data client.MessageCreatedData
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return
	}
	if session := s.c.Session(); session != nil && data.Message.SenderID == session.User.ID {
		return
	}
	if app := application.Get(); app != nil {
		if w, ok := app.Window.GetByName("main"); ok && w.IsVisible() && w.IsFocused() {
			return
		}
	}

	title := "新消息"
	if conv := s.c.Conversation(data.ConversationID); conv != nil && conv.Peer != nil {
		switch {
		case conv.Peer.Nickname != "":
			title = conv.Peer.Nickname
		case conv.Peer.Username != "":
			title = conv.Peer.Username
		}
	}
	_ = s.notifier.SendNotification(notifications.NotificationOptions{
		ID:       data.Message.MsgID,
		Title:    title,
		Body:     messageText(data.Message.Body),
		ThreadID: fmt.Sprintf("conv-%d", data.ConversationID),
	})
}

func messageText(body string) string {
	var v struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(body), &v) == nil && v.Text != "" {
		return v.Text
	}
	return body
}

func (s *ChatService) emitState(state client.ConnectionState) {
	if app := application.Get(); app != nil {
		app.Event.Emit("im:connection", state)
	}
}

// Register 注册账号。
func (s *ChatService) Register(serverURL, username, password, nickname string) (*client.User, error) {
	return s.c.Register(context.Background(), serverURL, username, password, nickname)
}

// Login 登录并建立 WS。
func (s *ChatService) Login(serverURL string, wsPort int, username, password string) (*client.SessionInfo, error) {
	return s.c.Login(context.Background(), serverURL, wsPort, username, password)
}

// AutoLogin 使用本地 token 恢复登录。
func (s *ChatService) AutoLogin() (*client.SessionInfo, bool, error) {
	return s.c.AutoLogin(context.Background())
}

// Logout 退出并清空本地缓存。
func (s *ChatService) Logout() {
	s.c.Logout()
}

// Conversations 返回本地会话列表。
func (s *ChatService) Conversations() []*client.Conversation {
	return s.c.Conversations()
}

// SearchUsers 搜索用户。
func (s *ChatService) SearchUsers(keyword string) ([]*client.User, error) {
	return s.c.SearchUsers(context.Background(), keyword)
}

// OpenConversation 打开（必要时创建）单聊。
func (s *ChatService) OpenConversation(toUserID uint64) (*client.Conversation, error) {
	return s.c.OpenConversation(context.Background(), toUserID)
}

// Messages 拉取会话消息。
func (s *ChatService) Messages(conversationID uint64, beforeSeq int64, limit int) ([]*client.Message, error) {
	return s.c.Messages(context.Background(), conversationID, beforeSeq, limit)
}

// SendMessage 发送文本消息。
func (s *ChatService) SendMessage(conversationID uint64, text string) (*client.Message, error) {
	return s.c.SendMessage(context.Background(), conversationID, text)
}

// MarkRead 上报已读位。
func (s *ChatService) MarkRead(conversationID uint64, readSeq int64) error {
	return s.c.MarkRead(context.Background(), conversationID, readSeq)
}
