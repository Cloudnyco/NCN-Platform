// mail_list.go — message-list pagination.
//
//	GET /api/v1/mail/folders/{name}/messages
package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// GET /api/v1/mail/folders/{name}/messages?limit=50&offset=0
//
// Returns most-recent first. The name segment is URL-encoded UTF-8 mailbox.
func (m *mailService) handleListMessages(w http.ResponseWriter, r *http.Request) {
	// path: /api/v1/mail/folders/<name>/messages
	folder, err := pathSegmentAfter(r.URL.Path, "/api/v1/mail/folders/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing folder"})
		return
	}
	folder = strings.TrimSuffix(folder, "/messages")
	if folder == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "empty folder"})
		return
	}

	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}
	if limit < 1 {
		limit = 1
	}
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		sel, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		total := uint32(sel.NumMessages)
		if total == 0 {
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
				"folder":   folder,
				"total":    0,
				"messages": []any{},
			}})
			return nil
		}

		// Clamp the offset to the last valid page rather than return empty
		// when offset >= total. Previous behaviour: stale offset (e.g. user
		// paginated to offset=50 then messages got deleted bringing total
		// down to 16) would silently render an empty list with "16 total"
		// in the sidebar — exactly the "INBOX shows unread but no
		// messages" symptom.
		if offset >= int(total) {
			// Snap to the page that ends at total (i.e. the newest batch).
			offset = 0
		}

		// Compute the sequence window from the back (newest = highest seq).
		// e.g. total=100, offset=0, limit=50  →  seq 51..100 (we'll reverse later)
		startOff := int(total) - offset - limit
		endOff := int(total) - offset
		if startOff < 1 {
			startOff = 1
		}

		seqSet := imap.SeqSetNum()
		for i := startOff; i <= endOff; i++ {
			seqSet.AddNum(uint32(i))
		}

		fetchOpts := &imap.FetchOptions{
			UID:           true,
			Flags:         true,
			InternalDate:  true,
			RFC822Size:    true,
			Envelope:      true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: false},
		}
		var rows []map[string]any
		messages := ic.Fetch(seqSet, fetchOpts)
		for {
			msg := messages.Next()
			if msg == nil {
				break
			}
			data, err := msg.Collect()
			if err != nil {
				return err
			}
			// IMAP returns a nil Flags slice for messages with no flags set
			// (typical for freshly-delivered mail that hasn't been touched).
			// Go's json marshalling turns a nil []imap.Flag into JSON `null`,
			// but the frontend does `m.flags.includes('\\Seen')` which throws
			// TypeError on null and BREAKS the entire v-for render → blank
			// list pane. Always emit `[]` instead of null.
			flags := data.Flags
			if flags == nil {
				flags = []imap.Flag{}
			}
			row := map[string]any{
				"uid":   uint32(data.UID),
				"seq":   data.SeqNum,
				"size":  data.RFC822Size,
				"flags": flags,
				"date":  data.InternalDate.UTC().Format(time.RFC3339),
			}
			if env := data.Envelope; env != nil {
				row["subject"] = env.Subject
				row["from"] = formatAddrs(env.From)
				row["to"] = formatAddrs(env.To)
				row["message_id"] = env.MessageID
			}
			// Same nil-vs-empty defence for from/to in case Envelope was
			// present but the address lists are empty.
			if row["from"] == nil { row["from"] = []string{} }
			if row["to"] == nil   { row["to"]   = []string{} }
			rows = append(rows, row)
		}
		if err := messages.Close(); err != nil {
			return err
		}

		// Reverse so newest is first.
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"folder":   folder,
			"total":    total,
			"limit":    limit,
			"offset":   offset,
			"messages": rows,
		}})
		return nil
	})
}
