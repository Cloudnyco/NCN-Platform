-- 0003_node_registry.sql — the PoP fleet registry (was nodes.json on ctrl-01).
-- Mirrors nodeRecord (noderegistry.go). `ord` preserves the site-wide display
-- order (the JSON file was an ordered slice). Dual-write: the JSON file stays
-- a live backup during the transition; reads come from here when present.
CREATE TABLE IF NOT EXISTS node_registry (
	ord          INT              NOT NULL,            -- display order (slice index)
	id           TEXT             PRIMARY KEY,
	label        TEXT             NOT NULL DEFAULT '',
	country      TEXT             NOT NULL DEFAULT '',
	address      TEXT             NOT NULL DEFAULT '',
	lat          DOUBLE PRECISION NOT NULL DEFAULT 0,
	lon          DOUBLE PRECISION NOT NULL DEFAULT 0,
	ssh_user     TEXT             NOT NULL DEFAULT '',
	ssh_identity TEXT             NOT NULL DEFAULT '',
	ssh_port     INT              NOT NULL DEFAULT 0,
	region       INT              NOT NULL DEFAULT 0,
	node_num     INT              NOT NULL DEFAULT 0,
	arch         TEXT             NOT NULL DEFAULT '',
	status       TEXT             NOT NULL DEFAULT 'active',
	notes        TEXT             NOT NULL DEFAULT '',
	created_by   TEXT             NOT NULL DEFAULT '',
	created_at   TIMESTAMPTZ      NOT NULL,
	updated_at   TIMESTAMPTZ      NOT NULL
);
CREATE INDEX IF NOT EXISTS node_registry_ord_idx ON node_registry (ord);
