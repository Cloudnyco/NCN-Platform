// replmon.go — primary-side Postgres replication watchdog. We built streaming
// replication (ctrl-01 → pop-03) as the HA foundation; without this, the replica
// silently falling behind / disconnecting would leave the DR + failover safety
// net broken with no warning. Runs on whichever node is the PRIMARY (the warm
// standby's ncn-api is stopped). Edge-triggered + debounced; posts to the error
// channel. No-op when Postgres is unconfigured (file-backed) or we're a standby.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

const (
	replCheckInterval = 60 * time.Second
	replLagThreshold  = 90 * time.Second // standby replay lag beyond this = unhealthy
	replBadStreak     = 2                // consecutive bad checks before alerting (debounce)
)

type replMonitor struct {
	notify    *tgNotifier
	badStreak int
	alerted   bool // an unhealthy alert is currently outstanding (edge-trigger)

	// WAL-archiver watchdog (PITR). ctrl-01's root disk is tight, so a stuck
	// archiver lets pg_wal accumulate until the DB halts — alert early.
	archPrevFailed int64
	archHaveFailed bool // archPrevFailed has been seeded
	archAlerted    bool
}

func newReplMonitor(n *tgNotifier) *replMonitor { return &replMonitor{notify: n} }

func (m *replMonitor) Start(ctx context.Context) {
	if globalDB == nil {
		log.Printf("replmon: Postgres not configured — replication watchdog disabled")
		return
	}
	go func() {
		time.Sleep(25 * time.Second) // let startup settle
		t := time.NewTicker(replCheckInterval)
		defer t.Stop()
		m.check()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.check()
			}
		}
	}()
}

// check evaluates the primary's replication state once.
func (m *replMonitor) check() {
	if globalDB == nil {
		return
	}
	var inRecovery bool
	if err := globalDB.QueryRow("SELECT pg_is_in_recovery()").Scan(&inRecovery); err != nil {
		return // transient DB error — don't false-alarm
	}
	if inRecovery {
		return // we're a standby; the primary owns this check
	}
	var slots, streaming int
	if err := globalDB.QueryRow("SELECT count(*) FROM pg_replication_slots").Scan(&slots); err != nil {
		return
	}
	if slots == 0 {
		return // no replication configured on this node → nothing to watch
	}
	_ = globalDB.QueryRow("SELECT count(*) FROM pg_stat_replication WHERE state='streaming'").Scan(&streaming)
	var lag sql.NullFloat64
	_ = globalDB.QueryRow("SELECT EXTRACT(EPOCH FROM max(replay_lag)) FROM pg_stat_replication WHERE state='streaming'").Scan(&lag)

	healthy := streaming > 0 && (!lag.Valid || lag.Float64 <= replLagThreshold.Seconds())
	if healthy {
		m.badStreak = 0
	} else {
		m.badStreak++
	}

	switch {
	case !healthy && m.badStreak >= replBadStreak && !m.alerted:
		m.alerted = true
		if streaming == 0 {
			m.post(fmt.Sprintf("🔴 <b>PG replication DOWN</b>\n%d slot(s) configured but NO standby streaming — the HA failover target is out of sync. Replication/standby needs attention.", slots))
		} else {
			m.post(fmt.Sprintf("🟡 <b>PG replication LAG</b>\nstandby %.0fs behind (threshold %.0fs).", lag.Float64, replLagThreshold.Seconds()))
		}
	case healthy && m.alerted:
		m.alerted = false
		m.post(fmt.Sprintf("✅ <b>PG replication restored</b>\n%d standby streaming, lag %.1fs.", streaming, nzFloat(lag)))
	}

	m.checkArchiver()
}

// checkArchiver watches the WAL archiver. We ship every completed segment
// offsite to pop-03 for PITR; archive_command exits non-zero on failure so PG
// RETAINS the segment and retries — which means a persistently failing archiver
// grows pg_wal on ctrl-01's tight root disk until the DB stops. Edge-triggered
// on failed_count rising; clears once a later archive succeeds.
func (m *replMonitor) checkArchiver() {
	if globalDB == nil {
		return
	}
	var mode string
	if err := globalDB.QueryRow("SELECT setting FROM pg_settings WHERE name='archive_mode'").Scan(&mode); err != nil || mode != "on" {
		return // archiving not enabled here → nothing to watch
	}
	var archived, failed int64
	var lastFailed sql.NullString
	if err := globalDB.QueryRow(
		"SELECT archived_count, failed_count, last_failed_wal FROM pg_stat_archiver").
		Scan(&archived, &failed, &lastFailed); err != nil {
		return
	}
	if !m.archHaveFailed {
		m.archPrevFailed = failed
		m.archHaveFailed = true
		return // seed only; need a delta to judge
	}
	rising := failed > m.archPrevFailed
	m.archPrevFailed = failed
	switch {
	case rising && !m.archAlerted:
		m.archAlerted = true
		lf := "?"
		if lastFailed.Valid {
			lf = lastFailed.String
		}
		m.post(fmt.Sprintf("🔴 <b>WAL archiving FAILING</b>\nfailed_count rose to %d (last failed: <code>%s</code>). PITR is no longer current and pg_wal will accumulate on ctrl-01's tight root disk — check the offsite archive link to pop-03.", failed, lf))
	case !rising && m.archAlerted:
		m.archAlerted = false
		m.post(fmt.Sprintf("✅ <b>WAL archiving recovered</b>\narchiver caught up (archived_count=%d, no new failures). PITR current again.", archived))
	}
}

func nzFloat(f sql.NullFloat64) float64 {
	if f.Valid {
		return f.Float64
	}
	return 0
}

func (m *replMonitor) post(text string) {
	if m.notify == nil {
		log.Printf("replmon: %s", text)
		return
	}
	channel := m.notify.errorChat
	if channel == "" {
		channel = m.notify.chatID
	}
	m.notify.enqueue(tgPayload{ChatID: channel, Text: text}, "replmon")
}
