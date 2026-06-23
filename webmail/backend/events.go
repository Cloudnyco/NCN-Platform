// events.go — SSE endpoint that pushes new-mail notifications to the
// browser, backed by an IMAP IDLE long connection per request.
//
// Each call to /api/v1/mail/events opens its OWN imap connection. When the
// browser disconnects (tab closed, network drop), ctx.Done fires and we
// tear the connection down. Multiple tabs from the same user = multiple
// connections. Fine at our user count.
package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

// GET /api/v1/mail/events  (mail session required)
//
// text/event-stream output:
//
//	event: ready    \n data: {"folder":"INBOX"}
//	event: mailbox  \n data: {"type":"exists","count":42}
//	: heartbeat                                            (every 30s)
func (m *mailService) handleEvents(w http.ResponseWriter, r *http.Request) {
	c, ok := mailClaimsFromRequest(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "not authenticated"})
		return
	}
	pw, ok := m.lookup(c.Mailbox)
	if !ok {
		writeJSON(w, http.StatusPreconditionRequired, envelope{OK: false, Error: "password not stashed"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: "no streaming"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer SSE
	w.Header().Set("Connection", "keep-alive")

	events := make(chan string, 16)

	// IMAP connection with unilateral data handler that pushes EXISTS events.
	tc := &tls.Config{ServerName: mailHost, MinVersion: tls.VersionTLS12}
	ic, err := imapclient.DialTLS(mailHost+":"+mailIMAPSPort, &imapclient.Options{
		TLSConfig: tc,
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(d *imapclient.UnilateralDataMailbox) {
				if d.NumMessages != nil {
					select {
					case events <- fmt.Sprintf(`{"type":"exists","count":%d}`, *d.NumMessages):
					default: // drop if buffer full
					}
				}
			},
			Expunge: func(seqNum uint32) {
				select {
				case events <- fmt.Sprintf(`{"type":"expunge","seq":%d}`, seqNum):
				default:
				}
			},
		},
	})
	if err != nil {
		http.Error(w, "imap dial: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer ic.Close()

	if err := ic.Login(c.Mailbox, pw).Wait(); err != nil {
		http.Error(w, "imap login: "+err.Error(), http.StatusBadGateway)
		return
	}
	if _, err := ic.Select("INBOX", nil).Wait(); err != nil {
		http.Error(w, "select INBOX: "+err.Error(), http.StatusBadGateway)
		return
	}

	idleCmd, err := ic.Idle()
	if err != nil {
		http.Error(w, "idle: "+err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("events: SSE+IDLE started for %s", c.Mailbox)

	// Initial event so the client knows we're connected.
	fmt.Fprintf(w, "event: ready\ndata: {\"folder\":\"INBOX\"}\n\n")
	flusher.Flush()

	ctx := r.Context()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			// Browser disconnected. Stop the IDLE cleanly so the server
			// doesn't hold a half-open connection.
			_ = idleCmd.Close()
			log.Printf("events: SSE+IDLE closed for %s", c.Mailbox)
			return

		case ev := <-events:
			if _, err := fmt.Fprintf(w, "event: mailbox\ndata: %s\n\n", ev); err != nil {
				return
			}
			flusher.Flush()

		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
