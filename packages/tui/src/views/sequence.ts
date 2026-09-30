// View 2: a live sequence diagram. One lane per agent (operator first when it spoke). A direct
// message is an arrow from the sender's lane to the target's lane; a broadcast is a double line
// over all lanes. Waits and joins show as marks on the agent's lane.
import { BROADCAST, OPERATOR } from '@coop/core'
import type { Derived, MsgRow } from '../model.js'
import { items, short, ticks, type ViewOptions } from './common.js'
import { clock, fit, fitLine, type Line, note, type Rendered, seg } from './line.js'

const TIME_W = 9
const TICK_W = 14

export interface Lanes {
  readonly names: readonly string[]
  /** Column of each lane's line, counted from the start of the row. */
  readonly x: readonly number[]
  readonly area: number
}

/** Lane columns: equal slots over the drawing area, each line in the middle of its slot. */
export function lanes(names: readonly string[], width: number): Lanes {
  const area = Math.max(0, width - TIME_W - TICK_W)
  const n = Math.max(1, names.length)
  const x = names.map((_, i) => TIME_W + Math.floor(((i + 0.5) * area) / n))
  return { names, x, area }
}

export function laneNames(d: Derived): string[] {
  const names = d.agents.map((a) => a.address)
  return d.messages.some((m) => m.from === OPERATOR) ? [OPERATOR, ...names] : names
}

export function renderSequence(d: Derived, o: ViewOptions): Rendered {
  const L = lanes(laneNames(d), o.width)
  const lines: Line[] = []
  const ids: (string | undefined)[] = []
  if (L.names.length === 0) return note('no agents in this session yet', o.width)

  lines.push(header(L, o.width))
  ids.push(undefined)

  for (const it of items(d, { ...o, system: true })) {
    if (it.kind === 'msg') {
      const row = messageRow(it.msg, L)
      if (row === undefined) continue
      const sel = it.msg.id === o.selected
      lines.push(
        fitLine(
          [
            seg(fit(clock(it.at), TIME_W - 1), { dim: true, inverse: sel }),
            seg(' '),
            seg(row.chars.slice(TIME_W).join(''), row.style),
            ticks(it.msg),
          ],
          o.width,
        ),
      )
      ids.push(it.msg.id)
      continue
    }
    // Joins, leaves and waits always show: they explain the arrows. Other system lines on request.
    const mark = LANE_MARKS.find(([prefix]) => it.text.startsWith(prefix))?.[1]
    if (mark === undefined && o.system !== true) continue
    const i = L.names.indexOf(it.who)
    if (i < 0) continue
    const row = base(L)
    const x = L.x[i] ?? TIME_W
    row[x] = mark ?? '·'
    // The note stops before the next lane, so that lane's line stays visible.
    const end = L.x[i + 1] ?? TIME_W + L.area
    put(row, x + 2, fit(it.text, Math.max(0, end - x - 3)).trimEnd(), end - 1)
    lines.push(
      fitLine(
        [
          seg(fit(clock(it.at), TIME_W - 1), { dim: true }),
          seg(' '),
          seg(row.slice(TIME_W).join(''), { dim: true }),
        ],
        o.width,
      ),
    )
    ids.push(undefined)
  }
  return { lines, ids }
}

const LANE_MARKS: readonly (readonly [string, string])[] = [
  ['waits', '┆'],
  ['wait ended', '┆'],
  ['joined', '●'],
  ['left', '✕'],
]

function header(L: Lanes, width: number): Line {
  const row = Array.from({ length: TIME_W + L.area }, () => ' ')
  const slot = Math.max(1, Math.floor(L.area / Math.max(1, L.names.length)))
  L.names.forEach((name, i) => {
    const label = short(name, slot - 1)
    const x = (L.x[i] ?? TIME_W) - Math.floor([...label].length / 2)
    put(row, Math.max(TIME_W, x), label, TIME_W + L.area)
  })
  return fitLine([seg(row.join(''), { bold: true })], width)
}

function base(L: Lanes): string[] {
  const row = Array.from({ length: TIME_W + L.area }, () => ' ')
  for (const x of L.x) row[x] = '│'
  return row
}

function put(row: string[], at: number, text: string, end: number) {
  const cps = [...text]
  for (let i = 0; i < cps.length && at + i < end; i++) row[at + i] = cps[i] ?? ' '
}

export interface Row {
  readonly chars: string[]
  readonly style: { color?: string; dim?: boolean }
}

/** The drawing of one message on the lanes, or undefined if a lane is missing. */
export function messageRow(m: MsgRow, L: Lanes): Row | undefined {
  const a = L.names.indexOf(m.from)
  if (a < 0) return undefined
  const row = base(L)
  const xa = L.x[a] ?? 0
  const label = `#${m.id} ${m.redacted ? '[withdrawn]' : m.text}`
  const style = m.redacted
    ? { dim: true }
    : m.from === OPERATOR
      ? { color: 'yellow' }
      : m.to === BROADCAST
        ? { color: 'magenta' }
        : {}
  if (m.to === BROADCAST) {
    // Span the sender and its recipients only; the operator lane is not a recipient.
    const xs = [
      xa,
      ...m.recipients.flatMap((r) => {
        const i = L.names.indexOf(r)
        return i < 0 ? [] : [L.x[i] ?? 0]
      }),
    ]
    const lo = Math.min(...xs)
    const hi = Math.max(...xs)
    for (let x = lo; x <= hi; x++) row[x] = '═'
    row[xa] = '●'
    if (hi > xa) row[hi - 1] = '►'
    if (lo < xa) row[lo + 1] = '◄'
    labelInside(row, lo, hi, label)
    return { chars: row, style }
  }
  const b = L.names.indexOf(m.to)
  if (b < 0 || b === a) return undefined
  const xb = L.x[b] ?? 0
  if (xb > xa) {
    row[xa] = '├'
    for (let x = xa + 1; x < xb - 1; x++) row[x] = '─'
    row[xb - 1] = '►'
    labelInside(row, xa, xb, label)
  } else {
    row[xa] = '┤'
    for (let x = xb + 2; x < xa; x++) row[x] = '─'
    row[xb + 1] = '◄'
    labelInside(row, xb, xa, label)
  }
  return { chars: row, style }
}

/** Write the label on the arrow's shaft, leaving the ends visible. */
function labelInside(row: string[], lo: number, hi: number, label: string) {
  const room = hi - lo - 5
  if (room < 4) return
  const text = fit(` ${label} `, Math.min(room, [...label].length + 2)).trimEnd()
  put(row, lo + 3, text, hi - 2)
}
