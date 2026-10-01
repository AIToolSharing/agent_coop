#!/usr/bin/env node
// coop-tui: observe and control all sessions through the hub's admin API. It runs on any
// machine that can reach the hub; see config.ts for where the address and the token come from.
import { DEFAULT_ENV_FILE } from '@coop/core'
import { render } from 'ink'
import { App } from './app.js'
import { AdminClient, RefusedError } from './client.js'
import { login, parse, USAGE } from './config.js'
import { startFeed } from './data.js'
import { Store } from './model.js'

const parsed = parse(process.argv.slice(2))
if (parsed.kind === 'usage') {
  if (parsed.error !== undefined) console.error(parsed.error)
  console.error(USAGE)
  process.exit(parsed.error === undefined ? 0 : 2)
}
if (parsed.kind === 'login') {
  const r = await login(parsed.url, parsed.token)
  if (r === 'ok') {
    console.log(`wrote ${DEFAULT_ENV_FILE} (mode 0600); run coop-tui`)
    process.exit(0)
  }
  console.error(
    r === 'bad_token'
      ? `${parsed.url} refused the token; ask for one with: coop-hub token add --operator <name>`
      : r === 'machine_token'
        ? 'that is a machine token; the TUI needs an operator token (coop-hub token add --operator)'
        : r === 'unreachable'
          ? `cannot reach ${parsed.url}`
          : `${parsed.url} answered ${r.unexpected}`,
  )
  process.exit(1)
}

const client = new AdminClient(parsed.config.url, parsed.config.token)
const store = new Store()
const listeners = new Set<() => void>()
const ctl = new AbortController()
const feed = startFeed(client.feed(ctl.signal), store, () => {
  for (const l of listeners) l()
})

// SGR mouse reports: clicks select, the wheel scrolls. Only on a terminal.
const mouseOn = '\x1b[?1000h\x1b[?1006h'
const mouseOff = '\x1b[?1006l\x1b[?1000l'
const tty = process.stdout.isTTY === true
if (tty) process.stdout.write(mouseOn)
const app = render(
  <App
    store={store}
    op={client}
    subscribe={(f) => {
      listeners.add(f)
      return () => listeners.delete(f)
    }}
  />,
  { alternateScreen: true },
)
// A refused token ends the feed; the TUI then has nothing to show.
void feed.catch((err: unknown) => {
  app.unmount()
  if (tty) process.stdout.write(mouseOff)
  console.error(err instanceof RefusedError ? `the service refused the token: ${err.message}` : err)
  process.exit(1)
})
await app.waitUntilExit()
if (tty) process.stdout.write(mouseOff)
ctl.abort()
await feed.catch(() => undefined)
process.exit(0)
