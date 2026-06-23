#!/usr/bin/env bash
# =============================================================================
# scripts/restore-secrets.sh — decrypt + (optionally) push back to prod.
# =============================================================================
#
# Decrypts an age-encrypted backup produced by backup-secrets.sh.
# Identity is found automatically:
#   1. ~/.ssh/id_ed25519  (this machine, no password) — usual case.
#   2. ~/.age-recovery    (your recovery key from Bitwarden, pasted here)
#                         — disaster recovery on a fresh machine.
#
# Modes:
#   --list    [file]              show what's inside (manifest + tar contents)
#   --extract [file] [destdir]    decrypt + extract to destdir for inspection
#   --push    tyo|hkg|both [file] decrypt + push back to live hosts (DANGEROUS)
#
# Default file: the newest backups/secrets-*.age in the workspace.
#
# Examples:
#   ./scripts/restore-secrets.sh --list
#   ./scripts/restore-secrets.sh --extract /tmp/peek
#   ./scripts/restore-secrets.sh --push tyo
#   ./scripts/restore-secrets.sh --push both backups/secrets-20260526T160000Z.age
#
# WARNING — --push overwrites /etc/ncn-core-console/ on tyo and/or
# /etc/ncn-mail/ on pop-03 with the BACKUP CONTENTS. Each push asks for
# explicit confirmation; you should also stop the relevant service first
# if you don't want it running with a stale-then-new-state mismatch.
# =============================================================================

set -euo pipefail

WORKSPACE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_DIR="$WORKSPACE/backups"

TYO_HOST="root@deploy-host"
TYO_SECRETS_DIR="/etc/ncn-core-console"

HKG_VIA_TYO="ssh -i /etc/ncn-core-console/fleet-key debian@198.51.100.3"
HKG_SECRETS_DIR="/etc/ncn-mail"

color() { printf "\033[%sm%s\033[0m" "$1" "$2"; }
hdr()   { printf "\n%s\n" "$(color "1;36" "── $* ──")"; }
ok()    { printf "  %s %s\n" "$(color 32 "✓")" "$*"; }
warn()  { printf "  %s %s\n" "$(color 33 "⚠")" "$*"; }
err()   { printf "  %s %s\n" "$(color 31 "✗")" "$*" >&2; }

usage() {
  awk 'NR>1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "$0"
  exit 0
}

# ── Argument parsing ───────────────────────────────────────────────────────
MODE=""
TARGET=""
FILE=""
DEST=""

case "${1:-}" in
  -h|--help|"") usage ;;
  --list)
    MODE="list"; FILE="${2:-}"
    ;;
  --extract)
    MODE="extract"
    # Two forms:  --extract destdir   or  --extract file destdir
    if [[ $# -ge 3 ]]; then FILE="$2"; DEST="$3"
    else                    DEST="${2:-}"
    fi
    ;;
  --push)
    MODE="push"; TARGET="${2:-}"; FILE="${3:-}"
    case "$TARGET" in tyo|hkg|both) ;;
      *) err "--push needs tyo|hkg|both"; exit 2 ;;
    esac
    ;;
  *) err "unknown flag: $1"; usage ;;
esac

# Default to newest backup if not specified.
if [[ -z "$FILE" ]]; then
  FILE=$(ls -1t "$BACKUP_DIR"/secrets-*.age 2>/dev/null | head -1 || true)
  if [[ -z "$FILE" ]]; then
    err "no backups/*.age files found and no file given"
    exit 3
  fi
fi
[[ -f "$FILE" ]] || { err "not a file: $FILE"; exit 3; }
ok "using $FILE"

# ── Locate decryption identity ─────────────────────────────────────────────
# age accepts -i multiple times; we try the SSH key first, then look for
# a pasted recovery key at ~/.age-recovery (mode-protected).
IDENTITIES=()
[[ -f "$HOME/.ssh/id_ed25519"   ]] && IDENTITIES+=(-i "$HOME/.ssh/id_ed25519")
[[ -f "$HOME/.age-recovery"     ]] && IDENTITIES+=(-i "$HOME/.age-recovery")

if [[ ${#IDENTITIES[@]} -eq 0 ]]; then
  err "no decryption identity available."
  err "either this machine's ~/.ssh/id_ed25519, or paste recovery key into ~/.age-recovery"
  err "(recovery key is the AGE-SECRET-KEY-… line from your Bitwarden vault)"
  exit 4
fi

# ── Decrypt into a tmp staging dir ─────────────────────────────────────────
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

hdr "decrypt"
if ! age -d "${IDENTITIES[@]}" "$FILE" | tar -xf - -C "$STAGE"; then
  err "decryption FAILED — wrong identity or file is corrupt"
  exit 5
fi
ok "decrypted to $STAGE"
ls -la "$STAGE" | grep -v '^total' | awk '{printf "  %s  %s\n", $5, $NF}'

# ── Mode dispatch ──────────────────────────────────────────────────────────
case "$MODE" in
list)
  hdr "manifest"
  cat "$STAGE/manifest.txt" 2>/dev/null || warn "no manifest.txt in archive"
  if [[ -f "$STAGE/tyo.tar.gz" ]]; then
    hdr "tyo.tar.gz contents"
    tar -tvzf "$STAGE/tyo.tar.gz"
  fi
  if [[ -f "$STAGE/hkg.tar.gz" ]]; then
    hdr "hkg.tar.gz contents"
    tar -tvzf "$STAGE/hkg.tar.gz"
  fi
  ;;

extract)
  [[ -n "$DEST" ]] || { err "extract needs a destdir"; exit 2; }
  mkdir -p "$DEST"
  cp "$STAGE"/* "$DEST/"
  ok "extracted to $DEST"
  warn "individual files are still inside tyo.tar.gz / hkg.tar.gz —"
  warn "untar them in place to inspect:"
  echo "    cd $DEST && tar -xvzf tyo.tar.gz && tar -xvzf hkg.tar.gz"
  ;;

push)
  hdr "push: $TARGET"
  warn "this OVERWRITES live secrets on $TARGET host(s)."
  warn "what gets restored is whatever is in the snapshot — including potentially STALE state files"
  warn "(operators.json, invites.json, peering-applications.json) — those should normally NOT be"
  warn "restored from backup unless you're rebuilding a host from scratch."
  warn ""
  warn "if you only want the KEY files (HMAC + SSH + turnstile + session secrets), --extract"
  warn "first and rsync just those files manually. This --push is the all-or-nothing flag."
  warn ""
  read -r -p "type 'YES' to proceed: " confirm
  [[ "$confirm" == "YES" ]] || { err "aborted"; exit 6; }

  if [[ "$TARGET" == "tyo" || "$TARGET" == "both" ]]; then
    [[ -f "$STAGE/tyo.tar.gz" ]] || { err "no tyo.tar.gz in this backup"; exit 7; }
    hdr "push to tyo"
    cat "$STAGE/tyo.tar.gz" | ssh "$TYO_HOST" "tar --numeric-owner -xzf - -C /"
    ssh "$TYO_HOST" "systemctl restart ncn-api && sleep 1 && systemctl is-active ncn-api"
    ok "tyo secrets restored, ncn-api restarted"
  fi
  if [[ "$TARGET" == "hkg" || "$TARGET" == "both" ]]; then
    [[ -f "$STAGE/hkg.tar.gz" ]] || { err "no hkg.tar.gz in this backup"; exit 7; }
    hdr "push to pop-03"
    # stream → ssh tyo → ssh hkg → sudo tar
    cat "$STAGE/hkg.tar.gz" | ssh "$TYO_HOST" "$HKG_VIA_TYO 'sudo tar --numeric-owner -xzf - -C /'"
    ssh "$TYO_HOST" "$HKG_VIA_TYO 'sudo systemctl restart ncn-mail && sleep 1 && sudo systemctl is-active ncn-mail'"
    ok "hkg secrets restored, ncn-mail restarted"
  fi
  ;;
esac

hdr "done"
