// The skill and the launcher are files outside the packages; these tests keep them honest.
import { spawnSync } from 'node:child_process'
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from 'vitest'
import { FORBIDDEN_RE } from '../src/server.js'

const root = fileURLToPath(new URL('../../../', import.meta.url))

test('the skill names no part of how the service works', () => {
  const skill = readFileSync(join(root, 'skill/coop/SKILL.md'), 'utf8')
  expect(skill.match(FORBIDDEN_RE)).toBeNull()
  expect(skill).toMatch(/^---\nname: coop\ndescription: .+\n---\n/)
})

test('coop-claude sets the session, turns push on, and loads the channel', () => {
  const dir = mkdtempSync(join(tmpdir(), 'coop-bin-'))
  const fake = join(dir, 'claude')
  writeFileSync(fake, '#!/usr/bin/env bash\necho "$COOP_SESSION|$COOP_PUSH|$*"\n')
  chmodSync(fake, 0o755)
  const r = spawnSync(join(root, 'bin/coop-claude'), ['demo', '--model', 'x'], {
    encoding: 'utf8',
    env: { ...process.env, PATH: `${dir}:${process.env.PATH}` },
  })
  expect(r.stdout.trim()).toBe(
    'demo|1|--dangerously-load-development-channels server:coop --model x',
  )
  const usage = spawnSync(join(root, 'bin/coop-claude'), [], { encoding: 'utf8' })
  expect(usage.status).toBe(2)
})
