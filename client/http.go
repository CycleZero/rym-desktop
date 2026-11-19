package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	v1im "github.com/CycleZero/rym/api/im/v1"
	v1user "github.com/CycleZero/rym/api/user/v1"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
)

// APIError 是服务端统一信封里的业务错误。
type APIError struct {
	Status  int
	Code    int
	Message string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("服务端错误 %d: %s", e.Code, e.Message)
}

// ErrUnauthorized 便于前端判断 token 失效。
var ErrUnauthorized = errors.New("登录已失效")

// jsonOpts 覆盖生成客户端默认的 application/protojson：
// 本服务端使用 application/json + 统一信封。
var jsonOpts = []khttp.CallOption{
	khttp.ContentType("application/json"),
	khttp.Accept("application/json"),
}

// authTransport 给所有请求附加 Bearer token。
type authTransport struct {
	base  http.RoundTripper
	mu    sync.RWMutex
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.RLock()
	token := t.token
	t.mu.RUnlock()
	if token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return t.base.RoundTrip(req)
}

func (t *authTransport) setToken(token string) {
	t.mu.Lock()
	t.token = token
	t.mu.Unlock()
}

// httpClient 使用 proto 生成的 HTTP 客户端，只做信封/鉴权适配与 proto -> DTO 映射。
type httpClient struct {
	tr   *authTransport
	user v1user.UserServiceHTTPClient
	im   v1im.ImServiceHTTPClient
}

func newHTTPClient(base string) (*httpClient, error) {
	tr := &authTransport{base: &http.Transport{Proxy: nil}}
	cc, err := khttp.NewClient(context.Background(),
		khttp.WithEndpoint(normalizeBase(base)),
		khttp.WithTimeout(15*time.Second),
		khttp.WithTransport(tr),
		khttp.WithResponseDecoder(decodeEnvelope),
		khttp.WithErrorDecoder(decodeAPIError),
	)
	if err != nil {
		return nil, err
	}
	return &httpClient{
		tr:   tr,
		user: v1user.NewUserServiceHTTPClient(cc),
		im:   v1im.NewImServiceHTTPClient(cc),
	}, nil
}

func (c *httpClient) setToken(token string) {
	if c != nil && c.tr != nil {
		c.tr.setToken(token)
	}
}

// decodeEnvelope 拆开 {code,message,data,trace_id}，把 data 交给生成客户端。
func decodeEnvelope(_ context.Context, res *http.Response, out any) error {
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if env.Code != 0 {
		return &APIError{Status: res.StatusCode, Code: env.Code, Message: env.Message}
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

func decodeAPIError(_ context.Context, res *http.Response) error {
	// kratos 对每个响应都会调用 errorDecoder（包括 2xx），
	// 2xx 必须直接返回 nil，且不能读 body，否则 responseDecoder 拿不到内容。
	if res.StatusCode >= http.StatusOK && res.StatusCode <= 299 {
		return nil
	}
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &env)
	return &APIError{Status: res.StatusCode, Code: env.Code, Message: env.Message}
}

type loginData struct {
	AccessToken string
	TokenType   string
	ExpiresAt   int64
	User        User
}

func (c *httpClient) login(ctx context.Context, username, password string) (*loginData, error) {
	resp, err := c.user.Login(ctx, &v1user.LoginRequest{
		Username: username,
		Password: password,
	}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	user := toUser(resp.GetUser())
	if user == nil {
		user = &User{}
	}
	return &loginData{
		AccessToken: resp.GetAccessToken(),
		TokenType:   resp.GetTokenType(),
		ExpiresAt:   resp.GetExpiresAt(),
		User:        *user,
	}, nil
}

func (c *httpClient) register(ctx context.Context, username, password, nickname string) (*User, error) {
	resp, err := c.user.Register(ctx, &v1user.RegisterRequest{
		Username: username,
		Password: password,
		Nickname: nickname,
	}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return toUser(resp), nil
}

func (c *httpClient) me(ctx context.Context) (*User, error) {
	resp, err := c.user.GetMe(ctx, &v1user.GetMeRequest{}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return toUser(resp), nil
}

func (c *httpClient) searchUsers(ctx context.Context, keyword string, limit int) ([]*User, error) {
	resp, err := c.user.SearchUsers(ctx, &v1user.SearchUsersRequest{
		Keyword: keyword,
		Limit:   int32(limit),
	}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return toUsers(resp.GetList()), nil
}

func (c *httpClient) batchUsers(ctx context.Context, ids []uint64) ([]*User, error) {
	resp, err := c.user.BatchGetUsers(ctx, &v1user.BatchGetUsersRequest{Ids: ids}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return toUsers(resp.GetList()), nil
}

func (c *httpClient) listConversations(ctx context.Context, limit int) ([]*Conversation, error) {
	resp, err := c.im.ListConversations(ctx, &v1im.ListConversationsRequest{Limit: int32(limit)}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return toConversations(resp.GetList()), nil
}

func (c *httpClient) openConversation(ctx context.Context, toUserID uint64) (*Conversation, error) {
	resp, err := c.im.OpenSingleConversation(ctx, &v1im.OpenSingleConversationRequest{
		ToUserId: toUserID,
	}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return toConversation(resp), nil
}

type pullResult struct {
	List    []*Message
	MaxSeq  int64
	HasMore bool
}

func (c *httpClient) pullMessages(ctx context.Context, conversationID uint64, beforeSeq, afterSeq int64, limit int) (*pullResult, error) {
	resp, err := c.im.PullMessages(ctx, &v1im.PullMessagesRequest{
		ConversationId: conversationID,
		BeforeSeq:      beforeSeq,
		AfterSeq:       afterSeq,
		Limit:          int32(limit),
	}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return &pullResult{
		List:    toMessages(resp.GetList()),
		MaxSeq:  resp.GetMaxSeq(),
		HasMore: resp.GetHasMore(),
	}, nil
}

type sendResult struct {
	Message    *Message
	Duplicated bool
	LastSeq    int64
}

func (c *httpClient) sendMessage(ctx context.Context, conversationID, toUserID uint64, clientMsgID, body string) (*sendResult, error) {
	resp, err := c.im.SendMessage(ctx, &v1im.SendMessageRequest{
		ConversationId: conversationID,
		ToUserId:       toUserID,
		ClientMsgId:    clientMsgID,
		Type:           v1im.MessageType_MESSAGE_TYPE_TEXT,
		Body:           body,
	}, jsonOpts...)
	if err != nil {
		return nil, err
	}
	return &sendResult{
		Message:    toMessage(resp.GetMessage()),
		Duplicated: resp.GetDuplicated(),
		LastSeq:    resp.GetLastSeq(),
	}, nil
}

func (c *httpClient) markRead(ctx context.Context, conversationID uint64, readSeq int64) error {
	_, err := c.im.MarkRead(ctx, &v1im.MarkReadRequest{
		ConversationId: conversationID,
		ReadSeq:        readSeq,
	}, jsonOpts...)
	return err
}

func textBody(text string) string {
	raw, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: text})
	return string(raw)
}

func wsURL(serverURL string, port int) string {
	u, err := url.Parse(normalizeBase(serverURL))
	if err != nil {
		return ""
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	host := u.Hostname()
	if port <= 0 {
		port = 8001
	}
	return fmt.Sprintf("%s://%s:%d/v1/im/ws", scheme, host, port)
}

func normalizeBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "http://127.0.0.1:8000"
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/")
}

func toUser(u *v1user.User) *User {
	if u == nil {
		return nil
	}
	return &User{ID: u.GetId(), Username: u.GetUsername(), Nickname: u.GetNickname(), Status: u.GetStatus()}
}

func toUsers(list []*v1user.User) []*User {
	out := make([]*User, 0, len(list))
	for _, u := range list {
		out = append(out, toUser(u))
	}
	return out
}

func toMessage(m *v1im.Message) *Message {
	if m == nil {
		return nil
	}
	return &Message{
		MsgID:          m.GetMsgId(),
		ConversationID: m.GetConversationId(),
		Seq:            m.GetSeq(),
		SenderID:       m.GetSenderId(),
		ClientMsgID:    m.GetClientMsgId(),
		Type:           int32(m.GetType()),
		Body:           m.GetBody(),
		Status:         m.GetStatus(),
		ReplyTo:        m.GetReplyTo(),
		Version:        m.GetVersion(),
		CreatedAt:      m.GetCreatedAt(),
	}
}

func toMessages(list []*v1im.Message) []*Message {
	out := make([]*Message, 0, len(list))
	for _, m := range list {
		out = append(out, toMessage(m))
	}
	return out
}

func toConversation(c *v1im.Conversation) *Conversation {
	if c == nil {
		return nil
	}
	return &Conversation{
		ID:          c.GetId(),
		Type:        int32(c.GetType()),
		LastSeq:     c.GetLastSeq(),
		ReadSeq:     c.GetReadSeq(),
		Unread:      c.GetUnreadCount(),
		Version:     c.GetVersion(),
		PeerReadSeq: c.GetPeerReadSeq(),
		PeerID:      c.GetPeerId(),
		LastMessage: toMessage(c.GetLastMessage()),
		Pinned:      c.GetPinned(),
		Muted:       c.GetMuted(),
		JoinedAt:    c.GetJoinedAt(),
	}
}

func toConversations(list []*v1im.Conversation) []*Conversation {
	out := make([]*Conversation, 0, len(list))
	for _, c := range list {
		out = append(out, toConversation(c))
	}
	return out
}
