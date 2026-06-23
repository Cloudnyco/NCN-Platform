#!/usr/bin/env bash
# bootstrap.sh — first-time bring-up of ncn-core-console on a FRESH host (new box
# or DR rebuild). Idempotent: safe to re-run. Run AS ROOT, from a checkout of
# this repo. Installs deps, builds the API + SPA, installs systemd + nginx, starts.
#
# It does NOT invent secrets. The API tolerates their absence (runs file-backed,
# reduced features); supply these out-of-band for full function:
#   /etc/ncn-core-console/oauth.env   — NCN_DATABASE_URL (Postgres; omit→file-backed),
#                                       NCN_DEEPSEEK_API_KEY, OAuth client creds, …
#   /etc/ncn-core-console/tg.env      — NCN_TG_BOT_TOKEN / NCN_TG_CHAT_ID (+ error chan)
#   /etc/ncn-core-console/fleet-key   — SSH key to the PoP fleet (+ fleet-known-hosts)
#   /etc/ncn-core-console/turnstile.secret, agent-ca/, agent-keys/ — as needed
# For a full DR restore, drop the latest state snapshot into /etc/ncn-core-console
# BEFORE starting (see scripts/state-backup.sh / scripts/pitr for Postgres).
#
# Migrations auto-apply on ncn-api start (backend/migrations/*.sql, embedded).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX=/opt/ncn-core-console
ETC=/etc/ncn-core-console
say() { printf '\n\033[1;36m── %s ──\033[0m\n' "$*"; }

[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }

say "dependencies"
if command -v apt-get >/dev/null; then
  apt-get update -qq || true
  apt-get install -y -qq curl rsync nginx git ca-certificates >/dev/null || true
  command -v go   >/dev/null || apt-get install -y -qq golang   >/dev/null || true
  command -v node >/dev/null || apt-get install -y -qq nodejs npm >/dev/null || true
fi
command -v go   >/dev/null || { echo "FATAL: need Go (>=1.22) on PATH"; exit 1; }
command -v node >/dev/null || { echo "FATAL: need Node (>=18) on PATH"; exit 1; }

say "layout"
install -d -m700 "$ETC"
install -d -m755 "$PREFIX" "$PREFIX/backend" "$PREFIX/dist" "$PREFIX/scripts" "$PREFIX/backups"
install -d -m755 /var/log/ncn-incidents

say "build SPA (i18n lint + typecheck + vite)"
( cd "$ROOT" && npm ci && npm run build )
rsync -a "$ROOT/dist/" "$PREFIX/dist/"

say "build API"
rsync -a --exclude ncn-api --exclude '*.exe' "$ROOT/backend/" "$PREFIX/backend/"
rsync -a "$ROOT/scripts/" "$PREFIX/scripts/"
( cd "$PREFIX/backend" && go mod tidy && go build -o ncn-api . && test -x ncn-api )

say "systemd"
install -m644 "$ROOT/deploy/ncn-api.service" "$ROOT/deploy/ncn-api.socket" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now ncn-api.socket
systemctl restart ncn-api.service

say "nginx (TLS/ACME must be provisioned separately on a fresh host)"
if [ -d /etc/nginx/sites-available ]; then
  install -m644 "$ROOT/deploy/nginx-ncn-core-console.conf" /etc/nginx/sites-available/ncn-core-console
  ln -sf /etc/nginx/sites-available/ncn-core-console /etc/nginx/sites-enabled/ncn-core-console
  if nginx -t 2>/dev/null; then systemctl reload nginx; else
    echo "  nginx -t failed (likely missing TLS certs) — provision certs then: systemctl reload nginx"
  fi
fi

say "verify"
for _ in 1 2 3 4 5; do
  curl -fs -o /dev/null --max-time 2 http://127.0.0.1:9000/api/v1/health && break || sleep 1
done
curl -s -o /dev/null -w "  ncn-api /health → %{http_code}\n" http://127.0.0.1:9000/api/v1/health || true
echo
echo "bootstrap done. Supply secrets in $ETC (oauth.env / tg.env / fleet-key) and"
echo "restart ncn-api for full features:  systemctl restart ncn-api"
echo "Then check everything:  deploy/deploy.sh health"
