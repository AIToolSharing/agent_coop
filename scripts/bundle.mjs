// Build the shim into one file, plugin/coop-mcp.mjs, from the compiled output. With --check,
// build in memory and fail when the committed file differs, so a source change cannot ship
// without a new bundle. Run `tsc -b` first.
import { readFileSync, writeFileSync } from 'node:fs'
import { rolldown } from 'rolldown'

const OUT = 'plugin/coop-mcp.mjs'
const bundle = await rolldown({
  input: 'packages/mcp/dist/main.js',
  platform: 'node',
  logLevel: 'silent',
})
const { output } = await bundle.generate({ format: 'esm' })
await bundle.close()
const code = output[0].code

if (process.argv.includes('--check')) {
  let committed = ''
  try {
    committed = readFileSync(OUT, 'utf8')
  } catch {
    // A missing file is stale too.
  }
  if (committed !== code) {
    console.error(`${OUT} is stale: run \`npm run bundle\` and commit it`)
    process.exit(1)
  }
  console.log(`${OUT} is up to date`)
} else {
  writeFileSync(OUT, code)
  console.log(`wrote ${OUT} (${(code.length / 1024).toFixed(0)} KiB)`)
}
