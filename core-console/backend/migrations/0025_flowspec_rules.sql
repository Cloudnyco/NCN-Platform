-- 0025_flowspec_rules.sql — DDoS mitigation rules (nft-based, internal drop).
--
-- "FlowSpec-style" 5-tuple drop/rate-limit rules distributed to our own edge
-- PoPs as nftables rules in a dedicated `inet ncn_ddos` table (BIRD/Linux can't
-- program the kernel from BGP flowspec, so we push nft directly — also dodging
-- the BIRD-3.3.1 roa_check fragility). Human-confirmed, TTL auto-expire, never
-- auto-applied. Singleton JSONB config-doc (map[id]rule); dual-written with its
-- JSON file + healed by reconcileConfigDocs. Per-rule, not per-row, since the
-- store loads them all into memory to regenerate each node's table.
CREATE TABLE IF NOT EXISTS flowspec_rules (
	id         TEXT        PRIMARY KEY,  -- always 'singleton'
	doc        JSONB       NOT NULL,     -- {ruleID: {match, action, ttl, applied_pops, ...}}
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
