#!/usr/bin/env bash
# deploy.sh — one-shot core-console (admin) deploy.
#
# WHY THIS EXISTS
#   Mirrors webmail's deploy.sh. Without it, we used to manually rsync the
#   built dist to deploy-host and forget to lint, forget to typecheck, or
#   forget to verify nginx reloaded with the new config. The 4 KB
#   client_max_body_size that broke passkey registration would have been
#   caught by `smoke` if `smoke` had existed.
#
# WHAT IT DOES
#   1. lint:i18n (catch vue-i18n footguns BEFORE shipping)
#   2. typecheck (vue-tsc --noEmit)
#   3. vite build
#   4. rsync backend → deploy-host (go mod tidy + go build there)
#   5. rsync dist → /opt/ncn-core-console/dist on tyo
#   6. systemctl restart ncn-api + nginx reload
#   7. smoke probe https://admin.example.com/api/v1/*
#
# Usage:
#   ./deploy.sh            # backend + frontend + smoke
#   ./deploy.sh backend    # backend only (GATED: go vet + go test must pass;
#                          #   current binary backed up before swap)
#   ./deploy.sh frontend   # frontend only (gated: i18n lint + vite build)
#   ./deploy.sh smoke      # smoke probe against live admin.example.com
#   ./deploy.sh nginx      # ship deploy/nginx-*.conf + reload (config-only)
#   ./deploy.sh rollback   # restore the previous ncn-api binary + restart
#
# Safe-change foundation (2026-06-21):
#   - backend deploy is GATED on `go vet ./...` + `go test ./...` (run on tyo
#     before the binary swap); failure aborts with the live binary untouched.
#   - the current binary is backed up to backups/ncn-api.<ts> (keep 5) so
#     `./deploy.sh rollback` reverts a bad deploy in seconds.
#   - ncn-state-backup.timer (daily 02:30 UTC) snapshots /etc/ncn-core-console
#     to /var/backups/ncn-state + offsite mirror on pop-04 (DR backstop until
#     the JSON stores move to Postgres). See scripts/state-backup.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-all}"

color() { printf "\033[%sm%s\033[0m" "$1" "$2"; }
hdr()   { printf "\n%s\n" "$(color "1;36" "── $* ──")"; }
ok()    { printf "  %s %s\n" "$(color 32 "✓")" "$*"; }
err()   { printf "  %s %s\n" "$(color 31 "✗")" "$*" >&2; }

deploy_backend() {
  hdr "backend: rsync source → tyo"
  rsync -av --delete \
    --exclude='ncn-api' --exclude='*.exe' \
    "$ROOT/backend/" \
    root@deploy-host:/opt/ncn-core-console/backend/ | tail -3
  ok "backend synced"

  # Operational scripts (agent-node-provision.sh etc.) live at
  # /opt/ncn-core-console/scripts and are invoked by ncn-api (e.g. the Servers
  # page "配置 agent" flow). They were NOT synced before — a stale provision
  # script on tyo caused onboarding to exit 2 on any node not in its old
  # hardcoded list. Sync them with the backend so script + binary never drift.
  hdr "backend: rsync scripts → tyo"
  rsync -av --chmod=F755 "$ROOT/scripts/" \
    root@deploy-host:/opt/ncn-core-console/scripts/ | tail -4
  ok "scripts synced"

  hdr "backend: go mod tidy + go build on tyo"
  # Ship the systemd units (service + socket) so the live copies match git.
  # The socket unit is what makes `restart` zero-downtime — systemd keeps the
  # :9000 listener bound across the binary swap (apiListener() inherits fd 3),
  # so nginx never sees a connection-refused → 502 during a deploy.
  rsync -av "$ROOT/deploy/ncn-api.service" "$ROOT/deploy/ncn-api.socket" \
    "$ROOT/deploy/ncn-state-backup.service" "$ROOT/deploy/ncn-state-backup.timer" \
    root@deploy-host:/etc/systemd/system/ | tail -2

  # GATE: vet + test BEFORE building/swapping the live binary. If either
  # fails, `set -e` aborts here → the running ncn-api is left untouched (no
  # restart), so a broken commit can't reach production. Build to ncn-api.new
  # then atomically swap, after backing up the current binary for `rollback`.
  ssh -o StrictHostKeyChecking=no root@deploy-host "set -e
    cd /opt/ncn-core-console/backend
    go mod tidy
    printf '\n── go vet ──\n';  go vet ./...
    printf '\n── go test ──\n'; go test ./...
    gofmt -l . | sed 's/^/  (unformatted, informational): /' || true
    rm -f ncn-api.new
    go build -o ncn-api.new .
    test -x ncn-api.new
    mkdir -p /opt/ncn-core-console/backups
    [ -f ncn-api ] && cp -a ncn-api \"/opt/ncn-core-console/backups/ncn-api.\$(date -u +%Y%m%d-%H%M%S)\" || true
    mv -f ncn-api.new ncn-api
    systemctl daemon-reload
    systemctl enable --now ncn-api.socket
    systemctl enable --now ncn-state-backup.timer 2>/dev/null || true
    systemctl restart ncn-api.service
    # keep the 5 most recent binary backups for rollback
    ls -1t /opt/ncn-core-console/backups/ncn-api.* 2>/dev/null | tail -n +6 | xargs -r rm -f
  "
  ok "ncn-api gated (vet+test) + backed up + restarted (socket-activated, zero-downtime)"

  # Wait for the new process to bind :9000. Without this, the immediate
  # smoke run races the restart and gets 502s from nginx (the upstream
  # is briefly unreachable). 5s of polling is plenty in practice.
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if curl -sk -o /dev/null --max-time 2 https://admin.example.com/api/v1/health; then
      break
    fi
    sleep 1
  done
}

deploy_frontend() {
  hdr "frontend: i18n lint (catches vue-i18n footguns BEFORE shipping)"
  (cd "$ROOT" && npm run lint:i18n) || { err "i18n lint failed — aborting"; exit 1; }
  ok "i18n clean"

  hdr "frontend: vite build"
  (cd "$ROOT" && npm run build 2>&1 | tail -5)
  ok "dist built"

  hdr "frontend: ship dist → tyo"
  # No --delete: vite emits content-hashed asset filenames; the new index.html
  # points to the new hashes but a user's browser may still hold the OLD
  # index.html (or be on the page since before the deploy). If we delete the
  # old hashed bundles, that user hits /assets/index-OLD.js → 404 → black
  # screen until they hard-refresh. Keep old assets around; prune below.
  rsync -av "$ROOT/dist/" root@deploy-host:/opt/ncn-core-console/dist/ | tail -3
  # Per-chunk-prefix prune: vite emits 10+ .js files per build (vendor-vue,
  # Alerts-XYZ.js, Bird-XYZ.js, index-XYZ.js, ...). They all share the same
  # build-time mtime second so a flat "ls -1t | tail -n +7" gives nondeterministic
  # ordering and can delete the newest index-XYZ.js while keeping older ones.
  # Instead, group by chunk PREFIX (everything before the last -<hash>.{js,css})
  # and keep only the 2 newest of each prefix. That way each chunk type keeps
  # current + one older, which is plenty for stale-tab users.
  ssh -o StrictHostKeyChecking=no root@deploy-host "
    cd /opt/ncn-core-console/dist/assets || exit 0
    for ext in js css; do
      # All files matching *.\$ext, but EXCLUDE the precompressed *.\$ext.br
      # and *.\$ext.gz variants (those are paired with the originals).
      ls -1 *.\$ext 2>/dev/null | sed -E 's/-[A-Za-z0-9_-]+\\.\$ext\$//' | sort -u | while read prefix; do
        ls -1t \"\${prefix}-\"*.\$ext 2>/dev/null | tail -n +3 | while read victim; do
          rm -f \"\$victim\" \"\$victim.br\" \"\$victim.gz\"
        done
      done
    done
  "
  ok "dist deployed (each chunk: 2 newest builds kept)"
}

rollback_backend() {
  hdr "rollback: restore previous ncn-api binary on tyo"
  ssh -o StrictHostKeyChecking=no root@deploy-host "set -e
    cd /opt/ncn-core-console/backend
    BK=\$(ls -1t /opt/ncn-core-console/backups/ncn-api.* 2>/dev/null | head -1)
    [ -z \"\$BK\" ] && { echo 'no backup binary to roll back to'; exit 1; }
    echo \"restoring \$BK\"
    cp -a \"\$BK\" ncn-api
    systemctl restart ncn-api.service
  "
  # wait for the restored binary to bind :9000 before smoke
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    curl -sk -o /dev/null --max-time 2 https://admin.example.com/api/v1/health && break
    sleep 1
  done
  ok "rolled back to previous binary"
}

deploy_nginx() {
  hdr "nginx: ship configs + test + reload"
  rsync -av "$ROOT/deploy/nginx-ncn-core-console.conf" \
    root@deploy-host:/etc/nginx/sites-available/ncn-core-console | tail -1
  # sites-enabled MUST be a symlink to sites-available, else this rsync
  # writes a file nginx never reads (it diverged once as a stale copy and
  # silently no-op'd a config change — 2026-05-30). Re-assert the symlink
  # every deploy so the bug can't recur.
  ssh -o StrictHostKeyChecking=no root@deploy-host "
    ln -sf /etc/nginx/sites-available/ncn-core-console /etc/nginx/sites-enabled/ncn-core-console
    nginx -t && systemctl reload nginx
  "
  ok "nginx reloaded"
}

smoke() {
  hdr "smoke"
  # Same shape as webmail's smoke. Authenticated routes should 401 (envelope)
  # not 404 (route missing) or 5xx (panic) or 413 (nginx body-limit
  # regression).
  local probes=(
    "GET  /api/v1/health                       200  public health"
    "GET  /api/v1/auth/me                      401  auth gate"
    "GET  /api/v1/visitor                      200  public visitor probe"
    "GET  /api/v1/status/summary               200  public status summary"
    "GET  /api/v1/status/latency               200  public latency matrix"
    "GET  /api/v1/lg/sessions                  200  public LG bgp sessions"
    "GET  /api/v1/auth/nodes                    401  node registry admin-gated"
    "GET  /api/v1/auth/alert-rules              401  alert rules admin-gated"
    "GET  /api/v1/auth/oauth-identities         401  oauth identities protected"
    "POST /api/v1/auth/passkey/login/begin     200  passkey-login challenge"
    "POST /api/v1/auth/passkey/register/begin  401  passkey-register protected"
    "GET  /api/v1/auth/passkey                 401  passkey list protected"
    # operator → webmail bridge. 401 = endpoint mounted + auth-gated as
    # expected. 404 = mail_bridge.go missing from the deployed binary
    # (this exact regression cost a "unexpected error" outage and got
    # noticed only because a user complained — exactly the kind of
    # silent gap smoke is supposed to catch).
    "POST /api/v1/auth/mail-self-invite                  401  operator → webmail bridge"
    # Admin-driven role mailbox recovery (proxies to ncn-mail on pop-03).
    # 401 = auth gate engaged; 404 = mail_role_recover.go didn't ship.
    "POST /api/v1/auth/mail-role-recover                 401  admin role-recover proxy"
    # Forgot-password queue mirror (proxies to webmail). 401 = admin gate
    # engaged; 404 = mail_forgot_bridge.go didn't ship.
    "GET  /api/v1/auth/mail-forgot                       401  forgot-queue list (admin proxy)"
    "DELETE /api/v1/auth/mail-forgot/abc                 401  forgot-queue dismiss (admin proxy)"
    "POST /api/v1/auth/mail-forgot/abc/approve           401  forgot-queue approve+send-link (admin proxy)"
    # Peering application intake (public, anonymous). 400 = body shape
    # validation engaged. 404 = peering_apply.go didn't ship.
    "POST /api/v1/peering/apply                          400  peering apply intake (public)"
    # Peering review (admin-only). 401 = auth gate.
    "GET  /api/v1/auth/peering/applications              401  peering review list (admin)"
    "POST /api/v1/auth/peering/applications/abc/decide   401  peering decide (admin)"
    # Security audit log (admin-only). 401 across the board without a
    # session. If audit.go didn't ship we'd get 404 here instead.
    "POST /api/v1/auth/ssh-login/begin                   400  ssh-login begin (empty body)"
    "POST /api/v1/auth/ssh-login/finish                  401  ssh-login finish (empty body → unknown challenge)"
    "GET  /api/v1/auth/ssh-login/redeem                  302  ssh-login redeem (missing t -> /login)"
    "GET  /api/v1/auth/ssh-keys                          401  ssh-keys list (operator-only)"
    "POST /api/v1/auth/ssh-keys                          401  ssh-keys add (operator-only)"
    "DELETE /api/v1/auth/ssh-keys/abc                    401  ssh-keys delete (operator-only)"
    "GET  /api/v1/auth/api-tokens                        401  api-tokens list (operator-only)"
    "POST /api/v1/auth/api-tokens                        401  api-tokens create (operator-only)"
    "DELETE /api/v1/auth/api-tokens/abc                  401  api-tokens revoke (operator-only)"
    "GET  /api/v1/auth/audit                             401  audit query (admin)"
    "GET  /api/v1/auth/audit/stats                       401  audit stats (admin)"
    "GET  /api/v1/auth/audit/export                      401  audit export (admin)"
    # SSO bidirectional with webmail. ingest with no ticket = 400
    # "missing ticket"; mint needs live operator session = 401.
    "GET  /api/v1/auth/sso/ingest                        400  sso ingest (no ticket)"
    "POST /api/v1/auth/sso/mail-ticket                   401  sso mint mail ticket"
    # sso-out without ?target=mail returns 400; sending a probe with
    # target=mail would redirect to /login = 302 (no session here).
    "GET  /api/v1/auth/sso-out?target=mail               302  sso-out entry (unauth → /login)"
    # break-glass recovery (signed URL minted by `ncn-api admin
    # mint-recover`). Preview with no token = 401 (verifier engaged);
    # POST with bad body = 400. Either way, the route MUST be mounted —
    # 404 means recover_bootstrap.go didn't ship.
    "GET  /api/v1/auth/bootstrap-recover/preview         401  recovery verifier (preview)"
    "POST /api/v1/auth/bootstrap-recover                 400  recovery verifier (submit)"
  )
  local failed=0
  for row in "${probes[@]}"; do
    local method path want label
    read -r method path want label <<<"$row"
    local args=()
    if [[ "$method" == POST ]]; then args+=(-X POST -H 'Content-Type: application/json' --data '{}'); fi
    local got
    got=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 8 \
          "${args[@]}" "https://admin.example.com$path")
    if [[ "$got" == "$want" ]]; then
      ok "$method $path → $got ($label)"
    else
      err "$method $path → $got, want $want ($label)"
      failed=1
    fi
  done

  # Body-limit check: nginx must accept ≥ 32 KB on /api/v1/auth/* so
  # WebAuthn attestation responses don't 413. Posts 16 KB of dummy JSON,
  # expects 401 (auth fail) not 413 (nginx limit).
  hdr "nginx body-limit regression probe"
  local big_body
  big_body=$(python3 -c 'import json; print(json.dumps({"x":"A"*16000}))')
  local big_code
  big_code=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 8 \
    -X POST -H 'Content-Type: application/json' --data "$big_body" \
    https://admin.example.com/api/v1/auth/passkey/register/finish)
  if [[ "$big_code" == "401" ]] || [[ "$big_code" == "400" ]]; then
    ok "16 KB body → $big_code (nginx accepted; backend rejected as expected)"
  else
    err "16 KB body → $big_code (nginx may have 413'd — check client_max_body_size)"
    failed=1
  fi

  # NOTE: admin.example.com now sits behind Cloudflare with a Managed
  # Challenge on all non-/api paths. A scripted curl cannot solve the JS
  # challenge so we can't fetch the SPA index.html or /assets/*.js from
  # here. Real browsers complete the challenge once and get a
  # cf_clearance cookie that exempts subsequent asset loads — fully
  # transparent to the user. We just verify the dist file exists on
  # disk via ssh, as a proxy for "the latest hash is shipped".
  local hash
  hash=$(ssh -o StrictHostKeyChecking=no root@deploy-host \
         "ls -t /opt/ncn-core-console/dist/assets/index-*.js 2>/dev/null | head -1" \
         | xargs -n1 basename 2>/dev/null || echo '?')
  ok "dist hash (origin): $hash"

  [[ $failed -eq 0 ]] || { err "smoke FAILED"; exit 1; }
}

# health — full-stack "is everything up?" check (beyond smoke's HTTP probes):
# control-plane services on ctrl-01, the flow pipeline, and the HA/RPKI bits on
# pop-03. Each check is independent; prints a green/red line and a final tally.
health() {
  local bad=0
  pass() { ok "$*"; }
  miss() { err "$*"; bad=$((bad+1)); }

  # Probe public endpoints FROM the ctrl-01 edge (consistent vantage with the
  # rest of the checks; the operator's box may have restricted egress — e.g.
  # mail.example.com is reachable from the edge but not necessarily from here).
  hdr "public endpoints (from ctrl-01 edge)"
  while read -r u code; do
    [[ -z "$u" ]] && continue
    [[ "$code" =~ ^(200|302|401)$ ]] && pass "$u → $code" || miss "$u → ${code:-000}"
  done < <(ssh -o ConnectTimeout=8 -o StrictHostKeyChecking=no root@deploy-host bash -s <<'R' 2>/dev/null
for u in https://admin.example.com/api/v1/health https://admin.example.com/api/v1/status/summary https://example.com/ https://mail.example.com/; do
  printf "%s %s\n" "$u" "$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 "$u" 2>/dev/null)"
done
R
)

  hdr "control plane (ctrl-01)"
  # one ssh gathers all local service states + the flow pipeline freshness
  local rpt
  rpt=$(ssh -o ConnectTimeout=8 -o StrictHostKeyChecking=no root@deploy-host bash -s <<'R' 2>/dev/null
for s in ncn-api.socket nginx goflow2 softflowd; do
  printf "%s %s\n" "$s" "$(systemctl is-active "$s" 2>/dev/null)"
done
# flow pipeline: distinct sFlow exporters seen in the last 2000 lines (proves the PoP fleet)
n=$(tail -2000 /var/log/ncn-flows/flows.jsonl 2>/dev/null | grep -oE '"sampler_address":"[^"]+"' | sort -u | wc -l)
printf "flow-exporters %s\n" "${n:-0}"
R
)
  while read -r name state; do
    case "$name" in
      flow-exporters) [[ "${state:-0}" -ge 1 ]] && pass "flow exporters seen: $state" || miss "flow exporters seen: 0 (collector idle)";;
      *) [[ "$state" == active ]] && pass "$name: active" || miss "$name: ${state:-unknown}";;
    esac
  done <<< "$rpt"

  hdr "HA + RPKI (pop-03)"
  local h
  h=$(ssh -o ConnectTimeout=8 -o StrictHostKeyChecking=no root@deploy-host \
      "ssh -i /etc/ncn-core-console/fleet-key -o StrictHostKeyChecking=no -o ConnectTimeout=8 root@198.51.100.3 \
        'echo routinator \$(systemctl is-active routinator); echo pg \$(sudo -u postgres psql -tAc \"select case when pg_is_in_recovery() then '\''replica-ok'\'' else '\''PRIMARY?'\'' end\" 2>/dev/null)'" 2>/dev/null)
  while read -r name state; do
    [[ -z "$name" ]] && continue
    case "$name" in
      routinator) [[ "$state" == active ]] && pass "pop-03 routinator: active" || miss "pop-03 routinator: ${state:-unknown}";;
      pg) [[ "$state" == replica-ok ]] && pass "pop-03 PG: streaming replica" || miss "pop-03 PG: ${state:-unknown}";;
    esac
  done <<< "$h"

  printf "\n"
  [[ $bad -eq 0 ]] && ok "health: all green" || { err "health: $bad check(s) failed"; exit 1; }
}

case "$MODE" in
  backend)  deploy_backend;            smoke ;;
  frontend) deploy_frontend;           smoke ;;
  nginx)    deploy_nginx;              smoke ;;
  smoke)    smoke ;;
  health)   health ;;
  rollback) rollback_backend;          smoke ;;
  all)      deploy_backend; deploy_frontend; smoke ;;
  *)        err "usage: deploy.sh [backend|frontend|nginx|smoke|health|rollback|all]"; exit 2 ;;
esac

ok "done"
