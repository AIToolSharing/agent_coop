import { headers } from '@nats-io/nats-core'
import { connect } from '@nats-io/transport-node'
import { afterAll, beforeAll, describe, expect, test } from 'vitest'
import {
  type Broker,
  follow,
  Operator,
  PRESENCE_TTL_MS,
  publishEvent,
  readRange,
} from '../src/broker/index.js'
import {
  type Address,
  type BusEvent,
  deliverySubjects,
  encode,
  PresenceRecord,
  STREAM_NAME,
  sessionSubjects,
} from '../src/index.js'
import { startNats, type TestNats } from './helpers/nats.js'

const alice: Address = { agent: 'alice', machine: 'mac-1' }
const bob: Address = { agent: 'bob', machine: 'vps-2' }
const at = () => new Date().toISOString()

let nats: TestNats
let b: Broker
let op: Operator

beforeAll(async () => {
  nats = await startNats()
  b = await nats.broker()
  op = new Operator(b)
})
afterAll(async () => {
  await nats?.stop()
})

function msg(sid: string, from: Address, text: string) {
  return { kind: 'msg' as const, sid, from, to: 'all' as const, text, sent_at: at() }
}

describe('infrastructure', () => {
  test('stream COOP has the safe config', async () => {
    const c = (await b.jsm.streams.info(STREAM_NAME)).config
    expect(c.subjects).toEqual(['coop.>'])
    expect(c.allow_rollup_hdrs).toBe(false)
    expect(c.allow_msg_ttl ?? false).toBe(false)
    expect(c.max_msg_size).toBe(64 * 1024)
  })

  test('presence bucket expires keys and keeps expiry markers', async () => {
    const s = await b.presence.status()
    expect(s.ttl).toBe(PRESENCE_TTL_MS)
    expect(s.markerTTL).toBeGreaterThan(0)
  })

  test('setup is idempotent: a second privileged connection succeeds', async () => {
    const again = await nats.broker()
    expect((await again.jsm.streams.info(STREAM_NAME)).config.name).toBe(STREAM_NAME)
  })

  test('a connection without the right password is refused', async () => {
    await expect(connect({ servers: nats.url, user: 'hub', pass: 'wrong' })).rejects.toThrow()
    await expect(connect({ servers: nats.url })).rejects.toThrow()
  })

  test.each([
    ['Nats-Rollup', 'all'],
    ['Nats-TTL', '1s'],
  ])('a publish with header %s is refused', async (name, value) => {
    const h = headers()
    h.set(name, value)
    await expect(
      b.js.publish('coop.hdr.msg.mac-1.alice', new Uint8Array([123, 125]), { headers: h }),
    ).rejects.toThrow()
  })
})

describe('reading the stream', () => {
  test('readRange returns only the filtered session, in order', async () => {
    const s1 = await publishEvent(b.js, msg('r1', alice, 'one'))
    await publishEvent(b.js, msg('r2', alice, 'other session'))
    const s3 = await publishEvent(b.js, msg('r1', bob, 'two'))
    const got = await readRange(b.js, b.jsm, [sessionSubjects('r1')])
    expect(got.map((e) => e.seq)).toEqual([s1, s3])
    const upTo = await readRange(b.js, b.jsm, [sessionSubjects('r1')], 1, s1)
    expect(upTo.map((e) => e.seq)).toEqual([s1])
  })

  test('readRange of an empty session returns quickly', async () => {
    const t0 = Date.now()
    expect(await readRange(b.js, b.jsm, [sessionSubjects('empty')])).toEqual([])
    expect(Date.now() - t0).toBeLessThan(2000)
  })

  test('follow survives a server restart and delivers each event once', async () => {
    const first = await publishEvent(b.js, msg('f1', alice, 'before'))
    const ctl = new AbortController()
    const seen: BusEvent[] = []
    const reader = (async () => {
      for await (const e of follow(b.js, deliverySubjects('f1'), first, ctl.signal)) {
        seen.push(e)
        if (seen.length === 2) ctl.abort()
      }
    })()
    await waitFor(() => seen.length === 1, 5000)

    await nats.kill()
    await nats.start()
    await waitFor(() => !b.nc.isClosed() && b.nc.info !== undefined, 10_000)
    const after = await retry(() => publishEvent(b.js, msg('f1', bob, 'after')), 10_000)

    await reader
    expect(seen.map((e) => e.seq)).toEqual([first, after])
  }, 30_000)
})

describe('operator', () => {
  test('session lifecycle', async () => {
    await op.createSession('life', 'Life')
    expect(await op.getSession('life')).toMatchObject({ status: 'open', title: 'Life' })
    await expect(op.createSession('life')).rejects.toThrow(/exists/)
    await op.closeSession('life')
    expect((await op.getSession('life'))?.status).toBe('closed')
    expect((await op.getSession('life'))?.closed_at).toBeDefined()
    await op.reopenSession('life')
    const r = await op.getSession('life')
    expect(r?.status).toBe('open')
    expect(r?.closed_at).toBeUndefined()
    expect(await op.listSessions()).toContainEqual({ sid: 'life', record: r })
    expect((await op.listSessions()).map((s) => s.sid)).not.toContain('life.kick.m.a')
  })

  test('delete needs a closed session and removes only that session', async () => {
    await op.createSession('gone')
    await publishEvent(b.js, msg('gone', alice, 'x'))
    const keep = await publishEvent(b.js, msg('kept', alice, 'y'))
    await op.kick('gone', bob)
    await expect(op.deleteSession('gone')).rejects.toThrow(/open/)
    await op.closeSession('gone')
    await op.deleteSession('gone')
    expect(await op.getSession('gone')).toBeUndefined()
    expect(await readRange(b.js, b.jsm, [sessionSubjects('gone')])).toEqual([])
    const kept = await readRange(b.js, b.jsm, [sessionSubjects('kept')])
    expect(kept.map((e) => e.seq)).toContain(keep)
    const kicks: string[] = []
    for await (const k of await b.sessions.keys('gone.>')) kicks.push(k)
    expect(kicks).toEqual([])
  })

  test('kick writes the kick key and a kick event', async () => {
    const seq = await op.kick('k1', bob)
    expect((await b.sessions.get('k1.kick.vps-2.bob'))?.operation).toBe('PUT')
    const [e] = await readRange(b.js, b.jsm, [sessionSubjects('k1')], seq)
    expect(e).toMatchObject({ kind: 'kick', target: bob })
  })

  test('redact erases the message and records a redact event', async () => {
    const seq = await publishEvent(b.js, msg('rd', alice, 'secret'))
    expect(await op.redact('rd', String(seq))).toBe(true)
    const events = await readRange(b.js, b.jsm, [sessionSubjects('rd')])
    expect(events.map((e) => e.kind)).toEqual(['redact'])
    expect(events[0]).toMatchObject({ kind: 'redact', id: String(seq) })
  })

  test('redact refuses a message of another session, an activity event, or a missing id', async () => {
    const other = await publishEvent(b.js, msg('rd-other', alice, 'x'))
    const evt = await publishEvent(b.js, {
      kind: 'evt',
      sid: 'rd2',
      from: alice,
      evt: { kind: 'state', state: 'working', at: at() },
    })
    expect(await op.redact('rd2', String(other))).toBe(false)
    expect(await op.redact('rd2', String(evt))).toBe(false)
    expect(await op.redact('rd2', '999999')).toBe(false)
  })

  test('operator messages come from operator', async () => {
    const id = await op.send('om', bob, 'hello', undefined)
    const [e] = await readRange(b.js, b.jsm, [sessionSubjects('om')])
    expect(e).toMatchObject({ kind: 'msg', seq: Number(id), from: 'operator', to: bob })
  })
})

describe('presence', () => {
  test(
    'a key without heartbeat expires and a watcher sees PURGE',
    async () => {
      const key = op.presenceKey('p1', alice)
      const w = await b.presence.watch({ key })
      const ops: string[] = []
      const watching = (async () => {
        for await (const e of w) {
          ops.push(e.operation)
          if (e.operation === 'PURGE') break
        }
      })()
      await b.presence.put(
        key,
        encode(PresenceRecord, {
          host: 'h',
          cwd: '/',
          client: { name: 't', version: '0' },
          state: 'idle',
          joined_at: at(),
          queued: 0,
        }),
      )
      await watching
      w.stop()
      expect(ops).toEqual(['PUT', 'PURGE'])
    },
    PRESENCE_TTL_MS + 15_000,
  )
})

async function waitFor(cond: () => boolean, ms: number): Promise<void> {
  const end = Date.now() + ms
  while (!cond()) {
    if (Date.now() > end) throw new Error('waitFor timed out')
    await new Promise((r) => setTimeout(r, 50))
  }
}

async function retry<T>(f: () => Promise<T>, ms: number): Promise<T> {
  const end = Date.now() + ms
  for (;;) {
    try {
      return await f()
    } catch (err) {
      if (Date.now() > end) throw err
      await new Promise((r) => setTimeout(r, 200))
    }
  }
}
