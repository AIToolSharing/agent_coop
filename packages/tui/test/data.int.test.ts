// The feed over a real hub: events, buckets, reconnect with reconciliation.
import { buildPresenceKey } from '@coop/core'
import { afterAll, beforeAll, expect, test } from 'vitest'
import { Api, type Harness, startHub } from '../../hub/test/harness.js'
import { AdminClient, RefusedError } from '../src/client.js'
import { startFeed } from '../src/data.js'
import { derive, Store } from '../src/model.js'

let h: Harness
let client: AdminClient
beforeAll(async () => {
  h = await startHub()
  client = new AdminClient(h.base, await h.operatorToken('viewer'))
})
afterAll(async () => {
  await h?.stop()
})

async function until(cond: () => boolean, ms = 5000) {
  const end = Date.now() + ms
  while (!cond()) {
    if (Date.now() > end) throw new Error('timed out')
    await new Promise((r) => setTimeout(r, 20))
  }
}

test('the feed brings events, sessions, presence and kicks into the store, live', async () => {
  await h.op.createSession('live')
  const mac = new Api(h.base, await h.token('m1'))
  const store = new Store()
  let changes = 0
  const ctl = new AbortController()
  const feed = startFeed(client.feed(ctl.signal), store, () => changes++, 10)

  await until(() => store.sessions.has('live') && store.link === 'live')
  const a = await mac.stream('live', 'alice')
  await a.next()
  await until(() => store.presence.size === 1 && store.events.size >= 1)
  expect(derive(store, 'live').agents).toMatchObject([
    { address: 'alice@m1', online: true, state: 'idle' },
  ])
  await client.send('live', 'all', 'hello from the operator')
  await until(() => derive(store, 'live').messages.length === 1)
  expect(derive(store, 'live').messages[0]).toMatchObject({ from: 'operator', to: 'all' })

  await client.kick('live', { agent: 'alice', machine: 'm1' })
  await until(() => store.kicks.size === 1 && store.presence.size === 0)
  expect(derive(store, 'live').agents[0]).toMatchObject({ online: false, kicked: true })
  await client.unkick('live', { agent: 'alice', machine: 'm1' })
  await until(() => store.kicks.size === 0)

  await client.closeSession('live')
  await until(() => store.sessions.get('live')?.status === 'closed')
  await client.deleteSession('live')
  await until(() => !store.sessions.has('live'))
  expect([...store.events.values()].filter((e) => e.sid === 'live')).toEqual([])
  expect(changes).toBeGreaterThan(0)
  ctl.abort()
  await feed
})

test('after a reconnect, entries that vanished meanwhile are dropped', async () => {
  await h.op.createSession('gone')
  await h.op.createSession('stays')
  await h.hubBroker.presence.put(
    buildPresenceKey({ sid: 'gone', agent: { agent: 'x', machine: 'm' } }),
    new TextEncoder().encode(
      JSON.stringify({
        host: 'h',
        cwd: '/',
        client: { name: 'c', version: '1' },
        state: 'idle',
        joined_at: new Date().toISOString(),
        queued: 0,
      }),
    ),
  )
  const store = new Store()
  const ctl = new AbortController()
  const feed = startFeed(client.feed(ctl.signal), store, () => undefined, 10)
  await until(() => store.sessions.has('gone') && store.presence.size === 1)

  // The hub goes away; meanwhile the operator's other tools delete things on the broker.
  await h.restartHub()
  await h.op.closeSession('gone')
  await h.op.deleteSession('gone')
  // The two buckets travel on two loops; neither one comes first.
  await until(
    () => store.link === 'live' && !store.sessions.has('gone') && store.presence.size === 0,
    15_000,
  )
  expect(store.sessions.has('stays')).toBe(true)
  ctl.abort()
  await feed
})

test('a machine token is refused for good', async () => {
  const wrong = new AdminClient(h.base, await h.token('m2'))
  const ctl = new AbortController()
  await expect(startFeed(wrong.feed(ctl.signal), new Store(), () => undefined)).rejects.toThrow(
    RefusedError,
  )
})
