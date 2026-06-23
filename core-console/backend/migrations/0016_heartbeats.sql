-- 0016_heartbeats.sql — durable availability history behind the public status
-- page. heartbeat.go kept its per-component up/down day buckets only in a JSON
-- file; this is the last store still file-only, inconsistent with the rest of
-- the persistence foundation. Wholesale load/save → single-row JSONB document
-- (the full map[name]*hbComponent), dual-written with the file every 5 min.
CREATE TABLE IF NOT EXISTS heartbeats (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- the full map[name]*hbComponent
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
