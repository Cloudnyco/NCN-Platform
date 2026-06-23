// mail_drafts.go — IMAP APPEND-based draft autosave + cleanup.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// POST /api/v1/mail/draft
//
//	{ "to": "...", "cc": "...", "subject": "...", "body": "...", "replace_uid": <optional> }
//
// IMAP APPEND to the Drafts folder with \Draft flag. If replace_uid is set,
// the prior draft is silently expunged on success (atomic-ish replace). The
// returned uid is what the client passes as `replace_uid` on the next save.
//
// The Drafts folder is auto-created if missing — every webmail user is
// expected to have one. We don't auto-subscribe; that's the client's job.
func (m *mailService) handleDraftSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To, Cc, Bcc, Subject, Body string
		ReplaceUID                 uint32 `json:"replace_uid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, mailMaxBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	for _, v := range []string{req.Subject, req.To, req.Cc, req.Bcc} {
		if strings.ContainsAny(v, "\r\n") {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "header injection rejected"})
			return
		}
	}

	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}

	// Compose RFC 5322 text/plain message. Drafts deliberately don't go
	// through the SMTP path (no DKIM, no SPF — they live entirely inside
	// IMAP). User-Agent is dropped so re-opening + sending later uses the
	// regular send path's headers.
	var buf bytes.Buffer
	mid := fmt.Sprintf("<draft-%d.%s@%s>", time.Now().UnixNano(), randomTokenHex(6), mailHost)
	headers := []string{
		"From: " + c.Mailbox,
		"To: " + req.To,
	}
	if strings.TrimSpace(req.Cc) != "" {
		headers = append(headers, "Cc: "+req.Cc)
	}
	// Bcc is stored in the draft so when the user reopens it the recipients
	// come back. The send path is responsible for stripping Bcc from the
	// transmitted message body — drafts never leave IMAP, so writing the
	// header here is safe and required for round-tripping.
	if strings.TrimSpace(req.Bcc) != "" {
		headers = append(headers, "Bcc: "+req.Bcc)
	}
	headers = append(headers,
		"Subject: "+mimeHeader(req.Subject),
		"Date: "+time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: "+mid,
		"MIME-Version: 1.0",
		`Content-Type: text/plain; charset="UTF-8"`,
		"Content-Transfer-Encoding: 8bit",
		"X-NCN-Draft: 1",
	)
	buf.WriteString(strings.Join(headers, "\r\n"))
	buf.WriteString("\r\n\r\n")
	buf.WriteString(req.Body)

	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		// Ensure Drafts exists. CREATE is idempotent enough — dovecot
		// returns NO[ALREADYEXISTS] which we swallow.
		if err := ic.Create("Drafts", nil).Wait(); err != nil {
			// just log; APPEND will fail clearly if it really can't be created
			if !strings.Contains(strings.ToLower(err.Error()), "already") {
				log.Printf("draft: create Drafts: %v", err)
			}
		}

		ac := ic.Append("Drafts", int64(buf.Len()), &imap.AppendOptions{
			Flags: []imap.Flag{imap.FlagDraft, imap.FlagSeen},
			Time:  time.Now(),
		})
		if _, err := ac.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("append write: %w", err)
		}
		if err := ac.Close(); err != nil {
			return fmt.Errorf("append close: %w", err)
		}
		appendData, err := ac.Wait()
		if err != nil {
			return fmt.Errorf("append: %w", err)
		}
		newUID := uint32(appendData.UID)

		// Replace: expunge the old draft if specified.
		if req.ReplaceUID != 0 && uint32(req.ReplaceUID) != newUID {
			if _, err := ic.Select("Drafts", nil).Wait(); err == nil {
				set := imap.UIDSetNum(imap.UID(req.ReplaceUID))
				_ = ic.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close()
				_ = ic.Expunge().Close()
			}
		}

		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"uid":      newUID,
			"saved_at": time.Now().UTC().Format(time.RFC3339),
		}})
		return nil
	})
}

// DELETE /api/v1/mail/draft/<uid>  — discard a saved draft
func (m *mailService) handleDraftDelete(w http.ResponseWriter, r *http.Request) {
	uidStr := strings.TrimPrefix(r.URL.Path, "/api/v1/mail/draft/")
	uidStr = strings.TrimSuffix(uidStr, "/")
	uid64, err := strconv.ParseUint(uidStr, 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad uid"})
		return
	}
	uid := imap.UID(uid64)
	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		if _, err := ic.Select("Drafts", nil).Wait(); err != nil {
			return fmt.Errorf("select Drafts: %w", err)
		}
		set := imap.UIDSetNum(uid)
		if err := ic.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
			return err
		}
		if err := ic.Expunge().Close(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, envelope{OK: true})
		return nil
	})
}
