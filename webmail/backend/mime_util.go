// mime_util.go — MIME parsing + address/HTML helpers shared by every
// mail handler. Pure functions, no service state.
package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"regexp"
	"strings"

	"github.com/emersion/go-imap/v2"
	gomessage "github.com/emersion/go-message"
	// charset registers transcoders for common non-UTF-8 encodings
	// (ascii, iso-8859-*, gb2312, gb18030, big5, shift_jis, koi8-*…).
	// Without this blank import, any Content-Type with `charset=ascii`
	// or `charset=gbk` throws "unhandled charset" and parseMessage
	// degrades to a partial parse + a `warn` surfaced to the UI.
	_ "github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"
)

// attachmentMeta is the JSON-shaped descriptor of one MIME attachment we
// surface to the frontend (filename + content-type + size). The actual
// bytes are streamed separately via /api/v1/mail/attachment.
type attachmentMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

// parseMessage walks an RFC 5322 message body and returns the first
// text/plain part, the first text/html part, and a flat list of any
// attachment headers. Inline images keyed by Content-ID (the
// multipart/related layout used by our own noreply notifications, and
// by most marketing mail) are extracted and substituted directly into
// the HTML body as `data:` URLs — browsers don't speak the `cid:` URL
// scheme natively, so without this step inline logos render as broken
// images.
//
// `warn` is set when parsing partially succeeded — callers may still
// want to surface what they got.
func parseMessage(raw []byte) (text, html string, parts []attachmentMeta, warn error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		// Fall back to interpreting as a flat text/* message via go-message.
		ent, err2 := gomessage.Read(bytes.NewReader(raw))
		if err2 != nil {
			return string(raw), "", nil, err
		}
		ct, _, _ := ent.Header.ContentType()
		body, _ := io.ReadAll(ent.Body)
		if strings.HasPrefix(ct, "text/html") {
			return "", string(body), nil, nil
		}
		return string(body), "", nil, nil
	}
	defer mr.Close()

	// cid → "data:<content-type>;base64,<bytes>" for later substitution
	// into the HTML body. Built up as we walk inline parts; only applied
	// if we end up with an HTML body that references at least one cid:.
	inlines := make(map[string]string)
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			warn = err
			break
		}
		switch h := p.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := h.ContentType()
			body, _ := io.ReadAll(p.Body)
			if strings.HasPrefix(ct, "text/html") && html == "" {
				html = string(body)
				continue
			}
			if strings.HasPrefix(ct, "text/plain") && text == "" {
				text = string(body)
				continue
			}
			// Non-text inline part: most commonly an inline image
			// referenced via Content-ID by a multipart/related layout.
			// Capture its Content-ID + bytes for HTML substitution
			// below. The header's value is "<id@host>" — strip the
			// angle brackets so it matches the `cid:` form.
			cid := strings.Trim(h.Get("Content-ID"), "<>")
			if cid != "" && len(body) > 0 {
				inlines[cid] = "data:" + ct + ";base64," +
					base64.StdEncoding.EncodeToString(body)
			}
		case *gomail.AttachmentHeader:
			filename, _ := h.Filename()
			ct, _, _ := h.ContentType()
			body, _ := io.ReadAll(p.Body)
			parts = append(parts, attachmentMeta{
				Filename:    filename,
				ContentType: ct,
				Size:        len(body),
			})
			// CRITICAL: some senders (notably Outlook variants) mark
			// CID-referenced inline images as Content-Disposition:
			// attachment even though they are referenced by `cid:` in
			// the HTML body. If we only treat them as attachments the
			// HTML's `cid:foo` stays unresolved and the browser renders
			// a broken image. Detect by the presence of a Content-ID
			// header and ALSO record them in the inlines map.
			cid := strings.Trim(h.Get("Content-ID"), "<>")
			if cid != "" && strings.HasPrefix(ct, "image/") && len(body) > 0 {
				inlines[cid] = "data:" + ct + ";base64," +
					base64.StdEncoding.EncodeToString(body)
			}
		}
	}

	// Rewrite cid: references in the HTML body to data: URLs. We do a
	// straight string-replace per known CID rather than regex-rewriting,
	// so we never accidentally touch unrelated href/src attributes.
	if html != "" && len(inlines) > 0 {
		for cid, dataURL := range inlines {
			html = strings.ReplaceAll(html, "cid:"+cid, dataURL)
		}
	}
	// Rewrite external <img src="http(s)://..."> to our local img-proxy.
	// Privacy: every external reference now goes through ncn-mail, which
	// strips Referer/UA/cookies, so trackers can't correlate opens to a
	// specific user/time. Browser still benefits from per-URL caching.
	if html != "" {
		html = rewriteExternalImgs(html)
	}
	return
}

// rewriteExternalImgs runs every rewriting pass that nudges external
// image references through the local img-proxy. CSP on webmail is
// `img-src 'self' data: blob:` — every external URL that escapes
// these passes will be silently blocked by the browser and render as
// a broken image. So we cover the realistic universe of marketing-
// mail constructs:
//
//   1. <img src="http(s)://..."> — the obvious case
//   2. <img srcset="...">         — retina/responsive; browser prefers
//                                   srcset over src when present
//   3. <tag background="http...">  — legacy HTML email "background"
//                                   attribute (table/td/tr/body)
//   4. style="...url(http...)..."  — CSS background-image, background,
//                                   list-style-image, etc. Any tag.
//
// Each pass is idempotent — URLs already pointing at the proxy are
// left alone. Order doesn't matter; the passes don't overlap.
func rewriteExternalImgs(html string) string {
	html = rewriteImgSrc(html)
	html = rewriteImgSrcset(html)
	html = rewriteBackgroundAttr(html)
	html = rewriteStyleURL(html)
	return html
}

// Pass 1: <img src="http(s)://…">
func rewriteImgSrc(html string) string {
	return imgSrcRE.ReplaceAllStringFunc(html, func(m string) string {
		sub := imgSrcRE.FindStringSubmatch(m)
		if len(sub) < 6 {
			return m
		}
		// Group order:  1=before  2=open-quote  3=url  4=close-quote  5=after
		before, quote, urlStr, after := sub[1], sub[2], sub[3], sub[5]
		return "<img " + before + "src=" + quote + proxify(urlStr) + quote + after + ">"
	})
}

// Pass 2: <img ... srcset="url1 1x, url2 2x, …">
// Browsers PREFER srcset over src when both are present, so leaving
// srcset alone defeats pass 1 on every retina-capable marketing email.
// We split the value on commas, rewrite each URL, rejoin.
func rewriteImgSrcset(html string) string {
	return imgSrcsetRE.ReplaceAllStringFunc(html, func(m string) string {
		sub := imgSrcsetRE.FindStringSubmatch(m)
		if len(sub) < 5 {
			return m
		}
		before, quote, value, after := sub[1], sub[2], sub[3], sub[4]
		out := make([]string, 0, 4)
		for _, cand := range strings.Split(value, ",") {
			cand = strings.TrimSpace(cand)
			if cand == "" {
				continue
			}
			// Each candidate is "URL [descriptor]" where descriptor
			// is e.g. "2x" or "640w". Split on first whitespace.
			urlStr := cand
			descriptor := ""
			if i := strings.IndexAny(cand, " \t"); i > 0 {
				urlStr = cand[:i]
				descriptor = " " + strings.TrimSpace(cand[i:])
			}
			out = append(out, proxify(urlStr)+descriptor)
		}
		return "<img " + before + "srcset=" + quote + strings.Join(out, ", ") + quote + after + ">"
	})
}

// Pass 3: <table|td|tr|th|body|div background="http(s)://…">
// Legacy HTML-email background attribute. Same shape as src.
func rewriteBackgroundAttr(html string) string {
	return bgAttrRE.ReplaceAllStringFunc(html, func(m string) string {
		sub := bgAttrRE.FindStringSubmatch(m)
		if len(sub) < 6 {
			return m
		}
		open, before, quote, urlStr, after := sub[1], sub[2], sub[3], sub[4], sub[5]
		return "<" + open + " " + before + "background=" + quote + proxify(urlStr) + quote + after + ">"
	})
}

// Pass 4: style="…url(http(s)://…)…" inside any tag.
// Catches `background:url(...)`, `background-image:url(...)`,
// `list-style-image:url(...)`, etc. We don't need to be picky about
// which CSS property — anything pointing at an external image must
// go through the proxy or CSP will block it.
func rewriteStyleURL(html string) string {
	return cssURLRE.ReplaceAllStringFunc(html, func(m string) string {
		sub := cssURLRE.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		// Group 1 = optional quote inside url(), 2 = URL itself, 3 = closing quote
		quote, urlStr := sub[1], sub[2]
		return "url(" + quote + proxify(urlStr) + quote + ")"
	})
}

// proxify wraps an upstream URL into the local img-proxy reference.
// Idempotent — already-proxied URLs pass through unchanged.
func proxify(urlStr string) string {
	if strings.HasPrefix(urlStr, "/api/v1/mail/img-proxy") {
		return urlStr
	}
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		// Protocol-relative `//host/path` — make it https.
		if strings.HasPrefix(urlStr, "//") {
			urlStr = "https:" + urlStr
		} else {
			// Not an absolute http(s) URL; don't touch it.
			return urlStr
		}
	}
	return "/api/v1/mail/img-proxy?u=" + base64.RawURLEncoding.EncodeToString([]byte(urlStr))
}

var (
	// <img …src="http(s)://…"…>
	imgSrcRE = regexp.MustCompile(`(?is)<img\s+([^>]*?)src=(["'])(https?://[^"']+|//[^"']+)(["'])([^>]*)>`)

	// <img …srcset="…"…>
	imgSrcsetRE = regexp.MustCompile(`(?is)<img\s+([^>]*?)srcset=(["'])([^"']+)(["'])([^>]*)>`)

	// <table|td|tr|th|body|div …background="http(s)://…"…>
	bgAttrRE = regexp.MustCompile(`(?is)<(table|td|tr|th|body|div)\s+([^>]*?)background=(["'])(https?://[^"']+|//[^"']+)(["'])([^>]*)>`)

	// url(<optional-quote><http(s)://...></optional-quote>) — used inside
	// CSS values. Quote group can be ", ', or empty. We accept // for
	// protocol-relative URLs which proxify() will upgrade to https.
	cssURLRE = regexp.MustCompile(`url\(\s*(["']?)(https?://[^"')\s]+|//[^"')\s]+)\s*(["']?)\s*\)`)
)

// formatAddrs flattens an IMAP address list into RFC 5322 display form:
// `Name <mailbox@host>` (or bare `mailbox@host` when there's no name).
func formatAddrs(addrs []imap.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		s := a.Mailbox + "@" + a.Host
		if a.Name != "" {
			s = a.Name + " <" + s + ">"
		}
		out = append(out, s)
	}
	return out
}

// splitAddrList splits a comma-separated address line (as the user types
// it into the Compose form) into trimmed, non-empty entries.
func splitAddrList(s string) []string {
	if s = strings.TrimSpace(s); s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stripAddrName reduces `Name <addr@host>` to bare `addr@host`. Used when
// we need the raw envelope address (SMTP RCPT TO).
func stripAddrName(a string) string {
	if i := strings.LastIndex(a, "<"); i >= 0 {
		if j := strings.Index(a[i+1:], ">"); j >= 0 {
			return a[i+1 : i+1+j]
		}
	}
	return strings.TrimSpace(a)
}

func stripAddrNames(addrs []string) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = stripAddrName(a)
	}
	return out
}

// htmlBlockRE / htmlTagRE / htmlMultiNL together do a coarse HTML → text
// pass for populating the text/plain alternative inside a
// multipart/alternative MIME body. Not a real HTML parser — just enough
// for "user wrote some <b>/<br>/<ul> in the compose box" cases.
var (
	htmlBlockRE = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h[1-6])\b[^>]*>`)
	htmlTagRE   = regexp.MustCompile(`(?s)<[^>]+>`)
	htmlMultiNL = regexp.MustCompile(`\n{3,}`)
)

// htmlToPlain extracts a reasonable text/plain rendering from a snippet
// of HTML — block-level newlines, strip tags, decode a few common
// entities.
func htmlToPlain(s string) string {
	out := htmlBlockRE.ReplaceAllString(s, "\n")
	out = htmlTagRE.ReplaceAllString(out, "")
	out = strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&nbsp;", " ",
	).Replace(out)
	out = htmlMultiNL.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// mimeHeader encodes a header value per RFC 2047 when it contains
// non-ASCII; pure ASCII is shipped as-is so common headers stay readable.
func mimeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}
