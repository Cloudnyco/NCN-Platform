-- 0001: op-failures — failed fleet actions. Was a bounded in-memory ring
-- (newest 100, lost on restart); this makes it durable. No data to migrate.
CREATE TABLE IF NOT EXISTS op_failures (
    id              TEXT PRIMARY KEY,        -- 8-hex, stable for retry/dismiss callbacks
    kind            TEXT NOT NULL,
    target          TEXT NOT NULL DEFAULT '',
    actor           TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT '',
    at              BIGINT NOT NULL,         -- unix seconds
    status          TEXT NOT NULL DEFAULT 'open',  -- open | dismissed
    mesh_targets    JSONB,
    mesh_transports JSONB,
    mesh_region     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS op_failures_at_idx ON op_failures (at DESC);
