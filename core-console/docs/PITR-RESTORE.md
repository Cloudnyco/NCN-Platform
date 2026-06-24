# PITR — point-in-time recovery runbook

> **English** · [简体中文](PITR-RESTORE.zh-CN.md)

This runbook covers the logical-corruption / accidental-write safety net, which
is distinct from streaming-replication HA failover. Streaming replication would
propagate a bad `DELETE` to the standby; it protects against node loss. PITR
protects against data loss inside the database, allowing a restore to any moment
back to the oldest retained base backup.

## Components in place

**On ctrl-01 (primary):**
- `archive_mode=on`, `archive_command=/usr/local/bin/ncn-wal-archive %p %f`,
  `archive_timeout=300`, so any write reaches the offsite archive within five
  minutes even when the database is idle.
- `ncn-wal-archive` gzips each completed WAL segment and streams it to pop-03.
  It exits non-zero on any failure, so PostgreSQL retains the segment and
  retries and WAL is never silently lost. Trade-off: a persistently failing
  archiver causes `pg_wal` to grow on the ctrl-01 root volume (see
  "Archiver stuck" below).
- `ncn-wal-restore %f %p` is the matching restore_command. It pulls a segment
  back from pop-03 and decompresses it, and is used by the recovery procedure.
- `/var/lib/postgresql/.ssh/id_ed25519` is the postgres user's dedicated archive
  key, separate from the root fleet key, which the postgres user cannot read.

**On pop-03 (offsite archive, on the secondary `sdb` volume at
`/var/mail/vhosts/ncn-ha/`):**
- `/usr/local/bin/ncn-wal-recv` is an SSH forced-command receiver. The archive
  key is pinned to it in `/root/.ssh/authorized_keys`
  (`command="...ncn-wal-recv",restrict`), so that key can only `recv`/`get`/
  `have`/`list` segments in the archive directory and can never obtain a shell.
  Filenames are whitelisted, rejecting path traversal and shell metacharacters.
- `wal-archive/wal/` holds the archived segments (`*.gz`, mode 0640
  group-readable).
- `wal-archive/base/base-<ts>/` holds frozen base backups (`base.tar.gz` plus
  `backup_manifest`). These are produced weekly by `ncn-pitr-basebackup.timer`
  (Sun 03:30 UTC), which runs `ncn-pitr-basebackup` — a `pg_basebackup` from the
  ctrl-01 primary over the backbone replication connection.
- Retention (`ncn-pitr-basebackup`): keep the newest two bases; prune older
  bases and any WAL segment older than the oldest kept base's START WAL, read
  from its `backup_label`.

## RPO / RTO

- RPO is ≤ 5 min (archive_timeout) for the offsite copy and sub-second for the
  streaming standby. The value of PITR is not a lower RPO but the ability to
  stop before a bad transaction.
- RTO: a restore extracts one base backup and replays the WAL since it,
  typically completing in minutes.

## Recovery procedure (restore to a point in time)

Run on a recovery host with PostgreSQL 17 installed. pop-03 itself is the
simplest target because the archive is local there, so no SSH is needed in the
restore_command. On any other host, copy the postgres archive key and
`ncn-wal-restore` and use the SSH form.

1. **Pick the target.** Choose a `recovery_target_time`, typically just before
   the bad write, for example `'2026-06-22 11:02:00+00'`.

2. **Lay down the base.** Choose the newest base whose backup is older than the
   target:
   ```bash
   BASE=/var/mail/vhosts/ncn-ha/wal-archive/base/base-<ts>
   DEST=/var/lib/postgresql/17/restore        # any empty datadir
   install -d -o postgres -g postgres -m 700 "$DEST"
   sudo -u postgres tar -xzf "$BASE/base.tar.gz" -C "$DEST"
   ```
   On Debian the base tar contains no `postgresql.conf`/`pg_hba.conf` (these
   live in `/etc/postgresql/...`); synthesize minimal ones in `$DEST` (see
   `scripts/pitr/ncn-pitr-drill.sh` for an exact template).

3. **Configure recovery** in `$DEST/postgresql.conf` (or `.auto.conf`):
   ```
   restore_command = 'gunzip -c /var/mail/vhosts/ncn-ha/wal-archive/wal/%f.gz > %p'
   # off-host instead:  restore_command = '/usr/local/bin/ncn-wal-restore %f %p'
   recovery_target_time = '2026-06-22 11:02:00+00'
   recovery_target_action = 'promote'
   archive_mode = off          # CRITICAL: never let the restored instance write
   archive_command = ''        #           to the real archive
   ```
   ```bash
   sudo -u postgres touch "$DEST/recovery.signal"
   ```

4. **Start it** on a spare port and observe convergence:
   ```bash
   sudo -u postgres /usr/lib/postgresql/17/bin/pg_ctl -D "$DEST" -l "$DEST/rec.log" start
   grep -iE 'consistent|recovery stopping|promot' "$DEST/rec.log"
   ```
   Expect `recovery stopping before commit of transaction ...` near the target,
   then `consistent recovery state reached`, then promotion.

5. **Verify, then cut over.** Inspect the recovered data on the spare port. Once
   satisfied, stop the production instance and swap this datadir in, or
   `pg_dump` the needed rows out and re-import them. Re-enable `archive_mode`
   only on the instance that becomes the new primary, and take a fresh base
   backup so the timeline is re-baselined.

## Monitoring

- `replmon` (in ncn-api) watches `pg_stat_archiver.failed_count`. A rising count
  posts "WAL archiving FAILING" to the error channel and clears on recovery.
- `/metrics` exposes `ncn_wal_archived_total`, `ncn_wal_archive_failed_total`,
  and `ncn_wal_last_archive_age_seconds`.

## Archiver stuck (disk-fill risk)

If pop-03 is unreachable for an extended period, `archive_command` keeps failing,
PostgreSQL retains the segments, and `pg_wal` grows on the ctrl-01 root volume,
which has limited free space. The alert fires early. If the condition cannot be
resolved quickly and the disk is filling, the escape hatch is to temporarily
point `archive_command` at a local spool on a roomier mount, or, as a last
resort, set `archive_command='/bin/true'` to drain. The last resort breaks PITR
continuity because those segments are lost, so take a fresh base backup
immediately after restoring real archiving.

## Smoke test

`ncn-pitr-drill.sh` on pop-03 restores the newest base into a throwaway
directory on a spare port (with archive_mode forced off), replays the archived
WAL to consistency, checks the data, and then tears the instance down. Run it
after any archive change. The most recent verified run (2026-06-22) restored the
base, replayed WAL, reached `consistent recovery state reached`, and found
15 tables present.
