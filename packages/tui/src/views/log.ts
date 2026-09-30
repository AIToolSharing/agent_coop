// View 1: every message, live, with delivery ticks; system lines between them.
import type { Derived } from '../model.js'
import { items, senderColor, short, ticks, type ViewOptions } from './common.js'
import { clock, fitLine, type Line, type Rendered, seg } from './line.js'

export function renderLog(d: Derived, o: ViewOptions): Rendered {
  const lines: Line[] = []
  const ids: (string | undefined)[] = []
  const who = Math.max(10, Math.min(24, Math.floor(o.width / 6)))
  for (const it of items(d, o)) {
    if (it.kind === 'sys') {
      lines.push(fitLine([seg(`${clock(it.at)}  · ${it.who} ${it.text}`, { dim: true })], o.width))
      ids.push(undefined)
      continue
    }
    const m = it.msg
    const sel = m.id === o.selected
    const t = ticks(m)
    const head: Line = [
      seg(`${clock(m.sent_at)} `, { dim: true, inverse: sel }),
      seg(short(m.from, who).padEnd(who), { color: senderColor(m.from), bold: true }),
      seg(' → '),
      seg(short(m.to, who).padEnd(who)),
      seg(` #${m.id}`, { dim: true }),
      seg(m.reply_to === undefined ? ' ' : ` ↩${m.reply_to} `, { color: 'cyan' }),
    ]
    const tail: Line = [
      seg(' '),
      t,
      m.replies.length > 0 ? seg(` ↳${m.replies.length}`, { color: 'cyan' }) : seg(''),
    ]
    const used = [...head, ...tail].reduce((n, s) => n + [...s.text].length, 0)
    const body = seg(m.redacted ? '[withdrawn]' : m.text, m.redacted ? { dim: true } : {})
    const bodyLine = fitLine([body], Math.max(0, o.width - used))
    lines.push(fitLine([...head, ...bodyLine, ...tail], o.width))
    ids.push(m.id)
  }
  return { lines, ids }
}
