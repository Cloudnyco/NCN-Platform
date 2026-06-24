# Deployment

A from-scratch guide to running NCN for your own network — from a one-command
demo to a production HA control plane. For the four entry points at a glance see
[`core-console/QUICKSTART.md`](core-console/QUICKSTART.md); this document is the
long form.

Everything operator-specific is a **placeholder** in this repo (`example.com`,
`AS64500`, RFC5737/RFC3849 addresses, `ctrl-01`/`pop-0N`). Nothing points at a
real network until you set the `NCN_*` env vars and populate the node registry.

---

## Contents

- [Architecture](#architecture)
- [Pick a topology](#pick-a-topology)
- [Prerequisites](#prerequisites)
- [Path A — single host (demo or small prod)](#path-a--single-host)
- [Configuration reference](#configuration-reference)
- [Database (PostgreSQL)](#database-postgresql)
- [nginx, TLS & host separation](#nginx-tls--host-separation)
- [Defining your fleet](#defining-your-fleet)
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
                     Cloudflare / your edge (TLS)
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
      pop-01     pop-02  pop-04  pop-0N …  (your PoPs, each runs BIRD)
```

- **ncn-api** is one Go binary on the control node. It is the *only* writer to
  the fleet; the bot, MCP server and CLIs are all clients.
- **State** lives in PostgreSQL when `NCN_DATABASE_URL` is set, otherwise in JSON
  files under `/etc/ncn-core-console` (so it runs with or without a database).
- **One SPA build** serves three hosts; the router + nginx enforce host
  separation (public hosts never expose `/admin` APIs).
- Everything that touches a production router is **confirm-gated, audited and
  reversible** by design.

---

## Pick a topology

| You want… | Use | Hosts | Postgres |
|---|---|---|---|
| To try the UI/API | `docker compose` or `scripts/dev.sh` | 0 (local) | bundled / none |
| A small real deployment | `bootstrap.sh` on one host | 1 | recommended |
| Production with failover | Path B (HA) | 2+ | required (replicated) |

You can start single-host and grow into HA later — the steps are additive.

---

## Prerequisites

- **Control host:** Linux (systemd), root access. 1 vCPU / 1 GB is enough to
  start.
- **Build tools** (only where you build): Go ≥ 1.22, Node ≥ 18. `bootstrap.sh`
  installs these for you; `deploy.sh` builds on your workstation.
- **DNS** for your public + admin (+ mail) hostnames pointing at the host/edge.
- **TLS:** a reverse proxy / CDN (Cloudflare etc.) or local certs (see below).
- For multi-host: SSH from the control host to each PoP, and a Postgres you can
  replicate.

---

## Path A — single host

Stand the whole console up on one fresh box. Run as root from a checkout:

```bash
git clone https://github.com/<you>/<repo>.git && cd <repo>
sudo core-console/deploy/bootstrap.sh
```

`bootstrap.sh` is idempotent and:

1. installs deps (go / node / nginx),
2. builds the API (`ncn-api`) and the SPA (`dist/`),
3. installs the systemd units — `ncn-api.socket` + `ncn-api.service`
   (socket-activated on `127.0.0.1:9000`),
4. wires nginx (`deploy/nginx-ncn-core-console.conf`),
5. starts it; **migrations auto-apply on first start**.

At this point the console is up and **file-backed** (no database, empty fleet) —
you can log in via the recovery bootstrap and click around. To make it *yours*,
do the next two sections (config + fleet), then `systemctl restart ncn-api`.

> `bootstrap.sh` never invents secrets. Anything not provided is simply disabled
> (gracefully) until you add it.

---

## Configuration reference

Runtime config lives in **`/etc/ncn-core-console/`** on the control node and is
loaded by `ncn-api.service` via `EnvironmentFile`. Copy the blocks you need from
[`.env.example`](.env.example) and [`core-console/deploy/oauth.env.example`](core-console/deploy/oauth.env.example).

Apply changes with `systemctl restart ncn-api`.

### Core env vars

| Var | Default | What |
|---|---|---|
| `NCN_ASN` | `64500` | Your AS number (digits only). |
| `NCN_OUR_PREFIXES` | `2001:db8::/32` | Your address space (comma-separated CIDRs). |
| `NCN_LOCAL_NODE_ID` | `ctrl-01` | Which node id *this* (control) host is. |
| `NCN_RPKI_ROV_NODE` | `ctrl-01` | Node whose BIRD runs RPKI ROV. |
| `NCN_PUBLIC_HOST` / `NCN_ADMIN_HOST` / `NCN_DOMAIN` | `example.com` family | Your public / admin / mail-domain names. |
| `NCN_BRAND_NAME` | `Acme Net` | User-facing brand string. |
| `NCN_OAUTH_REDIRECT_BASE` | `https://admin.example.com` | Base for OAuth redirect URIs. |
| `NCN_DATABASE_URL` | *(unset → file-backed)* | `postgres://…` to use PostgreSQL. |
| `NCN_DEPLOY_HOST` | `deploy-host` | SSH target `deploy.sh` ships to. |
| `NCN_PROBE_TARGETS` | *(none)* | Extra ICMP probe targets. |
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
| `agent-ca/`, `agent-keys/` | mTLS CA + keys for `ncn-agent` (if you use REST telemetry) |

`.gitignore` already excludes `*.env`, `*.key`, `*.pem`, `*.age`. **Treat any
exposed key as compromised and rotate it** — see [`SECURITY.md`](SECURITY.md).

---

## Database (PostgreSQL)

Optional — without `NCN_DATABASE_URL` the API is file-backed (fine for demo /
small single-host). For anything you care about, use Postgres:

```bash
sudo -u postgres createuser ncn --pwprompt
sudo -u postgres createdb -O ncn ncn
# then in /etc/ncn-core-console/oauth.env:
#   NCN_DATABASE_URL=postgres://ncn:YOURPASS@localhost:5432/ncn?sslmode=disable
sudo systemctl restart ncn-api      # embedded migrations auto-apply on start
```

Migrations are embedded in the binary (`//go:embed migrations/*.sql`) and run on
startup — no separate migrate step. Stores are nil-DB tolerant, so switching
file↔DB is safe.

---

## nginx, TLS & host separation

`deploy/nginx-ncn-core-console.conf` serves all three vhosts (public / admin /
wiki) from the one SPA `dist/`, proxying `/api` → `127.0.0.1:9000`. Public hosts
**allow-list** only the public API paths; everything else under `/api/` returns
404 from public hosts (defense in depth, also enforced in the SPA router).

- **Behind Cloudflare / a CDN:** point the hostnames at it (proxied, SSL Strict)
  and let it terminate TLS; the snippet `deploy/snippets/cloudflare-real-ip.conf`
  restores real client IPs.
- **Direct TLS:** drop your certs in and set `ssl_certificate*` in the conf; use
  `deploy/nginx-ncn-acme-bootstrap.conf` for an HTTP-01 ACME bootstrap.

Edit `server_name`s to your hostnames, then `deploy.sh nginx` (or copy + `nginx -t
&& systemctl reload nginx`).

---

## Defining your fleet

Nodes live in a **runtime registry** (no code changes). Manage them in the admin
console (**Onboarding** / **Servers**) or seed `nodes.json` under
`/etc/ncn-core-console/`. Each node has: `id` (e.g. `pop-01`), label, region, and
probe anchors (v4/v6). The control node scrapes each node for load, BGP sessions
(`birdc`), WireGuard and uptime — over SSH by default, or over HTTPS+HMAC if you
run `ncn-agent` there.

---

## Adding a PoP

On the control node, provision a node end-to-end:

```bash
core-console/scripts/agent-node-provision.sh <node-id> <node-address>
```

This pushes the fleet SSH key (or, for REST telemetry, the agent CA bundle via
`scripts/agent-ca-bootstrap.sh`), registers the node, and verifies a scrape. Each
PoP is expected to run **BIRD** (BGP / anycast); the console reads it, it does not
install it.

---

## Day-2 operations

From your workstation (builds locally, ships over SSH to `NCN_DEPLOY_HOST`):

```bash
deploy/deploy.sh all        # backend + frontend + smoke (default)
deploy/deploy.sh backend    # Go: GATED on `go vet` + `go test`, atomic zero-downtime swap
deploy/deploy.sh frontend   # SPA: i18n lint + typecheck + vite build + ship
deploy/deploy.sh nginx      # ship + reload nginx
deploy/deploy.sh health     # full-stack "is everything up?" (non-zero if red)
deploy/deploy.sh rollback   # restore the previous ncn-api binary
```

- Backend deploys **refuse to ship** unless vet + tests pass on the target; the
  previous binary is backed up for instant `rollback`.
- `ncn-api` is socket-activated, so the swap is **zero-downtime**.
- Daily state DR snapshots and audit-log offsite rsync run on systemd timers
  (`deploy/ncn-state-backup.*`, `deploy/ncn-audit-rsync.*`).

---

## Path B — HA control plane & DR

Add a second host (`pop-04` here) so a control-node outage is survivable.

1. **PostgreSQL streaming replication** — primary on `ctrl-01`, hot standby on
   `pop-04` (sub-second RPO). Standard `primary_conninfo` + replication slot.
2. **Warm-standby `ncn-api`** on `pop-04` (same build, pointed at the local
   replica), kept stopped until promotion.
3. **`ncn-lb` failover controller** (`lb/`, `deploy/ncn-lb.service`) on `pop-04`:
   health-checks the primary and, on sustained failure, promotes the PG replica,
   starts the standby API, and repoints DNS. Configure `lb/config.json` and
   `lb/cf.env` (Cloudflare token + record ids — see `lb/cf.env.example`).
   - **Ships in `observe` mode** (logs what it *would* do). Arm only after you've
     tested `lb/failover.sh`. **No automatic fail-back** (anti-flap) — failing
     back is a manual, deliberate step.
4. **PITR** — WAL archiving + weekly base-backups + a restore drill, under
   `scripts/pitr/` (`ncn-wal-archive`, `ncn-pitr-basebackup`, `ncn-pitr-drill.sh`).
   This guards against logical corruption that replication would just propagate.
5. **State snapshots** — `scripts/state-backup.sh` (local + offsite); restore by
   dropping the snapshot into `/etc/ncn-core-console` before starting.

> Single-watcher caveat: `ncn-lb` is one controller — it removes the control-node
> SPOF but is not itself HA. Keep its scope to "promote on clear failure".

---

## Webmail (optional)

`webmail/` is a self-contained mail UI (Go + Vue) fronting Postfix/Dovecot, served
at `mail.example.com`. Deploy with `webmail/deploy/deploy.sh` (or the top-level
`deploy-all.sh`, which does webmail then console). It bridges to the console for
SSO and role-mailbox recovery. You supply the actual MTA/IMAP (Postfix/Dovecot/
rspamd); webmail is the front end.

---

## Observability (optional)

`deploy/monitoring/` ships Prometheus + Grafana + Gatus units (intended for an
observability node such as `pop-03`). The API exposes a hand-written `/metrics`
(no secrets; restrict it to your backbone or gate with `NCN_METRICS_TOKEN`).
Grafana is embedded in the console via an admin-gated reverse proxy. `deploy/sflow/`
rolls out an sFlow/NetFlow collector (goflow2 + softflowd) feeding `NCN_FLOW_FILE`.

---

## CLIs

Build + install the operator CLIs for your team:

```bash
cd cli && ./install.sh            # builds (or uses prebuilt) + installs to /usr/local/bin
#         ./install.sh --build    # force a fresh compile
#  PREFIX=$HOME/.local/bin ./install.sh   # no-sudo
```

- **`ncn-login`** — authenticate: bare run opens a browser login that hands a
  token back to the terminal; `--token` to paste one; `--user` for SSH-key signed
  login. Set `NCN_HOST` to your admin host.
- **`ncn-debug`** — read-only ops over the API: `fleet`, `node`, `bgp`, `alerts`,
  `rpki`, `oncall`, … or an interactive `console`. `ncn-debug status` is public
  (no token).

---

## Security checklist

- [ ] All secrets in `/etc/<service>/` env/key files, mode 0600 — **never** in git.
- [ ] `session.key`, `fleet-key`, `turnstile.secret`, OAuth secrets,
      `NCN_DATABASE_URL` password all set and unique to you.
- [ ] TLS terminated (CDN or local certs); `admin` host not publicly enumerable.
- [ ] `/metrics` restricted to your backbone or `NCN_METRICS_TOKEN`-gated.
- [ ] Fleet SSH key is dedicated (not your personal key); remote command
      execution stays human-approved through the agent flow.
- [ ] Reviewed the sensitive-by-design modules listed in `SECURITY.md`.
- [ ] If any key was ever exposed (e.g. committed), **rotate it**.

---

## Troubleshooting

| Symptom | Check |
|---|---|
| API won't start | `journalctl -u ncn-api -e`; bad `NCN_DATABASE_URL` → unset to fall back to files. |
| 404 on `/api/...` from public host | Expected — admin APIs are host-separated. Use the admin host. |
| Blank admin page | Hard-refresh (stale hashed bundle); confirm `dist/` shipped; check the browser console. |
| Node shows down | SSH/agent reachability from the control node; `fleet-key` loaded; `birdc` present on the PoP. |
| OAuth button errors | That provider's client id/secret unset, or callback URL mismatch (must match `NCN_OAUTH_REDIRECT_BASE` + path exactly). |
| `deploy.sh backend` aborts | vet/test failed on the target — that's the gate doing its job; fix and retry. |

Still stuck? Open an issue (functional) or see `SECURITY.md` (vulnerabilities).
