# NCN — self-hosted network operations platform

> **English** · [简体中文](README.zh-CN.md)

NCN is an operations platform for a multi-PoP anycast network. It provides
BGP/BIRD fleet telemetry, alerting with rule- and anomaly-based detection, RPKI
monitoring, peering intake, capacity / SLA / flow analytics, DDoS mitigation,
on-call scheduling, webmail, a load balancer with active/passive failover, a
wiki, and identity with SSO.

All operator-specific values are supplied through environment variables
(`NCN_*`, see [`.env.example`](.env.example)) and a runtime node registry. The
repository ships with placeholders only — `AS64500`, `example.com`,
RFC5737/RFC3849 addresses, and `ctrl-01` / `pop-0N` node names — and references
no real network.

Setup is documented in [`core-console/QUICKSTART.md`](core-console/QUICKSTART.md)
(four entry points: local development, single-command deploy, fresh-host /
disaster-recovery bootstrap, and Docker Compose) and
[`DEPLOYMENT.md`](DEPLOYMENT.md) (from scratch to a high-availability control
plane).

**License:** [Apache 2.0](LICENSE) · **Contributing:**
[`CONTRIBUTING.md`](CONTRIBUTING.md) · **Security:** [`SECURITY.md`](SECURITY.md)

---

## Contents

- [Architecture](#architecture)
- [Repository layout](#repository-layout)
- [Roles & topology](#roles--topology)
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
                        edge / CDN (TLS)
                                │
        admin.example.com   example.com / wiki.example.com   mail.example.com
                │                    │                          │
                ▼                    ▼                          ▼
        ┌──────────── control node ──────────────────────┐  ┌─ service node ─┐
        │  nginx → ncn-api (:9000, Go)                    │  │  webmail        │
        │  PostgreSQL (primary)                           │  │  observability  │
        │  Vue SPA (admin console + public site + wiki)   │  │  PG standby …   │
        └───────────────────────┬─────────────────────────┘  └─────────────────┘
                                 │ telemetry (SSH / HTTPS)
            ┌──────────┬─────────┼─────────┬───────────┐
            ▼          ▼         ▼         ▼           ▼
          PoP        PoP       PoP       PoP    …   (any number of PoPs)

   Each PoP runs BIRD (BGP, anycast) and, optionally, ncn-agent for telemetry.
   Roles — control, observability, HA standby, RPKI, edge — are composable and
   may share a host or be distributed. The backbone is an IPv6 mesh between PoPs.
```

The control plane reads live infrastructure by executing real probes (`uptime`,
`/proc/loadavg`, `birdc`, `wg`); there is no mock data. All writes to the fleet
pass through a single executor (`ncn-api`), which enforces authentication and
records an audit entry for every action.

---

## Repository layout

| Path | Description |
|---|---|
| [`core-console/`](core-console/) | The platform: `ncn-api` (Go backend) and the Vue 3 SPA. |
| `core-console/agent/` | `ncn-agent` — per-PoP telemetry agent (HTTPS + HMAC), an alternative to SSH polling. |
| `core-console/lb/` | `ncn-lb` — load balancer: health-checked origin pools with active/passive failover. |
| `core-console/mcp/` | `ncn-mcp` — MCP server exposing the console's operations tools. |
| [`webmail/`](webmail/) | `ncn-mail` — standalone webmail for `mail.example.com`. |
| `cli/ncn-debug/` | Read-only operations CLI over the console REST API (single static Go binary). |
| `cli/ncn-login/` | Operator authentication helper for the CLIs. |
| `deploy-all.sh` | Single-command deploy of the full stack (webmail, then core-console). |
| `scripts/` | Workspace-level helper scripts (backup/restore, PITR, provisioning). |

---

## Roles & topology

The platform is a set of composable roles mapped onto an arbitrary number of
hosts, from a single machine to many PoPs. No fixed node count is assumed.

| Role | Components | Cardinality |
|---|---|---|
| **Control** | `ncn-api`, PostgreSQL primary, nginx (console / public / wiki vhosts) | exactly one; the sole writer to the fleet |
| **Edge PoP** | BIRD (BGP / anycast), optionally `ncn-agent` | any number; the control plane reads them |
| **Observability** | Prometheus, Grafana, Gatus | optional; any host |
| **HA standby** | PostgreSQL streaming replica, warm `ncn-api`, `ncn-lb` failover | optional; see [`DEPLOYMENT.md`](DEPLOYMENT.md) |
| **RPKI** | Krill CA (publishes ROAs), Routinator (validates) | optional |
| **Webmail** | `ncn-mail`, Postfix/Dovecot | optional |

Roles may be collapsed onto one host for a minimal deployment or distributed
across dedicated hosts at scale; edge PoPs scale horizontally. Public endpoints
are served behind an external edge / CDN over TLS. Node identifiers such as
`ctrl-01` and `pop-01` are illustrative placeholders, assigned by the operator
in the node registry.

---

## core-console

### Backend — `ncn-api`

A Go `net/http` service (`package main`) listening on `:9000`, socket-activated
by systemd. State is stored in PostgreSQL via `pgx` (`database/sql`) with
embedded migrations (`//go:embed migrations/*.sql`). Every store is nil-DB
tolerant and falls back to JSON files when no database is configured, so the
binary runs with or without PostgreSQL.

Capabilities are organized approximately one concern per file under
`core-console/backend/`:

- **Fleet & nodes** — `fleet.go`, `noderegistry.go`, `nodes_api.go`,
  `nodes_onboard.go`, `heartbeat.go`, `bird_scrape.go`
- **Anycast** — `anycast.go` (drain / undrain a PoP from anycast)
- **Mesh / config** — `mesh_config.go`, `meshApply.go`, `tunnel.go`
- **HA / DR / replication** — `replmon.go` (with `lb/` and the PITR scripts)
- **Observability** — `metrics.go`, `alertmetrics.go`, `grafana_proxy.go`
- **Alerting** — `alerts.go`, `alertrules.go`, `alertrules_api.go`,
  `alertanomaly.go`
- **RPKI** — `rpki.go` (ROA-validity and ROV monitoring)
- **Auth & access** — `auth.go`, `auth_apitoken.go`, `auth_ssh.go`,
  `auth_sso.go`, `oauth.go`, `oauth_telegram.go`, `passkey.go`,
  `idp_provider.go` (console as OAuth2 IdP), `recover_bootstrap.go`
- **Telegram bot** — `bot_tg.go`, `bot_manage.go`, `bot_netadmin.go`,
  `bot_opfail.go`, `bot_ai.go`, `bot_agent_tg.go`, `bot_drill.go`, `notify_tg.go`
- **AI assistant** — `deepseek.go`, `agent.go`, `agent_tools.go`,
  `ai_usage.go`, `ai_history_api.go`, `model_config.go`
- **Wiki** — `wikistore.go`, `wiki_api.go`
- **Member-facing** — `peering_apply.go`, `peeringdb.go`, `incidents.go`,
  `visitor.go`, `billing.go`, `invite.go`
- **Mail bridge** — `mail_bridge.go`, `mail_forgot_bridge.go`,
  `mail_role_recover.go` (console ↔ webmail)
- **Operations plumbing** — `opfailures.go`, `audit.go`, `ratelimit.go`,
  `turnstile.go`, `term.go`, `admincli.go`, `db.go`, `fx.go`

### Frontend — SPA

A Vue 3 + Vite single-page application (TypeScript, Tailwind, Pinia, Vue Router,
`vue-i18n`). One build serves three hosts; `App.vue` selects a layout by route:

- **admin host** — operator console (`AdminLayout`): Dashboard, Fleet, Servers,
  Alerts, Alert Rules, Observability, Performance, Connectivity, Bird, Security,
  Audit, Billing, Assistant, Wiki, Onboarding.
- **public host** — public site (`PublicLayout`): Landing, Looking Glass, Status,
  peering information and application, legal pages, and the public wiki (`/docs`).
- **wiki host** — public wiki (the same SPA build).

Localization ships `en` / `zh-CN` / `zh-TW`; a pre-build `lint:i18n` step fails
the build if a key is missing from any locale. `marked` and `DOMPurify` render
wiki markdown; `@xterm/xterm` provides the in-console terminal.

### Sub-components (`agent` / `lb` / `mcp`)

- **`ncn-agent`** (`agent/`) — runs on a PoP (`:9101`, HTTPS + HMAC bearer) and
  returns the same telemetry that `fleet.go` otherwise collects over SSH. A
  per-node `Transport` selects `ssh` (default) or `rest` with SSH fallback,
  allowing incremental rollout.
- **`ncn-lb`** (`lb/`) — health-checked origin pools with automatic
  active/passive failover (promote the PostgreSQL replica, start the standby
  `ncn-api`, repoint DNS). Ships in `observe` mode (logging intended actions
  only); arming is a deliberate step after the failover script is validated.
  There is no automatic fail-back, to avoid flapping.
- **`ncn-mcp`** (`mcp/`) — a Model Context Protocol proxy exposing the same
  operations tools used by the in-console AI agent. The console backend remains
  the sole executor and enforces every safety rule.

---

## Feature areas

| Area | Summary |
|---|---|
| **Fleet telemetry** | Per-PoP metrics (load, BGP sessions via `birdc`, WireGuard, uptime); node registry with an onboarding/decommission lifecycle. |
| **Anycast operations** | Approval-gated drain/undrain of a PoP from anycast (`birdc disable upstream_*`); refuses to drain the last or a critical node. |
| **HA & DR** | PostgreSQL streaming replication (primary → standby, sub-second RPO); PITR via WAL archiving, weekly base-backups, and restore drills; `ncn-lb` failover; daily state snapshots (local and offsite). |
| **Observability** | Hand-written Prometheus `/metrics` (no secrets); Prometheus and Grafana on an observability host (Grafana embedded via an admin-gated reverse proxy); Gatus uptime. |
| **Alerting** | Data-driven rules engine (sustain / resolve / escalate / repeat) and EWMA-based anomaly detection (per node+metric baselines), routed to Telegram. |
| **RPKI** | Monitors the operator's prefixes for ROA validity (via RIPEstat) and live route-origin-validation tags from PoP BIRD; Krill publishes ROAs, Routinator validates. |
| **Auth & access** | HMAC session cookies and TOTP; OAuth/OIDC (GitHub, Telegram); passkeys; API tokens; SSH-key login. The console can act as an OAuth2 identity provider for SSO into other services. |
| **Telegram bot** | Operator-bound, approval-gated fleet control; operation-failure cards; AI Q&A; group companion. |
| **AI assistant** | DeepSeek-compatible tool-calling agent (inspect, plus human-approved actions including `run_command`); available in the console and over MCP. |
| **Self-hosted wiki** | Markdown content in PostgreSQL, rendered in-app; public (anonymous) and internal (operator-gated) tiers; in-browser editing with version history. |
| **Member-facing** | Public landing page, Looking Glass, status/incidents page, and PeeringDB-backed peering information and application intake. |
| **Webmail** | `ncn-mail` for `mail.example.com` (IMAP/SMTP loopback to Dovecot/Postfix, DKIM via rspamd). |
| **Audit** | Every privileged action is recorded; the audit log is replicated offsite on a timer. |

---

## Tech stack

- **Backend:** Go, `net/http`, `pgx` v5 / PostgreSQL, embedded SQL migrations.
- **Frontend:** Vue 3, Vite, TypeScript, Tailwind, Pinia, Vue Router, vue-i18n,
  marked + DOMPurify, xterm.js.
- **Infrastructure:** nginx, systemd (socket-activated services and timers),
  BIRD (BGP), Krill + Routinator (RPKI), Prometheus + Grafana + Gatus,
  an external edge/CDN, WireGuard (backbone).

---

## Build & run

Prerequisites: Go ≥ 1.22 and Node ≥ 18 (for the SPA build).

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

PostgreSQL is optional for a local run; without `NCN_DATABASE_URL` the backend
uses its JSON file fallback.

---

## Deployment

Deployments are gated and run from a workstation against the target hosts over
SSH. See [`DEPLOYMENT.md`](DEPLOYMENT.md) for the full guide.

```sh
# core-console
core-console/deploy/deploy.sh backend     # go vet + go test on the target, build, atomic swap, zero-downtime restart
core-console/deploy/deploy.sh frontend    # i18n lint + type-check + vite build, ship dist/

# full stack (webmail, then core-console)
./deploy-all.sh
```

- The backend deploy refuses to ship unless `go vet ./...` and `go test ./...`
  pass on the target; the previous binary is backed up and `deploy.sh rollback`
  is supported.
- `ncn-api` is socket-activated (`ncn-api.socket` + `ncn-api.service`), making
  the binary swap zero-downtime.
- A daily state snapshot (local and offsite) and an offsite audit-log copy run on
  systemd timers (`deploy/ncn-state-backup.*`, `deploy/ncn-audit-rsync.*`).

---

## Configuration

- Runtime configuration and secrets reside under `/etc/ncn-core-console/` on the
  control node (for example `session.key`, `oauth.env`, the fleet SSH key); see
  `core-console/deploy/oauth.env.example`.
- nginx vhosts are defined in `core-console/deploy/nginx-ncn-core-console.conf`
  (console / public / wiki). Public endpoints are explicitly allow-listed; any
  other path under `/api/` returns 404 from public hosts.
- `ncn-lb` reads `/etc/ncn-lb/config.json`; webmail and the bot read their own
  env files on their respective hosts.

Secrets are never committed. Production values are provisioned out-of-band on the
hosts; `*.example` files document the expected shape.

---

## Security model

- **Three access tiers:** public (nginx allow-list), internal (any authenticated
  operator), and admin (role-gated). The session cookie is HMAC-signed and scoped
  to the admin host.
- **Host separation** is enforced at both nginx and the SPA router (defense in
  depth): public hosts do not expose admin APIs.
- **Single executor:** all fleet writes pass through `ncn-api`. The MCP server,
  the bot, and the CLIs are clients and cannot reach the fleet directly. Write
  and command tools require an admin operator and are audited.
- **Fleet access** uses a dedicated SSH key on the control node; remote command
  execution is human-approved through the agent flow.
- **Content safety:** wiki markdown is sanitized with DOMPurify before rendering;
  the public wiki API enforces `is_public` server-side so internal pages are not
  exposed.

---

## Documentation

- **Setup:** [`core-console/QUICKSTART.md`](core-console/QUICKSTART.md) and
  [`DEPLOYMENT.md`](DEPLOYMENT.md).
- **Operator and member documentation:** the self-hosted wiki (public at the
  wiki host, internal under the admin host); source markdown in
  `core-console/wiki/`.
- **Runbooks:** `core-console/docs/` (`PITR-RESTORE.md`, `POP-ONBOARDING.md`).
- **Per-component READMEs:** `webmail/README.md`, `core-console/agent/README.md`,
  `core-console/mcp/README.md`, `cli/ncn-debug/README.md`.
