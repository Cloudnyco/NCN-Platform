#!/usr/bin/env bash
# dev.sh — local quick-start for UI / frontend dev.
#
# Runs the Go API file-backed (NO Postgres, NO fleet keys needed — globalDB stays
# nil and the node list is empty) on 127.0.0.1:9000, plus Vite (HMR) on :5173
# which proxies /api → :9000 (see vite.config.ts). Ctrl-C stops both.
#
# Notes:
#   - Needs Go (>=1.22) + Node (>=18) on this machine.
#   - The API writes state under /var/log/ncn-incidents and reads /etc/ncn-core-console/*
#     (all optional). If those aren't writable by you, run: sudo scripts/dev.sh
#   - To develop against a remote API instead, skip this and just `npm run dev`
#     after pointing vite.config.ts proxy target at the remote.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v go   >/dev/null || { echo "need Go on PATH"; exit 1; }
command -v node >/dev/null || { echo "need Node on PATH"; exit 1; }

mkdir -p /var/log/ncn-incidents /etc/ncn-core-console 2>/dev/null || true

echo "── starting API (file-backed) on :9000 ──"
( cd backend && go run . -addr=127.0.0.1:9000 ) &
API=$!
trap 'kill "$API" 2>/dev/null || true' EXIT INT TERM

# wait for the API to answer before launching vite
for _ in $(seq 1 30); do
  curl -fs -o /dev/null --max-time 1 http://127.0.0.1:9000/api/v1/health && break || sleep 0.5
done

echo "── starting Vite (HMR) on :5173 → proxy /api → :9000 ──"
npm run dev
