# Wiki.js — editable, polished docs

Replaces the static MkDocs wiki with **Wiki.js 2.x** (in-browser editor, version
history, full-text search, polished theme), reusing our Postgres.

## Topology

```
operator browser
  ├─ setup:  ssh -L 3002:127.0.0.1:3002 deploy-host  →  http://localhost:3002
  └─ prod:   https://wiki.example.com  (tyo nginx → tyo:3002 tunnel → pop-03 Wiki.js)
                                       │
       Wiki.js (pop-03, localhost:3002, user ncnmon, on sdb /var/mail/vhosts/wikijs)
                                       │
       DB: wikijs @ ctrl-01 primary Postgres (2001:db8:53::1) over the backbone
           → rides streaming replication + PITR for free
```

## Pieces

| File | Where | What |
|---|---|---|
| `install-pop03.sh` | pop-03 | download + extract Wiki.js + systemd unit |
| `config.sample.yml` | pop-03 `/var/mail/vhosts/wikijs/config.yml` | DB DSN (pass from tyo `wikijs.pass`) |
| `ncn-wikijs.service` | pop-03 | the Wiki.js service |
| `ncn-wikijs-tunnel.service` | ctrl-01 | `ssh -L` tyo:3002 → pop-03 Wiki.js |

DB: `wikijs` database + role on the ctrl-01 primary; `pg_hba` allows `wikijs` from
`2001:db8:50::/44`. Password in tyo `/etc/ncn-core-console/wikijs.pass` (0600).

## Setup (one-time, browser via tunnel)

1. `ssh -L 3002:127.0.0.1:3002 deploy-host`, open `http://localhost:3002`.
2. Complete the wizard: admin email + a strong password + site URL
   `https://wiki.example.com`.
3. Post-setup (Admin area, or scripted via the GraphQL API):
   - **Storage → Git**: point at this repo (deploy key) so edits sync to git.
   - **Groups**: a `Guests` (anonymous) group with read on the public path only;
     authenticated users read all; admins write.
   - **Theme / Locale**: dark theme, zh.
   - Import the existing Markdown (from the old `wiki/` tree) as the initial pages.

## SSO — login via OUR page (console as IdP)

Wiki.js authenticates through the console's own login page, not an external
provider:

```
Wiki.js "NCN 账号登录"  →  admin.example.com/api/v1/auth/idp/authorize
   (no console session? → 302 /login → operator logs in → full-page back)
   → code → wiki.example.com/login/oauth2/callback
   → Wiki.js exchanges at /idp/token → reads /idp/userinfo → signs in
```

- IdP is a minimal OAuth2 authorization-code provider in ncn-api
  (`backend/idp_provider.go`, `/api/v1/auth/idp/{authorize,token,userinfo}`),
  gated by the existing console session. Client creds in tyo `oauth.env`
  (`NCN_WIKI_OAUTH_CLIENT_ID/_SECRET/_REDIRECT`).
- Only an authenticated console operator (already bound+approved) can obtain a
  code, so Wiki.js **auto-enrolls** every SSO user into the **Operators** group
  (id 3 → read `ops/*`). Strategy `oauth2` in the `authentication` table.
- `Login.vue` routes all post-login redirects through `navigateAfterLogin()` so
  an `/api/` next (the authorize URL) does a full-page redirect, not SPA routing.

## Branding

In the `settings` table (value is `json`, not jsonb — use `pg_read_file(...)::json`
+ restart): `title`/`company` = Acme Net; `theming.darkMode=true`;
`theming.injectHead` = a teal anycast-glyph SVG favicon; `theming.injectCSS`
prepends the same glyph in the header via `.nav-header-inner::before`. A proper
header logo IMAGE can also be uploaded in Admin → General → Site Logo (sets
`logo.hasLogo`). Groups: Guests(2)=read `home`+`public/*` only; Operators(3)=read all.

## Cutover

Once configured + access rules locked down, point `wiki.example.com` at Wiki.js
(tyo nginx reverse-proxy → :3002 tunnel) and retire the static MkDocs vhost.
Wiki.js handles its own auth, so public pages are anonymous-read and internal
pages require login — both on the one host.
