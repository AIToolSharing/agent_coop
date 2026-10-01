// Shim configuration. The session and the agent name come from the agent's environment or from a
// `.coop` file in the project; the service address and the machine credential come from the
// environment or from ~/.config/coop/env (mode 0600).
import { existsSync, readFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { AGENT_RE, DEFAULT_ENV_FILE, parseEnv, readEnvFile, TOKEN_RE } from '@coop/core'

export interface ShimConfig {
  /** The shared session, or undefined when this agent is in no session. */
  readonly session: string | undefined
  readonly agent: string
  /** True when Claude Code loads this server as a channel, so messages can be pushed. */
  readonly push: boolean
  readonly url: string | undefined
  readonly token: string | undefined
}

export { DEFAULT_ENV_FILE } from '@coop/core'
/** The per-project file. It holds the session and the agent name, never a credential. */
export const PROJECT_FILE = '.coop'

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
