# NCN — self-hosted network operations platform

> **English** · [简体中文](README.zh-CN.md)

An open-source operations platform for running a small **multi-PoP anycast
network**: BGP/BIRD fleet telemetry, alerting + AI triage, RPKI monitoring,
peering intake, capacity/SLA/flow analytics, DDoS mitigation, on-call, webmail,
a self-hosted load balancer/failover, wiki, and identity — **self-hosting over
paid SaaS**.

Run it for **your own** network. Everything operator-specific is configured via
environment variables (`NCN_*`, see [`.env.example`](.env.example)) and a runtime
node registry. The repo ships with **placeholders only** — `AS64500`,
`example.com`, RFC5737/RFC3849 addresses, `ctrl-01`/`pop-0N` node names — and
points at no real network.

**Get started:** [`core-console/QUICKSTART.md`](core-console/QUICKSTART.md) covers
the four entry points — local dev, one-command deploy, fresh-host/DR bootstrap,
and Docker Compose. **License:** [Apache 2.0](LICENSE) · **Contributing:**
[`CONTRIBUTING.md`](CONTRIBUTING.md) · **Security:** [`SECURITY.md`](SECURITY.md).

> Code, comments and commit messages are English. Member-facing documentation
> (the wiki) is primarily Simplified Chinese.

---

## Contents

- [Architecture](#architecture)
- [Repository layout](#repository-layout)
- [Hosts & topology](#hosts--topology)
- [core-console](#core-console)
  - [Backend — `ncn-api`](#backend--ncn-api)
  - [Frontend — SPA](#frontend--spa)
  - [Sub-components](#sub-components-agent--lb--mcp)
- [Feature areas](#feature-areas)
- [Tech stack](#tech-stack)
- [Build & run](#build--run)
- [Deployment](#deployment)
- [Configuration](#configuration)
- [Security model](#security-model)
- [Documentation](#documentation)

---

## Architecture

```
                        Cloudflare (proxy, TLS Strict)
                                   │
        admin.example.com   example.com / wiki.example.com   mail.example.com
                │                    │                          │
                ▼                    ▼                          ▼
        ┌───────────────── ctrl-01 (control node) ───────┐  ┌── pop-03 ──┐
        │  nginx → ncn-api (:9000, Go)                   │  │  ncn-mail  │
        │  PostgreSQL (primary)                          │  │  dovecot   │
        │  Vue SPA (admin console + public site + wiki)  │  │  postfix   │
        └───────────────────────┬────────────────────────┘  │  rspamd    │
                                 │ telemetry (SSH / HTTPS)    │  Prometheus│
            ┌────────────┬───────┼───────┬────────────┐      │  Grafana   │
            ▼            ▼       ▼       ▼            ▼      │  Gatus     │
         pop-03       pop-04   pop-01   pop-08 … (8 PoPs)    └────────────┘
       (observ.+    (HA stby   (Krill CA
        standby)     + ncn-lb)  + Routinator)

   Each PoP runs BIRD (BGP, anycast) and optionally ncn-agent (telemetry).
   Backbone: 2001:db8:50::/44 (IPv6 mesh between PoPs).
```

The console **shells out to real probes** (`uptime`, `/proc/loadavg`, `birdc`,
`wg`) and reads live infrastructure — there is no mock data. Writes to the
fleet flow through one executor (`ncn-api`) which enforces auth and audits
every action.

---

## Repository layout

| Path | What it is |
|---|---|
| [`core-console/`](core-console/) | The platform: `ncn-api` (Go backend) + Vue 3 SPA. The bulk of the project. |
| `core-console/agent/` | `ncn-agent` — per-PoP telemetry agent (HTTPS + HMAC) replacing SSH polling. |
| `core-console/lb/` | `ncn-lb` — self-hosted Cloudflare-LB-equivalent: health-checked origin pools + active/passive failover. |
| `core-console/mcp/` | `ncn-mcp` — MCP server exposing the console's ops tools to Claude Code. |
| [`webmail/`](webmail/) | `ncn-mail` — standalone webmail for `mail.example.com` (self-contained on pop-03). |
| `cli/ncn-debug/` | Read-only ops CLI over the console REST API (single static Go binary). |
| `cli/ncn-login/` | Operator login helper for the CLIs. |
| `deploy-all.sh` | Single-command deploy of the whole stack (webmail → core-console). |
| `scripts/`, `backups/` | Workspace-level helper scripts and snapshot staging. |

---

## Hosts & topology

| Host | Role |
|---|---|
| **ctrl-01** | Control node. `ncn-api`, PostgreSQL **primary**, nginx for all three vhosts (console / public / wiki), WAL archiving source. |
| **pop-03** | Observability (Prometheus + Grafana + Gatus), **webmail**, PG standby + WAL-archive receiver, warm-standby `ncn-api`. |
| **pop-04** | HA: PostgreSQL **streaming standby** + `ncn-lb` failover controller + offsite DR target. |
| **pop-01** | RPKI: **Krill** CA (publishes ROAs) + **Routinator** validator (central RTR). Also a BGP node. |
| **pop-02 / pop-08 / pop-06 / pop-05** | PoPs (BGP / anycast). |

8 PoPs participate in anycast over the `2001:db8:50::/44` backbone. Public
endpoints are fronted by Cloudflare (proxied, SSL Strict).

---

## core-console

### Backend — `ncn-api`

Go `net/http` service (`package main`, ~28k LOC) listening on `:9000`,
socket-activated by systemd. State lives in **PostgreSQL** via `pgx` (`database/sql`)
with **embedded migrations** (`//go:embed migrations/*.sql`); every store is
**nil-DB tolerant** and falls back to JSON files when no database is configured,
so the binary runs with or without Postgres.

Capabilities are organized roughly one concern per file under `core-console/backend/`:

- **Fleet & nodes** — `fleet.go`, `noderegistry.go`, `nodes_api.go`,
  `nodes_onboard.go`, `heartbeat.go`, `bird_scrape.go`
- **Anycast** — `anycast.go` (drain / undrain a PoP from anycast)
- **Mesh / config** — `mesh_config.go`, `meshApply.go`, `tunnel.go`
- **HA / DR / replication** — `replmon.go` (+ `lb/`, PITR scripts)
- **Observability** — `metrics.go`, `alertmetrics.go`, `grafana_proxy.go`
- **Alerting** — `alerts.go`, `alertrules.go`, `alertrules_api.go`,
  `alertanomaly.go`
- **RPKI** — `rpki.go` (ROA-validity + ROV monitoring)
- **Auth & access** — `auth.go`, `auth_apitoken.go`, `auth_ssh.go`,
  `auth_sso.go`, `oauth.go`, `oauth_telegram.go`, `passkey.go`,
  `idp_provider.go` (console-as-OAuth2-IdP), `recover_bootstrap.go`
- **Telegram bot** — `bot_tg.go`, `bot_identity_test.go`, `bot_manage.go`,
  `bot_netadmin.go`, `bot_opfail.go`, `bot_ai.go`, `bot_agent_tg.go`,
  `bot_drill.go`, `notify_tg.go`
- **AI assistant** — `deepseek.go`, `agent.go`, `agent_tools.go`,
  `ai_usage.go`, `ai_history_api.go`, `model_config.go`
- **Wiki** — `wikistore.go`, `wiki_api.go`
- **Member-facing** — `peering_apply.go`, `peeringdb.go`, `incidents.go`,
  `visitor.go`, `billing.go`, `invite.go`
- **Mail bridge** — `mail_bridge.go`, `mail_forgot_bridge.go`,
  `mail_role_recover.go` (console ↔ webmail)
- **Ops plumbing** — `opfailures.go`, `audit.go`, `ratelimit.go`,
  `turnstile.go`, `term.go`, `admincli.go`, `db.go`, `fx.go`

### Frontend — SPA

Vue 3 + Vite single-page app (TypeScript, Tailwind, Pinia, Vue Router,
`vue-i18n`). One build serves three hosts; `App.vue` picks a layout by route:

- **admin.example.com** — the operator console (`AdminLayout`): Dashboard,
  Fleet, Servers, Alerts, Alert Rules, Observability, Performance,
  Connectivity, Bird, Security, Audit, Billing, Assistant, Wiki, Onboarding.
- **example.com** — the public site (`PublicLayout`): Landing, Looking Glass,
  Status, Peering info + application, legal pages, and the public wiki
  (`/docs`).
- **wiki.example.com** — the public wiki (same SPA dist).

i18n carries **en / zh-CN / zh-TW**; a pre-build `lint:i18n` step fails the
build if a key is missing from any locale. `marked` + `DOMPurify` render wiki
markdown; `@xterm/xterm` powers the in-console terminal.

### Sub-components (`agent` / `lb` / `mcp`)

- **`ncn-agent`** (`agent/`) — runs on each PoP (`:9101`, HTTPS + HMAC-bearer)
  and returns the same telemetry pipeline output that `fleet.go` used to
  collect over SSH. Per-node `Transport` selects `ssh` (default) or `rest`
  with SSH fallback, so it rolls out one PoP at a time.
- **`ncn-lb`** (`lb/`) — health-checked origin pools with automatic
  active/passive failover (promote PG replica → start standby `ncn-api` →
  repoint Cloudflare DNS). Runs on pop-04 to survive a ctrl-01 outage. **Ships
  in `observe` mode** (logs what it would do); arm only after the failover
  script is tested. No automatic fail-back (anti-flap).
- **`ncn-mcp`** (`mcp/`) — a thin MCP proxy that exposes the same ops tools the
  in-console AI agent uses (`list_nodes`, `fleet_status`, `run_command`, …) to
  a local Claude Code. The console backend remains the only executor and
  enforces every safety rule.

---

## Feature areas

| Area | Summary |
|---|---|
| **Fleet telemetry** | Live per-PoP metrics (load, BGP sessions via `birdc`, WireGuard, uptime); node registry + onboarding/decommission lifecycle. |
| **Anycast ops** | Human-approved drain/undrain of a PoP from anycast (`birdc disable upstream_*`); refuses to drain the last/critical node. |
| **HA & DR** | PostgreSQL streaming replication (ctrl-01 → pop-04, sub-second RPO); PITR via WAL archiving + weekly base-backups + restore drills; `ncn-lb` failover; daily state snapshots (local + offsite). |
| **Observability** | Hand-written Prometheus `/metrics` (no secrets); Prometheus + Grafana on pop-03 (Grafana embedded in the console via an admin-gated reverse proxy); Gatus uptime. |
| **Alerting** | Data-driven rules engine (sustain / resolve / escalate / repeat) **plus** EWMA-based anomaly detection (per node+metric baselines), routed to Telegram. |
| **RPKI** | Monitors our own prefixes' ROA validity (via RIPEstat) and live route-origin-validation tags read from PoP BIRD; Krill publishes ROAs, Routinator validates. |
| **Auth & access** | Session cookies (HMAC) + TOTP; OAuth (Google / Microsoft / GitHub / Telegram); passkeys; API tokens; SSH-key login; the console itself acts as an OAuth2 **IdP** for SSO into other services. |
| **Telegram bot** | Operator-bound, approval-gated control of the fleet; op-failure cards; AI Q&A; group companion. |
| **AI assistant** | DeepSeek-backed tool-calling ops agent (inspect + human-approved act, incl. `run_command`); available in the console and over MCP. |
| **Self-hosted wiki** | Markdown content in Postgres, rendered in-app; public (anonymous) + internal (operator-gated) tiers; in-browser editing with version history. |
| **Member-facing** | Public landing, Looking Glass, status/incidents page, PeeringDB-backed peering info + application intake. |
| **Webmail** | `ncn-mail` on pop-03 backing `mail.example.com` (IMAP/SMTP loopback to dovecot/postfix, DKIM via rspamd). |
| **Audit** | Every privileged action is recorded; audit log is rsynced offsite on a timer. |

---

## Tech stack

- **Backend:** Go 1.25, `net/http`, `pgx` v5 / PostgreSQL 17, embedded SQL migrations.
- **Frontend:** Vue 3.5, Vite 6, TypeScript, Tailwind 3, Pinia, Vue Router, vue-i18n, marked + DOMPurify, xterm.js.
- **Infrastructure:** nginx, systemd (socket-activated services + timers), BIRD (BGP), Krill + Routinator (RPKI), Prometheus + Grafana + Gatus, Cloudflare (edge), WireGuard (backbone).

---

## Build & run

Prerequisites: **Go ≥ 1.25**, **Node ≥ 20** (for the SPA builds).

```sh
# Backend (core-console)
cd core-console/backend
go vet ./... && go test ./...
go build -o ncn-api .
./ncn-api                      # listens on :9000

# Frontend (core-console SPA)
cd core-console
npm install
npm run dev                    # Vite dev server
npm run build                  # type-check + i18n lint + production dist/

# Operator CLI
cd cli/ncn-debug && go build -o ncn-debug
```

PostgreSQL is optional for a local run — without `NCN_DB_*` the backend uses
its JSON file fallback.

---

## Deployment

Deploys are **gated** and run from the workspace against the live hosts over SSH.

```sh
# core-console only
bash core-console/deploy/deploy.sh backend     # go vet + go test on tyo, build, atomic swap, zero-downtime restart
bash core-console/deploy/deploy.sh frontend    # i18n lint + type-check + vite build, ship dist/

# whole stack (webmail first, then core-console)
./deploy-all.sh
```

- The backend deploy refuses to ship unless `go vet ./...` and `go test ./...`
  pass on the target; it backs up the previous binary and supports
  `deploy.sh rollback`.
- `ncn-api` is socket-activated (`ncn-api.socket` + `ncn-api.service`), so the
  swap is zero-downtime.
- A daily state DR snapshot (local + offsite pop-04) and an audit-log offsite
  rsync run on systemd timers (`deploy/ncn-state-backup.*`, `deploy/ncn-audit-rsync.*`).

---

## Configuration

- Runtime config and secrets live under `/etc/ncn-core-console/` on ctrl-01
  (e.g. `session.key`, `oauth.env`, the fleet SSH key). See
  `core-console/deploy/oauth.env.example`.
- nginx vhosts: `core-console/deploy/nginx-ncn-core-console.conf` (console /
  public / wiki) — public endpoints are explicitly allow-listed; everything
  else under `/api/` returns 404 from public hosts.
- `ncn-lb` reads `/etc/ncn-lb/config.json`; webmail and the bot read their own
  env files on their respective hosts.

Secrets are never committed. Real values are provisioned out-of-band on the
hosts; `*.example` files document the expected shape.

---

## Security model

- **Three auth tiers:** public (nginx allow-list), internal (any authenticated
  operator), and admin (role-gated). The session cookie is HMAC-signed and
  **host-scoped to `admin.example.com`**.
- **Host separation** is enforced at both nginx and the SPA router (defense in
  depth): the public hosts never expose admin APIs.
- **One executor:** all fleet writes go through `ncn-api`. The MCP server, the
  bot, and the CLIs are clients — they cannot touch the fleet directly. Write /
  command tools require an **admin** operator and are **audited**.
- **Fleet access** uses a dedicated SSH key on ctrl-01; remote command execution
  is human-approved through the agent flow.
- **Content safety:** wiki markdown is sanitized with DOMPurify before render;
  the public wiki API server-side-enforces `is_public` so internal pages never
  leak.

---

## Documentation

- **Operator handbook & member docs:** the self-hosted wiki — `wiki.example.com`
  (public) and `admin.example.com/admin/wiki` (internal ops). Source markdown in
  `core-console/wiki/`.
- **Runbooks:** `core-console/docs/` (`PITR-RESTORE.md`, `POP-ONBOARDING.md`)
  and the wiki's `ops/runbooks/*`.
- **Per-component READMEs:** `webmail/README.md`, `core-console/agent/README.md`,
  `core-console/mcp/README.md`, `cli/ncn-debug/README.md`.
