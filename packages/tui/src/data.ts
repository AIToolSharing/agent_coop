// The live feed from the hub's admin API into the store. Updates are applied in batches so
// that a burst of events causes one repaint. After a (re)connection, a bucket's `snapshot`
// marker says that every current entry has been sent: entries not seen since are gone.
import { decodeBusEvent } from '@coop/core'
import type { FeedItem } from './client.js'
import type { Store, Update } from './model.js'

const encoder = new TextEncoder()

export function startFeed(
  feed: AsyncIterable<FeedItem>,
  store: Store,
  onChange: () => void,
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
  const flush = () => {
    clearTimeout(timer)
    timer = undefined
    const batch = pending
    pending = []
    if (batch.length > 0) store.apply(batch)
    onChange()
  }

  // Keys seen since the current connection began, per bucket.
  let seenSessions = new Set<string>()
  let seenKicks = new Set<string>()
  let seenPresence = new Set<string>()

  return (async () => {
    for await (const item of feed) {
      switch (item.kind) {
        case 'connected':
          seenSessions = new Set()
          seenKicks = new Set()
          seenPresence = new Set()
          push({ kind: 'link', state: 'live' })
          break
        case 'disconnected':
          push({ kind: 'link', state: 'reconnecting' })
          break
        case 'event': {
          const e = decodeBusEvent(item.subject, encoder.encode(item.payload), item.seq)
          if (e !== undefined) push({ kind: 'event', e })
          break
        }
        case 'session':
          seenSessions.add(item.session)
          push({
            kind: 'session',
            sid: item.session,
            record: item.record ?? undefined,
            revision: item.revision,
          })
          break
        case 'kick':
          seenKicks.add(item.key)
          push({
            kind: 'kick',
            key: item.key,
            record: item.record ?? undefined,
            revision: item.revision,
          })
          break
        case 'presence':
          seenPresence.add(item.key)
          push({
            kind: 'presence',
            key: item.key,
            record: item.record ?? undefined,
            revision: item.revision,
          })
          break
        case 'snapshot':
          // Apply what is pending first, so that the store's keys are current.
          flush()
          if (item.bucket === 'sessions') {
            for (const sid of store.sessions.keys()) {
              if (!seenSessions.has(sid)) push({ kind: 'session', sid, record: undefined })
            }
            for (const key of store.kicks.keys()) {
              if (!seenKicks.has(key)) push({ kind: 'kick', key, record: undefined })
            }
          } else {
            for (const key of store.presence.keys()) {
              if (!seenPresence.has(key)) push({ kind: 'presence', key, record: undefined })
            }
          }
          break
      }
    }
    flush()
  })()
}
