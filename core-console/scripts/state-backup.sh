#!/usr/bin/env bash
# state-backup.sh — daily versioned snapshot of the core-console PERSISTENT
# STATE to a local versioned dir AND an offsite mirror on pop-04. Two artifacts:
#   1. state-<ts>.tar.gz — /etc/ncn-core-console (file-backed stores + keys +
#      config: fleet-key, oauth.env, the JSON stores, agent-keys, …).
#   2. ncn-pg-<ts>.sql.gz — a pg_dump of the `ncn` Postgres database. As stores
#      migrate off JSON into Postgres (persistence foundation), the tar no
#      longer captures them (op-failures/audit are DB-primary; node-registry/
#      alert-rules dual-write but PG is the read source) — pg_dump closes that
#      gap. A whole-ctrl-01 loss is recoverable from these two artifacts.
#
# Runs as: root on ctrl-01. Fired by ncn-state-backup.timer (daily 02:30 UTC).
# Failure domain for offsite: pop-04 (different country + ASN), same rationale
# as audit-rsync-offsite.sh.
#
# Tarballs contain secrets (fleet-key, oauth.env) → 0600, dir 0700.
set -uo pipefail

SRC=/etc/ncn-core-console
DST=/var/backups/ncn-state
KEEP=14
# Offsite mirror → pop-03 (the HA standby; pop-04 was decommissioned 2026-06-21).
# Reach it over the private v6 backbone; land on its roomy sdb (/var/mail/vhosts),
# NOT its tiny root disk. Same failure-domain rationale: different country + ASN.
HKG=2001:db8:51::1
HKG_MIRROR=/var/mail/vhosts/ncn-state-mirror/ctrl-01
KEY=/etc/ncn-core-console/fleet-key
SSHOPTS="-o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ConnectTimeout=15"
TS=$(date -u +%Y%m%d-%H%M%S)
TB="$DST/state-$TS.tar.gz"

mkdir -p "$DST"; chmod 700 "$DST"
if ! tar czf "$TB" -C "$SRC" . 2>/dev/null; then
  echo "state-backup: tar FAILED" >&2; exit 1
fi
chmod 600 "$TB"
echo "state-backup: wrote $TB ($(du -h "$TB" | cut -f1))"

# prune: keep newest $KEEP locally
ls -1t "$DST"/state-*.tar.gz 2>/dev/null | tail -n +$((KEEP+1)) | xargs -r rm -f

# --- Postgres dump: the migrated stores live here now; the tar above only
#     still captures the dual-written ones. Non-fatal if PG isn't present
#     (the DB is optional — see db.go). Uses local peer auth as the postgres
#     superuser, so no password/DSN is needed in this script. ---
PGDUMP="$DST/ncn-pg-$TS.sql.gz"
if command -v pg_dump >/dev/null 2>&1 && sudo -u postgres psql -tAc 'SELECT 1' ncn >/dev/null 2>&1; then
  if sudo -u postgres pg_dump --no-owner --no-privileges ncn 2>/dev/null | gzip > "$PGDUMP" && [ -s "$PGDUMP" ]; then
    chmod 600 "$PGDUMP"
    echo "state-backup: wrote $PGDUMP ($(du -h "$PGDUMP" | cut -f1))"
  else
    rm -f "$PGDUMP"
    echo "state-backup: pg_dump FAILED (non-fatal; state tar kept)" >&2
  fi
  ls -1t "$DST"/ncn-pg-*.sql.gz 2>/dev/null | tail -n +$((KEEP+1)) | xargs -r rm -f
else
  echo "state-backup: Postgres not present, skipping pg_dump"
fi

# offsite mirror → pop-03 (non-fatal: a failed mirror must not lose the local copy)
if [ -f "$KEY" ]; then
  if ssh -n -i "$KEY" $SSHOPTS root@"$HKG" "mkdir -p '$HKG_MIRROR' && chmod 700 '$(dirname "$HKG_MIRROR")'" 2>/dev/null; then
    if rsync -az --delete -e "ssh -i $KEY $SSHOPTS" \
        --include='state-*.tar.gz' --include='ncn-pg-*.sql.gz' --exclude='*' \
        "$DST/" root@"[$HKG]":"$HKG_MIRROR"/ 2>/dev/null; then
      echo "state-backup: offsite mirror → pop-03 ok"
    else
      echo "state-backup: offsite rsync failed (non-fatal; local copy kept)" >&2
    fi
  else
    echo "state-backup: offsite ssh prep failed (non-fatal; local copy kept)" >&2
  fi
else
  echo "state-backup: no fleet-key, skipping offsite (local copy kept)" >&2
fi
