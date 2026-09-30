// Broker payloads (stream COOP and the KV buckets) and the conversion between a decoded event and
// its wire form. The sender of an agent event comes from the subject, never from the payload.
import { z } from 'zod'
import {
  ADDRESS_RE,
  type Address,
  AGENT_RE,
  BROADCAST,
  buildSubject,
  formatAddress,
  OPERATOR,
  parseAddress,
  parseSubject,
  TOKEN_RE,
} from './names.js'

export const MAX_TEXT = 8000

/** A message id: the stream sequence of the message, in decimal. */
export const Id = z.string().regex(/^[1-9][0-9]{0,15}$/)
export const Iso = z.iso.datetime()
export const Token = z.string().regex(TOKEN_RE)
export const AgentName = z.string().regex(AGENT_RE)
export const AddressStr = z.string().regex(ADDRESS_RE)
export const Recipient = z.union([z.literal(BROADCAST), AddressStr])
export const Text = z.string().min(1).max(MAX_TEXT)
export const AgentState = z.enum(['working', 'blocked', 'done', 'idle'])
export const DeliveryVia = z.enum(['push', 'pull', 'ask'])
export const Client = z.strictObject({ name: z.string().max(64), version: z.string().max(64) })

export type AgentState = z.infer<typeof AgentState>
export type DeliveryVia = z.infer<typeof DeliveryVia>

/** Payload on coop.<sid>.msg.<machine>.<agent>. */
export const MsgPayload = z.strictObject({
  to: Recipient,
  text: Text,
  reply_to: Id.optional(),
  sent_at: Iso,
})

/** Payload on coop.<sid>.ops. Only the operator writes it. */
export const OpsPayload = z.discriminatedUnion('kind', [
  z.strictObject({
    kind: z.literal('msg'),
    to: Recipient,
    text: Text,
    reply_to: Id.optional(),
    sent_at: Iso,
  }),
  z.strictObject({ kind: z.literal('kick'), target: AddressStr, at: Iso }),
  z.strictObject({ kind: z.literal('redact'), id: Id, at: Iso }),
])

/** Payload on coop.<sid>.evt.<machine>.<agent>: agent activity, written by the hub. */
export const EvtPayload = z.discriminatedUnion('kind', [
  z.strictObject({
    kind: z.literal('joined'),
    host: z.string().max(256),
    cwd: z.string().max(4096),
    client: Client,
    at: Iso,
  }),
  z.strictObject({
    kind: z.literal('left'),
    reason: z.enum(['disconnected', 'kicked', 'revoked', 'closed']),
    at: Iso,
  }),
  z.strictObject({
    kind: z.literal('state'),
    state: AgentState,
    note: z.string().max(500).optional(),
    at: Iso,
  }),
  z.strictObject({ kind: z.literal('delivered'), id: Id, via: DeliveryVia, at: Iso }),
  z.strictObject({
    kind: z.literal('wait_start'),
    from: AddressStr.optional(),
    reply_to: Id.optional(),
    timeout_s: z.int().min(1).max(600),
    at: Iso,
  }),
  z.strictObject({
    kind: z.literal('wait_end'),
    result: z.enum(['message', 'timeout', 'cancelled']),
    at: Iso,
  }),
])

export type EvtPayload = z.infer<typeof EvtPayload>

/** Value of key <sid> in bucket coop_sessions. */
export const SessionRecord = z.strictObject({
  status: z.enum(['open', 'closed']),
  title: z.string().max(200).optional(),
  created_at: Iso,
  closed_at: Iso.optional(),
})

/** Value of key <sid>.kick.<machine>.<agent> in bucket coop_sessions. */
export const KickRecord = z.strictObject({ at: Iso })

/** Value of key <sid>.<machine>.<agent> in bucket coop_presence. */
export const PresenceRecord = z.strictObject({
  host: z.string().max(256),
  cwd: z.string().max(4096),
  client: Client,
  state: AgentState,
  note: z.string().max(500).optional(),
  joined_at: Iso,
  queued: z.int().min(0),
  waiting: z
    .strictObject({ on: AddressStr.optional(), reply_to: Id.optional(), since: Iso })
    .optional(),
})

/** Value of key <machine> in bucket coop_tokens. Only the SHA-256 of a token is stored. */
export const TokenRecord = z.strictObject({
  sha256: z.string().regex(/^[0-9a-f]{64}$/),
  created_at: Iso,
  revoked_at: Iso.optional(),
})

export type SessionRecord = z.infer<typeof SessionRecord>
export type KickRecord = z.infer<typeof KickRecord>
export type PresenceRecord = z.infer<typeof PresenceRecord>
export type TokenRecord = z.infer<typeof TokenRecord>

const encoder = new TextEncoder()
const decoder = new TextDecoder()

/** Validate and serialize a value. A schema failure here is a bug in the caller, so it throws. */
export function encode<S extends z.ZodType>(schema: S, value: z.input<S>): Uint8Array {
  return encoder.encode(JSON.stringify(schema.parse(value)))
}

/** Parse and validate bytes from the broker. Bad data gives undefined; it never throws. */
export function decode<S extends z.ZodType>(schema: S, data: Uint8Array): z.output<S> | undefined {
  let json: unknown
  try {
    json = JSON.parse(decoder.decode(data))
  } catch {
    return undefined
  }
  const r = schema.safeParse(json)
  return r.success ? r.data : undefined
}

export type Sender = Address | typeof OPERATOR
export type To = Address | typeof BROADCAST

/** One decoded event of stream COOP. `seq` is the stream sequence. */
export type BusEvent =
  | {
      readonly kind: 'msg'
      readonly seq: number
      readonly sid: string
      readonly from: Sender
      readonly to: To
      readonly text: string
      readonly reply_to?: string
      readonly sent_at: string
    }
  | {
      readonly kind: 'kick'
      readonly seq: number
      readonly sid: string
      readonly target: Address
      readonly at: string
    }
  | {
      readonly kind: 'redact'
      readonly seq: number
      readonly sid: string
      readonly id: string
      readonly at: string
    }
  | {
      readonly kind: 'evt'
      readonly seq: number
      readonly sid: string
      readonly from: Address
      readonly evt: EvtPayload
    }

export type BusEventInput = BusEvent extends infer E
  ? E extends BusEvent
    ? Omit<E, 'seq'>
    : never
  : never

function toStr(to: To): string {
  return to === BROADCAST ? BROADCAST : formatAddress(to)
}

function fromStr(to: string): To {
  if (to === BROADCAST) return BROADCAST
  const a = parseAddress(to)
  if (a === undefined) throw new Error(`schema accepted a bad recipient: ${to}`)
  return a
}

function withReply<T extends object>(
  base: T,
  reply_to: string | undefined,
): T & { reply_to?: string } {
  return reply_to === undefined ? base : { ...base, reply_to }
}

function msgEvent(
  seq: number,
  sid: string,
  from: Sender,
  p: { to: string; text: string; reply_to?: string | undefined; sent_at: string },
): BusEvent {
  const base = { kind: 'msg' as const, seq, sid, from, to: fromStr(p.to), text: p.text }
  return withReply({ ...base, sent_at: p.sent_at }, p.reply_to)
}

/** The subject and payload that carry an event. The inverse of decodeBusEvent. */
export function toWire(e: BusEventInput): { subject: string; data: Uint8Array } {
  switch (e.kind) {
    case 'msg': {
      const body = withReply({ to: toStr(e.to), text: e.text, sent_at: e.sent_at }, e.reply_to)
      if (e.from === OPERATOR) {
        return {
          subject: buildSubject({ kind: 'ops', sid: e.sid }),
          data: encode(OpsPayload, { kind: 'msg', ...body }),
        }
      }
      return {
        subject: buildSubject({ kind: 'msg', sid: e.sid, from: e.from }),
        data: encode(MsgPayload, body),
      }
    }
    case 'kick':
      return {
        subject: buildSubject({ kind: 'ops', sid: e.sid }),
        data: encode(OpsPayload, { kind: 'kick', target: formatAddress(e.target), at: e.at }),
      }
    case 'redact':
      return {
        subject: buildSubject({ kind: 'ops', sid: e.sid }),
        data: encode(OpsPayload, { kind: 'redact', id: e.id, at: e.at }),
      }
    case 'evt':
      return {
        subject: buildSubject({ kind: 'evt', sid: e.sid, from: e.from }),
        data: encode(EvtPayload, e.evt),
      }
  }
}

/** Decode one stream message. Unknown subjects and bad payloads give undefined. */
export function decodeBusEvent(
  subject: string,
  data: Uint8Array,
  seq: number,
): BusEvent | undefined {
  const s = parseSubject(subject)
  if (s === undefined) return undefined
  switch (s.kind) {
    case 'msg': {
      const p = decode(MsgPayload, data)
      if (p === undefined) return undefined
      return msgEvent(seq, s.sid, s.from, p)
    }
    case 'evt': {
      const evt = decode(EvtPayload, data)
      return evt === undefined ? undefined : { kind: 'evt', seq, sid: s.sid, from: s.from, evt }
    }
    case 'ops': {
      const p = decode(OpsPayload, data)
      if (p === undefined) return undefined
      if (p.kind === 'kick') {
        const target = parseAddress(p.target)
        return target && { kind: 'kick', seq, sid: s.sid, target, at: p.at }
      }
      if (p.kind === 'redact') return { kind: 'redact', seq, sid: s.sid, id: p.id, at: p.at }
      return msgEvent(seq, s.sid, OPERATOR, p)
    }
  }
}
