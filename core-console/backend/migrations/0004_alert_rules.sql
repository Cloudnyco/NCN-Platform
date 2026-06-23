-- 0004_alert_rules.sql — the alert rules + groups config (was alert-rules.json
-- on ctrl-01 only). This store is only ever loaded/saved WHOLESALE (the engine
-- queries rules in memory, never in SQL), so it's a single-row JSONB document
-- rather than per-field columns — schema-stable as ruleDef/ruleGroup gain
-- fields. Dual-write: the JSON file stays a live backup during the transition.
CREATE TABLE IF NOT EXISTS alert_rules (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- the full {groups, rules} document
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
