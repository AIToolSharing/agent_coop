// HTTP layer of the hub. Routes are declared with zod schemas from @coop/core, so the same
// schemas validate requests and generate /openapi.json.
import {
  ActivityRequest,
  ErrorBody,
  HistoryQuery,
  HistoryResponse,
  SendRequest,
  SendResponse,
  SessionView,
  StreamQuery,
  Token,
} from '@coop/core'
import { createRoute, OpenAPIHono, z } from '@hono/zod-openapi'
import { bodyLimit } from 'hono/body-limit'
import { streamSSE } from 'hono/streaming'
import { HubError } from './errors.js'
import type { Hub, Sink } from './hub.js'

type Env = { Variables: { machine: string } }

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

  app.doc('/openapi.json', {
    openapi: '3.0.3',
    info: { title: 'coop hub', version: '1' },
  })

  return app
}
