// metrics.go — Prometheus text-format /metrics (hand-rolled, no client_golang
// dep). Exposes the operational signals worth graphing: fleet health, active
// alerts by severity, DB + replication health, open op-failures, AI token
// spend. Optionally gated by NCN_METRICS_TOKEN (Bearer); open when unset, for
// an internal / localhost scrape. Reveals counts only, no secrets.
package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type metricSeries struct {
	labels string // e.g. `{severity="crit"}` or ""
	val    float64
}

func metricsHandler(fleet *fleetScraper, ae *alertEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok := strings.TrimSpace(os.Getenv("NCN_METRICS_TOKEN")); tok != "" {
			if r.Header.Get("Authorization") != "Bearer "+tok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		var b strings.Builder
		emit := func(name, help string, series ...metricSeries) {
			if len(series) == 0 {
				return
			}
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
			for _, s := range series {
				fmt.Fprintf(&b, "%s%s %g\n", name, s.labels, s.val)
			}
		}
		// Counter variant — monotonic series Grafana can rate()/topk() over.
		emitc := func(name, help string, series ...metricSeries) {
			if len(series) == 0 {
				return
			}
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
			for _, s := range series {
				fmt.Fprintf(&b, "%s%s %g\n", name, s.labels, s.val)
			}
		}
		b2f := func(ok bool) float64 {
			if ok {
				return 1
			}
			return 0
		}

		emit("ncn_db_up", "Postgres pool available (1) or file-backed (0)", metricSeries{"", b2f(globalDB != nil)})

		if globalDB != nil {
			var streaming int
			_ = globalDB.QueryRow("SELECT count(*) FROM pg_stat_replication WHERE state='streaming'").Scan(&streaming)
			emit("ncn_replication_streaming_standbys", "Standbys currently streaming from this primary", metricSeries{"", float64(streaming)})
			var lag sql.NullFloat64
			_ = globalDB.QueryRow("SELECT EXTRACT(EPOCH FROM max(replay_lag)) FROM pg_stat_replication WHERE state='streaming'").Scan(&lag)
			if lag.Valid {
				emit("ncn_replication_lag_seconds", "Worst standby replay lag (seconds)", metricSeries{"", lag.Float64})
			}

			// WAL archiver (PITR) — surface so a stuck offsite archive is graphable
			// before pg_wal fills ctrl-01's tight root disk.
			var archMode string
			_ = globalDB.QueryRow("SELECT setting FROM pg_settings WHERE name='archive_mode'").Scan(&archMode)
			if archMode == "on" {
				var archived, failed int64
				var ageSecs sql.NullFloat64
				_ = globalDB.QueryRow("SELECT archived_count, failed_count, EXTRACT(EPOCH FROM now()-last_archived_time) FROM pg_stat_archiver").Scan(&archived, &failed, &ageSecs)
				emit("ncn_wal_archived_total", "WAL segments successfully archived offsite", metricSeries{"", float64(archived)})
				emit("ncn_wal_archive_failed_total", "WAL archive_command failures (rising = PITR breaking)", metricSeries{"", float64(failed)})
				if ageSecs.Valid {
					emit("ncn_wal_last_archive_age_seconds", "Seconds since the last successful WAL archive", metricSeries{"", ageSecs.Float64})
				}
			}
		}

		if fleet != nil {
			nodes := fleet.snapshotNodes()
			up := 0
			for _, n := range nodes {
				if n != nil && n.OK {
					up++
				}
			}
			emit("ncn_fleet_nodes_total", "PoP nodes in the registry", metricSeries{"", float64(len(nodes))})
			emit("ncn_fleet_nodes_up", "PoP nodes reachable on the last scrape", metricSeries{"", float64(up)})

			// Anycast announcement state per node — upstream eBGP sessions up, and
			// whether the node is drained (announcing nothing). Makes a withdrawal
			// graphable + alertable, not just visible in the console.
			var upSeries, drainSeries []metricSeries
			for _, n := range nodes {
				if n == nil {
					continue
				}
				upN, downN := anycastUpstreams(n.Protocols)
				lbl := fmt.Sprintf("{node=%q}", n.Node.ID)
				upSeries = append(upSeries, metricSeries{lbl, float64(len(upN))})
				drained := 0.0
				if len(upN) == 0 && len(downN) > 0 {
					drained = 1
				}
				drainSeries = append(drainSeries, metricSeries{lbl, drained})
			}
			emit("ncn_anycast_upstreams_up", "Upstream eBGP sessions currently announcing, per node", upSeries...)
			emit("ncn_anycast_drained", "1 if the node is drained (all upstreams withdrawn), per node", drainSeries...)

			// Per-interface cumulative byte counters. The console keeps only
			// ~15 min of rates natively; exposing the raw monotonic counters
			// lets Prometheus/Grafana compute rate()/topk() over real retention.
			var ifRx, ifTx []metricSeries
			for _, n := range nodes {
				if n == nil {
					continue
				}
				for _, ifc := range n.Ifaces {
					lbl := fmt.Sprintf("{node=%q,iface=%q}", n.Node.ID, ifc.Name)
					ifRx = append(ifRx, metricSeries{lbl, float64(ifc.RxTotal)})
					ifTx = append(ifTx, metricSeries{lbl, float64(ifc.TxTotal)})
				}
			}
			emitc("ncn_iface_rx_bytes_total", "Cumulative received bytes, per node interface", ifRx...)
			emitc("ncn_iface_tx_bytes_total", "Cumulative transmitted bytes, per node interface", ifTx...)
		}

		if ae != nil {
			counts := map[string]int{"crit": 0, "warn": 0, "info": 0}
			for _, a := range ae.activeSnapshot("") {
				counts[string(a.Severity)]++
			}
			emit("ncn_alerts_active", "Active alerts by severity",
				metricSeries{`{severity="crit"}`, float64(counts["crit"])},
				metricSeries{`{severity="warn"}`, float64(counts["warn"])},
				metricSeries{`{severity="info"}`, float64(counts["info"])},
			)
		}

		if globalOpFailures != nil {
			emit("ncn_op_failures_open", "Open operational-action failures", metricSeries{"", float64(globalOpFailures.openCount())})
		}

		if globalAIUsage != nil {
			var tok, calls []metricSeries
			for model, bk := range globalAIUsage.totalsSnapshot() {
				tok = append(tok, metricSeries{fmt.Sprintf(`{model=%q,kind="prompt"}`, model), float64(bk.PromptTok)})
				tok = append(tok, metricSeries{fmt.Sprintf(`{model=%q,kind="output"}`, model), float64(bk.OutputTok)})
				calls = append(calls, metricSeries{fmt.Sprintf(`{model=%q}`, model), float64(bk.Calls)})
			}
			emit("ncn_ai_tokens_total", "AI tokens consumed since boot", tok...)
			emit("ncn_ai_calls_total", "AI completions since boot", calls...)
		}

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	}
}
