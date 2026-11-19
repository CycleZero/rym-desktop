package client

import (
	"context"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// WSClient 是带重连的推送连接（只收事件）。
type WSClient struct {
	url     string
	token   string
	onEvent func(Event)
	onState func(ConnectionState)

	mu     sync.Mutex
	conn   *websocket.Conn
	cancel context.CancelFunc
	done   chan struct{}
}

func newWSClient(url, token string, onEvent func(Event), onState func(ConnectionState)) *WSClient {
	return &WSClient{url: url, token: token, onEvent: onEvent, onState: onState}
}

func (w *WSClient) Start(parent context.Context) {
	w.mu.Lock()
	if w.cancel != nil {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.done = make(chan struct{})
	w.mu.Unlock()

	go func() {
		defer close(w.done)
		w.loop(ctx)
	}()
}

func (w *WSClient) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	conn := w.conn
	done := w.done
	w.cancel = nil
	w.conn = nil
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.CloseNow()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (w *WSClient) loop(ctx context.Context) {
	backoff := 500 * time.Millisecond

	for ctx.Err() == nil {
		conn, resp, err := websocket.Dial(ctx, w.url, &websocket.DialOptions{
			HTTPClient: &http.Client{Transport: &http.Transport{Proxy: nil}},
			HTTPHeader: http.Header{"Authorization": []string{"Bearer " + w.token}},
		})
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				w.emitState(ConnectionState{Connected: false, Reason: "unauthorized"})
				return
			}
			w.emitState(ConnectionState{Connected: false, Reason: err.Error()})
			if !sleepJitter(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		conn.SetReadLimit(1 << 20)
		w.mu.Lock()
		w.conn = conn
		w.mu.Unlock()
		w.emitState(ConnectionState{Connected: true})
		connectedAt := time.Now()

		err = w.readLoop(ctx, conn)
		_ = conn.CloseNow()
		w.mu.Lock()
		w.conn = nil
		w.mu.Unlock()

		if ctx.Err() != nil {
			return
		}
		if time.Since(connectedAt) > 30*time.Second {
			backoff = 500 * time.Millisecond
		}
		reason := "连接断开"
		if err != nil {
			reason = err.Error()
		}
		w.emitState(ConnectionState{Connected: false, Reason: reason})
		if !sleepJitter(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (w *WSClient) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		var event Event
		if err := wsjson.Read(ctx, conn, &event); err != nil {
			return err
		}
		if event.Type == "" {
			continue
		}
		if w.onEvent != nil {
			w.onEvent(event)
		}
	}
}

func (w *WSClient) emitState(state ConnectionState) {
	if w.onState != nil {
		w.onState(state)
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > 30*time.Second {
		next = 30 * time.Second
	}
	return next
}

// sleepJitter 使用 full jitter，避免服务重启时的重连风暴。
func sleepJitter(ctx context.Context, d time.Duration) bool {
	delay := time.Duration(float64(d) * (0.5 + rand.Float64()*0.5))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
