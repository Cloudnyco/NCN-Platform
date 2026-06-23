// Telegram bot-driven bind tickets.
//
// Telegram WEB LOGIN now goes through the standard OIDC auth-code flow in
// oauth.go (BotFather Client ID/Secret) like any other provider. What remains
// here is the bot-driven /bind flow: the bot mints a one-time ticket carrying
// the requesting Telegram user's (id, username); the authenticated operator
// redeems it on /admin/bind, binding that Telegram identity to their account.
// The identity comes from the /bind chat message, not a browser flow.

package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// handleTGBindTicket — protected GET (peek) / POST (consume) for the bot-driven
// /bind flow.
//
//	GET  ?t=<token> → { telegram_id, telegram_username }  (no consume)
//	POST { "token": <token> } → binds → { provider, telegram_username }
func (svc *oauthService) handleTGBindTicket(w http.ResponseWriter, r *http.Request) {
	op := adminOperator(r)
	if op == "" {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		t, ok := svc.auth.peekTGBindTicket(r.URL.Query().Get("t"))
		if !ok {
			writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "link expired or invalid — send /bind to the bot again"})
			return
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{
			"telegram_id": t.TGID, "telegram_username": t.TGUsername,
		}})
	case http.MethodPost:
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad request"})
			return
		}
		t, ok := svc.auth.consumeTGBindTicket(strings.TrimSpace(body.Token))
		if !ok {
			writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "link expired or invalid — send /bind to the bot again"})
			return
		}
		if err := svc.auth.bindIdentity(op, "telegram", t.TGID, t.TGUsername); err != nil {
			auditRecord(r, AuditEvent{Event: "oauth.bind.fail", Severity: auditSevWarn, Actor: op, Outcome: "fail",
				Details: map[string]any{"provider": "telegram", "path": "bind-ticket", "error": err.Error()}})
			writeJSON(w, http.StatusConflict, envelope{OK: false, Error: err.Error()})
			return
		}
		auditRecord(r, AuditEvent{Event: "oauth.bind", Severity: auditSevWarn, Actor: op,
			Details: map[string]any{"provider": "telegram", "path": "bind-ticket", "tg": t.TGUsername}})
		// Close the loop on the Telegram side: DM the just-bound user.
		svc.tg.notifyBindSuccess(t.TGID, op)
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]string{
			"provider": "telegram", "telegram_username": t.TGUsername,
		}})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET or POST only"})
	}
}
