import { mkdtempSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { updateEnvFile } from '@coop/core'
import { describe, expect, test } from 'vitest'
import { login, parse } from '../src/config.js'

const noWarn = () => undefined
const answer =
  (status: number): typeof fetch =>
  async () =>
    new Response('{}', { status })

describe('where the TUI gets its address and token', () => {
  test('flags, then the environment, then the shared file', () => {
    const dir = mkdtempSync(join(tmpdir(), 'coop-tui-'))
    const file = join(dir, 'env')
    updateEnvFile(file, { COOP_URL: 'https://file', COOP_OPERATOR_TOKEN: 'f.t', COOP_TOKEN: 'm.s' })
    expect(parse([], {}, file, noWarn)).toEqual({
      kind: 'run',
      config: { url: 'https://file', token: 'f.t' },
    })
    expect(
      parse([], { COOP_URL: 'https://env/', COOP_OPERATOR_TOKEN: 'e.t' }, file, noWarn),
    ).toEqual({ kind: 'run', config: { url: 'https://env', token: 'e.t' } })
    expect(parse(['--url', 'https://flag', '--token', 'g.t'], {}, file, noWarn)).toEqual({
      kind: 'run',
      config: { url: 'https://flag', token: 'g.t' },
    })
    expect(parse([], {}, join(dir, 'none'), noWarn).kind).toBe('usage')
    expect(parse(['--bogus'], {}, file, noWarn).kind).toBe('usage')
    expect(parse(['login', 'https://x/', 'o.s'], {}, file, noWarn)).toEqual({
      kind: 'login',
      url: 'https://x',
      token: 'o.s',
    })
  })

  test('login checks the token and keeps the other keys of the file', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'coop-tui-'))
    const file = join(dir, 'env')
    writeFileSync(file, 'COOP_URL=https://old\nCOOP_TOKEN=m.s\n', { mode: 0o600 })
    expect(await login('https://x', 'o.s', file, answer(401))).toBe('bad_token')
    expect(await login('https://x', 'o.s', file, answer(403))).toBe('machine_token')
    expect(await login('https://x', 'o.s', file, answer(503))).toEqual({ unexpected: 503 })
    expect(readFileSync(file, 'utf8')).toBe('COOP_URL=https://old\nCOOP_TOKEN=m.s\n')
    expect(await login('https://x', 'o.s', file, answer(200))).toBe('ok')
    expect(readFileSync(file, 'utf8')).toBe(
      'COOP_URL=https://x\nCOOP_TOKEN=m.s\nCOOP_OPERATOR_TOKEN=o.s\n',
    )
    expect(statSync(file).mode & 0o777).toBe(0o600)
  })
})
