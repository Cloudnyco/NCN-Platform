// PeeringDB record cache.
//
// Pulls AS64500's PeeringDB net + netixlan records on startup, refreshes
// every 6 hours, and serves a normalized JSON view at /api/v1/peeringdb.
// The upstream API has no auth requirement for public read access.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	peeringDBNetURL     = "https://www.peeringdb.com/api/net?asn=64500"
	peeringDBNetIXURL   = "https://www.peeringdb.com/api/netixlan?asn=64500"
	peeringDBRefreshAge = 6 * time.Hour
	peeringDBHTTPTO     = 12 * time.Second
)

// Public-facing shape (subset of PeeringDB's schema, normalized snake_case
// fields the frontend cares about).
type peeringDBNet struct {
	ID            int    `json:"id"`
	ASN           int    `json:"asn"`
	Name          string `json:"name"`
	Website       string `json:"website"`
	IRRASSet      string `json:"irr_as_set"`
	InfoPrefixes4 int    `json:"info_prefixes4"`
	InfoPrefixes6 int    `json:"info_prefixes6"`
	InfoIPv6      bool   `json:"info_ipv6"`
	InfoMulticast bool   `json:"info_multicast"`
	InfoUnicast   bool   `json:"info_unicast"`
	PolicyGeneral string `json:"policy_general"`
	PolicyLocations string `json:"policy_locations"`
	PolicyRatio   bool   `json:"policy_ratio"`
	PolicyContracts string `json:"policy_contracts"`
	IXCount       int    `json:"ix_count"`
	FacCount      int    `json:"fac_count"`
	Updated       string `json:"updated"`
}
type peeringDBIX struct {
	Name        string `json:"name"`
	Speed       int    `json:"speed"`        // Mbps
	IPAddr4     string `json:"ipaddr4"`
	IPAddr6     string `json:"ipaddr6"`
	IsRSPeer    bool   `json:"is_rs_peer"`
	BFDSupport  bool   `json:"bfd_support"`
	Operational bool   `json:"operational"`
}
type peeringDBSnapshot struct {
	Net        *peeringDBNet `json:"net"`
	IX         []peeringDBIX `json:"ix"`
	NetURL     string        `json:"net_url"`     // direct PeeringDB net page
	FetchedAt  int64         `json:"fetched_at"`  // unix
	UpdatedAt  string        `json:"upstream_updated"`
	Error      string        `json:"error,omitempty"`
}

type peeringDBState struct {
	mu       sync.RWMutex
	snapshot peeringDBSnapshot
	httpc    *http.Client
}

func newPeeringDBState() *peeringDBState {
	return &peeringDBState{
		httpc: &http.Client{Timeout: peeringDBHTTPTO},
	}
}

func (p *peeringDBState) Start(ctx context.Context) {
	go func() {
		p.refresh(ctx)
		t := time.NewTicker(peeringDBRefreshAge)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.refresh(ctx)
			}
		}
	}()
}

func (p *peeringDBState) refresh(ctx context.Context) {
	start := time.Now()
	netRec, err := p.fetchNet(ctx)
	if err != nil {
		log.Printf("peeringdb: net fetch failed: %v", err)
		p.mu.Lock()
		p.snapshot.Error = err.Error()
		p.snapshot.FetchedAt = time.Now().Unix()
		p.mu.Unlock()
		return
	}
	ix, err := p.fetchIX(ctx)
	if err != nil {
		// Keep partial — net record on its own is still useful.
		log.Printf("peeringdb: netixlan fetch failed: %v", err)
	}

	snap := peeringDBSnapshot{
		Net:       netRec,
		IX:        ix,
		FetchedAt: time.Now().Unix(),
	}
	if netRec != nil {
		snap.NetURL = "https://www.peeringdb.com/net/" + itoa(netRec.ID)
		snap.UpdatedAt = netRec.Updated
	}
	p.mu.Lock()
	p.snapshot = snap
	p.mu.Unlock()
	log.Printf("peeringdb: refreshed net=%d ix=%d in %s",
		netRec.ID, len(ix), time.Since(start).Round(time.Millisecond))
}

func (p *peeringDBState) fetchNet(ctx context.Context) (*peeringDBNet, error) {
	body, err := p.fetch(ctx, peeringDBNetURL)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []peeringDBNet `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, errors.New("no net record returned")
	}
	r := resp.Data[0]
	return &r, nil
}

func (p *peeringDBState) fetchIX(ctx context.Context) ([]peeringDBIX, error) {
	body, err := p.fetch(ctx, peeringDBNetIXURL)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []peeringDBIX `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (p *peeringDBState) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ncn-api/1.0 (+https://example.com)")
	req.Header.Set("Accept", "application/json")
	resp, err := p.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, errors.New("peeringdb http " + resp.Status)
	}
	return body, nil
}

func (p *peeringDBState) handleHTTP(w http.ResponseWriter, _ *http.Request) {
	p.mu.RLock()
	snap := p.snapshot
	p.mu.RUnlock()
	// Cache 5 minutes — public, low-churn data.
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, envelope{OK: true, Data: snap})
}

// Small helper so we don't pull in strconv just for one Itoa call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
