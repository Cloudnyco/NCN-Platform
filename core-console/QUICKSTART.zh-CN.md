# 快速开始 — 运行 / 部署 ncn-core-console

> [English](QUICKSTART.md) · **简体中文**

按使用场景划分，共有四个入口。所有入口均为幂等操作，凡涉及生产环境的部分均经过门控与校验。

## 1. 日常部署（生产环境，从工作站执行）

控制台运行于 **ctrl-01**。`deploy.sh` 在本地构建并通过 SSH（`root@deploy-host`）发布。后端部署以 `go vet` 与 `go test` 作为门控；旧二进制文件会被保留以便回滚。

```bash
deploy/deploy.sh all        # backend + frontend + smoke   (default)
deploy/deploy.sh backend    # Go only — vet+test gate, atomic zero-downtime swap
deploy/deploy.sh frontend   # SPA only — i18n lint + typecheck + vite + ship
deploy/deploy.sh nginx      # ship nginx conf + reload
deploy/deploy.sh health     # full-stack "is everything up?" (see below)
deploy/deploy.sh rollback   # restore the previous ncn-api binary
```

`deploy.sh health` 一次性检查：公网端点（从 ctrl-01 边缘节点）、控制平面服务（ncn-api / nginx / goflow2 / softflowd）以及流水线（已发现的不同 sFlow 导出器），还有 pop-03 上的 HA/RPKI 组件（Postgres 流复制副本、Routinator）。任一检查失败时以非零状态退出。

## 2. 全新主机 / 灾难恢复

以下命令可在新主机上搭建整个栈（或进行重建）。从代码检出目录以 root 身份运行：

```bash
sudo deploy/bootstrap.sh
```

该脚本安装依赖（go/node/nginx），构建 API 与 SPA，安装 systemd 单元（`ncn-api.service`/`.socket`，在 127.0.0.1:9000 上通过 socket 激活），配置 nginx 并启动服务。首次启动时自动应用迁移。

该脚本不会生成密钥。如需完整功能，请在 `/etc/ncn-core-console/` 中提供以下内容：`oauth.env`（包含用于 Postgres 的 `NCN_DATABASE_URL` —— 省略则以文件方式运行）、`tg.env`、`fleet-key`（以及 `fleet-known-hosts`）、`turnstile.secret`、`agent-ca/` 与 `agent-keys/`。进行灾难恢复还原时，请在启动前将最新的状态快照放入 `/etc/ncn-core-console`（参见 `scripts/state-backup.sh`；Postgres PITR 位于 `scripts/pitr`）。随后运行：`systemctl restart ncn-api && deploy/deploy.sh health`。

## 3. 本地开发

API 以文件方式运行（无 Postgres、无 fleet 密钥）并启用 Vite HMR：

```bash
scripts/dev.sh         # API :9000 (file-backed) + Vite :5173 (proxies /api → :9000)
# open http://localhost:5173    (Ctrl-C stops both)
```

需要 Go ≥1.22 与 Node ≥18。如果当前用户对 `/var/log` 与 `/etc/ncn-core-console` 没有写权限，请运行 `sudo scripts/dev.sh`。若要针对远程 API 开发 SPA，运行 `npm run dev` 并将 vite 代理目标指向远程地址。

## 4. 容器（可移植的演示 / 开发栈）

自包含的栈（api + Postgres + 由 nginx 提供的 SPA），无需主机配置：

```bash
docker compose -f deploy/docker/docker-compose.yml up --build
# open http://localhost:8080
```

`web`（nginx）提供 SPA 并将 `/api` 代理至 `api`（Go），后者与 `db`（Postgres）通信。未挂载 fleet 密钥或任何密钥，因此节点抓取与 OAuth/TG 处于关闭状态 —— 此配置面向 UI/API 演示与开发，而非生产环境。向 `api` 服务添加 env 文件或挂载 `/etc/ncn-core-console` 即可启用更多功能。

---

**相关：** `deploy/sflow/`（流采集器部署）、`scripts/state-backup.sh` 与 `scripts/pitr`（灾难恢复备份）、`scripts/agent-node-provision.sh`（接入一个 PoP）。
