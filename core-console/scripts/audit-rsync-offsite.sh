#!/usr/bin/env bash
# audit-rsync-offsite.sh — daily mirror /var/log/ncn-audit → pop-04.
#
# Closes the third hardening step left as TODO in backend/audit.go:
#
#   1. logrotate (done — deploy/logrotate/ncn-audit, daily, 365d)
#   2. chattr +a   (done — /var/log/ncn-audit/audit.jsonl is append-only)
#   3. offsite rsync — THIS SCRIPT
#
# Failure domain choice: pop-04 (Region C) vs ctrl-01 (Region A). Different
# country, different ASN (DataSphere vs Cyberjet for tyo), different
# operational scope (pop-04 is xray-only, not running ncn-api). If a
# whole-tyo loss happens, the mirror survives at pop-04 long enough to
# matter for forensics.
#
# Runs as: root on ctrl-01 (where /var/log/ncn-audit/ lives).
# Triggered by: /etc/systemd/system/ncn-audit-rsync.timer (daily 03:00 UTC).
#
# Mirror sink: /var/log/ncn-audit-mirror/ctrl-01/ on pop-04.
# We don't push to / overwrite the live audit log on the destination —
# mirror is a one-way *.jsonl* dump indexed by source hostname so a
# future second-source (e.g. webmail audit) can be mirrored to the same
# tree without collision.
#
# What this script does:
#
#   1. rsync /var/log/ncn-audit/{audit.jsonl,*.jsonl.gz} → pop-04 mirror.
#   2. On the destination: chattr +a each rotated *.jsonl.gz so even a
#      compromised pop-04 can't tamper with historical chunks.
#   3. Log start/finish + exit code to systemd journal.
#
# Reverse / disable: `systemctl disable --now ncn-audit-rsync.timer`.

set -uo pipefail

LOG_DIR="/var/log/ncn-audit"
DEST_HOST="root@198.51.100.4"          # pop-04
DEST_DIR="/var/log/ncn-audit-mirror/ctrl-01"
FLEET_KEY="/etc/ncn-core-console/fleet-key"
KNOWN_HOSTS="/etc/ncn-core-console/fleet-known-hosts"

SSH_OPTS=(-i "$FLEET_KEY"
          -o "StrictHostKeyChecking=accept-new"
          -o "UserKnownHostsFile=$KNOWN_HOSTS"
          -o "BatchMode=yes"
          -o "ConnectTimeout=10")

log() { echo "$(date -u +%FT%TZ) audit-rsync-offsite: $*"; }

if [[ ! -d "$LOG_DIR" ]]; then
  log "ERROR: $LOG_DIR not present — is ncn-api logging audit?"
  exit 1
fi

log "starting mirror to $DEST_HOST:$DEST_DIR"

# Ensure destination directory exists with locked-down permissions.
ssh "${SSH_OPTS[@]}" "$DEST_HOST" "mkdir -p $DEST_DIR && chmod 700 $DEST_DIR" \
  || { log "ERROR: destination mkdir failed"; exit 2; }

# rsync the audit dir. -a preserves perms/times; -z compresses on the
# wire (audit.jsonl is highly compressible). --inplace avoids creating
# .tmp files in a chattr+a tree on the destination.
#
# NOTE: We do NOT use --delete. The mirror is APPEND-ONLY history; once
# a chunk lands at pop-04 it stays even if the source rotates it out
# of the 365-day window. Disk on pop-04 is the only practical limit.
rsync -az --inplace \
  -e "ssh ${SSH_OPTS[*]}" \
  "$LOG_DIR/" "$DEST_HOST:$DEST_DIR/" 2>&1 | tee -a /tmp/ncn-audit-rsync.last \
  || { log "ERROR: rsync failed (exit $?)"; exit 3; }

# Belt-and-braces: chattr +a all rotated chunks at the destination so a
# compromised pop-04 can't rewrite history. The LIVE chunk (audit.jsonl
# with no .gz suffix) is excluded because logrotate's next cycle may
# need to copytruncate it.
ssh "${SSH_OPTS[@]}" "$DEST_HOST" "
  find $DEST_DIR -maxdepth 2 -name '*.jsonl.gz' -exec chattr +a {} + 2>/dev/null || true
" || log "WARN: chattr +a sweep failed (non-fatal)"

log "done — mirror in sync"
