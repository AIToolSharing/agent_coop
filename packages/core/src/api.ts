// The HTTP contract between the hub and the local MCP server (the shim). Both sides validate with
// these schemas, so a contract change is a change here.
import { z } from 'zod'
import { BROADCAST, formatAddress, OPERATOR, PEER_RE, RECIPIENT_RE } from './names.js'
import {
  AddressStr,
  AgentName,
  AgentState,
  type BusEvent,
  DeliveryVia,
  Id,
  Iso,
  SessionRecord,
  Text,
  Token,
  WaitTarget,
} from './schema.js'

/** A peer as a client writes it: a bare agent name, or `agent@machine`. */
export const PeerInput = z.string().regex(PEER_RE)
/** `to` as a client writes it: `all`, or a peer. */
export const RecipientInput = z.string().regex(RECIPIENT_RE)
/** Whom an `ask` or a `wait` is about, as a client writes it: a peer, or `operator` (the user). */
export const WaitTargetInput = z.union([z.literal(OPERATOR), PeerInput])

export const ApiMessage = z.strictObject({
  id: Id,
  from: z.union([z.literal(OPERATOR), AddressStr]),
  to: z.union([z.literal(BROADCAST), z.literal(OPERATOR), AddressStr]),
  text: Text,
  reply_to: Id.optional(),
  sent_at: Iso,
})
export type ApiMessage = z.infer<typeof ApiMessage>

export const StreamQuery = z.strictObject({
  agent: AgentName,
  /** Random per shim process. A join with the same instance replaces the old stream (resume). */
  instance: z.uuid(),
  host: z.string().max(256),
  cwd: z.string().max(4096),
  client_name: z.string().max(64),
  client_version: z.string().max(64),
})
export type StreamQuery = z.infer<typeof StreamQuery>

export const SendRequest = z.strictObject({
  agent: AgentName,
  to: RecipientInput,
  text: Text,
  reply_to: Id.optional(),
})
export type SendRequest = z.infer<typeof SendRequest>

export const SendResponse = z.strictObject({
  id: Id,
  to: ApiMessage.shape.to,
  /** False when the recipient left the session: it gets the message when it joins again. */
  online: z.boolean(),
  sent_at: Iso,
})
export type SendResponse = z.infer<typeof SendResponse>

export const ActivityRequest = z.discriminatedUnion('kind', [
  z.strictObject({
    kind: z.literal('state'),
    agent: AgentName,
    state: AgentState,
    note: z.string().max(500).optional(),
  }),
  z.strictObject({ kind: z.literal('delivered'), agent: AgentName, id: Id, via: DeliveryVia }),
  z.strictObject({
    kind: z.literal('wait_start'),
    agent: AgentName,
    from: WaitTargetInput.optional(),
    reply_to: Id.optional(),
    timeout_s: z.int().min(1).max(600),
  }),
  z.strictObject({
    kind: z.literal('wait_end'),
    agent: AgentName,
    result: z.enum(['message', 'timeout', 'cancelled']),
  }),
])
export type ActivityRequest = z.infer<typeof ActivityRequest>

export const Peer = z.strictObject({
  name: AddressStr,
  state: AgentState,
  note: z.string().max(500).optional(),
  /** False for a peer that left; it is listed with its last state and can still be written to. */
  online: z.boolean(),
  waiting_on: WaitTarget.optional(),
})
export type Peer = z.infer<typeof Peer>

export const SessionView = z.strictObject({
  session: Token,
  status: z.enum(['open', 'closed']),
  me: AddressStr,
  peers: z.array(Peer),
})
export type SessionView = z.infer<typeof SessionView>

export const HistoryQuery = z.strictObject({
  agent: AgentName,
  with: PeerInput.optional(),
  limit: z.coerce.number().int().min(1).max(200).default(50),
})

export const HistoryResponse = z.strictObject({ messages: z.array(ApiMessage) })
export type HistoryResponse = z.infer<typeof HistoryResponse>

/** SSE event `joined`: the first event of a stream. */
export const JoinedEvent = z.strictObject({ me: AddressStr, session: Token })

/** SSE event `notice`: something the agent must know that is not a message. */
export const NoticeEvent = z.strictObject({
  kind: z.enum(['kicked', 'closed', 'reopened', 'redacted', 'peer_left']),
  /** The withdrawn message, for `redacted`. */
  id: Id.optional(),
  /** The peer that left, for `peer_left`. */
  peer: AddressStr.optional(),
  at: Iso,
})
export type NoticeEvent = z.infer<typeof NoticeEvent>

export const ErrorCode = z.enum([
  'unauthorized',
  'forbidden',
  'not_found',
  'conflict',
  'ambiguous',
  'too_large',
  'invalid',
  'rate_limited',
  'unavailable',
])
export type ErrorCode = z.infer<typeof ErrorCode>

export const ErrorBody = z.strictObject({ error: ErrorCode, message: z.string() })
export type ErrorBody = z.infer<typeof ErrorBody>

// --- Admin API: the operator token (the TUI) -------------------------------------------------

export const SessionInfo = z.strictObject({ session: Token, ...SessionRecord.shape })
export type SessionInfo = z.infer<typeof SessionInfo>
export const SessionList = z.strictObject({ sessions: z.array(SessionInfo) })
export type SessionList = z.infer<typeof SessionList>
export const CreateSessionRequest = z.strictObject({
  session: Token,
  title: z.string().max(200).optional(),
})
export const TargetRequest = z.strictObject({ target: AddressStr })
export const RedactRequest = z.strictObject({ id: Id })
/** What the operator writes as `to`: `all`, or a peer (`name` or `name@machine`). */
export const OperatorSendRequest = z.strictObject({
  to: z.union([z.literal(BROADCAST), PeerInput]),
  text: Text,
  reply_to: Id.optional(),
})
export type OperatorSendRequest = z.infer<typeof OperatorSendRequest>
export const OperatorSendResponse = z.strictObject({
  id: Id,
  to: z.union([z.literal(BROADCAST), AddressStr]),
  sent_at: Iso,
})
export type OperatorSendResponse = z.infer<typeof OperatorSendResponse>

/** The API form of a message event. */
export function toApiMessage(e: Extract<BusEvent, { kind: 'msg' }>): ApiMessage {
  const m: ApiMessage = {
    id: String(e.seq),
    from: e.from === OPERATOR ? OPERATOR : formatAddress(e.from),
    to: typeof e.to === 'string' ? e.to : formatAddress(e.to),
    text: e.text,
    sent_at: e.sent_at,
  }
  return e.reply_to === undefined ? m : { ...m, reply_to: e.reply_to }
}
