// The credential file ~/.config/coop/env: KEY=VALUE lines. The shim and the TUI share it.
import { chmodSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { dirname, join } from 'node:path'

export const DEFAULT_ENV_FILE = join(homedir(), '.config', 'coop', 'env')

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

/** Read the credential file. It must not be readable by others; else it is ignored with a warning. */
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

/**
 * Set keys in the credential file and keep the others (directory 0700, file 0600). Comments
 * are not kept: the file is written by these commands, not by hand.
 */
export function updateEnvFile(path: string, values: Record<string, string>): void {
  let current: Record<string, string> = {}
  try {
    current = parseEnv(readFileSync(path, 'utf8'))
  } catch {
    // A missing file starts empty.
  }
  const next = { ...current, ...values }
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 })
  const text = Object.entries(next)
    .map(([k, v]) => `${k}=${v}\n`)
    .join('')
  writeFileSync(path, text, { mode: 0o600 })
  chmodSync(path, 0o600)
}
