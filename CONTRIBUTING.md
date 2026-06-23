# Contributing

Thanks for your interest! This is a self-hosted network-operations platform you
can run for **your own** network. Contributions — bug fixes, features, docs,
new integrations — are welcome.

## Project layout

| Path | What |
|---|---|
| `core-console/` | Operations console: Vue 3 SPA + Go API (`ncn-api`) + per-PoP `agent`, `lb` (failover), `mcp`. The bulk of the platform. |
| `webmail/` | Self-hosted webmail (Go + Vue) fronting Postfix/Dovecot. |
| `cli/` | `ncn-login` (SSH-signed login) and `ncn-debug` (read-only ops CLI). |
| `scripts/` | Backup/restore, PITR, per-PoP provisioning helpers. |
| `deploy-all.sh` | Top-level orchestrator (webmail → console). |

## Local development

```bash
# Console: API (file-backed, no Postgres/fleet needed) + Vite HMR
cd core-console && scripts/dev.sh        # → http://localhost:5173
# or the full containerized stack (api + Postgres + nginx-served SPA)
docker compose -f core-console/deploy/docker/docker-compose.yml up --build  # → :8080
```

See `core-console/QUICKSTART.md` for all four entry points (dev / deploy /
bootstrap / docker) and `DEPLOYMENT.md` for a from-scratch production install.

## Configuration

Everything operator-specific is configured via environment variables (prefix
`NCN_*`) and runtime files — see `.env.example`. The codebase ships with
**placeholder** values (`example.com`, `AS64500`, RFC5737/RFC3849 addresses,
`ctrl-01`/`pop-0N` node names). Replace them with your own via env / the node
registry; nothing in the repo points at a real network.

## Conventions

- **Backend** (Go): `cd <module> && go vet ./... && go test ./...` before a PR.
  Keep new operator-specific values behind `getenvDefault("NCN_...", "<placeholder>")`.
- **Frontend** (Vue): `npm run lint:i18n && npm run typecheck && npm run build`.
  All three locales (en / zh-CN / zh-TW) must stay key-aligned (`lint:i18n` enforces it).
- Commits: conventional-ish (`feat(console): …`, `fix(...)`, `docs(...)`).
- Anything that touches a production router/host must be confirm-gated + reversible.

## Reporting issues / security

Functional bugs → GitHub issues. **Security issues → see `SECURITY.md`** (do not
open a public issue for vulnerabilities).
