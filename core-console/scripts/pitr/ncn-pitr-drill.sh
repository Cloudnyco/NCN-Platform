#!/bin/bash
# ncn-pitr-drill.sh — prove the PITR archive is actually restorable (run on
# pop-03). Restores the newest base into a throwaway dir on a spare port, replays
# archived WAL to the end, promotes, checks the data, and tears everything down.
# SAFETY: the test instance has archive_mode forced OFF so it can never write to
# the real WAL archive. Debian keeps postgresql.conf/pg_hba in /etc, so the base
# tar lacks them — we synthesize minimal ones here (also the template referenced
# by docs/PITR-RESTORE.md for a real recovery).
set -uo pipefail
ARCH=/var/mail/vhosts/ncn-ha/wal-archive
TEST=/var/mail/vhosts/ncn-ha/pitr-test
BIN=/usr/lib/postgresql/17/bin
PORT=5599
log(){ echo "[pitr-drill] $*"; }

base=$(ls -1dt "$ARCH"/base/base-* 2>/dev/null | head -1)
[ -n "$base" ] || { log "no base backup found"; exit 1; }
log "restoring from $base"

rm -rf "$TEST"; install -d -o postgres -g postgres -m 700 "$TEST"
sudo -u postgres tar -xzf "$base/base.tar.gz" -C "$TEST"

sudo -u postgres tee "$TEST/postgresql.conf" >/dev/null <<EOF
archive_mode = off
restore_command = 'gunzip -c $ARCH/wal/%f.gz > %p'
recovery_target_action = 'promote'
listen_addresses = ''
unix_socket_directories = '/tmp'
port = $PORT
hba_file = '$TEST/pg_hba.conf'
ident_file = '$TEST/pg_ident.conf'
EOF
echo 'local all all trust' | sudo -u postgres tee "$TEST/pg_hba.conf" >/dev/null
sudo -u postgres tee "$TEST/pg_ident.conf" >/dev/null </dev/null
sudo -u postgres tee "$TEST/postgresql.auto.conf" >/dev/null <<EOF
archive_mode = off
archive_command = ''
EOF
sudo -u postgres touch "$TEST/recovery.signal"

log "starting recovery instance on :$PORT ..."
if ! sudo -u postgres "$BIN/pg_ctl" -D "$TEST" -w -t 60 -l "$TEST/recovery.log" start; then
  log "pg_ctl start FAILED; recovery log:"; tail -30 "$TEST/recovery.log"; rm -rf "$TEST"; exit 1
fi

log "recovery instance up; verifying data ..."
echo -n "in_recovery=";   sudo -u postgres "$BIN/psql" -p $PORT -h /tmp -tAc 'SELECT pg_is_in_recovery()' ncn
echo -n "operators rows="; sudo -u postgres "$BIN/psql" -p $PORT -h /tmp -tAc 'SELECT count(*) FROM operators' ncn
echo -n "tables=";        sudo -u postgres "$BIN/psql" -p $PORT -h /tmp -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" ncn
echo "--- recovery log (redo/consistent/promote) ---"; grep -iE 'redo|consistent|recovery|promot|archive recovery' "$TEST/recovery.log" | tail -14

log "tearing down test instance"
sudo -u postgres "$BIN/pg_ctl" -D "$TEST" -m immediate stop >/dev/null 2>&1 || true
rm -rf "$TEST"
log "drill complete"
