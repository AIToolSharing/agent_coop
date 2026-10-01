// View 1: the conversation as a transcript. Every message shows whole, wrapped; a reply quotes
// the message it answers; system events (joins, leaves, states, waits) fold into one dim line
// per burst; a separator marks a change of day.
import type { Derived, MsgRow, TimelineItem } from '../model.js'
import { items, senderColor, ticks, type ViewOptions } from './common.js'
import { clock, fitLine, type Line, len, note, type Rendered, seg, stamp, wrap } from './line.js'

const INDENT = '  '

export function renderTranscript(d: Derived, o: ViewOptions): Rendered {
  const list = items(d, o)
  if (list.length === 0) return note('no messages yet', o.width)
  const lines: Line[] = []
  const ids: (string | undefined)[] = []
  const add = (l: Line, id?: string) => {
    lines.push(fitLine(l, o.width))
    ids.push(id)
  }
  const byId = new Map(d.messages.map((m) => [m.id, m]))
  let day: string | undefined
  let burst: Extract<TimelineItem, { kind: 'sys' }>[] = []
  const flush = () => {
    const first = burst[0]
    if (first === undefined) return
    const texts = burst.map((s) => `${s.who} ${s.text}`).join(' · ')
    add([seg(`${clock(first.at)}  · `, { dim: true }), seg(texts, { dim: true })])
    burst = []
  }
  for (const it of list) {
    const date = stamp(it.kind === 'sys' ? it.at : it.msg.sent_at).slice(0, 10)
    if (date !== day) {
      flush()
      day = date
      add([seg(`── ${date} ${'─'.repeat(Math.max(0, o.width - len(date) - 4))}`, { dim: true })])
    }
    if (it.kind === 'sys') {
      burst.push(it)
      continue
    }
    flush()
    const m = it.msg
    add(header(m, o), m.id)
    if (m.reply_to !== undefined) {
      const p = byId.get(m.reply_to)
      const quote =
        p === undefined
          ? `↩ #${m.reply_to}`
          : `↩ #${p.id} ${p.from}: ${p.redacted ? '[withdrawn]' : p.text}`
      add([seg(INDENT), seg(quote, { color: 'cyan', dim: true })])
    }
    const body = m.redacted ? ['[withdrawn]'] : wrap(m.text, Math.max(1, o.width - INDENT.length))
    for (const t of body) add([seg(INDENT), seg(t, m.redacted ? { dim: true } : {})])
  }
  flush()
  return { lines, ids }
}

/** Time, sender and target on the left; id, delivery and reply count on the right. */
function header(m: MsgRow, o: ViewOptions): Line {
  const sel = m.id === o.selected
  const right: Line = [
    seg(`#${m.id}`, { dim: true }),
    seg('  '),
    ticks(m),
    m.replies.length > 0 ? seg(`  ↳${m.replies.length}`, { color: 'cyan' }) : seg(''),
  ]
  const left: Line = [
    seg(clock(m.sent_at), { dim: true, inverse: sel }),
    seg('  '),
    seg(m.from, { bold: true, color: senderColor(m.from) }),
    seg(' → '),
    seg(m.to),
  ]
  const used = [...left, ...right].reduce((n, s) => n + len(s.text), 0)
  return [...left, seg(' '.repeat(Math.max(1, o.width - used))), ...right]
}
