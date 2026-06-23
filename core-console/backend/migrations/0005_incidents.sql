-- 0005_incidents.sql — public status incidents (was incidents.json on ctrl-01).
-- Loaded/saved wholesale (filtered in memory: 30-day public window, status),
-- so a single-row JSONB document, not per-incident columns. Dual-write: the
-- JSON file stays a live backup during the transition.
CREATE TABLE IF NOT EXISTS incidents (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- the full []Incident array
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
