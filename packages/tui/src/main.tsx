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
ctl.abort()
await broker.close()
process.exit(0)
