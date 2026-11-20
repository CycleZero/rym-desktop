package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// 发送队列的执行侧：把 outbox 里的消息按顺序发出去。
//
// 设计要点：
//   - 单飞：同一时刻只有一个 goroutine 在发，避免同会话消息乱序拿到 seq；
//   - 退避：网络错误 / 429 / 5xx 指数退避（1s 起，封顶 30s），复用 client_msg_id；
//   - 4xx：标记 failed，不再自动重试（参数/权限问题重试也不会成功）；
//   - 401：保留队列并停下，等重新登录后继续（不能把消息标死）；
//   - 停止：Close/Logout 取消退避等待，队列留在库里，下次登录或 WS 重连继续。
const (
	outboxBatchSize = 20
	sendTimeout     = 15 * time.Second
)

// pendingSender 发送一条队列消息；默认走 HTTP，测试可替换。
type pendingSender func(item *OutboxMessage) (*Message, error)

func (c *Client) sendPendingHTTP(item *OutboxMessage) (*Message, error) {
	c.mu.Lock()
	hc := c.http
	c.mu.Unlock()
	if hc == nil {
		return nil, errors.New("未登录")
	}
	// 队列里没有调用方的 ctx（可能是重启后补发），只能自己给超时。
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()

	res, err := hc.sendMessage(ctx, item.ConversationID, 0, item.ClientMsgID, item.Body)
	if err != nil {
		return nil, err
	}
	if res.Message == nil {
		return nil, errors.New("服务端未返回消息")
	}
	return res.Message, nil
}

func (c *Client) flushOutbox() {
	c.outboxMu.Lock()
	if c.outboxRunning {
		c.outboxMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.outboxRunning = true
	c.outboxStop = cancel
	c.outboxMu.Unlock()

	defer func() {
		cancel()
		c.outboxMu.Lock()
		c.outboxRunning = false
		c.outboxStop = nil
		c.outboxMu.Unlock()
	}()

	for ctx.Err() == nil {
		if !c.loggedIn() {
			return
		}

		items := c.store.DueOutbox(time.Now().UnixMilli(), outboxBatchSize)
		if len(items) == 0 {
			next, ok := c.store.OutboxNextAttempt()
			if !ok {
				return // 队列空了，或者剩下的都是失败态
			}
			wait := time.Until(time.UnixMilli(next))
			if wait < 50*time.Millisecond {
				wait = 50 * time.Millisecond // 时钟粒度可能导致 next 刚过期，避免忙等
			}
			if !waitOrStop(ctx, wait) {
				return
			}
			continue
		}

		var retryDelay time.Duration
		for _, item := range items {
			msg, err := c.sendPending(item)
			if err == nil && msg == nil {
				err = errors.New("服务端未返回消息")
			}
			switch {
			case err == nil:
				c.store.ResolveOutbox(item.ClientMsgID, msg)
				c.store.MarkUnreadZero(item.ConversationID)
				c.emitOutboxEvent("message.sent", item, msg, "")

			case errors.Is(err, ErrUnauthorized):
				return // token 失效：等重新登录，别把消息标成失败

			case !retryableSendError(err):
				c.store.FailOutbox(item.ClientMsgID, err.Error())
				c.emitOutboxEvent("message.failed", item, nil, err.Error())

			default:
				attempts := item.Attempts + 1
				delay := c.outboxBackoff(attempts)
				c.store.RetryOutbox(item.ClientMsgID, attempts, time.Now().Add(delay).UnixMilli(), err.Error())
				retryDelay = delay
			}
			if retryDelay > 0 {
				// 同一会话必须按创建顺序发送：前面的没成功，后面的不能抢先拿 seq。
				break
			}
		}
		if retryDelay > 0 && !waitOrStop(ctx, retryDelay) {
			return
		}
	}
}

// stopOutbox 中断退避等待（Close/Logout）；队列本身不动。
func (c *Client) stopOutbox() {
	c.outboxMu.Lock()
	cancel := c.outboxStop
	c.outboxMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Client) outboxBackoff(attempts int32) time.Duration {
	base := c.outboxBase
	if base <= 0 {
		base = time.Second
	}
	maxDelay := c.outboxMax
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	if attempts < 1 {
		attempts = 1
	}
	delay := base
	for i := int32(1); i < attempts && delay < maxDelay; i++ {
		delay *= 2
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

// retryableSendError 判断是否值得重试：网络层错误、429、5xx 可重试；
// 其它 4xx（参数/权限/不存在）重试也不会成功。
func retryableSendError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == http.StatusTooManyRequests {
			return true
		}
		return apiErr.Status >= http.StatusInternalServerError
	}
	return true
}

func (c *Client) emitOutboxEvent(typ string, item *OutboxMessage, msg *Message, cause string) {
	data, err := json.Marshal(MessageSentData{
		ConversationID: item.ConversationID,
		ClientMsgID:    item.ClientMsgID,
		Message:        msg,
		Error:          cause,
	})
	if err != nil {
		return
	}
	c.emitEvent(Event{V: 1, Type: typ, Data: data})
}

func (c *Client) loggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.http != nil && c.session != nil
}

func waitOrStop(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
