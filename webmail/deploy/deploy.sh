#!/usr/bin/env bash
# deploy.sh — one-shot webmail deploy.
#
# WHY THIS EXISTS
#   We don't have `go` installed on the dev workstation, so go.mod /
#   go.sum can't be tidied locally. Every time we add a new import to a
#   backend .go file, rsync would push a stale go.mod, then `go build`
#   on tyo would silently fail with "go: updates to go.mod needed".
#   /tmp/ncn-mail would not be regenerated → scp would ship the previous
#   binary → new endpoints return 404.
#
#   Fix: enforce `go mod tidy` immediately before `go build` on every
#   deploy, and `set -e` so any failure aborts the pipeline.
#
# WHAT IT DOES
#   1. rsync backend/ to deploy-host
#   2. tyo: go mod tidy (canonical step) + go build (must produce binary)
#   3. tyo: scp the fresh binary to pop-03 via fleet-key
#   4. pop-03: install + restart ncn-mail
#   5. local: npm run build
#   6. tar dist, ship to pop-03 via tyo, unpack to /var/www/webmail
#   7. smoke probe https://mail.example.com/api/v1/mail/health
#
# Usage:
#   ./deploy.sh            # backend + frontend
#   ./deploy.sh backend    # backend only (skip npm)
#   ./deploy.sh frontend   # frontend only (skip go)
#   ./deploy.sh smoke      # just the smoke probe

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-all}"

color() { printf "\033[%sm%s\033[0m" "$1" "$2"; }
hdr()   { printf "\n%s\n" "$(color "1;36" "── $* ──")"; }
ok()    { printf "  %s %s\n" "$(color 32 "✓")" "$*"; }
err()   { printf "  %s %s\n" "$(color 31 "✗")" "$*" >&2; }

deploy_backend() {
  hdr "1. rsync backend source → tyo"
  rsync -av --delete \
    --exclude='*.exe' \
    --exclude='ncn-mail' \
    "$ROOT/backend/" \
    root@deploy-host:/opt/ncn-mail-build/ | tail -3
  ok "backend synced"

  hdr "2. tyo: go mod tidy + go build"
  ssh -o StrictHostKeyChecking=no root@deploy-host "set -e
    cd /opt/ncn-mail-build
    go mod tidy
    rm -f /tmp/ncn-mail
    go build -o /tmp/ncn-mail .
    test -x /tmp/ncn-mail
    ls -la /tmp/ncn-mail | awk '{print \$5}' | xargs -I{} echo '  binary size: {} bytes'
    scp -i /etc/ncn-core-console/fleet-key /tmp/ncn-mail debian@198.51.100.3:/tmp/ >/dev/null
  "
  ok "ncn-mail built ($(date +%H:%M:%S))"

  hdr "3. pop-03: install + restart ncn-mail"
  ssh -o StrictHostKeyChecking=no root@deploy-host "
    ssh -i /etc/ncn-core-console/fleet-key debian@198.51.100.3 '
      sudo install -m 0755 /tmp/ncn-mail /opt/ncn-mail/ncn-mail
      sudo systemctl restart ncn-mail
      sleep 1
      sudo systemctl is-active ncn-mail
    '
  "
  ok "ncn-mail restarted on pop-03"
}

deploy_frontend() {
  hdr "4a. i18n lint (catches vue-i18n footguns BEFORE shipping)"
  (cd "$ROOT" && npm run lint:i18n) || { err "i18n lint failed — aborting"; exit 1; }
  ok "i18n clean"

  hdr "4b. npm build"
  (cd "$ROOT" && npm run build 2>&1 | tail -5)
  ok "dist built"

  hdr "5. ship dist → pop-03"
  # IMPORTANT: do NOT `rm -rf` the existing /var/www/webmail before unpacking.
  # Vite emits content-hashed asset filenames; the new index.html points to
  # the new hashes but a user's browser may still have the OLD index.html
  # cached (or be sitting on the page since before the deploy). If we delete
  # the old hashed bundles, that user hits /assets/index-OLD.js → 404 →
  # whole SPA fails to load → black screen until they hard-refresh.
  #
  # Instead: unpack the new tar OVER the existing tree (tar replaces files
  # in place), keeping old hashed assets around. Then prune /assets/ to
  # the most-recent N bundles so the directory doesn't grow forever. The
  # newest index.html (no hash in name) always wins because tar overwrites it.
  local tar=/tmp/wm-dist-$(date +%s).tar.gz
  tar czf "$tar" -C "$ROOT/dist" .
  local size=$(du -h "$tar" | awk '{print $1}')
  rsync -av "$tar" root@deploy-host:/tmp/ | tail -1
  ssh -o StrictHostKeyChecking=no root@deploy-host "
    scp -i /etc/ncn-core-console/fleet-key '$tar' debian@198.51.100.3:/tmp/ >/dev/null
    ssh -i /etc/ncn-core-console/fleet-key debian@198.51.100.3 '
      set -e
      sudo mkdir -p /var/www/webmail
      sudo tar xzf $tar -C /var/www/webmail/
      sudo chown -R www-data:www-data /var/www/webmail
      # Prune /assets/ to the 6 most-recent files per extension (keeps old
      # hashes available for 1-2 deploys worth of stale-tab users without
      # letting the dir grow forever).
      for ext in js css; do
        sudo ls -1t /var/www/webmail/assets/*.\$ext 2>/dev/null | tail -n +7 | sudo xargs -r rm -f
      done
    '
  "
  rm -f "$tar"
  ok "dist deployed ($size, old hashed assets kept)"
}

smoke() {
  hdr "6. smoke"
  # Each row: METHOD PATH EXPECT_CODE LABEL
  # Authenticated endpoints should return 401 (proper envelope) without a
  # cookie. Returning 404 means routing broke; 5xx means the handler
  # panicked; anything else means nginx is in the way (think: the 4 KB
  # body-limit regression on admin).
  local probes=(
    "GET  /api/v1/mail/health                              200  public health"
    "GET  /api/v1/mail/me                                  401  auth gate (must not 404)"
    "POST /api/v1/mail/auth                                400  auth shape check (no body → 400, not 5xx)"
    "GET  /api/v1/mail/folders                             401  protected route gate"
    # Operator bridge: invite/preview with a syntactically valid op- token
    # whose signature is bogus. 401 = bridge verifier engaged. 404 here
    # means the invite route is gone; any 5xx means the verifier crashed.
    # This is the matching half of admin's mail-self-invite probe.
    "GET  /api/v1/mail/invite/preview?token=op-eyJ.bogus   401  bridge token verifier"
    # Break-glass mailbox recovery (signed URL minted by `ncn-mail admin
    # mint-recover` on pop-03). Both preview + submit should be mounted.
    "GET  /api/v1/mail/admin/bootstrap-recover/preview     401  mailbox-recover verifier (preview)"
    "POST /api/v1/mail/admin/bootstrap-recover             400  mailbox-recover verifier (submit)"
    # Admin-bridge role-recover: POST without X-Bridge-Sig → 401. 404
    # here would mean role_recover.go didn't ship to pop-03.
    "POST /api/v1/mail/admin/role-recover                  401  role-recover bridge (signature required)"
    # Other HMAC bridges from ncn-api/tyo. 401 = bad/missing signature
    # (probe doesn't sign anything). 404 = the bridge file didn't ship.
    "POST /api/v1/mail/admin/forgot-bridge/list             401  forgot-bridge list (signature required)"
    "POST /api/v1/mail/admin/forgot-bridge/dismiss          401  forgot-bridge dismiss (signature required)"
    "POST /api/v1/mail/admin/forgot-bridge/approve          401  forgot-bridge approve+send-link (signature required)"
    "POST /api/v1/mail/admin/send-bridge                    401  send-bridge (signature required)"
    # Transactional send API — bearer ncntok_ auth. No Authorization header →
    # 401 "missing Bearer token". 404 here means api_send.go didn't ship.
    "POST /api/v1/mail/api/send                             401  transactional send API (bearer required)"
    # Mutual SSO with admin.example.com
    "GET  /api/v1/mail/sso/ingest                           400  sso ingest (no ticket)"
    "POST /api/v1/mail/sso/admin-ticket                     401  sso mint admin ticket"
    # Privacy image proxy — auth-gated, GET-only
    "GET  /api/v1/mail/img-proxy?u=aHR0cHM6Ly9leGFtcGxlLmNvbS9waXhlbC5wbmc  401  img-proxy auth gate"
    # Forward-address verification endpoint — public, token-protected.
    # 401 = verifier engaged with no/bad token. 404 = file didn't ship.
    "GET  /api/v1/mail/forward/verify                       401  forward-verify endpoint"
  )
  local failed=0
  for row in "${probes[@]}"; do
    local method path want label
    read -r method path want label <<<"$row"
    local got
    got=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 8 \
          -X "$method" "https://mail.example.com$path" \
          ${method:+-H 'Content-Type: application/json'} \
          ${method:+--data '{}'})
    if [[ "$got" == "$want" ]]; then
      ok "$method $path → $got ($label)"
    else
      err "$method $path → $got, want $want ($label)"
      failed=1
    fi
  done

  # Frontend hash so we know the new bundle is live.
  local hash
  hash=$(curl -sk --max-time 8 https://mail.example.com/ | grep -oE 'index-[A-Za-z0-9_-]+\.js' | head -1)
  ok "dist hash: $hash"

  [[ $failed -eq 0 ]] || { err "smoke FAILED"; exit 1; }
}

case "$MODE" in
  backend)  deploy_backend;            smoke ;;
  frontend) deploy_frontend;           smoke ;;
  smoke)    smoke ;;
  all)      deploy_backend; deploy_frontend; smoke ;;
  *)        err "usage: deploy.sh [backend|frontend|smoke|all]"; exit 2 ;;
esac

ok "done"
