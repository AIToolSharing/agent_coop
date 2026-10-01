#!/usr/bin/env node
// coop-tui: observe and control all sessions. Runs on the server, next to nats-server.
//
// Environment:
//   COOP_NATS_URL                default nats://127.0.0.1:4222
//   COOP_OPERATOR_NATS_PASSWORD  password of NATS user "operator" (required)
import { Operator, openBroker } from '@coop/core/broker'
import { render } from 'ink'
import { App } from './app.js'
import { startFeed } from './data.js'
import { Store } from './model.js'

const pass = process.env.COOP_OPERATOR_NATS_PASSWORD
if (pass === undefined || pass === '') {
  console.error('COOP_OPERATOR_NATS_PASSWORD is not set')
  process.exit(2)
}
const broker = await openBroker({
  servers: process.env.COOP_NATS_URL ?? 'nats://127.0.0.1:4222',
  user: 'operator',
  pass,
  name: 'coop-tui',
})
const store = new Store()
const listeners = new Set<() => void>()
const ctl = new AbortController()
void startFeed(
  broker,
  store,
  () => {
    for (const l of listeners) l()
  },
  ctl.signal,
)

// SGR mouse reports: clicks select, the wheel scrolls. Only on a terminal.
const mouseOn = '\x1b[?1000h\x1b[?1006h'
const mouseOff = '\x1b[?1006l\x1b[?1000l'
const tty = process.stdout.isTTY === true
if (tty) process.stdout.write(mouseOn)
const app = render(
  <App
    store={store}
    op={new Operator(broker)}
    subscribe={(f) => {
      listeners.add(f)
      return () => listeners.delete(f)
    }}
  />,
  { alternateScreen: true },
)
await app.waitUntilExit()
if (tty) process.stdout.write(mouseOff)
ctl.abort()
await broker.close()
process.exit(0)
