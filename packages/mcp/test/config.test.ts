// Where the shim gets its session, agent name and credential from.
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, test } from 'vitest'
import { loadConfig } from '../src/config.js'

const noWarn = () => undefined
/** A credential file that is not there: the tests here are about the project side. */
const noCred = '/nonexistent/coop/env'

function tree(): { root: string; project: string; deep: string } {
  const root = mkdtempSync(join(tmpdir(), 'coop-cfg-'))
  const project = join(root, 'proj')
  const deep = join(project, 'src', 'lib')
  mkdirSync(join(project, '.git'), { recursive: true })
  mkdirSync(deep, { recursive: true })
  return { root, project, deep }
}

describe('the .coop file', () => {
  test('a .coop file in a parent directory, up to the git root, sets session and agent', () => {
    const t = tree()
    writeFileSync(join(t.project, '.coop'), 'COOP_SESSION=build-42\nCOOP_AGENT=reviewer\n')
    const c = loadConfig({}, noCred, noWarn, t.deep)
    expect(c.session).toBe('build-42')
    expect(c.agent).toBe('reviewer')
  })

  test('a .coop file above the git root is not read', () => {
    const t = tree()
    writeFileSync(join(t.root, '.coop'), 'COOP_SESSION=outside\n')
    expect(loadConfig({}, noCred, noWarn, t.deep).session).toBeUndefined()
  })

  test('the environment wins over the .coop file', () => {
    const t = tree()
    writeFileSync(join(t.project, '.coop'), 'COOP_SESSION=from-file\nCOOP_AGENT=file-agent\n')
    const c = loadConfig(
      { COOP_SESSION: 'from-env', COOP_AGENT: 'env-agent' },
      noCred,
      noWarn,
      t.deep,
    )
    expect(c.session).toBe('from-env')
    expect(c.agent).toBe('env-agent')
  })

  test('a .coop file cannot set the service address or the credential', () => {
    const t = tree()
    writeFileSync(
      join(t.project, '.coop'),
      'COOP_SESSION=s\nCOOP_URL=https://evil.example\nCOOP_TOKEN=m.secret\n',
    )
    const c = loadConfig({}, noCred, noWarn, t.deep)
    expect(c.url).toBeUndefined()
    expect(c.token).toBeUndefined()
  })

  test('a bad session name in the file is reported and ignored', () => {
    const t = tree()
    writeFileSync(join(t.project, '.coop'), 'COOP_SESSION=Not Valid\n')
    const warnings: string[] = []
    const c = loadConfig({}, noCred, (m) => warnings.push(m), t.deep)
    expect(c.session).toBeUndefined()
    expect(warnings.join('\n')).toContain('Not Valid')
  })
})
