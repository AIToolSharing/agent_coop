// View 4: who talks to whom. Message counts per sender and receiver, and median delivery latency.
import { BROADCAST } from '@coop/core'
import type { Derived } from '../model.js'
import type { ViewOptions } from './common.js'
import { fit, fitLine, type Line, note, type Rendered, seg } from './line.js'

export function renderMatrix(d: Derived, o: ViewOptions): Rendered {
  const { senders, receivers, count, latency } = d.matrix
  const lines: Line[] = []
  if (senders.length === 0) {
    return note('no messages yet', o.width)
  }
  const labelW = Math.min(22, Math.max(8, ...senders.map((s) => [...s].length)) + 1)
  const cellW = Math.max(6, Math.floor((o.width - labelW) / Math.max(1, receivers.length)))
  const col = (s: string) => fit(s === BROADCAST ? 'all' : (s.split('@')[0] ?? s), cellW)

  const table = (title: string, cell: (f: string, t: string) => string) => {
    lines.push(fitLine([seg(title, { bold: true })], o.width))
    lines.push(
      fitLine(
        [
          seg(fit('from \\ to', labelW), { dim: true }),
          ...receivers.map((r) => seg(col(r), { bold: true })),
        ],
        o.width,
      ),
    )
    for (const f of senders) {
      lines.push(
        fitLine(
          [
            seg(fit(f, labelW), { bold: true }),
            ...receivers.map((t) => {
              const v = cell(f, t)
              return seg(fit(v, cellW), v === '·' ? { dim: true } : {})
            }),
          ],
          o.width,
        ),
      )
    }
  }
  table('MESSAGES', (f, t) => {
    const n = count(f, t)
    return n === 0 ? '·' : String(n)
  })
  lines.push([seg('')])
  table('MEDIAN DELIVERY (ms)', (f, t) => {
    if (t === BROADCAST) return '·'
    const ms = latency(f, t)
    return ms === undefined ? '·' : String(Math.round(ms))
  })
  return { lines, ids: lines.map(() => undefined) }
}
