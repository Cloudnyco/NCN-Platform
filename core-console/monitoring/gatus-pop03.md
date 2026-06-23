# Gatus uptime monitoring on pop-03 (deployed)

正经 uptime tracker(config-as-code),替自研 bot 接管节点 up/down + 公网服务探测。
(替换了早期短暂试过的 Uptime Kuma —— Kuma 2.x 安装链路较脆、需交互建库/管理员;Gatus 单二进制 + YAML 更可靠、可完全自动化。)

## 部署形态
- **主机**:pop-03(`198.51.100.3`)。原生二进制(**非 Docker** —— pop-03 是 BGP 路由器,避免 Docker 改 iptables/ip_forward)。
- **二进制**:Gatus v5.36.0。Gatus 不发布预编译二进制,故在 **tyo 上 `go install github.com/TwiN/gatus/v5@v5.36.0`** 编出 linux-amd64,scp 到 pop-03 `/usr/local/bin/gatus`。
- **配置**:`/etc/gatus/config.yaml`(= 仓库 `monitoring/gatus-config.yaml`,版本化)。data sqlite 在 `/var/mail/vhosts/monitoring/gatus/`(40G sdb)。
- **服务**:systemd `gatus.service`,绑 **127.0.0.1:8080**(外部走 SSH 隧道)。Telegram 密钥经 `EnvironmentFile=/etc/gatus/gatus.env`(`TG_BOT_TOKEN`/`TG_CHAT_ID`,取自 `tg.env`;配置里用 `${...}` 占位,不入库)。
- **监控(13)**:5 公网 HTTP(site/health/status/LG/admin,`[STATUS]==200`)+ 8 个 PoP **v6 anchor** ICMP(`2001:db8:R::N`,`[CONNECTED]==true`)。
- **抗抖动**:`default-alert` = 连续 3 次失败才报 / 2 次成功才恢复 / `send-on-resolved`。

## 为什么只探 v6 anchor、不探 v4 单播
从 pop-03 这单一 mesh 内视角,PoP 间 **v4 跨运营商可达性不可靠**(如 pop-08 v4 从 HK 不通,但它 v6 明明在线)——会误报。v6 anchor 走 NCN 骨干,是可靠的逐 PoP 信号(与代码里 `ncnProbeV6` 一致)。公网 v4 可达性留给**外部多地 SaaS 层**(见 `uptime-targets.md` 第一节)。

## 访问
- **公网(推荐)**:https://monitor.example.com —— 经 **Cloudflare Tunnel**(pop-03 上 `cloudflared-gatus.service`,出站连接,**不开任何入站端口**)→ Gatus :8080。由 **Cloudflare Access** 鉴权(Zero Trust org `your-org`,策略 "NCN operators" 仅放行 admin@example.com,邮箱 OTP)。
  - tunnel 名 `ncn-gatus-pop03`(id `ce17e9d0-…`),连接 token 在 pop-03 `/etc/cloudflared/tunnel.env`(0600)。
  - DNS:`monitor` CNAME → `<tunnel-id>.cfargotunnel.com`(proxied)。
- **应急**:`ssh -L 8080:127.0.0.1:8080 root@198.51.100.3` → http://localhost:8080。

## 改配置
```
# 编辑仓库 monitoring/gatus-config.yaml → scp 到 pop-03 /etc/gatus/config.yaml → systemctl restart gatus
```

## 后续
自研 bot 的 node up/down(`node-unreachable`/`probe-down`)TG 推送已在 `alerts.go` 静音(Kuma/Gatus 接管);其余 crit(cpu/mem/disk、bgp-peer-down、bird-unreachable)保留。外部独立兜底层按 `uptime-targets.md` 第一节加(待办)。
