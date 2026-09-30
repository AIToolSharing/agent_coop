import { buildPresenceKey, encode, PresenceRecord } from '@coop/core'
import { type Broker, Operator, publishEvent } from '@coop/core/broker'
import { afterAll, beforeAll, expect, test } from 'vitest'
import { startNats, type TestNats } from '../../core/test/helpers/nats.js'
import { startFeed } from '../src/data.js'
import { derive, Store } from '../src/model.js'

let nats: TestNats
let b: Broker
beforeAll(async () => {
  nats = await startNats()
  b = await nats.broker('operator')
})
afterAll(async () => {
  await nats?.stop()
})

async function until(cond: () => boolean, ms = 5000) {
  const end = Date.now() + ms
  while (!cond()) {
    if (Date.now() > end) throw new Error('timed out')
    await new Promise((r) => setTimeout(r, 20))
  }
}

test('the feed brings events, sessions and presence into the store, live', async () => {
  const op = new Operator(b)
  await op.createSession('live')
  const alice = { agent: 'alice', machine: 'm1' }
  await publishEvent(b.js, {
    kind: 'evt',
    sid: 'live',
    from: alice,
    evt: {
      kind: 'joined',
      host: 'h',
      cwd: '/',
      client: { name: 'c', version: '1' },
      at: new Date().toISOString(),
    },
  })
  const store = new Store()
  let changes = 0
  const ctl = new AbortController()
  const feed = startFeed(b, store, () => changes++, ctl.signal, 10)

  await until(() => store.sessions.has('live') && store.events.size >= 1)
  await op.send('live', 'all', 'hello from the operator')
  await b.presence.put(
    buildPresenceKey({ sid: 'live', agent: alice }),
    encode(PresenceRecord, {
      host: 'h',
      cwd: '/',
      client: { name: 'c', version: '1' },
      state: 'working',
      joined_at: new Date().toISOString(),
      queued: 0,
    }),
  )
  await until(() => derive(store, 'live').messages.length === 1 && store.presence.size === 1)
  expect(derive(store, 'live').agents).toMatchObject([
    { address: 'alice@m1', online: true, state: 'working' },
  ])

  await b.presence.delete(buildPresenceKey({ sid: 'live', agent: alice }))
  await until(() => store.presence.size === 0)
  expect(derive(store, 'live').agents[0]?.online).toBe(false)

  await op.closeSession('live')
  await until(() => store.sessions.get('live')?.status === 'closed')
  await op.deleteSession('live')
  await until(() => !store.sessions.has('live'))
  expect([...store.events.values()].filter((e) => e.sid === 'live')).toEqual([])
  expect(changes).toBeGreaterThan(0)
  ctl.abort()
  await feed.catch(() => undefined)
})
