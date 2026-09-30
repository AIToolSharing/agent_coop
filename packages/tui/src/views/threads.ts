// View 3: conversations as reply chains. Open asks (questions without an answer) come first,
// oldest first, because they show who is blocked on whom.
import type { Derived, MsgRow } from '../model.js'
import { msgVisible, senderColor, ticks, type ViewOptions } from './common.js'
import { age, fitLine, type Line, type Rendered, seg } from './line.js'

export function renderThreads(d: Derived, o: ViewOptions): Rendered {
  const lines: Line[] = []
  const ids: (string | undefined)[] = []
  const add = (l: Line, id?: string) => {
    lines.push(fitLine(l, o.width))
    ids.push(id)
  }

  add([seg(`OPEN ASKS (${d.openAsks.length})`, { bold: true })])
  if (d.openAsks.length === 0) add([seg('  none', { dim: true })])
  for (const q of d.openAsks) {
    add(
      [
        seg(`  ⏳ ${age(q.since, o.now).padStart(4)}  `, {
          color: 'yellow',
          inverse: q.id === o.selected,
        }),
        seg(`${q.from} → ${q.to}`, { bold: true }),
        seg(`  #${q.id} `, { dim: true }),
        seg(q.text),
      ],
      q.id,
    )
  }

  add([seg('')])
  add([seg('THREADS', { bold: true })])
  const byId = new Map(d.messages.map((m) => [m.id, m]))
  const roots = d.messages.filter(
    (m) => (m.reply_to === undefined || !byId.has(m.reply_to)) && msgVisible(m, o),
  )
  const walk = (m: MsgRow, depth: number) => {
    const indent = depth === 0 ? '' : `${'   '.repeat(depth - 1)} └─ `
    add(
      [
        seg(indent, { dim: true }),
        seg(`#${m.id} `, { dim: true, inverse: m.id === o.selected }),
        seg(m.from, { bold: true, color: senderColor(m.from) }),
        seg(` → ${m.to}: `),
        seg(m.redacted ? '[withdrawn]' : m.text, m.redacted ? { dim: true } : {}),
        seg('  '),
        ticks(m),
      ],
      m.id,
    )
    for (const r of m.replies) {
      const child = byId.get(r)
      if (child !== undefined) walk(child, depth + 1)
    }
  }
  for (const r of roots) walk(r, 0)
  return { lines, ids }
}
