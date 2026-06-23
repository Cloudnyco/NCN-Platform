// api_send.go — Resend-style transactional send API.
//
//	POST /api/v1/mail/api/send
//	Authorization: Bearer ncntok_<secret>
//	Content-Type: application/json
//	{
//	  "from":      "noc@example.com",      // must match the token's bound mailbox
//	  "to":        ["a@x.com","b@y.com"], // string or array; cc/bcc likewise
//	  "cc":        [],
//	  "bcc":       [],
//	  "subject":   "Hello",
//	  "html":      "<p>hi</p>",           // either/both of html, text
//	  "text":      "hi",
//	  "transport": "local"                // "local" (default) | "gmail"
//	}
//
// Two delivery paths, chosen per request:
//
//   - "local"  — injected into postfix over SMTP at 127.0.0.1:25. postfix's
//     `permit_mynetworks` accepts loopback without auth, and its
//     `smtpd_milters` (rspamd) DKIM-signs on egress, so mail leaves as a
//     properly authenticated @example.com message. No mailbox password
//     needed: the API key IS the authorisation. (We use SMTP-to-localhost
//     rather than the `sendmail` binary because the service runs with
//     ProtectSystem=strict + NoNewPrivileges, under which the setgid
//     `postdrop` maildrop injection hangs — outbound SMTP is unaffected.)
//
//   - "gmail"  — relayed through smtp.gmail.com:587 (SASL PLAIN, app
//     password) using the credentials in /etc/ncn-mail/gmail-relay.json.
//     Gmail rewrites From to the authenticated account UNLESS `from` is a
//     verified "send mail as" alias on that account, so we require
//     from ∈ gmail.allowed_from. Best deliverability to Gmail/Outlook inboxes.
//
// API sends do NOT append to a Sent folder (no mailbox password, and
// transactional senders don't expect it — same as Resend). The send is
// logged with the message-id for auditing.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

const gmailRelayPath = stateDir + "/gmail-relay.json"

// gmailRelayConfig is the on-disk shape of the Gmail relay credentials.
// SECRET file (app password) — 0600, never committed; back up after edits.
type gmailRelayConfig struct {
	Host        string   `json:"host"`         // default smtp.gmail.com
	Port        string   `json:"port"`         // default 587
	Username    string   `json:"username"`     // the Gmail account
	AppPassword string   `json:"app_password"` // 16-char Google app password
	AllowedFrom []string `json:"allowed_from"` // verified send-as identities
}

func loadGmailRelay() (*gmailRelayConfig, error) {
	data, err := os.ReadFile(gmailRelayPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("gmail relay not configured — run `ncn-mail gmail-setup`")
		}
		return nil, fmt.Errorf("read %s: %w", gmailRelayPath, err)
	}
	var c gmailRelayConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", gmailRelayPath, err)
	}
	if c.Host == "" {
		c.Host = "smtp.gmail.com"
	}
	if c.Port == "" {
		c.Port = "587"
	}
	if c.Username == "" || c.AppPassword == "" {
		return nil, errors.New("gmail relay config missing username/app_password")
	}
	if len(c.AllowedFrom) == 0 {
		c.AllowedFrom = []string{c.Username}
	}
	return &c, nil
}

func (c *gmailRelayConfig) allowsFrom(from string) bool {
	from = strings.ToLower(stripAddrName(from))
	for _, a := range c.AllowedFrom {
		if strings.EqualFold(stripAddrName(a), from) {
			return true
		}
	}
	return false
}

type apiSendService struct {
	tokens *apiTokenStore
}

func newAPISendService(tokens *apiTokenStore) *apiSendService {
	return &apiSendService{tokens: tokens}
}

// apiSendRequest accepts to/cc/bcc as either a JSON string or array of
// strings, so callers can write "to":"a@x.com" or "to":["a@x.com","b@y.com"].
type apiSendRequest struct {
	From      string      `json:"from"`
	To        flexAddrs   `json:"to"`
	Cc        flexAddrs   `json:"cc"`
	Bcc       flexAddrs   `json:"bcc"`
	Subject   string      `json:"subject"`
	HTML      string      `json:"html"`
	Text      string      `json:"text"`
	Transport string      `json:"transport"`
}

// flexAddrs unmarshals from either a string or []string.
type flexAddrs []string

func (f *flexAddrs) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*f = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	// Allow a comma/semicolon-separated single string too.
	*f = splitAddrList(s)
	return nil
}

func (s *apiSendService) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "POST only"})
		return
	}

	// --- bearer auth ---
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: "missing Bearer token"})
		return
	}
	tok, err := s.tokens.verify(strings.TrimPrefix(authz, "Bearer "))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, envelope{OK: false, Error: err.Error()})
		return
	}

	// --- parse ---
	var req apiSendRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
		return
	}
	transport := strings.ToLower(strings.TrimSpace(req.Transport))
	if transport == "" {
		transport = "local"
	}
	if transport != "local" && transport != "gmail" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "transport must be local or gmail"})
		return
	}
	if !tok.allowsTransport(transport) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false,
			Error: "this API key is not permitted to use transport " + transport})
		return
	}

	from := strings.TrimSpace(req.From)
	if from == "" {
		from = tok.Mailbox
	}
	// The token is bound to ONE sender identity — enforce it.
	if !strings.EqualFold(stripAddrName(from), tok.Mailbox) {
		writeJSON(w, http.StatusForbidden, envelope{OK: false,
			Error: fmt.Sprintf("from must be %s (the identity bound to this key)", tok.Mailbox)})
		return
	}

	// Header-injection guard on every header-bound field.
	for _, v := range append([]string{req.Subject, from},
		append(append(append([]string{}, req.To...), req.Cc...), req.Bcc...)...) {
		if strings.ContainsAny(v, "\r\n") {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "header injection rejected"})
			return
		}
	}

	to := stripAddrNames([]string(req.To))
	cc := stripAddrNames([]string(req.Cc))
	bcc := stripAddrNames([]string(req.Bcc))
	allRcpts := append(append(append([]string{}, to...), cc...), bcc...)
	if len(allRcpts) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "at least one recipient required"})
		return
	}
	for _, a := range allRcpts {
		if _, err := mail.ParseAddress(a); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid recipient: " + a})
			return
		}
	}
	if strings.TrimSpace(req.HTML) == "" && strings.TrimSpace(req.Text) == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "html or text body required"})
		return
	}

	msg, mid := buildOutboundMIME(from, to, cc, req.Subject, req.Text, req.HTML)

	switch transport {
	case "local":
		if err := localSMTPInject(stripAddrName(from), allRcpts, msg); err != nil {
			log.Printf("api-send: local inject from=%s key=%s: %v", from, tok.ID, err)
			writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: "local submission failed: " + err.Error()})
			return
		}
	case "gmail":
		cfg, err := loadGmailRelay()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, envelope{OK: false, Error: err.Error()})
			return
		}
		if !cfg.allowsFrom(from) {
			writeJSON(w, http.StatusForbidden, envelope{OK: false,
				Error: from + " is not a verified send-as identity on the Gmail relay account"})
			return
		}
		tc := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
		if err := smtpSend(cfg.Host+":"+cfg.Port, tc, stripAddrName(from), cfg.Username, cfg.AppPassword, allRcpts, msg); err != nil {
			log.Printf("api-send: gmail relay from=%s key=%s: %v", from, tok.ID, err)
			writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: "gmail relay failed: " + err.Error()})
			return
		}
	}

	log.Printf("api-send: OK transport=%s from=%s rcpts=%d key=%s(%s) mid=%s",
		transport, from, len(allRcpts), tok.ID, tok.Label, mid)
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"message_id": mid,
		"transport":  transport,
		"recipients": allRcpts,
	}})
}

// buildOutboundMIME assembles an RFC 5322 message. If both text and html are
// present it emits multipart/alternative; otherwise a single text/plain or
// text/html part. Returns the bytes and the generated Message-ID.
func buildOutboundMIME(from string, to, cc []string, subject, text, html string) ([]byte, string) {
	var buf bytes.Buffer
	mid := fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), randomTokenHex(6), mailHost)

	headers := []string{
		"From: " + from,
		"To: " + strings.Join(to, ", "),
	}
	if len(cc) > 0 {
		headers = append(headers, "Cc: "+strings.Join(cc, ", "))
	}
	headers = append(headers,
		"Subject: "+mimeHeader(subject),
		"Date: "+time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: "+mid,
		"MIME-Version: 1.0",
		"X-NCN-Source: api-send",
	)

	hasText := strings.TrimSpace(text) != ""
	hasHTML := strings.TrimSpace(html) != ""
	// If only HTML was supplied, derive a plaintext alternative so the
	// message isn't HTML-only (spam-filter and accessibility hygiene).
	if hasHTML && !hasText {
		text = htmlToPlain(html)
		hasText = true
	}

	switch {
	case hasHTML && hasText:
		boundary := "ncn_alt_" + randomTokenHex(8)
		headers = append(headers, `Content-Type: multipart/alternative; boundary="`+boundary+`"`)
		buf.WriteString(strings.Join(headers, "\r\n"))
		buf.WriteString("\r\n\r\nThis is a multi-part message in MIME format.\r\n")
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(text + "\r\n")
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(html + "\r\n")
		buf.WriteString("--" + boundary + "--\r\n")
	case hasHTML:
		headers = append(headers, `Content-Type: text/html; charset="UTF-8"`, "Content-Transfer-Encoding: 8bit")
		buf.WriteString(strings.Join(headers, "\r\n"))
		buf.WriteString("\r\n\r\n")
		buf.WriteString(html)
	default:
		headers = append(headers, `Content-Type: text/plain; charset="UTF-8"`, "Content-Transfer-Encoding: 8bit")
		buf.WriteString(strings.Join(headers, "\r\n"))
		buf.WriteString("\r\n\r\n")
		buf.WriteString(text)
	}
	return buf.Bytes(), mid
}

// localSMTPInject submits a full RFC 5322 message to postfix over SMTP at
// 127.0.0.1:25. No STARTTLS, no AUTH — postfix's permit_mynetworks accepts
// loopback, and its smtpd_milters (rspamd) DKIM-signs on egress. A 30s
// deadline guards against a wedged MTA hanging the request goroutine.
func localSMTPInject(from string, rcpts []string, msg []byte) error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:25", 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial localhost:25: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, "127.0.0.1")
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	if err := c.Hello(mailHost); err != nil {
		return fmt.Errorf("ehlo: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rc := range rcpts {
		if err := c.Rcpt(rc); err != nil {
			return fmt.Errorf("rcpt %s: %w", rc, err)
		}
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return c.Quit()
}
