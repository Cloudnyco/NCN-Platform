// Tests for the Telegram-bot operator-identity layer. STRICTLY pure / in-memory
// — they construct an authStore by hand (no loadAuthStore, no persist()) and
// never touch the real operators.json / nodes.json / live fleet.

package main

import "testing"

// newMemStore builds an authStore backed only by an in-memory map. It never
// reads or writes /etc/ncn-core-console — safe to run anywhere, including tyo.
func newMemStore(ops ...operatorRecord) *authStore {
	m := map[string]operatorRecord{}
	for _, op := range ops {
		m[op.Username] = op
	}
	return &authStore{operators: m}
}

func tgIdentity(sub string) externalIdentity {
	return externalIdentity{Provider: "telegram", Subject: sub}
}

func TestResolveOperator(t *testing.T) {
	store := newMemStore(
		operatorRecord{Username: "alice", Approved: true, ExternalIdentities: []externalIdentity{tgIdentity("111")}},
		operatorRecord{Username: "bob", Approved: false, ExternalIdentities: []externalIdentity{tgIdentity("222")}},
	)

	cases := []struct {
		name   string
		auth   *authStore
		id     int64
		wantOp string
		wantOK bool
	}{
		{"bound+approved", store, 111, "alice", true},
		{"bound+unapproved", store, 222, "", false},
		{"unbound", store, 999, "", false},
		{"zero id", store, 0, "", false},
		{"nil auth", nil, 111, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := &tgNotifier{auth: c.auth}
			op, ok := n.resolveOperator(c.id)
			if op != c.wantOp || ok != c.wantOK {
				t.Fatalf("resolveOperator(%d) = (%q,%v), want (%q,%v)", c.id, op, ok, c.wantOp, c.wantOK)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	store := newMemStore(
		operatorRecord{Username: "alice", BotNick: "老王"},
		operatorRecord{Username: "bob"},
	)
	n := &tgNotifier{auth: store}

	cases := []struct {
		name       string
		op         string
		tgUsername string
		want       string
	}{
		{"custom nick wins", "alice", "alice_tg", "老王"},
		{"falls back to @username", "bob", "bob_tg", "@bob_tg"},
		{"falls back to account when no username", "bob", "", "bob"},
		{"unknown operator → @username", "carol", "carol_tg", "@carol_tg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := n.displayName(c.op, c.tgUsername); got != c.want {
				t.Fatalf("displayName(%q,%q) = %q, want %q", c.op, c.tgUsername, got, c.want)
			}
		})
	}

	// nil auth: no nick lookup possible → @username / account fallback.
	nNil := &tgNotifier{}
	if got := nNil.displayName("alice", "alice_tg"); got != "@alice_tg" {
		t.Fatalf("nil-auth displayName = %q, want @alice_tg", got)
	}
}

func TestNormalizeBotNick(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "字"
	}
	cases := []struct {
		in, want string
	}{
		{"  老王  ", "老王"},
		{"", ""},
		{"a\x00b\x7fc", "abc"},          // control chars stripped
		{"line\nbreak", "linebreak"},    // newline is a control char
		{long, string([]rune(long)[:32])}, // capped at 32 runes
	}
	for _, c := range cases {
		if got := normalizeBotNick(c.in); got != c.want {
			t.Fatalf("normalizeBotNick(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if rs := []rune(normalizeBotNick(long)); len(rs) != 32 {
		t.Fatalf("capped nick len = %d, want 32", len(rs))
	}
}

// TestPendKeyIsolation verifies two operators in the same chat get distinct
// pending-confirm keys, so one can't confirm the other's DELETE/APPLY MESH.
func TestPendKeyIsolation(t *testing.T) {
	chat := "-100123"
	a := pendKey(chat, 111)
	b := pendKey(chat, 222)
	if a == b {
		t.Fatalf("pendKey collided for distinct users: %q", a)
	}
	if pendKey(chat, 111) != a {
		t.Fatalf("pendKey not stable for same (chat,user)")
	}
}
