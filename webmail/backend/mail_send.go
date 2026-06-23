// mail_send.go — outbound SMTP submission + multipart MIME assembly.
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-sasl"
)

// POST /api/v1/mail/send
//
// Two accepted Content-Types:
//
//	application/json   — { "to","cc","bcc","subject","body" } (text/plain only)
//	multipart/form-data — same fields as form values; plus zero-or-more
//	                       file inputs named "attachments" (RFC 7578).
//	                       Result: multipart/mixed MIME with a text/plain
//	                       body part + each attachment as a base64-encoded
//	                       part with its original Content-Type + filename.
//
// Body is always treated as text/plain UTF-8. HTML composing is a future
// deliverable.
func (m *mailService) handleSend(w http.ResponseWriter, r *http.Request) {
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

	type sendFields struct {
		To, Cc, Bcc, Subject, Body, Format string
	}
	type attachUpload struct {
		Filename    string
		ContentType string
		Data        []byte
	}

	var req sendFields
	var attachments []attachUpload

	ctype := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ctype, "multipart/form-data"):
		// 25 MB cap (everything combined: form fields + all files).
		if err := r.ParseMultipartForm(25 * 1024 * 1024); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "parse multipart: " + err.Error()})
			return
		}
		req.To = r.FormValue("to")
		req.Cc = r.FormValue("cc")
		req.Bcc = r.FormValue("bcc")
		req.Subject = r.FormValue("subject")
		req.Body = r.FormValue("body")
		req.Format = r.FormValue("format")
		if r.MultipartForm != nil {
			for _, fh := range r.MultipartForm.File["attachments"] {
				if fh.Size > 25*1024*1024 {
					writeJSON(w, http.StatusBadRequest, envelope{OK: false,
						Error: "attachment too large: " + fh.Filename})
					return
				}
				f, err := fh.Open()
				if err != nil {
					writeJSON(w, http.StatusBadRequest, envelope{OK: false,
						Error: "open attachment " + fh.Filename + ": " + err.Error()})
					return
				}
				data, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil {
					writeJSON(w, http.StatusBadRequest, envelope{OK: false,
						Error: "read attachment " + fh.Filename + ": " + err.Error()})
					return
				}
				ct := fh.Header.Get("Content-Type")
				if ct == "" {
					ct = "application/octet-stream"
				}
				// CRLF in filename is a header-injection vector — strip.
				name := strings.ReplaceAll(strings.ReplaceAll(fh.Filename, "\r", ""), "\n", "")
				name = strings.ReplaceAll(name, `"`, `'`)
				attachments = append(attachments, attachUpload{
					Filename: name, ContentType: ct, Data: data,
				})
			}
		}

	default:
		if err := json.NewDecoder(io.LimitReader(r.Body, mailMaxBody)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad json"})
			return
		}
	}

	// SMTP header injection defense — see comment in critical fix #1.
	for _, field := range []struct{ name, val string }{
		{"subject", req.Subject},
		{"to", req.To}, {"cc", req.Cc}, {"bcc", req.Bcc},
	} {
		if strings.ContainsAny(field.val, "\r\n") {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false,
				Error: "header injection rejected in field: " + field.name})
			return
		}
	}

	to := splitAddrList(req.To)
	cc := splitAddrList(req.Cc)
	bcc := splitAddrList(req.Bcc)
	if len(to)+len(cc)+len(bcc) == 0 {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "at least one recipient required"})
		return
	}
	for _, a := range append(append(append([]string{}, to...), cc...), bcc...) {
		if _, err := mail.ParseAddress(a); err != nil {
			writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "invalid recipient: " + a})
			return
		}
	}

	// Build RFC 5322 message. Postfix on pop-03 will DKIM-sign on egress.
	var buf bytes.Buffer
	mid := fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), randomTokenHex(6), mailHost)
	commonHeaders := []string{
		"From: " + c.Mailbox,
		"To: " + strings.Join(to, ", "),
	}
	if len(cc) > 0 {
		commonHeaders = append(commonHeaders, "Cc: "+strings.Join(cc, ", "))
	}
	commonHeaders = append(commonHeaders,
		"Subject: "+mimeHeader(req.Subject),
		"Date: "+time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: "+mid,
		"MIME-Version: 1.0",
		"User-Agent: NCN-Webmail/1",
	)

	isHTML := strings.EqualFold(req.Format, "html")

	// Helper: write a multipart/alternative block (text + html parts) to buf.
	writeAlternative := func(boundary string) {
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(htmlToPlain(req.Body))
		buf.WriteString("\r\n")
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(req.Body)
		buf.WriteString("\r\n")
		buf.WriteString("--" + boundary + "--\r\n")
	}

	switch {
	case len(attachments) == 0 && !isHTML:
		// Single-part text/plain (historical path)
		commonHeaders = append(commonHeaders,
			`Content-Type: text/plain; charset="UTF-8"`,
			"Content-Transfer-Encoding: 8bit",
		)
		buf.WriteString(strings.Join(commonHeaders, "\r\n"))
		buf.WriteString("\r\n\r\n")
		buf.WriteString(req.Body)

	case len(attachments) == 0 && isHTML:
		// multipart/alternative (text + html, no attachments)
		altBoundary := "ncn_alt_" + randomTokenHex(8)
		commonHeaders = append(commonHeaders,
			`Content-Type: multipart/alternative; boundary="`+altBoundary+`"`,
		)
		buf.WriteString(strings.Join(commonHeaders, "\r\n"))
		buf.WriteString("\r\n\r\nThis is a multi-part message in MIME format.\r\n")
		writeAlternative(altBoundary)

	default:
		// multipart/mixed wrapping:
		//   - multipart/alternative (if HTML) or text/plain part
		//   - each attachment
		mixedBoundary := "ncn_mix_" + randomTokenHex(8)
		commonHeaders = append(commonHeaders,
			`Content-Type: multipart/mixed; boundary="`+mixedBoundary+`"`,
		)
		buf.WriteString(strings.Join(commonHeaders, "\r\n"))
		buf.WriteString("\r\n\r\nThis is a multi-part message in MIME format.\r\n")

		if isHTML {
			altBoundary := "ncn_alt_" + randomTokenHex(8)
			buf.WriteString("--" + mixedBoundary + "\r\n")
			buf.WriteString(`Content-Type: multipart/alternative; boundary="` + altBoundary + "\"\r\n\r\n")
			writeAlternative(altBoundary)
		} else {
			buf.WriteString("--" + mixedBoundary + "\r\n")
			buf.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
			buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
			buf.WriteString(req.Body)
			buf.WriteString("\r\n")
		}

		// attachments
		for _, a := range attachments {
			buf.WriteString("--" + mixedBoundary + "\r\n")
			buf.WriteString("Content-Type: " + a.ContentType + "; name=\"" + a.Filename + "\"\r\n")
			buf.WriteString(`Content-Disposition: attachment; filename="` + a.Filename + "\"\r\n")
			buf.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
			enc := base64.StdEncoding.EncodeToString(a.Data)
			for i := 0; i < len(enc); i += 76 {
				end := i + 76
				if end > len(enc) {
					end = len(enc)
				}
				buf.WriteString(enc[i:end])
				buf.WriteString("\r\n")
			}
		}
		buf.WriteString("--" + mixedBoundary + "--\r\n")
	}

	envelopeRcpts := append(append([]string{}, to...), cc...)
	envelopeRcpts = append(envelopeRcpts, bcc...)
	envelopeRcpts = stripAddrNames(envelopeRcpts)
	from := stripAddrName(c.Mailbox)

	// Submit via SMTP STARTTLS on 587 using SASL PLAIN.
	tc := &tls.Config{ServerName: mailHost, MinVersion: tls.VersionTLS12}
	addr := mailHost + ":" + mailSubmissionPort
	if err := smtpSend(addr, tc, from, c.Mailbox, pw, envelopeRcpts, buf.Bytes()); err != nil {
		log.Printf("mail: send from %s: %v", c.Mailbox, err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: "submission failed: " + err.Error()})
		return
	}

	// IMAP APPEND a copy to Sent (already auto-created via dovecot's
	// 15-mailboxes.conf with auto = subscribe). Best-effort: a failure
	// here doesn't undo the SMTP send, but we log it so a missing-Sent
	// regression is visible.
	if err := appendToSent(c.Mailbox, pw, buf.Bytes()); err != nil {
		log.Printf("mail: append-to-Sent failed for %s: %v (mail was sent OK)", c.Mailbox, err)
	}

	writeJSON(w, http.StatusOK, envelope{OK: true, Data: map[string]any{
		"message_id": mid,
		"recipients": envelopeRcpts,
	}})
}

// appendToSent opens a fresh IMAP connection, ensures the Sent folder
// exists, and APPENDs the given RFC 5322 bytes with the \Seen flag.
// Returns early without error if the message size is implausibly small
// (defense against empty buffers).
func appendToSent(mailbox, password string, msg []byte) error {
	if len(msg) < 32 {
		return fmt.Errorf("message too small (%d bytes)", len(msg))
	}
	ic, err := dialIMAP(mailbox, password)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer ic.Close()

	// auto = subscribe in dovecot already takes care of creation on
	// account boot; but defense-in-depth — Create is idempotent enough,
	// errors on existing mailbox are swallowed.
	_ = ic.Create("Sent", nil).Wait()

	ac := ic.Append("Sent", int64(len(msg)), &imap.AppendOptions{
		Flags: []imap.Flag{imap.FlagSeen},
		Time:  time.Now(),
	})
	if _, err := ac.Write(msg); err != nil {
		return fmt.Errorf("append-write: %w", err)
	}
	if err := ac.Close(); err != nil {
		return fmt.Errorf("append-close: %w", err)
	}
	if _, err := ac.Wait(); err != nil {
		return fmt.Errorf("append-wait: %w", err)
	}
	return nil
}

func smtpSend(addr string, tc *tls.Config, from, user, pass string, rcpts []string, msg []byte) error {
	cl, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer cl.Close()
	if err := cl.Hello(mailHost); err != nil {
		return fmt.Errorf("ehlo: %w", err)
	}
	if ok, _ := cl.Extension("STARTTLS"); ok {
		if err := cl.StartTLS(tc); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if ok, _ := cl.Extension("AUTH"); ok {
		auth := sasl.NewPlainClient("", user, pass)
		mech, ir, err := auth.Start()
		if err != nil {
			return fmt.Errorf("sasl start: %w", err)
		}
		// net/smtp's Auth interface is opinionated; use the SASL bytes directly.
		_ = mech
		if err := smtpRawAuth(cl, "PLAIN", ir, auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := cl.Mail(from); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	for _, rc := range rcpts {
		if err := cl.Rcpt(rc); err != nil {
			return fmt.Errorf("rcpt %s: %w", rc, err)
		}
	}
	wc, err := cl.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return cl.Quit()
}

// smtpRawAuth drives a SASL exchange over net/smtp using its text protocol.
// net/smtp's Auth interface predates go-sasl, so we wrap the SASL client
// manually.
func smtpRawAuth(cl *smtp.Client, mech string, initialResp []byte, auth sasl.Client) error {
	// net/smtp does not expose raw cmd, but its Auth path expects a custom
	// Auth implementation. We adapt sasl.Client → smtp.Auth via this shim.
	return cl.Auth(saslSMTPAdapter{mech: mech, ir: initialResp, c: auth})
}

type saslSMTPAdapter struct {
	mech string
	ir   []byte
	c    sasl.Client
}

func (a saslSMTPAdapter) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	if len(a.ir) > 0 {
		return a.mech, a.ir, nil
	}
	mech, ir, err := a.c.Start()
	if err != nil {
		return "", nil, err
	}
	return mech, ir, nil
}

func (a saslSMTPAdapter) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return a.c.Next(fromServer)
}
