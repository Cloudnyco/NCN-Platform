-- 0013_invites.sql — operator invite tokens (was invites.json on ctrl-01).
-- Loaded/saved wholesale (gc'd in memory), single-row JSONB document.
-- Dual-write: invites.json stays a live backup during the transition.
CREATE TABLE IF NOT EXISTS invites (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- the full []inviteToken array
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
