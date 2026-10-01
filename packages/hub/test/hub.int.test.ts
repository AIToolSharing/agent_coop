import {
  ApiMessage,
  ErrorBody,
  HistoryResponse,
  JoinedEvent,
  NoticeEvent,
  PresenceRecord,
  SendResponse,
  SessionView,
  sessionSubjects,
} from '@coop/core'
import { readRange } from '@coop/core/broker'
import { afterAll, beforeAll, describe, expect, test } from 'vitest'
import { Api, data, type Harness, isMsg, startHub } from './harness.js'

let h: Harness
let mac1: Api
let vps2: Api
let mac3: Api

beforeAll(async () => {
  // High limits here; the rate limit test below uses its own hub with low limits.
  const high = { burst: 10_000, perSecond: 10_000 }
  h = await startHub({ join: high, msg: high, activity: high })
  mac1 = new Api(h.base, await h.token('mac-1'))
  vps2 = new Api(h.base, await h.token('vps-2'))
  mac3 = new Api(h.base, await h.token('mac-3'))
})
afterAll(async () => {
  await h?.stop()
})

let n = 0
/** A fresh open session per test, so tests do not see each other's traffic. */
async function session(): Promise<string> {
  const sid = `s${++n}`
  await h.op.createSession(sid)
  return sid
}

const err = (json: unknown) => ErrorBody.parse(json)

describe('authentication', () => {
  test('a request without a valid token gets 401', async () => {
    const sid = await session()
    expect((await new Api(h.base, 'nope').view(sid, 'a')).status).toBe(401)
    expect((await new Api(h.base, 'mac-1.wrong-secret').view(sid, 'a')).status).toBe(401)
    const res = await fetch(`${h.base}/v1/sessions/${sid}`)
    expect(res.status).toBe(401)
  })
})

describe('joining', () => {
  test('the first event names the agent; the join is recorded', async () => {
    const sid = await session()
    const s = await mac1.stream(sid, 'alice')
    expect(s.status).toBe(200)
    expect(JoinedEvent.parse(data(await s.next()))).toEqual({ me: 'alice@mac-1', session: sid })
    const p = await h.hubBroker.presence.get(`${sid}.mac-1.alice`)
    expect(p?.operation).toBe('PUT')
    s.close()
  })

  test('an unknown session is 404 and a closed session is 403', async () => {
    expect((await mac1.stream('nosuch', 'alice')).status).toBe(404)
    const sid = await session()
    await h.op.closeSession(sid)
    const s = await mac1.stream(sid, 'alice')
    expect(s.status).toBe(403)
  })

  test('a taken name is 409; the same instance takes over its old stream', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    await a.next()
    expect((await mac1.stream(sid, 'alice')).status).toBe(409)
    const again = await mac1.stream(sid, 'alice', { instance: a.instance })
    expect(again.status).toBe(200)
    await a.ended()
    again.close()
  })

  // Found by the shim tests: two joins at the same moment both got the name.
  test('two joins of one name at the same moment: exactly one wins', async () => {
    const sid = await session()
    for (let round = 0; round < 5; round++) {
      const name = `race${round}`
      const [a, b] = await Promise.all([mac1.stream(sid, name), mac1.stream(sid, name)])
      expect([a.status, b.status].sort()).toEqual([200, 409])
      a.close()
      b.close()
    }
  })

  test('the same name on two machines is allowed', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'agent')
    const b = await vps2.stream(sid, 'agent')
    expect([a.status, b.status]).toEqual([200, 200])
    a.close()
    b.close()
  })
})

describe('messages', () => {
  test('a broadcast reaches others, not the sender, with the sender stamped by the hub', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    const b = await vps2.stream(sid, 'bob')
    await a.next()
    await b.next()
    const t0 = performance.now()
    const r = await mac1.send(sid, 'alice', 'all', 'hello all')
    expect(r.status).toBe(200)
    const sent = SendResponse.parse(r.json)
    const got = ApiMessage.parse(data(await b.next(isMsg)))
    const ms = performance.now() - t0
    expect(got).toMatchObject({ id: sent.id, from: 'alice@mac-1', to: 'all', text: 'hello all' })
    expect(ms).toBeLessThan(250)
    await expect(a.next(isMsg, 500)).rejects.toThrow()
    a.close()
    b.close()
  })

  test('a direct message reaches only its target, in stream and in history', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    const b = await vps2.stream(sid, 'bob')
    const c = await mac3.stream(sid, 'carol')
    for (const s of [a, b, c]) await s.next()
    const r = SendResponse.parse((await mac1.send(sid, 'alice', 'bob', 'secret')).json)
    expect(r.to).toBe('bob@vps-2')
    expect(ApiMessage.parse(data(await b.next(isMsg))).text).toBe('secret')
    await expect(c.next(isMsg, 500)).rejects.toThrow()
    const hb = HistoryResponse.parse((await vps2.history(sid, 'bob')).json)
    const hc = HistoryResponse.parse((await mac3.history(sid, 'carol')).json)
    expect(hb.messages.map((m) => m.id)).toContain(r.id)
    expect(hc.messages.map((m) => m.id)).not.toContain(r.id)
    for (const s of [a, b, c]) s.close()
  })

  test('a body with a from field is refused', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    await a.next()
    const r = await mac1.req('POST', `/v1/sessions/${sid}/messages`, {
      agent: 'alice',
      to: 'all',
      text: 'x',
      from: 'bob@vps-2',
    })
    expect(r.status).toBe(422)
    a.close()
  })

  test('an agent name without a live stream of this token is 403', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    await a.next()
    expect((await vps2.send(sid, 'alice', 'all', 'spoof')).status).toBe(403)
    expect(err((await vps2.send(sid, 'alice', 'all', 'spoof')).json).message).toBe(
      'not in a session',
    )
    a.close()
  })

  test('unknown, ambiguous, self, and full-name recipients', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    const x1 = await vps2.stream(sid, 'x')
    const x3 = await mac3.stream(sid, 'x')
    for (const s of [a, x1, x3]) await s.next()
    const unknown = await mac1.send(sid, 'alice', 'zed', 'hi')
    expect(unknown.status).toBe(404)
    expect(err(unknown.json).message).toContain('x@vps-2')
    const amb = await mac1.send(sid, 'alice', 'x', 'hi')
    expect([amb.status, err(amb.json).error]).toEqual([409, 'ambiguous'])
    expect((await mac1.send(sid, 'alice', 'alice', 'hi')).status).toBe(409)
    expect((await mac1.send(sid, 'alice', 'alice@mac-1', 'hi')).status).toBe(409)
    expect((await mac1.send(sid, 'alice', 'operator', 'hi')).status).toBe(200)
    // Found by Schemathesis: a schema-valid peer that is the caller is a conflict, not bad input.
    expect((await mac1.history(sid, 'alice', '&with=alice%40mac-1')).status).toBe(409)
    expect((await mac1.send(sid, 'alice', 'x@mac-3', 'hi')).status).toBe(200)
    expect(ApiMessage.parse(data(await x3.next(isMsg))).from).toBe('alice@mac-1')
    for (const s of [a, x1, x3]) s.close()
  })

  test('size limits: text over 8000 is 422, a body over 32 KiB is 413', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    await a.next()
    expect((await mac1.send(sid, 'alice', 'all', 'x'.repeat(8001))).status).toBe(422)
    expect((await mac1.send(sid, 'alice', 'all', 'x'.repeat(40_000))).status).toBe(413)
    a.close()
  })
})

describe('rate limits', () => {
  test('bursts over the limit get 429, per machine', async () => {
    const low = await startHub({
      join: { burst: 2, perSecond: 0.001 },
      msg: { burst: 3, perSecond: 0.001 },
    })
    try {
      await low.op.createSession('rl')
      const api = new Api(low.base, await low.token('mac-1'))
      const other = new Api(low.base, await low.token('vps-2'))
      const a = await api.stream('rl', 'alice')
      await a.next()
      const codes = []
      for (let i = 0; i < 5; i++) codes.push((await api.send('rl', 'alice', 'all', 'x')).status)
      expect(codes).toEqual([200, 200, 200, 429, 429])
      await api.stream('rl', 'bob')
      expect((await api.stream('rl', 'carol')).status).toBe(429)
      expect((await other.stream('rl', 'dave')).status).toBe(200)
      a.close()
    } finally {
      await low.stop()
    }
  })
})

describe('resume', () => {
  test('Last-Event-ID resumes after a drop: the gap once, nothing twice', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    const b = await vps2.stream(sid, 'bob')
    await a.next()
    await b.next()
    await mac1.send(sid, 'alice', 'all', 'm1')
    const m1 = await b.next(isMsg)
    b.close()
    // bob is away: a broadcast still goes into the log.
    expect((await mac1.send(sid, 'alice', 'all', 'm2')).status).toBe(200)
    const b2 = await vps2.stream(sid, 'bob', { instance: b.instance, lastEventId: m1.id ?? '' })
    await b2.next((e) => e.event === 'joined')
    expect(ApiMessage.parse(data(await b2.next(isMsg))).text).toBe('m2')
    await expect(b2.next(isMsg, 500)).rejects.toThrow()
    a.close()
    b2.close()
  })
})

describe('operator actions reach agents', () => {
  test('kick: notice, stream ends, rejoin and sends refused', async () => {
    const sid = await session()
    const b = await vps2.stream(sid, 'bob')
    await b.next()
    await h.op.kick(sid, { agent: 'bob', machine: 'vps-2' })
    expect(NoticeEvent.parse(data(await b.next((e) => e.event === 'notice'))).kind).toBe('kicked')
    await b.ended()
    expect((await vps2.stream(sid, 'bob')).status).toBe(403)
    expect((await vps2.send(sid, 'bob', 'all', 'x')).status).toBe(403)
  })

  test('close and reopen: notices, and sends refused while closed', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    await a.next()
    await h.op.closeSession(sid)
    expect(NoticeEvent.parse(data(await a.next((e) => e.event === 'notice'))).kind).toBe('closed')
    const r = await mac1.send(sid, 'alice', 'all', 'x')
    expect([r.status, err(r.json).message]).toEqual([403, 'session closed'])
    await h.op.reopenSession(sid)
    expect(NoticeEvent.parse(data(await a.next((e) => e.event === 'notice'))).kind).toBe('reopened')
    expect((await mac1.send(sid, 'alice', 'all', 'x')).status).toBe(200)
    a.close()
  })

  test('redact: only agents that got the message get a notice; history drops it', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    const b = await vps2.stream(sid, 'bob')
    const c = await mac3.stream(sid, 'carol')
    for (const s of [a, b, c]) await s.next()
    const { id } = SendResponse.parse((await mac1.send(sid, 'alice', 'bob', 'oops')).json)
    await b.next(isMsg)
    expect(await h.op.redact(sid, id)).toBe(true)
    expect(NoticeEvent.parse(data(await b.next((e) => e.event === 'notice')))).toMatchObject({
      kind: 'redacted',
      id,
    })
    await expect(c.next((e) => e.event === 'notice', 500)).rejects.toThrow()
    const hist = HistoryResponse.parse((await vps2.history(sid, 'bob')).json)
    expect(hist.messages.map((m) => m.id)).not.toContain(id)
    for (const s of [a, b, c]) s.close()
  })

  // Found in the end-to-end run: an agent could not answer the operator ("send can't address operator").
  test('an agent can answer the operator; no other agent receives it', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    const b = await vps2.stream(sid, 'bob')
    await a.next()
    await b.next()
    const r = await mac1.send(sid, 'alice', 'operator', 'ONLINE')
    expect(r.status).toBe(200)
    expect(SendResponse.parse(r.json).to).toBe('operator')
    await expect(b.next(isMsg, 500)).rejects.toThrow()
    const mine = HistoryResponse.parse((await mac1.history(sid, 'alice')).json).messages
    expect(mine.map((m) => [m.to, m.text])).toContainEqual(['operator', 'ONLINE'])
    const theirs = HistoryResponse.parse((await vps2.history(sid, 'bob')).json).messages
    expect(theirs.map((m) => m.text)).not.toContain('ONLINE')
    a.close()
    b.close()
  })

  test('operator messages arrive from operator', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    await a.next()
    await h.op.send(sid, { agent: 'alice', machine: 'mac-1' }, 'hi from the user')
    expect(ApiMessage.parse(data(await a.next(isMsg))).from).toBe('operator')
    a.close()
  })

  test('revoking a token ends its streams and its requests', async () => {
    const api = new Api(h.base, await h.token('temp'))
    const sid = await session()
    const s = await api.stream(sid, 'tmp')
    await s.next()
    const { revokeToken } = await import('../src/tokens.js')
    await revokeToken(h.hubBroker.tokens, 'temp')
    await s.ended()
    expect((await api.view(sid, 'tmp')).status).toBe(401)
  })
})

describe('activity and presence', () => {
  test('state, waits and deliveries show in the view, presence, and the stream', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    const b = await vps2.stream(sid, 'bob')
    await a.next()
    await b.next()
    expect(
      (
        await vps2.activity(sid, {
          kind: 'state',
          agent: 'bob',
          state: 'blocked',
          note: 'need API',
        })
      ).status,
    ).toBe(204)
    await vps2.activity(sid, { kind: 'wait_start', agent: 'bob', from: 'alice', timeout_s: 60 })
    const v = SessionView.parse((await mac1.view(sid, 'alice')).json)
    expect(v.peers).toEqual([
      {
        name: 'bob@vps-2',
        state: 'blocked',
        note: 'need API',
        online: true,
        waiting_on: 'alice@mac-1',
      },
    ])
    const { id } = SendResponse.parse((await mac1.send(sid, 'alice', 'bob', 'answer')).json)
    await b.next(isMsg)
    await vps2.activity(sid, { kind: 'delivered', agent: 'bob', id, via: 'ask' })
    await vps2.activity(sid, { kind: 'wait_end', agent: 'bob', result: 'message' })
    const p = await h.hubBroker.presence.get(`${sid}.vps-2.bob`)
    const rec =
      p === null ? undefined : PresenceRecord.parse(JSON.parse(new TextDecoder().decode(p.value)))
    expect(rec).toMatchObject({ state: 'blocked', queued: 0 })
    expect(rec?.waiting).toBeUndefined()
    const kinds = (await readRange(h.hubBroker.js, h.hubBroker.jsm, [sessionSubjects(sid)]))
      .filter((e) => e.kind === 'evt')
      .map((e) => (e.kind === 'evt' ? e.evt.kind : ''))
    expect(kinds).toEqual(
      expect.arrayContaining(['joined', 'state', 'wait_start', 'delivered', 'wait_end']),
    )
    a.close()
    b.close()
  })

  test('closing a stream removes presence and records left', async () => {
    const sid = await session()
    const a = await mac1.stream(sid, 'alice')
    await a.next()
    a.close()
    const deadline = Date.now() + 5000
    for (;;) {
      const p = await h.hubBroker.presence.get(`${sid}.mac-1.alice`)
      if (p === null || p.operation !== 'PUT') break
      if (Date.now() > deadline) throw new Error('presence not removed')
      await new Promise((r) => setTimeout(r, 50))
    }
    const evts = await readRange(h.hubBroker.js, h.hubBroker.jsm, [sessionSubjects(sid)])
    expect(evts.some((e) => e.kind === 'evt' && e.evt.kind === 'left')).toBe(true)
  })
})

describe('session auto-create', () => {
  test('with the option on, the first join creates an open session; off, it is 404', async () => {
    const auto = await startHub({}, { autoCreate: true })
    try {
      const api = new Api(auto.base, await auto.token('mac-1'))
      const s = await api.stream('fresh', 'alice')
      expect(s.status).toBe(200)
      expect(JoinedEvent.parse(data(await s.next()))).toEqual({
        me: 'alice@mac-1',
        session: 'fresh',
      })
      expect((await auto.op.getSession('fresh'))?.status).toBe('open')
      expect((await auto.op.listSessions()).map((x) => x.sid)).toEqual(['fresh'])
      // A closed session stays closed: auto-create never reopens.
      s.close()
      await s.ended()
      await auto.op.closeSession('fresh')
      expect((await api.stream('fresh', 'alice')).status).toBe(403)
    } finally {
      await auto.stop()
    }
    expect((await mac1.stream('fresh', 'alice')).status).toBe(404)
  })
})

describe('contract document', () => {
  test('/openapi.json lists every route', async () => {
    const doc = (await (await fetch(`${h.base}/openapi.json`)).json()) as { paths: object }
    expect(Object.keys(doc.paths).sort()).toEqual([
      '/v1/sessions/{sid}',
      '/v1/sessions/{sid}/activity',
      '/v1/sessions/{sid}/messages',
      '/v1/sessions/{sid}/stream',
    ])
  })
})
