#!/usr/bin/env node
// coop-hub: the HTTPS-facing service (behind a TLS proxy) and its token commands.
//
//   coop-hub serve                  start the API
//   coop-hub token add <machine>    print a new token for a machine (replaces its old token)
//   coop-hub token list             list machines and token state
//   coop-hub token revoke <machine> revoke a machine's token; its agents drop out
//
// Environment:
//   COOP_NATS_URL            default nats://127.0.0.1:4222
//   COOP_HUB_NATS_PASSWORD   password of NATS user "hub" (required)
//   COOP_HUB_LISTEN          default 127.0.0.1:8080
import { openBroker } from '@coop/core/broker'
import { serve } from '@hono/node-server'
import { createApp } from './app.js'
import { Hub } from './hub.js'
import { issueToken, listTokens, revokeToken } from './tokens.js'

const USAGE = 'usage: coop-hub serve | token add <machine> | token list | token revoke <machine>'

async function main(argv: string[]): Promise<number> {
  const [cmd, sub, arg] = argv
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
    const hub = new Hub(broker)
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
    if (cmd === 'token' && sub === 'add' && arg !== undefined) {
      console.log(await issueToken(broker.tokens, arg))
      return 0
    }
    if (cmd === 'token' && sub === 'list') {
      for (const t of await listTokens(broker.tokens)) {
        console.log(
          `${t.machine}\tcreated ${t.created_at}\t${t.revoked_at ? `revoked ${t.revoked_at}` : 'valid'}`,
        )
      }
      return 0
    }
    if (cmd === 'token' && sub === 'revoke' && arg !== undefined) {
      const ok = await revokeToken(broker.tokens, arg)
      console.error(ok ? `revoked ${arg}` : `no valid token for ${arg}`)
      return ok ? 0 : 1
    }
    console.error(USAGE)
    return 2
  } finally {
    await broker.close()
  }
}

process.exitCode = await main(process.argv.slice(2))
