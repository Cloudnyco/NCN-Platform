// mail_move.go — mutations on a single message: move folder, set/clear flags, delete-to-trash.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// POST /api/v1/mail/messages/<uid>/move?folder=INBOX
//
//	{ "to": "Archive" }
//
// IMAP MOVE to a target folder. Caller MUST pre-verify "to" exists (else
// dovecot returns NO and we surface that). Existing /messages/<uid> DELETE
// remains as a shortcut for Trash; this is the general case.
func (m *mailService) handleMove(w http.ResponseWriter, r *http.Request) {
	uidStr, err := pathSegmentAfter(r.URL.Path, "/api/v1/mail/messages/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing uid"})
		return
	}
	uidStr = strings.TrimSuffix(uidStr, "/move")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad uid"})
		return
	}
	uid := imap.UID(uid64)

	var req struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	target := strings.TrimSpace(req.To)
	if target == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "'to' folder required"})
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "INBOX"
	}
	if folder == target {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "source and target folders are the same"})
		return
	}

	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		if _, err := ic.Move(imap.UIDSetNum(uid), target).Wait(); err != nil {
			return fmt.Errorf("move to %s: %w", target, err)
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"from": folder, "to": target, "uid": uint32(uid),
		}})
		return nil
	})
}

// POST /api/v1/mail/messages/{uid}/flag?folder=INBOX
//
//	{ "op": "add"|"remove", "flag": "\\Seen"|"\\Flagged"|"\\Deleted" }
func (m *mailService) handleFlag(w http.ResponseWriter, r *http.Request) {
	uidStr, err := pathSegmentAfter(r.URL.Path, "/api/v1/mail/messages/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing uid"})
		return
	}
	uidStr = strings.TrimSuffix(uidStr, "/flag")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad uid"})
		return
	}
	uid := imap.UID(uid64)

	var req struct {
		Op   string `json:"op"`
		Flag string `json:"flag"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	if req.Flag == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "flag required"})
		return
	}
	op := imap.StoreFlagsAdd
	if req.Op == "remove" {
		op = imap.StoreFlagsDel
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "INBOX"
	}

	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		set := imap.UIDSetNum(uid)
		if err := ic.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.Flag(req.Flag)}}, nil).Close(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, envelope{OK: true})
		return nil
	})
}

// DELETE /api/v1/mail/messages/{uid}?folder=INBOX
//
// Moves to Trash (or marks \Deleted + expunge if Trash unavailable).
func (m *mailService) handleDelete(w http.ResponseWriter, r *http.Request) {
	uidStr, err := pathSegmentAfter(r.URL.Path, "/api/v1/mail/messages/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing uid"})
		return
	}
	uidStr = strings.TrimSuffix(uidStr, "/")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad uid"})
		return
	}
	uid := imap.UID(uid64)
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "INBOX"
	}

	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, nil).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		set := imap.UIDSetNum(uid)
		mv, err := ic.Move(set, "Trash").Wait()
		_ = mv
		if err != nil {
			// Fallback: mark deleted + expunge.
			if err := ic.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
				return err
			}
			if err := ic.Expunge().Close(); err != nil {
				return err
			}
		}
		writeJSON(w, http.StatusOK, envelope{OK: true})
		return nil
	})
}
