// The coop-mcp commands that set a project up. The MCP server itself is in main.ts.
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { parseArgs } from 'node:util'
import { AGENT_RE, TOKEN_RE } from '@coop/core'
import { findGitRoot, PROJECT_FILE } from './config.js'

/** A wrong command line. The message is for the person, and the exit code is 2. */
export class UsageError extends Error {}

export const USAGE = `usage: coop-mcp                                 serve (an MCP client starts it this way)
       coop-mcp session <name> [--agent <name>]  agents started in this directory join <name>`

/**
 * Write `.coop` in `cwd` and add it to the repository's .gitignore (a session is the person's,
 * not the project's). Returns the lines to show.
 */
export function writeProjectFile(cwd: string, session: string, agent?: string): string[] {
  if (!TOKEN_RE.test(session)) {
    throw new UsageError(`"${session}" is not a valid session name: a-z, 0-9, _ and -, up to 64`)
  }
  if (agent !== undefined && !AGENT_RE.test(agent)) {
    throw new UsageError(`"${agent}" is not a valid agent name: a-z, 0-9, _ and -, up to 64`)
  }
  const file = join(cwd, PROJECT_FILE)
  const lines = [`COOP_SESSION=${session}`]
  if (agent !== undefined) lines.push(`COOP_AGENT=${agent}`)
  writeFileSync(file, `${lines.join('\n')}\n`)
  const out = [`wrote ${file}`]
  const root = findGitRoot(cwd)
  if (root !== undefined && ignoreProjectFile(root)) {
    out.push(`added ${PROJECT_FILE} to ${join(root, '.gitignore')}`)
  }
  out.push(`an agent started in ${cwd} (or below) joins session ${session}`)
  return out
}

/** Add `.coop` to the .gitignore at `root` if it is not there. True if the file changed. */
function ignoreProjectFile(root: string): boolean {
  const path = join(root, '.gitignore')
  const text = existsSync(path) ? readFileSync(path, 'utf8') : ''
  const listed = text
    .split('\n')
    .some((l) => l.trim() === PROJECT_FILE || l.trim() === `/${PROJECT_FILE}`)
  if (listed) return false
  const sep = text === '' || text.endsWith('\n') ? '' : '\n'
  appendFileSync(path, `${sep}${PROJECT_FILE}\n`)
  return true
}

/** Run one command. Returns the exit code. */
export function runCommand(
  cmd: string,
  args: string[],
  io: { cwd: string; print: (line: string) => void; error: (line: string) => void },
): number {
  try {
    if (cmd === 'session') {
      const { values, positionals } = parseArgs({
        args,
        options: { agent: { type: 'string' } },
        allowPositionals: true,
      })
      const [session, extra] = positionals
      if (session === undefined || extra !== undefined) throw new UsageError(USAGE)
      for (const l of writeProjectFile(io.cwd, session, values.agent)) io.print(l)
      return 0
    }
    if (cmd === 'help' || cmd === '--help' || cmd === '-h') {
      io.print(USAGE)
      return 0
    }
    throw new UsageError(USAGE)
  } catch (err) {
    if (err instanceof UsageError) {
      io.error(err.message)
      return 2
    }
    // parseArgs throws on an unknown option; its message names the option.
    io.error(err instanceof Error ? err.message : String(err))
    return 2
  }
}
