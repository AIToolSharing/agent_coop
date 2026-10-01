// Where the TUI gets the service address and the operator token: flags, then the environment,
// then ~/.config/coop/env (shared with the shim, which keeps its own keys there).
import { parseArgs } from 'node:util'
import { DEFAULT_ENV_FILE, readEnvFile, updateEnvFile } from '@coop/core'

export interface OperatorConfig {
  readonly url: string
  readonly token: string
}

export const USAGE = `usage: coop-tui [--url <url>] [--token <token>]
       coop-tui login <url> <token>       store the service address and the operator token

The address and the token also come from COOP_URL and COOP_OPERATOR_TOKEN, or from
${DEFAULT_ENV_FILE}. An operator token comes from: coop-hub token add --operator <name>.`

export type Parsed =
  | { readonly kind: 'run'; readonly config: OperatorConfig }
  | { readonly kind: 'login'; readonly url: string; readonly token: string }
  | { readonly kind: 'usage'; readonly error: string | undefined }

export function parse(
  argv: string[],
  env: NodeJS.ProcessEnv = process.env,
  file: string = DEFAULT_ENV_FILE,
  warn: (m: string) => void = (m) => console.error(`coop-tui: ${m}`),
): Parsed {
  let values: { url?: string; token?: string; help?: boolean }
  let positionals: string[]
  try {
    ;({ values, positionals } = parseArgs({
      args: argv,
      options: { url: { type: 'string' }, token: { type: 'string' }, help: { type: 'boolean' } },
      allowPositionals: true,
    }))
  } catch (err) {
    return { kind: 'usage', error: err instanceof Error ? err.message : String(err) }
  }
  if (values.help) return { kind: 'usage', error: undefined }
  if (positionals[0] === 'login') {
    const [, url, token, extra] = positionals
    if (url === undefined || token === undefined || extra !== undefined) {
      return { kind: 'usage', error: 'login takes a URL and a token' }
    }
    return { kind: 'login', url: url.replace(/\/+$/, ''), token }
  }
  if (positionals.length > 0) return { kind: 'usage', error: `unknown command ${positionals[0]}` }
  const fromFile = readEnvFile(file, warn)
  const url = values.url ?? env.COOP_URL ?? fromFile.COOP_URL
  const token = values.token ?? env.COOP_OPERATOR_TOKEN ?? fromFile.COOP_OPERATOR_TOKEN
  if (url === undefined || url === '' || token === undefined || token === '') {
    return { kind: 'usage', error: 'no service address or no operator token' }
  }
  return { kind: 'run', config: { url: url.replace(/\/+$/, ''), token } }
}

export type LoginResult =
  | 'ok'
  | 'bad_token'
  | 'machine_token'
  | 'unreachable'
  | { unexpected: number }

/** Check the token against the admin API, then store it. */
export async function login(
  url: string,
  token: string,
  file: string = DEFAULT_ENV_FILE,
  fetchFn: typeof fetch = fetch,
): Promise<LoginResult> {
  let res: Response
  try {
    res = await fetchFn(`${url}/v1/admin/sessions`, {
      headers: { authorization: `Bearer ${token}` },
    })
  } catch {
    return 'unreachable'
  }
  if (res.status === 401) return 'bad_token'
  if (res.status === 403) return 'machine_token'
  if (!res.ok) return { unexpected: res.status }
  updateEnvFile(file, { COOP_URL: url, COOP_OPERATOR_TOKEN: token })
  return 'ok'
}
