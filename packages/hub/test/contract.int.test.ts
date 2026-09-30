// Property-based contract test of the hub against its own /openapi.json (Schemathesis).
// Slow and needs uvx, so it runs only with COOP_CONTRACT=1 (npm run contract).
import { spawn } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { afterAll, beforeAll, expect, test } from 'vitest'
import { Api, type Harness, startHub } from './harness.js'

const enabled = process.env.COOP_CONTRACT === '1'
let h: Harness
let token: string

beforeAll(async () => {
  if (!enabled) return
  const high = { burst: 100_000, perSecond: 100_000 }
  h = await startHub({ join: high, msg: high, activity: high })
  token = await h.token('mac-1')
  await h.op.createSession('contract')
  // Live agents that schemathesis.toml names, so requests reach the hub's rules.
  const api = new Api(h.base, token)
  for (const agent of ['alice', 'bob']) await (await api.stream('contract', agent)).next()
})
afterAll(async () => {
  await h?.stop()
})

test.skipIf(!enabled)(
  'the hub conforms to its OpenAPI document',
  async () => {
    // Async spawn: the hub runs in this process and must keep answering.
    const r = await run('uvx', [
      'schemathesis@4.28.0',
      '--config-file',
      fileURLToPath(new URL('./schemathesis.toml', import.meta.url)),
      'run',
      `${h.base}/openapi.json`,
      '--url',
      h.base,
      '-H',
      `Authorization: Bearer ${token}`,
      '--exclude-path-regex',
      '/stream$',
      '--max-examples',
      process.env.COOP_CONTRACT_EXAMPLES ?? '100',
      '--seed',
      '1',
      '--no-color',
      ...(process.env.COOP_CONTRACT_HAR === undefined
        ? []
        : ['--report', 'har', '--report-har-path', process.env.COOP_CONTRACT_HAR]),
    ])
    const out = process.env.COOP_CONTRACT_OUT
    if (out !== undefined) writeFileSync(out, r.stdout + r.stderr)
    if (r.status !== 0) console.log(r.stdout, r.stderr)
    expect(r.status).toBe(0)
  },
  660_000,
)

function run(
  cmd: string,
  args: string[],
): Promise<{ status: number | null; stdout: string; stderr: string }> {
  return new Promise((resolve) => {
    const p = spawn(cmd, args, { stdio: ['ignore', 'pipe', 'pipe'] })
    let stdout = ''
    let stderr = ''
    p.stdout.on('data', (b: Buffer) => {
      stdout += b.toString()
    })
    p.stderr.on('data', (b: Buffer) => {
      stderr += b.toString()
    })
    p.on('close', (status) => resolve({ status, stdout, stderr }))
  })
}
