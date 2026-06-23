package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidWikiPath(t *testing.T) {
	ok := []string{"home", "public/network", "ops/systems/ha", "ops/runbooks/pitr-restore", "a", "x_y-z/1"}
	bad := []string{
		"", "/leading", "trailing/", "ops//double", "../etc/passwd", "ops/../secret",
		"Ops/Caps", "with space", "has?query", "ops/系统", strings.Repeat("a", 200),
	}
	for _, p := range ok {
		if !validWikiPath(p) {
			t.Errorf("expected VALID: %q", p)
		}
	}
	for _, p := range bad {
		if validWikiPath(p) {
			t.Errorf("expected INVALID: %q", p)
		}
	}
}

func TestWikiSnippet(t *testing.T) {
	body := "The quick brown fox jumps over the lazy dog and then keeps running for a while."
	s := wikiSnippet(body, "fox")
	if !strings.Contains(s, "fox") {
		t.Errorf("snippet should contain the match: %q", s)
	}
	// no match → returns a head slice (capped), never panics
	s2 := wikiSnippet(body, "zzzznotpresent")
	if s2 == "" {
		t.Errorf("snippet should be non-empty even without a match")
	}
	// empty content safe
	if got := wikiSnippet("", "x"); got != "" {
		t.Errorf("empty content should give empty snippet, got %q", got)
	}
	// CJK: byte-slicing used to cut 3-byte runes in half → U+FFFD. The snippet
	// must always be valid UTF-8 and contain the match, for both a CJK query
	// and an ASCII query whose context window lands inside CJK text.
	cjk := "Acme Net 是自治系统 AS64500，一个多 PoP 的 anycast 网络，同一段 IP 地址在全球多个机房宣告，用户流量被就近送达最近的那个节点。"
	for _, q := range []string{"anycast", "宣告", "节点", "PoP"} {
		s := wikiSnippet(cjk, q)
		if !utf8.ValidString(s) {
			t.Errorf("snippet for %q is not valid UTF-8: %q", q, s)
		}
		if strings.ContainsRune(s, '�') {
			t.Errorf("snippet for %q contains U+FFFD (truncated rune): %q", q, s)
		}
		if !strings.Contains(s, q) {
			t.Errorf("snippet for %q should contain the match: %q", q, s)
		}
	}
}
