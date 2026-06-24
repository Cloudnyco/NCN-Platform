# PITR 脚本

> [English](README.md) · **简体中文**

用于时间点恢复（PITR）的手动部署产物。完整的操作手册见
`docs/PITR-RESTORE.md`。这些产物不由 `deploy/deploy.sh` 分发，需在下列节点上手动安装。

| 文件 | 主机 | 安装路径 |
|---|---|---|
| `ncn-wal-archive` | ctrl-01（主库） | `/usr/local/bin/ncn-wal-archive`（设为 `archive_command`） |
| `ncn-wal-restore` | 恢复主机 | `/usr/local/bin/ncn-wal-restore`（即 `restore_command`） |
| `ncn-wal-recv` | pop-03（归档） | `/usr/local/bin/ncn-wal-recv`（SSH 强制命令） |
| `ncn-pitr-basebackup` | pop-03 | `/usr/local/bin/ncn-pitr-basebackup` |
| `ncn-pitr-basebackup.{service,timer}` | pop-03 | `/etc/systemd/system/` |
| `ncn-pitr-drill.sh` | pop-03 | 临时运行以验证可恢复性 |

**主库侧 PostgreSQL 配置**（通过 `ALTER SYSTEM` 应用；`archive_mode` 需重启方可生效）：
```
archive_mode = on
archive_command = '/usr/local/bin/ncn-wal-archive %p %f'
archive_timeout = 300
```

**归档密钥**：ctrl-01 上的 `postgres` 账户持有专用的 `~/.ssh/id_ed25519`。其公钥
以 `command="/usr/local/bin/ncn-wal-recv",restrict <pubkey>` 的形式固定写入
pop-03 的 `root/.ssh/authorized_keys`。
