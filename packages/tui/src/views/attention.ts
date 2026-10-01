// What needs the operator: messages for the operator with no answer, asks that wait too long,
// and agents that say they are blocked.
import { OPERATOR } from '@coop/core'
import type { Derived } from '../model.js'
import { age, type Line, seg } from './line.js'

/** An ask older than this is stale: the asked peer has not answered in a while. */
export const STALE_ASK_MS = 2 * 60_000

export type AttentionItem =
  | { readonly kind: 'for_you'; readonly id: string; readonly from: string }
  | {
      readonly kind: 'ask'
      readonly id: string
      readonly from: string
      readonly to: string
      readonly since: string
    }
  | { readonly kind: 'blocked'; readonly address: string; readonly note: string | undefined }

export function attention(d: Derived, now: number): AttentionItem[] {
  const answered = new Set<string>()
  for (const m of d.messages)
    if (m.from === OPERATOR && m.reply_to !== undefined) answered.add(m.reply_to)
  const forYou: AttentionItem[] = d.messages
    .filter((m) => m.to === OPERATOR && !m.redacted && !answered.has(m.id))
    .map((m) => ({ kind: 'for_you', id: m.id, from: m.from }))
  const asks: AttentionItem[] = d.openAsks
    .filter((q) => now - Date.parse(q.since) >= STALE_ASK_MS)
    .map((q) => ({ kind: 'ask', id: q.id, from: q.from, to: q.to, since: q.since }))
  const blocked: AttentionItem[] = d.agents
    .filter((a) => a.online && a.state === 'blocked')
    .map((a) => ({ kind: 'blocked', address: a.address, note: a.note }))
  return [...forYou, ...asks, ...blocked]
}

/** One line that counts the items, or undefined when nothing needs the operator. */
export function renderAttention(items: readonly AttentionItem[], now: number): Line | undefined {
  if (items.length === 0) return undefined
  const n = (k: AttentionItem['kind']) => items.filter((i) => i.kind === k).length
  const parts: string[] = []
  if (n('for_you') > 0) parts.push(`${n('for_you')} for you`)
  const asks = items.filter((i): i is Extract<AttentionItem, { kind: 'ask' }> => i.kind === 'ask')
  if (asks.length > 0) {
    const oldest = asks.reduce((a, b) => (a.since < b.since ? a : b))
    parts.push(`${asks.length} ask${asks.length > 1 ? 's' : ''} waiting ${age(oldest.since, now)}`)
  }
  if (n('blocked') > 0) parts.push(`${n('blocked')} blocked`)
  return [
    seg('⚑ ', { color: 'yellow', bold: true }),
    seg(parts.join(' · '), { color: 'yellow' }),
    seg('   a: next', { dim: true }),
  ]
}

/** A short description of one item for the status line. */
export function describe(item: AttentionItem): string {
  switch (item.kind) {
    case 'for_you':
      return `for you: #${item.id} from ${item.from}`
    case 'ask':
      return `${item.from} waits for ${item.to} (ask #${item.id})`
    case 'blocked':
      return `${item.address} is blocked${item.note ? `: ${item.note}` : ''}`
  }
}
