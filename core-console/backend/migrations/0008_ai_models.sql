-- 0008_ai_models.sql — ai_models store migrated to Postgres (JSONB doc, dual-write).
-- Loaded/saved wholesale → single-row document, not per-record columns.
CREATE TABLE IF NOT EXISTS ai_models (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
