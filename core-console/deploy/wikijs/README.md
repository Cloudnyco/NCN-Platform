# Wiki.js — editable documentation

> **English** · [简体中文](README.zh-CN.md)

This deployment replaces the static MkDocs wiki with Wiki.js 2.x, which provides
an in-browser editor, version history, full-text search, and a configurable
theme. It reuses the existing PostgreSQL database.

## Topology

```
operator browser
  ├─ setup:  ssh -L 3002:127.0.0.1:3002 deploy-host  →  http://localhost:3002
  └─ prod:   https://wiki.example.com  (ctrl-01 nginx → ctrl-01:3002 tunnel → pop-03 Wiki.js)
                                       │
       Wiki.js (pop-03, localhost:3002, dedicated service user, on a data volume at /var/lib/wikijs)
                                       │
       DB: wikijs @ ctrl-01 primary Postgres (2001:db8:53::1) over the backbone
           → inherits streaming replication + PITR
```

## Components

| File | Where | What |
|---|---|---|
| `install-pop03.sh` | pop-03 | download + extract Wiki.js + systemd unit |
| `config.sample.yml` | pop-03 `/var/lib/wikijs/config.yml` | DB DSN (password from `wikijs.pass`) |
| `ncn-wikijs.service` | pop-03 | the Wiki.js service |
| `ncn-wikijs-tunnel.service` | ctrl-01 | `ssh -L` ctrl-01:3002 → pop-03 Wiki.js |

The `wikijs` database and role reside on the ctrl-01 primary. `pg_hba` allows
`wikijs` connections from `2001:db8:50::/44`. The password is stored on ctrl-01
at `/etc/ncn-core-console/wikijs.pass` (mode 0600).

## Setup (one-time, browser via tunnel)

1. `ssh -L 3002:127.0.0.1:3002 deploy-host`, open `http://localhost:3002`.
2. Complete the wizard: admin email, a strong password, and the site URL
   `https://wiki.example.com`.
3. Post-setup (Admin area, or scripted via the GraphQL API):
   - **Storage → Git**: point at this repository (deploy key) so edits sync to git.
   - **Groups**: a `Guests` (anonymous) group with read access on the public path
     only; authenticated users read all pages; admins write.
   - **Theme / Locale**: dark theme, locale as required.
   - Import the existing Markdown (from the prior `wiki/` tree) as the initial pages.

## SSO — login via the console (console as IdP)

Wiki.js authenticates through the console's own login page rather than an
external provider:

```
Wiki.js account login  →  admin.example.com/api/v1/auth/idp/authorize
   (no console session? → 302 /login → operator logs in → full-page return)
   → code → wiki.example.com/login/oauth2/callback
   → Wiki.js exchanges at /idp/token → reads /idp/userinfo → signs in
```

- The IdP is an OAuth2 authorization-code provider in ncn-api
  (`backend/idp_provider.go`, `/api/v1/auth/idp/{authorize,token,userinfo}`),
  gated by the existing console session. Client credentials are stored in
  `oauth.env` (`NCN_WIKI_OAUTH_CLIENT_ID/_SECRET/_REDIRECT`).
- Only an authenticated console operator (already bound and approved) can obtain
  a code, so Wiki.js auto-enrolls every SSO user into the Operators group
  (id 3 → read `ops/*`). The strategy is `oauth2` in the `authentication` table.
- `Login.vue` routes all post-login redirects through `navigateAfterLogin()`, so
  an `/api/` next target (the authorize URL) performs a full-page redirect rather
  than SPA routing.

## Branding

Branding values are stored in the `settings` table (the value column is `json`,
not jsonb — use `pg_read_file(...)::json` and restart afterward): `title` and
`company` set the site name; `theming.darkMode=true`; `theming.injectHead`
defines an SVG favicon; `theming.injectCSS` prepends the same glyph in the header
via `.nav-header-inner::before`. A header logo image can also be uploaded in
Admin → General → Site Logo (sets `logo.hasLogo`). Groups: Guests(2) read
`home` and `public/*` only; Operators(3) read all pages.

## Cutover

Once configuration and access rules are in place, point `wiki.example.com` at
Wiki.js (nginx reverse-proxy → :3002 tunnel) and retire the static MkDocs vhost.
Wiki.js manages its own authentication, so public pages are anonymous-read and
internal pages require login, both served from the same host.
