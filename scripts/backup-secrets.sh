#!/usr/bin/env bash
# =============================================================================
# scripts/backup-secrets.sh — snapshot + encrypt all NCN production secrets.
# =============================================================================
#
# What it backs up:
#   * deploy-host:/etc/ncn-core-console/   admin-side HMAC keys, fleet SSH key,
#                                          operators.json, recovery keys, etc.
#   * pop-03:/etc/ncn-mail/                webmail HMAC keys, mail-creds.json,
#                                          dovecot-users, etc.
#
# How it's protected:
#   age-encrypted with TWO recipients (either decrypts the same file):
#     1. This machine's ssh-ed25519 key (~/.ssh/id_ed25519.pub).
#        Day-to-day restores from this machine need NO password — age
#        finds the matching identity in ~/.ssh/id_ed25519 automatically.
#     2. The NCN recovery age public key, baked into RECOVERY_PUBKEY below.
#        Disaster recovery from a fresh machine uses the AGE-SECRET-KEY-…
#        you saved into Bitwarden when the keypair was generated.
#
# Output:
#   backups/secrets-<UTC-timestamp>.age      — encrypted blob, safe to commit
#   backups/secrets-<UTC-timestamp>.sha256   — checksum for tampering check
#
# Run when:
#   * After rotating any HMAC key (operator-mail-bridge.key, etc.)
#   * After significant operators.json changes (new admin onboarded, etc.)
#   * Before any maintenance that might brick a host
#   * Monthly anyway, as cheap insurance.
#
# Usage:
#   ./scripts/backup-secrets.sh         # full backup of both hosts
#   ./scripts/backup-secrets.sh --tyo   # tyo only (faster, dev tweak)
#   ./scripts/backup-secrets.sh --hkg   # pop-03 only
# =============================================================================

set -euo pipefail

# ── Configuration ──────────────────────────────────────────────────────────
RECOVERY_PUBKEY="your-recovery-age-pubkey"
SSH_PUBKEY_FILE="${HOME}/.ssh/id_ed25519.pub"

WORKSPACE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_DIR="$WORKSPACE/backups"
mkdir -p "$BACKUP_DIR"

# Where each host's secrets live.
TYO_HOST="root@deploy-host"
TYO_SECRETS_DIR="/etc/ncn-core-console"

# pop-03 is reached via fleet-key from deploy-host (same proxy chain
# webmail/deploy/deploy.sh uses).
HKG_VIA_TYO="ssh -i /etc/ncn-core-console/fleet-key debian@198.51.100.3"
HKG_SECRETS_DIR="/etc/ncn-mail"

# ── Helpers ────────────────────────────────────────────────────────────────
color() { printf "\033[%sm%s\033[0m" "$1" "$2"; }
hdr()   { printf "\n%s\n" "$(color "1;36" "── $* ──")"; }
ok()    { printf "  %s %s\n" "$(color 32 "✓")" "$*"; }
warn()  { printf "  %s %s\n" "$(color 33 "⚠")" "$*"; }
err()   { printf "  %s %s\n" "$(color 31 "✗")" "$*" >&2; }

# ── Argument parsing ───────────────────────────────────────────────────────
DO_TYO=1
DO_HKG=1
case "${1:-}" in
  --tyo)   DO_HKG=0 ;;
  --hkg)   DO_TYO=0 ;;
  -h|--help)
    awk 'NR>1 { if ($0 !~ /^#/) exit; sub(/^# ?/, ""); print }' "$0"
    exit 0
    ;;
  "")      ;;
  *)       err "unknown flag: $1"; exit 2 ;;
esac

# ── Preflight ──────────────────────────────────────────────────────────────
hdr "preflight"
if ! command -v age >/dev/null; then
  err "age is not installed — apt install age (or equivalent)"
  exit 3
fi
if [[ ! -f "$SSH_PUBKEY_FILE" ]]; then
  err "missing $SSH_PUBKEY_FILE — the local SSH pubkey is one of the two recipients"
  exit 3
fi
ok "age $(age --version 2>&1)"
ok "ssh pubkey: $(awk '{print $2}' "$SSH_PUBKEY_FILE" | cut -c1-32)…"
ok "recovery pubkey: ${RECOVERY_PUBKEY:0:32}…"

# ── Stage tarballs in a tmp dir ────────────────────────────────────────────
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

if [[ $DO_TYO -eq 1 ]]; then
  hdr "deploy-host: $TYO_SECRETS_DIR"
  # --numeric-owner: don't depend on UID/GID name resolution at restore.
  # tar over ssh with stdout pipe — nothing touches tyo's disk.
  ssh "$TYO_HOST" "tar --numeric-owner -czf - -C / etc/ncn-core-console" \
    > "$STAGE/tyo.tar.gz"
  ls -la "$STAGE/tyo.tar.gz" | awk '{printf "  size %s bytes\n", $5}'
  ok "captured tyo secrets"
fi

if [[ $DO_HKG -eq 1 ]]; then
  hdr "pop-03: $HKG_SECRETS_DIR (via tyo + fleet-key)"
  # debian@pop-03 needs sudo to read /etc/ncn-mail/; the existing fleet
  # config gives debian NOPASSWD sudo (same as the webmail deploy script).
  ssh "$TYO_HOST" "$HKG_VIA_TYO 'sudo tar --numeric-owner -czf - -C / etc/ncn-mail'" \
    > "$STAGE/hkg.tar.gz"
  ls -la "$STAGE/hkg.tar.gz" | awk '{printf "  size %s bytes\n", $5}'
  ok "captured hkg secrets"
fi

# ── Manifest ───────────────────────────────────────────────────────────────
hdr "writing manifest"
MANIFEST="$STAGE/manifest.txt"
{
  echo "ncn secrets backup"
  echo "==================="
  echo "captured: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "host:     $(uname -n)"
  echo ""
  if [[ $DO_TYO -eq 1 ]]; then
    echo "── tyo: $TYO_SECRETS_DIR ──"
    tar -tvzf "$STAGE/tyo.tar.gz" | grep -v '^d' | awk '{printf "  %-8s  %-30s  %s\n", $3, $4, $6}'
    echo ""
  fi
  if [[ $DO_HKG -eq 1 ]]; then
    echo "── pop-03: $HKG_SECRETS_DIR ──"
    tar -tvzf "$STAGE/hkg.tar.gz" | grep -v '^d' | awk '{printf "  %-8s  %-30s  %s\n", $3, $4, $6}'
  fi
} > "$MANIFEST"
ok "manifest: $(wc -l < "$MANIFEST") lines"

# ── Bundle + encrypt ───────────────────────────────────────────────────────
hdr "encrypt"
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="$BACKUP_DIR/secrets-${TS}.age"

# One outer tar containing tyo.tar.gz, hkg.tar.gz, manifest.txt → age.
tar -C "$STAGE" -cf - . | age \
  -R "$SSH_PUBKEY_FILE" \
  -r "$RECOVERY_PUBKEY" \
  -o "$OUT"

sha256sum "$OUT" | awk '{print $1}' > "$OUT.sha256"
SIZE=$(stat -c %s "$OUT" 2>/dev/null || stat -f %z "$OUT")
ok "wrote $OUT ($SIZE bytes)"
ok "sha256: $(cat "$OUT.sha256")"

# ── Self-test (decrypt with local ssh key) ─────────────────────────────────
# Catches the embarrassing case where the encrypted file is corrupt before
# we trust it as the actual backup.
hdr "self-test"
if age -d -i "$HOME/.ssh/id_ed25519" "$OUT" | tar -t > /dev/null; then
  ok "decryption with local ssh key succeeded — backup is intact"
else
  err "self-test FAILED — backup may be corrupt!"
  exit 4
fi

hdr "done"
ok "encrypted backup: backups/$(basename "$OUT")"
warn "commit + push so the blob lands in github too:"
echo "    cd $WORKSPACE && git add backups/ && git commit -m \"backup: secrets ${TS}\" && git push"
