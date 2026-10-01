// Operator actions. The TUI calls these; tests call them directly. Agents cannot do any of them:
// they reach the broker only through the hub, which has no route for these actions.
import {
  type Address,
  buildPresenceKey,
  buildSessionsKey,
  OPERATOR,
  parseSessionsKey,
  parseSubject,
  STREAM_NAME,
  sessionSubjects,
} from '../names.js'
import {
  type BusEventInput,
  decode,
  encode,
  KickRecord,
  SessionRecord,
  type To,
} from '../schema.js'
import type { Broker } from './connect.js'
import { publishEvent } from './read.js'

export class OperatorError extends Error {}

const now = () => new Date().toISOString()

export class Operator {
  constructor(private readonly b: Broker) {}

  async getSession(sid: string): Promise<SessionRecord | undefined> {
    const e = await this.b.sessions.get(buildSessionsKey({ kind: 'session', sid }))
    return e?.operation === 'PUT' ? decode(SessionRecord, e.value) : undefined
  }

  /** Every session with its record, sorted by id. */
  async listSessions(): Promise<{ sid: string; record: SessionRecord }[]> {
    const out: { sid: string; record: SessionRecord }[] = []
    for await (const key of await this.b.sessions.keys()) {
      const k = parseSessionsKey(key)
      if (k?.kind !== 'session') continue
      const record = await this.getSession(k.sid)
      if (record !== undefined) out.push({ sid: k.sid, record })
    }
    return out.sort((a, b) => a.sid.localeCompare(b.sid))
  }

  /** Create an open session. Fails if the session exists. */
  async createSession(sid: string, title?: string): Promise<void> {
    const r: SessionRecord = { status: 'open', created_at: now() }
    if (title !== undefined) r.title = title
    try {
      await this.b.sessions.create(sid, encode(SessionRecord, r))
    } catch (err) {
      throw new OperatorError(`session ${sid} exists`, { cause: err })
    }
  }

  async closeSession(sid: string): Promise<void> {
    await this.setStatus(sid, 'closed')
  }

  async reopenSession(sid: string): Promise<void> {
    await this.setStatus(sid, 'open')
  }

  private async setStatus(sid: string, status: 'open' | 'closed'): Promise<void> {
    const r = await this.getSession(sid)
    if (r === undefined) throw new OperatorError(`no session ${sid}`)
    const next: SessionRecord = { ...r, status }
    if (status === 'closed') next.closed_at = now()
    else delete next.closed_at
    await this.b.sessions.put(sid, encode(SessionRecord, next))
  }

  /** Delete a closed session: its log, kicks and presence. Irreversible. */
  async deleteSession(sid: string): Promise<void> {
    const r = await this.getSession(sid)
    if (r === undefined) throw new OperatorError(`no session ${sid}`)
    if (r.status !== 'closed') throw new OperatorError(`session ${sid} is open; close it first`)
    await this.b.jsm.streams.purge(STREAM_NAME, { filter: sessionSubjects(sid) })
    for (const kv of [this.b.sessions, this.b.presence]) {
      const keys = await kv.keys(`${sid}.>`)
      for await (const k of keys) await kv.purge(k)
    }
    await this.b.sessions.purge(sid)
  }

  /** Remove an agent from a session. The hub closes its stream and refuses it from now on. */
  async kick(sid: string, target: Address): Promise<number> {
    const at = now()
    await this.b.sessions.put(
      buildSessionsKey({ kind: 'kick', sid, target }),
      encode(KickRecord, { at }),
    )
    return publishEvent(this.b.js, { kind: 'kick', sid, target, at })
  }

  /** Allow a kicked agent to join again. */
  async unkick(sid: string, target: Address): Promise<void> {
    await this.b.sessions.purge(buildSessionsKey({ kind: 'kick', sid, target }))
  }

  /**
   * Erase one message of a session from the stream, then record the redact so that the hub can
   * tell agents that already got it. Returns false if `id` is not a message of this session.
   */
  async redact(sid: string, id: string): Promise<boolean> {
    const seq = Number(id)
    let subject: string | undefined
    try {
      subject = (await this.b.jsm.streams.getMessage(STREAM_NAME, { seq }))?.subject
    } catch {
      return false
    }
    if (subject === undefined) return false
    const s = parseSubject(subject)
    if (s === undefined || s.sid !== sid || s.kind === 'evt') return false
    await this.b.jsm.streams.deleteMessage(STREAM_NAME, seq, true)
    await publishEvent(this.b.js, { kind: 'redact', sid, id, at: now() })
    return true
  }

  /** Send a message as the operator. Returns its id. */
  async send(sid: string, to: To, text: string, reply_to?: string): Promise<string> {
    const base = { kind: 'msg', sid, from: OPERATOR, to, text, sent_at: now() } as const
    const e: BusEventInput = reply_to === undefined ? base : { ...base, reply_to }
    return String(await publishEvent(this.b.js, e))
  }

  /** Current presence entries of a session, for tests and one-shot views. */
  async presenceKeys(sid: string): Promise<string[]> {
    const out: string[] = []
    for await (const k of await this.b.presence.keys(`${sid}.>`)) out.push(k)
    return out
  }

  presenceKey(sid: string, agent: Address): string {
    return buildPresenceKey({ sid, agent })
  }
}
