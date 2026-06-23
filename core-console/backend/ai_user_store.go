// ai_user_store.go — per-operator AI state: conversation history + memory.
//
// Both are PRIVATE to each operator (keyed by username) and persisted atomically
// in one JSON file. Conversations let the console assistant list/resume past
// chats; memory is a small set of facts the agent can remember (via the
// remember/forget tools) and that get injected into its system prompt, so it
// carries context across conversations — a per-operator "memory system".
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const aiUserStorePath = authConfigDir + "/ai-user-store.json"

const (
	maxConversationsPerOp = 50
	maxMessagesPerConvo   = 200
	maxMemoryPerOp        = 100
	maxMemoryTextLen      = 500
)

type aiConversation struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Messages  []aiMsg `json:"messages"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt int64   `json:"updated_at"`
}

type aiMemoryItem struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	CreatedAt int64  `json:"created_at"`
}

type aiUserData struct {
	Conversations []*aiConversation `json:"conversations,omitempty"`
	Memory        []*aiMemoryItem   `json:"memory,omitempty"`
}

type aiUserStore struct {
	mu    sync.Mutex
	users map[string]*aiUserData
	path  string
}

var globalAIUsers *aiUserStore

func shortID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newAIUserStore() *aiUserStore {
	s := &aiUserStore{users: map[string]*aiUserData{}, path: aiUserStorePath}
	loadedFromDB := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("ai_user_store"); err != nil {
			log.Printf("ai-user-store: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			if json.Unmarshal(doc, &s.users) == nil {
				loadedFromDB = true
			}
		}
	}
	if !loadedFromDB {
		if b, err := os.ReadFile(aiUserStorePath); err == nil && len(b) > 0 {
			_ = json.Unmarshal(b, &s.users)
		}
	}
	if s.users == nil {
		s.users = map[string]*aiUserData{}
	}
	if globalDB != nil && !loadedFromDB {
		s.mu.Lock()
		_ = s.persistLocked() // best-effort migrate file→DB
		s.mu.Unlock()
	}
	return s
}

func (s *aiUserStore) persistLocked() error {
	b, err := json.MarshalIndent(s.users, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if globalDB != nil {
		if err := saveConfigDoc("ai_user_store", b); err != nil {
			log.Printf("ai-user-store: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// userLocked returns (creating) the per-operator bucket. Caller holds s.mu.
func (s *aiUserStore) userLocked(op string) *aiUserData {
	u := s.users[op]
	if u == nil {
		u = &aiUserData{}
		s.users[op] = u
	}
	return u
}

// ── conversations ────────────────────────────────────────────────────────────

// convMeta is the lightweight list entry (no messages).
type convMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *aiUserStore) listConversations(op string) []convMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[op]
	if u == nil {
		return []convMeta{}
	}
	out := make([]convMeta, 0, len(u.Conversations))
	for _, c := range u.Conversations {
		out = append(out, convMeta{ID: c.ID, Title: c.Title, UpdatedAt: c.UpdatedAt})
	}
	// newest first
	for i := 0; i < len(out)/2; i++ {
		out[i], out[len(out)-1-i] = out[len(out)-1-i], out[i]
	}
	return out
}

func (s *aiUserStore) getConversation(op, id string) *aiConversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[op]
	if u == nil {
		return nil
	}
	for _, c := range u.Conversations {
		if c.ID == id {
			cp := *c
			cp.Messages = append([]aiMsg(nil), c.Messages...)
			return &cp
		}
	}
	return nil
}

func deriveTitle(msgs []aiMsg) string {
	for _, m := range msgs {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			t := strings.TrimSpace(m.Content)
			if len([]rune(t)) > 48 {
				t = string([]rune(t)[:48]) + "…"
			}
			return t
		}
	}
	return "(untitled)"
}

// saveConversation upserts a conversation by id (empty id → new), trims message
// history, refreshes the title/timestamp, and caps the per-op list.
func (s *aiUserStore) saveConversation(op, id string, msgs []aiMsg) (string, error) {
	now := time.Now().Unix()
	if len(msgs) > maxMessagesPerConvo {
		msgs = msgs[len(msgs)-maxMessagesPerConvo:]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.userLocked(op)
	if id != "" {
		for _, c := range u.Conversations {
			if c.ID == id {
				c.Messages = msgs
				c.Title = deriveTitle(msgs)
				c.UpdatedAt = now
				return id, s.persistLocked()
			}
		}
	}
	id = shortID()
	u.Conversations = append(u.Conversations, &aiConversation{
		ID: id, Title: deriveTitle(msgs), Messages: msgs, CreatedAt: now, UpdatedAt: now,
	})
	if len(u.Conversations) > maxConversationsPerOp {
		u.Conversations = u.Conversations[len(u.Conversations)-maxConversationsPerOp:]
	}
	return id, s.persistLocked()
}

func (s *aiUserStore) deleteConversation(op, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[op]
	if u == nil {
		return nil
	}
	kept := u.Conversations[:0:0]
	for _, c := range u.Conversations {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	u.Conversations = kept
	return s.persistLocked()
}

// ── memory ───────────────────────────────────────────────────────────────────

func (s *aiUserStore) listMemory(op string) []*aiMemoryItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[op]
	if u == nil {
		return []*aiMemoryItem{}
	}
	return append([]*aiMemoryItem(nil), u.Memory...)
}

// addMemory stores a fact (deduped by exact text, capped). Returns the item.
func (s *aiUserStore) addMemory(op, text string) (*aiMemoryItem, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	if len([]rune(text)) > maxMemoryTextLen {
		text = string([]rune(text)[:maxMemoryTextLen])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.userLocked(op)
	for _, m := range u.Memory {
		if m.Text == text {
			return m, nil // already known
		}
	}
	it := &aiMemoryItem{ID: shortID(), Text: text, CreatedAt: time.Now().Unix()}
	u.Memory = append(u.Memory, it)
	if len(u.Memory) > maxMemoryPerOp {
		u.Memory = u.Memory[len(u.Memory)-maxMemoryPerOp:]
	}
	return it, s.persistLocked()
}

// deleteMemory removes by id OR by substring match (whichever matches). Returns
// the number removed.
func (s *aiUserStore) deleteMemory(op, idOrText string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.users[op]
	if u == nil {
		return 0
	}
	q := strings.TrimSpace(idOrText)
	kept := u.Memory[:0:0]
	removed := 0
	for _, m := range u.Memory {
		if m.ID == q || (q != "" && strings.Contains(strings.ToLower(m.Text), strings.ToLower(q))) {
			removed++
			continue
		}
		kept = append(kept, m)
	}
	u.Memory = kept
	if removed > 0 {
		_ = s.persistLocked()
	}
	return removed
}

// memoryPrompt renders the operator's memory for injection into a system prompt.
func (s *aiUserStore) memoryPrompt(op string) string {
	items := s.listMemory(op)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Operator memory (facts you've saved for this operator — use them; update via the remember/forget tools):\n")
	for _, m := range items {
		b.WriteString("- " + m.Text + "\n")
	}
	return b.String()
}

// aiMemoryFor is the package-level resolver used when building system prompts.
func aiMemoryFor(op string) string {
	if globalAIUsers == nil {
		return ""
	}
	return globalAIUsers.memoryPrompt(op)
}
