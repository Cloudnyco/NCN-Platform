-- 0018_rpki_settings.sql — operator-adjustable RPKI monitor settings. Currently
-- just the auto-poll interval (rpki.go), set from the console RPKI card. Wholesale
-- load/save → single-row JSONB document, dual-written with the JSON file.
CREATE TABLE IF NOT EXISTS rpki_settings (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- {"interval_secs": N}
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
