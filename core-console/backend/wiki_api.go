// wiki_api.go — HTTP handlers for the self-hosted wiki. Three tiers, matching
// the rest of the console:
//   public  /api/v1/wiki/*        — anonymous, publicOnly=true (nginx allow-list)
//   read    /api/v1/auth/wiki/*   — any logged-in operator (requireAuth)
//   write   /api/v1/auth/wiki/... — admin only (requireRole), audited
// Routes are registered in main.go. publicOnly is enforced server-side here so
// internal pages never leak through the public tier.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var wikiPathRe = regexp.MustCompile(`^[a-z0-9][a-z0-9/_-]{0,120}$`)

func validWikiPath(p string) bool {
	return wikiPathRe.MatchString(p) && !strings.Contains(p, "//") && !strings.HasSuffix(p, "/")
}

// ── shared helpers (publicOnly toggles the tier) ─────────────────────────────

func serveWikiTree(w http.ResponseWriter, _ *http.Request, publicOnly bool) {
	list, err := globalWiki.list(publicOnly)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: err.Error()})
		return
	}
	if list == nil {
		list = []wikiMeta{}
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: list})
}

func serveWikiPage(w http.ResponseWriter, r *http.Request, publicOnly bool) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if !validWikiPath(path) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid path"})
		return
	}
	p, ok, err := globalWiki.get(path, publicOnly)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "page not found"})
		return
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: p})
}

func serveWikiSearch(w http.ResponseWriter, r *http.Request, publicOnly bool) {
	hits, err := globalWiki.search(r.URL.Query().Get("q"), publicOnly)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: err.Error()})
		return
	}
	if hits == nil {
		hits = []wikiHit{}
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: hits})
}

// ── public tier ──────────────────────────────────────────────────────────────

func handleWikiTreePublic(w http.ResponseWriter, r *http.Request)   { serveWikiTree(w, r, true) }
func handleWikiPagePublic(w http.ResponseWriter, r *http.Request)   { serveWikiPage(w, r, true) }
func handleWikiSearchPublic(w http.ResponseWriter, r *http.Request) { serveWikiSearch(w, r, true) }

// ── internal read tier (any operator) ───────────────────────────────────────

func handleWikiTree(w http.ResponseWriter, r *http.Request)     { serveWikiTree(w, r, false) }
func handleWikiPageRead(w http.ResponseWriter, r *http.Request) { serveWikiPage(w, r, false) }
func handleWikiSearch(w http.ResponseWriter, r *http.Request)   { serveWikiSearch(w, r, false) }

// ── admin write tier ─────────────────────────────────────────────────────────

func handleWikiSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		Path     string `json:"path"`
		Title    string `json:"title"`
		Content  string `json:"content"`
		Locale   string `json:"locale"`
		IsPublic bool   `json:"is_public"`
		Sort     int    `json:"sort"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if !validWikiPath(req.Path) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "path must match ^[a-z0-9][a-z0-9/_-]{0,120}$"})
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "title required"})
		return
	}
	if req.Locale == "" {
		req.Locale = "zh"
	}
	op := adminOperator(r)
	saved, err := globalWiki.upsert(wikiPage{
		Path: req.Path, Title: req.Title, Content: req.Content, Locale: req.Locale,
		IsPublic: req.IsPublic, Sort: req.Sort,
	}, op)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{Event: "wiki.page.save", Severity: auditSevInfo, Actor: op, Target: req.Path,
		Details: map[string]any{"version": saved.Version, "is_public": saved.IsPublic}})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: saved})
}

func handleWikiDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "DELETE only"})
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if !validWikiPath(path) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid path"})
		return
	}
	ok, err := globalWiki.delete(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "page not found"})
		return
	}
	op := adminOperator(r)
	auditRecord(r, AuditEvent{Event: "wiki.page.delete", Severity: auditSevWarn, Actor: op, Target: path})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{"deleted": path}})
}

func handleWikiVersions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if !validWikiPath(path) {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid path"})
		return
	}
	vs, err := globalWiki.versions(path)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: err.Error()})
		return
	}
	if vs == nil {
		vs = []wikiVersion{}
	}
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: vs})
}

func handleWikiRevert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}
	var req struct {
		Path      string `json:"path"`
		VersionID int64  `json:"version_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil || !validWikiPath(strings.TrimSpace(req.Path)) || req.VersionID <= 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "need valid path + version_id"})
		return
	}
	op := adminOperator(r)
	saved, err := globalWiki.revert(strings.TrimSpace(req.Path), req.VersionID, op)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	auditRecord(r, AuditEvent{Event: "wiki.page.revert", Severity: auditSevWarn, Actor: op, Target: req.Path,
		Details: map[string]any{"version_id": req.VersionID, "new_version": saved.Version}})
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: saved})
}
