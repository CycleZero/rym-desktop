package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Store 是本地会话/消息缓存。
//
// MVP 用单个 JSON 文件（原子替换）；下一步换 SQLite 时只替换本文件。
type Store struct {
	mu   sync.Mutex
	path string
	data StoreData
}

// NewStore 打开（或创建）本地缓存。
func NewStore(path string) *Store {
	s := &Store{path: path}
	s.data = StoreData{
		Conversations: map[uint64]*Conversation{},
		Messages:      map[uint64][]*Message{},
	}
	_ = s.load()
	return s
}

// DefaultStorePath 返回平台配置目录下的 store.json。
func DefaultStorePath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "rym-desktop", "store.json")
	}
	return "rym-desktop-store.json"
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var data StoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if data.Conversations == nil {
		data.Conversations = map[uint64]*Conversation{}
	}
	if data.Messages == nil {
		data.Messages = map[uint64][]*Message{}
	}
	s.data = data
	return nil
}

// Save 原子写入磁盘。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Session 返回当前登录态副本。
func (s *Store) Session() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Session == nil {
		return nil
	}
	cp := *s.data.Session
	return &cp
}

func (s *Store) SetSession(session *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session == nil {
		s.data.Session = nil
	} else {
		cp := *session
		s.data.Session = &cp
	}
	_ = s.saveLocked()
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = StoreData{
		Conversations: map[uint64]*Conversation{},
		Messages:      map[uint64][]*Message{},
	}
	_ = s.saveLocked()
}

// Conversations 返回按最后消息时间倒序的会话副本。
func (s *Store) Conversations() []*Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Conversation, 0, len(s.data.Conversations))
	for _, c := range s.data.Conversations {
		out = append(out, cloneConversation(c))
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := "", ""
		if out[i].LastMessage != nil {
			ti = out[i].LastMessage.CreatedAt
		}
		if out[j].LastMessage != nil {
			tj = out[j].LastMessage.CreatedAt
		}
		if ti != tj {
			return ti > tj
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// Conversation 返回单个会话副本。
func (s *Store) Conversation(id uint64) *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneConversation(s.data.Conversations[id])
}

// UpsertConversations 合并服务端会话摘要。
func (s *Store) UpsertConversations(list []*Conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, incoming := range list {
		if incoming == nil {
			continue
		}
		existing := s.data.Conversations[incoming.ID]
		if existing == nil {
			s.data.Conversations[incoming.ID] = cloneConversation(incoming)
			continue
		}
		existing.Type = incoming.Type
		existing.LastSeq = maxInt64(existing.LastSeq, incoming.LastSeq)
		existing.ReadSeq = maxInt64(existing.ReadSeq, incoming.ReadSeq)
		existing.Unread = incoming.Unread
		existing.Version = incoming.Version
		existing.PeerReadSeq = maxInt64(existing.PeerReadSeq, incoming.PeerReadSeq)
		existing.PeerID = incoming.PeerID
		existing.Pinned = incoming.Pinned
		existing.Muted = incoming.Muted
		existing.JoinedAt = incoming.JoinedAt
		if incoming.Peer != nil {
			existing.Peer = incoming.Peer
		}
		if incoming.LastMessage != nil {
			existing.LastMessage = cloneMessage(incoming.LastMessage)
		}
	}
	_ = s.saveLocked()
}

// Messages 返回会话消息副本（按 seq 升序）。
func (s *Store) Messages(conversationID uint64) []*Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := s.data.Messages[conversationID]
	out := make([]*Message, 0, len(raw))
	for _, m := range raw {
		out = append(out, cloneMessage(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// AddMessage 去重后插入消息，并更新会话最后一条/本地最大 seq。
func (s *Store) AddMessage(msg *Message) {
	if msg == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	list := s.data.Messages[msg.ConversationID]
	for _, m := range list {
		if m.MsgID == msg.MsgID {
			return
		}
	}
	list = append(list, cloneMessage(msg))
	sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
	s.data.Messages[msg.ConversationID] = list

	c := s.data.Conversations[msg.ConversationID]
	if c == nil {
		c = &Conversation{ID: msg.ConversationID, PeerID: 0}
		s.data.Conversations[msg.ConversationID] = c
	}
	c.LastSeq = maxInt64(c.LastSeq, msg.Seq)
	if c.LastMessage == nil || msg.Seq >= c.LastMessage.Seq {
		c.LastMessage = cloneMessage(msg)
	}
	_ = s.saveLocked()
}

// MarkUnreadZero 清空本地未读（前端已读到最新）。
func (s *Store) MarkUnreadZero(conversationID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.data.Conversations[conversationID]; c != nil {
		c.Unread = 0
		_ = s.saveLocked()
	}
}

// SetRead 更新已读位与未读（近似值，服务端会话列表可校准）。
func (s *Store) SetRead(conversationID uint64, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.data.Conversations[conversationID]
	if c == nil {
		return
	}
	c.ReadSeq = maxInt64(c.ReadSeq, seq)
	if c.ReadSeq >= c.LastSeq {
		c.Unread = 0
	} else {
		c.Unread = c.LastSeq - c.ReadSeq
	}
	_ = s.saveLocked()
}

// SetPeerRead 更新对端已读位。
func (s *Store) SetPeerRead(conversationID uint64, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.data.Conversations[conversationID]; c != nil {
		c.PeerReadSeq = maxInt64(c.PeerReadSeq, seq)
		_ = s.saveLocked()
	}
}

func (s *Store) IncrementUnread(conversationID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.data.Conversations[conversationID]; c != nil {
		c.Unread++
		_ = s.saveLocked()
	}
}

func cloneConversation(c *Conversation) *Conversation {
	if c == nil {
		return nil
	}
	cp := *c
	if c.Peer != nil {
		p := *c.Peer
		cp.Peer = &p
	}
	cp.LastMessage = cloneMessage(c.LastMessage)
	return &cp
}

func cloneMessage(m *Message) *Message {
	if m == nil {
		return nil
	}
	cp := *m
	return &cp
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// LastSeq 返回本地已连续收到（或拉取到）的最大 seq。
func (s *Store) LastSeq(conversationID uint64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out int64
	for _, m := range s.data.Messages[conversationID] {
		if m.Seq > out {
			out = m.Seq
		}
	}
	return out
}
