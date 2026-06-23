# ncn-mcp — drive the NCN fleet from Claude Code

An MCP server that exposes the same ops tools the in-console DeepSeek agent uses
(`list_nodes`, `fleet_status`, `node_detail`, `active_alerts`, `op_failures`,
`decommission`, `recommission`, `delete_node`, `mesh_apply`, `run_command`) to a
**local Claude Code**. It's a thin proxy: it fetches the tool catalog from
`ncn-api` and forwards each call to the console backend, which is the only
executor and enforces every safety rule.

## Safety model (unchanged from the in-console agent)
- The **console backend is the only executor.** This MCP server never touches
  the fleet directly — it just relays tool calls over HTTPS.
- **Read-only** tools work for any operator token. **Write / command** tools
  (`decommission`, `recommission`, `delete_node`, `mesh_apply`, `run_command`)
  require the token's operator to be an **admin**, and every write is audited
  as `ai.tool.exec` actor `mcp:<operator>`.
- `run_command` has an 8s timeout and bounded output; `ctrl-01` keeps its
  decommission/delete guards.
- **You are the human-in-the-loop:** Claude Code asks you to approve each tool
  call before it runs. Read the exact command/target before approving.

## Setup
1. **Mint an API token** in the console: `/admin/security` → API tokens →
   create one (use an **admin** operator if you want the write/command tools).
   Copy the `ncntok_…` secret (shown once).
2. **Export it** where you run Claude Code:
   ```sh
   export NCN_MCP_TOKEN=ncntok_xxxxxxxx
   # optional, defaults to https://admin.example.com
   export NCN_MCP_BASE=https://admin.example.com
   ```
3. The repo's `.mcp.json` already registers this server (`node mcp/ncn-mcp.mjs`,
   reading `${NCN_MCP_TOKEN}`). Start Claude Code in the repo and approve the
   `ncn` MCP server when prompted (`/mcp` lists it; `claude mcp list` checks it).
4. Deps: `cd mcp && npm install` (once).

## Use
Ask Claude Code things like “which NCN nodes are unhealthy?” (read-only, runs
straight away) or “restart bird on pop-05” (proposes `run_command`; you approve
the exact command, an admin token executes it, the output comes back).

A non-admin token still gets all read-only tools — handy for a diagnosis-only
setup.
