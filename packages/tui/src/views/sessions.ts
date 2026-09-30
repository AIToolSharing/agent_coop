// The sessions pane: every session with its live numbers, and the "all traffic" entry.
import { ALL_SESSIONS, derive, type Store } from '../model.js'
import { fitLine, type Line, type Rendered, seg } from './line.js'

export interface SessionSummary {
  readonly sid: string
  readonly status: 'open' | 'closed' | 'all'
  readonly online: number
  readonly agents: number
  readonly openAsks: number
  /** Messages in the last minute. */
  readonly perMinute: number
}

export function summarize(store: Store, now: number): SessionSummary[] {
  const minuteAgo = new Date(now - 60_000).toISOString()
  const one = (sid: string, status: SessionSummary['status']): SessionSummary => {
    const d = derive(store, sid)
    return {
      sid,
      status,
      online: d.agents.filter((a) => a.online).length,
      agents: d.agents.length,
      openAsks: d.openAsks.length,
      perMinute: d.messages.filter((m) => m.sent_at >= minuteAgo).length,
    }
  }
  const list = [...store.sessions.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([sid, r]) => one(sid, r.status))
  return [one(ALL_SESSIONS, 'all'), ...list]
}

export function renderSessions(
  list: readonly SessionSummary[],
  selected: string,
  width: number,
): Rendered {
  const lines: Line[] = list.map((s) => {
    const sel = s.sid === selected
    if (s.status === 'all') {
      return fitLine(
        [
          seg('* all traffic', { bold: true, inverse: sel }),
          seg(`  ${s.perMinute}/min`, { dim: true }),
        ],
        width,
      )
    }
    const icon = s.status === 'closed' ? '✕ ' : s.online > 0 ? '● ' : '○ '
    return fitLine(
      [
        seg(icon, { color: s.status === 'closed' ? 'gray' : s.online > 0 ? 'green' : 'gray' }),
        seg(s.sid, { bold: true, inverse: sel }),
        seg(` ${s.online}/${s.agents}`, { dim: true }),
        s.openAsks > 0
          ? seg(` ${s.openAsks} ask${s.openAsks > 1 ? 's' : ''}`, { color: 'yellow' })
          : seg(''),
        s.perMinute > 0 ? seg(` ${s.perMinute}/min`, { dim: true }) : seg(''),
        s.status === 'closed' ? seg(' closed', { dim: true }) : seg(''),
      ],
      width,
    )
  })
  return { lines, ids: list.map((s) => s.sid) }
}
