package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Client 是桌面/移动客户端核心：HTTP + WS + 本地缓存。
type Client struct {
	mu      sync.Mutex
	store   *Store
	tokens  tokenStore
	http    *httpClient
	ws      *WSClient
	session *Session

	onEvent func(Event)
	onState func(ConnectionState)

	// 发送队列：sendPending 默认走 HTTP，测试注入假实现。
	sendPending   pendingSender
	outboxMu      sync.Mutex
	outboxRunning bool
	outboxStop    context.CancelFunc
	outboxBase    time.Duration
	outboxMax     time.Duration
}

// New 创建客户端；dbPath 为空时使用平台默认路径。
func New(dbPath string) (*Client, error) {
	return newClient(dbPath, newTokenStore())
}

// newClient 允许测试注入令牌存储，避免 `go test` 往真实凭据库里写东西。
func newClient(dbPath string, tokens tokenStore) (*Client, error) {
	if dbPath == "" {
		path, err := DefaultDBPath()
		if err != nil {
			return nil, fmt.Errorf("定位应用数据目录: %w", err)
		}
		dbPath = path
	}
	store, err := NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开本地数据库: %w", err)
	}
	c := &Client{
		store:      store,
		tokens:     tokens,
		outboxBase: time.Second,
		outboxMax:  30 * time.Second,
	}
	c.sendPending = c.sendPendingHTTP
	return c, nil
}

// SetEventHandlers 注册 WS 事件/连接状态回调（Wails 服务在这里桥接事件）。
func (c *Client) SetEventHandlers(onEvent func(Event), onState func(ConnectionState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEvent = onEvent
	c.onState = onState
}

// Register 注册账号（不自动登录）。
func (c *Client) Register(ctx context.Context, serverURL, username, password, nickname string) (*User, error) {
	hc, err := newHTTPClient(serverURL)
	if err != nil {
		return nil, err
	}
	return hc.register(ctx, username, password, nickname)
}

// Login 登录并拉会话列表、建立 WS。
func (c *Client) Login(ctx context.Context, serverURL string, wsPort int, username, password string) (*SessionInfo, error) {
	hc, err := newHTTPClient(serverURL)
	if err != nil {
		return nil, err
	}
	data, err := hc.login(ctx, username, password)
	if err != nil {
		return nil, err
	}
	hc.setToken(data.AccessToken)

	session := &Session{
		User:      data.User,
		Token:     data.AccessToken,
		ServerURL: normalizeBase(serverURL),
		WSPort:    wsPort,
	}

	// 换账号：清掉上一个账号的本地消息与发送队列。
	// 必须清 outbox，否则会用新账号把别人的待发消息发出去。
	if cached := c.store.Session(); cached != nil && cached.User.ID != 0 && cached.User.ID != data.User.ID {
		c.store.Clear()
	}

	c.mu.Lock()
	c.http = hc
	c.session = session
	c.mu.Unlock()
	c.store.SetSession(session)
	// 令牌写系统凭据库；写失败只影响“下次自动登录”，不阻断本次登录。
	_ = c.tokens.Set(session.Token)

	if _, err := c.fetchConversations(ctx); err != nil {
		// 登录成功但列表失败：保留登录态，返回错误让 UI 重试。
		c.startWS()
		go c.flushOutbox() // 上次没发完的消息继续发
		return sessionInfo(session), err
	}
	c.startWS()
	go c.flushOutbox()
	return sessionInfo(session), nil
}

// AutoLogin 用本地保存的 token 恢复登录；token 失效时清空。
func (c *Client) AutoLogin(ctx context.Context) (*SessionInfo, bool, error) {
	session := c.store.Session()
	if session == nil {
		return nil, false, nil
	}

	token, err := c.tokens.Get()
	if err != nil && session.Token != "" {
		// 升级路径：旧版本把 token 存在库里。搬到凭据库后把库里的副本抹掉，
		// 成功后 store.SetSession 不会再写 token。
		token = session.Token
		_ = c.tokens.Set(token)
		c.store.SetSession(session)
	}
	if token == "" {
		return nil, false, nil
	}
	session.Token = token

	hc, err := newHTTPClient(session.ServerURL)
	if err != nil {
		return nil, false, err
	}
	hc.setToken(token)
	user, err := hc.me(ctx)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			// 只清令牌，保留本地消息与发送队列：同一用户重新登录后还能补发。
			// 换账号时由 Login 按 user_id 清库（见 Login 里的 cached 判断）。
			_ = c.tokens.Delete()
			return nil, false, nil
		}
		return nil, false, err
	}
	session.User = *user

	c.mu.Lock()
	c.http = hc
	c.session = session
	c.mu.Unlock()
	c.store.SetSession(session)

	if _, err := c.fetchConversations(ctx); err != nil {
		c.startWS()
		go c.flushOutbox()
		return sessionInfo(session), true, err
	}
	c.startWS()
	go c.flushOutbox()
	return sessionInfo(session), true, nil
}

// closeWS 断开 WS，不碰本地库（登出也要用它，因为库还要留着被 Clear）。
func (c *Client) closeWS() {
	c.mu.Lock()
	ws := c.ws
	c.ws = nil
	c.mu.Unlock()
	if ws != nil {
		ws.Stop()
	}
}

// Close 关闭 WS 与本地数据库（应用退出时用；之后 Client 不可再用）。
// 只关连接，不清数据——下次启动还要自动登录。
func (c *Client) Close() {
	c.closeWS()
	c.stopOutbox()
	_ = c.store.Close()
}

// Logout 断开 WS 并清空本地数据（库文件保留，下次登录复用）。
func (c *Client) Logout() {
	c.closeWS()
	c.stopOutbox()
	c.mu.Lock()
	c.http = nil
	c.session = nil
	c.mu.Unlock()
	c.store.Clear()
	_ = c.tokens.Delete()
}

// Session 返回当前登录信息（不含 token）。
func (c *Client) Session() *SessionInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil
	}
	return sessionInfo(c.session)
}

// Conversations 返回本地会话列表。
func (c *Client) Conversations() []*Conversation {
	return c.store.Conversations()
}

// Conversation 返回单个会话（通知里取对端昵称用）。
func (c *Client) Conversation(id uint64) *Conversation {
	return c.store.Conversation(id)
}

// SearchUsers 搜索用户。
func (c *Client) SearchUsers(ctx context.Context, keyword string) ([]*User, error) {
	c.mu.Lock()
	hc := c.http
	c.mu.Unlock()
	if hc == nil {
		return nil, errors.New("未登录")
	}
	return hc.searchUsers(ctx, keyword, 20)
}

// OpenConversation 打开单聊（不存在则创建）。
func (c *Client) OpenConversation(ctx context.Context, toUserID uint64) (*Conversation, error) {
	c.mu.Lock()
	hc := c.http
	c.mu.Unlock()
	if hc == nil {
		return nil, errors.New("未登录")
	}
	conv, err := hc.openConversation(ctx, toUserID)
	if err != nil {
		return nil, err
	}
	if users, err := hc.batchUsers(ctx, []uint64{toUserID}); err == nil && len(users) > 0 {
		conv.Peer = users[0]
	}
	c.store.UpsertConversations([]*Conversation{conv})
	return conv, nil
}

// Messages 拉取会话消息（beforeSeq=0 表示最新一页），合并本地缓存。
func (c *Client) Messages(ctx context.Context, conversationID uint64, beforeSeq int64, limit int) ([]*Message, error) {
	c.mu.Lock()
	hc := c.http
	c.mu.Unlock()
	if hc == nil {
		return nil, errors.New("未登录")
	}
	if limit <= 0 {
		limit = 50
	}
	res, err := hc.pullMessages(ctx, conversationID, beforeSeq, 0, limit)
	if err != nil {
		return nil, err
	}
	for _, m := range res.List {
		c.store.AddMessage(m)
	}
	return c.store.Messages(conversationID), nil
}

// SendMessage 先把消息放进本地发送队列并立刻返回（state=sending）。
//
// 真正的网络发送在后台做：网络错误/429/5xx 指数退避重试，复用 client_msg_id；
// 确认后发 message.sent 事件并写入 messages，4xx 则发 message.failed 且不再重试。
// 这样断网、进程重启都不会丢“已经点了发送”的消息。
func (c *Client) SendMessage(_ context.Context, conversationID uint64, text string) (*Message, error) {
	c.mu.Lock()
	hc := c.http
	session := c.session
	c.mu.Unlock()
	if hc == nil || session == nil {
		return nil, errors.New("未登录")
	}

	now := time.Now().Truncate(time.Millisecond)
	item := &OutboxMessage{
		ClientMsgID:    uuid.NewString(),
		ConversationID: conversationID,
		SenderID:       session.User.ID,
		Body:           textBody(text),
		CreatedAt:      outboxCreatedAt(now),
		Status:         OutboxSending,
		NextAttemptAt:  now.UnixMilli(),
	}
	if err := c.store.EnqueueOutbox(item); err != nil {
		return nil, fmt.Errorf("写入发送队列: %w", err)
	}
	go c.flushOutbox()
	return item.Message(), nil
}

// MarkRead 上报已读位。
func (c *Client) MarkRead(ctx context.Context, conversationID uint64, readSeq int64) error {
	c.mu.Lock()
	hc := c.http
	c.mu.Unlock()
	if hc == nil {
		return errors.New("未登录")
	}
	if err := hc.markRead(ctx, conversationID, readSeq); err != nil {
		return err
	}
	c.store.SetRead(conversationID, readSeq)
	c.store.MarkUnreadZero(conversationID)
	return nil
}

func (c *Client) startWS() {
	c.mu.Lock()
	session := c.session
	old := c.ws
	c.ws = nil
	c.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	if session == nil || session.Token == "" {
		return
	}

	ws := newWSClient(wsURL(session.ServerURL, session.WSPort), session.Token, c.handleEvent, c.emitState)
	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()
	ws.Start(context.Background())
}

func (c *Client) emitState(state ConnectionState) {
	if state.Connected {
		// WS 重连成功 = 网络恢复的信号：顺手把积压的发送队列推一遍。
		go c.flushOutbox()
	}
	c.mu.Lock()
	cb := c.onState
	c.mu.Unlock()
	if cb != nil {
		cb(state)
	}
}

func (c *Client) emitEvent(event Event) {
	c.mu.Lock()
	cb := c.onEvent
	session := c.session
	c.mu.Unlock()

	// 自己的消息不计未读；推送里带 sender_id，能判断。
	if event.Type == "message.created" {
		var data MessageCreatedData
		if err := json.Unmarshal(event.Data, &data); err == nil && session != nil {
			if data.Message.SenderID != session.User.ID {
				c.store.IncrementUnread(data.ConversationID)
			}
		}
	}
	if cb != nil {
		cb(event)
	}
}

func (c *Client) handleEvent(event Event) {
	switch event.Type {
	case "message.created":
		var data MessageCreatedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return
		}
		before := c.store.LastSeq(data.ConversationID)
		c.store.AddMessage(&data.Message)

		// 跳号：异步补洞。首次加载（before=0）交给 Messages 接口。
		if before > 0 && data.Message.Seq > before+1 {
			go func(convID uint64) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = c.syncAfter(ctx, convID)
			}(data.ConversationID)
		}
		c.emitEvent(event)

	case "read.updated":
		var data ReadUpdatedData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return
		}
		c.store.SetPeerRead(data.ConversationID, data.ReadSeq)
		c.emitEvent(event)

	default:
		// hello/未知类型：只透传给前端，便于显示连接信息。
		c.emitEvent(event)
	}
}

// syncAfter 从本地最大 seq 往后补洞，直到 has_more=false。
func (c *Client) syncAfter(ctx context.Context, conversationID uint64) error {
	c.mu.Lock()
	hc := c.http
	c.mu.Unlock()
	if hc == nil {
		return errors.New("未登录")
	}
	for i := 0; i < 100; i++ {
		after := c.store.LastSeq(conversationID)
		res, err := hc.pullMessages(ctx, conversationID, 0, after, 100)
		if err != nil {
			return err
		}
		for _, m := range res.List {
			c.store.AddMessage(m)
		}
		if !res.HasMore {
			return nil
		}
	}
	return nil
}

func (c *Client) fetchConversations(ctx context.Context) ([]*Conversation, error) {
	c.mu.Lock()
	hc := c.http
	c.mu.Unlock()
	if hc == nil {
		return nil, errors.New("未登录")
	}
	list, err := hc.listConversations(ctx, 100)
	if err != nil {
		return nil, err
	}

	peerIDs := make([]uint64, 0, len(list))
	for _, conv := range list {
		if conv.PeerID > 0 {
			peerIDs = append(peerIDs, conv.PeerID)
		}
	}
	if len(peerIDs) > 0 {
		if users, err := hc.batchUsers(ctx, peerIDs); err == nil {
			byID := make(map[uint64]*User, len(users))
			for _, u := range users {
				byID[u.ID] = u
			}
			for _, conv := range list {
				if conv.PeerID > 0 {
					conv.Peer = byID[conv.PeerID]
				}
			}
		}
	}
	c.store.UpsertConversations(list)
	return c.store.Conversations(), nil
}

func sessionInfo(s *Session) *SessionInfo {
	if s == nil {
		return nil
	}
	return &SessionInfo{User: s.User, ServerURL: s.ServerURL, WSPort: s.WSPort}
}
