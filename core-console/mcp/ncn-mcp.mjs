#!/usr/bin/env node
// ncn-mcp — an MCP server that exposes the NCN fleet ops tools to a local
// Claude Code. It is a thin proxy: on startup it fetches the tool catalog from
// the ncn-api agent-tool bridge (GET /api/v1/auth/agent/tools) and forwards
// each tools/call to POST /api/v1/auth/agent/tool. The console backend is the
// only executor and enforces the safety model (read-only open to any operator;
// write/command tools require an ADMIN token and are audited mcp:<op>). The
// human-in-the-loop is YOU approving each tool call in Claude Code.
//
// Auth: an operator API token (mint at /admin/security → API tokens).
//   NCN_MCP_BASE   default https://admin.example.com
//   NCN_MCP_TOKEN  required, e.g. ncntok_xxx
import { Server } from '@modelcontextprotocol/sdk/server/index.js'
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { ListToolsRequestSchema, CallToolRequestSchema } from '@modelcontextprotocol/sdk/types.js'

const BASE = (process.env.NCN_MCP_BASE || 'https://admin.example.com').replace(/\/$/, '')
const TOKEN = process.env.NCN_MCP_TOKEN || ''

function authHeaders(extra = {}) {
  return { Authorization: 'Bearer ' + TOKEN, Accept: 'application/json', ...extra }
}

async function fetchTools() {
  const r = await fetch(BASE + '/api/v1/auth/agent/tools', { headers: authHeaders() })
  const j = await r.json().catch(() => ({}))
  if (!r.ok || !j.ok) throw new Error('list tools failed: ' + (j.error || r.status))
  // backend toolDef: {type:"function", function:{name, description, parameters}}
  return (j.data?.tools || []).map((t) => ({
    name: t.function.name,
    description: t.function.description,
    inputSchema: t.function.parameters || { type: 'object', properties: {} },
  }))
}

async function callTool(name, args) {
  const r = await fetch(BASE + '/api/v1/auth/agent/tool', {
    method: 'POST',
    headers: authHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({ name, args: args || {} }),
  })
  const j = await r.json().catch(() => ({}))
  if (!r.ok || !j.ok) throw new Error(j.error || ('http ' + r.status))
  return j.data?.content ?? ''
}

async function main() {
  if (!TOKEN) {
    process.stderr.write('ncn-mcp: NCN_MCP_TOKEN not set — mint an API token at /admin/security and export it.\n')
    process.exit(1)
  }
  let tools = []
  try {
    tools = await fetchTools()
  } catch (e) {
    process.stderr.write('ncn-mcp: ' + e.message + '\n')
    process.exit(1)
  }
  const byName = new Map(tools.map((t) => [t.name, t]))

  const server = new Server(
    { name: 'ncn', version: '0.1.0' },
    { capabilities: { tools: {} } },
  )

  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools }))

  server.setRequestHandler(CallToolRequestSchema, async (req) => {
    const { name, arguments: args } = req.params
    if (!byName.has(name)) {
      return { isError: true, content: [{ type: 'text', text: 'unknown tool: ' + name }] }
    }
    try {
      const out = await callTool(name, args)
      return { content: [{ type: 'text', text: String(out) }] }
    } catch (e) {
      return { isError: true, content: [{ type: 'text', text: 'error: ' + e.message }] }
    }
  })

  const transport = new StdioServerTransport()
  await server.connect(transport)
  process.stderr.write(`ncn-mcp: connected · ${tools.length} tools · base ${BASE}\n`)
}

main().catch((e) => {
  process.stderr.write('ncn-mcp fatal: ' + (e?.stack || e) + '\n')
  process.exit(1)
})
