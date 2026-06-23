-- 0002_audit.sql — durable audit log (was append-only JSONL on ctrl-01 only).
-- Mirrors AuditEvent (audit.go). details is free-form JSONB. Dual-path: the
-- JSONL file stays the fallback when globalDB is nil or a query errors.
CREATE TABLE IF NOT EXISTS audit (
	id       TEXT        PRIMARY KEY,         -- 16-hex event id
	ts       TIMESTAMPTZ NOT NULL,            -- event time (UTC)
	event    TEXT        NOT NULL,            -- dot-separated name e.g. login.ok
	severity TEXT        NOT NULL,            -- info | warn | critical
	actor    TEXT        NOT NULL DEFAULT '', -- operator / anonymous / system
	peer     TEXT        NOT NULL DEFAULT '', -- sanitized client addr
	ua       TEXT        NOT NULL DEFAULT '', -- User-Agent
	target   TEXT        NOT NULL DEFAULT '', -- who/what was acted on
	outcome  TEXT        NOT NULL DEFAULT '', -- ok | fail | denied
	details  JSONB                            -- free-form context (no secrets)
);

-- Newest-first paging (the common query) + the cheap exact-match filters.
CREATE INDEX IF NOT EXISTS audit_ts_id_idx ON audit (ts DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_event_idx ON audit (event);
CREATE INDEX IF NOT EXISTS audit_actor_idx ON audit (actor);
