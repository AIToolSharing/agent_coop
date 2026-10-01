// The coop-mcp commands: they write files for the person, so they are tested on a temp tree.
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, test } from 'vitest'
import { runCommand, UsageError, writeProjectFile } from '../src/cli.js'
import { loadConfig } from '../src/config.js'

function repo(): { root: string; sub: string } {
  const root = mkdtempSync(join(tmpdir(), 'coop-cli-'))
  mkdirSync(join(root, '.git'))
  const sub = join(root, 'pkg')
  mkdirSync(sub)
  return { root, sub }
}

function run(cmd: string, args: string[], cwd: string) {
  const out: string[] = []
  const err: string[] = []
  const code = runCommand(cmd, args, { cwd, print: (l) => out.push(l), error: (l) => err.push(l) })
  return { code, out, err }
}

describe('coop-mcp session', () => {
  test('writes .coop where it runs, ignores it in git once, and the shim reads it back', () => {
    const r = repo()
    writeFileSync(join(r.root, '.gitignore'), 'node_modules/')
    expect(run('session', ['build-42', '--agent', 'reviewer'], r.sub).code).toBe(0)
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

  test('refuses a bad name and a missing name with exit code 2', () => {
    const r = repo()
    expect(() => writeProjectFile(r.sub, 'Bad Name')).toThrow(UsageError)
    expect(run('session', ['Bad Name'], r.sub).code).toBe(2)
    expect(run('session', [], r.sub).code).toBe(2)
    expect(run('session', ['a', 'b'], r.sub).code).toBe(2)
    expect(run('nosuch', [], r.sub).code).toBe(2)
    expect(run('help', [], r.sub).code).toBe(0)
  })
})
