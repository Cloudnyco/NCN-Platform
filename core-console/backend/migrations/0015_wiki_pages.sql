-- 0015: wiki pages — the self-hosted wiki content (replaces the Wiki.js dep;
-- Wiki.js stays as a backup). Pages are markdown, rendered on the frontend.
-- is_public gates the anonymous tier; internal pages need a logged-in operator.
CREATE TABLE IF NOT EXISTS wiki_pages (
    path        TEXT PRIMARY KEY,            -- e.g. home, public/network, ops/systems/ha
    title       TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL DEFAULT '',    -- markdown
    locale      TEXT NOT NULL DEFAULT 'zh',
    is_public   BOOLEAN NOT NULL DEFAULT false,
    sort        INTEGER NOT NULL DEFAULT 0,
    updated_by  TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS wiki_pages_public_idx ON wiki_pages (is_public);

-- Per-page version history (snapshot of the PREVIOUS content on each save), for
-- the history panel + revert.
CREATE TABLE IF NOT EXISTS wiki_page_versions (
    id          BIGSERIAL PRIMARY KEY,
    path        TEXT NOT NULL,
    title       TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL DEFAULT '',
    version     INTEGER NOT NULL DEFAULT 0,
    edited_by   TEXT NOT NULL DEFAULT '',
    edited_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS wiki_page_versions_path_idx ON wiki_page_versions (path, id DESC);
