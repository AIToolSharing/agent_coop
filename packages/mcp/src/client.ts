// Client of the hub API: REST calls and the event stream with reconnect and resume.
// Errors become CoopError with a message that an agent may read; it never names the service.
import {
  type ActivityRequest,
  ApiMessage,
  ErrorBody,
  HistoryResponse,
  JoinedEvent,
  NoticeEvent,
  SendResponse,
  SessionView,
} from '@coop/core'
import { createParser } from 'eventsource-parser'
import type { z } from 'zod'

export const UNREACHABLE = 'message service unreachable (retrying)'

/** An error whose message is fit to show an agent. */
export class CoopError extends Error {}

/** Where the stream stands, for the status tool and for tool errors. */
export type LinkState =
  | { readonly kind: 'joining' }
  | { readonly kind: 'joined'; readonly me: string }
  | { readonly kind: 'no_session' }
  | { readonly kind: 'closed' }
  | { readonly kind: 'removed' }
  | { readonly kind: 'refused' }
  | { readonly kind: 'unreachable' }

export interface StreamHandlers {
  state(s: LinkState): void
  message(m: ApiMessage): void
  notice(n: NoticeEvent): void
}

export interface JoinInfo {
  readonly agent: string
  readonly instance: string
  readonly host: string
  readonly cwd: string
  readonly clientName: string
  readonly clientVersion: string
}

const RETRY_CLOSED_MS = 30_000
const MAX_BACKOFF_MS = 30_000
/** The service pings every 15 s. A stream silent for this long is dead; open a new one. */
const IDLE_MS = 45_000

export class HubClient {
  /** The agent name the stream holds; it can get a suffix when the name is taken. */
  agent: string

  constructor(
    private readonly base: string,
    private readonly token: string,
    readonly session: string,
    private readonly join: JoinInfo,
    private readonly sleep: (ms: number, signal: AbortSignal) => Promise<void> = abortableSleep,
    private readonly idleMs: number = IDLE_MS,
  ) {
    this.agent = join.agent
  }

  /** Hold the stream open until `signal` aborts or the agent is removed. */
  async run(h: StreamHandlers, signal: AbortSignal): Promise<void> {
    let lastId: string | undefined
    let backoff = 1000
    let suffix = 1
    while (!signal.aborted) {
      const q = new URLSearchParams({
        agent: this.agent,
        instance: this.join.instance,
        host: this.join.host,
        cwd: this.join.cwd,
        client_name: this.join.clientName,
        client_version: this.join.clientVersion,
      })
      // A watchdog: no bytes for idleMs (a lost connection shows no error) aborts the stream.
      const idle = new AbortController()
      let timer: ReturnType<typeof setTimeout> | undefined
      const arm = () => {
        clearTimeout(timer)
        timer = setTimeout(() => idle.abort(), this.idleMs)
      }
      arm()
      let res: Response
      try {
        res = await fetch(`${this.path('/stream')}?${q}`, {
          headers: {
            authorization: `Bearer ${this.token}`,
            accept: 'text/event-stream',
            ...(lastId === undefined ? {} : { 'last-event-id': lastId }),
          },
          signal: AbortSignal.any([signal, idle.signal]),
        })
      } catch {
        clearTimeout(timer)
        if (signal.aborted) return
        h.state({ kind: 'unreachable' })
        await this.sleep(backoff, signal)
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
        continue
      }

      if (!res.ok || res.body === null) {
        clearTimeout(timer)
        const e = await errorBody(res)
        if (res.status === 409 && lastId === undefined && suffix < 9) {
          suffix++
          this.agent = `${this.join.agent.slice(0, 60)}-${suffix}`
          continue
        }
        if (res.status === 401) return h.state({ kind: 'refused' })
        if (res.status === 403 && e?.message === 'removed from session') {
          return h.state({ kind: 'removed' })
        }
        if (res.status === 403 || res.status === 404) {
          h.state({ kind: res.status === 404 ? 'no_session' : 'closed' })
          await this.sleep(RETRY_CLOSED_MS, signal)
          continue
        }
        h.state({ kind: 'unreachable' })
        await this.sleep(backoff, signal)
        backoff = Math.min(backoff * 2, MAX_BACKOFF_MS)
        continue
      }

      backoff = 1000
      let removed = false
      const parser = createParser({
        onEvent: (ev) => {
          const data = safeJson(ev.data)
          if (ev.event === 'joined') {
            const j = JoinedEvent.safeParse(data)
            if (j.success) h.state({ kind: 'joined', me: j.data.me })
          } else if (ev.event === 'message') {
            const m = ApiMessage.safeParse(data)
            if (m.success) {
              lastId = m.data.id
              h.message(m.data)
            }
          } else if (ev.event === 'notice') {
            const n = NoticeEvent.safeParse(data)
            if (n.success) {
              if (ev.id !== undefined) lastId = ev.id
              if (n.data.kind === 'kicked') removed = true
              h.notice(n.data)
            }
          }
        },
      })
      const decoder = new TextDecoder()
      try {
        for await (const chunk of res.body) {
          arm()
          parser.feed(decoder.decode(chunk, { stream: true }))
        }
      } catch {
        // The connection dropped, or the watchdog ended it; reconnect below.
      } finally {
        clearTimeout(timer)
      }
      if (removed) return h.state({ kind: 'removed' })
      if (signal.aborted) return
      h.state({ kind: 'unreachable' })
      await this.sleep(1000, signal)
    }
  }

  send(to: string, text: string, reply_to?: string) {
    const body = { agent: this.agent, to, text, ...(reply_to === undefined ? {} : { reply_to }) }
    return this.call('POST', '/messages', SendResponse, body)
  }

  async activity(req: ActivityRequest): Promise<void> {
    await this.request('POST', '/activity', req)
  }

  view() {
    return this.call('GET', `?agent=${encodeURIComponent(this.agent)}`, SessionView)
  }

  history(withPeer: string | undefined, limit: number) {
    const q = new URLSearchParams({ agent: this.agent, limit: String(limit) })
    if (withPeer !== undefined) q.set('with', withPeer)
    return this.call('GET', `/messages?${q}`, HistoryResponse)
  }

  private path(p: string) {
    return `${this.base}/v1/sessions/${encodeURIComponent(this.session)}${p}`
  }

  private async call<S extends z.ZodType>(
    method: string,
    p: string,
    schema: S,
    body?: unknown,
  ): Promise<z.output<S>> {
    const res = await this.request(method, p, body)
    const r = schema.safeParse(await res.json().catch(() => undefined))
    if (!r.success) throw new CoopError(UNREACHABLE)
    return r.data
  }

  private async request(method: string, p: string, body?: unknown): Promise<Response> {
    let res: Response
    try {
      res = await fetch(this.path(p), {
        method,
        headers: {
          authorization: `Bearer ${this.token}`,
          ...(body === undefined ? {} : { 'content-type': 'application/json' }),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      })
    } catch {
      throw new CoopError(UNREACHABLE)
    }
    if (!res.ok) throw new CoopError(errorText(res.status, await errorBody(res)))
    return res
  }
}

/** The agent-facing text for an error response. The hub's messages are already agent-facing. */
export function errorText(status: number, e: ErrorBody | undefined): string {
  if (status === 401) return 'not in a session: this machine is not allowed to join'
  if (status === 429) return 'too many requests; wait a moment and try again'
  if (status >= 500 || e === undefined) return UNREACHABLE
  return e.message
}

async function errorBody(res: Response): Promise<ErrorBody | undefined> {
  const r = ErrorBody.safeParse(await res.json().catch(() => undefined))
  return r.success ? r.data : undefined
}

function safeJson(s: string): unknown {
  try {
    return JSON.parse(s)
  } catch {
    return undefined
  }
}

function abortableSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms)
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(t)
        resolve()
      },
      { once: true },
    )
  })
}
