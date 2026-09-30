// Shim configuration. The session comes from the agent's environment; the service address and
// the machine credential come from the environment or from ~/.config/coop/env (mode 0600).
import { readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { AGENT_RE, TOKEN_RE } from '@coop/core'

export interface ShimConfig {
  /** The shared session, or undefined when this agent is in no session. */
  readonly session: string | undefined
  readonly agent: string
  /** True when Claude Code loads this server as a channel, so messages can be pushed. */
  readonly push: boolean
  readonly url: string | undefined
  readonly token: string | undefined
}

export const DEFAULT_ENV_FILE = join(homedir(), '.config', 'coop', 'env')

/** Read KEY=VALUE lines. The file holds a credential, so it must not be readable by others. */
export function readEnvFile(path: string, warn: (m: string) => void): Record<string, string> {
  let text: string
  try {
    if ((statSync(path).mode & 0o077) !== 0) {
      warn(`${path} is readable by other users; run: chmod 600 ${path}`)
      return {}
    }
    text = readFileSync(path, 'utf8')
  } catch {
    return {}
  }
  const out: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (line === '' || line.startsWith('#')) continue
    const eq = line.indexOf('=')
    if (eq < 1) continue
    const value = line.slice(eq + 1).trim()
    out[line.slice(0, eq).trim()] = value.replace(/^(['"])(.*)\1$/, '$2')
  }
  return out
}

export function loadConfig(
  env: NodeJS.ProcessEnv = process.env,
  file: string = DEFAULT_ENV_FILE,
  warn: (m: string) => void = (m) => console.error(`coop: ${m}`),
): ShimConfig {
  const fromFile = readEnvFile(file, warn)
  const pick = (k: string) => {
    const v = env[k] ?? fromFile[k]
    return v === undefined || v === '' ? undefined : v
  }
  let session = pick('COOP_SESSION')
  if (session !== undefined && !TOKEN_RE.test(session)) {
    warn(`COOP_SESSION "${session}" is not a valid session name; ignoring it`)
    session = undefined
  }
  let agent = pick('COOP_AGENT') ?? 'agent'
  if (!AGENT_RE.test(agent)) {
    warn(`COOP_AGENT "${agent}" is not a valid agent name; using "agent"`)
    agent = 'agent'
  }
  return {
    session,
    agent,
    push: pick('COOP_PUSH') === '1',
    url: pick('COOP_URL')?.replace(/\/+$/, ''),
    token: pick('COOP_TOKEN'),
  }
}
