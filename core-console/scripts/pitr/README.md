# PITR scripts

> **English** · [简体中文](README.zh-CN.md)

Hand-deployed artifacts for point-in-time recovery (PITR). The full runbook is
documented in `docs/PITR-RESTORE.md`. These artifacts are not shipped by
`deploy/deploy.sh`; they are installed manually on the nodes listed below.

| File | Host | Installed path |
|---|---|---|
| `ncn-wal-archive` | ctrl-01 (primary) | `/usr/local/bin/ncn-wal-archive` (set as `archive_command`) |
| `ncn-wal-restore` | recovery host | `/usr/local/bin/ncn-wal-restore` (the `restore_command`) |
| `ncn-wal-recv` | pop-03 (archive) | `/usr/local/bin/ncn-wal-recv` (SSH forced command) |
| `ncn-pitr-basebackup` | pop-03 | `/usr/local/bin/ncn-pitr-basebackup` |
| `ncn-pitr-basebackup.{service,timer}` | pop-03 | `/etc/systemd/system/` |
| `ncn-pitr-drill.sh` | pop-03 | run ad-hoc to verify restorability |

**Primary-side PostgreSQL settings** (applied via `ALTER SYSTEM`; `archive_mode`
requires a restart to take effect):
```
archive_mode = on
archive_command = '/usr/local/bin/ncn-wal-archive %p %f'
archive_timeout = 300
```

**Archive key**: the `postgres` account on ctrl-01 holds a dedicated
`~/.ssh/id_ed25519`. Its public key is pinned on pop-03 in
`root/.ssh/authorized_keys` as
`command="/usr/local/bin/ncn-wal-recv",restrict <pubkey>`.
