# pop-0N 上的 Gatus 在线监控（已部署）

> [English](gatus-pop03.md) · **简体中文**

Gatus 提供一套以配置即代码（config-as-code）方式运行的在线监控工具，接管原先由自研 bot 负责的节点 up/down 检测与公网服务探测。

此前对 Uptime Kuma 的评估已停止：Kuma 2.x 安装链路较脆，且需要交互式建库与管理员设置。Gatus 以单一二进制配合 YAML 驱动，更可靠，并可完全自动化。

## 部署形态

- **主机**：pop-0N（`198.51.100.3`）。以原生二进制运行，而非 Docker，因为该节点同时充当 BGP 路由器，Docker 会修改 `iptables`/`ip_forward`。
- **二进制**：Gatus v5.36.0。Gatus 不发布预编译二进制，因此在构建主机上通过 `go install github.com/TwiN/gatus/v5@v5.36.0` 编出 linux-amd64 二进制，再复制到 pop-0N 的 `/usr/local/bin/gatus`。
- **配置**：`/etc/gatus/config.yaml`（在仓库中以 `monitoring/gatus-config.yaml` 形式版本化）。SQLite 数据存储位于 `/var/lib/monitoring/gatus/`。
- **服务**：systemd 单元 `gatus.service`，绑定到 `127.0.0.1:8080`（外部访问经 SSH 隧道）。Telegram 凭据通过 `EnvironmentFile=/etc/gatus/gatus.env`（`TG_BOT_TOKEN` / `TG_CHAT_ID`）提供。配置中以 `${...}` 占位引用这些值，因此密钥不会提交入库。
- **监控端点**：若干公网 HTTP 端点（site/health/status/looking-glass/admin，断言 `[STATUS]==200`），以及逐 PoP 的 IPv6 anchor ICMP 检测（`2001:db8:R::N`，断言 `[CONNECTED]==true`）。
- **抗抖动**：`default-alert` 要求连续 3 次失败才报警、连续 2 次成功才恢复，并启用 `send-on-resolved`。

## 为何只探测 IPv6 anchor，而不探测 IPv4 单播

从单一 mesh 内某个节点的视角看，PoP 间的 IPv4 跨运营商可达性并不可靠（某个 PoP 在某地区可能 IPv4 不可达，但其 IPv6 明显在线），这会产生误报。IPv6 anchor 走骨干网，提供可靠的逐 PoP 信号（与代码中的 `ncnProbeV6` 逻辑一致）。公网 IPv4 可达性交由外部多地区 SaaS 层负责（参见 `uptime-targets.md` 第一节）。

## 访问

- **公网（推荐）**：https://monitor.example.com，经 Cloudflare Tunnel（pop-0N 上的 `cloudflared-gatus.service`，为出站连接，不开放任何入站端口）转发到 Gatus 的 8080 端口。鉴权由 Cloudflare Access 实施（一个 Zero Trust 组织，其策略放行运营人员，例如通过邮箱 OTP 放行 `admin@example.com`）。
  - 隧道连接 token 存放在 pop-0N 的 `/etc/cloudflared/tunnel.env`（权限 `0600`）。
  - DNS：`monitor` CNAME 指向 `<tunnel-id>.cfargotunnel.com`（proxied）。
- **应急**：`ssh -L 8080:127.0.0.1:8080 root@198.51.100.3`，然后打开 http://localhost:8080。

## 修改配置

```
# 编辑仓库中的 monitoring/gatus-config.yaml
# scp 到 pop-0N 的 /etc/gatus/config.yaml
# systemctl restart gatus
```

## 后续

自研 bot 的节点 up/down Telegram 推送（`node-unreachable` / `probe-down`）在 Gatus 接管后已于 `alerts.go` 中静音。其余关键告警（CPU/内存/磁盘、`bgp-peer-down`、`bird-unreachable`）保留。将按 `uptime-targets.md` 第一节增加一个独立的外部兜底层（待办）。
