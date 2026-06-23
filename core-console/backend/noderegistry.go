// Node registry — the persistent, runtime-editable source of truth for the
// PoP fleet.
//
// Before this, the node list lived as a hardcoded []fleetNode literal in
// newFleetScraper(): adding or removing a PoP meant editing Go, rebuilding,
// and restarting ncn-api. The registry moves that list to a JSON file the
// admin UI can mutate at runtime (add / edit / decommission / delete), so the
// common lifecycle operations never touch code.
//
// Storage: single JSON file at /etc/ncn-core-console/nodes.json, rewritten
// atomically on mutation. Same pattern as billing.go / incidents.go — admin
// write rate is human-keystroke pace, no real contention.
//
// First-run migration: if nodes.json is absent, it is seeded with the exact
// set of PoPs that used to be hardcoded in fleet.go, so a fresh deploy boots
// with byte-identical behaviour and the public site is unchanged.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	nodeRegistryDir  = "/etc/ncn-core-console"
	nodeRegistryPath = nodeRegistryDir + "/nodes.json"

	nodeStatusActive         = "active"
	nodeStatusDecommissioned = "decommissioned"
)

// nodeIDRe is the strict id grammar. Lowercase alnum + dashes, must start
// alnum, 2..31 chars. The id is used as a map key everywhere, as a filename
// (/etc/ncn-core-console/agent-keys/<id>.key), and passed as an argv to the
// provision script — so it must never contain shell metacharacters, slashes,
// or whitespace.
var nodeIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}$`)

func validNodeID(id string) bool { return nodeIDRe.MatchString(id) }

// nodeRecord is the on-disk + wire shape of one PoP. It is a superset of the
// fields fleetNode needs for scraping plus lifecycle/provisioning metadata
// (status, arch, audit stamps) the runtime struct doesn't carry.
type nodeRecord struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	Country     string    `json:"country"`
	Address     string    `json:"address"`
	Lat         float64   `json:"lat,omitempty"`
	Lon         float64   `json:"lon,omitempty"`
	SSHUser     string    `json:"ssh_user,omitempty"`     // empty = "root" (fleetNode default)
	SSHIdentity string    `json:"ssh_identity,omitempty"` // empty = fleet-key default
	SSHPort     int       `json:"ssh_port,omitempty"`     // 0 = default 22
	// Region / NodeNum drive the internal-mesh addressing convention:
	//   v6 anchor  = 2001:db8:<Region>::<NodeNum>   (on dummy0)
	//   link-local = fe80::<Region>:<NodeNum>        (per iBGP/GRE link)
	// Same-metro nodes share a Region (51=HKG 52=FMT 53=TYO 54=SIN 55=FRA
	// 56=TPE …); a new metro gets a Region from the operator. NodeNum is the
	// numeric suffix of the id (pop-02 → 3). 0 = not yet assigned (backfilled
	// at load from the historical metro map / id suffix where derivable).
	Region      int       `json:"region,omitempty"`
	NodeNum     int       `json:"node_num,omitempty"`
	Arch        string    `json:"arch,omitempty"`         // provisioning hint: amd64 | arm64
	Status      string    `json:"status"`                 // active | decommissioned
	Notes       string    `json:"notes,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toFleetNode projects a record into the runtime fleetNode the scraper uses.
// SSHHost mirrors ID (vestigial — term.go dials Address directly — but kept
// non-empty for parity with the old hardcoded entries). Local is decided by
// the scraper against NCN_LOCAL_NODE_ID, not stored in the registry.
func (r *nodeRecord) toFleetNode() fleetNode {
	return fleetNode{
		ID:          r.ID,
		Label:       r.Label,
		Country:     r.Country,
		Address:     r.Address,
		Lat:         r.Lat,
		Lon:         r.Lon,
		SSHHost:     r.ID,
		SSHUser:     r.SSHUser,
		SSHIdentity: r.SSHIdentity,
		SSHPort:     r.SSHPort,
		Region:      r.Region,
	}
}

func (r *nodeRecord) active() bool { return r.Status != nodeStatusDecommissioned }

type nodeRegistry struct {
	mu   sync.RWMutex
	recs []*nodeRecord
	path string
}

var globalNodes *nodeRegistry

// seedNodes is the migration payload: the PoPs that were hardcoded in
// fleet.go before the registry existed. Order matters — it is the display
// order across the whole site (landing, map, LG, admin grid, bot /status).
// arch is amd64 across the current fleet; SAN IP == Address.
func seedNodes() []*nodeRecord {
	now := time.Now().UTC()
	mk := func(id, label, country, addr string, lat, lon float64) *nodeRecord {
		return &nodeRecord{
			ID: id, Label: label, Country: country, Address: addr,
			Lat: lat, Lon: lon, SSHUser: "root", Arch: "amd64",
			Status: nodeStatusActive, CreatedBy: "seed", CreatedAt: now, UpdatedAt: now,
		}
	}
	return []*nodeRecord{
		mk("pop-03", "Region C, HK · Kwai Chung", "HK", "198.51.100.3", 22.37, 114.14),
		mk("pop-04", "Region C, HK", "HK", "198.51.100.4", 22.30, 114.17),
		mk("ctrl-01", "Region A, JP", "JP", "198.51.100.1", 35.68, 139.69),
		mk("pop-01", "Region A, JP", "JP", "198.51.100.2", 35.69, 139.70),
		mk("pop-08", "Region E", "TW", "198.51.100.6", 25.03, 121.56),
		mk("pop-06", "Region D, SG", "SG", "198.51.100.8", 1.30, 103.79),
		mk("pop-05", "Region B, DE", "DE", "198.51.100.7", 50.11, 8.68),
	}
}

// newNodeRegistry loads the registry, preferring Postgres when it already
// holds the fleet, else the JSON file (seeding the historical fleet on first
// run). When the DB is configured but empty, the file/seed is migrated into it.
func newNodeRegistry() (*nodeRegistry, error) {
	if err := os.MkdirAll(nodeRegistryDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", nodeRegistryDir, err)
	}
	r := &nodeRegistry{path: nodeRegistryPath}

	// Prefer Postgres when it already holds the registry (post-cutover).
	loadedFromDB := false
	if globalDB != nil {
		if recs, err := nodeRegistryLoadDB(); err != nil {
			log.Printf("noderegistry: db load failed (%v) — falling back to file", err)
		} else if len(recs) > 0 {
			r.recs = recs
			loadedFromDB = true
		}
	}

	// Otherwise load the JSON file, seeding the historical fleet on first run.
	seeded := false
	if !loadedFromDB {
		b, err := os.ReadFile(nodeRegistryPath)
		switch {
		case err == nil && len(b) > 0:
			if err := json.Unmarshal(b, &r.recs); err != nil {
				return nil, fmt.Errorf("parse %s: %w", nodeRegistryPath, err)
			}
		default:
			r.recs = seedNodes()
			seeded = true
		}
	}

	// Normalise: any record missing a status is treated as active (forward
	// compat if a hand-edited file omits it). Backfill Region/NodeNum for
	// records predating the mesh feature, from the historical metro map +
	// id suffix, so the mesh generator can reference every existing node.
	dirty := false
	for _, rec := range r.recs {
		if rec.Status == "" {
			rec.Status = nodeStatusActive
		}
		if rec.NodeNum == 0 {
			if n := nodeNumFromID(rec.ID); n > 0 {
				rec.NodeNum = n
				dirty = true
			}
		}
		if rec.Region == 0 {
			if reg, ok := metroRegionSeed[metroOfID(rec.ID)]; ok {
				rec.Region = reg
				dirty = true
			}
		}
	}

	// Persist when we seeded, backfilled, or need to migrate file/seed into an
	// empty DB. persistLocked dual-writes (file + DB), so this both seeds the
	// file on first run and cuts the data over to Postgres.
	if seeded || dirty || (globalDB != nil && !loadedFromDB) {
		r.mu.Lock()
		err := r.persistLocked()
		r.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("persist registry: %w", err)
		}
	}
	return r, nil
}

// nodeRegCols is the node_registry column list in nodeRecord field order
// (used for scan + the non-ord part of INSERT).
const nodeRegCols = `id,label,country,address,lat,lon,ssh_user,ssh_identity,ssh_port,region,node_num,arch,status,notes,created_by,created_at,updated_at`

// scanNodeRow reads one row in nodeRegCols order (*sql.Row or *sql.Rows).
func scanNodeRow(sc interface{ Scan(...any) error }) (*nodeRecord, error) {
	rec := &nodeRecord{}
	if err := sc.Scan(&rec.ID, &rec.Label, &rec.Country, &rec.Address, &rec.Lat, &rec.Lon,
		&rec.SSHUser, &rec.SSHIdentity, &rec.SSHPort, &rec.Region, &rec.NodeNum, &rec.Arch,
		&rec.Status, &rec.Notes, &rec.CreatedBy, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	rec.UpdatedAt = rec.UpdatedAt.UTC()
	return rec, nil
}

// nodeRegistryLoadDB returns the registry in display order from Postgres.
func nodeRegistryLoadDB() ([]*nodeRecord, error) {
	rows, err := globalDB.Query(`SELECT ` + nodeRegCols + ` FROM node_registry ORDER BY ord`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*nodeRecord
	for rows.Next() {
		rec, e := scanNodeRow(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// persistDB mirrors the whole in-memory registry into Postgres in one tx:
// the slice is small (~10 PoPs) and the file already rewrites wholesale, so a
// DELETE-all + ordered re-INSERT keeps the table an exact copy (order + any
// removals). Caller holds r's write lock.
func (r *nodeRegistry) persistDB() error {
	tx, err := globalDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op once committed
	if _, err := tx.Exec(`DELETE FROM node_registry`); err != nil {
		return err
	}
	ins := `INSERT INTO node_registry (ord,` + nodeRegCols + `)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`
	for i, rec := range r.recs {
		if _, err := tx.Exec(ins, i, rec.ID, rec.Label, rec.Country, rec.Address, rec.Lat, rec.Lon,
			rec.SSHUser, rec.SSHIdentity, rec.SSHPort, rec.Region, rec.NodeNum, rec.Arch,
			rec.Status, rec.Notes, rec.CreatedBy, rec.CreatedAt, rec.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// metroRegionSeed maps the historical metro prefixes to their region code,
// derived from the live v6 anchors (2001:db8:<region>::N). New metros are
// not here — the operator supplies a region when adding the first node.
var metroRegionSeed = map[string]int{

}

// regionForMetro returns the region code for a metro: an existing sibling
// node's Region if any, else the historical seed map, else 0 (unknown — the
// caller then requires the operator to supply one for this new metro).
func (r *nodeRegistry) regionForMetro(metro string) int {
	r.mu.RLock()
	for _, rec := range r.recs {
		if rec.Region > 0 && metroOfID(rec.ID) == metro {
			r.mu.RUnlock()
			return rec.Region
		}
	}
	r.mu.RUnlock()
	return metroRegionSeed[metro]
}

// metroOfID returns the metro prefix of a node id (the part before the last
// dash): "pop-02" → "pop".
func metroOfID(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 {
		return id[:i]
	}
	return id
}

// nodeNumFromID parses the numeric suffix after the last dash: "pop-02" → 3.
// Returns 0 when there's no numeric suffix.
func nodeNumFromID(id string) int {
	i := strings.LastIndex(id, "-")
	if i < 0 || i+1 >= len(id) {
		return 0
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// persistLocked serialises under the caller's held write lock. Dual-write
// during the Postgres transition: the JSON file is always written (the durable
// backup + the globalDB==nil path), then mirrored into Postgres when available.
// A DB error is non-fatal — the file is already saved, so a write never fails
// just because Postgres hiccupped, and the file stays the fallback source.
func (r *nodeRegistry) persistLocked() error {
	b, err := json.MarshalIndent(r.recs, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return err
	}
	if globalDB != nil {
		if err := r.persistDB(); err != nil {
			log.Printf("noderegistry: db persist failed (%v) — file is current", err)
		}
	}
	return nil
}

// listSnapshot returns a deep-ish copy (value copies of each record) in
// registry/display order. Safe to hand to a handler without holding the lock.
func (r *nodeRegistry) listSnapshot() []nodeRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]nodeRecord, 0, len(r.recs))
	for _, rec := range r.recs {
		out = append(out, *rec)
	}
	return out
}

// activeFleetNodes projects the active records into fleetNodes, in order.
// Used by the scraper at construction time.
func (r *nodeRegistry) activeFleetNodes() []fleetNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]fleetNode, 0, len(r.recs))
	for _, rec := range r.recs {
		if rec.active() {
			out = append(out, rec.toFleetNode())
		}
	}
	return out
}

// get returns a value copy of the record with the given id, or ok=false.
func (r *nodeRegistry) get(id string) (nodeRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rec := range r.recs {
		if rec.ID == id {
			return *rec, true
		}
	}
	return nodeRecord{}, false
}

// add appends a new record (validated by the caller) and persists. Fails if
// the id already exists.
func (r *nodeRegistry) add(rec nodeRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.recs {
		if x.ID == rec.ID {
			return fmt.Errorf("node %q already exists", rec.ID)
		}
	}
	cp := rec
	r.recs = append(r.recs, &cp)
	return r.persistLocked()
}

// nodePatch carries the optional-field metadata edit for update().
type nodePatch struct {
	Label       *string  `json:"label,omitempty"`
	Country     *string  `json:"country,omitempty"`
	Address     *string  `json:"address,omitempty"`
	Lat         *float64 `json:"lat,omitempty"`
	Lon         *float64 `json:"lon,omitempty"`
	SSHUser     *string  `json:"ssh_user,omitempty"`
	SSHIdentity *string  `json:"ssh_identity,omitempty"`
	SSHPort     *int     `json:"ssh_port,omitempty"`
	Region      *int     `json:"region,omitempty"`
	NodeNum     *int     `json:"node_num,omitempty"`
	Arch        *string  `json:"arch,omitempty"`
	Notes       *string  `json:"notes,omitempty"`
}

// update applies a metadata patch and returns the updated record copy.
func (r *nodeRegistry) update(id string, p nodePatch) (nodeRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.recs {
		if rec.ID != id {
			continue
		}
		if p.Label != nil {
			rec.Label = strings.TrimSpace(*p.Label)
		}
		if p.Country != nil {
			rec.Country = strings.ToUpper(strings.TrimSpace(*p.Country))
		}
		if p.Address != nil {
			rec.Address = strings.TrimSpace(*p.Address)
		}
		if p.Lat != nil {
			rec.Lat = *p.Lat
		}
		if p.Lon != nil {
			rec.Lon = *p.Lon
		}
		if p.SSHUser != nil {
			rec.SSHUser = strings.TrimSpace(*p.SSHUser)
		}
		if p.SSHIdentity != nil {
			rec.SSHIdentity = strings.TrimSpace(*p.SSHIdentity)
		}
		if p.SSHPort != nil {
			rec.SSHPort = *p.SSHPort
		}
		if p.Region != nil {
			rec.Region = *p.Region
		}
		if p.NodeNum != nil {
			rec.NodeNum = *p.NodeNum
		}
		if p.Arch != nil {
			rec.Arch = strings.TrimSpace(*p.Arch)
		}
		if p.Notes != nil {
			rec.Notes = *p.Notes
		}
		rec.UpdatedAt = time.Now().UTC()
		if err := r.persistLocked(); err != nil {
			return nodeRecord{}, err
		}
		return *rec, nil
	}
	return nodeRecord{}, fmt.Errorf("node %q not found", id)
}

// setStatus flips active/decommissioned and persists; returns the new record.
func (r *nodeRegistry) setStatus(id, status string) (nodeRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.recs {
		if rec.ID == id {
			rec.Status = status
			rec.UpdatedAt = time.Now().UTC()
			if err := r.persistLocked(); err != nil {
				return nodeRecord{}, err
			}
			return *rec, nil
		}
	}
	return nodeRecord{}, fmt.Errorf("node %q not found", id)
}

// remove deletes the record entirely and persists.
func (r *nodeRegistry) remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, rec := range r.recs {
		if rec.ID == id {
			r.recs = append(r.recs[:i], r.recs[i+1:]...)
			return r.persistLocked()
		}
	}
	return fmt.Errorf("node %q not found", id)
}
