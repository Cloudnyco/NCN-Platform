// mail_search.go — IMAP TEXT search over a folder.
//
//	GET /api/v1/mail/folders/{name}/search
package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// GET /api/v1/mail/folders/<name>/search?q=<text>&limit=50
//
// IMAP TEXT search: matches against the full RFC 822 header + body. Returns
// the same shape as /messages so the frontend can swap in the result list
// without changing rendering. Empty query → empty result.
func (m *mailService) handleSearch(w http.ResponseWriter, r *http.Request) {
	folder, err := pathSegmentAfter(r.URL.Path, "/api/v1/mail/folders/")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing folder"})
		return
	}
	folder = strings.TrimSuffix(folder, "/search")
	if folder == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "empty folder"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "query must be at least 2 chars"})
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}

	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		if _, err := ic.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			return fmt.Errorf("select %s: %w", folder, err)
		}
		// TEXT criterion searches headers + body together — close to the
		// "smart" search a normal user expects.
		sr, err := ic.Search(&imap.SearchCriteria{Text: []string{q}}, nil).Wait()
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}
		// SearchData.AllSeqNums returns sequence numbers; we want the most
		// recent N. Reverse so newest first.
		seqs := sr.AllSeqNums()
		total := len(seqs)
		if total == 0 {
			writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
				"folder": folder, "query": q, "total": 0, "messages": []any{},
			}})
			return nil
		}
		// Keep the last `limit` (highest seq numbers = newest).
		if total > limit {
			seqs = seqs[total-limit:]
		}
		seqSet := imap.SeqSetNum(seqs...)

		fetchOpts := &imap.FetchOptions{
			UID: true, Flags: true, InternalDate: true, RFC822Size: true,
			Envelope: true,
		}
		messages := ic.Fetch(seqSet, fetchOpts)
		var rows []map[string]any
		for {
			msg := messages.Next()
			if msg == nil {
				break
			}
			data, err := msg.Collect()
			if err != nil {
				return err
			}
			// nil-slice defence: see mail_list.go.
			flags := data.Flags
			if flags == nil {
				flags = []imap.Flag{}
			}
			row := map[string]any{
				"uid": uint32(data.UID), "seq": data.SeqNum,
				"size": data.RFC822Size, "flags": flags,
				"date": data.InternalDate.UTC().Format(time.RFC3339),
			}
			if env := data.Envelope; env != nil {
				row["subject"] = env.Subject
				row["from"] = formatAddrs(env.From)
				row["to"] = formatAddrs(env.To)
				row["message_id"] = env.MessageID
			}
			if row["from"] == nil { row["from"] = []string{} }
			if row["to"] == nil   { row["to"]   = []string{} }
			rows = append(rows, row)
		}
		if err := messages.Close(); err != nil {
			return err
		}
		// Reverse so newest first (matches handleListMessages).
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
			"folder":   folder,
			"query":    q,
			"total":    total,
			"messages": rows,
		}})
		return nil
	})
}
