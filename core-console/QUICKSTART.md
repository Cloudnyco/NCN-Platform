# Quickstart — run / deploy ncn-core-console

Four entry points, by use case. All are idempotent and (where they touch prod)
gated/verified.

## 1. Day-to-day deploy (prod, from your machine)

The console runs on **ctrl-01**; `deploy.sh` builds locally and ships over SSH
(`root@deploy-host`). Backend deploys are GATED on `go vet` + `go test`; the old
binary is backed up for instant rollback.

```bash
deploy/deploy.sh all        # backend + frontend + smoke   (default)
deploy/deploy.sh backend    # Go only — vet+test gate, atomic zero-downtime swap
deploy/deploy.sh frontend   # SPA only — i18n lint + typecheck + vite + ship
deploy/deploy.sh nginx      # ship nginx conf + reload
deploy/deploy.sh health     # ★ full-stack "is everything up?" (see below)
deploy/deploy.sh rollback   # restore the previous ncn-api binary
```

`deploy.sh health` checks, in one shot: public endpoints (from the ctrl-01 edge),
control-plane services (ncn-api / nginx / goflow2 / softflowd) + the flow
pipeline (distinct sFlow exporters seen), and the HA/RPKI bits on pop-03 (Postgres
streaming replica, Routinator). Exits non-zero if anything is red.

## 2. Fresh host / disaster recovery

Stand the whole thing up on a NEW box (or rebuild). Run as root from a checkout:

```bash
sudo deploy/bootstrap.sh
```

It installs deps (go/node/nginx), builds API + SPA, installs the systemd
units (`ncn-api.service`/`.socket`, socket-activated on 127.0.0.1:9000), wires
nginx, and starts. Migrations auto-apply on first start.

It does **not** invent secrets. For full features, supply in `/etc/ncn-core-console/`:
`oauth.env` (incl. `NCN_DATABASE_URL` for Postgres — omit to run file-backed),
`tg.env`, `fleet-key` (+ `fleet-known-hosts`), `turnstile.secret`, `agent-ca/`,
`agent-keys/`. For a real DR restore, drop the latest state snapshot into
`/etc/ncn-core-console` before starting (see `scripts/state-backup.sh`; Postgres
PITR under `scripts/pitr`). Then: `systemctl restart ncn-api && deploy/deploy.sh health`.

## 3. Local development

API file-backed (no Postgres, no fleet keys) + Vite HMR:

```bash
scripts/dev.sh         # API :9000 (file-backed) + Vite :5173 (proxies /api → :9000)
# open http://localhost:5173    (Ctrl-C stops both)
```

Needs Go ≥1.22 + Node ≥18. If `/var/log` + `/etc/ncn-core-console` aren't writable
by you, run `sudo scripts/dev.sh`. To develop the SPA against a *remote* API
instead, just `npm run dev` and point the vite proxy target at the remote.

## 4. Containers (portable demo / dev stack)

Self-contained stack (api + Postgres + nginx-served SPA), no host setup:

```bash
docker compose -f deploy/docker/docker-compose.yml up --build
# open http://localhost:8080
```

`web` (nginx) serves the SPA and proxies `/api` → `api` (Go) which talks to the
`db` (Postgres). No fleet keys/secrets are mounted, so node scraping + OAuth/TG
are off — this is for UI/API demo + dev, not production. Add an env file or mount
`/etc/ncn-core-console` into the `api` service to enable more.

---

**Related:** `deploy/sflow/` (flow-collector rollout), `scripts/state-backup.sh`
+ `scripts/pitr` (DR backups), `scripts/agent-node-provision.sh` (onboard a PoP).
