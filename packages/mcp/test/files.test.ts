// The plugin files and the bundle are outside the packages; these tests keep them honest.
import { spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from 'vitest'
import { FORBIDDEN_RE } from '../src/server.js'

const root = fileURLToPath(new URL('../../../', import.meta.url))
const read = (p: string) => readFileSync(join(root, p), 'utf8')

test('the skill names no part of how the service works', () => {
  const skill = read('plugin/skills/coop/SKILL.md')
  expect(skill.match(FORBIDDEN_RE)).toBeNull()
  expect(skill).toMatch(/^---\nname: coop\ndescription: .+\n---\n/)
})

test('the plugin manifest, the server entry, and the marketplace fit together', () => {
  const manifest = JSON.parse(read('plugin/.claude-plugin/plugin.json'))
  expect(manifest.name).toBe('coop')
  expect(manifest.channels).toEqual([{ server: 'coop', displayName: 'coop' }])
  const mcp = JSON.parse(read('plugin/.mcp.json'))
  expect(mcp.mcpServers.coop.command).toBe('node')
  expect(mcp.mcpServers.coop.args[0]).toMatch(/^\$\{CLAUDE_PLUGIN_ROOT\}\/coop-mcp\.mjs$/)
  const market = JSON.parse(read('.claude-plugin/marketplace.json'))
  expect(market.name).toBe('coop')
  expect(market.plugins).toContainEqual(
    expect.objectContaining({ name: 'coop', source: './plugin' }),
  )
})

test('the committed bundle runs on its own', () => {
  const r = spawnSync('node', [join(root, 'plugin/coop-mcp.mjs'), 'help'], { encoding: 'utf8' })
  expect(r.status).toBe(0)
  expect(r.stdout).toContain('coop-mcp session')
  expect(r.stdout).toContain('coop-mcp claude')
})

test('npm run cli puts coop-mcp and coop-tui into COOP_BIN', () => {
  const bin = mkdtempSync(join(tmpdir(), 'coop-bin-'))
  // A PATH with node but without `bin`, so the script must say how to add it.
  const PATH = `${dirname(process.execPath)}:/usr/bin:/bin`
  const r = spawnSync(process.execPath, [join(root, 'scripts/install-cli.mjs'), '--no-build'], {
    encoding: 'utf8',
    env: { ...process.env, COOP_BIN: bin, PATH },
  })
  expect(r.status).toBe(0)
  expect(r.stdout).toContain(`export PATH="${bin}:$PATH"`)
  expect(statSync(join(bin, 'coop-mcp')).mode & 0o111).toBe(0o111)
  expect(readFileSync(join(bin, 'coop-mcp'), 'utf8')).toBe(read('plugin/coop-mcp.mjs'))
  const launcher = readFileSync(join(bin, 'coop-tui'), 'utf8')
  expect(launcher).toContain('packages/tui/dist/bin.js')
  // React must run its production build in the TUI; the launcher says so.
  expect(launcher).toMatch(/NODE_ENV=.*production/)
  const help = spawnSync(join(bin, 'coop-mcp'), ['help'], {
    encoding: 'utf8',
    env: { ...process.env, PATH },
  })
  expect(help.status).toBe(0)
})
