// Shim configuration. The session and the agent name come from the agent's environment or from a
// `.coop` file in the project; the service address and the machine credential come from the
// environment or from ~/.config/coop/env (mode 0600).
import { existsSync, readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { basename, dirname, join } from 'node:path'
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
/** The per-project file. It holds the session and the agent name, never a credential. */
export const PROJECT_FILE = '.coop'

/** Parse KEY=VALUE lines. Blank lines and `#` comments are skipped; quotes around a value go. */
export function parseEnv(text: string): Record<string, string> {
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

/** Read the credential file. It must not be readable by others. */
export function readEnvFile(path: string, warn: (m: string) => void): Record<string, string> {
  try {
    if ((statSync(path).mode & 0o077) !== 0) {
      warn(`${path} is readable by other users; run: chmod 600 ${path}`)
      return {}
    }
    return parseEnv(readFileSync(path, 'utf8'))
  } catch {
    return {}
  }
}

/** The directory that holds `.git`, from `cwd` upwards, or undefined. */
export function findGitRoot(cwd: string): string | undefined {
  for (const dir of ancestors(cwd)) if (existsSync(join(dir, '.git'))) return dir
  return undefined
}

/**
 * The nearest `.coop` file from `cwd` upwards. The search stops at the git root, so a file in a
 * parent of the repository does not apply to it.
 */
export function findProjectFile(cwd: string): string | undefined {
  for (const dir of ancestors(cwd)) {
    const file = join(dir, PROJECT_FILE)
    if (existsSync(file)) return file
    if (existsSync(join(dir, '.git'))) return undefined
  }
  return undefined
}

function* ancestors(start: string): Generator<string> {
  let dir = start
  for (;;) {
    yield dir
    const parent = dirname(dir)
    if (parent === dir) return
    dir = parent
  }
}

export function loadConfig(
  env: NodeJS.ProcessEnv = process.env,
  file: string = DEFAULT_ENV_FILE,
  warn: (m: string) => void = (m) => console.error(`coop: ${m}`),
  cwd: string = process.cwd(),
): ShimConfig {
  const fromFile = readEnvFile(file, warn)
  const projectFile = findProjectFile(cwd)
  const fromProject = projectFile === undefined ? {} : readProjectFile(projectFile)
  const pick = (k: string, ...sources: Record<string, string | undefined>[]) => {
    for (const s of [env, ...sources]) {
      const v = s[k]
      if (v !== undefined && v !== '') return v
    }
    return undefined
  }
  const where = (k: string) =>
    env[k] !== undefined && env[k] !== '' ? k : `${k} in ${projectFile ?? PROJECT_FILE}`
  let session = pick('COOP_SESSION', fromProject)
  if (session !== undefined && !TOKEN_RE.test(session)) {
    warn(`${where('COOP_SESSION')} "${session}" is not a valid session name; ignoring it`)
    session = undefined
  }
  const fallback = defaultAgentName(cwd)
  let agent = pick('COOP_AGENT', fromProject) ?? fallback
  if (!AGENT_RE.test(agent)) {
    warn(`${where('COOP_AGENT')} "${agent}" is not a valid agent name; using "${fallback}"`)
    agent = fallback
  }
  return {
    session,
    agent,
    push: pick('COOP_PUSH', fromFile) === '1',
    url: pick('COOP_URL', fromFile)?.replace(/\/+$/, ''),
    token: pick('COOP_TOKEN', fromFile),
  }
}

/**
 * The agent name when none is set: the directory name, made valid. Two agents in different
 * projects on one machine then get different names without any setting.
 */
export function defaultAgentName(cwd: string): string {
  const name = basename(cwd)
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64)
  return AGENT_RE.test(name) ? name : 'agent'
}

function readProjectFile(path: string): Record<string, string> {
  try {
    return parseEnv(readFileSync(path, 'utf8'))
  } catch {
    return {}
  }
}
