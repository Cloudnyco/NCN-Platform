// mail_attach.go — stream a single attachment byte-for-byte.
//
//	GET /api/v1/mail/messages/{uid}/attachments/{i}
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomail "github.com/emersion/go-message/mail"
)

// GET /api/v1/mail/messages/<uid>/attachments/<index>?folder=INBOX
//
// Streams the i-th attachment (0-indexed, in the order parseMessage emits)
// with the original Content-Type + Content-Disposition. Inefficient at large
// scale — fetches the whole RFC822 — but fine for normal mail sizes; the
// IMAP server caches the message anyway from the prior read.
func (m *mailService) handleAttachmentDownload(w http.ResponseWriter, r *http.Request) {
	// path: /api/v1/mail/messages/<uid>/attachments/<index>
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/mail/messages/"), "/")
	if len(parts) < 3 || parts[1] != "attachments" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad path"})
		return
	}
	uid64, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad uid"})
		return
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad index"})
		return
	}
	uid := imap.UID(uid64)
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "INBOX"
	}

	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		fetchOpts := &imap.FetchOptions{
			BodySection: []*imap.FetchItemBodySection{{Specifier: imap.PartSpecifierNone}},
		}
		var raw []byte
		messages := ic.Fetch(imap.UIDSetNum(uid), fetchOpts)
		for {
			msg := messages.Next()
			if msg == nil {
				break
			}
			data, err := msg.Collect()
			if err != nil {
				return err
			}
			for _, sect := range data.BodySection {
				raw = sect.Bytes
				break
			}
		}
		if err := messages.Close(); err != nil {
			return err
		}
		if raw == nil {
			return errors.New("no such message")
		}

		mr, err := gomail.CreateReader(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("parse mime: %w", err)
		}
		defer mr.Close()
		i := 0
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				return fmt.Errorf("attachment index %d out of range", index)
			}
			if err != nil {
				return err
			}
			ah, ok := p.Header.(*gomail.AttachmentHeader)
			if !ok {
				continue
			}
			if i != index {
				i++
				continue
			}
			filename, _ := ah.Filename()
			ct, _, _ := ah.ContentType()
			if ct == "" {
				ct = "application/octet-stream"
			}
			if filename == "" {
				filename = fmt.Sprintf("attachment-%d", index)
			}
			// Quote filename per RFC 6266; strip CR/LF to defend against
			// header injection (filename comes from sender).
			safeName := strings.ReplaceAll(strings.ReplaceAll(filename, "\r", ""), "\n", "")
			safeName = strings.ReplaceAll(safeName, `"`, `'`)
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition", `attachment; filename="`+safeName+`"`)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "private, no-store")
			if _, err := io.Copy(w, p.Body); err != nil {
				log.Printf("mail: attach copy %s uid=%d idx=%d: %v", folder, uid, index, err)
			}
			return nil
		}
	})
}
