// Names, addresses, broker subjects and KV keys. This file is the only place that builds or
// parses them, so a change of layout is a change here.

// The patterns are the single definition of the name rules. The OpenAPI document carries them,
// so a schema-valid request never fails a name rule inside the hub.

/** A name token: a session id or a machine name. No dots (subject separator). */
export const TOKEN_RE = /^[a-z0-9_-]{1,64}$/
/** An agent name: a token that is not a reserved name. */
export const AGENT_RE = /^(?!(?:operator|all)$)[a-z0-9_-]{1,64}$/
/** An address `agent@machine`. */
export const ADDRESS_RE = /^(?!(?:operator|all)@)[a-z0-9_-]{1,64}@[a-z0-9_-]{1,64}$/
/** A peer as a client writes it: an agent name or an address. */
export const PEER_RE = /^(?!(?:operator|all)(?:@|$))[a-z0-9_-]{1,64}(?:@[a-z0-9_-]{1,64})?$/
/** A recipient as a client writes it: `all`, `operator` (the user), or a peer. */
export const RECIPIENT_RE =
  /^(?:all|operator|(?!(?:operator|all)(?:@|$))[a-z0-9_-]{1,64}(?:@[a-z0-9_-]{1,64})?)$/

/** Agent names that have a meaning in addressing. */
export const RESERVED_AGENT_NAMES: ReadonlySet<string> = new Set(['operator', 'all'])

export const OPERATOR = 'operator'
export const BROADCAST = 'all'

export const STREAM_NAME = 'COOP'
export const SESSIONS_BUCKET = 'coop_sessions'
export const PRESENCE_BUCKET = 'coop_presence'
export const TOKENS_BUCKET = 'coop_tokens'

export function isToken(s: string): boolean {
  return TOKEN_RE.test(s)
}

export function isAgentName(s: string): boolean {
  return AGENT_RE.test(s)
}

/** One agent: its name inside the session and the machine that runs it. */
export interface Address {
  readonly agent: string
  readonly machine: string
}

export function formatAddress(a: Address): string {
  return `${a.agent}@${a.machine}`
}

export function parseAddress(s: string): Address | undefined {
  const parts = s.split('@')
  if (parts.length !== 2) return undefined
  const [agent, machine] = parts as [string, string]
  if (!isAgentName(agent) || !isToken(machine)) return undefined
  return { agent, machine }
}

export function sameAddress(a: Address, b: Address): boolean {
  return a.agent === b.agent && a.machine === b.machine
}

/** A subject in stream COOP. */
export type Subject =
  | { readonly kind: 'msg'; readonly sid: string; readonly from: Address }
  | { readonly kind: 'evt'; readonly sid: string; readonly from: Address }
  | { readonly kind: 'ops'; readonly sid: string }

export function buildSubject(s: Subject): string {
  if (s.kind === 'ops') return `coop.${s.sid}.ops`
  return `coop.${s.sid}.${s.kind}.${s.from.machine}.${s.from.agent}`
}

export function parseSubject(subject: string): Subject | undefined {
  const t = subject.split('.')
  if (t[0] !== 'coop' || t[1] === undefined || !isToken(t[1])) return undefined
  const sid = t[1]
  if (t.length === 3 && t[2] === 'ops') return { kind: 'ops', sid }
  if (t.length !== 5) return undefined
  const [, , kind, machine, agent] = t as [string, string, string, string, string]
  if ((kind !== 'msg' && kind !== 'evt') || !isToken(machine) || !isAgentName(agent)) {
    return undefined
  }
  return { kind, sid, from: { agent, machine } }
}

/** Every subject of one session. */
export function sessionSubjects(sid: string): string {
  return `coop.${sid}.>`
}

/** The subjects that an agent can receive from: agent messages and operator events. */
export function deliverySubjects(sid: string): string[] {
  return [`coop.${sid}.msg.>`, `coop.${sid}.ops`]
}

/** A key in bucket coop_sessions. */
export type SessionsKey =
  | { readonly kind: 'session'; readonly sid: string }
  | { readonly kind: 'kick'; readonly sid: string; readonly target: Address }

export function buildSessionsKey(k: SessionsKey): string {
  if (k.kind === 'session') return k.sid
  return `${k.sid}.kick.${k.target.machine}.${k.target.agent}`
}

export function parseSessionsKey(key: string): SessionsKey | undefined {
  const t = key.split('.')
  if (t.length === 1 && isToken(key)) return { kind: 'session', sid: key }
  if (t.length !== 4 || t[1] !== 'kick') return undefined
  const [sid, , machine, agent] = t as [string, string, string, string]
  if (!isToken(sid) || !isToken(machine) || !isAgentName(agent)) return undefined
  return { kind: 'kick', sid, target: { agent, machine } }
}

/** A key in bucket coop_presence. */
export interface PresenceKey {
  readonly sid: string
  readonly agent: Address
}

export function buildPresenceKey(k: PresenceKey): string {
  return `${k.sid}.${k.agent.machine}.${k.agent.agent}`
}

export function parsePresenceKey(key: string): PresenceKey | undefined {
  const t = key.split('.')
  if (t.length !== 3) return undefined
  const [sid, machine, agent] = t as [string, string, string]
  if (!isToken(sid) || !isToken(machine) || !isAgentName(agent)) return undefined
  return { sid, agent: { agent, machine } }
}
