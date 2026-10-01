// The coop-mcp commands: they write files for the person, so they are tested on a temp tree.
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, test } from 'vitest'
import { probe, runCommand, UsageError, writeProjectFile } from '../src/cli.js'
import { loadConfig } from '../src/config.js'

function repo(): { root: string; sub: string } {
  const root = mkdtempSync(join(tmpdir(), 'coop-cli-'))
  mkdirSync(join(root, '.git'))
  const sub = join(root, 'pkg')
  mkdirSync(sub)
  return { root, sub }
}

async function run(cmd: string, args: string[], cwd: string, fetchFn?: typeof fetch) {
  const out: string[] = []
  const err: string[] = []
  const io = { cwd, print: (l: string) => out.push(l), error: (l: string) => err.push(l) }
  const code = await runCommand(cmd, args, fetchFn === undefined ? io : { ...io, fetch: fetchFn })
  return { code, out, err }
}

describe('coop-mcp session', () => {
  test('writes .coop where it runs, ignores it in git once, and the shim reads it back', async () => {
    const r = repo()
    writeFileSync(join(r.root, '.gitignore'), 'node_modules/')
    expect((await run('session', ['build-42', '--agent', 'reviewer'], r.sub)).code).toBe(0)
    expect(readFileSync(join(r.sub, '.coop'), 'utf8')).toBe(
      'COOP_SESSION=build-42\nCOOP_AGENT=reviewer\n',
    )
    expect(readFileSync(join(r.root, '.gitignore'), 'utf8')).toBe('node_modules/\n.coop\n')
    // A second run does not add a second line.
    writeProjectFile(r.sub, 'other')
    expect(readFileSync(join(r.root, '.gitignore'), 'utf8')).toBe('node_modules/\n.coop\n')
    const c = loadConfig({}, '/nonexistent', () => undefined, join(r.sub, 'deeper-not-existing'))
    expect(c.session).toBe('other')
  })

  test('refuses a bad name and a missing name with exit code 2', async () => {
    const r = repo()
    expect(() => writeProjectFile(r.sub, 'Bad Name')).toThrow(UsageError)
    expect((await run('session', ['Bad Name'], r.sub)).code).toBe(2)
    expect((await run('session', [], r.sub)).code).toBe(2)
    expect((await run('session', ['a', 'b'], r.sub)).code).toBe(2)
    expect((await run('nosuch', [], r.sub)).code).toBe(2)
    expect((await run('help', [], r.sub)).code).toBe(0)
  })
})

const answer =
  (status: number): typeof fetch =>
  async () =>
    new Response('', { status })
const down: typeof fetch = async () => {
  throw new TypeError('fetch failed')
}

describe('coop-mcp login', () => {
  test('the probe reads the service answer', async () => {
    expect(await probe('http://x', 'm.s', answer(403))).toBe('ok')
    expect(await probe('http://x', 'm.s', answer(401))).toBe('bad_token')
    expect(await probe('http://x', 'm.s', answer(503))).toEqual({ unexpected: 503 })
    expect(await probe('http://x', 'm.s', down)).toBe('unreachable')
  })

  test('a bad address or token form is a usage error; a refused or unreachable service is 1', async () => {
    const r = repo()
    expect((await run('login', ['not a url', 'm.s'], r.sub)).code).toBe(2)
    expect((await run('login', ['ftp://x', 'm.s'], r.sub)).code).toBe(2)
    expect((await run('login', ['https://x', 'no-dot'], r.sub)).code).toBe(2)
    expect((await run('login', ['https://x'], r.sub)).code).toBe(2)
    const refused = await run('login', ['https://x', 'm.s'], r.sub, answer(401))
    expect(refused.code).toBe(1)
    expect(refused.err.join(' ')).toContain('refused the token')
    const unreachable = await run('login', ['https://x', 'm.s'], r.sub, down)
    expect(unreachable.code).toBe(1)
    expect(unreachable.err.join(' ')).toContain('cannot reach')
  })
})
