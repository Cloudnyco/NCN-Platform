// mail_notify_template.go — embedded logo + branded HTML email helpers
// for all system mail sent from noreply@example.com.
//
// Two public flavors:
//   - sendForwardVerification  (HTML with CTA button — forward-address proof)
//   - sendSystemMail           (HTML info-style — no CTA, just headline + paragraphs)
//
// Both go through buildMIMEWithLogo, which assembles a multipart/related
// envelope: multipart/alternative (text+html) + inline WebP logo attached
// as a Content-ID image. The logo is compiled in via go:embed (~9.5 KB),
// referenced by `cid:logo.webp` in the HTML, and emitted without
// filename=/name= params so receiving clients render it inline rather
// than treating it as an attachment.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

//go:embed ncnlogo6.webp
var ncnLogoBytes []byte

const (
	noreplyMailboxLocal = "noreply"
	noreplyDisplay      = "Acme Net <noreply@example.com>"
)

// sendForwardVerification dispatches the "click this link to confirm
// forwarding" email. {mailbox} is the OWNER (used for the salutation
// "Dear <local-part>"); {fwd} is the destination being asked to consent.
func sendForwardVerification(mailbox, fwd string) error {
	if mailSvcGlobal == nil {
		return errors.New("forward-verify: mail service not initialised")
	}
	noreply := noreplyMailboxLocal + "@" + mailDomain
	pw, ok := mailSvcGlobal.lookup(noreply)
	if !ok {
		return fmt.Errorf("forward-verify: %s credential not stashed — run `ncn-mail admin setup-noreply`", noreply)
	}

	_, url, err := mintForwardVerifyToken(mailbox, fwd)
	if err != nil {
		return fmt.Errorf("mint token: %w", err)
	}

	// Local-part of the OWNER is used in salutation. We deliberately
	// don't reveal the owner's full address to the recipient in the
	// salutation (we use the local-part), but the From / "owner"
	// disclosure is in the body so the recipient knows whose request it is.
	ownerLocal := strings.SplitN(mailbox, "@", 2)[0]

	subject := "Email Verification"
	plain := buildForwardVerifyPlain(ownerLocal, mailbox, fwd, url)
	html := buildForwardVerifyHTML(ownerLocal, mailbox, fwd, url)

	msg, err := buildMIMEWithLogo(noreplyDisplay, fwd, subject, plain, html)
	if err != nil {
		return err
	}

	tc := &tls.Config{ServerName: mailHost, MinVersion: tls.VersionTLS12}
	addr := mailHost + ":" + mailSubmissionPort
	if err := smtpSend(addr, tc, noreply, noreply, pw, []string{fwd}, msg); err != nil {
		return fmt.Errorf("smtp submit: %w", err)
	}
	log.Printf("forward-verify: sent to %s on behalf of %s", fwd, mailbox)
	return nil
}

func buildForwardVerifyPlain(ownerLocal, owner, fwd, url string) string {
	return fmt.Sprintf(`Dear %s,

Someone — identifying as the owner of the Acme Net mailbox
%s — has requested that incoming mail be forwarded to this address
(%s).

If you authorise this, please confirm by opening the link below within
the next 24 hours:

    %s

If you do NOT recognise this request, simply ignore this message and
nothing will be changed. The link is single-use and will expire on its
own.

Regards,
Acme Net Team

Acme Net · All Rights Reserved
Terms — https://example.com/terms · Privacy — https://example.com/privacy
`, ownerLocal, owner, fwd, url)
}

func buildForwardVerifyHTML(ownerLocal, owner, fwd, url string) string {
	// Minimal, dark-themed but mail-client-safe inline styles. Wide
	// safety margin: tables-only layout, no flex/grid, no @media, no
	// external CSS. Logo references the inline CID we attach below.
	return fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"></head>
<body style="margin:0;padding:0;background:#0a0a0a;font-family:Arial,Helvetica,sans-serif;color:#d1d5db;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" style="background:#0a0a0a;">
    <tr><td align="center" style="padding:28px 16px 12px 16px;">
      <!-- ncnlogo6 is white wordmark + blue icon on a solid #000 surface
           that blends with this #0a0a0a body. No wrapper pill needed —
           the logo carries its own contrast against the body. -->
      <img src="cid:logo.webp" alt="Acme Net" width="320" style="display:block;width:320px;max-width:80%%;height:auto;">
    </td></tr>
    <tr><td align="center" style="padding:0 16px;">
      <table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;background:#111111;border:1px solid #1f1f1f;">
        <tr><td style="padding:28px 32px 8px 32px;">
          <h1 style="margin:0 0 16px 0;font-size:18px;font-weight:600;color:#f3f4f6;letter-spacing:0.02em;">Email Verification</h1>
          <p style="margin:0 0 14px 0;font-size:14px;line-height:1.6;color:#d1d5db;">Dear %s,</p>
          <p style="margin:0 0 14px 0;font-size:14px;line-height:1.6;color:#d1d5db;">
            The owner of the Acme Net mailbox
            <strong style="color:#86efac;">%s</strong>
            has requested that incoming mail be forwarded to this
            address (<strong style="color:#fbbf24;">%s</strong>).
          </p>
          <p style="margin:0 0 14px 0;font-size:14px;line-height:1.6;color:#d1d5db;">
            If you authorise this, please confirm by clicking the button
            below within the next 24 hours:
          </p>
        </td></tr>
        <tr><td align="center" style="padding:8px 32px 28px 32px;">
          <table role="presentation" cellpadding="0" cellspacing="0" border="0">
            <tr><td style="background:#10b981;border-radius:4px;">
              <a href="%s" target="_blank" style="display:inline-block;padding:12px 28px;font-size:14px;font-weight:600;color:#0a0a0a;text-decoration:none;letter-spacing:0.02em;">Confirm Forwarding</a>
            </td></tr>
          </table>
        </td></tr>
        <tr><td style="padding:0 32px 24px 32px;">
          <p style="margin:0 0 14px 0;font-size:12px;line-height:1.5;color:#6b7280;">
            If the button doesn't open, copy this URL into your browser:
          </p>
          <p style="margin:0 0 14px 0;font-size:11px;line-height:1.4;color:#9ca3af;word-break:break-all;">
            %s
          </p>
          <p style="margin:0 0 0 0;font-size:12px;line-height:1.5;color:#6b7280;">
            If you do not recognise this request, ignore this message and
            nothing will be changed. The link is single-use and will
            expire on its own.
          </p>
        </td></tr>
      </table>
    </td></tr>
    <tr><td align="center" style="padding:24px 16px 8px 16px;font-size:12px;color:#6b7280;line-height:1.5;">
      Regards,<br>
      <strong style="color:#9ca3af;">Acme Net Team</strong>
    </td></tr>
    <tr><td align="center" style="padding:0 16px 4px 16px;font-size:11px;color:#4b5563;letter-spacing:0.04em;">
      Acme Net · All Rights Reserved
    </td></tr>
    <tr><td align="center" style="padding:0 16px 32px 16px;font-size:11px;line-height:1.6;">
      <a href="https://example.com/terms"   style="color:#9ca3af;text-decoration:none;">Terms</a>
      <span style="color:#4b5563;"> | </span>
      <a href="https://example.com/privacy" style="color:#9ca3af;text-decoration:none;">Privacy</a>
    </td></tr>
  </table>
</body></html>`,
		htmlEscape(ownerLocal), htmlEscape(owner), htmlEscape(fwd),
		htmlEscape(url), htmlEscape(url))
}

// sendSystemMail dispatches a system notification (password-reset ack,
// passkey-registered alert, etc.) using the SAME dark-themed HTML chrome
// + inline logo as the forward-verification email. No CTA button — just
// headline + paragraphs.
//
// `subject` lands in the mail-client envelope subject line.
// `headline` is the big <h1> at the top of the card (e.g. "New passkey
// added"); it doubles as the X-NCN-Notification subtype for log greps.
// `paragraphs` are rendered as <p>…</p> in HTML and joined with blank
// lines in the text/plain alternative. Linebreaks inside a paragraph
// are preserved in both renderings.
func sendSystemMail(to, subject, headline string, paragraphs []string) error {
	if mailSvcGlobal == nil {
		return errors.New("system-mail: mail service not initialised")
	}
	noreply := noreplyMailboxLocal + "@" + mailDomain
	pw, ok := mailSvcGlobal.lookup(noreply)
	if !ok {
		return fmt.Errorf("system-mail: %s credential not stashed — run `ncn-mail admin setup-noreply`", noreply)
	}

	plain := buildSystemMailPlain(to, headline, paragraphs)
	html := buildSystemMailHTML(to, headline, paragraphs)

	msg, err := buildMIMEWithLogo(noreplyDisplay, to, subject, plain, html)
	if err != nil {
		return err
	}

	tc := &tls.Config{ServerName: mailHost, MinVersion: tls.VersionTLS12}
	addr := mailHost + ":" + mailSubmissionPort
	if err := smtpSend(addr, tc, noreply, noreply, pw, []string{to}, msg); err != nil {
		return fmt.Errorf("smtp submit: %w", err)
	}
	log.Printf("system-mail: sent %q to %s from %s", headline, to, noreply)
	return nil
}

// extractSalutation pops a leading "Hi …" / "Hello …" / "Dear …" / "Hey …"
// off the front of `paragraphs` and returns it as a distinct salutation
// line, alongside the remaining body paragraphs.
//
// Reason this exists: the template renders a greeting line BEFORE the
// paragraphs. Callers that want a personalized greeting ("Hi Alice,")
// used to put it as their first paragraph — which produced
//
//	Hi,
//	Hi Alice,
//	(body…)
//
// The auto-detect lets callers keep writing natural opening lines
// ("Hi Alice,") without needing a separate Greeting parameter; the
// template picks it up and replaces the default.
//
// If no caller-supplied greeting is found, the default is now
// derived from the recipient address — `alice@…` → "Hi Alice,",
// `张三@…` → "Hi 张三,", role mailboxes → bare "Hi,". This replaces
// the previous hardcoded "Hi," that made every system email read like
// it was addressed to nobody.
func extractSalutation(to string, paragraphs []string) (salutation string, rest []string) {
	def := defaultSalutationFor(to)
	if len(paragraphs) == 0 {
		return def, nil
	}
	first := strings.TrimSpace(paragraphs[0])
	if greetingRE.MatchString(first) {
		return first, paragraphs[1:]
	}
	return def, paragraphs
}

// defaultSalutationFor derives a personalised "Hi <name>," from an email
// local-part. Role mailboxes (postmaster/noc/abuse/...) and ambiguous
// local-parts get the bare "Hi," — addressing the postmaster mailbox
// as "Hi Postmaster," reads like robot fan-mail.
//
// For `first.last@…` / `first_last@…` we use the first segment.
// ASCII names get title-cased; non-ASCII names (Chinese, Japanese, etc.)
// pass through as-is — title-case rules don't make sense for ideographs.
func defaultSalutationFor(to string) string {
	local, _, ok := strings.Cut(to, "@")
	if !ok || local == "" {
		return "Hi,"
	}
	// Role mailboxes — these aren't people, no personal salutation.
	switch strings.ToLower(local) {
	case "postmaster", "hostmaster", "noc", "abuse", "security",
		"admin", "administrator", "support", "info", "contact",
		"noreply", "no-reply", "mailer-daemon", "webmaster", "ops",
		"helpdesk", "team", "billing":
		return "Hi,"
	}
	// Reduce first.last / first_last / first-last / first+tag → first.
	name := local
	for _, sep := range []string{".", "_", "+"} {
		if i := strings.Index(name, sep); i > 0 {
			name = name[:i]
		}
	}
	// Bail if what's left is too short to be a name (single letter,
	// all-digits, etc.) — better a bare "Hi," than "Hi A,".
	if len(name) < 2 || allDigitsOrPunct(name) {
		return "Hi,"
	}
	// Title-case the first byte if it's ASCII lowercase. Non-ASCII
	// (Chinese, Japanese, Korean, accented Latin) passes through —
	// trying to "title-case" 张 or é would either no-op or corrupt.
	if name[0] >= 'a' && name[0] <= 'z' {
		name = string(name[0]-32) + name[1:]
	}
	return "Hi " + name + ","
}

func allDigitsOrPunct(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127 {
			return false
		}
	}
	return true
}

// Matches the FIRST word of the paragraph being one of the common
// English/Chinese greetings, possibly followed by a name + comma.
// Case-insensitive on the English half; the Chinese forms (你好/您好)
// are unambiguous so no flag needed.
var greetingRE = regexp.MustCompile(`(?i)^(hi|hello|hey|dear|greetings|你好|您好|hi 各位|hello 各位)\b`)

// buildSystemMailPlain renders the text/plain alternative. The salutation
// is normally "Hi," but if the caller's first paragraph already starts
// with a greeting we use that instead so the recipient sees a single
// personalized opening line rather than two.
func buildSystemMailPlain(to, headline string, paragraphs []string) string {
	salutation, body := extractSalutation(to, paragraphs)
	var sb strings.Builder
	sb.WriteString(headline + "\n")
	sb.WriteString(strings.Repeat("─", len([]rune(headline))) + "\n\n")
	sb.WriteString(salutation + "\n\n")
	for _, p := range body {
		sb.WriteString(p)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Regards,\nAcme Net Team\n\n")
	sb.WriteString("Acme Net · All Rights Reserved\n")
	sb.WriteString("Terms — https://example.com/terms · Privacy — https://example.com/privacy\n")
	return sb.String()
}

// buildSystemMailHTML renders the HTML alternative. Same chrome as the
// forward-verification email: logo header → dark card with h1 + paragraphs
// → footer. No CTA button (these notifications don't have an action link).
func buildSystemMailHTML(to, headline string, paragraphs []string) string {
	// Pull a salutation off the front so the card renders ONE greeting
	// line — either the caller's personalized one ("Hi Alice,") or the
	// recipient-derived default ("Hi alice," for alice@…, "Hi," for
	// role mailboxes). Body paragraphs follow below.
	salutation, body := extractSalutation(to, paragraphs)
	// Build the body paragraph <p> tags. Each paragraph is wrapped in a
	// styled <p>; newlines inside are converted to <br> so multi-line
	// content (e.g. the "name:" / "time:" key-value rows in the passkey
	// email) keeps its shape.
	var pBuf strings.Builder
	for _, p := range body {
		pBuf.WriteString(`<p style="margin:0 0 14px 0;font-size:14px;line-height:1.6;color:#d1d5db;">`)
		pBuf.WriteString(strings.ReplaceAll(htmlEscape(p), "\n", "<br>"))
		pBuf.WriteString("</p>")
	}

	return fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"></head>
<body style="margin:0;padding:0;background:#0a0a0a;font-family:Arial,Helvetica,sans-serif;color:#d1d5db;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" style="background:#0a0a0a;">
    <tr><td align="center" style="padding:28px 16px 12px 16px;">
      <img src="cid:logo.webp" alt="Acme Net" width="320" style="display:block;width:320px;max-width:80%%;height:auto;">
    </td></tr>
    <tr><td align="center" style="padding:0 16px;">
      <table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="max-width:560px;background:#111111;border:1px solid #1f1f1f;">
        <tr><td style="padding:28px 32px 20px 32px;">
          <h1 style="margin:0 0 16px 0;font-size:18px;font-weight:600;color:#f3f4f6;letter-spacing:0.02em;">%s</h1>
          <p style="margin:0 0 14px 0;font-size:14px;line-height:1.6;color:#d1d5db;">%s</p>
          %s
        </td></tr>
      </table>
    </td></tr>
    <tr><td align="center" style="padding:24px 16px 8px 16px;font-size:12px;color:#6b7280;line-height:1.5;">
      Regards,<br>
      <strong style="color:#9ca3af;">Acme Net Team</strong>
    </td></tr>
    <tr><td align="center" style="padding:0 16px 4px 16px;font-size:11px;color:#4b5563;letter-spacing:0.04em;">
      Acme Net · All Rights Reserved
    </td></tr>
    <tr><td align="center" style="padding:0 16px 32px 16px;font-size:11px;line-height:1.6;">
      <a href="https://example.com/terms"   style="color:#9ca3af;text-decoration:none;">Terms</a>
      <span style="color:#4b5563;"> | </span>
      <a href="https://example.com/privacy" style="color:#9ca3af;text-decoration:none;">Privacy</a>
    </td></tr>
  </table>
</body></html>`, htmlEscape(headline), htmlEscape(salutation), pBuf.String())
}

// htmlEscape — minimal escaping for substitution into HTML attribute / text
// contexts. Adequate for our case (we only substitute known-shape inputs:
// a mailbox local-part, a full address validated by mail.ParseAddress, an
// HMAC token URL we minted ourselves).
func htmlEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(s)
}

// buildMIMEWithLogo assembles a multipart/related body whose first part is
// multipart/alternative (text+html) and whose second part is the embedded
// WebP referenced by `cid:logo.webp` in the HTML. The result is a full RFC
// 5322 message ready for smtpSend.
func buildMIMEWithLogo(from, to, subject, textBody, htmlBody string) ([]byte, error) {
	relBoundary := "ncn_rel_" + randomTokenHex(8)
	altBoundary := "ncn_alt_" + randomTokenHex(8)

	mid := fmt.Sprintf("<verify-%d.%s@%s>", time.Now().UnixNano(), randomTokenHex(6), mailHost)
	// random cid so two emails sent in the same session don't share cid
	cidNonce := make([]byte, 4)
	_, _ = rand.Read(cidNonce)
	cid := fmt.Sprintf("logo-%s@%s", base64.RawURLEncoding.EncodeToString(cidNonce), mailHost)
	// Replace the placeholder "cid:logo.webp" in the HTML with our concrete CID.
	htmlBody = strings.ReplaceAll(htmlBody, "cid:logo.webp", "cid:"+cid)

	var buf bytes.Buffer
	headers := []string{
		"From: " + from,
		"To: " + to,
		"Subject: " + mimeHeader(subject),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-ID: " + mid,
		"MIME-Version: 1.0",
		"X-NCN-Notification: verify-forward",
		"Auto-Submitted: auto-generated",
		"Precedence: bulk",
		`Content-Type: multipart/related; type="multipart/alternative"; boundary="` + relBoundary + `"`,
	}
	buf.WriteString(strings.Join(headers, "\r\n"))
	buf.WriteString("\r\n\r\nThis is a multi-part message in MIME format.\r\n")

	// --- part 1: multipart/alternative (text + html) ---
	buf.WriteString("--" + relBoundary + "\r\n")
	buf.WriteString(`Content-Type: multipart/alternative; boundary="` + altBoundary + "\"\r\n\r\n")

	buf.WriteString("--" + altBoundary + "\r\n")
	buf.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(textBody)
	buf.WriteString("\r\n")

	buf.WriteString("--" + altBoundary + "\r\n")
	buf.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.WriteString(htmlBody)
	buf.WriteString("\r\n")
	buf.WriteString("--" + altBoundary + "--\r\n")

	// --- part 2: the inline logo ---
	//
	// CRITICAL: do NOT include `filename="..."` on the Content-Disposition
	// of an inline image. Most clients (Outlook, Gmail, Apple Mail) treat
	// the presence of a filename parameter — even with disposition=inline —
	// as a signal that the part is an attachment and surface it in the
	// attachment tray instead of rendering it inline via the cid: ref.
	//
	// Same goes for `name=` on Content-Type. Pure inline images use only
	// Content-ID + Content-Type (no params) + base64 body.
	if len(ncnLogoBytes) > 0 {
		buf.WriteString("--" + relBoundary + "\r\n")
		buf.WriteString("Content-Type: image/webp\r\n")
		buf.WriteString("Content-Disposition: inline\r\n")
		buf.WriteString("Content-ID: <" + cid + ">\r\n")
		buf.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(ncnLogoBytes)
		for i := 0; i < len(enc); i += 76 {
			end := i + 76
			if end > len(enc) {
				end = len(enc)
			}
			buf.WriteString(enc[i:end])
			buf.WriteString("\r\n")
		}
	}
	buf.WriteString("--" + relBoundary + "--\r\n")

	return buf.Bytes(), nil
}
