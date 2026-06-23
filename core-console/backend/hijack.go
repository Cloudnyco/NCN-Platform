// hijack.go — prefix-hijack / anomalous-origin detection. Complements rpki.go:
// where rpki.go checks that OUR prefixes are RPKI-valid, this watches whether
// SOMEONE ELSE is announcing our address space (or a more-specific of it) with
// an origin AS that isn't ours. Streams live BGP UPDATEs from RIPE RIS Live
// (websocket) filtered to our prefixes; alerts the ops group on a suspect
// origin. Pure observation — touches no router.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// RIS Live websocket. `client` is a courtesy identifier per their API docs.
const risLiveURL = "wss://ris-live.ripe.net/v1/ws/?client=ncn-console"

// Re-alert the same (prefix, origin) at most this often, so a persistent
// mis-origination doesn't spam the channel every time RIS relays the update.
const hijackReAlertEvery = 6 * time.Hour

var globalHijack *hijackMonitor

type hijackEvent struct {
	Prefix string `json:"prefix"`
	Origin string `json:"origin"`  // "AS####" — the suspect origin
	Peer   string `json:"peer"`    // RIS collector peer that saw it
	ASPath string `json:"as_path"` // full path, for triage
	SeenAt int64  `json:"seen_at"`
}

type hijackState struct {
	Connected bool          `json:"connected"`
	Watching  []string      `json:"watching"`        // our prefixes being subscribed
	Events    []hijackEvent `json:"events"`          // newest first, capped
	CheckedAt int64         `json:"checked_at"`
	Err       string        `json:"error,omitempty"`
}

type hijackMonitor struct {
	asn        string // numeric, no "AS"
	notify     *tgNotifier
	prefixesFn func(context.Context) ([]string, error) // source of our prefixes (rpki.go announced)
	dialer     *websocket.Dialer

	mu    sync.RWMutex
	state hijackState
	seen  map[string]time.Time // dedup: "prefix|origin" → last alert
}

func newHijackMonitor(asn string, notify *tgNotifier, prefixesFn func(context.Context) ([]string, error)) *hijackMonitor {
	asn = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(asn)), "AS")
	if asn == "" {
		asn = "64500"
	}
	return &hijackMonitor{
		asn:        asn,
		notify:     notify,
		prefixesFn: prefixesFn,
		dialer:     &websocket.Dialer{HandshakeTimeout: 15 * time.Second},
		seen:       map[string]time.Time{},
		// Non-nil so the JSON snapshot is [] not null (a null would crash the
		// dashboard card's .length/.slice before the first session populates them).
		state: hijackState{Watching: []string{}, Events: []hijackEvent{}},
	}
}

func (m *hijackMonitor) Start(ctx context.Context) { go m.run(ctx) }

// run keeps a RIS Live session alive, reconnecting with capped exponential
// backoff. A session that stayed up a while resets the backoff.
func (m *hijackMonitor) run(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := m.session(ctx)
		m.mu.Lock()
		m.state.Connected = false
		if err != nil && ctx.Err() == nil {
			m.state.Err = err.Error()
		}
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Printf("hijack: ris-live session ended: %v", err)
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second // long-lived session → reset
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (m *hijackMonitor) session(ctx context.Context) error {
	prefixes, err := m.prefixesFn(ctx)
	if err != nil {
		return fmt.Errorf("fetch our prefixes: %w", err)
	}
	if len(prefixes) == 0 {
		return errors.New("no announced prefixes to watch")
	}

	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, _, err := m.dialer.DialContext(dctx, risLiveURL, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial ris-live: %w", err)
	}
	defer conn.Close()
	// Unblock ReadMessage on shutdown by closing the conn when ctx is done.
	go func() { <-ctx.Done(); conn.Close() }()

	// One subscription per prefix, moreSpecific so sub-prefix hijacks are caught.
	for _, p := range prefixes {
		sub := map[string]any{"type": "ris_subscribe", "data": map[string]any{
			"prefix": p, "moreSpecific": true, "type": "UPDATE",
		}}
		if err := conn.WriteJSON(sub); err != nil {
			return fmt.Errorf("subscribe %s: %w", p, err)
		}
	}

	m.mu.Lock()
	m.state.Connected = true
	m.state.Watching = prefixes
	m.state.Err = ""
	m.state.CheckedAt = time.Now().Unix()
	m.mu.Unlock()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		m.handleMessage(raw)
	}
}

func (m *hijackMonitor) handleMessage(raw []byte) {
	var msg struct {
		Type string `json:"type"`
		Data struct {
			Peer          string `json:"peer"`
			Path          []any  `json:"path"`
			Announcements []struct {
				Prefixes []string `json:"prefixes"`
			} `json:"announcements"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || msg.Type != "ris_message" {
		return
	}
	origin := originOf(msg.Data.Path)
	// Empty origin (no usable path) or our own announcement → not a hijack.
	if origin == "" || origin == m.asn {
		return
	}
	for _, ann := range msg.Data.Announcements {
		for _, pfx := range ann.Prefixes {
			m.flag(pfx, origin, msg.Data.Peer, msg.Data.Path)
		}
	}
}

// originOf returns the last AS in the path as a decimal string. AS_SETs are
// encoded as nested arrays; take the set's last element.
func originOf(path []any) string {
	if len(path) == 0 {
		return ""
	}
	switch v := path[len(path)-1].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case []any:
		if len(v) > 0 {
			if f, ok := v[len(v)-1].(float64); ok {
				return strconv.FormatInt(int64(f), 10)
			}
		}
	}
	return ""
}

func pathString(path []any) string {
	parts := make([]string, 0, len(path))
	for _, h := range path {
		switch v := h.(type) {
		case float64:
			parts = append(parts, strconv.FormatInt(int64(v), 10))
		case []any:
			set := make([]string, 0, len(v))
			for _, e := range v {
				if f, ok := e.(float64); ok {
					set = append(set, strconv.FormatInt(int64(f), 10))
				}
			}
			parts = append(parts, "{"+strings.Join(set, ",")+"}")
		}
	}
	return strings.Join(parts, " ")
}

func (m *hijackMonitor) flag(prefix, origin, peer string, path []any) {
	key := prefix + "|" + origin
	now := time.Now()
	ev := hijackEvent{Prefix: prefix, Origin: "AS" + origin, Peer: peer, ASPath: pathString(path), SeenAt: now.Unix()}

	m.mu.Lock()
	last, seen := m.seen[key]
	m.seen[key] = now
	if len(m.seen) > 2000 { // prune stale dedup keys
		for k, t := range m.seen {
			if now.Sub(t) > 2*hijackReAlertEvery {
				delete(m.seen, k)
			}
		}
	}
	m.state.Events = append([]hijackEvent{ev}, m.state.Events...)
	if len(m.state.Events) > 50 {
		m.state.Events = m.state.Events[:50]
	}
	m.mu.Unlock()

	if seen && now.Sub(last) < hijackReAlertEvery {
		return // already alerted recently for this (prefix, origin)
	}
	m.alert(ev)
}

func (m *hijackMonitor) alert(ev hijackEvent) {
	if m.notify == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🚨 <b>BGP 劫持嫌疑</b> — AS%s 地址空间被他方宣告\n", m.asn)
	fmt.Fprintf(&b, "⛔ <code>%s</code> origin <b>%s</b>\n", html.EscapeString(ev.Prefix), html.EscapeString(ev.Origin))
	fmt.Fprintf(&b, "AS_PATH: <code>%s</code>\nRIS peer: %s", html.EscapeString(ev.ASPath), html.EscapeString(ev.Peer))
	fmt.Fprintf(&b, "\n<blockquote>有人在 BGP 中宣告我方前缀或其 more-specific,origin 非 AS%s。核实是否为误配置或劫持。</blockquote>", m.asn)
	channel := m.notify.errorChat
	if channel == "" {
		channel = m.notify.chatID
	}
	m.notify.enqueue(tgPayload{ChatID: channel, Text: b.String()}, "hijack-alert")
}

func (m *hijackMonitor) snapshot() hijackState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// GET /api/v1/auth/hijack → live hijack-detection state (connection + recent suspects).
func handleHijack(w http.ResponseWriter, r *http.Request) {
	if globalHijack == nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "hijack monitor not ready"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: globalHijack.snapshot()})
}
