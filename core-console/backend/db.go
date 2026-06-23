// db.go — Postgres handle + embedded migration runner. First brick of the
// JSON-files → Postgres persistence foundation. The DB is OPTIONAL and
// introduced store-by-store: when NCN_DATABASE_URL is unset (or the DB is
// unreachable) globalDB stays nil and every store falls back to its existing
// file/in-memory behaviour. So a missing or broken DB can NEVER block startup
// or take the console down — every DB code path must tolerate globalDB == nil.
package main

import (
	"context"
	"database/sql"
	"embed"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// globalDB is the Postgres pool, or nil when DB is not configured/available.
var globalDB *sql.DB

// initDB opens the pool from NCN_DATABASE_URL and applies pending migrations.
// Non-fatal by design: any failure logs a warning and leaves globalDB nil
// (file-backed), so the console keeps running exactly as before.
func initDB() {
	dsn := strings.TrimSpace(os.Getenv("NCN_DATABASE_URL"))
	if dsn == "" {
		log.Printf("db: NCN_DATABASE_URL unset — running file-backed (no Postgres)")
		return
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Printf("db: open failed (%v) — falling back to file-backed", err)
		return
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Printf("db: ping failed (%v) — falling back to file-backed", err)
		_ = db.Close()
		return
	}
	if err := runMigrations(db); err != nil {
		log.Printf("db: migration failed (%v) — falling back to file-backed", err)
		_ = db.Close()
		return
	}
	globalDB = db
	log.Printf("db: Postgres connected + migrations applied")
}

// runMigrations applies every embedded migrations/*.sql not yet recorded in
// schema_migrations, in lexical order, each in its own transaction.
func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	applied := map[string]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		if applied[f] {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + f)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES($1)`, f); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		log.Printf("db: applied migration %s", f)
	}
	return nil
}

// dbReady reports whether a usable Postgres pool is available.
func dbReady() bool { return globalDB != nil }

// ----------------------------------------------------------------------------
// Single-row JSONB config-doc helpers — for stores that are loaded/saved
// WHOLESALE (the app queries them in memory, never via SQL): alert rules,
// incidents, billing, … Each lives as one row id='singleton' in its own table.
// `table` MUST be a trusted constant, never user input.
// ----------------------------------------------------------------------------

// loadConfigDoc returns the JSONB document from a single-row config table, or
// (nil, nil) when the row is absent (first DB-enabled boot → caller migrates).
func loadConfigDoc(table string) ([]byte, error) {
	var doc []byte
	err := globalDB.QueryRow(`SELECT doc FROM ` + table + ` WHERE id='singleton'`).Scan(&doc)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// reconcileConfigDocs heals the dual-write divergence hazard: a write that
// landed while PG was unwritable (e.g. mid-failover, PG read-only/down) goes
// file-only, so the file gets ahead of PG — and the next DB-first load would
// shadow it with the stale PG doc. At boot (after initDB, BEFORE the stores
// load) we compare each config-doc store's file mtime vs its PG doc's
// updated_at; if the FILE is newer, push file→PG so the load gets the latest.
// Safe no-op in steady state (dual-write keeps file mtime ≲ PG updated_at).
func reconcileConfigDocs() {
	if globalDB == nil {
		return
	}
	stores := []struct{ table, file string }{
		{"operators", operatorsPath},
		{"api_tokens", apiTokensPath},
		{"invites", invitesPath},
		{"incidents", incidentsPath},
		{"billing", billingPath},
		{"alert_rules", alertRulesPath},
		{"ai_user_store", aiUserStorePath},
		{"ai_models", aiModelsPath},
		{"peering_apply", peeringApplyPath},
		{"recovery_used", recoveryBootstrapUsedPath},
		{"heartbeats", heartbeatPath},
		{"peer_generations", peerGenerationsPath},
		{"rpki_settings", rpkiSettingsPath},
		{"link_capacity", linkCapacityPath},
		{"sla_targets", slaTargetsPath},
		{"config_declarations", configDeclPath},
		{"oncall", oncallPath},
		{"flowspec_rules", flowspecPath},
	}
	for _, s := range stores {
		fi, err := os.Stat(s.file)
		if err != nil {
			continue // no file → nothing to reconcile
		}
		var pgUpdated sql.NullTime
		_ = globalDB.QueryRow(`SELECT updated_at FROM `+s.table+` WHERE id='singleton'`).Scan(&pgUpdated)
		// File strictly newer than the PG doc (or PG row absent) → file wins.
		if pgUpdated.Valid && !fi.ModTime().After(pgUpdated.Time) {
			continue
		}
		b, err := os.ReadFile(s.file)
		if err != nil || len(b) == 0 {
			continue
		}
		if err := saveConfigDoc(s.table, b); err != nil {
			log.Printf("reconcile: %s file→PG failed (%v)", s.table, err)
		} else {
			log.Printf("reconcile: %s — file newer than PG doc, synced file→PG", s.table)
		}
	}
}

// saveConfigDoc upserts the singleton document into a config table.
func saveConfigDoc(table string, doc []byte) error {
	_, err := globalDB.Exec(`INSERT INTO `+table+` (id, doc, updated_at)
		VALUES ('singleton', $1, now())
		ON CONFLICT (id) DO UPDATE SET doc = EXCLUDED.doc, updated_at = now()`, doc)
	return err
}
