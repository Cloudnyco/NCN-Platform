-- 0021_config_declarations.sql — declarative config baseline for drift detection.
--
-- The console could generate + apply config but had no "source of truth" to
-- compare a node's LIVE config against, so a manual edit on a box went unnoticed
-- (we hit exactly this during the RPKI migration). config_declarations stores,
-- per node, the adopted baseline of /etc/bird/bird.conf, filters_templates.conf,
-- and the stateless nft ruleset (content + sha256). configdrift.go periodically
-- re-hashes the live files and flags drift. Singleton JSONB config-doc
-- (map[nodeID]decl), dual-written with its JSON file + healed by reconcileConfigDocs.
CREATE TABLE IF NOT EXISTS config_declarations (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- {nodeID: {bird_conf, filters, nft, *_hash, captured_at, captured_by}}
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
