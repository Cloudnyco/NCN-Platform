package main

import "testing"

// TestMdToTG locks the markdown→Telegram-HTML subset: correct rendering AND
// parse-safety (escape-first, code spans untouched, identifiers not mangled).
func TestMdToTG(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bold", "**hi**", "<b>hi</b>"},
		{"italic", "say *hi* now", "say <i>hi</i> now"},
		{"bold not italic", "**bold**", "<b>bold</b>"},
		{"inline code keeps stars", "`a*b*c`", "<code>a*b*c</code>"},
		{"inline code keeps underscores", "`bgp_down_count`", "<code>bgp_down_count</code>"},
		{"snake_case untouched", "metric bgp_down_count high", "metric bgp_down_count high"},
		{"link", "[docs](https://e.com/a)", `<a href="https://e.com/a">docs</a>`},
		{"heading to bold", "## Status", "<b>Status</b>"},
		{"bullet dash", "- item", "• item"},
		{"bullet star", "* item", "• item"},
		{"strikethrough", "~~no~~", "<s>no</s>"},
		{"escape html", "a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"lone star literal", "2 * 3 * 4", "2 * 3 * 4"},
		{"lone backtick literal", "a ` b", "a ` b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mdToTG(c.in); got != c.want {
				t.Fatalf("mdToTG(%q)\n got: %q\nwant: %q", c.in, got, c.want)
			}
		})
	}
}

// TestMdToTGFenceWithStars verifies a fenced block keeps its body literal
// (stars/underscores inside must not become emphasis) and is wrapped in <pre>.
func TestMdToTGFenceWithStars(t *testing.T) {
	got := mdToTG("```\nx = a*b _c_\n```")
	want := "<pre>x = a*b _c_\n</pre>"
	if got != want {
		t.Fatalf("fence:\n got: %q\nwant: %q", got, want)
	}
}
