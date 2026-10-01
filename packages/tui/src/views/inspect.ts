// Views 5 and 6: details of one agent, and of one message.
import type { Derived } from '../model.js'
import { senderColor, ticks, type ViewOptions } from './common.js'
import {
  age,
  clock,
  fitLine,
  type Line,
  note,
  type Rendered,
  STATE_COLOR,
  seg,
  stamp,
  wrap,
} from './line.js'

function kv(key: string, value: string, style = {}): Line {
  return [seg(`${key.padEnd(12)} `, { dim: true }), seg(value, style)]
}

export function renderAgent(d: Derived, address: string | undefined, o: ViewOptions): Rendered {
  const a = d.agents.find((x) => x.address === address)
  if (a === undefined) {
    return note('select an agent (tab to the agents pane, then enter)', o.width)
  }
  const lines: Line[] = []
  const ids: (string | undefined)[] = []
  const add = (l: Line, id?: string) => {
    lines.push(fitLine(l, o.width))
    ids.push(id)
  }
  add([
    seg(a.address, { bold: true }),
    seg(a.online ? '  ● online' : '  ○ offline', { color: a.online ? 'green' : 'gray' }),
  ])
  if (a.kicked) {
    add(
      kv('removed', 'by the operator; it cannot join until you allow it back (k)', {
        color: 'red',
      }),
    )
  }
  add(
    kv('state', `${a.state}${a.stateSince ? ` for ${age(a.stateSince, o.now)}` : ''}`, {
      color: STATE_COLOR[a.state],
    }),
  )
  if (a.note) add(kv('note', a.note))
  if (a.waiting) {
    add(
      kv(
        'waiting',
        `${a.waiting.on ? `on ${a.waiting.on}` : 'for any message'}${a.waiting.reply_to ? ` (ask #${a.waiting.reply_to})` : ''} for ${age(a.waiting.since, o.now)}`,
        { color: 'yellow' },
      ),
    )
  }
  add(kv('host', a.host ?? '?'))
  add(kv('directory', a.cwd ?? '?'))
  add(kv('client', a.client ?? '?'))
  add(kv('joined', a.joinedAt ? `${clock(a.joinedAt)} (${age(a.joinedAt, o.now)} ago)` : '?'))
  if (a.left) add(kv('left', `${clock(a.left.at)} (${a.left.reason})`))
  add(kv('messages', `sent ${a.sent}, received ${a.received}, queued ${a.queued}`))
  if (a.via !== undefined) {
    add(
      kv(
        'receives',
        a.via === 'push'
          ? 'push: a message goes into its session; the agent reads it on its next turn'
          : 'poll: the agent fetches messages with wait or inbox (no push)',
        a.via === 'push' ? {} : { color: 'yellow' },
      ),
    )
  }
  const lat = d.messages.flatMap((m) => {
    const x = m.deliveries.get(a.address)
    return x === undefined ? [] : [x.ms]
  })
  if (lat.length > 0) {
    add(
      kv(
        'reach time',
        `median ${Math.round([...lat].sort((p, q) => p - q)[Math.floor(lat.length / 2)] ?? 0)} ms over ${lat.length}`,
      ),
    )
  }
  add([seg('')])
  add([seg('TIMELINE', { bold: true })])
  for (const it of d.timeline) {
    if (it.kind === 'sys') {
      if (it.who === a.address)
        add([seg(`${clock(it.at)}  `, { dim: true }), seg(it.text, { dim: true })])
      continue
    }
    const m = it.msg
    if (m.from === a.address) {
      add(
        [
          seg(`${clock(it.at)}  `, { dim: true }),
          seg('sent ', { color: 'cyan' }),
          seg(`#${m.id} → ${m.to}: `),
          seg(m.redacted ? '[withdrawn]' : m.text),
        ],
        m.id,
      )
    } else if (m.recipients.includes(a.address)) {
      const got = m.deliveries.get(a.address)
      add(
        [
          seg(`${clock(it.at)}  `, { dim: true }),
          seg(got ? `got (${got.via}, ${got.ms}ms) ` : 'pending ', {
            color: got ? 'green' : 'yellow',
          }),
          seg(`#${m.id} ← ${m.from}: `),
          seg(m.redacted ? '[withdrawn]' : m.text),
        ],
        m.id,
      )
    }
  }
  return { lines, ids }
}

export function renderMessage(d: Derived, id: string | undefined, o: ViewOptions): Rendered {
  const m = d.messages.find((x) => x.id === id)
  if (m === undefined) {
    return note('select a message (in the log, sequence or threads view, then enter)', o.width)
  }
  const byId = new Map(d.messages.map((x) => [x.id, x]))
  const lines: Line[] = []
  const ids: (string | undefined)[] = []
  const add = (l: Line, lid?: string) => {
    lines.push(fitLine(l, o.width))
    ids.push(lid)
  }
  add([seg(`#${m.id}`, { bold: true }), seg('  '), ticks(m)])
  add(kv('from', m.from, { color: senderColor(m.from), bold: true }))
  add(kv('to', m.to))
  add(kv('sent', `${stamp(m.sent_at)} (${age(m.sent_at, o.now)} ago)`))
  if (m.reply_to !== undefined) {
    const p = byId.get(m.reply_to)
    add(kv('answers', `#${m.reply_to}${p ? ` from ${p.from}: ${p.text}` : ''}`), m.reply_to)
  }
  add([seg('')])
  for (const t of m.redacted
    ? ['[withdrawn by the operator]']
    : wrap(m.text, Math.max(10, o.width - 2))) {
    add([seg(`  ${t}`, m.redacted ? { dim: true } : {})])
  }
  add([seg('')])
  add([seg('REACHED', { bold: true })])
  if (m.recipients.length === 0) add([seg('  no recipient was in the session', { dim: true })])
  for (const r of m.recipients) {
    const x = m.deliveries.get(r)
    add(
      x === undefined
        ? [seg(`  ◌ ${r}`, { color: 'yellow' }), seg('  pending', { dim: true })]
        : [
            seg(`  ✓ ${r}`, { color: 'green' }),
            seg(`  ${clock(x.at)}  ${x.ms} ms  via ${x.via}`, { dim: true }),
          ],
    )
  }
  if (m.replies.length > 0) {
    add([seg('')])
    add([seg(`REPLIES (${m.replies.length})`, { bold: true })])
    for (const r of m.replies) {
      const c = byId.get(r)
      if (c !== undefined)
        add(
          [seg(`  #${c.id} `, { dim: true }), seg(c.from, { bold: true }), seg(`: ${c.text}`)],
          c.id,
        )
    }
  }
  return { lines, ids }
}
