// The coop-mcp commands that set a machine and a project up. The MCP server itself is in main.ts.

import { spawnSync } from 'node:child_process'
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'
import { AGENT_RE, DEFAULT_ENV_FILE, TOKEN_RE, updateEnvFile } from '@coop/core'
import { findGitRoot, PROJECT_FILE } from './config.js'

/** A wrong command line. The message is for the person, and the exit code is 2. */
export class UsageError extends Error {}

export const USAGE = `usage: coop-mcp                                    serve (an MCP client starts it this way)
       coop-mcp login <url> <token>                 store this machine's credential and check it
       coop-mcp session <name> [--agent <name>]     agents started in this directory join <name>
       coop-mcp claude [<session>] [claude args...] start Claude Code with messages pushed in

coop-mcp claude loads the channel with --dangerously-load-development-channels. The entry is
server:coop; set COOP_CHANNEL=plugin:coop@coop when coop is installed as a plugin.`

/** The channel entry for a server registered with `claude mcp add`. */
export const DEFAULT_CHANNEL = 'server:coop'

export type ProbeResult = 'ok' | 'bad_token' | 'unreachable' | { readonly unexpected: number }

/**
 * Check a credential against the service with one request. The service answers 401 to a bad
 * token; a good token gets a 4xx about the probe session itself, which is the expected answer.
 */
export async function probe(
  url: string,
  token: string,
  fetchFn: typeof fetch = fetch,
): Promise<ProbeResult> {
  let res: Response
  try {
    res = await fetchFn(`${url}/v1/sessions/_probe?agent=_probe`, {
      headers: { authorization: `Bearer ${token}` },
    })
  } catch {
    return 'unreachable'
  }
  if (res.status === 401) return 'bad_token'
  if (res.status >= 500) return { unexpected: res.status }
  return 'ok'
}

/** Store the machine credential; other keys of the file (the operator's) stay. */
export function writeCredentialFile(path: string, url: string, token: string): void {
  updateEnvFile(path, { COOP_URL: url, COOP_TOKEN: token })
}

const TOKEN_FORM = /^[a-z0-9_-]{1,64}\.\S+$/

function parseUrl(s: string): string {
  let u: URL
  try {
    u = new URL(s)
  } catch {
    throw new UsageError(`"${s}" is not a URL`)
  }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') {
    throw new UsageError(`"${s}" must start with https://`)
  }
  return u.toString().replace(/\/+$/, '')
}

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

export interface CommandIo {
  readonly cwd: string
  readonly print: (line: string) => void
  readonly error: (line: string) => void
  /** The credential file; tests point it at a temp file. */
  readonly envFile?: string
  readonly fetch?: typeof fetch
  readonly env?: NodeJS.ProcessEnv
  /** Run a program in the foreground and return its exit code. */
  readonly exec?: (cmd: string, args: string[], env: NodeJS.ProcessEnv) => number
}

function execForeground(cmd: string, args: string[], env: NodeJS.ProcessEnv): number {
  const r = spawnSync(cmd, args, { stdio: 'inherit', env })
  if (r.error !== undefined) {
    console.error(`cannot start ${cmd}: ${r.error.message}`)
    return 127
  }
  return r.status ?? 1
}

/** The absolute path of the served program, for the `claude mcp add` line. */
const MAIN = fileURLToPath(new URL('./main.js', import.meta.url))

/** Run one command. Returns the exit code: 0 done, 1 the service said no, 2 wrong command line. */
export async function runCommand(cmd: string, args: string[], io: CommandIo): Promise<number> {
  try {
    if (cmd === 'login') {
      const [rawUrl, token, extra] = args
      if (rawUrl === undefined || token === undefined || extra !== undefined) {
        throw new UsageError(USAGE)
      }
      const url = parseUrl(rawUrl)
      if (!TOKEN_FORM.test(token)) {
        throw new UsageError(
          'the token has the form <machine>.<secret>, as `coop-hub token add` printed it',
        )
      }
      const r = await probe(url, token, io.fetch)
      if (r === 'bad_token') {
        io.error(`${url} refused the token; ask the operator for a new one (coop-hub token add)`)
        return 1
      }
      if (r === 'unreachable') {
        io.error(`cannot reach ${url}; check the address and the network`)
        return 1
      }
      if (r !== 'ok') {
        io.error(`${url} answered ${r.unexpected}; the service may be down, try again later`)
        return 1
      }
      const file = io.envFile ?? DEFAULT_ENV_FILE
      writeCredentialFile(file, url, token)
      io.print(`wrote ${file} (mode 0600)`)
      io.print('next, once per user, one of:')
      io.print('  in Claude Code: /plugin marketplace add AIToolSharing/agent_coop')
      io.print('                  /plugin install coop@coop')
      io.print(`  any MCP client: claude mcp add --scope user coop -- node ${MAIN}`)
      io.print('then, in each project:')
      io.print('  coop-mcp session <name>   # agents started there join <name>')
      io.print('  coop-mcp claude           # Claude Code with messages pushed in')
      return 0
    }
    if (cmd === 'claude') {
      const [first, ...rest] = args
      const session = first !== undefined && !first.startsWith('-') ? first : undefined
      const base = io.env ?? process.env
      const env: NodeJS.ProcessEnv = { ...base, COOP_PUSH: '1' }
      if (session !== undefined) env.COOP_SESSION = session
      const channel = base.COOP_CHANNEL ?? DEFAULT_CHANNEL
      const claudeArgs = [
        '--dangerously-load-development-channels',
        channel,
        ...(session === undefined ? args : rest),
      ]
      return (io.exec ?? execForeground)('claude', claudeArgs, env)
    }
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
