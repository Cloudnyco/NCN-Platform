# PITR — 时间点恢复操作手册

> [English](PITR-RESTORE.md) · **简体中文**

本手册涵盖针对逻辑损坏 / 误写入的安全保障机制，它不同于流复制 HA 故障切换。
流复制会将一条错误的 `DELETE` 传播到备库，它防护的是节点丢失。PITR 防护的是
数据库内部的数据丢失，允许将数据恢复到自最早保留的基础备份以来的任意时刻。

## 现有组件

**在 ctrl-01（主库）上：**
- `archive_mode=on`、`archive_command=/usr/local/bin/ncn-wal-archive %p %f`、
  `archive_timeout=300`，因此即使数据库处于空闲状态，任何写入也会在五分钟内
  到达异地归档。
- `ncn-wal-archive` 将每个完成的 WAL 段进行 gzip 压缩并流式传输到 pop-03。
  它在任何失败时都以非零状态退出，因此 PostgreSQL 会保留该段并重试，WAL
  不会被静默丢失。权衡：持续失败的归档器会导致 `pg_wal` 在 ctrl-01 的根卷上
  增长（参见下文“归档器卡住”）。
- `ncn-wal-restore %f %p` 是与之匹配的 restore_command。它从 pop-03 拉回一个
  段并解压，供恢复流程使用。
- `/var/lib/postgresql/.ssh/id_ed25519` 是 postgres 用户专用的归档密钥，与 root
  机群密钥分开，postgres 用户无法读取后者。

**在 pop-03（异地归档，位于次级 `sdb` 卷的
`/var/mail/vhosts/ncn-ha/`）上：**
- `/usr/local/bin/ncn-wal-recv` 是一个 SSH 强制命令（forced-command）接收器。
  归档密钥在 `/root/.ssh/authorized_keys` 中绑定到它
  （`command="...ncn-wal-recv",restrict`），因此该密钥只能在归档目录中执行
  `recv`/`get`/`have`/`list` 段操作，永远无法获得 shell。文件名经过白名单校验，
  拒绝路径穿越和 shell 元字符。
- `wal-archive/wal/` 存放已归档的段（`*.gz`，模式 0640，组可读）。
- `wal-archive/base/base-<ts>/` 存放冻结的基础备份（`base.tar.gz` 加
  `backup_manifest`）。它们由 `ncn-pitr-basebackup.timer`（周日 03:30 UTC）
  每周生成，该计时器运行 `ncn-pitr-basebackup`——即通过骨干网复制连接从
  ctrl-01 主库执行的 `pg_basebackup`。
- 保留策略（`ncn-pitr-basebackup`）：保留最新的两个基础备份；裁剪更旧的基础
  备份，以及任何早于最旧保留基础备份的 START WAL（从其 `backup_label` 读取）
  的 WAL 段。

## RPO / RTO

- 异地副本的 RPO ≤ 5 分钟（archive_timeout），流复制备库为亚秒级。PITR 的价值
  不在于更低的 RPO，而在于能够在错误事务之前停止。
- RTO：一次恢复提取一个基础备份并重放其后的 WAL，通常在数分钟内完成。

## 恢复流程（恢复到某个时间点）

在已安装 PostgreSQL 17 的恢复主机上运行。pop-03 本身是最简单的目标，因为归档
在本地，restore_command 中无需 SSH。在任何其他主机上，复制 postgres 归档密钥和
`ncn-wal-restore`，并使用 SSH 形式。

1. **选定目标。** 选择一个 `recovery_target_time`，通常恰好在错误写入之前，
   例如 `'2026-06-22 11:02:00+00'`。

2. **铺设基础备份。** 选择备份时间早于目标的最新基础备份：
   ```bash
   BASE=/var/mail/vhosts/ncn-ha/wal-archive/base/base-<ts>
   DEST=/var/lib/postgresql/17/restore        # any empty datadir
   install -d -o postgres -g postgres -m 700 "$DEST"
   sudo -u postgres tar -xzf "$BASE/base.tar.gz" -C "$DEST"
   ```
   在 Debian 上，基础 tar 不包含 `postgresql.conf`/`pg_hba.conf`（它们位于
   `/etc/postgresql/...`）；在 `$DEST` 中合成最小化的配置文件（确切模板参见
   `scripts/pitr/ncn-pitr-drill.sh`）。

3. **配置恢复**，在 `$DEST/postgresql.conf`（或 `.auto.conf`）中：
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

4. **启动实例**，使用空闲端口并观察其收敛：
   ```bash
   sudo -u postgres /usr/lib/postgresql/17/bin/pg_ctl -D "$DEST" -l "$DEST/rec.log" start
   grep -iE 'consistent|recovery stopping|promot' "$DEST/rec.log"
   ```
   预期在目标附近出现 `recovery stopping before commit of transaction ...`，
   随后是 `consistent recovery state reached`，然后是提升（promotion）。

5. **验证后再切换。** 在空闲端口上检查恢复出的数据。确认无误后，停止生产实例
   并换入此数据目录，或用 `pg_dump` 导出所需行再重新导入。仅在将成为新主库的
   实例上重新启用 `archive_mode`，并执行一次新的基础备份以重新基线化时间线。

## 监控

- `replmon`（位于 ncn-api）监视 `pg_stat_archiver.failed_count`。计数上升时会向
  错误通道发送 “WAL archiving FAILING”，并在恢复后清除。
- `/metrics` 暴露 `ncn_wal_archived_total`、`ncn_wal_archive_failed_total` 和
  `ncn_wal_last_archive_age_seconds`。

## 归档器卡住（磁盘占满风险）

如果 pop-03 长时间不可达，`archive_command` 会持续失败，PostgreSQL 会保留这些段，
`pg_wal` 会在空闲空间有限的 ctrl-01 根卷上增长。告警会提早触发。如果无法快速
解决且磁盘正在被占满，应急手段是临时将 `archive_command` 指向空间更充裕挂载点
上的本地暂存目录，或作为最后手段设置 `archive_command='/bin/true'` 以排空。
最后手段会破坏 PITR 连续性，因为那些段将丢失，因此在恢复真正的归档后，应立即
执行一次新的基础备份。

## 冒烟测试

pop-03 上的 `ncn-pitr-drill.sh` 将最新基础备份恢复到空闲端口上的临时目录
（强制关闭 archive_mode），将已归档的 WAL 重放至一致状态，检查数据，然后拆除
该实例。任何归档变更后都应运行它。最近一次已验证的运行（2026-06-22）恢复了
基础备份、重放了 WAL、达到 `consistent recovery state reached`，并发现存在
15 张表。
