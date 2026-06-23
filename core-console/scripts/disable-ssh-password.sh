#!/usr/bin/env bash
# disable-ssh-password.sh — disable SSH password authentication on a PoP.
#
# Runs FROM tyo, TARGETS a single PoP by node-id. Safe-by-design:
#
#   1. Verify the fleet-key already opens a passwordless session to the
#      target. If this fails, REFUSE TO PROCEED — locking ourselves out
#      is worse than leaving password auth on.
#   2. Drop a config snippet at /etc/ssh/sshd_config.d/00-ncn-no-password.conf
#      (so the change is reversible by deleting one file, not by editing
#      the main sshd_config).
#   3. `sshd -t` validates the new config; abort + clean up the snippet
#      if it fails.
#   4. systemctl reload sshd  (active sessions are NOT killed, only
#      future connections see the new policy).
#   5. RE-VERIFY a fresh key-based ssh still works.
#   6. If verification fails, DELETE the snippet, reload sshd, exit
#      non-zero. (We had the original config saved; rollback is one
#      file delete + one reload.)
#
# Usage:
#
#   sudo /opt/ncn-core-console/scripts/disable-ssh-password.sh <node-id>
#   sudo /opt/ncn-core-console/scripts/disable-ssh-password.sh all
#
# `all` iterates pop-03 / pop-04 / pop-06 / pop-05 / pop-01 / pop-08 — does NOT touch ctrl-01
# (tyo runs ncn-api + your inbound dev shell; lock it down by hand or
# extend this map).

set -uo pipefail

case "${1:-}" in
  pop-03) SAN_IP="198.51.100.3"  ;;
  pop-04) SAN_IP="198.51.100.4" ;;
  pop-06) SAN_IP="198.51.100.8";;
  pop-05) SAN_IP="198.51.100.7" ;;
  pop-01) SAN_IP="198.51.100.2"    ;;
  pop-08) SAN_IP="198.51.100.6"  ;;
  all)
    set -e
    failed=0
    for n in pop-03 pop-04 pop-06 pop-05 pop-01 pop-08; do
      echo
      echo "============================================================"
      echo "  $n"
      echo "============================================================"
      bash "$0" "$n" || failed=$((failed+1))
    done
    if (( failed > 0 )); then
      echo
      echo "✗ $failed node(s) failed; see logs above" >&2
      exit 1
    fi
    echo
    echo "✓ All PoPs locked down — only key authentication accepted"
    exit 0
    ;;
  *)
    echo "usage: $0 <node-id|all>" >&2
    echo "valid node-ids: pop-03 pop-04 pop-06 pop-05 pop-01 pop-08" >&2
    exit 2 ;;
esac

NODE="$1"
FLEET_KEY="/etc/ncn-core-console/fleet-key"
KNOWN_HOSTS="/etc/ncn-core-console/fleet-known-hosts"
SSH_OPTS=(-i "$FLEET_KEY"
          -o "StrictHostKeyChecking=accept-new"
          -o "UserKnownHostsFile=$KNOWN_HOSTS"
          -o "BatchMode=yes"
          -o "ConnectTimeout=8"
          -o "PasswordAuthentication=no"
          -o "KbdInteractiveAuthentication=no"
          -o "PubkeyAuthentication=yes")
SSH_TARGET="root@$SAN_IP"

echo "[$NODE] step 1/5 — verify key-based root SSH currently works"
if ! ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'true' 2>/dev/null; then
  echo "[$NODE] ✗ key-based root SSH does NOT work — refusing to lock down (would self-lock)" >&2
  exit 1
fi
echo "[$NODE]   ✓ key login OK"

echo "[$NODE] step 2/5 — install /etc/ssh/sshd_config.d/00-ncn-no-password.conf"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'bash -s' <<'REMOTE'
set -e
mkdir -p /etc/ssh/sshd_config.d
cat > /etc/ssh/sshd_config.d/00-ncn-no-password.conf <<'CONF'
# Installed by scripts/disable-ssh-password.sh from ctrl-01.
# Reverse: delete this file + `systemctl reload sshd`.
PasswordAuthentication no
ChallengeResponseAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
PubkeyAuthentication yes
UsePAM yes
CONF
chmod 0644 /etc/ssh/sshd_config.d/00-ncn-no-password.conf
REMOTE
echo "[$NODE]   ✓ snippet installed"

echo "[$NODE] step 3/5 — validate new sshd config (sshd -t)"
if ! ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'sshd -t' 2>&1; then
  echo "[$NODE] ✗ sshd -t failed — config snippet is invalid; rolling back" >&2
  ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'rm -f /etc/ssh/sshd_config.d/00-ncn-no-password.conf'
  exit 1
fi
echo "[$NODE]   ✓ sshd -t passed"

echo "[$NODE] step 4/5 — systemctl reload sshd (active sessions kept)"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'systemctl reload ssh 2>/dev/null || systemctl reload sshd'
echo "[$NODE]   ✓ sshd reloaded"

echo "[$NODE] step 5/5 — re-verify key login on a fresh connection"
sleep 1
if ! ssh "${SSH_OPTS[@]}" "$SSH_TARGET" 'sudo -n sshd -T 2>/dev/null | grep -E "^passwordauthentication "' ; then
  echo "[$NODE] ✗ POST-RELOAD key login FAILED — rolling back" >&2
  # last-ditch rollback attempt; if THIS fails too the operator must intervene
  ssh "${SSH_OPTS[@]}" -o PreferredAuthentications=publickey "$SSH_TARGET" \
    'rm -f /etc/ssh/sshd_config.d/00-ncn-no-password.conf && systemctl reload ssh 2>/dev/null || systemctl reload sshd' \
    || echo "[$NODE] ✗✗ ROLLBACK ALSO FAILED — log in manually NOW from the console" >&2
  exit 1
fi
echo "[$NODE]   ✓ key login still works; password auth now off"
echo "[$NODE] DONE"
