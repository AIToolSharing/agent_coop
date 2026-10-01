#!/usr/bin/env node
// coop-mcp: the local MCP server (stdio) that gives an agent the shared-session tools, and the
// commands that set a project up. Without arguments it serves; see cli.ts for the commands.
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js'
import { runCommand } from './cli.js'
import { loadConfig } from './config.js'
import { Shim } from './server.js'

const [cmd, ...args] = process.argv.slice(2)
if (cmd !== undefined) {
  process.exitCode = await runCommand(cmd, args, {
    cwd: process.cwd(),
    print: (l) => console.log(l),
    error: (l) => console.error(l),
  })
} else {
  const shim = new Shim({ config: loadConfig() })
  const transport = new StdioServerTransport()
  transport.onclose = () => void shim.stop().then(() => process.exit(0))
  for (const sig of ['SIGINT', 'SIGTERM'] as const) {
    process.once(sig, () => void shim.stop().then(() => process.exit(0)))
  }
  await shim.server.connect(transport)
}
