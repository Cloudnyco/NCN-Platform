// ai_usage.go — DeepSeek token accounting, so spend is visible (/tokens) instead
// of guessed at. In-memory: today (UTC) + cumulative-since-boot, keyed by the
// resolved model. Resets on restart — fine for "see how much we burn"; persist
// to Postgres later if billing-grade history is wanted.
package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type aiUsageBucket struct {
	Calls     int64
	PromptTok int64
	OutputTok int64
}

type aiUsageStore struct {
	mu    sync.Mutex
	day   string // current UTC date for `today`
	since time.Time
	today map[string]*aiUsageBucket // model → bucket
	total map[string]*aiUsageBucket
}

var globalAIUsage = &aiUsageStore{
	today: map[string]*aiUsageBucket{},
	total: map[string]*aiUsageBucket{},
}

// record adds one completion's usage. Called from the deepseek client after a
// successful (non-stream) call. Zero usage (e.g. an unparsed stream) is ignored.
func (u *aiUsageStore) record(model string, promptTok, outputTok int) {
	if u == nil || (promptTok == 0 && outputTok == 0) {
		return
	}
	if model == "" {
		model = "?"
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.since.IsZero() {
		u.since = time.Now().UTC()
	}
	if d := time.Now().UTC().Format("2006-01-02"); d != u.day {
		u.day = d
		u.today = map[string]*aiUsageBucket{}
	}
	for _, m := range []map[string]*aiUsageBucket{u.today, u.total} {
		b := m[model]
		if b == nil {
			b = &aiUsageBucket{}
			m[model] = b
		}
		b.Calls++
		b.PromptTok += int64(promptTok)
		b.OutputTok += int64(outputTok)
	}
}

// totalsSnapshot returns a copy of the cumulative-since-boot per-model usage
// (for /metrics). Safe to call concurrently.
func (u *aiUsageStore) totalsSnapshot() map[string]aiUsageBucket {
	out := map[string]aiUsageBucket{}
	if u == nil {
		return out
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for m, b := range u.total {
		out[m] = *b
	}
	return out
}

func humanTok(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// report renders the /tokens summary (today + cumulative, per model).
func (u *aiUsageStore) report() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var b strings.Builder
	b.WriteString("🧮 <b>AI token 用量</b>")
	section := func(title string, m map[string]*aiUsageBucket) {
		fmt.Fprintf(&b, "\n\n<b>%s</b>", title)
		if len(m) == 0 {
			b.WriteString("\n<blockquote>—</blockquote>")
			return
		}
		models := make([]string, 0, len(m))
		for k := range m {
			models = append(models, k)
		}
		sort.Strings(models)
		var pT, oT, calls int64
		for _, model := range models {
			bk := m[model]
			fmt.Fprintf(&b, "\n<code>%s</code> · %d calls · in %s / out %s",
				model, bk.Calls, humanTok(bk.PromptTok), humanTok(bk.OutputTok))
			pT += bk.PromptTok
			oT += bk.OutputTok
			calls += bk.Calls
		}
		fmt.Fprintf(&b, "\n<blockquote>Σ %d calls · in %s / out %s · total %s tok</blockquote>",
			calls, humanTok(pT), humanTok(oT), humanTok(pT+oT))
	}
	if u.since.IsZero() {
		b.WriteString("\n<blockquote>no AI calls since boot</blockquote>")
		return b.String()
	}
	section("Today (UTC "+u.day+")", u.today)
	section("Since "+u.since.Format("01-02 15:04")+" UTC", u.total)
	return b.String()
}
