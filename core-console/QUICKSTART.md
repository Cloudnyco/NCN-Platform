# Quickstart — run / deploy ncn-core-console

> **English** · [简体中文](QUICKSTART.zh-CN.md)

There are four entry points, organized by use case. All are idempotent and, where they touch production, gated and verified.

## 1. Routine deploy (production, from a workstation)

The console runs on **ctrl-01**. `deploy.sh` builds locally and ships over SSH (`root@deploy-host`). Backend deploys are gated on `go vet` and `go test`; the previous binary is retained for rollback.

```bash
deploy/deploy.sh all        # backend + frontend + smoke   (default)
deploy/deploy.sh backend    # Go only — vet+test gate, atomic zero-downtime swap
deploy/deploy.sh frontend   # SPA only — i18n lint + typecheck + vite + ship
deploy/deploy.sh nginx      # ship nginx conf + reload
deploy/deploy.sh health     # full-stack "is everything up?" (see below)
deploy/deploy.sh rollback   # restore the previous ncn-api binary
```

`deploy.sh health` checks, in one pass: public endpoints (from the ctrl-01 edge), control-plane services (ncn-api / nginx / goflow2 / softflowd) and the flow pipeline (distinct sFlow exporters seen), and the HA/RPKI components on pop-03 (Postgres streaming replica, Routinator). It exits non-zero if any check fails.

## 2. Fresh host / disaster recovery

The following stands up the entire stack on a new host (or rebuilds it). Run as root from a checkout:

```bash
sudo deploy/bootstrap.sh
```

It installs dependencies (go/node/nginx), builds the API and SPA, installs the systemd units (`ncn-api.service`/`.socket`, socket-activated on 127.0.0.1:9000), wires nginx, and starts the service. Migrations apply automatically on first start.

It does not generate secrets. For full functionality, supply the following in `/etc/ncn-core-console/`: `oauth.env` (including `NCN_DATABASE_URL` for Postgres — omit to run file-backed), `tg.env`, `fleet-key` (plus `fleet-known-hosts`), `turnstile.secret`, `agent-ca/`, and `agent-keys/`. For a disaster-recovery restore, place the latest state snapshot into `/etc/ncn-core-console` before starting (see `scripts/state-backup.sh`; Postgres PITR is under `scripts/pitr`). Then run: `systemctl restart ncn-api && deploy/deploy.sh health`.

## 3. Local development

API file-backed (no Postgres, no fleet keys) plus Vite HMR:

```bash
scripts/dev.sh         # API :9000 (file-backed) + Vite :5173 (proxies /api → :9000)
# open http://localhost:5173    (Ctrl-C stops both)
```

Requires Go ≥1.22 and Node ≥18. If `/var/log` and `/etc/ncn-core-console` are not writable by the current user, run `sudo scripts/dev.sh`. To develop the SPA against a remote API instead, run `npm run dev` and point the vite proxy target at the remote.

## 4. Containers (portable demo / dev stack)

Self-contained stack (api + Postgres + nginx-served SPA), no host setup:

```bash
docker compose -f deploy/docker/docker-compose.yml up --build
# open http://localhost:8080
```

`web` (nginx) serves the SPA and proxies `/api` → `api` (Go), which talks to `db` (Postgres). No fleet keys or secrets are mounted, so node scraping and OAuth/TG are disabled — this configuration is intended for UI/API demo and development, not production. Add an env file or mount `/etc/ncn-core-console` into the `api` service to enable more.

---

**Related:** `deploy/sflow/` (flow-collector rollout), `scripts/state-backup.sh` and `scripts/pitr` (DR backups), `scripts/agent-node-provision.sh` (onboard a PoP).
