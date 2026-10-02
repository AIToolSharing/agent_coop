// Put the two commands on the PATH: `coop-mcp` (the shim, one file) and `coop-tui` (a launcher
// for the TUI in this clone). Builds first unless --no-build. The directory is ~/.local/bin, or
// COOP_BIN. Run it again after a `git pull`.
import { execFileSync } from 'node:child_process'
import { chmodSync, copyFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { homedir } from 'node:os'
import { delimiter, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('..', import.meta.url))
const bin = process.env.COOP_BIN ?? join(homedir(), '.local', 'bin')

if (!process.argv.includes('--no-build')) {
  execFileSync('npm', ['run', '-s', 'bundle'], { cwd: root, stdio: 'inherit' })
}
mkdirSync(bin, { recursive: true })

const mcp = join(bin, 'coop-mcp')
copyFileSync(join(root, 'plugin', 'coop-mcp.mjs'), mcp)
chmodSync(mcp, 0o755)

const tui = join(bin, 'coop-tui')
writeFileSync(
  tui,
  [
    '#!/usr/bin/env bash',
    `# The coop operator TUI, from the clone at ${root}`,
    '# React must run its production build in the TUI; see packages/tui/src/bin.ts.',
    'export NODE_ENV=production',
    `exec node "${join(root, 'packages', 'tui', 'dist', 'bin.js')}" "$@"`,
    '',
  ].join('\n'),
)
chmodSync(tui, 0o755)

console.log(`installed ${mcp}`)
console.log(`installed ${tui}`)
const onPath = (process.env.PATH ?? '').split(delimiter).includes(bin)
if (!onPath) console.log(`add it to your PATH:  export PATH="${bin}:$PATH"`)
console.log('next:  coop-mcp login <url> <token>      # the machine token from the operator')
console.log('       coop-tui login <url> <token>      # your operator token, for the TUI')
console.log('       claude mcp add --scope user coop -- coop-mcp')
