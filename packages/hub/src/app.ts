// HTTP layer of the hub. Routes are declared with zod schemas from @coop/core, so the same
// schemas validate requests and generate /openapi.json.
import {
  ActivityRequest,
  CreateSessionRequest,
  ErrorBody,
  HistoryQuery,
  HistoryResponse,
  Id,
  OperatorSendRequest,
  OperatorSendResponse,
  parseAddress,
  RedactRequest,
  SendRequest,
  SendResponse,
  SessionInfo,
  SessionList,
  SessionView,
  StreamQuery,
  TargetRequest,
  Token,
} from '@coop/core'
import { createRoute, OpenAPIHono, z } from '@hono/zod-openapi'
import { bodyLimit } from 'hono/body-limit'
import { HTTPException } from 'hono/http-exception'
import { streamSSE } from 'hono/streaming'
import { HubError } from './errors.js'
import type { Hub, Sink } from './hub.js'

type Env = { Variables: { machine: string; operator: string } }

const Params = z.object({ sid: Token })
const AgentQuery = z.strictObject({ agent: HistoryQuery.shape.agent })

const json = <S extends z.ZodType>(schema: S, description: string) => ({
  content: { 'application/json': { schema } },
  description,
})

const errors = {
  401: json(ErrorBody, 'Missing or invalid token'),
  403: json(ErrorBody, 'Not allowed: not in a session, session closed, or removed'),
  404: json(ErrorBody, 'Unknown session or peer'),
  409: json(ErrorBody, 'Conflict with the session state: name taken, ambiguous peer, or yourself'),
  422: json(ErrorBody, 'Invalid input'),
  429: json(ErrorBody, 'Rate limit'),
} as const

const security = [{ Bearer: [] }]

const streamRoute = createRoute({
  method: 'get',
  path: '/v1/sessions/{sid}/stream',
  summary: 'Join a session and receive its events as server-sent events',
  security,
  request: { params: Params, query: StreamQuery },
  responses: {
    200: {
      content: { 'text/event-stream': { schema: z.string() } },
      description: 'Events: joined, message, notice',
    },
    ...errors,
  },
})

const sendRoute = createRoute({
  method: 'post',
  path: '/v1/sessions/{sid}/messages',
  summary: 'Send a message',
  security,
  request: {
    params: Params,
    body: { content: { 'application/json': { schema: SendRequest } }, required: true },
  },
  responses: {
    200: json(SendResponse, 'Sent'),
    413: json(ErrorBody, 'Body too large'),
    ...errors,
  },
})

const activityRoute = createRoute({
  method: 'post',
  path: '/v1/sessions/{sid}/activity',
  summary: 'Report agent activity: state, delivery, waits',
  security,
  request: {
    params: Params,
    body: { content: { 'application/json': { schema: ActivityRequest } }, required: true },
  },
  responses: {
    204: { description: 'Recorded' },
    413: json(ErrorBody, 'Body too large'),
    ...errors,
  },
})

const viewRoute = createRoute({
  method: 'get',
  path: '/v1/sessions/{sid}',
  summary: 'Session status and peers',
  security,
  request: { params: Params, query: AgentQuery },
  responses: { 200: json(SessionView, 'Session'), ...errors },
})

const historyRoute = createRoute({
  method: 'get',
  path: '/v1/sessions/{sid}/messages',
  summary: 'Messages visible to the agent, oldest first',
  security,
  request: { params: Params, query: HistoryQuery },
  responses: { 200: json(HistoryResponse, 'Messages'), ...errors },
})

const body = <S extends z.ZodType>(schema: S) => ({
  content: { 'application/json': { schema } },
  required: true as const,
})
const done = { 204: { description: 'Done' } }

// --- the admin API: the operator token (the TUI) -------------------------------------------

const adminSessionsRoute = createRoute({
  method: 'get',
  path: '/v1/admin/sessions',
  summary: 'List every session with its state',
  security,
  responses: { 200: json(SessionList, 'Sessions'), ...errors },
})

const adminCreateRoute = createRoute({
  method: 'post',
  path: '/v1/admin/sessions',
  summary: 'Create an open session',
  security,
  request: { body: body(CreateSessionRequest) },
  responses: { 200: json(SessionInfo, 'Created'), ...errors },
})

const adminCloseRoute = createRoute({
  method: 'post',
  path: '/v1/admin/sessions/{sid}/close',
  summary: 'Close a session; its agents are disconnected',
  security,
  request: { params: Params },
  responses: { ...done, ...errors },
})

const adminReopenRoute = createRoute({
  method: 'post',
  path: '/v1/admin/sessions/{sid}/reopen',
  summary: 'Open a closed session again',
  security,
  request: { params: Params },
  responses: { ...done, ...errors },
})

const adminDeleteRoute = createRoute({
  method: 'delete',
  path: '/v1/admin/sessions/{sid}',
  summary: 'Delete a closed session with all its messages',
  security,
  request: { params: Params },
  responses: { ...done, ...errors },
})

const adminKickRoute = createRoute({
  method: 'post',
  path: '/v1/admin/sessions/{sid}/kick',
  summary: 'Remove an agent from a session; it cannot join again until unkick',
  security,
  request: { params: Params, body: body(TargetRequest) },
  responses: { ...done, ...errors },
})

const adminUnkickRoute = createRoute({
  method: 'post',
  path: '/v1/admin/sessions/{sid}/unkick',
  summary: 'Let a removed agent join again',
  security,
  request: { params: Params, body: body(TargetRequest) },
  responses: { ...done, ...errors },
})

const adminRedactRoute = createRoute({
  method: 'post',
  path: '/v1/admin/sessions/{sid}/redact',
  summary: 'Withdraw a message; agents that got it are told to disregard it',
  security,
  request: { params: Params, body: body(RedactRequest) },
  responses: { ...done, ...errors },
})

const adminSendRoute = createRoute({
  method: 'post',
  path: '/v1/admin/sessions/{sid}/messages',
  summary: 'Send a message as the operator',
  security,
  request: { params: Params, body: body(OperatorSendRequest) },
  responses: { 200: json(OperatorSendResponse, 'Sent'), 413: errors[422], ...errors },
})

const adminStreamRoute = createRoute({
  method: 'get',
  path: '/v1/admin/stream',
  summary: 'Every session, agent and message, live, as server-sent events',
  security,
  responses: {
    200: {
      content: { 'text/event-stream': { schema: z.string() } },
      description: 'Events: event, session, kick, presence, snapshot (see AdminEvent)',
    },
    ...errors,
  },
})

const ROUTES = [
  streamRoute,
  sendRoute,
  activityRoute,
  viewRoute,
  historyRoute,
  adminSessionsRoute,
  adminCreateRoute,
  adminCloseRoute,
  adminReopenRoute,
  adminDeleteRoute,
  adminKickRoute,
  adminUnkickRoute,
  adminRedactRoute,
  adminSendRoute,
  adminStreamRoute,
]

export function createApp(hub: Hub) {
  const app = new OpenAPIHono<Env>({
    defaultHook: (result, c) => {
      if (!result.success) {
        const body: ErrorBody = { error: 'invalid', message: z.prettifyError(result.error) }
        return c.json(body, 422)
      }
    },
  })

  app.openAPIRegistry.registerComponent('securitySchemes', 'Bearer', {
    type: 'http',
    scheme: 'bearer',
  })

  app.onError((err, c) => {
    if (err instanceof HubError) {
      const body: ErrorBody = { error: err.code, message: err.message }
      return c.json(body, err.status)
    }
    // Hono refuses a body it cannot parse (malformed JSON) with a 400; that is bad input to us.
    if (err instanceof HTTPException && err.status < 500) {
      return c.json({ error: 'invalid', message: err.message } satisfies ErrorBody, 422)
    }
    console.error(err)
    return c.json({ error: 'unavailable', message: 'internal error' } satisfies ErrorBody, 503)
  })

  app.use(
    '/v1/*',
    bodyLimit({
      maxSize: 32 * 1024,
      onError: (c) =>
        c.json({ error: 'too_large', message: 'body too large' } satisfies ErrorBody, 413),
    }),
  )
  app.use('/v1/sessions/*', async (c, next) => {
    const who = await hub.auth(c.req.header('authorization'))
    if (who.role !== 'machine') {
      throw new HubError('forbidden', 'an operator token cannot act as an agent')
    }
    c.set('machine', who.name)
    await next()
  })
  app.use('/v1/admin/*', async (c, next) => {
    const who = await hub.auth(c.req.header('authorization'))
    if (who.role !== 'operator') {
      throw new HubError('forbidden', 'this needs an operator token')
    }
    c.set('operator', who.name)
    await next()
  })

  app.openapi(streamRoute, async (c) => {
    const { sid } = c.req.valid('param')
    const joined = await hub.join(
      c.get('machine'),
      sid,
      c.req.valid('query'),
      c.req.header('last-event-id'),
    )
    return streamSSE(c, async (stream) => {
      stream.onAbort(() => joined.conn.close('disconnected'))
      const sink: Sink = {
        write: (event, data, id) =>
          stream.writeSSE({
            event,
            data: JSON.stringify(data),
            ...(id === undefined ? {} : { id }),
          }),
        ping: () => stream.write(': ping\n\n').then(() => undefined),
        close: () => void stream.close(),
      }
      await hub.run(joined, sink)
    })
  })

  app.openapi(sendRoute, async (c) => {
    const r = await hub.send(c.get('machine'), c.req.valid('param').sid, c.req.valid('json'))
    return c.json(r, 200)
  })

  app.openapi(activityRoute, async (c) => {
    await hub.activity(c.get('machine'), c.req.valid('param').sid, c.req.valid('json'))
    return c.body(null, 204)
  })

  app.openapi(viewRoute, async (c) => {
    const v = await hub.view(c.get('machine'), c.req.valid('param').sid, c.req.valid('query').agent)
    return c.json(v, 200)
  })

  app.openapi(historyRoute, async (c) => {
    const h = await hub.history(c.get('machine'), c.req.valid('param').sid, c.req.valid('query'))
    return c.json(h, 200)
  })

  app.openapi(adminStreamRoute, async (c) => {
    const last = c.req.header('last-event-id')
    const from = last !== undefined && Id.safeParse(last).success ? Number(last) + 1 : 1
    const name = c.get('operator')
    return streamSSE(c, async (stream) => {
      const ctl = new AbortController()
      stream.onAbort(() => ctl.abort())
      const sink: Sink = {
        write: (event, data, id) =>
          stream.writeSSE({
            event,
            data: JSON.stringify(data),
            ...(id === undefined ? {} : { id }),
          }),
        ping: () => stream.write(': ping\n\n').then(() => undefined),
        close: () => void stream.close(),
      }
      await hub.adminFeed(name, sink, from, ctl)
    })
  })

  const target = (s: string) => {
    const t = parseAddress(s)
    if (t === undefined) throw new HubError('invalid', `bad target ${s}`)
    return t
  }
  app.openapi(adminSessionsRoute, async (c) => c.json({ sessions: await hub.listSessions() }, 200))
  app.openapi(adminCreateRoute, async (c) => {
    const b = c.req.valid('json')
    return c.json(await hub.createSession(b.session, b.title), 200)
  })
  app.openapi(adminCloseRoute, async (c) => {
    await hub.closeSession(c.req.valid('param').sid)
    return c.body(null, 204)
  })
  app.openapi(adminReopenRoute, async (c) => {
    await hub.reopenSession(c.req.valid('param').sid)
    return c.body(null, 204)
  })
  app.openapi(adminDeleteRoute, async (c) => {
    await hub.deleteSession(c.req.valid('param').sid)
    return c.body(null, 204)
  })
  app.openapi(adminKickRoute, async (c) => {
    await hub.kick(c.req.valid('param').sid, target(c.req.valid('json').target))
    return c.body(null, 204)
  })
  app.openapi(adminUnkickRoute, async (c) => {
    await hub.unkick(c.req.valid('param').sid, target(c.req.valid('json').target))
    return c.body(null, 204)
  })
  app.openapi(adminRedactRoute, async (c) => {
    await hub.redact(c.req.valid('param').sid, c.req.valid('json').id)
    return c.body(null, 204)
  })
  app.openapi(adminSendRoute, async (c) => {
    const r = await hub.operatorSend(c.req.valid('param').sid, c.req.valid('json'))
    return c.json(r, 200)
  })

  // A known path with a method that no route lists is 405 with `Allow`, not 404. Registered
  // after the routes, so a listed method matches its route first.
  const allowed = new Map<string, string[]>()
  for (const r of ROUTES) {
    const path = r.path.replace(/\{(\w+)\}/g, ':$1')
    allowed.set(path, [...(allowed.get(path) ?? []), r.method.toUpperCase()])
  }
  for (const [path, methods] of allowed) {
    app.all(path, (c) => {
      c.header('Allow', methods.join(', '))
      return c.json({ error: 'invalid', message: 'method not allowed' } satisfies ErrorBody, 405)
    })
  }

  app.doc('/openapi.json', {
    openapi: '3.0.3',
    info: { title: 'coop hub', version: '1' },
  })

  return app
}
