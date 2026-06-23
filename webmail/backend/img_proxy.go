// img_proxy.go — privacy-preserving inline-image proxy for webmail.
//
// Why this exists:
//   Marketing / notification mail (Mailchimp, ARDC, GitHub, etc.) typically
//   references logos and content images via external HTTPS URLs instead of
//   embedded `cid:` parts. They also embed 1×1 tracking pixels that GET
//   back to the sender on every open, leaking the recipient's IP +
//   read-time.
//
//   We rewrite every `<img src="http(s)://...">` in HTML message bodies
//   to `<img src="/api/v1/mail/img-proxy?u=<base64url>">` in parseMessage.
//   When the browser loads that URL, ncn-mail fetches the upstream image
//   ONCE per cache lifetime, strips request fingerprints (User-Agent
//   becomes a fixed string, no Referer, no cookies), and streams the
//   bytes back. The sender now sees one hit from pop-03's IP, can't tell
//   which user opened the mail or when (with caching), and can't
//   correlate multiple opens.
//
// SSRF defense:
//   - http(s) schemes only
//   - DNS-resolve the host; reject if ANY resolved IP is private,
//     loopback, link-local, multicast, broadcast, or unspecified
//   - 8-second timeout for both header and body
//   - 8 MB response cap (typical mail logos are <1 MB)
//   - Content-Type must start with `image/`
//
// Caching:
//   - The browser caches per <img src>; the URL embeds the upstream URL
//     so re-renders of the same email = cache hit, no extra upstream GET.
//   - We send `Cache-Control: private, max-age=86400` so the cache is
//     per-user (no public/CDN sharing) and lives 24h. Long enough to
//     dedupe reopens of the same email, short enough that fixed upstream
//     content updates within a day.
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	imgProxyMaxBytes  = 8 << 20 // 8 MB cap on a single fetched image
	imgProxyTimeout   = 8 * time.Second
	imgProxyUserAgent = "ncn-mail-img-proxy/1.0"
)

// GET /api/v1/mail/img-proxy?u=<base64url-encoded URL>
// Auth: mailbox session (mailReq middleware enforces).
func (m *mailService) handleImgProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, envelope{OK: false, Error: "GET only"})
		return
	}
	enc := r.URL.Query().Get("u")
	if enc == "" {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "missing u"})
		return
	}
	rawURL, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad encoding"})
		return
	}
	u, err := url.Parse(string(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		writeJSON(w, http.StatusBadRequest, envelope{OK: false, Error: "bad url"})
		return
	}
	if err := assertPublicHost(u.Hostname()); err != nil {
		log.Printf("img-proxy: blocked SSRF candidate %q: %v", u.String(), err)
		writeJSON(w, http.StatusForbidden, envelope{OK: false, Error: "destination not allowed"})
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, envelope{OK: false, Error: err.Error()})
		return
	}
	// Don't leak any fingerprint. The default Go UA already lacks a
	// Referer / cookies, but we set an explicit UA so log analysis on
	// upstream can identify us cleanly.
	req.Header.Set("User-Agent", imgProxyUserAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	client := &http.Client{Timeout: imgProxyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("img-proxy: fetch %s failed: %v", u.Host, err)
		writeJSON(w, http.StatusBadGateway, envelope{OK: false, Error: "upstream fetch failed"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("img-proxy: upstream %s%s returned %d", u.Host, u.Path, resp.StatusCode)
		writeJSON(w, resp.StatusCode, envelope{OK: false,
			Error: fmt.Sprintf("upstream returned %d", resp.StatusCode)})
		return
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		log.Printf("img-proxy: upstream %s%s rejected — not image/* (got %q)", u.Host, u.Path, ct)
		writeJSON(w, http.StatusUnsupportedMediaType, envelope{OK: false,
			Error: "upstream is not an image"})
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Don't expose the upstream's CORS / cookie / link headers.
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, resp.Body, imgProxyMaxBytes+1)
}

// assertPublicHost resolves the hostname and returns an error if ANY of
// the resolved IPs is in a non-public range. Conservative — better to
// false-negative on a weird-but-legit host than to expose our internal
// network via fetch.
func assertPublicHost(host string) error {
	if host == "" {
		return errors.New("empty host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("dns: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("dns returned no addresses")
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("resolved to non-public %s", ip)
		}
	}
	return nil
}

// isPublicIP returns true only when `ip` is a globally-routable address.
// Anything else (private, loopback, link-local, multicast, unspecified,
// reserved) gets rejected to block SSRF into intranet / metadata services.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.IsPrivate() {
		return false
	}
	// Extra blocks not covered by IsPrivate / IsLoopback:
	//   100.64/10 CGNAT, 169.254/16 link-local (covered above), 198.18/15
	//   benchmarking, 240/4 reserved, IPv6 unique-local fc00::/7
	if v4 := ip.To4(); v4 != nil {
		// CGNAT 100.64.0.0/10
		if v4[0] == 100 && v4[1]&0xc0 == 64 {
			return false
		}
		// Benchmarking 198.18.0.0/15
		if v4[0] == 198 && (v4[1] == 18 || v4[1] == 19) {
			return false
		}
		// Reserved 240.0.0.0/4
		if v4[0] >= 240 {
			return false
		}
		// 0.0.0.0/8 (already covered by Unspecified for ::, but explicit)
		if v4[0] == 0 {
			return false
		}
	} else {
		// IPv6 unique-local fc00::/7 (ip.IsPrivate covers fd00::/8 in newer Go but not fc00::/8)
		if len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
			return false
		}
	}
	return true
}
