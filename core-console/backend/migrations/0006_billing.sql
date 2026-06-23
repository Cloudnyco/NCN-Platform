-- 0006_billing.sql — VPS subscriptions / billing (was subscriptions.json on
-- ctrl-01). Loaded/saved wholesale (renewal thresholds computed in memory), so
-- a single-row JSONB document, not per-subscription columns. Dual-write: the
-- JSON file stays a live backup during the transition.
CREATE TABLE IF NOT EXISTS billing (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- the full []VPSSubscription array
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
