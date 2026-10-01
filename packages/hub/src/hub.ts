// The hub's rules, apart from HTTP. Every agent request passes here: the hub decides who the
// sender is, what each agent may see, and what each agent receives.
import {
  type ActivityRequest,
  type Address,
  type AgentState,
  type ApiMessage,
  BROADCAST,
  type BusEvent,
  buildPresenceKey,
  buildSessionsKey,
  decode,
  deliveryFor,
  deliverySubjects,
  encode,
  formatAddress,
  type HistoryResponse,
  Id,
  isAgentName,
  isVisible,
  type NoticeEvent,
  OPERATOR,
  PresenceRecord,
  parseAddress,
  parseSessionsKey,
  type SendRequest,
  type SendResponse,
  SessionRecord,
  type SessionView,
  type StreamQuery,
  sameAddress,
  type To,
  TokenRecord,
  toApiMessage,
} from '@coop/core'
import {
  type Broker,
  follow,
  lastSeq,
  Operator,
  PRESENCE_HEARTBEAT_MS,
  publishEvent,
  readRange,
} from '@coop/core/broker'
import { HubError } from './errors.js'
import { RateLimiter } from './limiter.js'
import { verifyToken } from './tokens.js'

/** Where a connection's server-sent events go. */
export interface Sink {
  write(event: 'joined' | 'message' | 'notice', data: unknown, id?: string): Promise<void>
  ping(): Promise<void>
  close(): void
}

type CloseReason = 'disconnected' | 'kicked' | 'revoked' | 'closed' | 'replaced'

const PING_MS = 15_000

export interface Limit {
  readonly burst: number
  readonly perSecond: number
}

/** Per machine. A machine runs a few agents; these leave room for normal use. */
export const DEFAULT_LIMITS: Record<'join' | 'msg' | 'activity', Limit> = {
  join: { burst: 10, perSecond: 1 },
  msg: { burst: 30, perSecond: 5 },
  activity: { burst: 120, perSecond: 20 },
}
const now = () => new Date().toISOString()

export interface HubOptions {
  /** Let the first join of an unknown session create it, open. A closed session stays closed. */
  readonly autoCreate?: boolean
}

class Connection {
  state: AgentState = 'idle'
  note: string | undefined
  waiting: { on?: Address; reply_to?: string; since: string } | undefined
  /** Ids of messages written to the stream, and of those the shim reported delivered. */
  readonly sent = new Set<string>()
  readonly acked = new Set<string>()
  readonly ctl = new AbortController()
  closeReason: CloseReason = 'disconnected'
  sink: Sink | undefined

  constructor(
    readonly sid: string,
    readonly me: Address,
    readonly instance: string,
    readonly meta: { host: string; cwd: string; client: { name: string; version: string } },
    readonly joinedAt: string,
    readonly resumed: boolean,
  ) {}

  get key() {
    return buildPresenceKey({ sid: this.sid, agent: this.me })
  }

  get queued() {
    let n = 0
    for (const id of this.sent) if (!this.acked.has(id)) n++
    return n
  }

  close(reason: CloseReason) {
    this.closeReason = reason
    this.ctl.abort()
  }
}

export interface Joined {
  readonly conn: Connection
  readonly startSeq: number
}

export class Hub {
  private readonly conns = new Map<string, Connection>()
  private readonly status = new Map<string, 'open' | 'closed'>()
  private readonly stops: (() => void)[] = []
  private readonly limits: { join: RateLimiter; msg: RateLimiter; activity: RateLimiter }
  private readonly op: Operator

  constructor(
    private readonly b: Broker,
    limits: Partial<Record<'join' | 'msg' | 'activity', Limit>> = {},
    private readonly options: HubOptions = {},
  ) {
    this.op = new Operator(b)
    const l = { ...DEFAULT_LIMITS, ...limits }
    this.limits = {
      join: new RateLimiter(l.join.burst, l.join.perSecond),
      msg: new RateLimiter(l.msg.burst, l.msg.perSecond),
      activity: new RateLimiter(l.activity.burst, l.activity.perSecond),
    }
  }

  /** Watch sessions and tokens, and start the presence heartbeat. */
  async start(): Promise<void> {
    const sessions = await this.b.sessions.watch()
    const tokens = await this.b.tokens.watch()
    this.stops.push(
      () => sessions.stop(),
      () => tokens.stop(),
    )
    void this.onSessions(sessions)
    void this.onTokens(tokens)
    const beat = setInterval(() => void this.heartbeat(), PRESENCE_HEARTBEAT_MS)
    this.stops.push(() => clearInterval(beat))
  }

  async stop(): Promise<void> {
    for (const s of this.stops.splice(0)) s()
    for (const c of this.conns.values()) c.close('disconnected')
  }

  async auth(header: string | undefined): Promise<string> {
    const token = header?.match(/^Bearer (\S+)$/)?.[1]
    const machine = token === undefined ? undefined : await verifyToken(this.b.tokens, token)
    if (machine === undefined) throw new HubError('unauthorized', 'missing or invalid token')
    return machine
  }

  /** Check a join and reserve the name. The caller then runs the stream with `run`. */
  async join(
    machine: string,
    sid: string,
    q: StreamQuery,
    lastEventId: string | undefined,
  ): Promise<Joined> {
    if (!this.limits.join.take(machine)) throw new HubError('rate_limited', 'too many joins')
    await this.requireOpen(sid, this.options.autoCreate === true)
    const me: Address = { agent: q.agent, machine }
    const kick = await this.b.sessions.get(buildSessionsKey({ kind: 'kick', sid, target: me }))
    if (kick?.operation === 'PUT') throw new HubError('forbidden', 'removed from session')

    const resume = lastEventId !== undefined && Id.safeParse(lastEventId).success
    const startSeq = resume ? Number(lastEventId) + 1 : (await lastSeq(this.b.jsm)) + 1

    // No await from here to the reservation: two joins of one name must not both pass the check.
    const key = buildPresenceKey({ sid, agent: me })
    const old = this.conns.get(key)
    if (old !== undefined && old.instance !== q.instance) {
      throw new HubError('conflict', `name ${formatAddress(me)} is taken`)
    }
    old?.close('replaced')
    const meta = {
      host: q.host,
      cwd: q.cwd,
      client: { name: q.client_name, version: q.client_version },
    }
    const conn = new Connection(sid, me, q.instance, meta, now(), old !== undefined)
    this.conns.set(key, conn)
    return { conn, startSeq }
  }

  /** Deliver events to one connection until it closes. */
  async run({ conn, startSeq }: Joined, sink: Sink): Promise<void> {
    conn.sink = sink
    const { sid, me } = conn
    const ping = setInterval(
      () => void sink.ping().catch(() => conn.close('disconnected')),
      PING_MS,
    )
    try {
      if (!conn.resumed) {
        await this.publishEvt(conn, {
          kind: 'joined',
          host: conn.meta.host,
          cwd: conn.meta.cwd,
          client: conn.meta.client,
          at: conn.joinedAt,
        })
      }
      await this.putPresence(conn)
      await sink.write('joined', { me: formatAddress(me), session: sid })
      const wasSent = (id: string) => conn.sent.has(id)
      for await (const e of follow(this.b.js, deliverySubjects(sid), startSeq, conn.ctl.signal)) {
        const d = deliveryFor(e, me, wasSent)
        if (d === 'message' && e.kind === 'msg') {
          const m = toApiMessage(e)
          conn.sent.add(m.id)
          await sink.write('message', m, m.id)
        } else if (d === 'notice' && e.kind === 'kick') {
          await this.notice(conn, { kind: 'kicked', at: e.at }, String(e.seq))
          conn.close('kicked')
        } else if (d === 'notice' && e.kind === 'redact') {
          await this.notice(conn, { kind: 'redacted', id: e.id, at: e.at }, String(e.seq))
        }
      }
    } catch {
      conn.close('disconnected')
    } finally {
      clearInterval(ping)
      await this.leave(conn)
      sink.close()
    }
  }

  async send(machine: string, sid: string, req: SendRequest): Promise<SendResponse> {
    if (!this.limits.msg.take(machine)) throw new HubError('rate_limited', 'too many messages')
    const conn = this.requireConn(machine, sid, req.agent)
    await this.requireOpen(sid)
    const to = this.recipient(sid, req.to, conn.me)
    const sent_at = now()
    const base = { kind: 'msg', sid, from: conn.me, to, text: req.text, sent_at } as const
    const seq = await publishEvent(
      this.b.js,
      req.reply_to === undefined ? base : { ...base, reply_to: req.reply_to },
    )
    return { id: String(seq), to: typeof to === 'string' ? to : formatAddress(to), sent_at }
  }

  async activity(machine: string, sid: string, req: ActivityRequest): Promise<void> {
    if (!this.limits.activity.take(machine)) throw new HubError('rate_limited', 'too many updates')
    const conn = this.requireConn(machine, sid, req.agent)
    const at = now()
    switch (req.kind) {
      case 'state':
        conn.state = req.state
        conn.note = req.note
        await this.publishEvt(conn, withNote({ kind: 'state', state: req.state, at }, req.note))
        break
      case 'delivered':
        if (conn.sent.has(req.id)) conn.acked.add(req.id)
        await this.publishEvt(conn, { kind: 'delivered', id: req.id, via: req.via, at })
        break
      case 'wait_start': {
        const on = req.from === undefined ? undefined : this.peer(sid, req.from, conn.me, true)
        const w: { on?: Address; reply_to?: string; since: string } = { since: at }
        const evt: Parameters<Hub['publishEvt']>[1] & { kind: 'wait_start' } = {
          kind: 'wait_start',
          timeout_s: req.timeout_s,
          at,
        }
        if (on !== undefined) {
          w.on = on
          evt.from = formatAddress(on)
        }
        if (req.reply_to !== undefined) {
          w.reply_to = req.reply_to
          evt.reply_to = req.reply_to
        }
        conn.waiting = w
        await this.publishEvt(conn, evt)
        break
      }
      case 'wait_end':
        conn.waiting = undefined
        await this.publishEvt(conn, { kind: 'wait_end', result: req.result, at })
        break
    }
    await this.putPresence(conn)
  }

  async view(machine: string, sid: string, agent: string): Promise<SessionView> {
    const conn = this.requireConn(machine, sid, agent)
    const r = await this.getSession(sid)
    if (r === undefined) throw new HubError('not_found', `no session ${sid}`)
    const peers = this.inSession(sid)
      .filter((c) => c !== conn)
      .map((c) => {
        const p: SessionView['peers'][number] = {
          name: formatAddress(c.me),
          state: c.state,
          online: true,
        }
        if (c.note !== undefined) p.note = c.note
        if (c.waiting?.on !== undefined) p.waiting_on = formatAddress(c.waiting.on)
        return p
      })
    return { session: sid, status: r.status, me: formatAddress(conn.me), peers }
  }

  async history(
    machine: string,
    sid: string,
    q: { agent: string; with?: string | undefined; limit: number },
  ): Promise<HistoryResponse> {
    const conn = this.requireConn(machine, sid, q.agent)
    const peer = q.with === undefined ? undefined : this.peer(sid, q.with, conn.me, true)
    const events = await readRange(this.b.js, this.b.jsm, deliverySubjects(sid))
    const me = conn.me
    const msgs: ApiMessage[] = []
    for (const e of events) {
      if (e.kind !== 'msg' || !isVisible(e, me)) continue
      if (peer !== undefined && !between(e, me, peer)) continue
      msgs.push(toApiMessage(e))
    }
    return { messages: msgs.slice(-q.limit) }
  }

  // --- internals -------------------------------------------------------------------------

  private requireConn(machine: string, sid: string, agent: string): Connection {
    const c = this.conns.get(buildPresenceKey({ sid, agent: { agent, machine } }))
    if (c === undefined) throw new HubError('forbidden', 'not in a session')
    return c
  }

  private async getSession(sid: string): Promise<SessionRecord | undefined> {
    const e = await this.b.sessions.get(buildSessionsKey({ kind: 'session', sid }))
    return e?.operation === 'PUT' ? decode(SessionRecord, e.value) : undefined
  }

  private async requireOpen(sid: string, create = false): Promise<void> {
    let r = await this.getSession(sid)
    if (r === undefined && create) {
      // Two first joins can race; the loser's create fails and the re-read sees the winner's.
      await this.op.createSession(sid).catch(() => undefined)
      r = await this.getSession(sid)
    }
    if (r === undefined) throw new HubError('not_found', `no session ${sid}`)
    if (r.status !== 'open') throw new HubError('forbidden', 'session closed')
  }

  private inSession(sid: string): Connection[] {
    return [...this.conns.values()].filter((c) => c.sid === sid)
  }

  /** A `send` target: `all`, `operator` (the user), or a peer that is in the session now. */
  private recipient(sid: string, input: string, me: Address): To {
    if (input === BROADCAST || input === OPERATOR) return input
    return this.peer(sid, input, me, false)
  }

  /**
   * Turn a peer as a client writes it (`name` or `name@machine`) into an address. A bare name
   * must match exactly one agent that is in the session now.
   */
  private peer(sid: string, input: string, me: Address, offline: boolean): Address {
    const peers = this.inSession(sid).map((c) => c.me)
    const list = () =>
      peers
        .filter((p) => !sameAddress(p, me))
        .map(formatAddress)
        .join(', ')
    // The request schema already checked the format (PEER_RE, RECIPIENT_RE).
    const full = input.includes('@') ? parseAddress(input) : undefined
    const matches =
      full !== undefined ? [full] : peers.filter((p) => p.agent === input && !sameAddress(p, me))
    const target = matches[0]
    // Yourself and an ambiguous name conflict with the session state (409), not with the format.
    if (target === undefined ? input === me.agent : sameAddress(target, me)) {
      throw new HubError('conflict', 'cannot address yourself')
    }
    if (matches.length > 1) {
      throw new HubError('ambiguous', `"${input}" matches ${matches.map(formatAddress).join(', ')}`)
    }
    const online = target !== undefined && peers.some((p) => sameAddress(p, target))
    if (target === undefined || (!online && !offline)) {
      throw new HubError(
        'not_found',
        `no peer ${input} in this session; peers: ${list() || 'none'}`,
      )
    }
    return target
  }

  private async notice(conn: Connection, n: NoticeEvent, id?: string): Promise<void> {
    await conn.sink?.write('notice', n, id)
  }

  private async publishEvt(
    conn: Connection,
    evt: Extract<BusEvent, { kind: 'evt' }>['evt'],
  ): Promise<void> {
    await publishEvent(this.b.js, { kind: 'evt', sid: conn.sid, from: conn.me, evt })
  }

  private async putPresence(conn: Connection): Promise<void> {
    const r: PresenceRecord = {
      host: conn.meta.host,
      cwd: conn.meta.cwd,
      client: conn.meta.client,
      state: conn.state,
      joined_at: conn.joinedAt,
      queued: conn.queued,
    }
    if (conn.note !== undefined) r.note = conn.note
    if (conn.waiting !== undefined) {
      const w: NonNullable<PresenceRecord['waiting']> = { since: conn.waiting.since }
      if (conn.waiting.on !== undefined) w.on = formatAddress(conn.waiting.on)
      if (conn.waiting.reply_to !== undefined) w.reply_to = conn.waiting.reply_to
      r.waiting = w
    }
    await this.b.presence.put(conn.key, encode(PresenceRecord, r))
  }

  private async leave(conn: Connection): Promise<void> {
    if (this.conns.get(conn.key) !== conn) return
    this.conns.delete(conn.key)
    const reason = conn.closeReason
    if (reason === 'replaced') return
    await Promise.allSettled([
      this.publishEvt(conn, { kind: 'left', reason, at: now() }),
      this.b.presence.delete(conn.key),
    ])
  }

  private async heartbeat(): Promise<void> {
    await Promise.allSettled([...this.conns.values()].map((c) => this.putPresence(c)))
  }

  private async onSessions(
    w: AsyncIterable<{ key: string; operation: string; value: Uint8Array }>,
  ) {
    for await (const e of w) {
      const k = parseSessionsKey(e.key)
      if (k?.kind !== 'session') continue
      const r = e.operation === 'PUT' ? decode(SessionRecord, e.value) : undefined
      const before = this.status.get(k.sid)
      if (r === undefined) {
        this.status.delete(k.sid)
        for (const c of this.inSession(k.sid)) c.close('closed')
        continue
      }
      this.status.set(k.sid, r.status)
      if (before === undefined || before === r.status) continue
      const kind = r.status === 'closed' ? 'closed' : 'reopened'
      for (const c of this.inSession(k.sid)) await this.notice(c, { kind, at: now() })
    }
  }

  private async onTokens(w: AsyncIterable<{ key: string; operation: string; value: Uint8Array }>) {
    for await (const e of w) {
      const r = e.operation === 'PUT' ? decode(TokenRecord, e.value) : undefined
      if (r !== undefined && r.revoked_at === undefined) continue
      for (const c of this.conns.values()) if (c.me.machine === e.key) c.close('revoked')
    }
  }
}

function withNote<T extends object>(o: T, note: string | undefined): T & { note?: string } {
  return note === undefined ? o : { ...o, note }
}

/** A message between me and one peer: from the peer to me or to all, or from me to the peer. */
function between(e: Extract<BusEvent, { kind: 'msg' }>, me: Address, peer: Address): boolean {
  const is = (x: unknown, a: Address) => typeof x === 'object' && sameAddress(x as Address, a)
  return (
    (is(e.from, peer) && (e.to === BROADCAST || is(e.to, me))) || (is(e.from, me) && is(e.to, peer))
  )
}
