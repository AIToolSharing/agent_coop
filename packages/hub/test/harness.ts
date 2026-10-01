// A real hub on a free port, with a real nats-server, and small HTTP and SSE clients.
import { randomUUID } from 'node:crypto'
import type { AddressInfo } from 'node:net'
import { type Broker, Operator } from '@coop/core/broker'
import { serve } from '@hono/node-server'
import { createParser, type EventSourceMessage } from 'eventsource-parser'
import { startNats, type TestNats } from '../../core/test/helpers/nats.js'
import { createApp, Hub, type HubOptions, issueToken, type Limit } from '../src/index.js'

export interface Harness {
  readonly base: string
  readonly nats: TestNats
  readonly hubBroker: Broker
  readonly op: Operator
  readonly hub: Hub
  token(machine: string): Promise<string>
  /** Stop the hub and start a new one on the same port and broker, as a restart would. */
  restartHub(): Promise<void>
  stop(): Promise<void>
}

export async function startHub(
  limits: Partial<Record<'join' | 'msg' | 'activity', Limit>> = {},
  options: HubOptions = {},
): Promise<Harness> {
  const nats = await startNats()
  const hubBroker = await nats.broker('hub')
  const op = new Operator(await nats.broker('operator'))
  let hub = new Hub(hubBroker, limits, options)
  await hub.start()
  let server = serve({ fetch: createApp(hub).fetch, hostname: '127.0.0.1', port: 0 })
  await new Promise((r) => server.once('listening', r))
  const { port } = server.address() as AddressInfo
  const shutdown = async () => {
    await hub.stop()
    if ('closeAllConnections' in server) server.closeAllConnections()
    await new Promise((r) => server.close(r))
  }
  return {
    base: `http://127.0.0.1:${port}`,
    nats,
    hubBroker,
    op,
    get hub() {
      return hub
    },
    token: (machine) => issueToken(hubBroker.tokens, machine),
    async restartHub() {
      await shutdown()
      hub = new Hub(hubBroker, limits, options)
      await hub.start()
      server = serve({ fetch: createApp(hub).fetch, hostname: '127.0.0.1', port })
      await new Promise((r) => server.once('listening', r))
    },
    async stop() {
      await shutdown()
      await nats.stop()
    },
  }
}

export class Api {
  constructor(
    readonly base: string,
    readonly token: string,
  ) {}

  async req(
    method: string,
    path: string,
    body?: unknown,
  ): Promise<{ status: number; json: unknown }> {
    const res = await fetch(this.base + path, {
      method,
      headers: {
        authorization: `Bearer ${this.token}`,
        ...(body === undefined ? {} : { 'content-type': 'application/json' }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    })
    const text = await res.text()
    return { status: res.status, json: text === '' ? undefined : JSON.parse(text) }
  }

  send(sid: string, agent: string, to: string, text: string, reply_to?: string) {
    return this.req('POST', `/v1/sessions/${sid}/messages`, {
      agent,
      to,
      text,
      ...(reply_to === undefined ? {} : { reply_to }),
    })
  }

  activity(sid: string, body: object) {
    return this.req('POST', `/v1/sessions/${sid}/activity`, body)
  }

  view(sid: string, agent: string) {
    return this.req('GET', `/v1/sessions/${sid}?agent=${agent}`)
  }

  history(sid: string, agent: string, extra = '') {
    return this.req('GET', `/v1/sessions/${sid}/messages?agent=${agent}${extra}`)
  }

  /** Open a stream. `instance` defaults to a fresh id (a new process). */
  async stream(
    sid: string,
    agent: string,
    opts: { instance?: string; lastEventId?: string } = {},
  ): Promise<Stream> {
    const instance = opts.instance ?? randomUUID()
    const q = new URLSearchParams({
      agent,
      instance,
      host: 'test-host',
      cwd: '/work',
      client_name: 'test',
      client_version: '0',
    })
    const ctl = new AbortController()
    const res = await fetch(`${this.base}/v1/sessions/${sid}/stream?${q}`, {
      headers: {
        authorization: `Bearer ${this.token}`,
        ...(opts.lastEventId === undefined ? {} : { 'last-event-id': opts.lastEventId }),
      },
      signal: ctl.signal,
    })
    return new Stream(res, ctl, instance)
  }
}

/** One SSE connection. Events are buffered; `next` waits for one that matches. */
export class Stream {
  readonly events: EventSourceMessage[] = []
  private readonly waiters = new Set<() => void>()
  closed = false
  body: unknown

  constructor(
    readonly res: Response,
    private readonly ctl: AbortController,
    readonly instance: string,
  ) {
    if (!res.ok || res.body === null) {
      this.closed = true
      // A refused stream has a JSON body; the test may close the stream before it is read.
      void res
        .text()
        .then((t) => {
          this.body = t === '' ? undefined : JSON.parse(t)
        })
        .catch(() => undefined)
      return
    }
    void this.pump(res.body)
  }

  get status() {
    return this.res.status
  }

  private async pump(body: ReadableStream<Uint8Array>) {
    const parser = createParser({
      onEvent: (e) => {
        this.events.push(e)
        for (const w of this.waiters) w()
      },
    })
    const decoder = new TextDecoder()
    try {
      for await (const chunk of body) parser.feed(decoder.decode(chunk, { stream: true }))
    } catch {
      // Aborted by the test or closed by the server.
    } finally {
      this.closed = true
      for (const w of this.waiters) w()
    }
  }

  /** Wait for the first buffered or future event that matches, and remove it from the buffer. */
  async next(
    pred: (e: EventSourceMessage) => boolean = () => true,
    ms = 5000,
  ): Promise<EventSourceMessage> {
    const end = Date.now() + ms
    for (;;) {
      const i = this.events.findIndex(pred)
      if (i >= 0) return this.events.splice(i, 1)[0] as EventSourceMessage
      if (this.closed) throw new Error('stream closed before the event came')
      const left = end - Date.now()
      if (left <= 0) throw new Error('timed out waiting for event')
      await new Promise<void>((resolve) => {
        const w = () => {
          this.waiters.delete(w)
          resolve()
        }
        this.waiters.add(w)
        setTimeout(w, left)
      })
    }
  }

  /** Wait until the server closes the stream. */
  async ended(ms = 5000): Promise<void> {
    const end = Date.now() + ms
    while (!this.closed) {
      if (Date.now() > end) throw new Error('stream did not end')
      await new Promise((r) => setTimeout(r, 20))
    }
  }

  close() {
    this.ctl.abort()
  }
}

export const isMsg = (e: EventSourceMessage) => e.event === 'message'
export const data = (e: EventSourceMessage) => JSON.parse(e.data)
