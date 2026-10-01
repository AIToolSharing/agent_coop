// The hub's admin API as the TUI uses it: the live feed with reconnect and resume, and the
// operator's actions. Both need an operator token.
import {
  type Address,
  AdminEvent,
  abortableSleep,
  ErrorBody,
  formatAddress,
  type OperatorSendResponse,
  OperatorSendResponse as OperatorSendResponseSchema,
  readSse,
  type SessionInfo,
  SessionInfo as SessionInfoSchema,
  type To,
} from '@coop/core'
import type { OperatorApi } from './app.js'

/** The service is reachable but refuses this token; retrying cannot help. */
export class RefusedError extends Error {}

/** What the feed yields: the hub's events, and the connection's own lifecycle. */
export type FeedItem =
  | AdminEvent
  | { readonly kind: 'connected' }
  | { readonly kind: 'disconnected' }

const IDLE_MS = 45_000
const MAX_BACKOFF_MS = 30_000

export class AdminClient implements OperatorApi {
  constructor(
    private readonly base: string,
    private readonly token: string,
    private readonly idleMs: number = IDLE_MS,
    private readonly sleep: (ms: number, signal: AbortSignal) => Promise<void> = abortableSleep,
  ) {}

  /**
   * The feed, from the start of the stream, until `signal` aborts. A dropped connection is
   * opened again from the last event id; `connected` and `disconnected` mark the boundaries.
   * A refused token ends the feed with a RefusedError.
   */
  async *feed(signal: AbortSignal): AsyncGenerator<FeedItem> {
    let lastId: string | undefined
    let backoff = 1000
    while (!signal.aborted) {
      const idle = new AbortController()
      let res: Response
      try {
        res = await fetch(`${this.base}/v1/admin/stream`, {
          headers: {
            authorization: `Bearer ${this.token}`,
            accept: 'text/event-stream',
            ...(lastId === undefined ? {} : { 'last-event-id': lastId }),
          },
          signal: AbortSignal.any([signal, idle.signal]),
        })
      } catch {
        if (signal.aborted) return
        await this.sleep(backoff, signal)
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
        continue
      }
      if (res.status === 401 || res.status === 403) {
        const e = ErrorBody.safeParse(await res.json().catch(() => undefined))
        throw new RefusedError(e.success ? e.data.message : `the service answered ${res.status}`)
      }
      if (!res.ok || res.body === null) {
        await this.sleep(backoff, signal)
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
        continue
      }
      backoff = 1000
      yield { kind: 'connected' }
      try {
        for await (const ev of readSse(res.body, this.idleMs, idle)) {
          const parsed = AdminEvent.safeParse(safeJson(ev.data))
          if (!parsed.success) continue
          if (parsed.data.kind === 'event') lastId = String(parsed.data.seq)
          yield parsed.data
        }
      } catch {
        // The connection dropped, or the watchdog ended it; reconnect below.
      }
      if (signal.aborted) return
      yield { kind: 'disconnected' }
      await this.sleep(1000, signal)
    }
  }

  async listSessions(): Promise<SessionInfo[]> {
    const r = await this.call('GET', '/sessions')
    return SessionInfoSchema.array().parse(((await r.json()) as { sessions: unknown }).sessions)
  }

  async createSession(sid: string): Promise<void> {
    await this.call('POST', '/sessions', { session: sid })
  }

  async closeSession(sid: string): Promise<void> {
    await this.call('POST', `/sessions/${encodeURIComponent(sid)}/close`)
  }

  async reopenSession(sid: string): Promise<void> {
    await this.call('POST', `/sessions/${encodeURIComponent(sid)}/reopen`)
  }

  async deleteSession(sid: string): Promise<void> {
    await this.call('DELETE', `/sessions/${encodeURIComponent(sid)}`)
  }

  async kick(sid: string, target: Address): Promise<void> {
    await this.call('POST', `/sessions/${encodeURIComponent(sid)}/kick`, {
      target: formatAddress(target),
    })
  }

  async unkick(sid: string, target: Address): Promise<void> {
    await this.call('POST', `/sessions/${encodeURIComponent(sid)}/unkick`, {
      target: formatAddress(target),
    })
  }

  /** False if `id` is not a message of this session. */
  async redact(sid: string, id: string): Promise<boolean> {
    try {
      await this.call('POST', `/sessions/${encodeURIComponent(sid)}/redact`, { id })
      return true
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) return false
      throw err
    }
  }

  async send(sid: string, to: To, text: string, reply_to?: string): Promise<string> {
    if (to === 'operator') throw new Error('the operator cannot write to the operator')
    const r = await this.call('POST', `/sessions/${encodeURIComponent(sid)}/messages`, {
      to: typeof to === 'string' ? to : formatAddress(to),
      text,
      ...(reply_to === undefined ? {} : { reply_to }),
    })
    const sent: OperatorSendResponse = OperatorSendResponseSchema.parse(await r.json())
    return sent.id
  }

  private async call(method: string, path: string, body?: unknown): Promise<Response> {
    const res = await fetch(`${this.base}/v1/admin${path}`, {
      method,
      headers: {
        authorization: `Bearer ${this.token}`,
        ...(body === undefined ? {} : { 'content-type': 'application/json' }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
    if (!res.ok) {
      const e = ErrorBody.safeParse(await res.json().catch(() => undefined))
      throw new ApiError(
        res.status,
        e.success ? e.data.message : `the service answered ${res.status}`,
      )
    }
    return res
  }
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
  }
}

function safeJson(s: string): unknown {
  try {
    return JSON.parse(s)
  } catch {
    return undefined
  }
}
