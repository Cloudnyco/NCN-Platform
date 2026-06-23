// util.go — tiny pure helpers with no dependencies on mail state.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// atoiDefault parses s as base-10 and returns dflt on failure. Used for
// parsing optional URL query knobs (limit=, offset=).
func atoiDefault(s string, dflt int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return dflt
}

// pathSegmentAfter returns the rest of a URL path after a fixed prefix,
// with any leading slash stripped. Used to pull the trailing identifier
// out of routes like `/api/v1/mail/folder/INBOX/123`.
func pathSegmentAfter(p, prefix string) (string, error) {
	if !strings.HasPrefix(p, prefix) {
		return "", errors.New("no match")
	}
	rest := strings.TrimPrefix(p, prefix)
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "", errors.New("empty")
	}
	return rest, nil
}

// randomTokenHex returns 2n hex characters of crypto-random bytes. Used
// for one-shot identifiers (challenge IDs, draft sequence numbers, …).
func randomTokenHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
