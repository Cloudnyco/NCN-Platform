// ai_history_api.go — per-operator conversation history + memory HTTP API for
// the console assistant. All routes are operator-scoped (the caller only sees
// their own data via adminOperator(r)).
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// GET  /api/v1/auth/ai/conversations            → {conversations:[{id,title,updated_at}]}
// POST /api/v1/auth/ai/conversations  {id?,messages} → {id}
func handleAIConversations(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"conversations": globalAIUsers.listConversations(op)}})
	case http.MethodPost:
		var body struct {
			ID       string  `json:"id"`
			Messages []aiMsg `json:"messages"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.Messages) == 0 {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
			return
		}
		id, err := globalAIUsers.saveConversation(op, body.ID, body.Messages)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"id": id}})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// GET    /api/v1/auth/ai/conversations/{id}  → full conversation
// DELETE /api/v1/auth/ai/conversations/{id}  → remove
func handleAIConversationItem(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/ai/conversations/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		c := globalAIUsers.getConversation(op, id)
		if c == nil {
			writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "not found"})
			return
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: c})
	case http.MethodDelete:
		_ = globalAIUsers.deleteConversation(op, id)
		writeJSON(w, http.StatusOK, envelope{OK: true})
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// GET  /api/v1/auth/ai/memory          → {memory:[{id,text,created_at}]}
// POST /api/v1/auth/ai/memory  {text}  → adds one
func handleAIMemory(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"memory": globalAIUsers.listMemory(op)}})
	case http.MethodPost:
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "text required"})
			return
		}
		if _, err := globalAIUsers.addMemory(op, body.Text); err != nil {
			writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"memory": globalAIUsers.listMemory(op)}})
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
	}
}

// DELETE /api/v1/auth/ai/memory/{id}  → remove one
func handleAIMemoryItem(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "method not allowed"})
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/ai/memory/"), "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "id required"})
		return
	}
	globalAIUsers.deleteMemory(op, id)
	writeJSON(w, http.StatusOK, envelope{OK: true})
}
