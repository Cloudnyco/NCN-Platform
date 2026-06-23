#!/usr/bin/env bash
# =============================================================================
# deploy-all.sh — single-command deploy for the entire NCN stack.
# =============================================================================
#
# What it does:
#   1. Sanity-check the workspace (warns if working tree is dirty so you
#      don't ship something you forgot to commit).
#   2. Deploy webmail first (ncn-mail on pop-03). The admin console's
#      bridge endpoints call into webmail — putting webmail first means
#      any new webmail endpoint exists before admin tries to proxy it.
#   3. Deploy core-console (ncn-api on deploy-host).
#   4. Each project's own deploy.sh already runs smoke probes and exits
#      non-zero on failure; `set -e` here propagates that out.
#   5. Final summary: dist hashes shipped, and any "you forgot to commit"
#      breadcrumbs.
#
# Flags:
#   --admin-only     skip webmail; just deploy core-console
#   --webmail-only   skip core-console; just deploy webmail
#   --check          run only the smoke probes from both projects, no
#                    rsync / build / restart. Useful after a manual change
#                    to nginx or to verify state at any time.
#   -h | --help      print this banner and exit
#
# Exit codes:
#   0   everything green
#   1   webmail deploy failed (smoke or build)
#   2   core-console deploy failed
#   3   bad invocation (unknown flag, missing project dir, etc.)
#
# Emergency-recovery scenario:
#   git clone <this-repo> /root/ncn-workspace
#   cd /root/ncn-workspace && ./deploy-all.sh
#   (Requires SSH access to deploy-host and the fleet-key for pop-03.)
# =============================================================================

set -euo pipefail

# Resolve workspace root from the script's own location so deploy-all.sh
# works whether you cd in first or just type the full path.
WORKSPACE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ADMIN_DIR="$WORKSPACE/core-console"
WEBMAIL_DIR="$WORKSPACE/webmail"

# --- Colour helpers (match style of the per-project deploy.sh) -------------
color() { printf "\033[%sm%s\033[0m" "$1" "$2"; }
hdr()   { printf "\n%s\n" "$(color "1;35" "═══ $* ═══")"; }   # magenta for top-level
sub()   { printf "%s\n"   "$(color "1;36" "── $* ──")"; }     # cyan for substeps
ok()    { printf "  %s %s\n" "$(color 32 "✓")" "$*"; }
warn()  { printf "  %s %s\n" "$(color 33 "⚠")" "$*"; }
err()   { printf "  %s %s\n" "$(color 31 "✗")" "$*" >&2; }

# --- Argument parsing ------------------------------------------------------
DO_ADMIN=1
DO_WEBMAIL=1
CHECK_ONLY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --admin-only)    DO_WEBMAIL=0 ;;
    --webmail-only)  DO_ADMIN=0 ;;
    --check)         CHECK_ONLY=1 ;;
    -h|--help)
      # Print the top doc-block (every leading `#` line after the shebang)
      # so usage docs live in one place. awk stops at the first non-`#`
      # line so `set -euo pipefail` and the code below don't leak into
      # the help output.
      awk 'NR>1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "$0"
      exit 0
      ;;
    *)
      err "unknown flag: $1"
      err "try: $0 --help"
      exit 3
      ;;
  esac
  shift
done

if [[ $DO_ADMIN -eq 0 && $DO_WEBMAIL -eq 0 ]]; then
  err "nothing to do (--admin-only and --webmail-only are mutually exclusive)"
  exit 3
fi

# --- Working-tree sanity check ---------------------------------------------
# Don't BLOCK on dirty tree — the operator may be mid-iteration and need
# to ship a hotfix before stashing. Just warn loudly so it's obvious.
hdr "preflight"
if [[ -d "$WORKSPACE/.git" ]]; then
  DIRTY=$(cd "$WORKSPACE" && git status --porcelain | wc -l)
  CURRENT_BRANCH=$(cd "$WORKSPACE" && git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "?")
  HEAD_SHA=$(cd "$WORKSPACE" && git rev-parse --short HEAD 2>/dev/null || echo "?")
  if [[ $DIRTY -gt 0 ]]; then
    warn "workspace has $DIRTY uncommitted change(s) on branch $CURRENT_BRANCH ($HEAD_SHA)"
    warn "deploy will ship these too — commit afterward so the next emergency-rebuild matches prod"
  else
    ok "workspace clean (branch=$CURRENT_BRANCH HEAD=$HEAD_SHA)"
  fi
else
  warn "no git in $WORKSPACE — backup story is broken if this machine dies"
fi

# --- Project-dir existence guards ------------------------------------------
if [[ $DO_ADMIN -eq 1 && ! -x "$ADMIN_DIR/deploy/deploy.sh" ]]; then
  err "missing $ADMIN_DIR/deploy/deploy.sh"
  exit 3
fi
if [[ $DO_WEBMAIL -eq 1 && ! -x "$WEBMAIL_DIR/deploy/deploy.sh" ]]; then
  err "missing $WEBMAIL_DIR/deploy/deploy.sh"
  exit 3
fi

# --- Deploy phases ---------------------------------------------------------
# Webmail goes first so the bridge endpoints admin uses (forgot-bridge,
# send-bridge, sso/ingest, role-recover) exist before admin's proxy code
# tries to reach them. Doing it in the other order means admin's first
# probe-after-deploy would hit a brand-new endpoint that hasn't shipped
# to pop-03 yet → 404.
WEBMAIL_HASH=""
ADMIN_HASH=""

run_webmail() {
  hdr "webmail (ncn-mail @ pop-03)"
  if [[ $CHECK_ONLY -eq 1 ]]; then
    sub "smoke only"
    (cd "$WEBMAIL_DIR" && bash deploy/deploy.sh smoke) || { err "webmail smoke FAILED"; exit 1; }
  else
    (cd "$WEBMAIL_DIR" && bash deploy/deploy.sh) || { err "webmail deploy FAILED"; exit 1; }
  fi
  WEBMAIL_HASH=$(cd "$WEBMAIL_DIR" && ls -t dist/assets/index-*.js 2>/dev/null | head -1 | xargs -I{} basename {} || echo "?")
  ok "webmail done (dist=$WEBMAIL_HASH)"
}

run_admin() {
  hdr "core-console (ncn-api @ deploy-host)"
  if [[ $CHECK_ONLY -eq 1 ]]; then
    sub "smoke only"
    (cd "$ADMIN_DIR" && bash deploy/deploy.sh smoke) || { err "admin smoke FAILED"; exit 2; }
  else
    (cd "$ADMIN_DIR" && bash deploy/deploy.sh) || { err "admin deploy FAILED"; exit 2; }
  fi
  ADMIN_HASH=$(cd "$ADMIN_DIR" && ls -t dist/assets/index-*.js 2>/dev/null | head -1 | xargs -I{} basename {} || echo "?")
  ok "core-console done (dist=$ADMIN_HASH)"
}

[[ $DO_WEBMAIL -eq 1 ]] && run_webmail
[[ $DO_ADMIN   -eq 1 ]] && run_admin

# --- Final summary ---------------------------------------------------------
hdr "summary"
if [[ $CHECK_ONLY -eq 1 ]]; then
  ok "smoke probes passed — no deploy was performed"
else
  [[ $DO_WEBMAIL -eq 1 ]] && ok "webmail dist: $WEBMAIL_HASH (mail.example.com)"
  [[ $DO_ADMIN   -eq 1 ]] && ok "admin   dist: $ADMIN_HASH (admin.example.com)"
  if [[ -d "$WORKSPACE/.git" ]]; then
    DIRTY_AFTER=$(cd "$WORKSPACE" && git status --porcelain | wc -l)
    if [[ $DIRTY_AFTER -gt 0 ]]; then
      warn "$DIRTY_AFTER file(s) still uncommitted — back up the workspace by committing + pushing"
    fi
  fi
fi
ok "done"
