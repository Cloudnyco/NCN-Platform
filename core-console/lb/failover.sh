#!/usr/bin/env bash
# failover.sh — invoked by ncn-lb (armed mode) on the standby (pop-03, where
# the streaming replica lives) when the primary (ctrl-01) is detected down.
# Promotes this node's PG replica, starts the local ncn-api, and repoints the
# Cloudflare DNS origin to this node. ncn-lb passes the actual from/to origin
# names from its config; the defaults below are only a manual-run fallback.
#
# Args: $1 = failed primary name, $2 = target (this node).
# DESTRUCTIVE + outward-facing. ncn-lb only runs it in mode="armed".
# Idempotent-ish + fail-fast: aborts if the local replica isn't promotable.
#
# Split-brain guard: best-effort FENCE of the old primary first (stop its
# ncn-api + PG over SSH). If the old primary is reachable AND we can stop it,
# good; if unreachable (truly dead), proceed. The Cloudflare DNS flip is the
# authoritative "who serves clients", so even a missed fence won't split client
# traffic — only the DB must be single-writer, which the fence + promote ensure.
set -uo pipefail

FROM="${1:-ctrl-01}"
TO="${2:-pop-03}"
KEY=/etc/ncn-core-console/fleet-key
SSHO="-o StrictHostKeyChecking=no -o ConnectTimeout=8 -n"
CF_ENV=/etc/ncn-lb/cf.env       # CF_API_TOKEN, CF_ZONE_ID, CF_RECORDS[, PRIMARY_PUB]
# Source the env file up front so it can also override PRIMARY_PUB (the old
# primary's address used for fencing) instead of it being hardcoded here.
# shellcheck disable=SC1090
[ -f "$CF_ENV" ] && . "$CF_ENV"
PRIMARY_PUB="${PRIMARY_PUB:-198.51.100.1}"   # ctrl-01 public (for fencing over SSH)
log(){ echo "[failover $(date -u +%H:%M:%SZ)] $*"; }

log "BEGIN failover $FROM → $TO"

# 1) Fence the old primary (best-effort).
if ssh -i "$KEY" $SSHO "root@$PRIMARY_PUB" "systemctl stop ncn-api 2>/dev/null; systemctl stop postgresql@17-main 2>/dev/null; echo fenced" 2>/dev/null | grep -q fenced; then
  log "fenced old primary $FROM (ncn-api + postgres stopped)"
else
  log "could not reach old primary $FROM to fence (assuming truly down) — continuing"
fi

# 2) Verify the local replica is in recovery (promotable) before promoting.
inrec=$(sudo -u postgres psql -tAc "SELECT pg_is_in_recovery()" 2>/dev/null | tr -d '[:space:]')
if [ "$inrec" != "t" ]; then
  log "ABORT: local PG is not a standby in recovery (pg_is_in_recovery=$inrec) — refusing to promote"
  exit 1
fi

# 3) Promote the replica → becomes a writable primary.
if sudo -u postgres pg_ctlcluster 17 main promote 2>/dev/null; then
  log "promote issued"
else
  log "pg_ctlcluster promote returned non-zero (may already be promoting)"
fi
for i in $(seq 1 20); do
  inrec=$(sudo -u postgres psql -tAc "SELECT pg_is_in_recovery()" 2>/dev/null | tr -d '[:space:]')
  [ "$inrec" = "f" ] && { log "PG promoted to primary (writable)"; break; }
  sleep 1
done
[ "$inrec" = "f" ] || { log "ABORT: PG did not finish promotion"; exit 1; }

# 4) Start the local (warm-standby) ncn-api.
if [ -f /etc/systemd/system/ncn-api.service ] || [ -f /lib/systemd/system/ncn-api.service ]; then
  systemctl start ncn-api.socket 2>/dev/null
  if systemctl start ncn-api.service 2>/dev/null; then
    log "started ncn-api on $TO"
  else
    log "WARN: ncn-api start failed (check unit/config)"
  fi
else
  log "WARN: ncn-api unit not installed here — warm-standby app step skipped (Step 2 pending)"
fi

# 4b) Free :443 (held by x-ui/xray on pop-04) and start the console nginx so CF
#     can reach this origin. x-ui is sacrificed on failover — the control-plane
#     console takes priority over pop-04's proxy role during an outage.
if systemctl is-active --quiet x-ui.service 2>/dev/null; then
  systemctl stop x-ui.service 2>/dev/null && log "stopped x-ui.service (freed :443 for nginx)"
fi
if systemctl start nginx 2>/dev/null; then
  log "started console nginx on $TO"
else
  log "WARN: nginx start failed on $TO (check :443 conflict / config)"
fi

# 5) Repoint the Cloudflare DNS origin(s) to this node (the client-traffic
#    switch). CF_RECORDS is space-separated "recordid=newcontent" pairs (one per
#    A/AAAA record for the proxied hostname).
if [ -f "$CF_ENV" ]; then
  # already sourced at the top
  if [ -n "${CF_API_TOKEN:-}" ] && [ -n "${CF_ZONE_ID:-}" ] && [ -n "${CF_RECORDS:-}" ]; then
    for pair in $CF_RECORDS; do
      rid="${pair%%=*}"; content="${pair#*=}"
      resp=$(curl -s -X PATCH \
        "https://api.cloudflare.com/client/v4/zones/$CF_ZONE_ID/dns_records/$rid" \
        -H "Authorization: Bearer $CF_API_TOKEN" -H "Content-Type: application/json" \
        --data "{\"content\":\"$content\"}")
      if echo "$resp" | grep -q '"success":true'; then
        log "Cloudflare DNS record $rid repointed → $content"
      else
        log "WARN: Cloudflare DNS update failed for $rid: $resp"
      fi
    done
  else
    log "WARN: $CF_ENV present but incomplete (need CF_API_TOKEN/CF_ZONE_ID/CF_RECORDS) — CF DNS steer skipped"
  fi
else
  log "WARN: $CF_ENV absent (no Cloudflare API token) — CF DNS steer SKIPPED (do it manually)"
fi

# 6) Post-failover health check — confirm this origin actually serves before
#    declaring success, so a half-failover (DB promoted but app/nginx not up)
#    is loud in the log rather than silently leaving clients on a dead origin.
sleep 2
code=$(curl -s -k -o /dev/null -w '%{http_code}' --max-time 8 -H 'Host: admin.example.com' https://127.0.0.1/api/v1/health 2>/dev/null)
if [ "$code" = "200" ]; then
  log "post-failover health check OK (local origin returned 200)"
else
  log "WARN: post-failover health check FAILED (local origin returned ${code:-no-response}) — investigate immediately"
fi

log "DONE failover $FROM → $TO"
