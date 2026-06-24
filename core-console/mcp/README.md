# ncn-mcp — MCP server for fleet operations from Claude Code

> **English** · [简体中文](README.zh-CN.md)

An MCP server that exposes the same operations tools used by the in-console
DeepSeek agent (`list_nodes`, `fleet_status`, `node_detail`, `active_alerts`,
`op_failures`, `decommission`, `recommission`, `delete_node`, `mesh_apply`,
`run_command`) to a local Claude Code instance. It acts as a proxy: it fetches
the tool catalog from `ncn-api` and forwards each call to the console backend,
which is the only executor and enforces all safety rules.

## Safety model (identical to the in-console agent)
- The console backend is the only executor. This MCP server does not touch the
  fleet directly; it relays tool calls over HTTPS.
- Read-only tools are available to any operator token. Write and command tools
  (`decommission`, `recommission`, `delete_node`, `mesh_apply`, `run_command`)
  require the token's operator to have admin privileges, and every write is
  audited as `ai.tool.exec` with actor `mcp:<operator>`.
- `run_command` enforces an 8s timeout and bounded output; `ctrl-01` retains its
  decommission/delete guards.
- The operator is the human in the loop: Claude Code requests approval for each
  tool call before it runs. Review the exact command and target before
  approving.

## Setup
1. Mint an API token in the console: `/admin/security` → API tokens → create
   one (use an admin operator to enable the write and command tools). Copy the
   `ncntok_…` secret (shown once).
2. Export it in the environment where Claude Code runs:
   ```sh
   export NCN_MCP_TOKEN=ncntok_xxxxxxxx
   # optional, defaults to https://admin.example.com
   export NCN_MCP_BASE=https://admin.example.com
   ```
3. The repository's `.mcp.json` already registers this server
   (`node mcp/ncn-mcp.mjs`, reading `${NCN_MCP_TOKEN}`). Start Claude Code in
   the repository and approve the `ncn` MCP server when prompted (`/mcp` lists
   it; `claude mcp list` verifies it).
4. Install dependencies once: `cd mcp && npm install`.

## Usage
Read-only requests such as "which nodes are unhealthy?" run immediately.
Write or command requests such as "restart bird on pop-05" cause the server to
propose a `run_command` call; after the operator approves the exact command, an
admin token executes it and the output is returned.

A non-admin token retains access to all read-only tools, which is suitable for a
diagnosis-only configuration.
