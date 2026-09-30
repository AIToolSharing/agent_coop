import { BROADCAST, formatAddress } from '@coop/core'
import { fc, test } from '@fast-check/vitest'
import { describe, expect } from 'vitest'
import { type Derived, derive } from '../src/model.js'
import { SID, scenario, storeOf } from './scenario.js'

/** A plain, comparable form of the derived model (Maps and functions become arrays). */
function normal(d: Derived) {
  return {
    agents: d.agents.map((a) => ({ ...a, order: undefined })),
    messages: d.messages.map((m) => ({ ...m, deliveries: [...m.deliveries].sort() })),
    timeline: d.timeline.map((t) => (t.kind === 'msg' ? { seq: t.seq, id: t.msg.id } : t)),
    openAsks: d.openAsks,
    matrix: d.matrix.senders.map((f) =>
      d.matrix.receivers.map((t) => [d.matrix.count(f, t), d.matrix.latency(f, t)]),
    ),
  }
}

describe('derive', () => {
  test.prop([scenario, fc.array(fc.double({ noNaN: true }), { minLength: 400, maxLength: 400 })])(
    'the order in which facts arrive does not change the result',
    (sc, keys) => {
      const shuffled = sc.updates
        .map((u, i) => ({ u, k: keys[i % keys.length] ?? 0 }))
        .sort((a, b) => a.k - b.k)
        .map((x) => x.u)
      // A session record must be known for its events to show; apply it first in both orders.
      const session = sc.updates.filter((u) => u.kind === 'session')
      const rest = shuffled.filter((u) => u.kind !== 'session')
      const a = derive(storeOf(sc.updates), SID)
      const b = derive(storeOf([...session, ...rest]), SID)
      expect(normal(b)).toEqual(normal(a))
    },
  )

  test.prop([scenario])('the matrix counts every message that is not withdrawn, once', (sc) => {
    const d = derive(storeOf(sc.updates), SID)
    let total = 0
    for (const f of d.matrix.senders)
      for (const t of d.matrix.receivers) total += d.matrix.count(f, t)
    expect(total).toBe(d.messages.filter((m) => !m.redacted).length)
  })

  test.prop([scenario])('an open ask has no answer from the asked agent', (sc) => {
    const d = derive(storeOf(sc.updates), SID)
    for (const q of d.openAsks) {
      expect(d.messages.some((m) => m.reply_to === q.id && m.from === q.to)).toBe(false)
    }
  })

  test.prop([scenario])('a broadcast never lists its sender as a recipient', (sc) => {
    const d = derive(storeOf(sc.updates), SID)
    for (const m of d.messages) {
      if (m.to === BROADCAST) expect(m.recipients).not.toContain(m.from)
      else expect(m.recipients).toEqual([m.to])
    }
  })

  test.prop([scenario])('deliveries are never before the send', (sc) => {
    const d = derive(storeOf(sc.updates), SID)
    for (const m of d.messages)
      for (const x of m.deliveries.values()) expect(x.ms).toBeGreaterThanOrEqual(0)
  })

  test.prop([scenario])('an agent is online exactly when it has presence', (sc) => {
    const d = derive(storeOf(sc.updates), SID)
    const live = new Set(
      sc.updates.flatMap((u) =>
        u.kind === 'presence'
          ? [
              formatAddress({
                agent: u.key.split('.')[2] ?? '',
                machine: u.key.split('.')[1] ?? '',
              }),
            ]
          : [],
      ),
    )
    for (const a of d.agents) expect(a.online).toBe(live.has(a.address))
  })
})
