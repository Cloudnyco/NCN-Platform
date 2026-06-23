-- 0012_operators.sql — operator accounts (was operators.json on ctrl-01). Holds
-- the full operatorRecord array incl. embedded passkeys + trusted devices +
-- recovery codes + invite/approval state. Loaded wholesale; the ~9 mutation
-- handlers all rewrite the whole list, so a single-row JSONB document (no
-- per-field columns). Dual-write: operators.json stays a live backup.
CREATE TABLE IF NOT EXISTS operators (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- the full []operatorRecord array
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
