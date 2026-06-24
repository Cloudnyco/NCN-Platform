# Deployment

> **English** · [简体中文](DEPLOYMENT.zh-CN.md)

A from-scratch guide to running NCN for a network — from a single-command demo to
a production high-availability (HA) control plane. The four entry points are
summarized in [`core-console/QUICKSTART.md`](core-console/QUICKSTART.md); this
document is the long-form reference.

Every operator-specific value in this repository is a **placeholder**
(`example.com`, `AS64500`, RFC5737/RFC3849 addresses, `ctrl-01`/`pop-0N`). Nothing
points at a real network until the `NCN_*` environment variables are set and the
node registry is populated.

---

## Contents

- [Architecture](#architecture)
- [Pick a topology](#pick-a-topology)
- [Prerequisites](#prerequisites)
- [Path A — single host (demo or small prod)](#path-a--single-host)
- [Configuration reference](#configuration-reference)
- [Database (PostgreSQL)](#database-postgresql)
- [nginx, TLS & host separation](#nginx-tls--host-separation)
- [Defining the fleet](#defining-the-fleet)
- [Adding a PoP (agent)](#adding-a-pop)
- [Day-2 operations](#day-2-operations)
- [Path B — HA control plane & DR](#path-b--ha-control-plane--dr)
- [Webmail (optional)](#webmail-optional)
- [Observability (optional)](#observability-optional)
- [CLIs](#clis)
- [Security checklist](#security-checklist)
- [Troubleshooting](#troubleshooting)

---

## Architecture

```
                     Cloudflare / edge (TLS)
                                │
       example.com   admin.example.com   mail.example.com
            │              │                   │
            ▼              ▼                   ▼
   ┌──────────────── ctrl-01 (control node) ────────────┐   ┌── pop-03 ──┐
   │  nginx → ncn-api (:9000, Go, socket-activated)      │   │  webmail   │
   │  PostgreSQL (primary, optional)                     │   │  observ.   │
   │  Vue SPA: admin console + public site + wiki        │   │  PG standby│
   └──────────────────────┬──────────────────────────────┘  └────────────┘
                          │ telemetry (SSH or HTTPS+HMAC)
         ┌──────────┬─────┼─────┬───────────┐
         ▼          ▼     ▼     ▼           ▼
      pop-01     pop-02  pop-04  pop-0N …  (PoPs, each runs BIRD)
```

- **ncn-api** is a single Go binary on the control node. It is the *only* writer to
  the fleet; the bot, MCP server, and CLIs are all clients.
- **State** is held in PostgreSQL when `NCN_DATABASE_URL` is set, otherwise in JSON
  files under `/etc/ncn-core-console` (so it runs with or without a database).
- **A single SPA build** serves three hosts; the router and nginx enforce host
  separation (public hosts do not expose `/admin` APIs).
- Operations that touch a production router are confirm-gated, audited, and
  reversible by design.

---

## Pick a topology

| Goal | Use | Hosts | Postgres |
|---|---|---|---|
| Evaluate the UI/API | `docker compose` or `scripts/dev.sh` | 0 (local) | bundled / none |
| A single-host deployment | `bootstrap.sh` on one host | 1 | recommended |
| Production with failover | Path B (HA) | 2+ | required (replicated) |

A deployment can start single-host and grow into HA later; the steps are additive.

---

## Prerequisites

- **Control host:** Linux (systemd), root access. 1 vCPU / 1 GB is sufficient to
  start.
- **Build tools** (only where a build occurs): Go ≥ 1.22, Node ≥ 18. `bootstrap.sh`
  installs these; `deploy.sh` builds on a workstation.
- **DNS** for the public, admin, and (optionally) mail hostnames, pointing at the
  host or edge.
- **TLS:** a reverse proxy / CDN (Cloudflare or equivalent) or local certificates
  (see below).
- For multi-host: SSH from the control host to each PoP, and a Postgres instance
  that can be replicated.

---

## Path A — single host

This brings the entire console up on one fresh host. Run as root from a checkout:

```bash
git clone https://github.com/<you>/<repo>.git && cd <repo>
sudo core-console/deploy/bootstrap.sh
```

`bootstrap.sh` is idempotent and:

1. installs dependencies (go / node / nginx),
2. builds the API (`ncn-api`) and the SPA (`dist/`),
3. installs the systemd units — `ncn-api.socket` + `ncn-api.service`
   (socket-activated on `127.0.0.1:9000`),
4. wires nginx (`deploy/nginx-ncn-core-console.conf`),
5. starts the service; **migrations auto-apply on first start**.

At this point the console is running and **file-backed** (no database, empty
fleet); login is possible via the recovery bootstrap. To configure it for a
specific network, complete the next two sections (config + fleet), then run
`systemctl restart ncn-api`.

> `bootstrap.sh` does not generate secrets. Anything not provided is disabled
> gracefully until it is added.

---

## Configuration reference

Runtime config is held in **`/etc/ncn-core-console/`** on the control node and is
loaded by `ncn-api.service` via `EnvironmentFile`. Copy the required blocks from
[`.env.example`](.env.example) and [`core-console/deploy/oauth.env.example`](core-console/deploy/oauth.env.example).

Apply changes with `systemctl restart ncn-api`.

### Core env vars

| Var | Default | Description |
|---|---|---|
| `NCN_ASN` | `64500` | AS number (digits only). |
| `NCN_OUR_PREFIXES` | `2001:db8::/32` | Address space (comma-separated CIDRs). |
| `NCN_LOCAL_NODE_ID` | `ctrl-01` | The node id of *this* (control) host. |
| `NCN_RPKI_ROV_NODE` | `ctrl-01` | Node whose BIRD runs RPKI ROV. |
| `NCN_PUBLIC_HOST` / `NCN_ADMIN_HOST` / `NCN_DOMAIN` | `example.com` family | Public / admin / mail-domain names. |
| `NCN_BRAND_NAME` | `Acme Net` | User-facing brand string. |
| `NCN_OAUTH_REDIRECT_BASE` | `https://admin.example.com` | Base for OAuth redirect URIs. |
| `NCN_DATABASE_URL` | *(unset → file-backed)* | `postgres://…` to use PostgreSQL. |
| `NCN_DEPLOY_HOST` | `deploy-host` | SSH target that `deploy.sh` ships to. |
| `NCN_PROBE_TARGETS` | *(none)* | Additional ICMP probe targets. |
| `NCN_RPKI_REFRESH` | `24h` | RPKI ROA poll interval. |
| `NCN_FLOW_FILE` | `/var/log/ncn-flows/flows.jsonl` | sFlow/NetFlow collector output. |
| `NCN_METRICS_TOKEN` | *(none)* | Bearer for `/metrics` (Grafana scrape). |
| `NCN_ALERT_WEBHOOK` | *(none)* | Generic alert webhook. |

### Optional feature env (all gracefully disabled if unset)

- **Telegram bot:** `NCN_TG_BOT_TOKEN`, `NCN_TG_BOT_USERNAME`, `NCN_TG_CHAT_ID`,
  `NCN_TG_ERROR_CHANNEL` (in `tg.env`).
- **OAuth login:** GitHub / Telegram client id + secret (in `oauth.env`). Register
  each app's callback as `https://admin.example.com/api/v1/auth/oauth/<provider>/callback`.
- **AI assistant:** `NCN_DEEPSEEK_API_KEY` (+ `NCN_DEEPSEEK_MODEL`).

### Secret / key files (mode 0600, root-owned, **never commit**)

Under `/etc/ncn-core-console/`:

| File | Purpose |
|---|---|
| `oauth.env`, `tg.env` | the env files above |
| `session.key` | HMAC session-cookie key (auto-generated if absent) |
| `fleet-key` (+ `fleet-known-hosts`) | SSH key the control node uses to reach PoPs |
| `turnstile.secret` | Cloudflare Turnstile secret (public-form bot wall) |
| `agent-ca/`, `agent-keys/` | mTLS CA + keys for `ncn-agent` (for REST telemetry) |

`.gitignore` already excludes `*.env`, `*.key`, `*.pem`, `*.age`. Treat any exposed
key as compromised and rotate it — see [`SECURITY.md`](SECURITY.md).

---

## Database (PostgreSQL)

Optional — without `NCN_DATABASE_URL` the API is file-backed, which is suitable for
a demo or a single-host evaluation. For production data, use Postgres:

```bash
sudo -u postgres createuser ncn --pwprompt
sudo -u postgres createdb -O ncn ncn
# then in /etc/ncn-core-console/oauth.env:
#   NCN_DATABASE_URL=postgres://ncn:YOURPASS@localhost:5432/ncn?sslmode=disable
sudo systemctl restart ncn-api      # embedded migrations auto-apply on start
```

Migrations are embedded in the binary (`//go:embed migrations/*.sql`) and run on
startup; no separate migrate step is required. Stores are nil-DB tolerant, so
switching between file and DB backends is safe.

---

## nginx, TLS & host separation

`deploy/nginx-ncn-core-console.conf` serves all three vhosts (public / admin /
wiki) from the single SPA `dist/`, proxying `/api` → `127.0.0.1:9000`. Public hosts
**allow-list** only the public API paths; everything else under `/api/` returns 404
from public hosts (defense in depth, also enforced in the SPA router).

- **Behind Cloudflare / a CDN:** point the hostnames at it (proxied, SSL Strict)
  and let it terminate TLS; the snippet `deploy/snippets/cloudflare-real-ip.conf`
  restores real client IPs.
- **Direct TLS:** install the certificates and set `ssl_certificate*` in the conf;
  use `deploy/nginx-ncn-acme-bootstrap.conf` for an HTTP-01 ACME bootstrap.

Edit the `server_name`s to match the hostnames, then run `deploy.sh nginx` (or copy
the file and run `nginx -t && systemctl reload nginx`).

---

## Defining the fleet

Nodes are held in a **runtime registry** (no code changes required). Manage them in
the admin console (**Onboarding** / **Servers**) or seed `nodes.json` under
`/etc/ncn-core-console/`. Each node has an `id` (e.g. `pop-01`), label, region, and
probe anchors (v4/v6). The control node scrapes each node for load, BGP sessions
(`birdc`), WireGuard, and uptime — over SSH by default, or over HTTPS+HMAC when
`ncn-agent` is running there.

---

## Adding a PoP

On the control node, provision a node end-to-end:

```bash
core-console/scripts/agent-node-provision.sh <node-id> <node-address>
```

This pushes the fleet SSH key (or, for REST telemetry, the agent CA bundle via
`scripts/agent-ca-bootstrap.sh`), registers the node, and verifies a scrape. Each
PoP is expected to run **BIRD** (BGP / anycast); the console reads it and does not
install it.

---

## Day-2 operations

Run from a workstation (builds locally, ships over SSH to `NCN_DEPLOY_HOST`):

```bash
deploy/deploy.sh all        # backend + frontend + smoke (default)
deploy/deploy.sh backend    # Go: GATED on `go vet` + `go test`, atomic zero-downtime swap
deploy/deploy.sh frontend   # SPA: i18n lint + typecheck + vite build + ship
deploy/deploy.sh nginx      # ship + reload nginx
deploy/deploy.sh health     # full-stack health check (non-zero if red)
deploy/deploy.sh rollback   # restore the previous ncn-api binary
```

- Backend deploys refuse to ship unless vet and tests pass on the target; the
  previous binary is backed up for an immediate `rollback`.
- `ncn-api` is socket-activated, so the swap is zero-downtime.
- Daily state DR snapshots and audit-log offsite rsync run on systemd timers
  (`deploy/ncn-state-backup.*`, `deploy/ncn-audit-rsync.*`).

---

## Path B — HA control plane & DR

Add a second host (`pop-04` in this example) so that a control-node outage is
survivable.

1. **PostgreSQL streaming replication** — primary on `ctrl-01`, hot standby on
   `pop-04` (sub-second RPO). Standard `primary_conninfo` + replication slot.
2. **Warm-standby `ncn-api`** on `pop-04` (same build, pointed at the local
   replica), kept stopped until promotion.
3. **`ncn-lb` failover controller** (`lb/`, `deploy/ncn-lb.service`) on `pop-04`:
   it health-checks the primary and, on sustained failure, promotes the PG replica,
   starts the standby API, and repoints DNS. Configure `lb/config.json` and
   `lb/cf.env` (Cloudflare token + record ids — see `lb/cf.env.example`).
   - **Ships in `observe` mode** (logs the action it *would* take). Arm it only
     after `lb/failover.sh` has been tested. There is **no automatic fail-back**
     (anti-flap) — failing back is a manual, deliberate step.
4. **PITR** — WAL archiving + weekly base-backups + a restore drill, under
   `scripts/pitr/` (`ncn-wal-archive`, `ncn-pitr-basebackup`, `ncn-pitr-drill.sh`).
   This guards against logical corruption that replication would otherwise
   propagate.
5. **State snapshots** — `scripts/state-backup.sh` (local + offsite); restore by
   placing the snapshot into `/etc/ncn-core-console` before starting.

> Single-watcher caveat: `ncn-lb` is one controller — it removes the control-node
> SPOF but is not itself HA. Keep its scope to "promote on clear failure".

---

## Webmail (optional)

`webmail/` is a self-contained mail UI (Go + Vue) fronting Postfix/Dovecot, served
at `mail.example.com`. Deploy with `webmail/deploy/deploy.sh` (or the top-level
`deploy-all.sh`, which deploys webmail then the console). It bridges to the console
for SSO and role-mailbox recovery. The MTA/IMAP layer (Postfix/Dovecot/rspamd) is
supplied separately; webmail is the front end.

---

## Observability (optional)

`deploy/monitoring/` ships Prometheus + Grafana + Gatus units (intended for an
observability node such as `pop-03`). The API exposes a hand-written `/metrics`
endpoint (no secrets; restrict it to the backbone or gate it with
`NCN_METRICS_TOKEN`). Grafana is embedded in the console via an admin-gated reverse
proxy. `deploy/sflow/` rolls out an sFlow/NetFlow collector (goflow2 + softflowd)
feeding `NCN_FLOW_FILE`.

---

## CLIs

Build and install the operator CLIs:

```bash
cd cli && ./install.sh            # builds (or uses prebuilt) + installs to /usr/local/bin
#         ./install.sh --build    # force a fresh compile
#  PREFIX=$HOME/.local/bin ./install.sh   # no-sudo
```

- **`ncn-login`** — authenticates: a bare run opens a browser login that hands a
  token back to the terminal; `--token` pastes one; `--user` performs an SSH-key
  signed login. Set `NCN_HOST` to the admin host.
- **`ncn-debug`** — read-only operations over the API: `fleet`, `node`, `bgp`,
  `alerts`, `rpki`, `oncall`, … or an interactive `console`. `ncn-debug status` is
  public (no token).

---

## Security checklist

- [ ] All secrets in `/etc/<service>/` env/key files, mode 0600 — **never** in git.
- [ ] `session.key`, `fleet-key`, `turnstile.secret`, OAuth secrets, and the
      `NCN_DATABASE_URL` password are all set and unique to the deployment.
- [ ] TLS terminated (CDN or local certs); the `admin` host is not publicly
      enumerable.
- [ ] `/metrics` restricted to the backbone or `NCN_METRICS_TOKEN`-gated.
- [ ] The fleet SSH key is dedicated (not a personal key); remote command
      execution remains human-approved through the agent flow.
- [ ] The sensitive-by-design modules listed in `SECURITY.md` have been reviewed.
- [ ] If any key was ever exposed (e.g. committed), it has been **rotated**.

---

## Troubleshooting

| Symptom | Check |
|---|---|
| API won't start | `journalctl -u ncn-api -e`; a bad `NCN_DATABASE_URL` → unset it to fall back to files. |
| 404 on `/api/...` from public host | Expected — admin APIs are host-separated. Use the admin host. |
| Blank admin page | Hard-refresh (stale hashed bundle); confirm `dist/` shipped; check the browser console. |
| Node shows down | SSH/agent reachability from the control node; `fleet-key` loaded; `birdc` present on the PoP. |
| OAuth button errors | That provider's client id/secret is unset, or the callback URL does not match (must match `NCN_OAUTH_REDIRECT_BASE` + path exactly). |
| `deploy.sh backend` aborts | vet/test failed on the target — the gate is working as intended; fix and retry. |

For functional problems, open an issue; for vulnerabilities, see `SECURITY.md`.
