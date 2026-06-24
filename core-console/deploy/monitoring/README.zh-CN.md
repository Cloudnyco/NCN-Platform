# 监控 — Prometheus + Grafana

> [English](README.md) · **简体中文**

一套自托管的 Prometheus + Grafana 栈运行在 **pop-03** 上，抓取 ncn-api 的
`/metrics` 端点。pop-03 充当可观测性主机：它在 ctrl-01 故障时仍可存活，并且已经
运行着 Gatus。所有组件均位于一块独立的辅助磁盘
（`/var/mail/vhosts/ncn-mon/`）上，以避免根盘空间压力，并以非特权用户
`ncnmon` 运行。两个服务都仅绑定 **localhost**，通过 SSH 隧道访问，与 Gatus
（:8080）采用相同模式。

## 布局（位于 pop-03）

```
/var/mail/vhosts/ncn-mon/
  prometheus/            # release binary + prometheus.yml
  prometheus-data/       # TSDB (90d retention)
  grafana/               # release tree + conf/provisioning + dashboards/
  grafana-data/          # grafana.db, logs, plugins
```
发行版 tarball 缓存在 `/var/mail/vhosts/ncn-ha/dl/` 中以便重新安装。

## 抓取路径

Prometheus 抓取 `https://[2001:db8:53::1]/metrics`（ctrl-01 主节点，经由私有
IPv6 骨干网）。ctrl-01 上的 nginx 定义了一个 `location = /metrics`，限制为
`2001:db8:50::/44` 加 localhost（其余 `deny all`），因此只有骨干网可以抓取它，
公网无法访问。TLS 以 IP 形式提供，因此抓取使用 `insecure_skip_verify`（在可信
骨干网上可接受）。

在将 pop-03 提升为主节点的故障切换之后，将 `prometheus.yml` 重新指向新的主节点
（或同时列出两者——热备的 ncn-api 处于停止状态，因此在被提升之前只会显示为不可
达）。

## 访问方式（嵌入控制台——主要方式）

控制台在 **admin.example.com/grafana** 处嵌入 Grafana（导航项 监控 /
Monitoring → `Observability.vue`，以 kiosk 模式将仪表盘以 iframe 形式嵌入）：

```
browser ──> admin.example.com/grafana  (nginx /grafana → ncn_api)
        ──> ncn-api  requireRole("admin")  reverse-proxy            (grafana_proxy.go)
        ──> ctrl-01 127.0.0.1:3001  (ncn-grafana-tunnel.service, ssh -L over the backbone)
        ──> pop-03 127.0.0.1:3000  Grafana (anonymous Viewer, serve_from_sub_path=/grafana)
```
Grafana 从不离开 pop-03 的 localhost；唯一的入口是受管理员会话保护的代理。它以
匿名 **Viewer**（只读）身份运行，使操作员无需二次登录。
`GF_SECURITY_ALLOW_EMBEDDING=true` 以及对 `/grafana/` 的 nginx
`X-Frame-Options: SAMEORIGIN` 覆盖，使同源 iframe 得以渲染。如需进行管理员编辑，
请使用下方的隧道（完整登录）。

## 访问方式（SSH 隧道——直接管理）

```bash
# Grafana
ssh -L 3000:127.0.0.1:3000 root@<pop-03>     # → http://localhost:3000  (admin/admin, change on first login)
# Prometheus (optional, for ad-hoc PromQL)
ssh -L 9090:127.0.0.1:9090 root@<pop-03>     # → http://localhost:9090
```
**NCN Control Plane** 仪表盘（uid `ncn-overview`）在 **NCN** 文件夹中自动预置，
并报告：DB up、复制备库及延迟、fleet up/total、未关闭的 op-failures、WAL 归档失败
及最近一次归档的时长、按严重级别划分的活动告警，以及 AI token 速率。

## 安装 / 重新安装

`install-pop03.sh` 是幂等的。将配置文件准备到 `/tmp/ncn-mon/`，确保发行版
tarball 位于 `/var/mail/vhosts/ncn-ha/dl/`，然后在 pop-03 上运行该脚本。它**未**
接入 `deploy/deploy.sh`；它由人工部署，与 PITR 脚本相同。

## PITR 基础备份新鲜度（textfile）

每周的基础备份（`scripts/pitr/ncn-pitr-basebackup`）会写入
`/var/mail/vhosts/ncn-mon/pitr.prom`，其中包含最近一次成功的时间戳和保留的基础
备份数量。`ncn-textfile.nginx.conf`（安装到 pop-03 的 `/etc/nginx/conf.d/`）将其
在 **127.0.0.1:9102** 上提供（9101 已被 ncn-agent 占用），`pitr` 抓取作业会采集
它。Grafana 据此绘制 “PITR base backup age”，并可在每周作业静默停止时发出告警。

## 暴露的指标

参见 `backend/metrics.go`。Gauges: `ncn_db_up`,
`ncn_replication_streaming_standbys`, `ncn_replication_lag_seconds`,
`ncn_wal_archived_total`, `ncn_wal_archive_failed_total`,
`ncn_wal_last_archive_age_seconds`, `ncn_fleet_nodes_{total,up}`,
`ncn_alerts_active{severity}`, `ncn_op_failures_open`,
`ncn_ai_tokens_total{model,kind}`, `ncn_ai_calls_total{model}`,
`ncn_wal_archived_total`, `ncn_wal_archive_failed_total`,
`ncn_wal_last_archive_age_seconds`, `ncn_anycast_upstreams_up{node}`,
`ncn_anycast_drained{node}`。此外（textfile 作业 `pitr`）：
`ncn_pitr_last_basebackup_timestamp_seconds`, `ncn_pitr_bases_count`。
