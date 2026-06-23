-- 0020_sla.sql — active SLA probing.
--
-- Each PoP already pings public anchors + the other PoPs; this adds operator-
-- defined SLA targets probed from every PoP, rolled into per-(pop,target,UTC-day)
-- buckets by sla.go so the status page can show real availability/loss/latency
-- SLOs (not just up/down). sla_history is real time-series (one row per
-- pop/target/day, pruned past ~400 days), so NOT a reconcileConfigDocs entry.
CREATE TABLE IF NOT EXISTS sla_history (
	pop_id      TEXT    NOT NULL,
	target      TEXT    NOT NULL,
	day         DATE    NOT NULL,
	sent        INTEGER NOT NULL DEFAULT 0,
	ok_count    INTEGER NOT NULL DEFAULT 0,
	rtt_sum_ms  DOUBLE PRECISION NOT NULL DEFAULT 0,
	rtt_max_ms  DOUBLE PRECISION NOT NULL DEFAULT 0,
	PRIMARY KEY (pop_id, target, day)
);
CREATE INDEX IF NOT EXISTS sla_history_day_idx ON sla_history (day DESC);

-- sla_targets — operator-defined probe targets + their SLO. Singleton JSONB
-- config-doc (a list); dual-written with its JSON file, healed by reconcileConfigDocs.
CREATE TABLE IF NOT EXISTS sla_targets (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- [{name,target,type,slo_pct,rtt_budget_ms}]
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
