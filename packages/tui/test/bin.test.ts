// coop-tui must run React's production build: the development build keeps memory on every
// repaint (Ink issue #869). The entry sets NODE_ENV before anything imports React.
import { spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test } from 'vitest'

const pkg = fileURLToPath(new URL('../', import.meta.url))

test('the entry sets NODE_ENV to production before it imports anything', () => {
  const src = readFileSync(join(pkg, 'src/bin.ts'), 'utf8')
  expect(src).not.toMatch(/^import /m)
  const set = src.indexOf("process.env.NODE_ENV ??= 'production'")
  const load = src.indexOf("await import('./main.js')")
  expect(set).toBeGreaterThan(-1)
  expect(load).toBeGreaterThan(set)
})

test('dist/bin.js loads the production build of React when NODE_ENV is unset', () => {
  const entry = join(pkg, 'dist/bin.js')
  expect(existsSync(entry), 'dist/bin.js is missing: run npm run build').toBe(true)
  // A preload that reports, at exit, which React build the process ran. The ESM loader also
  // pre-parses the other build to find its exports; only `loaded` entries were executed.
  const probe = join(mkdtempSync(join(tmpdir(), 'coop-bin-')), 'probe.cjs')
  writeFileSync(
    probe,
    [
      "process.on('exit', () => {",
      '  const ran = Object.entries(require.cache)',
      '    .filter(([f, m]) => m.loaded && /react\\.(development|production)\\.js$/.test(f))',
      '    .map(([f]) => f)',
      "  process.stderr.write('REACT ' + ran.join(',') + '\\n')",
      '})',
      '',
    ].join('\n'),
  )
  const env = { ...process.env }
  delete env.NODE_ENV
  const r = spawnSync(process.execPath, ['-r', probe, entry, '--help'], { encoding: 'utf8', env })
  expect(r.stderr).toMatch(/REACT .*react\.production\.js/)
  expect(r.stderr).not.toMatch(/react\.development\.js/)
})
