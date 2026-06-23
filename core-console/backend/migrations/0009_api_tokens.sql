-- 0009_api_tokens.sql — api_tokens store migrated to Postgres (JSONB doc, dual-write).
-- Loaded/saved wholesale → single-row document, not per-record columns.
CREATE TABLE IF NOT EXISTS api_tokens (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
