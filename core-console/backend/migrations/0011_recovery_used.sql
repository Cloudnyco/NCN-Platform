-- 0011_recovery_used.sql — recovery_used store migrated to Postgres (JSONB doc, dual-write).
-- Loaded/saved wholesale → single-row document, not per-record columns.
CREATE TABLE IF NOT EXISTS recovery_used (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
