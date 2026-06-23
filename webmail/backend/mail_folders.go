// mail_folders.go — IMAP folder listing.
//
//	GET /api/v1/mail/folders
package main

import (
	"net/http"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// GET /api/v1/mail/folders
func (m *mailService) handleFolders(w http.ResponseWriter, r *http.Request) {
	m.withIMAP(w, r, func(ic *imapclient.Client) error {
		list := ic.List("", "*", &imap.ListOptions{
			ReturnStatus: &imap.StatusOptions{NumMessages: true, NumUnseen: true},
		})
		var out []map[string]any
		for {
			mb := list.Next()
			if mb == nil {
				break
			}
			row := map[string]any{
				"name":      mb.Mailbox,
				"delimiter": string(mb.Delim),
				"flags":     mb.Attrs,
			}
			if mb.Status != nil {
				if mb.Status.NumMessages != nil {
					row["total"] = *mb.Status.NumMessages
				}
				if mb.Status.NumUnseen != nil {
					row["unseen"] = *mb.Status.NumUnseen
				}
			}
			out = append(out, row)
		}
		if err := list.Close(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, envelope{OK: true, Data: out})
		return nil
	})
}
