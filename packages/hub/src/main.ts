#!/usr/bin/env node
// coop-hub: the HTTPS-facing service (behind a TLS proxy), and its token and session commands.
//
//   coop-hub serve                    start the API
//   coop-hub token add <machine>      print a new token for an agent machine (replaces its old one)
//   coop-hub token add --operator <name>  print a new operator token (the admin API and the TUI)
//   coop-hub token list               list tokens: name, role, state
//   coop-hub token revoke <name>      revoke a token; a machine's agents drop out
//   coop-hub session add <sid> [title]  create an open session
//   coop-hub session list             list sessions and their state
//   coop-hub session close <sid>      close a session; its agents are disconnected
//   coop-hub session reopen <sid>     open a closed session again
//   coop-hub session delete <sid>     delete a closed session with all its messages
//
// Environment:
//   COOP_NATS_URL                default nats://127.0.0.1:4222
//   COOP_HUB_NATS_PASSWORD       password of NATS user "hub" (required)
//   COOP_HUB_LISTEN              default 127.0.0.1:8080
//   COOP_AUTO_CREATE_SESSIONS    1: the first agent to join an unknown session creates it
import { Operator, OperatorError, openBroker } from '@coop/core/broker'
import { serve } from '@hono/node-server'
import { createApp } from './app.js'
import { Hub } from './hub.js'
import { issueToken, listTokens, revokeToken } from './tokens.js'

const USAGE = `usage: coop-hub serve
       coop-hub token add [--operator] <name> | list | revoke <name>
       coop-hub session add <sid> [title] | list | close <sid> | reopen <sid> | delete <sid>`

async function main(argv: string[]): Promise<number> {
  const [cmd, sub, arg, extra] = argv
  const pass = process.env.COOP_HUB_NATS_PASSWORD
  if (pass === undefined || pass === '') {
    console.error('COOP_HUB_NATS_PASSWORD is not set')
    return 2
  }
  const broker = await openBroker({
    servers: process.env.COOP_NATS_URL ?? 'nats://127.0.0.1:4222',
    user: 'hub',
    pass,
    name: 'coop-hub',
  })

  if (cmd === 'serve') {
    const [hostname = '127.0.0.1', port = '8080'] = (
      process.env.COOP_HUB_LISTEN ?? '127.0.0.1:8080'
    ).split(':')
    const hub = new Hub(broker, {}, { autoCreate: process.env.COOP_AUTO_CREATE_SESSIONS === '1' })
    await hub.start()
    const server = serve({ fetch: createApp(hub).fetch, hostname, port: Number(port) })
    console.error(`coop-hub listening on ${hostname}:${port}`)
    await new Promise<void>((resolve) => {
      for (const sig of ['SIGINT', 'SIGTERM'] as const) process.once(sig, () => resolve())
    })
    await hub.stop()
    server.close()
    await broker.close()
    return 0
  }

  try {
    if (cmd === 'token') return await token(broker.tokens, sub, arg, extra)
    if (cmd === 'session') return await session(new Operator(broker), sub, arg, extra)
    console.error(USAGE)
    return 2
  } catch (err) {
    if (!(err instanceof OperatorError)) throw err
    console.error(err.message)
    return 1
  } finally {
    await broker.close()
  }
}

async function token(
  tokens: Parameters<typeof issueToken>[0],
  sub: string | undefined,
  arg: string | undefined,
  extra: string | undefined,
): Promise<number> {
  if (sub === 'add' && arg !== undefined) {
    const operator = arg === '--operator'
    const name = operator ? extra : arg
    if (name === undefined) {
      console.error(USAGE)
      return 2
    }
    console.log(await issueToken(tokens, name, operator ? 'operator' : 'machine'))
    return 0
  }
  if (sub === 'list') {
    for (const t of await listTokens(tokens)) {
      console.log(
        `${t.name}\t${t.role}\tcreated ${t.created_at}\t${t.revoked_at ? `revoked ${t.revoked_at}` : 'valid'}`,
      )
    }
    return 0
  }
  if (sub === 'revoke' && arg !== undefined) {
    const ok = await revokeToken(tokens, arg)
    console.error(ok ? `revoked ${arg}` : `no valid token for ${arg}`)
    return ok ? 0 : 1
  }
  console.error(USAGE)
  return 2
}

async function session(
  op: Operator,
  sub: string | undefined,
  sid: string | undefined,
  title: string | undefined,
): Promise<number> {
  if (sub === 'list') {
    for (const s of await op.listSessions()) {
      const r = s.record
      console.log(
        `${s.sid}\t${r.status}\tcreated ${r.created_at}${r.closed_at ? `\tclosed ${r.closed_at}` : ''}${r.title ? `\t${r.title}` : ''}`,
      )
    }
    return 0
  }
  if (sid === undefined) {
    console.error(USAGE)
    return 2
  }
  switch (sub) {
    case 'add':
      await op.createSession(sid, title)
      console.error(`created ${sid}`)
      return 0
    case 'close':
      await op.closeSession(sid)
      console.error(`closed ${sid}`)
      return 0
    case 'reopen':
      await op.reopenSession(sid)
      console.error(`reopened ${sid}`)
      return 0
    case 'delete':
      await op.deleteSession(sid)
      console.error(`deleted ${sid}`)
      return 0
    default:
      console.error(USAGE)
      return 2
  }
}

process.exitCode = await main(process.argv.slice(2))
