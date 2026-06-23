// fx.go — tiny FX rates fetcher.
//
// Powers the CNY-equivalent rollup on the billing page + /billing bot
// command. Not an exchange feed for trading — just enough precision to
// say "our monthly VPS spend is roughly X CNY total" so the operator
// can compare against their bank statement without keying numbers into
// a separate converter.
//
// Source: open.er-api.com (free, no API key, ~1500 req/day quota,
// updated daily from a basket of central-bank feeds). Picks CNY as the
// canonical base; the response rates are "1 CNY = X target", inverted
// at lookup time to "1 target = (1/X) CNY" so the caller's mental model
// is "give me USD-to-CNY".
//
// Cache: in-memory only. Refreshed every 12h by a background goroutine
// launched from main.go. Startup tries an immediate fetch; if it fails
// the rates map stays empty and toCNY returns ok=false (caller's UI
// degrades to per-currency totals without a unified CNY figure).
//
// No disk persistence: the FX provider updates daily and a few hours
// of stale rates after a process restart is fine for "rough monthly
// total" use. If the upstream is down across a whole restart, we just
// don't show CNY — the per-currency totals still work.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// open.er-api.com response is keyed "1 CNY = X target", so we set
	// base=CNY and invert at lookup.
	fxAPIURL       = "https://open.er-api.com/v6/latest/CNY"
	fxRefreshEvery = 12 * time.Hour
	fxFetchTimeout = 8 * time.Second
)

type fxStore struct {
	mu      sync.RWMutex
	rates   map[string]float64 // target currency → "1 unit of target equals X CNY"
	fetched time.Time
	lastErr error
}

var globalFX *fxStore

func newFXStore() *fxStore {
	return &fxStore{rates: map[string]float64{"CNY": 1.0}}
}

// ToCNY converts amount in src currency to CNY. Returns ok=false if
// the rate is unavailable (no cached fetch yet OR src not in the FX
// provider's basket). Caller is responsible for displaying the source
// total without a CNY figure in that case.
func (f *fxStore) ToCNY(amount float64, src string) (float64, bool) {
	if f == nil {
		return 0, false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	rate, ok := f.rates[strings.ToUpper(src)]
	if !ok || rate <= 0 {
		return 0, false
	}
	return amount * rate, true
}

// FetchedAt returns when the cache was last successfully refreshed.
// The UI shows this so the operator can see how stale the conversion
// figure is. Zero time = never fetched.
func (f *fxStore) FetchedAt() time.Time {
	if f == nil {
		return time.Time{}
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.fetched
}

// LastErr is the last fetch error, if any. Returned alongside rates so
// the /admin/billing UI can show a small "FX feed degraded" banner
// rather than silently shipping stale numbers.
func (f *fxStore) LastErr() error {
	if f == nil {
		return errors.New("FX store not initialised")
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.lastErr
}

// refresh hits the FX provider and updates the cache. On error, the
// PREVIOUS rates are kept (no zero-out) so the system degrades to
// "slightly stale" rather than "no FX at all" during transient
// upstream outages.
func (f *fxStore) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fxFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", fxAPIURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ncn-api/fx-fetch")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.recordErr(err)
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		f.recordErr(err)
		return err
	}
	if resp.StatusCode != 200 {
		err := fmt.Errorf("FX provider %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(200, len(body))])))
		f.recordErr(err)
		return err
	}

	var parsed struct {
		Result    string             `json:"result"`
		BaseCode  string             `json:"base_code"`
		TimeLast  int64              `json:"time_last_update_unix"`
		Rates     map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		f.recordErr(err)
		return err
	}
	if parsed.Result != "success" {
		err := errors.New("FX provider returned non-success result")
		f.recordErr(err)
		return err
	}
	if parsed.BaseCode != "CNY" {
		err := fmt.Errorf("FX provider returned unexpected base %q (expected CNY)", parsed.BaseCode)
		f.recordErr(err)
		return err
	}

	// Invert: response says "1 CNY = X target" → cache "1 target = 1/X CNY".
	fresh := map[string]float64{"CNY": 1.0}
	for ccy, rate := range parsed.Rates {
		if rate <= 0 {
			continue
		}
		fresh[strings.ToUpper(ccy)] = 1.0 / rate
	}

	f.mu.Lock()
	f.rates = fresh
	f.fetched = time.Now().UTC()
	f.lastErr = nil
	f.mu.Unlock()
	log.Printf("fx: refreshed — %d currencies, base=CNY, USD≈%.4f CNY", len(fresh), fresh["USD"])
	return nil
}

func (f *fxStore) recordErr(err error) {
	f.mu.Lock()
	f.lastErr = err
	f.mu.Unlock()
	log.Printf("fx: fetch failed (%v) — last successful rates kept", err)
}

// handleFXRates is an admin-only endpoint returning the current
// per-currency rates to CNY, alongside the last-fetch timestamp + any
// recent error. Frontend reads this once on the /admin/billing page
// load to render the CNY-equivalent rollup. Lives at
// /api/v1/auth/fx/rates.
//
// Response shape:
//   { "rates": { "USD": 7.19, "EUR": 7.78, ... }, "fetched_at": "...",
//     "stale": bool, "error": "..." (if last fetch failed) }
func handleFXRates(w http.ResponseWriter, r *http.Request) {
	if globalFX == nil {
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"rates": map[string]float64{}, "stale": true,
			"error": "FX not initialised",
		}})
		return
	}
	globalFX.mu.RLock()
	ratesCopy := make(map[string]float64, len(globalFX.rates))
	for k, v := range globalFX.rates {
		ratesCopy[k] = v
	}
	fetched := globalFX.fetched
	lastErr := globalFX.lastErr
	globalFX.mu.RUnlock()

	stale := fetched.IsZero() || time.Since(fetched) > 24*time.Hour
	out := map[string]any{
		"rates":      ratesCopy,
		"fetched_at": fetched,
		"stale":      stale,
	}
	if lastErr != nil {
		out["error"] = lastErr.Error()
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
}

// startFXRefresher launches the background goroutine that fetches FX
// rates on a fxRefreshEvery cadence. Initial fetch fires immediately
// (synchronously, with a 30s grace timeout) so the first /billing
// after startup has a CNY figure to show.
func startFXRefresher(ctx context.Context) {
	if err := globalFX.refresh(ctx); err != nil {
		log.Printf("fx: initial fetch failed — will retry in background: %v", err)
	}
	go func() {
		t := time.NewTicker(fxRefreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = globalFX.refresh(ctx)
			}
		}
	}()
}
