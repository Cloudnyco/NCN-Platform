// model_config.go — per-purpose DeepSeek model selection.
//
// Different AI features want different models: casual chat / summaries run on
// the cheap fast model, while the agent / grounded Q&A / failure diagnosis run
// on the strong one. This store maps a small set of PURPOSES to model ids,
// persisted + editable from the console and the bot (/model). Available models
// are discovered from DeepSeek's /models at startup (falling back to the two
// known v4 ids), so the picker reflects what the account actually offers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

var (
	errBadPurpose = errors.New("unknown purpose")
	errBadModel   = errors.New("model not available")
)

const aiModelsPath = authConfigDir + "/ai-models.json"

// purposes (kept few + meaningful). Each maps to a model id.
const (
	purposeChat     = "chat"     // /chat + @mention + reply (casual companion)
	purposeAsk      = "ask"      // /ask + console simple chat (grounded Q&A)
	purposeSummary  = "summary"  // /summary
	purposeAgent    = "agent"    // /agent + console agent (tool-using)
	purposeDiagnose = "diagnose" // op-failure AI diagnosis
)

var aiPurposes = []string{purposeChat, purposeAsk, purposeSummary, purposeAgent, purposeDiagnose}

const (
	modelFlash = "deepseek-v4-flash"
	modelPro   = "deepseek-v4-pro"
)

// defaults: casual/summary on flash; agent/ask/diagnose on pro.
func defaultModelMap() map[string]string {
	return map[string]string{
		purposeChat:     modelFlash,
		purposeAsk:      modelPro,
		purposeSummary:  modelFlash,
		purposeAgent:    modelPro,
		purposeDiagnose: modelPro,
	}
}

type aiModelStore struct {
	mu        sync.RWMutex
	models    map[string]string // purpose → model id
	available []string          // discovered from DeepSeek /models
	path      string
}

var globalAIModels *aiModelStore

func newAIModelStore() *aiModelStore {
	s := &aiModelStore{models: defaultModelMap(), path: aiModelsPath, available: []string{modelFlash, modelPro}}
	apply := func(saved map[string]string) {
		for _, p := range aiPurposes {
			if m := saved[p]; m != "" {
				s.models[p] = m
			}
		}
	}
	loadedFromDB := false
	if globalDB != nil {
		if doc, err := loadConfigDoc("ai_models"); err != nil {
			log.Printf("ai-models: db load failed (%v) — falling back to file", err)
		} else if doc != nil {
			var saved map[string]string
			if json.Unmarshal(doc, &saved) == nil {
				apply(saved)
				loadedFromDB = true
			}
		}
	}
	if !loadedFromDB {
		if b, err := os.ReadFile(aiModelsPath); err == nil && len(b) > 0 {
			var saved map[string]string
			if json.Unmarshal(b, &saved) == nil {
				apply(saved)
			}
		}
	}
	if globalDB != nil && !loadedFromDB {
		s.mu.Lock()
		_ = s.persistLocked() // best-effort migrate file→DB
		s.mu.Unlock()
	}
	return s
}

// refreshAvailable populates the available-models list from DeepSeek (best
// effort; keeps the fallback on failure).
func (s *aiModelStore) refreshAvailable(c *deepseekClient) {
	if !c.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ids, err := c.listModels(ctx)
	if err != nil || len(ids) == 0 {
		return
	}
	sort.Strings(ids)
	s.mu.Lock()
	s.available = ids
	s.mu.Unlock()
}

func (s *aiModelStore) persistLocked() error {
	b, err := json.MarshalIndent(s.models, "", "  ")
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
		if err := saveConfigDoc("ai_models", b); err != nil {
			log.Printf("ai-models: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// modelFor returns the configured model id for a purpose (or "" if unknown,
// which lets the client fall back to its default).
func (s *aiModelStore) modelFor(purpose string) string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.models[purpose]
}

func (s *aiModelStore) snapshot() (map[string]string, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]string, len(s.models))
	for k, v := range s.models {
		m[k] = v
	}
	av := append([]string(nil), s.available...)
	return m, av
}

func (s *aiModelStore) isAvailable(model string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.available {
		if a == model {
			return true
		}
	}
	return false
}

// set assigns a model to a purpose (validated: known purpose + available model).
func (s *aiModelStore) set(purpose, model string) error {
	known := false
	for _, p := range aiPurposes {
		if p == purpose {
			known = true
			break
		}
	}
	if !known {
		return errBadPurpose
	}
	if !s.isAvailable(model) {
		return errBadModel
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models[purpose] = model
	return s.persistLocked()
}

// aiModelFor is the package-level resolver used by the AI features.
func aiModelFor(purpose string) string {
	if globalAIModels == nil {
		return ""
	}
	return globalAIModels.modelFor(purpose)
}

// ── HTTP API ─────────────────────────────────────────────────────────────────
// GET → {available, purposes:{purpose:model}, order:[...]} ; POST {purpose,model} (admin) → set one.
func handleAIModels(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if globalAIModels == nil {
			writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: "model store not ready"})
			return
		}
		m, av := globalAIModels.snapshot()
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"available": av, "purposes": m, "order": aiPurposes,
		}})
	case http.MethodPost:
		if !operatorIsAdmin(op) {
			writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "admin required"})
			return
		}
		var body struct {
			Purpose string `json:"purpose"`
			Model   string `json:"model"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
			return
		}
		if err := globalAIModels.set(body.Purpose, body.Model); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: err.Error()})
			return
		}
		auditRecord(r, AuditEvent{Event: "ai.model.set", Severity: auditSevInfo, Actor: op,
			Details: map[string]any{"purpose": body.Purpose, "model": body.Model}})
		m, av := globalAIModels.snapshot()
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"available": av, "purposes": m, "order": aiPurposes}})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}
