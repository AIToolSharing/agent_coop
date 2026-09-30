#!/usr/bin/env node
// coop-mcp: the local MCP server (stdio) that gives an agent the shared-session tools.
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { loadConfig } from './config.js'
import { Shim } from './server.js'

const shim = new Shim({ config: loadConfig() })
const transport = new StdioServerTransport()
transport.onclose = () => void shim.stop().then(() => process.exit(0))
for (const sig of ['SIGINT', 'SIGTERM'] as const) {
  process.once(sig, () => void shim.stop().then(() => process.exit(0)))
}
await shim.server.connect(transport)
