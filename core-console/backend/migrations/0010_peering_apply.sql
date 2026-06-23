-- 0010_peering_apply.sql — peering_apply store migrated to Postgres (JSONB doc, dual-write).
-- Loaded/saved wholesale → single-row document, not per-record columns.
CREATE TABLE IF NOT EXISTS peering_apply (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
