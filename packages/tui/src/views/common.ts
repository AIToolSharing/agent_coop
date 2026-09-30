// Filters and short forms that several views share.
import { BROADCAST, OPERATOR } from '@coop/core'
import type { Derived, MsgRow, TimelineItem } from '../model.js'
import { type Seg, seg } from './line.js'

export interface ViewOptions {
  readonly width: number
  readonly now: number
  /** Only messages from or to this agent. */
  readonly agent?: string | undefined
  /** Only messages whose text contains this (case-insensitive). */
  readonly search?: string | undefined
  /** Show joins, leaves, state changes and waits. */
  readonly system?: boolean | undefined
  readonly selected?: string | undefined
}

export function msgVisible(m: MsgRow, o: ViewOptions): boolean {
  if (
    o.agent !== undefined &&
    m.from !== o.agent &&
    m.to !== o.agent &&
    !m.recipients.includes(o.agent)
  ) {
    return false
  }
  if (
    o.search !== undefined &&
    o.search !== '' &&
    !m.text.toLowerCase().includes(o.search.toLowerCase())
  ) {
    return false
  }
  return true
}

export function items(d: Derived, o: ViewOptions): TimelineItem[] {
  return d.timeline.filter((it) =>
    it.kind === 'msg'
      ? msgVisible(it.msg, o)
      : o.system === true && (o.agent === undefined || it.who === o.agent),
  )
}

/** The short tick text for a message: ✓ with latency for one recipient, k/n for many. */
export function ticks(m: MsgRow): Seg {
  if (m.redacted) return seg('withdrawn', { dim: true })
  const n = m.recipients.length
  const k = [...m.deliveries.keys()].filter((r) => m.recipients.includes(r)).length
  if (m.to !== BROADCAST) {
    const d = m.deliveries.get(m.to)
    return d === undefined
      ? seg('◌ pending', { color: 'yellow' })
      : seg(`✓ ${d.ms}ms ${d.via}`, { color: 'green' })
  }
  if (n === 0) return seg('no one', { dim: true })
  return seg(`✓${k}/${n}`, { color: k === n ? 'green' : 'yellow' })
}

export function senderColor(from: string): string | undefined {
  return from === OPERATOR ? 'yellow' : undefined
}

/** `alice@mac-1` stays whole when there is room; else the agent part only. */
export function short(address: string, width: number): string {
  if ([...address].length <= width) return address
  return address.split('@')[0] ?? address
}
