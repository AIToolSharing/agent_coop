import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { ErrorCode } from '@coop/core'
import { fc, test } from '@fast-check/vitest'
import { afterEach, expect } from 'vitest'
import { errorText, HubClient, type LinkState, UNREACHABLE } from '../src/client.js'
import { FORBIDDEN_RE } from '../src/server.js'

let server: Server | undefined
afterEach(() => {
  server?.closeAllConnections()
  server?.close()
  server = undefined
})

test('a stream that goes silent is dropped after idleMs and opened again', async () => {
  let opened = 0
  server = createServer((_req, res) => {
    opened++
    res.writeHead(200, { 'content-type': 'text/event-stream' })
    res.write('event: joined\ndata: {"me":"a@m","session":"s"}\n\n')
    // Then nothing: no ping, no close.
  })
  await new Promise<void>((r) => server?.listen(0, '127.0.0.1', r))
  const { port } = server.address() as AddressInfo
  const states: string[] = []
  const ctl = new AbortController()
  const client = new HubClient(
    `http://127.0.0.1:${port}`,
    'm.s',
    's',
    { agent: 'a', instance: 'i', host: 'h', cwd: '/', clientName: 'c', clientVersion: '1' },
    (ms, signal) =>
      new Promise((r) => {
        const t = setTimeout(r, Math.min(ms, 20))
        signal.addEventListener('abort', () => {
          clearTimeout(t)
          r()
        })
      }),
    150,
  )
  const run = client.run(
    { state: (s: LinkState) => void states.push(s.kind), message: () => {}, notice: () => {} },
    ctl.signal,
  )
  const until = Date.now() + 5000
  while (opened < 3 && Date.now() < until) await new Promise((r) => setTimeout(r, 20))
  ctl.abort()
  await run
  expect(opened).toBeGreaterThanOrEqual(3)
  expect(states.slice(0, 3)).toEqual(['joined', 'unreachable', 'joined'])
})

test('401 and server errors get fixed texts; the service text never passes through', () => {
  const leaky = { error: 'unauthorized' as const, message: 'missing or invalid token' }
  expect(errorText(401, leaky)).toBe('not in a session: this machine is not allowed to join')
  expect(errorText(429, { error: 'rate_limited', message: 'too many joins' })).toContain('too many')
  expect(errorText(503, { error: 'unavailable', message: 'internal error' })).toBe(UNREACHABLE)
  expect(errorText(404, undefined)).toBe(UNREACHABLE)
})

test.prop([
  fc.constantFrom(401, 500, 502, 503),
  fc.constantFrom(...ErrorCode.options),
  fc.string(),
])('fixed texts never contain a forbidden word', (status, error, message) => {
  expect(FORBIDDEN_RE.test(errorText(status, { error, message: `${message} token stream` }))).toBe(
    false,
  )
})
