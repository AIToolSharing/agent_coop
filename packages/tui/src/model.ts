// The TUI model. The store keeps raw facts (stream events by sequence, session records, presence
// records). `derive` turns them into what the views show. It sorts by sequence first, so the
// result does not depend on the order in which facts arrived.
import {
  type AgentState,
  BROADCAST,
  type BusEvent,
  type DeliveryVia,
  formatAddress,
  OPERATOR,
  type PresenceRecord,
  parsePresenceKey,
  type SessionRecord,
} from '@coop/core'

export type Update =
  | { readonly kind: 'event'; readonly e: BusEvent }
  | { readonly kind: 'session'; readonly sid: string; readonly record: SessionRecord | undefined }
  | { readonly kind: 'presence'; readonly key: string; readonly record: PresenceRecord | undefined }

/** Raw facts. Mutable; `version` changes on every update so views can cache. */
export class Store {
  readonly events = new Map<number, BusEvent>()
  readonly sessions = new Map<string, SessionRecord>()
  readonly presence = new Map<string, PresenceRecord>()
  version = 0

  apply(updates: readonly Update[]): void {
    for (const u of updates) {
      if (u.kind === 'event') this.events.set(u.e.seq, u.e)
      else if (u.kind === 'session') {
        if (u.record === undefined) {
          this.sessions.delete(u.sid)
          for (const [seq, e] of this.events) if (e.sid === u.sid) this.events.delete(seq)
        } else this.sessions.set(u.sid, u.record)
      } else if (u.record === undefined) this.presence.delete(u.key)
      else this.presence.set(u.key, u.record)
    }
    this.version++
  }
}

export interface Delivery {
  readonly at: string
  readonly via: DeliveryVia
  /** Milliseconds from send to delivery. */
  readonly ms: number
}

export interface MsgRow {
  readonly id: string
  readonly seq: number
  readonly sid: string
  readonly from: string
  readonly to: string
  readonly text: string
  readonly reply_to: string | undefined
  readonly sent_at: string
  /** Who should get it: the target, or every agent present at send time for `all`. */
  readonly recipients: readonly string[]
  readonly deliveries: ReadonlyMap<string, Delivery>
  readonly replies: readonly string[]
  readonly redacted: boolean
}

export interface AgentRow {
  readonly address: string
  readonly sid: string
  readonly host: string | undefined
  readonly cwd: string | undefined
  readonly client: string | undefined
  readonly online: boolean
  readonly state: AgentState | 'left' | 'unknown'
  readonly stateSince: string | undefined
  readonly note: string | undefined
  readonly waiting:
    | {
        readonly on: string | undefined
        readonly reply_to: string | undefined
        readonly since: string
      }
    | undefined
  readonly queued: number
  readonly sent: number
  readonly received: number
  readonly joinedAt: string | undefined
  readonly left: { readonly reason: string; readonly at: string } | undefined
  readonly order: number
}

export type TimelineItem =
  | { readonly kind: 'msg'; readonly seq: number; readonly at: string; readonly msg: MsgRow }
  | {
      readonly kind: 'sys'
      readonly seq: number
      readonly at: string
      readonly sid: string
      readonly who: string
      readonly text: string
    }

export interface OpenAsk {
  readonly id: string
  readonly from: string
  readonly to: string
  readonly since: string
  readonly text: string
}

export interface Matrix {
  /** Row and column labels: senders (operator first when present) and receivers plus `all`. */
  readonly senders: readonly string[]
  readonly receivers: readonly string[]
  readonly count: (from: string, to: string) => number
  /** Median delivery latency in ms from `from` to `to`, or undefined. */
  readonly latency: (from: string, to: string) => number | undefined
}

export interface Derived {
  readonly sid: string
  readonly agents: readonly AgentRow[]
  readonly messages: readonly MsgRow[]
  readonly timeline: readonly TimelineItem[]
  readonly openAsks: readonly OpenAsk[]
  readonly matrix: Matrix
}

export const ALL_SESSIONS = '*'

const who = (s: string | { agent: string; machine: string }) =>
  typeof s === 'string' ? s : formatAddress(s)

/** Everything the views need for one session, or for all sessions with `ALL_SESSIONS`. */
export function derive(store: Store, sid: string): Derived {
  const events = [...store.events.values()]
    .filter((e) => (sid === ALL_SESSIONS ? store.sessions.has(e.sid) : e.sid === sid))
    .sort((a, b) => a.seq - b.seq)

  const redacted = new Set<string>()
  for (const e of events) if (e.kind === 'redact') redacted.add(e.id)

  type AgentAcc = {
    -readonly [K in keyof AgentRow]: AgentRow[K]
  }
  const agents = new Map<string, AgentAcc>()
  const agent = (address: string, s: string): AgentAcc => {
    const key = `${s}/${address}`
    let a = agents.get(key)
    if (a === undefined) {
      a = {
        address,
        sid: s,
        host: undefined,
        cwd: undefined,
        client: undefined,
        online: false,
        state: 'unknown',
        stateSince: undefined,
        note: undefined,
        waiting: undefined,
        queued: 0,
        sent: 0,
        received: 0,
        joinedAt: undefined,
        left: undefined,
        order: agents.size,
      }
      agents.set(key, a)
    }
    return a
  }

  const present = new Map<string, Set<string>>()
  const presentIn = (s: string) => {
    let p = present.get(s)
    if (p === undefined) {
      p = new Set()
      present.set(s, p)
    }
    return p
  }

  type MsgAcc = Omit<MsgRow, 'deliveries' | 'replies'> & {
    deliveries: Map<string, Delivery>
    replies: string[]
  }
  const msgs = new Map<string, MsgAcc>()
  const timeline: TimelineItem[] = []
  const sys = (e: BusEvent, at: string, w: string, text: string) =>
    timeline.push({ kind: 'sys', seq: e.seq, at, sid: e.sid, who: w, text })
  const asks: { id: string; from: string; to: string; since: string }[] = []

  for (const e of events) {
    switch (e.kind) {
      case 'msg': {
        const from = who(e.from)
        const to = e.to === BROADCAST ? BROADCAST : who(e.to)
        if (from !== OPERATOR) agent(from, e.sid).sent++
        const recipients = to === BROADCAST ? [...presentIn(e.sid)].filter((a) => a !== from) : [to]
        const m: MsgAcc = {
          id: String(e.seq),
          seq: e.seq,
          sid: e.sid,
          from,
          to,
          text: e.text,
          reply_to: e.reply_to,
          sent_at: e.sent_at,
          recipients,
          deliveries: new Map(),
          replies: [],
          redacted: redacted.has(String(e.seq)),
        }
        msgs.set(m.id, m)
        if (e.reply_to !== undefined) msgs.get(e.reply_to)?.replies.push(m.id)
        timeline.push({ kind: 'msg', seq: e.seq, at: e.sent_at, msg: m })
        break
      }
      case 'kick':
        sys(e, e.at, who(e.target), 'removed by the operator')
        break
      case 'redact':
        sys(e, e.at, OPERATOR, `withdrew message #${e.id}`)
        break
      case 'evt': {
        const address = who(e.from)
        const a = agent(address, e.sid)
        const v = e.evt
        switch (v.kind) {
          case 'joined':
            a.host = v.host
            a.cwd = v.cwd
            a.client = `${v.client.name} ${v.client.version}`
            a.joinedAt = v.at
            a.left = undefined
            if (a.state === 'left' || a.state === 'unknown') {
              a.state = 'idle'
              a.stateSince = v.at
            }
            presentIn(e.sid).add(address)
            sys(e, v.at, address, `joined from ${v.host} (${a.client})`)
            break
          case 'left':
            a.left = { reason: v.reason, at: v.at }
            a.state = 'left'
            a.stateSince = v.at
            a.waiting = undefined
            presentIn(e.sid).delete(address)
            sys(e, v.at, address, `left (${v.reason})`)
            break
          case 'state':
            a.state = v.state
            a.note = v.note
            a.stateSince = v.at
            sys(e, v.at, address, `is ${v.state}${v.note ? `: ${v.note}` : ''}`)
            break
          case 'delivered': {
            const m = msgs.get(v.id)
            if (m !== undefined && !m.deliveries.has(address)) {
              const ms = Math.max(0, Date.parse(v.at) - Date.parse(m.sent_at))
              m.deliveries.set(address, { at: v.at, via: v.via, ms })
              a.received++
            }
            break
          }
          case 'wait_start':
            a.waiting = { on: v.from, reply_to: v.reply_to, since: v.at }
            if (v.reply_to !== undefined && v.from !== undefined) {
              asks.push({ id: v.reply_to, from: address, to: v.from, since: v.at })
            }
            sys(
              e,
              v.at,
              address,
              `waits${v.from ? ` for ${v.from}` : ''}${v.reply_to ? ` (ask #${v.reply_to})` : ''}`,
            )
            break
          case 'wait_end':
            a.waiting = undefined
            sys(e, v.at, address, `wait ended: ${v.result}`)
            break
        }
        break
      }
    }
  }

  // Live presence overrides the derived state: it is what the agent reports now.
  for (const [key, rec] of store.presence) {
    const k = parsePresenceKey(key)
    if (k === undefined || (sid !== ALL_SESSIONS && k.sid !== sid)) continue
    const a = agent(formatAddress(k.agent), k.sid)
    a.online = true
    a.state = rec.state
    a.note = rec.note
    a.queued = rec.queued
    a.waiting = rec.waiting && {
      on: rec.waiting.on,
      reply_to: rec.waiting.reply_to,
      since: rec.waiting.since,
    }
    a.host ??= rec.host
    a.cwd ??= rec.cwd
    a.client ??= `${rec.client.name} ${rec.client.version}`
  }

  const messages = [...msgs.values()]
  const openAsks: OpenAsk[] = []
  for (const q of asks) {
    const answered = messages.some((m) => m.reply_to === q.id && m.from === q.to)
    const question = msgs.get(q.id)
    if (!answered && question !== undefined && !openAsks.some((o) => o.id === q.id)) {
      openAsks.push({ ...q, text: question.text })
    }
  }
  openAsks.sort((a, b) => a.since.localeCompare(b.since))

  const agentRows = [...agents.values()].sort((a, b) => a.order - b.order)
  return {
    sid,
    agents: agentRows,
    messages,
    timeline,
    openAsks,
    matrix: matrixOf(
      messages,
      agentRows.map((a) => a.address),
    ),
  }
}

function matrixOf(messages: readonly MsgRow[], agents: readonly string[]): Matrix {
  const counts = new Map<string, number>()
  const lat = new Map<string, number[]>()
  const key = (f: string, t: string) => `${f}\u0000${t}`
  for (const m of messages) {
    if (m.redacted) continue
    counts.set(key(m.from, m.to), (counts.get(key(m.from, m.to)) ?? 0) + 1)
    for (const [r, d] of m.deliveries) {
      const k = key(m.from, r)
      const l = lat.get(k) ?? []
      l.push(d.ms)
      lat.set(k, l)
    }
  }
  const hasOperator = messages.some((m) => m.from === OPERATOR)
  return {
    senders: hasOperator ? [OPERATOR, ...agents] : [...agents],
    receivers: [...agents, BROADCAST],
    count: (f, t) => counts.get(key(f, t)) ?? 0,
    latency: (f, t) => median(lat.get(key(f, t))),
  }
}

function median(xs: number[] | undefined): number | undefined {
  if (xs === undefined || xs.length === 0) return undefined
  const s = [...xs].sort((a, b) => a - b)
  const mid = Math.floor(s.length / 2)
  return s.length % 2 === 1 ? s[mid] : ((s[mid - 1] ?? 0) + (s[mid] ?? 0)) / 2
}
