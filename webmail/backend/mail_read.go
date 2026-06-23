// mail_read.go — fetch + parse a single message body.
//
//	GET /api/v1/mail/messages/{uid}
package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// GET /api/v1/mail/messages/{uid}?folder=INBOX
func (m *mailService) handleReadMessage(w http.ResponseWriter, r *http.Request) {
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
		uidSet := imap.UIDSetNum(uid)
		// Fetch the whole RFC822 (body + headers). Cheap for typical mail.
		fetchOpts := &imap.FetchOptions{
			UID: true, Flags: true, InternalDate: true, Envelope: true,
			BodySection: []*imap.FetchItemBodySection{{Specifier: imap.PartSpecifierNone}},
		}
		messages := ic.Fetch(uidSet, fetchOpts)
		var raw []byte
		var meta *imapclient.FetchMessageBuffer
		for {
			msg := messages.Next()
			if msg == nil {
				break
			}
			data, err := msg.Collect()
			if err != nil {
				return err
			}
			meta = data
			for _, sect := range data.BodySection {
				raw = sect.Bytes
				break
			}
		}
		if err := messages.Close(); err != nil {
			return err
		}
		if meta == nil {
			writeJSON(w, http.StatusNotFound, envelope{OK: false, Error: "no such message"})
			return nil
		}

		text, htmlBody, parts, parseErr := parseMessage(raw)
		// nil-slice defence: see mail_list.go for the same fix. The
		// frontend reads activeMessage.flags.includes('\\Flagged') which
		// would TypeError on null.
		flags := meta.Flags
		if flags == nil {
			flags = []imap.Flag{}
		}
		if parts == nil {
			parts = []attachmentMeta{}
		}
		out := map[string]any{
			"uid":         uint32(meta.UID),
			"flags":       flags,
			"date":        meta.InternalDate.UTC().Format(time.RFC3339),
			"text":        text,
			"html":        htmlBody,
			"attachments": parts,
		}
		if env := meta.Envelope; env != nil {
			out["subject"] = env.Subject
			out["from"] = formatAddrs(env.From)
			out["to"] = formatAddrs(env.To)
			out["cc"] = formatAddrs(env.Cc)
			out["reply_to"] = formatAddrs(env.ReplyTo)
			out["message_id"] = env.MessageID
		}
		for _, k := range []string{"from", "to", "cc", "reply_to"} {
			if out[k] == nil {
				out[k] = []string{}
			}
		}
		if parseErr != nil {
			out["parse_warning"] = parseErr.Error()
		}

		// Side effect: silently mark \Seen — matches mail clients.
		_ = ic.Store(uidSet, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}, nil)

		writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
		return nil
	})
}
