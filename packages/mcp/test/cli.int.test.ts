// `coop-mcp login` against a real hub: the probe must tell a good token from a bad one.
import { mkdtempSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, expect, test } from 'vitest'
import { type Harness, startHub } from '../../hub/test/harness.js'
import { runCommand } from '../src/cli.js'
import { loadConfig } from '../src/config.js'

let h: Harness
beforeAll(async () => {
  h = await startHub()
})
afterAll(async () => {
  await h?.stop()
})

test('a good token is stored with mode 0600 and the shim reads it; a bad one is refused', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'coop-login-'))
  const envFile = join(dir, 'cfg', 'env')
  const out: string[] = []
  const err: string[] = []
  const io = {
    cwd: dir,
    envFile,
    print: (l: string) => out.push(l),
    error: (l: string) => err.push(l),
  }
  const token = await h.token('laptop')

  expect(await runCommand('login', [`${h.base}/`, `${token}`], io)).toBe(0)
  expect(statSync(envFile).mode & 0o777).toBe(0o600)
  expect(readFileSync(envFile, 'utf8')).toBe(`COOP_URL=${h.base}\nCOOP_TOKEN=${token}\n`)
  expect(out.join('\n')).toContain('claude mcp add --scope user coop')
  const c = loadConfig({}, envFile, () => undefined, dir)
  expect(c.url).toBe(h.base)
  expect(c.token).toBe(token)

  expect(
    await runCommand('login', [h.base, 'laptop.wrong'], { ...io, envFile: join(dir, 'x') }),
  ).toBe(1)
  expect(err.join(' ')).toContain('refused the token')
})
