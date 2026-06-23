-- 0014_alert_history.sql — durable alert history. The engine's history was an
-- in-memory ring (last 200 events), lost on every restart — inconsistent with
-- the rest of the persistence foundation. Wholesale load/save → single-row
-- JSONB document (the capped ring), flushed periodically by the engine.
CREATE TABLE IF NOT EXISTS alert_history (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- the full []alertEvent ring (newest cap)
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
