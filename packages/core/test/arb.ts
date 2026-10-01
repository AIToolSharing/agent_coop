// fast-check arbitraries for core types. Other packages' tests import them from here.
import { fc } from '@fast-check/vitest'
import {
  type Address,
  BROADCAST,
  type BusEvent,
  type EvtPayload,
  isAgentName,
  OPERATOR,
  type PresenceRecord,
  type SessionRecord,
  type Subject,
  type TokenRecord,
} from '../src/index.js'

export const token = fc.stringMatching(/^[a-z0-9_-]{1,64}$/)
export const agentName = token.filter(isAgentName)
export const address: fc.Arbitrary<Address> = fc.record({ agent: agentName, machine: token })
export const iso = fc
  .date({
    min: new Date('2000-01-01T00:00:00Z'),
    max: new Date('2100-01-01T00:00:00Z'),
    noInvalidDate: true,
  })
  .map((d) => d.toISOString())
export const seq = fc.integer({ min: 1, max: Number.MAX_SAFE_INTEGER })
export const id = seq.map(String)
/** Any Unicode, lone surrogates included, 1 to 300 code points. */
export const text = fc.string({ unit: 'binary', minLength: 1, maxLength: 300 })
export const shortText = (max: number) => fc.string({ maxLength: max })
export const agentState = fc.constantFrom('working', 'blocked', 'done', 'idle' as const)
export const client = fc.record({ name: shortText(64), version: shortText(64) })

export const subject: fc.Arbitrary<Subject> = fc.oneof(
  fc.record({ kind: fc.constant('msg' as const), sid: token, from: address }),
  fc.record({ kind: fc.constant('evt' as const), sid: token, from: address }),
  fc.record({ kind: fc.constant('ops' as const), sid: token }),
)

const sender = fc.oneof(address, fc.constant(OPERATOR))
const recipient = fc.oneof(address, fc.constant(BROADCAST), fc.constant(OPERATOR))

export const evtPayload: fc.Arbitrary<EvtPayload> = fc.oneof(
  fc.record({
    kind: fc.constant('joined' as const),
    host: shortText(256),
    cwd: shortText(4096),
    client,
    at: iso,
  }),
  fc.record({
    kind: fc.constant('left' as const),
    reason: fc.constantFrom('disconnected', 'kicked', 'revoked', 'closed' as const),
    at: iso,
  }),
  fc.record(
    { kind: fc.constant('state' as const), state: agentState, note: shortText(500), at: iso },
    { requiredKeys: ['kind', 'state', 'at'] },
  ),
  fc.record({
    kind: fc.constant('delivered' as const),
    id,
    via: fc.constantFrom('push', 'pull', 'ask' as const),
    at: iso,
  }),
  fc.record(
    {
      kind: fc.constant('wait_start' as const),
      from: fc.oneof(
        address.map((a) => `${a.agent}@${a.machine}`),
        fc.constant(OPERATOR),
      ),
      reply_to: id,
      timeout_s: fc.integer({ min: 1, max: 600 }),
      at: iso,
    },
    { requiredKeys: ['kind', 'timeout_s', 'at'] },
  ),
  fc.record({
    kind: fc.constant('wait_end' as const),
    result: fc.constantFrom('message', 'timeout', 'cancelled' as const),
    at: iso,
  }),
)

export const msgEvent = fc.record(
  {
    kind: fc.constant('msg' as const),
    seq,
    sid: token,
    from: sender,
    to: recipient,
    text,
    reply_to: id,
    sent_at: iso,
  },
  { requiredKeys: ['kind', 'seq', 'sid', 'from', 'to', 'text', 'sent_at'] },
)

export const busEvent: fc.Arbitrary<BusEvent> = fc.oneof(
  msgEvent,
  fc.record({ kind: fc.constant('kick' as const), seq, sid: token, target: address, at: iso }),
  fc.record({ kind: fc.constant('redact' as const), seq, sid: token, id, at: iso }),
  fc.record({ kind: fc.constant('evt' as const), seq, sid: token, from: address, evt: evtPayload }),
)

export const sessionRecord: fc.Arbitrary<SessionRecord> = fc.record(
  {
    status: fc.constantFrom('open', 'closed' as const),
    title: shortText(200),
    created_at: iso,
    closed_at: iso,
  },
  { requiredKeys: ['status', 'created_at'] },
)

export const presenceRecord: fc.Arbitrary<PresenceRecord> = fc.record(
  {
    host: shortText(256),
    cwd: shortText(4096),
    client,
    state: agentState,
    note: shortText(500),
    joined_at: iso,
    queued: fc.integer({ min: 0, max: 1_000_000 }),
    waiting: fc.record(
      {
        on: fc.oneof(
          address.map((a) => `${a.agent}@${a.machine}`),
          fc.constant(OPERATOR),
        ),
        reply_to: id,
        since: iso,
      },
      { requiredKeys: ['since'] },
    ),
  },
  { requiredKeys: ['host', 'cwd', 'client', 'state', 'joined_at', 'queued'] },
)

export const tokenRecord: fc.Arbitrary<TokenRecord> = fc.record(
  { sha256: fc.stringMatching(/^[0-9a-f]{64}$/), created_at: iso, revoked_at: iso },
  { requiredKeys: ['sha256', 'created_at'] },
)
