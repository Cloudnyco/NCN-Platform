-- 0023_oncall.sql — on-call rotation + escalation policy.
--
-- The alert engine could auto-bump severity (EscalateSecs) but never PAGED a
-- specific person. This stores a weekly on-call rotation + an escalation policy
-- (tiers: after N minutes unacked → DM on-call / DM admins / post to group).
-- oncall.go runs the escalation loop. Singleton JSONB config-doc; dual-written
-- with its JSON file + healed by reconcileConfigDocs. Per-alert escalation state
-- is ephemeral (in-memory), not persisted.
CREATE TABLE IF NOT EXISTS oncall (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- {rotation, start_date, period_days, tiers}
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
