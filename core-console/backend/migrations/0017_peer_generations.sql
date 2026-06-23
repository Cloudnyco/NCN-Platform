-- 0017_peer_generations.sql — generated per-peer BIRD configs (peering/IRR
-- automation). One singleton JSONB document holding map[asn]peerGeneration:
-- the bgpq4-expanded prefix set + generated config + prefix hash (drift) +
-- apply state. Dual-written with peer-generations.json, like the other stores.
CREATE TABLE IF NOT EXISTS peer_generations (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
