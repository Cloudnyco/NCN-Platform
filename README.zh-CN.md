# NCN — 自托管网络运维平台

> [English](README.md) · **简体中文**

一个开源的运维平台,用来运营一张小型**多 PoP 任播(anycast)网络**:BGP/BIRD
机群遥测、告警 + AI 分诊、RPKI 监控、Peering 申请受理、容量/SLA/流量分析、DDoS
缓解、值班排班、Webmail、自建负载均衡/故障切换、Wiki,以及身份认证 —— 主张
**自托管优先于付费 SaaS**。

为**你自己的**网络运行它。所有与运营方相关的内容都通过环境变量(`NCN_*`,见
[`.env.example`](.env.example))和运行时的节点注册表配置。仓库里**只含占位值**
—— `AS64500`、`example.com`、RFC5737/RFC3849 地址、`ctrl-01`/`pop-0N` 节点名 ——
不指向任何真实网络。

**快速开始:** [`core-console/QUICKSTART.md`](core-console/QUICKSTART.md) 覆盖四种
入口 —— 本地开发、一键部署、全新主机/灾备引导、Docker Compose。**许可证:**
[Apache 2.0](LICENSE) · **贡献指南:** [`CONTRIBUTING.md`](CONTRIBUTING.md) ·
**安全:** [`SECURITY.md`](SECURITY.md)。

> 代码、注释与提交信息均为英文。面向成员的文档(Wiki)以简体中文为主。

---

## 目录

- [架构](#架构)
- [仓库结构](#仓库结构)
- [主机与拓扑](#主机与拓扑)
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
                        Cloudflare(代理,TLS Strict)
                                   │
        admin.example.com   example.com / wiki.example.com   mail.example.com
                │                    │                          │
                ▼                    ▼                          ▼
        ┌───────────────── ctrl-01(控制节点)───────────┐  ┌── pop-03 ──┐
        │  nginx → ncn-api (:9000, Go)                   │  │  ncn-mail  │
        │  PostgreSQL(主库)                             │  │  dovecot   │
        │  Vue SPA(管理控制台 + 公开站点 + Wiki)        │  │  postfix   │
        └───────────────────────┬────────────────────────┘  │  rspamd    │
                                 │ 遥测(SSH / HTTPS)        │  Prometheus│
            ┌────────────┬───────┼───────┬────────────┐      │  Grafana   │
            ▼            ▼       ▼       ▼            ▼      │  Gatus     │
         pop-03       pop-04   pop-01   pop-08 …(8 个 PoP)  └────────────┘
       (观测 +       (HA 备机  (Krill CA
        备机)         + ncn-lb)  + Routinator)

   每个 PoP 运行 BIRD(BGP、任播),可选运行 ncn-agent(遥测)。
   骨干:2001:db8:50::/44(PoP 之间的 IPv6 mesh)。
```

控制台**直接调用真实探针**(`uptime`、`/proc/loadavg`、`birdc`、`wg`)并读取
实时基础设施 —— 没有任何 mock 数据。对机群的写操作都经由唯一执行器
(`ncn-api`),它强制鉴权并审计每一次动作。

---

## 仓库结构

| 路径 | 说明 |
|---|---|
| [`core-console/`](core-console/) | 平台主体:`ncn-api`(Go 后端)+ Vue 3 SPA,占工程绝大部分。 |
| `core-console/agent/` | `ncn-agent` —— 每个 PoP 上的遥测代理(HTTPS + HMAC),替代 SSH 轮询。 |
| `core-console/lb/` | `ncn-lb` —— 自建的 Cloudflare-LB 等价物:带健康检查的源站池 + 主备故障切换。 |
| `core-console/mcp/` | `ncn-mcp` —— 把控制台的运维工具暴露给 Claude Code 的 MCP 服务器。 |
| [`webmail/`](webmail/) | `ncn-mail` —— 服务 `mail.example.com` 的独立 Webmail(自包含于 pop-03)。 |
| `cli/ncn-debug/` | 基于控制台 REST API 的只读运维 CLI(单个静态 Go 二进制)。 |
| `cli/ncn-login/` | CLI 的运维者登录辅助工具。 |
| `deploy-all.sh` | 一条命令部署整套栈(先 webmail 后 core-console)。 |
| `scripts/`、`backups/` | 工作区级别的辅助脚本与快照暂存。 |

---

## 主机与拓扑

| 主机 | 角色 |
|---|---|
| **ctrl-01** | 控制节点。`ncn-api`、PostgreSQL **主库**、承载三个 vhost(控制台 / 公开站 / Wiki)的 nginx、WAL 归档源。 |
| **pop-03** | 观测(Prometheus + Grafana + Gatus)、**Webmail**、PG 备库 + WAL 归档接收端、温备 `ncn-api`。 |
| **pop-04** | HA:PostgreSQL **流复制备库** + `ncn-lb` 故障切换控制器 + 异地灾备目标。 |
| **pop-01** | RPKI:**Krill** CA(发布 ROA)+ **Routinator** 校验器(中央 RTR);同时是 BGP 节点。 |
| **pop-02 / pop-08 / pop-06 / pop-05** | PoP(BGP / 任播)。 |

8 个 PoP 在 `2001:db8:50::/44` 骨干上参与任播。公开端点由 Cloudflare 前置
(代理、SSL Strict)。

---

## core-console

### 后端 — `ncn-api`

Go `net/http` 服务(`package main`,约 28k 行),监听 `:9000`,由 systemd
socket 激活。状态通过 `pgx`(`database/sql`)存于 **PostgreSQL**,并使用
**内嵌迁移**(`//go:embed migrations/*.sql`);每个存储都**容忍无数据库**,在未
配置数据库时回退到 JSON 文件 —— 所以二进制有没有 Postgres 都能跑。

各项能力大致按「一个文件一个关注点」组织在 `core-console/backend/` 下:

- **机群与节点** — `fleet.go`、`noderegistry.go`、`nodes_api.go`、`nodes_onboard.go`、`heartbeat.go`、`bird_scrape.go`
- **任播** — `anycast.go`(将某 PoP 从任播中 drain / undrain)
- **Mesh / 配置** — `mesh_config.go`、`meshApply.go`、`tunnel.go`
- **HA / 灾备 / 复制** — `replmon.go`(+ `lb/`、PITR 脚本)
- **观测** — `metrics.go`、`alertmetrics.go`、`grafana_proxy.go`
- **告警** — `alerts.go`、`alertrules.go`、`alertrules_api.go`、`alertanomaly.go`
- **RPKI** — `rpki.go`(ROA 有效性 + ROV 监控)
- **鉴权与访问** — `auth.go`、`auth_apitoken.go`、`auth_ssh.go`、`auth_sso.go`、`oauth.go`、`oauth_telegram.go`、`passkey.go`、`idp_provider.go`(控制台作为 OAuth2 IdP)、`recover_bootstrap.go`
- **Telegram 机器人** — `bot_tg.go`、`bot_identity_test.go`、`bot_manage.go`、`bot_netadmin.go`、`bot_opfail.go`、`bot_ai.go`、`bot_agent_tg.go`、`bot_drill.go`、`notify_tg.go`
- **AI 助手** — `deepseek.go`、`agent.go`、`agent_tools.go`、`ai_usage.go`、`ai_history_api.go`、`model_config.go`
- **Wiki** — `wikistore.go`、`wiki_api.go`
- **面向成员** — `peering_apply.go`、`peeringdb.go`、`incidents.go`、`visitor.go`、`billing.go`、`invite.go`
- **邮件桥接** — `mail_bridge.go`、`mail_forgot_bridge.go`、`mail_role_recover.go`(控制台 ↔ Webmail)
- **运维基础设施** — `opfailures.go`、`audit.go`、`ratelimit.go`、`turnstile.go`、`term.go`、`admincli.go`、`db.go`、`fx.go`

### 前端 — SPA

Vue 3 + Vite 单页应用(TypeScript、Tailwind、Pinia、Vue Router、`vue-i18n`)。
一次构建服务三个站点;`App.vue` 按路由选择布局:

- **admin.example.com** —— 运维控制台(`AdminLayout`):Dashboard、Fleet、
  Servers、Alerts、Alert Rules、Observability、Performance、Connectivity、
  Bird、Security、Audit、Billing、Assistant、Wiki、Onboarding。
- **example.com** —— 公开站点(`PublicLayout`):Landing、Looking Glass、
  Status、Peering 信息 + 申请、法律页,以及公开 Wiki(`/docs`)。
- **wiki.example.com** —— 公开 Wiki(同一份 SPA 产物)。

i18n 含 **en / zh-CN / zh-TW**;构建前的 `lint:i18n` 步骤会在任一语言缺 key 时
让构建失败。`marked` + `DOMPurify` 渲染 Wiki Markdown;`@xterm/xterm` 驱动控制台
内置终端。

### 子组件(agent / lb / mcp)

- **`ncn-agent`**(`agent/`)—— 跑在每个 PoP 上(`:9101`,HTTPS + HMAC-bearer),
  返回与 `fleet.go` 过去经 SSH 采集相同的遥测流水线输出。按节点的 `Transport`
  在 `ssh`(默认)与 `rest`(带 SSH 回退)间选择,因此可一个 PoP 一个 PoP 地灰度。
- **`ncn-lb`**(`lb/`)—— 带健康检查的源站池,自动主备切换(提升 PG 备库 →
  启动温备 `ncn-api` → 重指 Cloudflare DNS)。跑在 pop-04 上以扛住 ctrl-01 宕机。
  **默认以 `observe` 模式发布**(只记录「将会做什么」);切换脚本验证通过后再
  arm。无自动 fail-back(防抖)。
- **`ncn-mcp`**(`mcp/`)—— 一个薄 MCP 代理,把控制台内 AI agent 使用的同一批
  运维工具(`list_nodes`、`fleet_status`、`run_command` …)暴露给本地 Claude
  Code。控制台后端仍是唯一执行器,并强制执行每一条安全规则。

---

## 功能板块

| 板块 | 概述 |
|---|---|
| **机群遥测** | 实时的每 PoP 指标(负载、经 `birdc` 的 BGP 会话、WireGuard、uptime);节点注册表 + 上线/下线生命周期。 |
| **任播运维** | 人工批准地将某 PoP 从任播中 drain/undrain(`birdc disable upstream_*`);拒绝 drain 最后一个/关键节点。 |
| **HA 与灾备** | PostgreSQL 流复制(ctrl-01 → pop-04,亚秒级 RPO);经 WAL 归档 + 每周基础备份 + 恢复演练的 PITR;`ncn-lb` 故障切换;每日状态快照(本地 + 异地)。 |
| **观测** | 手写的 Prometheus `/metrics`(不含密钥);pop-03 上的 Prometheus + Grafana(Grafana 经管理员限定的反代内嵌进控制台);Gatus uptime。 |
| **告警** | 数据驱动的规则引擎(sustain / resolve / escalate / repeat)**外加**基于 EWMA 的异常检测(每节点+指标基线),路由到 Telegram。 |
| **RPKI** | 监控自有前缀的 ROA 有效性(经 RIPEstat),并读取 PoP BIRD 上实时的 route-origin-validation 标记;Krill 发布 ROA,Routinator 校验。 |
| **鉴权与访问** | 会话 cookie(HMAC)+ TOTP;OAuth(Google / Microsoft / GitHub / Telegram);Passkey;API token;SSH-key 登录;控制台自身作为 OAuth2 **IdP** 供其他服务 SSO。 |
| **Telegram 机器人** | 绑定运维者、需审批的机群控制;操作失败卡片;AI 问答;群组伴侣。 |
| **AI 助手** | DeepSeek 驱动的工具调用运维 agent(检查 + 人工批准执行,含 `run_command`);控制台内与 MCP 上均可用。 |
| **自托管 Wiki** | Markdown 内容存于 Postgres、应用内渲染;公开(匿名)+ 内部(运维限定)两级;浏览器内编辑带版本历史。 |
| **面向成员** | 公开 Landing、Looking Glass、状态/事件页、基于 PeeringDB 的 Peering 信息 + 申请受理。 |
| **Webmail** | pop-03 上的 `ncn-mail` 承载 `mail.example.com`(到 dovecot/postfix 的 IMAP/SMTP 回环,经 rspamd 做 DKIM)。 |
| **审计** | 每个特权动作都被记录;审计日志按定时器异地 rsync。 |

---

## 技术栈

- **后端:** Go 1.25、`net/http`、`pgx` v5 / PostgreSQL 17、内嵌 SQL 迁移。
- **前端:** Vue 3.5、Vite 6、TypeScript、Tailwind 3、Pinia、Vue Router、vue-i18n、marked + DOMPurify、xterm.js。
- **基础设施:** nginx、systemd(socket 激活的服务 + 定时器)、BIRD(BGP)、Krill + Routinator(RPKI)、Prometheus + Grafana + Gatus、Cloudflare(边缘)、WireGuard(骨干)。

---

## 构建与运行

前置:**Go ≥ 1.25**、**Node ≥ 20**(用于 SPA 构建)。

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

本地运行时 PostgreSQL 可选 —— 不设 `NCN_DB_*` 时后端使用 JSON 文件回退。

---

## 部署

部署是**带闸门的**,从工作区经 SSH 对线上主机执行。

```sh
# 仅 core-console
bash core-console/deploy/deploy.sh backend     # 在目标上 go vet + go test,构建,原子切换,零停机重启
bash core-console/deploy/deploy.sh frontend    # i18n lint + 类型检查 + vite 构建,发布 dist/

# 整套栈(先 webmail,后 core-console)
./deploy-all.sh
```

- 后端部署在目标上 `go vet ./...` 与 `go test ./...` 不通过时**拒绝发布**;它会
  备份上一个二进制,并支持 `deploy.sh rollback`。
- `ncn-api` 由 socket 激活(`ncn-api.socket` + `ncn-api.service`),所以切换是
  零停机的。
- 每日状态灾备快照(本地 + 异地 pop-04)与审计日志异地 rsync 经 systemd 定时器
  运行(`deploy/ncn-state-backup.*`、`deploy/ncn-audit-rsync.*`)。

---

## 配置

- 运行时配置与密钥位于 ctrl-01 的 `/etc/ncn-core-console/` 下(如 `session.key`、
  `oauth.env`、机群 SSH 密钥)。见 `core-console/deploy/oauth.env.example`。
- nginx vhost:`core-console/deploy/nginx-ncn-core-console.conf`(控制台 / 公开 /
  Wiki)—— 公开端点显式白名单;`/api/` 下的其余一切从公开主机返回 404。
- `ncn-lb` 读 `/etc/ncn-lb/config.json`;Webmail 与机器人在各自主机上读各自的
  env 文件。

密钥从不入库。真实值在主机上带外配置;`*.example` 文件记录其预期形态。

---

## 安全模型

- **三层鉴权:** 公开(nginx 白名单)、内部(任意已认证运维者)、管理(角色限定)。
  会话 cookie 经 HMAC 签名,并**按主机限定于 `admin.example.com`**。
- **主机隔离**在 nginx 与 SPA 路由两处都强制(纵深防御):公开主机绝不暴露
  管理 API。
- **唯一执行器:** 对机群的所有写操作都经 `ncn-api`。MCP 服务器、机器人、CLI 都是
  客户端 —— 它们不能直接动机群。写 / 命令类工具要求**管理员**运维者并被**审计**。
- **机群访问**使用 ctrl-01 上一把专用 SSH 密钥;远程命令执行经 agent 流程人工批准。
- **内容安全:** Wiki Markdown 渲染前经 DOMPurify 清洗;公开 Wiki API 在服务端
  强制 `is_public`,内部页面绝不外泄。

---

## 文档

- **运维手册与成员文档:** 自托管 Wiki —— `wiki.example.com`(公开)与
  `admin.example.com/admin/wiki`(内部运维)。源 Markdown 在 `core-console/wiki/`。
- **Runbook:** `core-console/docs/`(`PITR-RESTORE.md`、`POP-ONBOARDING.md`)
  以及 Wiki 的 `ops/runbooks/*`。
- **各组件 README:** `webmail/README.md`、`core-console/agent/README.md`、
  `core-console/mcp/README.md`、`cli/ncn-debug/README.md`。
