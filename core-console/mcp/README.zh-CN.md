# ncn-mcp — 用于从 Claude Code 执行集群运维的 MCP 服务器

> [English](README.md) · **简体中文**

一个 MCP 服务器，向本地 Claude Code 实例暴露与控制台内 DeepSeek agent 相同的
运维工具（`list_nodes`、`fleet_status`、`node_detail`、`active_alerts`、
`op_failures`、`decommission`、`recommission`、`delete_node`、`mesh_apply`、
`run_command`）。它充当代理：从 `ncn-api` 获取工具目录，并将每次调用转发给
控制台后端，后端是唯一的执行方并负责强制执行所有安全规则。

## 安全模型（与控制台内 agent 完全一致）
- 控制台后端是唯一的执行方。该 MCP 服务器不直接操作集群，仅通过 HTTPS 转发
  工具调用。
- 只读工具对任意运维者令牌可用。写入与命令类工具（`decommission`、
  `recommission`、`delete_node`、`mesh_apply`、`run_command`）要求该令牌对应的
  运维者具备管理员权限，且每次写入都以 `ai.tool.exec`、actor 为
  `mcp:<operator>` 的形式记入审计。
- `run_command` 强制 8 秒超时并限制输出大小；`ctrl-01` 保留其
  decommission/delete 保护机制。
- 运维者处于回路之中：Claude Code 在每次工具调用运行前请求批准。批准前请核对
  确切的命令与目标。

## 配置
1. 在控制台中创建 API 令牌：`/admin/security` → API tokens → 创建一个（使用
   管理员运维者以启用写入与命令类工具）。复制 `ncntok_…` 密钥（仅显示一次）。
2. 在运行 Claude Code 的环境中导出该令牌：
   ```sh
   export NCN_MCP_TOKEN=ncntok_xxxxxxxx
   # 可选，默认为 https://admin.example.com
   export NCN_MCP_BASE=https://admin.example.com
   ```
3. 仓库的 `.mcp.json` 已注册该服务器（`node mcp/ncn-mcp.mjs`，读取
   `${NCN_MCP_TOKEN}`）。在仓库中启动 Claude Code，并在提示时批准 `ncn` MCP
   服务器（`/mcp` 可列出；`claude mcp list` 可验证）。
4. 安装依赖（一次性）：`cd mcp && npm install`。

## 使用
只读类请求（例如“哪些节点不健康？”）会立即运行。写入或命令类请求（例如“在
pop-05 上重启 bird”）会使服务器提议一次 `run_command` 调用；运维者批准确切命令
后，由管理员令牌执行并返回输出。

非管理员令牌仍可访问所有只读工具，适用于仅诊断的配置。
