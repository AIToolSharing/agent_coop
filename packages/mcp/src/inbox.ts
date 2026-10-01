// Routes each incoming message to exactly one place, in this order:
//   1. an open `ask` that waits for this reply,
//   2. an open `wait` whose filter matches,
//   3. a push into the session (channel), when push is on,
//   4. the queue, for `inbox` and later `wait` calls.
// Notices (removed, closed, reopened, withdrawn) follow the same order, without step 1.
// A `peer_left` notice only ends the asks and the filtered waits on that peer; it goes nowhere
// else, so that presence changes never wake an agent that did not wait for that peer.
import type { ApiMessage, DeliveryVia, NoticeEvent } from '@coop/core'

export type Item =
  | { readonly kind: 'message'; readonly msg: ApiMessage }
  | { readonly kind: 'notice'; readonly notice: NoticeEvent }

export interface InboxOptions {
  readonly push: boolean
  /** Push one item into the session. */
  readonly onPush: (item: Item) => Promise<void>
  /** Called once per message, when it reaches the agent. */
  readonly onDelivered: (id: string, via: DeliveryVia) => void
  /** Queue size; the oldest items go first when it is full. */
  readonly cap?: number
}

interface Waiter {
  readonly from: string | undefined
  resolve(items: Item[]): void
}

/** The end of an `ask`: the answer, or the asked peer left. Timeout gives undefined. */
export type AskResult = ApiMessage | 'peer_left' | undefined

interface Asker {
  readonly from: string
  resolve(r: AskResult): void
}

export class Inbox {
  private queue: Item[] = []
  private readonly waiters = new Set<Waiter>()
  private readonly asks = new Map<string, Asker>()
  /** Items dropped because the queue was full. */
  dropped = 0

  constructor(private readonly o: InboxOptions) {}

  get unread(): number {
    return this.queue.length
  }

  async accept(item: Item): Promise<void> {
    if (item.kind === 'notice' && item.notice.kind === 'peer_left') {
      this.peerLeft(item, item.notice.peer)
      return
    }
    if (item.kind === 'message') {
      const m = item.msg
      const a = m.reply_to === undefined ? undefined : this.asks.get(m.reply_to)
      if (a !== undefined && m.reply_to !== undefined && a.from === m.from) {
        this.asks.delete(m.reply_to)
        this.o.onDelivered(m.id, 'ask')
        a.resolve(m)
        return
      }
    }
    for (const w of this.waiters) {
      if (w.from === undefined || item.kind === 'notice' || matchesPeer(w.from, fromOf(item))) {
        this.waiters.delete(w)
        this.delivered([item], 'pull')
        w.resolve([item])
        return
      }
    }
    if (this.o.push) {
      try {
        await this.o.onPush(item)
        this.delivered([item], 'push')
        return
      } catch {
        // The push failed (the session is closing or busy). The queue keeps the item.
      }
    }
    this.queue.push(item)
    const cap = this.o.cap ?? 1000
    if (this.queue.length > cap) {
      this.dropped += this.queue.length - cap
      this.queue = this.queue.slice(-cap)
    }
  }

  /** Everything queued, oldest first. The queue is then empty. */
  take(): Item[] {
    const items = this.queue
    this.queue = []
    this.delivered(items, 'pull')
    return items
  }

  /**
   * The next item (from `from`, when given; notices always match). Queued items come first.
   * Resolves with [] on timeout.
   */
  wait(from: string | undefined, timeoutMs: number): Promise<Item[]> {
    const i = this.queue.findIndex(
      (it) => from === undefined || it.kind === 'notice' || matchesPeer(from, fromOf(it)),
    )
    const it = this.queue[i]
    if (it !== undefined) {
      this.queue.splice(i, 1)
      this.delivered([it], 'pull')
      return Promise.resolve([it])
    }
    return new Promise((resolve) => {
      const w: Waiter = {
        from,
        resolve: (items) => {
          clearTimeout(t)
          resolve(items)
        },
      }
      const t = setTimeout(() => {
        this.waiters.delete(w)
        resolve([])
      }, timeoutMs)
      this.waiters.add(w)
    })
  }

  /** End every ask and every filtered wait on `peer`. */
  private peerLeft(item: Item, peer: string | undefined) {
    if (peer === undefined) return
    for (const [id, a] of this.asks) {
      if (!matchesPeer(a.from, peer)) continue
      this.asks.delete(id)
      a.resolve('peer_left')
    }
    for (const w of this.waiters) {
      if (w.from === undefined || !matchesPeer(w.from, peer)) continue
      this.waiters.delete(w)
      w.resolve([item])
    }
  }

  /** Wait for the reply to message `id` from `from`. Resolves undefined on timeout. */
  expectReply(id: string, from: string, timeoutMs: number): Promise<AskResult> {
    const i = this.queue.findIndex(
      (it) => it.kind === 'message' && it.msg.reply_to === id && it.msg.from === from,
    )
    const queued = this.queue[i]
    if (queued?.kind === 'message') {
      this.queue.splice(i, 1)
      this.o.onDelivered(queued.msg.id, 'ask')
      return Promise.resolve(queued.msg)
    }
    return new Promise((resolve) => {
      const t = setTimeout(() => {
        this.asks.delete(id)
        resolve(undefined)
      }, timeoutMs)
      this.asks.set(id, {
        from,
        resolve: (r) => {
          clearTimeout(t)
          resolve(r)
        },
      })
    })
  }

  private delivered(items: Item[], via: DeliveryVia) {
    for (const it of items) if (it.kind === 'message') this.o.onDelivered(it.msg.id, via)
  }
}

function fromOf(item: Item): string | undefined {
  return item.kind === 'message' ? item.msg.from : undefined
}

/** A filter `name` matches `name@any-machine`; a filter `name@machine` matches only itself. */
export function matchesPeer(filter: string, from: string | undefined): boolean {
  if (from === undefined) return false
  return filter.includes('@') ? from === filter : from.split('@')[0] === filter
}
