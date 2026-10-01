// The sidebar: every session, then the agents of the selected session. One cursor moves over
// both lists; the shell maps a row back to a session or an agent through `rows`.
import { type AgentRow, ALL_SESSIONS, derive, type Store } from '../model.js'
import { age, fit, fitLine, type Line, type Rendered, STATE_COLOR, seg } from './line.js'

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

export type SidebarRow =
  | { readonly kind: 'session'; readonly sid: string }
  | { readonly kind: 'agent'; readonly address: string }

export interface Sidebar extends Rendered {
  /** What each line stands for; headers and blanks are undefined. */
  readonly rows: readonly (SidebarRow | undefined)[]
}

export interface SidebarSelection {
  /** The shown session. */
  readonly sid: string
  /** The row under the cursor, when the sidebar has the focus. */
  readonly cursor: number | undefined
}

/** The narrowest width that shows every row whole, within `max`. */
export function sidebarWidth(
  sessions: readonly SessionSummary[],
  agents: readonly AgentRow[],
  max: number,
): number {
  // A session row: icon, name, counts. An agent row: icon, name, state, wait marker.
  const longest = Math.max(
    14,
    ...sessions.map((s) => s.sid.length + 14),
    ...agents.map((a) => a.address.length + 18),
  )
  return Math.min(Math.max(max, 26), longest)
}

export function renderSidebar(
  sessions: readonly SessionSummary[],
  agents: readonly AgentRow[],
  sel: SidebarSelection,
  width: number,
  now: number,
): Sidebar {
  const lines: Line[] = []
  const rows: (SidebarRow | undefined)[] = []
  const add = (l: Line, row?: SidebarRow) => {
    lines.push(fitLine(l, width))
    rows.push(row)
  }
  add([seg('SESSIONS', { bold: true, dim: true })])
  for (const s of sessions) {
    const here = rows.length === sel.cursor
    const current = s.sid === sel.sid
    if (s.status === 'all') {
      add(
        [
          seg('* ', { dim: true }),
          seg('all traffic', { bold: current, inverse: here }),
          seg(s.perMinute > 0 ? `  ${s.perMinute}/min` : '', { dim: true }),
        ],
        { kind: 'session', sid: s.sid },
      )
      continue
    }
    const live = s.status === 'open' && s.online > 0
    add(
      [
        seg(s.status === 'closed' ? '✕ ' : live ? '● ' : '○ ', { color: live ? 'green' : 'gray' }),
        seg(s.sid, { bold: current, inverse: here }),
        seg(` ${s.online}/${s.agents}`, { dim: true }),
        s.openAsks > 0
          ? seg(` ${s.openAsks} ask${s.openAsks > 1 ? 's' : ''}`, { color: 'yellow' })
          : seg(''),
        s.status === 'closed' ? seg(' closed', { dim: true }) : seg(''),
      ],
      { kind: 'session', sid: s.sid },
    )
  }
  if (sel.sid !== ALL_SESSIONS) {
    add([seg('')])
    add([seg(`AGENTS · ${sel.sid}`, { bold: true, dim: true })])
    if (agents.length === 0) add([seg('  none yet', { dim: true })])
    for (const a of agents) {
      const here = rows.length === sel.cursor
      const state = a.kicked
        ? seg('removed', { color: 'red' })
        : seg(a.state, { color: STATE_COLOR[a.state] })
      // Icon, name, a space, the state, and room for the wait marker when there is one.
      const nameW = Math.max(6, width - 3 - state.text.length - (a.waiting ? 6 : 0))
      add(
        [
          seg(a.online ? '● ' : '○ ', { color: a.online ? 'green' : 'gray' }),
          seg(fit(a.address, nameW).trimEnd(), { bold: true, inverse: here }),
          seg(' '),
          state,
          a.waiting ? seg(` ⏳${age(a.waiting.since, now)}`, { color: 'yellow' }) : seg(''),
        ],
        { kind: 'agent', address: a.address },
      )
    }
  }
  return {
    lines,
    ids: rows.map((r) => (r === undefined ? undefined : r.kind === 'session' ? r.sid : r.address)),
    rows,
  }
}
