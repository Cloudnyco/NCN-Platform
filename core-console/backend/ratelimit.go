// Tiny per-IP sliding-window rate limiter for the public Looking Glass.
// In-memory, no external dependency, ~30 LoC of real logic.
package main

import (
	"net/http"
	"sync"
	"time"
)

type rateLimiter struct {
	mu      sync.Mutex
	hits    map[string][]time.Time
	limit   int           // max requests within `window`
	window  time.Duration // sliding window length
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		hits:   make(map[string][]time.Time, 256),
		limit:  limit,
		window: window,
	}
}

// Allow returns (allowed, retryAfter). retryAfter is non-zero only when blocked.
func (rl *rateLimiter) Allow(key string) (bool, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	prev := rl.hits[key]
	// Prune entries outside the window in place.
	out := prev[:0]
	for _, t := range prev {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	if len(out) >= rl.limit {
		// Time until the oldest entry falls out of the window.
		retry := out[0].Add(rl.window).Sub(now)
		if retry < 0 {
			retry = 0
		}
		rl.hits[key] = out
		return false, retry
	}
	out = append(out, now)
	rl.hits[key] = out
	return true, 0
}

// Middleware factory. Identifies the caller by clientAddr().
func (rl *rateLimiter) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, retry := rl.Allow(clientAddr(r))
		if !ok {
			w.Header().Set("Retry-After", retry.Round(time.Second).String())
			writeJSON(w, http.StatusTooManyRequests, envelope{
				OK:    false,
				Error: "rate-limited · retry in " + retry.Round(time.Second).String(),
			})
			return
		}
		next(w, r)
	}
}
