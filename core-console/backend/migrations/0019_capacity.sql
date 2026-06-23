-- 0019_capacity.sql — long-term capacity planning.
--
-- The fleet only kept 15-min in-memory rings (lost on restart), so there was no
-- way to see multi-week trends or forecast link saturation. capacity_series is
-- a durable per-(node,metric,UTC-day) rollup (max / mean / p95) written by
-- capacity.go — one row per node/metric/day, pruned past ~400 days. NOT a
-- singleton config-doc (it's real time-series), so it is NOT in reconcileConfigDocs.
CREATE TABLE IF NOT EXISTS capacity_series (
	node_id  TEXT    NOT NULL,
	metric   TEXT    NOT NULL,
	day      DATE    NOT NULL,
	maxv     DOUBLE PRECISION NOT NULL DEFAULT 0,
	meanv    DOUBLE PRECISION NOT NULL DEFAULT 0,
	p95v     DOUBLE PRECISION NOT NULL DEFAULT 0,
	samples  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (node_id, metric, day)
);
CREATE INDEX IF NOT EXISTS capacity_series_day_idx ON capacity_series (day DESC);

-- link_capacity — operator-set per-node link capacity (Mbps), used to forecast
-- "days until saturation". Singleton JSONB config-doc ({node_id: mbps}); dual-
-- written with its JSON file and healed by reconcileConfigDocs.
CREATE TABLE IF NOT EXISTS link_capacity (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- {"ctrl-01": 1000, ...} Mbps
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
