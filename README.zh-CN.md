# NCN — 自托管网络运维平台

> [English](README.md) · **简体中文**

NCN 是面向多 PoP 任播(anycast)网络的运维平台,提供 BGP/BIRD 机群遥测、基于
规则与异常的告警、RPKI 监控、Peering 申请受理、容量 / SLA / 流量分析、DDoS
缓解、值班排班、Webmail、带主备故障切换的负载均衡、Wiki,以及带 SSO 的身份认证。

所有与运营方相关的取值均通过环境变量(`NCN_*`,见 [`.env.example`](.env.example))
和运行时节点注册表提供。仓库仅含占位值 —— `AS64500`、`example.com`、
RFC5737/RFC3849 地址,以及 `ctrl-01` / `pop-0N` 节点名 —— 不引用任何真实网络。

部署文档见 [`core-console/QUICKSTART.md`](core-console/QUICKSTART.md)(四种入口:
本地开发、单命令部署、全新主机 / 灾备引导、Docker Compose)与
[`DEPLOYMENT.md`](DEPLOYMENT.md)(从零到高可用控制面)。

**许可证:** [Apache 2.0](LICENSE) · **贡献指南:**
[`CONTRIBUTING.md`](CONTRIBUTING.md) · **安全:** [`SECURITY.md`](SECURITY.md)

---

## 目录

- [架构](#架构)
- [仓库结构](#仓库结构)
- [角色与拓扑](#角色与拓扑)
- [core-console](#core-console)
  - [后端 — `ncn-api`](#后端--ncn-api)
  - [前端 — SPA](#前端--spa)
  - [子组件 — agent / lb / mcp](#子组件agent--lb--mcp)
- [功能板块](#功能板块)
- [技术栈](#技术栈)
- [构建与运行](#构建与运行)
- [部署](#部署)
- [配置](#配置)
- [安全模型](#安全模型)
- [文档](#文档)

---

## 架构

```
                        边缘 / CDN(TLS)
                                │
        admin.example.com   example.com / wiki.example.com   mail.example.com
                │                    │                          │
                ▼                    ▼                          ▼
        ┌──────────── 控制节点 ───────────────────────────┐  ┌─ 服务节点 ─┐
        │  nginx → ncn-api (:9000, Go)                    │  │  Webmail    │
        │  PostgreSQL(主库)                              │  │  观测        │
        │  Vue SPA(管理控制台 + 公开站点 + Wiki)         │  │  PG 备库 …  │
        └───────────────────────┬─────────────────────────┘  └─────────────┘
                                 │ 遥测(SSH / HTTPS)
            ┌──────────┬─────────┼─────────┬───────────┐
            ▼          ▼         ▼         ▼           ▼
          PoP        PoP       PoP       PoP    …   (任意数量的 PoP)

   每个 PoP 运行 BIRD(BGP、任播),并可选运行 ncn-agent 提供遥测。
   控制 / 观测 / HA 备机 / RPKI / 边缘等角色可自由组合,既可共置一台,
   也可分散部署。骨干为 PoP 间的 IPv6 mesh。
```

控制面通过执行真实探针(`uptime`、`/proc/loadavg`、`birdc`、`wg`)读取实时
基础设施,不含任何 mock 数据。对机群的所有写操作均经由唯一执行器(`ncn-api`),
由其强制鉴权,并对每一次动作记录审计。

---

## 仓库结构

| 路径 | 说明 |
|---|---|
| [`core-console/`](core-console/) | 平台主体:`ncn-api`(Go 后端)与 Vue 3 SPA。 |
| `core-console/agent/` | `ncn-agent` —— 每 PoP 的遥测代理(HTTPS + HMAC),SSH 轮询的替代方案。 |
| `core-console/lb/` | `ncn-lb` —— 负载均衡:带健康检查的源站池,主备故障切换。 |
| `core-console/mcp/` | `ncn-mcp` —— 暴露控制台运维工具的 MCP 服务器。 |
| [`webmail/`](webmail/) | `ncn-mail` —— 服务 `mail.example.com` 的独立 Webmail。 |
| `cli/ncn-debug/` | 基于控制台 REST API 的只读运维 CLI(单个静态 Go 二进制)。 |
| `cli/ncn-login/` | CLI 的运维者认证辅助工具。 |
| `deploy-all.sh` | 单命令部署整套栈(先 webmail,后 core-console)。 |
| `scripts/` | 工作区级辅助脚本(备份/恢复、PITR、provisioning)。 |

---

## 角色与拓扑

平台为一组可组合的角色,映射到任意数量的主机 —— 从单台到多个 PoP,不假设
固定节点数。

| 角色 | 组件 | 数量 |
|---|---|---|
| **控制** | `ncn-api`、PostgreSQL 主库、nginx(控制台 / 公开 / Wiki vhost) | 恰好一个;机群唯一写入者 |
| **边缘 PoP** | BIRD(BGP / 任播),可选 `ncn-agent` | 任意数量;由控制面读取 |
| **观测** | Prometheus、Grafana、Gatus | 可选;任意主机 |
| **HA 备机** | PostgreSQL 流复制备库、温备 `ncn-api`、`ncn-lb` 故障切换 | 可选;见 [`DEPLOYMENT.md`](DEPLOYMENT.md) |
| **RPKI** | Krill CA(发布 ROA)、Routinator(校验) | 可选 |
| **Webmail** | `ncn-mail`、Postfix/Dovecot | 可选 |

最小部署可将各角色合并到一台主机,规模部署可分散到专用主机;边缘 PoP 横向
扩展。公开端点经外部边缘 / CDN 以 TLS 提供。`ctrl-01`、`pop-01` 等节点标识为
示例占位,由运营方在节点注册表中指定。

---

## core-console

### 后端 — `ncn-api`

Go `net/http` 服务(`package main`),监听 `:9000`,由 systemd socket 激活。
状态经 `pgx`(`database/sql`)存于 PostgreSQL,使用内嵌迁移
(`//go:embed migrations/*.sql`)。每个存储均容忍无数据库,在未配置数据库时
回退到 JSON 文件,故二进制在有无 PostgreSQL 时均可运行。

各项能力大致按「一个文件一个关注点」组织于 `core-console/backend/`:

- **机群与节点** — `fleet.go`、`noderegistry.go`、`nodes_api.go`、`nodes_onboard.go`、`heartbeat.go`、`bird_scrape.go`
- **任播** — `anycast.go`(将某 PoP 从任播中 drain / undrain)
- **HA / 灾备 / 复制** — `replmon.go`(及 `lb/`、PITR 脚本)
- **观测** — `metrics.go`、`alertmetrics.go`、`grafana_proxy.go`
- **告警** — `alerts.go`、`alertrules.go`、`alertrules_api.go`、`alertanomaly.go`
- **RPKI** — `rpki.go`(ROA 有效性与 ROV 监控)
- **鉴权与访问** — `auth.go`、`auth_apitoken.go`、`auth_ssh.go`、`auth_sso.go`、`oauth.go`、`oauth_telegram.go`、`passkey.go`、`idp_provider.go`(控制台作为 OAuth2 IdP)、`recover_bootstrap.go`
- **Telegram 机器人** — `bot_tg.go`、`bot_manage.go`、`bot_netadmin.go`、`bot_opfail.go`、`bot_ai.go`、`bot_agent_tg.go`、`bot_drill.go`、`notify_tg.go`
- **AI 助手** — `deepseek.go`、`agent.go`、`agent_tools.go`、`ai_usage.go`、`ai_history_api.go`、`model_config.go`
- **Wiki** — `wikistore.go`、`wiki_api.go`
- **面向成员** — `peering_apply.go`、`peeringdb.go`、`incidents.go`、`visitor.go`、`billing.go`、`invite.go`
- **邮件桥接** — `mail_bridge.go`、`mail_forgot_bridge.go`、`mail_role_recover.go`(控制台 ↔ Webmail)
- **运维基础设施** — `opfailures.go`、`audit.go`、`ratelimit.go`、`turnstile.go`、`term.go`、`admincli.go`、`db.go`、`fx.go`

### 前端 — SPA

Vue 3 + Vite 单页应用(TypeScript、Tailwind、Pinia、Vue Router、`vue-i18n`)。
一次构建服务三个站点,`App.vue` 按路由选择布局:

- **管理主机** —— 运维控制台(`AdminLayout`):Dashboard、Fleet、Servers、
  Alerts、Alert Rules、Observability、Performance、Connectivity、Bird、
  Security、Audit、Billing、Assistant、Wiki、Onboarding。
- **公开主机** —— 公开站点(`PublicLayout`):Landing、Looking Glass、Status、
  Peering 信息与申请、法律页,以及公开 Wiki(`/docs`)。
- **Wiki 主机** —— 公开 Wiki(同一份 SPA 构建)。

本地化提供 `en` / `zh-CN` / `zh-TW`;构建前的 `lint:i18n` 步骤会在任一语言缺
key 时使构建失败。`marked` 与 `DOMPurify` 渲染 Wiki Markdown;`@xterm/xterm`
提供控制台内置终端。

### 子组件(agent / lb / mcp)

- **`ncn-agent`**(`agent/`)—— 运行于 PoP(`:9101`,HTTPS + HMAC bearer),返回
  与 `fleet.go` 经 SSH 采集相同的遥测。按节点的 `Transport` 在 `ssh`(默认)与
  `rest`(带 SSH 回退)间选择,支持增量灰度。
- **`ncn-lb`**(`lb/`)—— 带健康检查的源站池,自动主备切换(提升 PostgreSQL
  备库、启动温备 `ncn-api`、重指 DNS)。默认以 `observe` 模式发布(仅记录拟执行
  动作);arm 为切换脚本验证通过后的明确步骤。无自动 fail-back,以避免抖动。
- **`ncn-mcp`**(`mcp/`)—— 一个 Model Context Protocol 代理,暴露控制台内 AI
  agent 所用的同一批运维工具。控制台后端仍为唯一执行器,并强制执行全部安全规则。

---

## 功能板块

| 板块 | 概述 |
|---|---|
| **机群遥测** | 每 PoP 指标(负载、经 `birdc` 的 BGP 会话、WireGuard、uptime);带上线/下线生命周期的节点注册表。 |
| **任播运维** | 需审批地将某 PoP 从任播中 drain/undrain(`birdc disable upstream_*`);拒绝 drain 最后一个或关键节点。 |
| **HA 与灾备** | PostgreSQL 流复制(主库 → 备库,亚秒级 RPO);经 WAL 归档、每周基础备份与恢复演练的 PITR;`ncn-lb` 故障切换;每日状态快照(本地与异地)。 |
| **观测** | 手写的 Prometheus `/metrics`(不含密钥);某观测主机上的 Prometheus 与 Grafana(Grafana 经管理员限定的反代内嵌);Gatus uptime。 |
| **告警** | 数据驱动的规则引擎(sustain / resolve / escalate / repeat)与基于 EWMA 的异常检测(每节点+指标基线),路由到 Telegram。 |
| **RPKI** | 监控运营方前缀的 ROA 有效性(经 RIPEstat),并读取 PoP BIRD 的实时 route-origin-validation 标记;Krill 发布 ROA,Routinator 校验。 |
| **鉴权与访问** | HMAC 会话 cookie 与 TOTP;OAuth/OIDC(GitHub、Telegram);Passkey;API token;SSH-key 登录。控制台可作为 OAuth2 身份提供方,供其他服务 SSO。 |
| **Telegram 机器人** | 绑定运维者、需审批的机群控制;操作失败卡片;AI 问答;群组伴侣。 |
| **AI 助手** | DeepSeek 兼容的工具调用 agent(检查,以及含 `run_command` 的人工批准执行);控制台内与 MCP 上均可用。 |
| **自托管 Wiki** | Markdown 内容存于 PostgreSQL,应用内渲染;公开(匿名)与内部(运维限定)两级;浏览器内编辑,带版本历史。 |
| **面向成员** | 公开 Landing、Looking Glass、状态/事件页,及基于 PeeringDB 的 Peering 信息与申请受理。 |
| **Webmail** | 服务 `mail.example.com` 的 `ncn-mail`(到 Dovecot/Postfix 的 IMAP/SMTP 回环,经 rspamd 做 DKIM)。 |
| **审计** | 每个特权动作均被记录;审计日志按定时器异地复制。 |

---

## 技术栈

- **后端:** Go、`net/http`、`pgx` v5 / PostgreSQL、内嵌 SQL 迁移。
- **前端:** Vue 3、Vite、TypeScript、Tailwind、Pinia、Vue Router、vue-i18n、
  marked + DOMPurify、xterm.js。
- **基础设施:** nginx、systemd(socket 激活的服务与定时器)、BIRD(BGP)、
  Krill + Routinator(RPKI)、Prometheus + Grafana + Gatus、外部边缘/CDN、
  WireGuard(骨干)。

---

## 构建与运行

前置:Go ≥ 1.22、Node ≥ 18(用于 SPA 构建)。

```sh
# 后端(core-console)
cd core-console/backend
go vet ./... && go test ./...
go build -o ncn-api .
./ncn-api                      # 监听 :9000

# 前端(core-console SPA)
cd core-console
npm install
npm run dev                    # Vite 开发服务器
npm run build                  # 类型检查 + i18n lint + 生产 dist/

# 运维 CLI
cd cli/ncn-debug && go build -o ncn-debug
```

本地运行时 PostgreSQL 可选;未设 `NCN_DATABASE_URL` 时后端使用 JSON 文件回退。

---

## 部署

部署带门控,从工作站经 SSH 对目标主机执行。完整指南见 [`DEPLOYMENT.md`](DEPLOYMENT.md)。

```sh
# core-console
core-console/deploy/deploy.sh backend     # 在目标上 go vet + go test,构建,原子切换,零停机重启
core-console/deploy/deploy.sh frontend    # i18n lint + 类型检查 + vite 构建,发布 dist/

# 整套栈(先 webmail,后 core-console)
./deploy-all.sh
```

- 后端部署在目标上 `go vet ./...` 与 `go test ./...` 不通过时拒绝发布;上一个
  二进制会被备份,并支持 `deploy.sh rollback`。
- `ncn-api` 由 socket 激活(`ncn-api.socket` + `ncn-api.service`),二进制切换
  零停机。
- 每日状态快照(本地与异地)与审计日志异地复制经 systemd 定时器运行
  (`deploy/ncn-state-backup.*`、`deploy/ncn-audit-rsync.*`)。

---

## 配置

- 运行时配置与密钥位于控制节点的 `/etc/ncn-core-console/`(例如 `session.key`、
  `oauth.env`、机群 SSH 密钥);见 `core-console/deploy/oauth.env.example`。
- nginx vhost 定义于 `core-console/deploy/nginx-ncn-core-console.conf`(控制台 /
  公开 / Wiki)。公开端点显式白名单;`/api/` 下其余路径从公开主机返回 404。
- `ncn-lb` 读取 `/etc/ncn-lb/config.json`;Webmail 与机器人在各自主机读各自的
  env 文件。

密钥从不入库。生产取值在主机上带外配置;`*.example` 文件记录其预期形态。

---

## 安全模型

- **三级访问:** 公开(nginx 白名单)、内部(任意已认证运维者)、管理(角色限定)。
  会话 cookie 经 HMAC 签名,并限定于管理主机。
- **主机隔离**在 nginx 与 SPA 路由两处强制(纵深防御):公开主机不暴露管理 API。
- **唯一执行器:** 对机群的所有写操作均经 `ncn-api`。MCP 服务器、机器人与 CLI 为
  客户端,不能直接触及机群。写 / 命令类工具要求管理员运维者并被审计。
- **机群访问**使用控制节点上一把专用 SSH 密钥;远程命令执行经 agent 流程人工批准。
- **内容安全:** Wiki Markdown 渲染前经 DOMPurify 清洗;公开 Wiki API 在服务端
  强制 `is_public`,内部页面不被暴露。

---

## 文档

- **部署:** [`core-console/QUICKSTART.md`](core-console/QUICKSTART.md) 与
  [`DEPLOYMENT.md`](DEPLOYMENT.md)。
- **运维与成员文档:** 自托管 Wiki(公开主机为公开访问,管理主机下为内部)。源
  Markdown 位于 `core-console/wiki/`。
- **Runbook:** `core-console/docs/`(`PITR-RESTORE.md`、`POP-ONBOARDING.md`)。
- **各组件 README:** `webmail/README.md`、`core-console/agent/README.md`、
  `core-console/mcp/README.md`、`cli/ncn-debug/README.md`。
