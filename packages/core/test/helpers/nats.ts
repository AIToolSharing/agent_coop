// Start a private nats-server for a test file. It uses deploy/nats.conf, so tests run the same
// accounts and users as production; only the port and the store directory differ.
import { type ChildProcess, spawn } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { type Broker, openBroker } from '../../src/broker/index.js'

const CONF = fileURLToPath(new URL('../../../../deploy/nats.conf', import.meta.url))

export const HUB_PASS = 'hub-test-pw'

export interface TestNats {
  readonly url: string
  /** Open a broker connection as user `hub`, the only user. */
  broker(): Promise<Broker>
  /** Stop the server process and keep its store (for restart tests). */
  kill(): Promise<void>
  /** Start the server again on the same port and store. */
  start(): Promise<void>
  /** Stop the server and delete its store. */
  stop(): Promise<void>
}

/** deploy/nats.conf with only the listen address and the store directory changed. */
function testConfig(port: number, store: string): string {
  let text = readFileSync(CONF, 'utf8')
  for (const [from, to] of [
    ['listen: 127.0.0.1:4222', `listen: 127.0.0.1:${port}`],
    ['store_dir: /var/lib/nats', `store_dir: "${store}"`],
  ] as const) {
    if (!text.includes(from)) throw new Error(`deploy/nats.conf has no line "${from}"`)
    text = text.replace(from, to)
  }
  return text
}

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer()
    s.once('error', reject)
    s.listen(0, '127.0.0.1', () => {
      const addr = s.address()
      const port = typeof addr === 'object' && addr !== null ? addr.port : 0
      s.close(() => resolve(port))
    })
  })
}

function waitReady(p: ChildProcess): Promise<void> {
  return new Promise((resolve, reject) => {
    let log = ''
    const onData = (b: Buffer) => {
      log += b.toString()
      if (log.includes('Server is ready')) {
        p.stderr?.off('data', onData)
        resolve()
      }
    }
    p.stderr?.on('data', onData)
    p.once('exit', (code) => reject(new Error(`nats-server exited ${code}:\n${log}`)))
  })
}

export async function startNats(): Promise<TestNats> {
  const port = await freePort()
  const dir = mkdtempSync(join(tmpdir(), 'coop-nats-'))
  const conf = join(dir, 'nats.conf')
  writeFileSync(conf, testConfig(port, join(dir, 'store')))
  const url = `nats://127.0.0.1:${port}`
  const open: Broker[] = []
  let proc: ChildProcess | undefined

  const start = async () => {
    proc = spawn('nats-server', ['-c', conf], {
      env: { ...process.env, COOP_HUB_NATS_PASSWORD: HUB_PASS },
      stdio: ['ignore', 'ignore', 'pipe'],
    })
    await waitReady(proc)
  }

  const kill = async () => {
    const p = proc
    proc = undefined
    if (p === undefined || p.exitCode !== null) return
    const exited = new Promise((r) => p.once('exit', r))
    p.kill('SIGKILL')
    await exited
  }

  await start()
  return {
    url,
    async broker() {
      const b = await openBroker({ servers: url, user: 'hub', pass: HUB_PASS, name: 'test-hub' })
      open.push(b)
      return b
    },
    kill,
    start,
    async stop() {
      await Promise.allSettled(open.map((b) => b.nc.close()))
      await kill()
      rmSync(dir, { recursive: true, force: true })
    },
  }
}
