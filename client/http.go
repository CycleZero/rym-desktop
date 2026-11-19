package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
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

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type httpClient struct {
	base  string
	token string
	hc    *http.Client
}

func newHTTPClient(base string) *httpClient {
	return &httpClient{
		base: normalizeBase(base),
		hc: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
	}
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

func (c *httpClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var env envelope
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &env)
	}
	if resp.StatusCode != http.StatusOK || env.Code != 0 {
		if resp.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		msg := env.Message
		if msg == "" {
			msg = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Code: env.Code, Message: msg}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return err
		}
	}
	return nil
}

type loginData struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresAt   int64  `json:"expires_at"`
	User        User   `json:"user"`
}

func (c *httpClient) login(ctx context.Context, username, password string) (*loginData, error) {
	var out loginData
	err := c.do(ctx, http.MethodPost, "/v1/users/login", map[string]any{
		"username": username,
		"password": password,
	}, &out)
	return &out, err
}

func (c *httpClient) register(ctx context.Context, username, password, nickname string) (*User, error) {
	var out User
	err := c.do(ctx, http.MethodPost, "/v1/users/register", map[string]any{
		"username": username,
		"password": password,
		"nickname": nickname,
	}, &out)
	return &out, err
}

func (c *httpClient) me(ctx context.Context) (*User, error) {
	var out User
	err := c.do(ctx, http.MethodGet, "/v1/users/me", nil, &out)
	return &out, err
}

func (c *httpClient) searchUsers(ctx context.Context, keyword string, limit int) ([]*User, error) {
	q := url.Values{}
	q.Set("keyword", keyword)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		List []*User `json:"list"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/users/search?"+q.Encode(), nil, &out)
	return out.List, err
}

func (c *httpClient) batchUsers(ctx context.Context, ids []uint64) ([]*User, error) {
	var out struct {
		List []*User `json:"list"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/users/batch", map[string]any{"ids": ids}, &out)
	return out.List, err
}

func (c *httpClient) listConversations(ctx context.Context, limit int) ([]*Conversation, error) {
	path := "/v1/im/conversations"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out struct {
		List []*Conversation `json:"list"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.List, err
}

func (c *httpClient) openConversation(ctx context.Context, toUserID uint64) (*Conversation, error) {
	var out Conversation
	err := c.do(ctx, http.MethodPost, "/v1/im/conversations", map[string]any{"to_user_id": toUserID}, &out)
	return &out, err
}

type pullResult struct {
	List    []*Message `json:"list"`
	MaxSeq  int64      `json:"max_seq"`
	HasMore bool       `json:"has_more"`
}

func (c *httpClient) pullMessages(ctx context.Context, conversationID uint64, beforeSeq, afterSeq int64, limit int) (*pullResult, error) {
	q := url.Values{}
	q.Set("conversation_id", strconv.FormatUint(conversationID, 10))
	if beforeSeq > 0 {
		q.Set("before_seq", strconv.FormatInt(beforeSeq, 10))
	}
	if afterSeq > 0 {
		q.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out pullResult
	err := c.do(ctx, http.MethodGet, "/v1/im/messages?"+q.Encode(), nil, &out)
	return &out, err
}

type sendResult struct {
	Message    *Message `json:"message"`
	Duplicated bool     `json:"duplicated"`
	LastSeq    int64    `json:"last_seq"`
}

func (c *httpClient) sendMessage(ctx context.Context, conversationID, toUserID uint64, clientMsgID, body string) (*sendResult, error) {
	payload := map[string]any{
		"client_msg_id": clientMsgID,
		"type":          1,
		"body":          body,
	}
	if conversationID > 0 {
		payload["conversation_id"] = conversationID
	} else {
		payload["to_user_id"] = toUserID
	}
	var out sendResult
	err := c.do(ctx, http.MethodPost, "/v1/im/messages", payload, &out)
	return &out, err
}

func (c *httpClient) markRead(ctx context.Context, conversationID uint64, readSeq int64) error {
	return c.do(ctx, http.MethodPost,
		fmt.Sprintf("/v1/im/conversations/%d/read", conversationID),
		map[string]any{"read_seq": readSeq}, nil)
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
