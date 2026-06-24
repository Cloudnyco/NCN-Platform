# Contributing

> **English** · [简体中文](CONTRIBUTING.zh-CN.md)

This repository contains a self-hosted network-operations platform intended to be
run against an operator's own network. Contributions are accepted, including bug
fixes, features, documentation, and new integrations.

## Project layout

| Path | Description |
|---|---|
| `core-console/` | Operations console: Vue 3 SPA, Go API (`ncn-api`), and per-PoP `agent`, `lb` (failover), and `mcp`. Comprises the majority of the platform. |
| `webmail/` | Self-hosted webmail (Go and Vue) fronting Postfix/Dovecot. |
| `cli/` | `ncn-login` (SSH-signed login) and `ncn-debug` (read-only operations CLI). |
| `scripts/` | Backup/restore, PITR, and per-PoP provisioning helpers. |
| `deploy-all.sh` | Top-level orchestrator (webmail, then console). |

## Local development

```bash
# Console: API (file-backed, no Postgres/fleet needed) + Vite HMR
cd core-console && scripts/dev.sh        # → http://localhost:5173
# or the full containerized stack (api + Postgres + nginx-served SPA)
docker compose -f core-console/deploy/docker/docker-compose.yml up --build  # → :8080
```

Refer to `core-console/QUICKSTART.md` for the four entry points (dev / deploy /
bootstrap / docker) and to `DEPLOYMENT.md` for a from-scratch production install.

## Configuration

All operator-specific values are configured via environment variables (prefix
`NCN_*`) and runtime files; see `.env.example`. The codebase ships with
placeholder values (`example.com`, `AS64500`, RFC5737/RFC3849 addresses,
`ctrl-01`/`pop-0N` node names). These must be replaced with operator-specific
values via environment variables or the node registry. No value in the repository
references a real network.

## Conventions

- Backend (Go): run `cd <module> && go vet ./... && go test ./...` before opening
  a PR. New operator-specific values must be placed behind
  `getenvDefault("NCN_...", "<placeholder>")`.
- Frontend (Vue): run `npm run lint:i18n && npm run typecheck && npm run build`.
  All three locales (en / zh-CN / zh-TW) must remain key-aligned; `lint:i18n`
  enforces this.
- Commits follow a conventional-style format (`feat(console): …`, `fix(...)`,
  `docs(...)`).
- Any change that affects a production router or host must be confirmation-gated
  and reversible.

## Reporting issues and security

Functional bugs should be reported via GitHub issues. Security issues are
described in `SECURITY.md`; do not open a public issue for vulnerabilities.
