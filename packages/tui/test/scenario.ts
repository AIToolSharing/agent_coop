// Random but consistent session histories for property tests of the model and the views.
import {
  type Address,
  BROADCAST,
  type BusEvent,
  type BusEventInput,
  buildPresenceKey,
  formatAddress,
  OPERATOR,
  type PresenceRecord,
  type To,
} from '@coop/core'
import { fc } from '@fast-check/vitest'
import { Store, type Update } from '../src/model.js'

export const SID = 's'
const T0 = Date.parse('2026-09-30T00:00:00.000Z')
const text = fc.string({ minLength: 1, maxLength: 30 })
const idx = fc.nat({ max: 3 })

type Step =
  | { k: 'send'; from: number; to: number; reply: number | undefined; text: string }
  | { k: 'op'; to: number; text: string }
  | { k: 'deliver'; msg: number; agent: number; via: 'push' | 'pull' | 'ask' }
  | { k: 'ask'; from: number; to: number; text: string }
  | { k: 'answer'; ask: number; text: string }
  | { k: 'wait_end'; agent: number }
  | { k: 'state'; agent: number; state: 'working' | 'blocked' | 'done' | 'idle' }
  | { k: 'redact'; msg: number }
  | { k: 'leave'; agent: number }
  | { k: 'join'; agent: number }

const step: fc.Arbitrary<Step> = fc.oneof(
  fc.record({
    k: fc.constant('send' as const),
    from: idx,
    to: fc.integer({ min: -1, max: 3 }),
    reply: fc.option(fc.nat({ max: 40 }), { nil: undefined }),
    text,
  }),
  fc.record({ k: fc.constant('op' as const), to: fc.integer({ min: -1, max: 3 }), text }),
  fc.record({
    k: fc.constant('deliver' as const),
    msg: fc.nat({ max: 40 }),
    agent: idx,
    via: fc.constantFrom('push', 'pull', 'ask' as const),
  }),
  fc.record({ k: fc.constant('ask' as const), from: idx, to: idx, text }),
  fc.record({ k: fc.constant('answer' as const), ask: fc.nat({ max: 10 }), text }),
  fc.record({ k: fc.constant('wait_end' as const), agent: idx }),
  fc.record({
    k: fc.constant('state' as const),
    agent: idx,
    state: fc.constantFrom('working', 'blocked', 'done', 'idle' as const),
  }),
  fc.record({ k: fc.constant('redact' as const), msg: fc.nat({ max: 40 }) }),
  fc.record({ k: fc.constant('leave' as const), agent: idx }),
  fc.record({ k: fc.constant('join' as const), agent: idx }),
)

export interface Scenario {
  readonly agents: readonly Address[]
  readonly updates: readonly Update[]
}

export const address = (i: number): Address => ({ agent: `a${i}`, machine: `m${i}` })

export function build(n: number, steps: readonly Step[], online: readonly boolean[]): Scenario {
  const agents = Array.from({ length: n }, (_, i) => address(i))
  const events: BusEvent[] = []
  const msgIds: string[] = []
  const asks: { id: string; from: Address; to: Address }[] = []
  let seq = 0
  const at = () => new Date(T0 + (seq + 1) * 1000).toISOString()
  const push = (e: BusEventInput) => {
    seq++
    const full: BusEvent = { ...e, seq }
    events.push(full)
    return String(seq)
  }
  const agentAt = (i: number) => agents[i % n] as Address
  const join = (a: Address) =>
    push({
      kind: 'evt',
      sid: SID,
      from: a,
      evt: {
        kind: 'joined',
        host: `h-${a.machine}`,
        cwd: '/w',
        client: { name: 'c', version: '1' },
        at: at(),
      },
    })
  for (const a of agents) join(a)
  for (const s of steps) {
    switch (s.k) {
      case 'send': {
        const from = agentAt(s.from)
        const to: To = s.to < 0 || s.to % n === s.from % n ? BROADCAST : agentAt(s.to)
        const reply =
          s.reply === undefined ? undefined : msgIds[s.reply % Math.max(1, msgIds.length)]
        const e = { kind: 'msg' as const, sid: SID, from, to, text: s.text, sent_at: at() }
        msgIds.push(push(reply === undefined ? e : { ...e, reply_to: reply }))
        break
      }
      case 'op': {
        const to: To = s.to < 0 ? BROADCAST : agentAt(s.to)
        msgIds.push(
          push({ kind: 'msg', sid: SID, from: OPERATOR, to, text: s.text, sent_at: at() }),
        )
        break
      }
      case 'deliver': {
        const id = msgIds[s.msg % Math.max(1, msgIds.length)]
        if (id === undefined) break
        push({
          kind: 'evt',
          sid: SID,
          from: agentAt(s.agent),
          evt: { kind: 'delivered', id, via: s.via, at: at() },
        })
        break
      }
      case 'ask': {
        const from = agentAt(s.from)
        const to = agentAt(s.to)
        if (s.from % n === s.to % n) break
        const id = push({ kind: 'msg', sid: SID, from, to, text: s.text, sent_at: at() })
        msgIds.push(id)
        asks.push({ id, from, to })
        push({
          kind: 'evt',
          sid: SID,
          from,
          evt: {
            kind: 'wait_start',
            from: formatAddress(to),
            reply_to: id,
            timeout_s: 60,
            at: at(),
          },
        })
        break
      }
      case 'answer': {
        const q = asks[s.ask % Math.max(1, asks.length)]
        if (q === undefined) break
        msgIds.push(
          push({
            kind: 'msg',
            sid: SID,
            from: q.to,
            to: q.from,
            text: s.text,
            reply_to: q.id,
            sent_at: at(),
          }),
        )
        break
      }
      case 'wait_end':
        push({
          kind: 'evt',
          sid: SID,
          from: agentAt(s.agent),
          evt: { kind: 'wait_end', result: 'timeout', at: at() },
        })
        break
      case 'state':
        push({
          kind: 'evt',
          sid: SID,
          from: agentAt(s.agent),
          evt: { kind: 'state', state: s.state, at: at() },
        })
        break
      case 'redact': {
        const id = msgIds[s.msg % Math.max(1, msgIds.length)]
        if (id !== undefined) push({ kind: 'redact', sid: SID, id, at: at() })
        break
      }
      case 'leave':
        push({
          kind: 'evt',
          sid: SID,
          from: agentAt(s.agent),
          evt: { kind: 'left', reason: 'disconnected', at: at() },
        })
        break
      case 'join':
        join(agentAt(s.agent))
        break
    }
  }
  const presence: Update[] = agents.flatMap((a, i) => {
    if (online[i] !== true) return []
    const record: PresenceRecord = {
      host: `h-${a.machine}`,
      cwd: '/w',
      client: { name: 'c', version: '1' },
      state: 'working',
      joined_at: new Date(T0).toISOString(),
      queued: 0,
    }
    return [{ kind: 'presence' as const, key: buildPresenceKey({ sid: SID, agent: a }), record }]
  })
  return {
    agents,
    updates: [
      {
        kind: 'session',
        sid: SID,
        record: { status: 'open', created_at: new Date(T0).toISOString() },
      },
      ...events.map((e) => ({ kind: 'event' as const, e })),
      ...presence,
    ],
  }
}

export const scenario: fc.Arbitrary<Scenario> = fc
  .tuple(
    fc.integer({ min: 1, max: 4 }),
    fc.array(step, { maxLength: 40 }),
    fc.array(fc.boolean(), { minLength: 4, maxLength: 4 }),
  )
  .map(([n, steps, online]) => build(n, steps, online))

export function storeOf(updates: readonly Update[]): Store {
  const s = new Store()
  s.apply(updates)
  return s
}
