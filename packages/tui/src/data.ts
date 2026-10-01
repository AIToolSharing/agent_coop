// The live feed: every stream event from the start, plus the session and presence buckets.
// Updates are applied in batches so that a burst of events causes one repaint.
import {
  decode,
  KickRecord,
  PresenceRecord,
  parsePresenceKey,
  parseSessionsKey,
  SessionRecord,
} from '@coop/core'
import { type Broker, follow } from '@coop/core/broker'
import type { Store, Update } from './model.js'

export function startFeed(
  b: Broker,
  store: Store,
  onChange: () => void,
  signal: AbortSignal,
  batchMs = 50,
): Promise<void> {
  let pending: Update[] = []
  let timer: ReturnType<typeof setTimeout> | undefined
  const push = (u: Update) => {
    pending.push(u)
    if (timer !== undefined) return
    timer = setTimeout(() => {
      timer = undefined
      const batch = pending
      pending = []
      store.apply(batch)
      onChange()
    }, batchMs)
  }

  const events = (async () => {
    for await (const e of follow(b.js, ['coop.>'], 1, signal)) push({ kind: 'event', e })
  })()

  const sessions = (async () => {
    const w = await b.sessions.watch()
    signal.addEventListener('abort', () => w.stop(), { once: true })
    for await (const e of w) {
      const k = parseSessionsKey(e.key)
      if (k === undefined) continue
      if (k.kind === 'kick') {
        const record = e.operation === 'PUT' ? decode(KickRecord, e.value) : undefined
        push({ kind: 'kick', key: e.key, record })
        continue
      }
      const record = e.operation === 'PUT' ? decode(SessionRecord, e.value) : undefined
      push({ kind: 'session', sid: k.sid, record })
    }
  })()

  const presence = (async () => {
    const w = await b.presence.watch()
    signal.addEventListener('abort', () => w.stop(), { once: true })
    for await (const e of w) {
      if (parsePresenceKey(e.key) === undefined) continue
      const record = e.operation === 'PUT' ? decode(PresenceRecord, e.value) : undefined
      push({ kind: 'presence', key: e.key, record })
    }
  })()

  return Promise.all([events, sessions, presence]).then(() => undefined)
}
