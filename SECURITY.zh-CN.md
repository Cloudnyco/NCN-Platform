# 安全策略

> [English](SECURITY.md) · **简体中文**

## 报告漏洞

请勿为安全漏洞创建公开 issue。请通过邮件联系维护者（参见仓库所有者的资料页），
或使用 GitHub 的私密漏洞报告功能（Security 标签页下的 "Report a vulnerability"）
进行报告。确认回复通常会在数日内发出。

报告应包含：受影响的组件、版本或提交（commit）、复现步骤以及影响范围。

## 运维指南

本平台控制网络基础设施。以下实践适用于任何部署：

- 密钥仅存放于 `/etc/<service>/` 环境变量文件和运行时密钥文件中，绝不进入仓库。
  应将 `oauth.env`、`tg.env`、`fleet-key`、agent CA/密钥、`turnstile.secret`、
  恢复密钥以及 `NCN_DATABASE_URL` 密码排除在版本控制之外
  （`.gitignore` 已排除 `*.env`、`*.key`、`*.pem`、`*.age`）。
- 加密的密钥备份不得提交到当前或将来可能公开的仓库。一旦泄露，即使是经 age
  加密的数据块也应视为已被攻破。
- API 受身份认证保护。公开站点与 looking-glass 路径与 `/admin/*` 相隔离
  （在 nginx 与路由层进行主机分离）。
- 涉及生产环境的操作（配置回滚、mesh 应用、DDoS 缓解、故障切换）在设计上需要
  确认门控、可审计且可逆；应保持这一特性。
- 各 PoP 的 agent 使用经 mTLS 固定（pinned）的 HTTPS 并结合 HMAC。如怀疑密钥泄露，
  使用 `scripts/` 中的辅助脚本进行轮换。

## 设计上即敏感的模块

以下模块处理凭据或执行破坏性操作，任何变更都需额外审查：

- `core-console/backend/{auth*,oauth,passkey,recover_bootstrap,audit}.go`
- `lb/failover.sh`
- `scripts/{backup,restore}-secrets.sh`
- `scripts/pitr/` 下的任何内容
