// Real shims (MCP servers) against a real hub and nats-server. The test acts as the MCP client
// (Claude Code) through an in-memory transport and records every text an agent could see.
import { sessionSubjects } from '@coop/core'
import { readRange } from '@coop/core/broker'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js'
import { afterAll, beforeAll, describe, expect, test } from 'vitest'
import { type Harness, startHub } from '../../hub/test/harness.js'
import type { ShimConfig } from '../src/config.js'
import { FORBIDDEN_RE, INSTRUCTIONS, Shim } from '../src/server.js'

let h: Harness
const tokens: Record<string, string> = {}
/** Every text that reached a client: tool lists, results, notifications. */
const seen: string[] = []
const shims: Shim[] = []

beforeAll(async () => {
  const high = { burst: 10_000, perSecond: 10_000 }
  h = await startHub({ join: high, msg: high, activity: high })
  for (const m of ['mac-1', 'vps-2', 'mac-3']) tokens[m] = await h.token(m)
})
afterAll(async () => {
  await Promise.allSettled(shims.map((s) => s.stop()))
  await h?.stop()
})

interface Agent {
  readonly client: Client
  readonly shim: Shim
  readonly pushed: { content: string; meta: Record<string, string> }[]
  tool(name: string, args?: Record<string, unknown>): Promise<{ text: string; isError: boolean }>
  json(name: string, args?: Record<string, unknown>): Promise<Record<string, unknown>>
}

async function agent(
  machine: string,
  cfg: Partial<ShimConfig> & { session?: string | undefined },
): Promise<Agent> {
  const config: ShimConfig = {
    session: undefined,
    agent: 'agent',
    push: false,
    url: h.base,
    token: tokens[machine],
    ...cfg,
  }
  const shim = new Shim({ config, host: `host-${machine}`, cwd: '/work' })
  shims.push(shim)
  const [a, b] = InMemoryTransport.createLinkedPair()
  const client = new Client({ name: 'test-client', version: '1.0' })
  const pushed: Agent['pushed'] = []
  client.fallbackNotificationHandler = async (n) => {
    if (n.method === 'notifications/claude/channel') {
      const p = n.params as { content: string; meta: Record<string, string> }
      pushed.push(p)
      seen.push(JSON.stringify(p))
    }
  }
  await shim.server.connect(a)
  await client.connect(b)
  const tool: Agent['tool'] = async (name, args = {}) => {
    const r = await client.callTool({ name, arguments: args })
    const content = r.content as { type: string; text: string }[]
    const text = content.map((c) => c.text).join('\n')
    seen.push(text)
    return { text, isError: r.isError === true }
  }
  return {
    client,
    shim,
    pushed,
    tool,
    async json(name, args) {
      const r = await tool(name, args)
      if (r.isError) throw new Error(`${name} failed: ${r.text}`)
      return JSON.parse(r.text)
    },
  }
}

async function joined(a: Agent): Promise<Record<string, unknown>> {
  for (let i = 0; i < 100; i++) {
    const s = await a.json('status')
    if (s.joined === true) return s
    await new Promise((r) => setTimeout(r, 50))
  }
  throw new Error('agent did not join')
}

async function until<T>(f: () => T | undefined, ms = 5000): Promise<T> {
  const end = Date.now() + ms
  for (;;) {
    const v = f()
    if (v !== undefined) return v
    if (Date.now() > end) throw new Error('timed out')
    await new Promise((r) => setTimeout(r, 10))
  }
}

async function eventually(f: () => Promise<boolean>, ms = 5000): Promise<void> {
  const end = Date.now() + ms
  while (!(await f())) {
    if (Date.now() > end) throw new Error('condition never became true')
    await new Promise((r) => setTimeout(r, 50))
  }
}

let n = 0
async function session(): Promise<string> {
  const sid = `m${++n}`
  await h.op.createSession(sid)
  return sid
}

describe('outside a session', () => {
  test('no tool is offered, so the agent never spends a call; status still answers', async () => {
    const a = await agent('mac-1', {})
    const tools = await a.client.listTools()
    seen.push(JSON.stringify(tools))
    expect(tools.tools).toEqual([])
    expect(a.client.getServerCapabilities()?.experimental).toBeUndefined()
    expect(a.client.getInstructions()).toBeUndefined()
    expect(await a.json('status')).toEqual({
      joined: false,
      reason: 'no shared session is set for this agent',
    })
  })

  test('a session without a machine set-up gives a clear reason', async () => {
    const a = await agent('mac-1', { session: 'x', url: undefined })
    const s = await a.json('status')
    expect(s).toMatchObject({ joined: false, reason: expect.stringContaining('not set up') })
    expect((await a.tool('send', { to: 'all', text: 'x' })).isError).toBe(true)
  })

  test('an unreachable service is reported, not thrown', async () => {
    const a = await agent('mac-1', { session: 'x', url: 'http://127.0.0.1:9' })
    await eventually(
      async () => (await a.json('status')).reason === 'message service unreachable (retrying)',
    )
    expect(await a.tool('send', { to: 'all', text: 'x' })).toEqual({
      text: 'message service unreachable (retrying)',
      isError: true,
    })
  })
})

describe('a machine without permission', () => {
  test('a wrong credential gives a plain reason and leaks nothing', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice', token: 'mac-1.not-the-secret' })
    await eventually(async () =>
      String((await a.json('status')).reason).includes('not allowed to join'),
    )
    const r = await a.tool('send', { to: 'all', text: 'x' })
    expect(r.isError).toBe(true)
    expect(r.text).toContain('not allowed to join')
  })
})

describe('in a session', () => {
  test('the tool list and instructions', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice', push: true })
    const tools = await a.client.listTools()
    seen.push(JSON.stringify(tools), a.client.getInstructions() ?? '')
    expect(tools.tools.map((t) => t.name).sort()).toEqual(
      ['ask', 'history', 'inbox', 'send', 'set_state', 'status', 'wait'].sort(),
    )
    expect(a.client.getInstructions()).toBe(INSTRUCTIONS)
    expect(a.client.getServerCapabilities()?.experimental).toEqual({ 'claude/channel': {} })
  })

  test('push: a message reaches the idle peer as a channel event within 250 ms', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice', push: true })
    const b = await agent('vps-2', { session: sid, agent: 'bob', push: true })
    await joined(a)
    await joined(b)
    const t0 = performance.now()
    const sent = await a.json('send', { to: 'bob', text: 'hello bob' })
    const p = await until(() => b.pushed.find((x) => x.meta.id === sent.id))
    expect(performance.now() - t0).toBeLessThan(250)
    expect(p).toEqual({
      content: 'hello bob',
      meta: { kind: 'message', from: 'alice@mac-1', to: 'bob@vps-2', id: sent.id },
    })
    expect(a.pushed).toEqual([])
    await eventually(async () =>
      (await readRange(h.hubBroker.js, h.hubBroker.jsm, [sessionSubjects(sid)])).some(
        (e) =>
          e.kind === 'evt' &&
          e.from.agent === 'bob' &&
          e.evt.kind === 'delivered' &&
          e.evt.via === 'push' &&
          e.evt.id === sent.id,
      ),
    )
  })

  test('pull: wait returns the message; inbox is then empty', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice' })
    const b = await agent('vps-2', { session: sid, agent: 'bob' })
    await joined(a)
    await joined(b)
    const waiting = b.json('wait', { timeout_s: 5 })
    await new Promise((r) => setTimeout(r, 100))
    const sent = await a.json('send', { to: 'all', text: 'news' })
    const got = await waiting
    expect(got).toMatchObject({ messages: [{ id: sent.id, from: 'alice@mac-1', text: 'news' }] })
    expect(await b.json('inbox')).toEqual({ messages: [], notices: [] })
  })

  test('ask: returns the answer; the answer is not pushed a second time', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice', push: true })
    const b = await agent('vps-2', { session: sid, agent: 'bob' })
    await joined(a)
    await joined(b)
    const asking = a.json('ask', { to: 'bob', text: 'shape of /users?', timeout_s: 10 })
    const q = await b.json('wait', { from: 'alice', timeout_s: 5 })
    const question = (q.messages as { id: string }[])[0]
    expect(question).toBeDefined()
    await b.json('send', { to: 'alice', text: '{id, name}', reply_to: question?.id })
    const r = await asking
    expect(r).toMatchObject({
      question: question?.id,
      answer: { text: '{id, name}', from: 'bob@vps-2' },
    })
    await new Promise((res) => setTimeout(res, 200))
    expect(a.pushed).toEqual([])
    const kinds = (
      await readRange(h.hubBroker.js, h.hubBroker.jsm, [sessionSubjects(sid)])
    ).flatMap((e) => (e.kind === 'evt' ? [`${e.from.agent}:${e.evt.kind}`] : []))
    expect(kinds).toEqual(
      expect.arrayContaining([
        'alice:wait_start',
        'alice:wait_end',
        'alice:delivered',
        'bob:delivered',
      ]),
    )
  })

  test('ask and wait(from) end when that peer leaves; a plain wait runs on', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice' })
    const b = await agent('vps-2', { session: sid, agent: 'bob' })
    const c = await agent('mac-3', { session: sid, agent: 'carol' })
    for (const x of [a, b, c]) await joined(x)
    const asking = a.json('ask', { to: 'bob', text: 'still there?', timeout_s: 10 })
    const onBob = c.json('wait', { from: 'bob', timeout_s: 10 })
    const onAny = c.json('wait', { timeout_s: 1 })
    await new Promise((r) => setTimeout(r, 200))
    await b.shim.stop()
    expect(await asking).toMatchObject({ peer_left: true })
    expect(await onBob).toMatchObject({
      messages: [],
      notices: [{ kind: 'peer_left', peer: 'bob@vps-2', text: 'bob@vps-2 left the session.' }],
    })
    expect(await onAny).toEqual({ timeout: true })
    expect(await a.json('inbox')).toEqual({ messages: [], notices: [] })
    expect(await c.json('inbox')).toEqual({ messages: [], notices: [] })
  })

  test('wait(from) ignores other peers; their message stays in the inbox', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice' })
    const b = await agent('vps-2', { session: sid, agent: 'bob' })
    await joined(a)
    await joined(b)
    await b.json('send', { to: 'alice', text: 'from bob' })
    await new Promise((r) => setTimeout(r, 100))
    expect(await a.json('wait', { from: 'carol', timeout_s: 1 })).toEqual({ timeout: true })
    expect(await a.json('inbox')).toMatchObject({ messages: [{ text: 'from bob' }] })
  })

  test('a taken name gets a suffix', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'dev' })
    const b = await agent('mac-1', { session: sid, agent: 'dev' })
    // Both start at once; either may win the plain name.
    const names = [(await joined(a)).me, (await joined(b)).me].sort()
    expect(names).toEqual(['dev-2@mac-1', 'dev@mac-1'])
  })

  test('status lists peers with their state', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice' })
    const b = await agent('vps-2', { session: sid, agent: 'bob' })
    await joined(a)
    await joined(b)
    await b.json('set_state', { state: 'blocked', note: 'need the schema' })
    expect(await a.json('status')).toMatchObject({
      joined: true,
      session: sid,
      session_open: true,
      me: 'alice@mac-1',
      peers: [{ name: 'bob@vps-2', state: 'blocked', note: 'need the schema', online: true }],
      unread: 0,
    })
  })

  test('history shows what I can see, and the conversation with one peer', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice' })
    const b = await agent('vps-2', { session: sid, agent: 'bob' })
    const c = await agent('mac-3', { session: sid, agent: 'carol' })
    for (const x of [a, b, c]) await joined(x)
    await a.json('send', { to: 'bob', text: 'dm to bob' })
    await a.json('send', { to: 'all', text: 'to all' })
    const hc = (await c.json('history', {})).messages as { text: string }[]
    expect(hc.map((m) => m.text)).toEqual(['to all'])
    const hb = (await b.json('history', { with: 'alice' })).messages as { text: string }[]
    expect(hb.map((m) => m.text)).toEqual(['dm to bob', 'to all'])
  })

  test('bad arguments are reported to the agent', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice' })
    await joined(a)
    const r = await a.tool('send', { to: 'nobody here', text: '' })
    expect(r.isError).toBe(true)
    expect(r.text).toContain('invalid arguments')
    const u = await a.tool('send', { to: 'zed', text: 'hi' })
    expect(u).toMatchObject({ isError: true, text: expect.stringContaining('no peer zed') })
  })
})

describe('operator actions as the agent sees them', () => {
  test('kick: a notice, then every tool says removed', async () => {
    const sid = await session()
    const b = await agent('vps-2', { session: sid, agent: 'bob', push: true })
    await joined(b)
    await h.op.kick(sid, { agent: 'bob', machine: 'vps-2' })
    const p = await until(() => b.pushed.find((x) => x.meta.kind === 'notice'))
    expect(p.meta.notice).toBe('kicked')
    await eventually(async () => (await b.json('status')).reason === 'removed from session')
    expect(await b.tool('send', { to: 'all', text: 'x' })).toEqual({
      text: 'removed from session',
      isError: true,
    })
  })

  test('close and reopen: notices; sending fails while closed', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice', push: true })
    await joined(a)
    await h.op.closeSession(sid)
    await until(() => a.pushed.find((x) => x.meta.notice === 'closed'))
    expect(await a.tool('send', { to: 'all', text: 'x' })).toEqual({
      text: 'session closed',
      isError: true,
    })
    await h.op.reopenSession(sid)
    await until(() => a.pushed.find((x) => x.meta.notice === 'reopened'))
    expect((await a.tool('send', { to: 'all', text: 'x' })).isError).toBe(false)
  })

  test('redact: the agent that got the message is told to disregard it', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice' })
    const b = await agent('vps-2', { session: sid, agent: 'bob', push: true })
    await joined(a)
    await joined(b)
    const sent = await a.json('send', { to: 'bob', text: 'wrong numbers' })
    await until(() => b.pushed.find((x) => x.meta.id === sent.id))
    await h.op.redact(sid, String(sent.id))
    const notice = await until(() => b.pushed.find((x) => x.meta.notice === 'redacted'))
    expect(notice.content).toContain(`withdrew message ${sent.id}`)
  })

  test('an operator message arrives from operator', async () => {
    const sid = await session()
    const a = await agent('mac-1', { session: sid, agent: 'alice', push: true })
    await joined(a)
    await h.op.send(sid, 'all', 'stop and report')
    const p = await until(() => a.pushed.find((x) => x.meta.from === 'operator'))
    expect(p.content).toBe('stop and report')
  })
})

describe('opacity', () => {
  test('nothing an agent saw names how the service works', () => {
    expect(seen.length).toBeGreaterThan(30)
    const leaks = seen.filter((t) => FORBIDDEN_RE.test(t))
    expect(leaks).toEqual([])
  })
})
