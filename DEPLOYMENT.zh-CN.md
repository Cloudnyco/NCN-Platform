# 部署

> [English](DEPLOYMENT.md) · **简体中文**

本文档是为网络运行 NCN 的从零开始指南，涵盖从单条命令的演示到生产级高可用（HA）
控制平面的完整范围。四种入口方式的概要见
[`core-console/QUICKSTART.md`](core-console/QUICKSTART.md)；本文档为详细参考。

本仓库中所有与运营方相关的取值均为**占位符**（`example.com`、`AS64500`、
RFC5737/RFC3849 地址、`ctrl-01`/`pop-0N`）。在设置 `NCN_*` 环境变量并填充节点注册表
之前，不会指向任何真实网络。

---

## 目录

- [架构](#架构)
- [选择拓扑](#选择拓扑)
- [前置条件](#前置条件)
- [路径 A — 单主机（演示或小规模生产）](#路径-a--单主机)
- [配置参考](#配置参考)
- [数据库（PostgreSQL）](#数据库postgresql)
- [nginx、TLS 与主机隔离](#nginxtls-与主机隔离)
- [定义机群](#定义机群)
- [添加 PoP（agent）](#添加-pop)
- [Day-2 运维](#day-2-运维)
- [路径 B — HA 控制平面与灾备](#路径-b--ha-控制平面与灾备)
- [Webmail（可选）](#webmail可选)
- [可观测性（可选）](#可观测性可选)
- [CLI 工具](#cli-工具)
- [安全检查清单](#安全检查清单)
- [故障排查](#故障排查)

---

## 架构

```
                     Cloudflare / 边缘 (TLS)
                                │
       example.com   admin.example.com   mail.example.com
            │              │                   │
            ▼              ▼                   ▼
   ┌──────────────── ctrl-01 (control node) ────────────┐   ┌── pop-03 ──┐
   │  nginx → ncn-api (:9000, Go, socket-activated)      │   │  webmail   │
   │  PostgreSQL (primary, optional)                     │   │  observ.   │
   │  Vue SPA: admin console + public site + wiki        │   │  PG standby│
   └──────────────────────┬──────────────────────────────┘  └────────────┘
                          │ telemetry (SSH or HTTPS+HMAC)
         ┌──────────┬─────┼─────┬───────────┐
         ▼          ▼     ▼     ▼           ▼
      pop-01     pop-02  pop-04  pop-0N …  (PoPs, each runs BIRD)
```

- **ncn-api** 是控制节点上的单个 Go 二进制文件。它是机群的*唯一*写入方；bot、MCP
  服务器和 CLI 均为客户端。
- 当设置了 `NCN_DATABASE_URL` 时，**状态**保存在 PostgreSQL 中，否则保存在
  `/etc/ncn-core-console` 下的 JSON 文件中（因此在有无数据库的情况下均可运行）。
- **单个 SPA 构建**为三个主机提供服务；路由器与 nginx 强制执行主机隔离（公开主机
  不暴露 `/admin` API）。
- 任何触及生产路由器的操作在设计上均为确认门控、可审计且可回退。

---

## 选择拓扑

| 目标 | 使用 | 主机数 | Postgres |
|---|---|---|---|
| 评估 UI/API | `docker compose` 或 `scripts/dev.sh` | 0（本地） | 内置 / 无 |
| 单主机部署 | 在单主机上运行 `bootstrap.sh` | 1 | 推荐 |
| 带故障转移的生产环境 | 路径 B（HA） | 2+ | 必需（复制） |

部署可以从单主机开始，之后再扩展为 HA；各步骤是叠加式的。

---

## 前置条件

- **控制主机：** Linux（systemd），root 访问权限。1 vCPU / 1 GB 即可起步。
- **构建工具**（仅在执行构建处需要）：Go ≥ 1.22，Node ≥ 18。`bootstrap.sh` 会安装
  这些工具；`deploy.sh` 在工作站上构建。
- **DNS：** 公开、管理及（可选）邮件主机名指向该主机或边缘。
- **TLS：** 反向代理 / CDN（Cloudflare 或同类）或本地证书（见下文）。
- 多主机场景：从控制主机到每个 PoP 的 SSH 连接，以及一个可复制的 Postgres 实例。

---

## 路径 A — 单主机

此方式在一台全新主机上启动整个控制台。以 root 身份在检出目录中运行：

```bash
git clone https://github.com/<you>/<repo>.git && cd <repo>
sudo core-console/deploy/bootstrap.sh
```

`bootstrap.sh` 是幂等的，并会：

1. 安装依赖（go / node / nginx），
2. 构建 API（`ncn-api`）和 SPA（`dist/`），
3. 安装 systemd 单元 —— `ncn-api.socket` + `ncn-api.service`
   （在 `127.0.0.1:9000` 上 socket-activated），
4. 配置 nginx（`deploy/nginx-ncn-core-console.conf`），
5. 启动服务；**迁移在首次启动时自动应用**。

此时控制台已运行且为**文件后端**（无数据库，空机群）；可通过恢复引导登录。要为特定
网络进行配置，请完成接下来的两节（配置 + 机群），然后运行 `systemctl restart ncn-api`。

> `bootstrap.sh` 不生成密钥。任何未提供的项都会被优雅地禁用，直到将其添加为止。

---

## 配置参考

运行时配置保存在控制节点的 **`/etc/ncn-core-console/`** 中，并由 `ncn-api.service`
通过 `EnvironmentFile` 加载。从
[`.env.example`](.env.example) 和
[`core-console/deploy/oauth.env.example`](core-console/deploy/oauth.env.example)
复制所需的配置块。

使用 `systemctl restart ncn-api` 应用更改。

### 核心环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `NCN_ASN` | `64500` | AS 号（仅数字）。 |
| `NCN_OUR_PREFIXES` | `2001:db8::/32` | 地址空间（逗号分隔的 CIDR）。 |
| `NCN_LOCAL_NODE_ID` | `ctrl-01` | *本*（控制）主机的节点 id。 |
| `NCN_RPKI_ROV_NODE` | `ctrl-01` | 其 BIRD 运行 RPKI ROV 的节点。 |
| `NCN_PUBLIC_HOST` / `NCN_ADMIN_HOST` / `NCN_DOMAIN` | `example.com` 系列 | 公开 / 管理 / 邮件域名。 |
| `NCN_BRAND_NAME` | `Acme Net` | 面向用户的品牌字符串。 |
| `NCN_OAUTH_REDIRECT_BASE` | `https://admin.example.com` | OAuth 重定向 URI 的基础地址。 |
| `NCN_DATABASE_URL` | *（未设置 → 文件后端）* | 使用 PostgreSQL 时填 `postgres://…`。 |
| `NCN_DEPLOY_HOST` | `deploy-host` | `deploy.sh` 推送到的 SSH 目标。 |
| `NCN_PROBE_TARGETS` | *（无）* | 额外的 ICMP 探测目标。 |
| `NCN_RPKI_REFRESH` | `24h` | RPKI ROA 轮询间隔。 |
| `NCN_FLOW_FILE` | `/var/log/ncn-flows/flows.jsonl` | sFlow/NetFlow 采集器输出。 |
| `NCN_METRICS_TOKEN` | *（无）* | `/metrics` 的 Bearer（Grafana 抓取）。 |
| `NCN_ALERT_WEBHOOK` | *（无）* | 通用告警 webhook。 |

### 可选功能环境变量（未设置时均优雅禁用）

- **Telegram bot：** `NCN_TG_BOT_TOKEN`、`NCN_TG_BOT_USERNAME`、`NCN_TG_CHAT_ID`、
  `NCN_TG_ERROR_CHANNEL`（在 `tg.env` 中）。
- **OAuth 登录：** GitHub / Telegram 的 client id + secret（在 `oauth.env` 中）。将每个
  应用的回调注册为
  `https://admin.example.com/api/v1/auth/oauth/<provider>/callback`。
- **AI 助手：** `NCN_DEEPSEEK_API_KEY`（+ `NCN_DEEPSEEK_MODEL`）。

### 密钥 / 密钥文件（权限 0600，root 所有，**切勿提交**）

位于 `/etc/ncn-core-console/`：

| 文件 | 用途 |
|---|---|
| `oauth.env`、`tg.env` | 上述 env 文件 |
| `session.key` | HMAC 会话 cookie 密钥（缺失时自动生成） |
| `fleet-key`（+ `fleet-known-hosts`） | 控制节点用于连接 PoP 的 SSH 密钥 |
| `turnstile.secret` | Cloudflare Turnstile 密钥（公开表单的 bot 防护） |
| `agent-ca/`、`agent-keys/` | `ncn-agent` 的 mTLS CA + 密钥（用于 REST 遥测） |

`.gitignore` 已排除 `*.env`、`*.key`、`*.pem`、`*.age`。应将任何已暴露的密钥视为已泄露
并轮换之 —— 参见 [`SECURITY.md`](SECURITY.md)。

---

## 数据库（PostgreSQL）

可选 —— 在未设置 `NCN_DATABASE_URL` 时，API 为文件后端，适用于演示或单主机评估。对于
生产数据，请使用 Postgres：

```bash
sudo -u postgres createuser ncn --pwprompt
sudo -u postgres createdb -O ncn ncn
# then in /etc/ncn-core-console/oauth.env:
#   NCN_DATABASE_URL=postgres://ncn:YOURPASS@localhost:5432/ncn?sslmode=disable
sudo systemctl restart ncn-api      # embedded migrations auto-apply on start
```

迁移嵌入在二进制文件中（`//go:embed migrations/*.sql`），并在启动时运行；无需单独的迁移
步骤。各存储对空数据库具有容错性，因此在文件后端与数据库后端之间切换是安全的。

---

## nginx、TLS 与主机隔离

`deploy/nginx-ncn-core-console.conf` 从单个 SPA `dist/` 为全部三个虚拟主机（公开 /
管理 / wiki）提供服务，并将 `/api` 代理到 `127.0.0.1:9000`。公开主机仅**允许列出**公开
API 路径；`/api/` 下的其他所有内容在公开主机上返回 404（纵深防御，同时也在 SPA 路由器
中强制执行）。

- **位于 Cloudflare / CDN 之后：** 将主机名指向它（proxied、SSL Strict）并由其终止 TLS；
  代码段 `deploy/snippets/cloudflare-real-ip.conf` 会还原真实客户端 IP。
- **直接 TLS：** 安装证书并在该 conf 中设置 `ssl_certificate*`；使用
  `deploy/nginx-ncn-acme-bootstrap.conf` 进行 HTTP-01 ACME 引导。

将 `server_name` 编辑为对应的主机名，然后运行 `deploy.sh nginx`（或复制该文件并运行
`nginx -t && systemctl reload nginx`）。

---

## 定义机群

节点保存在**运行时注册表**中（无需修改代码）。可在管理控制台（**Onboarding** /
**Servers**）中管理它们，或在 `/etc/ncn-core-console/` 下填充 `nodes.json`。每个节点具有
`id`（例如 `pop-01`）、标签、区域以及探测锚点（v4/v6）。控制节点会就负载、BGP 会话
（`birdc`）、WireGuard 和正常运行时间抓取每个节点 —— 默认通过 SSH，或在该节点运行
`ncn-agent` 时通过 HTTPS+HMAC。

---

## 添加 PoP

在控制节点上，端到端地配置一个节点：

```bash
core-console/scripts/agent-node-provision.sh <node-id> <node-address>
```

这会推送机群 SSH 密钥（或在 REST 遥测情况下，通过 `scripts/agent-ca-bootstrap.sh` 推送
agent CA 包），注册该节点，并验证一次抓取。每个 PoP 预期运行 **BIRD**（BGP / anycast）；
控制台读取它，但不安装它。

---

## Day-2 运维

从工作站运行（本地构建，通过 SSH 推送到 `NCN_DEPLOY_HOST`）：

```bash
deploy/deploy.sh all        # backend + frontend + smoke (default)
deploy/deploy.sh backend    # Go: GATED on `go vet` + `go test`, atomic zero-downtime swap
deploy/deploy.sh frontend   # SPA: i18n lint + typecheck + vite build + ship
deploy/deploy.sh nginx      # ship + reload nginx
deploy/deploy.sh health     # full-stack health check (non-zero if red)
deploy/deploy.sh rollback   # restore the previous ncn-api binary
```

- 除非 vet 和测试在目标上通过，否则后端部署将拒绝发布；先前的二进制文件会被备份，以便
  立即 `rollback`。
- `ncn-api` 为 socket-activated，因此切换是零停机的。
- 每日状态灾备快照和审计日志异地 rsync 通过 systemd 定时器运行
  （`deploy/ncn-state-backup.*`、`deploy/ncn-audit-rsync.*`）。

---

## 路径 B — HA 控制平面与灾备

添加第二台主机（本例中为 `pop-04`），以便在控制节点发生故障时仍可存续。

1. **PostgreSQL 流复制** —— 主库在 `ctrl-01` 上，热备在 `pop-04` 上（亚秒级 RPO）。标准的
   `primary_conninfo` + 复制槽。
2. **温备 `ncn-api`** 位于 `pop-04`（相同构建，指向本地副本），在提升之前保持停止状态。
3. **`ncn-lb` 故障转移控制器**（`lb/`、`deploy/ncn-lb.service`）位于 `pop-04`：它对主库进行
   健康检查，并在持续失败时提升 PG 副本、启动备用 API 并重新指向 DNS。配置 `lb/config.json`
   和 `lb/cf.env`（Cloudflare token + record id —— 参见 `lb/cf.env.example`）。
   - **以 `observe` 模式发布**（记录它*将要*执行的操作）。仅在 `lb/failover.sh` 经过测试后
     再启用它。**没有自动故障回切**（防抖动）—— 回切是一项手动且经过审慎判断的步骤。
4. **PITR** —— WAL 归档 + 每周基础备份 + 一次恢复演练，位于 `scripts/pitr/`
   （`ncn-wal-archive`、`ncn-pitr-basebackup`、`ncn-pitr-drill.sh`）。这可防范复制本会传播的
   逻辑损坏。
5. **状态快照** —— `scripts/state-backup.sh`（本地 + 异地）；恢复时在启动前将快照放入
   `/etc/ncn-core-console`。

> 单看守者注意事项：`ncn-lb` 是单个控制器 —— 它消除了控制节点的单点故障，但其自身并非
> 高可用。应将其职责范围限定为"在明确故障时执行提升"。

---

## Webmail（可选）

`webmail/` 是一个自包含的邮件 UI（Go + Vue），位于 Postfix/Dovecot 之前，在
`mail.example.com` 提供服务。使用 `webmail/deploy/deploy.sh`（或顶层的 `deploy-all.sh`，
它先部署 webmail 再部署控制台）进行部署。它桥接到控制台以实现 SSO 和角色邮箱恢复。
MTA/IMAP 层（Postfix/Dovecot/rspamd）需单独提供；webmail 是前端。

---

## 可观测性（可选）

`deploy/monitoring/` 提供 Prometheus + Grafana + Gatus 单元（适用于诸如 `pop-03` 的可观测性
节点）。API 暴露一个手写的 `/metrics` 端点（不含密钥；将其限制在骨干网内，或用
`NCN_METRICS_TOKEN` 进行门控）。Grafana 通过一个管理门控的反向代理嵌入控制台。
`deploy/sflow/` 部署一个 sFlow/NetFlow 采集器（goflow2 + softflowd），将数据写入
`NCN_FLOW_FILE`。

---

## CLI 工具

构建并安装运维 CLI：

```bash
cd cli && ./install.sh            # builds (or uses prebuilt) + installs to /usr/local/bin
#         ./install.sh --build    # force a fresh compile
#  PREFIX=$HOME/.local/bin ./install.sh   # no-sudo
```

- **`ncn-login`** —— 进行身份验证：直接运行会打开浏览器登录，并将 token 交回终端；
  `--token` 用于粘贴一个 token；`--user` 执行 SSH 密钥签名登录。将 `NCN_HOST` 设置为管理
  主机。
- **`ncn-debug`** —— 通过 API 执行只读操作：`fleet`、`node`、`bgp`、`alerts`、`rpki`、
  `oncall` 等，或交互式 `console`。`ncn-debug status` 为公开（无需 token）。

---

## 安全检查清单

- [ ] 所有密钥位于 `/etc/<service>/` 的 env/key 文件中，权限 0600 —— **切勿**置于 git 中。
- [ ] `session.key`、`fleet-key`、`turnstile.secret`、OAuth 密钥以及 `NCN_DATABASE_URL`
      密码均已设置且对本部署唯一。
- [ ] 已终止 TLS（CDN 或本地证书）；`admin` 主机不可被公开枚举。
- [ ] `/metrics` 已限制在骨干网内或经 `NCN_METRICS_TOKEN` 门控。
- [ ] 机群 SSH 密钥为专用密钥（非个人密钥）；远程命令执行通过 agent 流程保持人工审批。
- [ ] 已审阅 `SECURITY.md` 中列出的按设计敏感的模块。
- [ ] 若任何密钥曾被暴露（例如被提交），已将其**轮换**。

---

## 故障排查

| 现象 | 检查 |
|---|---|
| API 无法启动 | `journalctl -u ncn-api -e`；`NCN_DATABASE_URL` 有误 → 取消设置以回退到文件。 |
| 公开主机上 `/api/...` 返回 404 | 符合预期 —— 管理 API 已做主机隔离。请使用管理主机。 |
| 管理页面空白 | 硬刷新（哈希包过期）；确认 `dist/` 已发布；检查浏览器控制台。 |
| 节点显示离线 | 控制节点到该节点的 SSH/agent 可达性；`fleet-key` 已加载；PoP 上存在 `birdc`。 |
| OAuth 按钮报错 | 该提供方的 client id/secret 未设置，或回调 URL 不匹配（必须与 `NCN_OAUTH_REDIRECT_BASE` + 路径完全一致）。 |
| `deploy.sh backend` 中止 | vet/test 在目标上失败 —— 该门控正在按预期工作；修复后重试。 |

功能性问题请提交 issue；漏洞请参见 `SECURITY.md`。
