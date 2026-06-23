# PITR — point-in-time recovery runbook

This is the **logical-corruption / accidental-write** safety net, distinct from
the streaming-replication HA failover (which would just propagate a bad `DELETE`
to the standby). Streaming replication protects against *node loss*; PITR
protects against *data loss inside the DB* — restore to any moment back to the
oldest retained base backup.

## What's in place

**On ctrl-01 (primary):**
- `archive_mode=on`, `archive_command=/usr/local/bin/ncn-wal-archive %p %f`,
  `archive_timeout=300` (so any write is offsite within 5 min even when idle).
- `ncn-wal-archive` gzips each completed WAL segment and streams it to pop-03.
  It exits non-zero on **any** failure → PG retains the segment and retries, so
  WAL is never silently lost. (Trade-off: a persistently failing archiver grows
  `pg_wal` on ctrl-01's tight root disk — see "Archiver stuck" below.)
- `ncn-wal-restore %f %p` — the matching restore_command (pulls a segment back
  from pop-03 and decompresses). Used by the recovery procedure.
- `/var/lib/postgresql/.ssh/id_ed25519` — the postgres user's dedicated archive
  key (separate from the root fleet-key, which postgres can't read).

**On pop-03 (offsite archive, on the 40G `sdb` at `/var/mail/vhosts/ncn-ha/`):**
- `/usr/local/bin/ncn-wal-recv` — SSH **forced-command** receiver. The archive
  key is pinned to it in `/root/.ssh/authorized_keys`
  (`command="...ncn-wal-recv",restrict`), so that key can only `recv`/`get`/
  `have`/`list` segments in the archive dir — never a shell. Filenames are
  whitelisted (rejects path traversal + shell metacharacters).
- `wal-archive/wal/` — the archived segments (`*.gz`, mode 0640 group-readable).
- `wal-archive/base/base-<ts>/` — frozen base backups (`base.tar.gz` +
  `backup_manifest`). Produced weekly by `ncn-pitr-basebackup.timer`
  (Sun 03:30 UTC), which runs `ncn-pitr-basebackup` (a `pg_basebackup` from the
  ctrl-01 primary over the backbone replication connection).
- **Retention** (`ncn-pitr-basebackup`): keep the newest 2 bases; prune older
  bases and any WAL segment older than the oldest kept base's START WAL (read
  from its `backup_label`).

## RPO / RTO

- **RPO** ≤ 5 min (archive_timeout) for the offsite copy; sub-second for the
  streaming standby. PITR's value isn't lower RPO — it's the ability to stop
  *before* a bad transaction.
- **RTO**: restore = extract one ~4 MB base + replay the WAL since. Minutes.

## Recovery procedure (restore to a point in time)

Run on a recovery host with PG 17 installed. Easiest is pop-03 itself (the
archive is local there — no SSH needed in restore_command). On any other host,
copy the postgres archive key + `ncn-wal-restore` and use the SSH form.

1. **Pick the target.** Decide `recovery_target_time` (e.g. just *before* the
   bad write): `'2026-06-22 11:02:00+00'`.

2. **Lay down the base.** Choose the newest base whose backup is *older* than
   the target:
   ```bash
   BASE=/var/mail/vhosts/ncn-ha/wal-archive/base/base-<ts>
   DEST=/var/lib/postgresql/17/restore        # any empty datadir
   install -d -o postgres -g postgres -m 700 "$DEST"
   sudo -u postgres tar -xzf "$BASE/base.tar.gz" -C "$DEST"
   ```
   On Debian the base tar has **no** `postgresql.conf`/`pg_hba.conf` (they live
   in `/etc/postgresql/...`); synthesize minimal ones in `$DEST` (see
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

4. **Start it** on a spare port and watch it converge:
   ```bash
   sudo -u postgres /usr/lib/postgresql/17/bin/pg_ctl -D "$DEST" -l "$DEST/rec.log" start
   grep -iE 'consistent|recovery stopping|promot' "$DEST/rec.log"
   ```
   Expect `recovery stopping before commit of transaction ...` near the target,
   then `consistent recovery state reached`, then promotion.

5. **Verify, then cut over.** Inspect the recovered data on the spare port. When
   satisfied, stop the production instance and swap this datadir in (or
   `pg_dump` the needed rows out and re-import). Re-enable `archive_mode` only on
   the instance that becomes the new primary, and take a fresh base backup so the
   timeline is re-baselined.

## Monitoring

- `replmon` (in ncn-api) watches `pg_stat_archiver.failed_count`; a rising count
  posts **"WAL archiving FAILING"** to the error channel and clears on recovery.
- `/metrics` exposes `ncn_wal_archived_total`, `ncn_wal_archive_failed_total`,
  `ncn_wal_last_archive_age_seconds`.

## Archiver stuck (disk-fill risk)

If pop-03 is unreachable for a long time, `archive_command` keeps failing → PG
keeps the segments → `pg_wal` grows on ctrl-01's ~10G root (only ~2G free). The
alert fires early. If it can't be fixed fast and the disk is filling, the
escape hatch is to **temporarily** point `archive_command` at a local spool on a
roomier mount, or (last resort) `archive_command='/bin/true'` to drain — but
that **breaks PITR continuity** (those segments are gone), so take a fresh base
backup immediately after restoring real archiving.

## Smoke test

`ncn-pitr-drill.sh` on pop-03 restores the newest base into a throwaway dir on a
spare port (archive_mode forced off), replays the archived WAL to consistency,
and checks the data — then tears it down. Run it after any archive change.
Last verified 2026-06-22: base restored, WAL replayed, `consistent recovery
state reached`, 15 tables present.
