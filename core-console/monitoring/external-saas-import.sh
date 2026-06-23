#!/usr/bin/env bash
# External independent uptime layer — creates monitors in a SaaS via its API.
# This is the multi-vantage, network-independent tier that complements the
# in-mesh Gatus on pop-03 (which can't be the independent watcher of its own
# network). It watches the PUBLIC anycast services + each PoP's v4 unicast from
# the real internet (external vantages reach v4 that in-mesh pop-03 can't).
#
# Usage:
#   UPTIMEROBOT_API_KEY=u123... ./external-saas-import.sh uptimerobot
#   BETTERSTACK_API_TOKEN=xxxxx ./external-saas-import.sh betterstack
#
# Idempotency: re-running may create duplicates — these APIs don't upsert by
# name. Run once; manage further edits in the provider UI.
set -euo pipefail

# Public anycast HTTPS services (name|url)
HTTP_TARGETS=(
  "NCN site|https://example.com/"
  "NCN health|https://example.com/api/v1/health"
  "NCN status|https://example.com/status"
  "NCN looking-glass|https://example.com/api/v1/lg/sessions"
  "NCN admin|https://admin.example.com/login"
)
# Per-PoP v4 unicast (name|ip) — external vantage; v6 anchors are internal-mesh
# only so they belong to Gatus, not here.
PING_TARGETS=(
  "pop-03|198.51.100.3" "pop-04|198.51.100.4"
  "ctrl-01|198.51.100.1" "pop-01|198.51.100.2" "pop-02|198.51.100.5"
  "pop-08|198.51.100.6" "pop-06|198.51.100.8" "pop-05|198.51.100.7"
)

provider="${1:-}"

ur() {  # UptimeRobot v2. Free tier: interval min 300s, no IPv6, no multi-region.
  : "${UPTIMEROBOT_API_KEY:?set UPTIMEROBOT_API_KEY}"
  local api="https://api.uptimerobot.com/v2/newMonitor"
  for t in "${HTTP_TARGETS[@]}"; do
    curl -s "$api" -d "api_key=$UPTIMEROBOT_API_KEY" -d format=json \
      -d type=1 -d "friendly_name=${t%%|*}" -d "url=${t##*|}" -d interval=300 \
      | grep -o '"stat":"[a-z]*"' && echo "  http ${t%%|*}"
  done
  for t in "${PING_TARGETS[@]}"; do
    curl -s "$api" -d "api_key=$UPTIMEROBOT_API_KEY" -d format=json \
      -d type=3 -d "friendly_name=PoP ${t%%|*} v4" -d "url=${t##*|}" -d interval=300 \
      | grep -o '"stat":"[a-z]*"' && echo "  ping ${t%%|*}"
  done
}

bs() {  # Better Stack Uptime v2. Supports 60s, multi-region, IPv6, confirmation.
  : "${BETTERSTACK_API_TOKEN:?set BETTERSTACK_API_TOKEN}"
  local api="https://uptime.betterstack.com/api/v2/monitors"
  local hdr=(-H "Authorization: Bearer $BETTERSTACK_API_TOKEN" -H "Content-Type: application/json")
  for t in "${HTTP_TARGETS[@]}"; do
    curl -s "${hdr[@]}" "$api" -d "{\"monitor_type\":\"status\",\"url\":\"${t##*|}\",\"pronounceable_name\":\"${t%%|*}\",\"check_frequency\":60,\"confirmation_period\":180,\"regions\":[\"us\",\"eu\",\"as\"]}" \
      | grep -o '"id":"[0-9]*"' | head -1 && echo "  http ${t%%|*}"
  done
  for t in "${PING_TARGETS[@]}"; do
    curl -s "${hdr[@]}" "$api" -d "{\"monitor_type\":\"ping\",\"url\":\"${t##*|}\",\"pronounceable_name\":\"PoP ${t%%|*} v4\",\"check_frequency\":60,\"confirmation_period\":180,\"regions\":[\"us\",\"eu\",\"as\"]}" \
      | grep -o '"id":"[0-9]*"' | head -1 && echo "  ping ${t%%|*}"
  done
}

case "$provider" in
  uptimerobot) ur ;;
  betterstack) bs ;;
  *) echo "usage: $0 {uptimerobot|betterstack}  (set the provider's API key env var first)"; exit 2 ;;
esac
echo "done — set anti-flap in the UI: alert after >=2 locations fail / confirmation, resend throttled."
