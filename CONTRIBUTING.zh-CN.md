# 贡献指南

> [English](CONTRIBUTING.md) · **简体中文**

本仓库包含一个自托管的网络运维平台，旨在针对运维方自有的网络运行。本项目接受
贡献，包括缺陷修复、功能、文档以及新的集成。

## 项目结构

| 路径 | 说明 |
|---|---|
| `core-console/` | 运维控制台：Vue 3 SPA、Go API（`ncn-api`），以及每个 PoP 的 `agent`、`lb`（故障切换）和 `mcp`。构成平台的主要部分。 |
| `webmail/` | 自托管 webmail（Go 与 Vue），作为 Postfix/Dovecot 的前端。 |
| `cli/` | `ncn-login`（SSH 签名登录）和 `ncn-debug`（只读运维 CLI）。 |
| `scripts/` | 备份/恢复、PITR 以及每个 PoP 的部署辅助脚本。 |
| `deploy-all.sh` | 顶层编排脚本（先 webmail，后 console）。 |

## 本地开发

```bash
# Console: API (file-backed, no Postgres/fleet needed) + Vite HMR
cd core-console && scripts/dev.sh        # → http://localhost:5173
# or the full containerized stack (api + Postgres + nginx-served SPA)
docker compose -f core-console/deploy/docker/docker-compose.yml up --build  # → :8080
```

四个入口（dev / deploy / bootstrap / docker）的说明参见
`core-console/QUICKSTART.md`，从零开始的生产环境安装参见 `DEPLOYMENT.md`。

## 配置

所有与运维方相关的值均通过环境变量（前缀 `NCN_*`）和运行时文件配置；参见
`.env.example`。代码库附带占位值（`example.com`、`AS64500`、RFC5737/RFC3849
地址、`ctrl-01`/`pop-0N` 节点名）。这些值须通过环境变量或节点注册表替换为
运维方自有的值。仓库中没有任何值指向真实网络。

## 约定

- 后端（Go）：在提交 PR 前运行 `cd <module> && go vet ./... && go test ./...`。
  新的运维方相关值须置于 `getenvDefault("NCN_...", "<placeholder>")` 之后。
- 前端（Vue）：运行 `npm run lint:i18n && npm run typecheck && npm run build`。
  三种语言（en / zh-CN / zh-TW）须保持键对齐；`lint:i18n` 会强制校验这一点。
- 提交信息采用约定式格式（`feat(console): …`、`fix(...)`、`docs(...)`）。
- 任何影响生产路由器或主机的改动都须经确认门控且可回滚。

## 问题与安全报告

功能性缺陷应通过 GitHub issue 报告。安全问题的处理方式见 `SECURITY.md`；请勿
为漏洞提交公开 issue。
