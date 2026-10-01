// The Ink shell: panes, keys, mouse, prompts. It paints what the views return and calls the
// operator.
//
// Keys can arrive faster than a repaint (paste, key repeat, scripts). So all UI state is one
// object in a ref: each key reads the latest state and writes the next state at once, and
// anything derived from the state is computed again from that latest state.
import {
  type Address,
  BROADCAST,
  isToken,
  MAX_TEXT,
  OPERATOR,
  parseAddress,
  type To,
} from '@coop/core'
import { Box, Text, useApp, useInput, useWindowSize } from 'ink'
import { useEffect, useRef, useState } from 'react'
import { ALL_SESSIONS, type Derived, derive, type Store } from './model.js'
import type { ViewOptions } from './views/common.js'
import { renderAgent, renderAgents, renderMessage } from './views/inspect.js'
import { fitLine, type Line, type Rendered, seg } from './views/line.js'
import { renderLog } from './views/log.js'
import { renderMatrix } from './views/matrix.js'
import { renderSequence } from './views/sequence.js'
import { renderSessions, type SessionSummary, summarize } from './views/sessions.js'
import { renderThreads } from './views/threads.js'

/** The operator actions the TUI needs. `Operator` from @coop/core/broker has them. */
export interface OperatorApi {
  createSession(sid: string): Promise<void>
  closeSession(sid: string): Promise<void>
  reopenSession(sid: string): Promise<void>
  deleteSession(sid: string): Promise<void>
  kick(sid: string, target: Address): Promise<unknown>
  unkick(sid: string, target: Address): Promise<unknown>
  redact(sid: string, id: string): Promise<boolean>
  send(sid: string, to: To, text: string, reply_to?: string): Promise<string>
}

export interface AppProps {
  readonly store: Store
  readonly subscribe: (onChange: () => void) => () => void
  readonly op: OperatorApi
  readonly now?: () => number
}

const VIEWS = ['log', 'sequence', 'threads', 'matrix', 'agent', 'message'] as const
type View = (typeof VIEWS)[number]
type Focus = 'sessions' | 'view' | 'agents'
type Mode =
  | { readonly kind: 'normal' }
  | {
      readonly kind: 'input'
      readonly purpose: 'send' | 'new' | 'search'
      readonly value: string
      /** For a send: the message this one answers, and its sender. */
      readonly reply?: { readonly id: string; readonly to: string }
    }
  | { readonly kind: 'confirm'; readonly text: string; readonly run: () => Promise<string> }

interface Ui {
  readonly sid: string
  readonly view: View
  readonly focus: Focus
  readonly msgId: string | undefined
  readonly agentAddr: string | undefined
  readonly follow: boolean
  readonly system: boolean
  readonly agentFilter: string | undefined
  readonly search: string
  /** First visible line of the view when the operator scrolled; undefined follows the cursor. */
  readonly scroll: number | undefined
  readonly help: boolean
  readonly mode: Mode
  readonly status: string
}

const INITIAL: Ui = {
  sid: ALL_SESSIONS,
  view: 'log',
  focus: 'view',
  msgId: undefined,
  agentAddr: undefined,
  follow: true,
  system: true,
  agentFilter: undefined,
  search: '',
  scroll: undefined,
  help: false,
  mode: { kind: 'normal' },
  status: '',
}

const SESSIONS_W = 30
/** Rows under the panes: the prompt or status line, and two lines of key hints. */
const HINT_H = 2
const NEXT_FOCUS: Record<Focus, Focus> = { view: 'agents', agents: 'sessions', sessions: 'view' }
const PREV_FOCUS: Record<Focus, Focus> = { agents: 'view', sessions: 'agents', view: 'sessions' }
const WHEEL_LINES = 3

/** SGR mouse reports, as Ink hands them to useInput (the leading escape is stripped). */
const MOUSE_RE = /\[<(\d+);(\d+);(\d+)([Mm])/g

interface Screen {
  readonly d: Derived
  readonly sessions: readonly SessionSummary[]
  readonly rendered: Rendered
  /** The message under the cursor: the newest one while following the log or sequence. */
  readonly current: string | undefined
  readonly selectable: readonly string[]
  readonly following: boolean
}

function screen(store: Store, u: Ui, width: number, now: number): Screen {
  const d = derive(store, u.sid)
  const opts: ViewOptions = {
    width,
    now,
    agent: u.agentFilter,
    search: u.search,
    system: u.system,
    selected: u.view === 'agent' ? u.agentAddr : u.msgId,
  }
  const rendered = (() => {
    if (u.help) return helpLines(width)
    switch (u.view) {
      case 'log':
        return renderLog(d, opts)
      case 'sequence':
        return renderSequence(d, opts)
      case 'threads':
        return renderThreads(d, opts)
      case 'matrix':
        return renderMatrix(d, opts)
      case 'agent':
        return renderAgent(d, u.agentAddr, opts)
      case 'message':
        return renderMessage(d, u.msgId, opts)
    }
  })()
  const selectable = rendered.ids.flatMap((id) => (id === undefined ? [] : [id]))
  const following = u.follow && (u.view === 'log' || u.view === 'sequence') && !u.help
  const current = following ? selectable.at(-1) : u.msgId
  return { d, sessions: summarize(store, now), rendered, current, selectable, following }
}

/** The first visible line of the view pane. */
function viewOffset(u: Ui, s: Screen, bodyH: number): number {
  const max = Math.max(0, s.rendered.lines.length - bodyH)
  if (s.following) return max
  if (u.scroll !== undefined) return Math.min(max, Math.max(0, u.scroll))
  const cursorLine = s.rendered.ids.indexOf(s.current)
  if (cursorLine < 0) return 0
  return Math.min(max, Math.max(0, cursorLine - bodyH + 1))
}

/** The first visible row of a list pane, so that the selected row stays in view. */
function listOffset(index: number, length: number, height: number): number {
  if (index < 0 || length <= height) return 0
  return Math.min(length - height, Math.max(0, index - height + 1))
}

/** Where a scrolled window must start so that `line` is inside it; `offset` if it already is. */
function reveal(line: number, offset: number, bodyH: number): number {
  if (line < offset) return line
  if (line >= offset + bodyH) return line - bodyH + 1
  return offset
}

const HELP: readonly (readonly [string, string])[] = [
  ['NAVIGATION', ''],
  ['tab / shift+tab', 'move the focus: view → agents → sessions'],
  ['↑ ↓', 'move the cursor in the focused pane'],
  ['pgup pgdn home end', 'scroll the view; end follows the newest message again'],
  ['enter', 'open what the cursor is on: a session, an agent (view 5), a message (view 6)'],
  ['mouse', 'click selects, a second click opens, the wheel scrolls'],
  ['1 2 3 4 5 6', 'views: log, sequence, threads, matrix, agent, message'],
  ['space', 'follow the newest message on or off (log, sequence)'],
  ['FILTERS', ''],
  ['/', 'search in message text; an empty search clears'],
  ['f', 'only messages of one agent; press again for the next agent, then off'],
  ['s', 'show or hide system lines: joins, leaves, states, waits'],
  ['esc', 'clear the search and the agent filter'],
  ['SESSIONS', ''],
  ['n', 'new session'],
  ['c', 'close the session (its agents are disconnected), or reopen a closed one'],
  ['D', 'delete a closed session with all its messages'],
  ['ACTIONS', ''],
  ['m', 'message all agents of the session; "@agent text" for one agent'],
  ['a', 'answer the selected message: to its sender, in its thread'],
  ['k', 'remove the selected agent from its session; on a removed agent: allow it back'],
  ['r', 'withdraw the selected message'],
  ['q', 'quit'],
  ['?', 'this help'],
]

function helpLines(width: number): Rendered {
  const lines: Line[] = HELP.map(([key, text]) =>
    text === ''
      ? fitLine([seg(key, { bold: true })], width)
      : fitLine([seg(`  ${key.padEnd(20)}`, { color: 'cyan' }), seg(text)], width),
  )
  return { lines, ids: lines.map(() => undefined) }
}

function hint(u: Ui): string {
  if (u.help) return '? or esc  close help'
  if (u.mode.kind === 'input') return 'enter  send · esc  cancel'
  if (u.mode.kind === 'confirm') return 'y  yes · n  no'
  switch (u.focus) {
    case 'sessions':
      return '↑↓ select session · enter show it · n new · c close/reopen · D delete · tab next pane · ? help · q quit'
    case 'agents':
      return '↑↓ select agent · enter details · k remove/allow back · m message · tab next pane · ? help · q quit'
    case 'view':
      return '↑↓ select message · enter details · pgup/pgdn scroll · space follow · / search · f agent filter · s system lines · m message · a answer · r withdraw · 1-6 views · tab next pane · ? help · q quit'
  }
}

export function App({ store, subscribe, op, now = Date.now }: AppProps) {
  const { exit } = useApp()
  const { columns, rows } = useWindowSize()
  const [, setVersion] = useState(store.version)
  const [ui, setUiState] = useState<Ui>(INITIAL)
  const ref = useRef<Ui>(INITIAL)
  const set = (patch: Partial<Ui>) => {
    ref.current = { ...ref.current, ...patch }
    setUiState(ref.current)
  }

  useEffect(() => subscribe(() => setVersion(store.version)), [store, subscribe])
  // A clock tick keeps ages and waits current.
  useEffect(() => {
    const t = setInterval(() => setVersion((v) => v + 1), 1000)
    return () => clearInterval(t)
  }, [])

  const viewW = Math.max(20, columns - SESSIONS_W - 4)
  const t = now()
  const s = screen(store, ui, viewW, t)
  const agentsH = Math.min(8, Math.max(3, s.d.agents.length + 2))
  const mainH = Math.max(6, rows - agentsH - 1 - HINT_H)
  const bodyH = mainH - 3
  const agentsBodyH = agentsH - 2
  const agentsPane = renderAgents(s.d.agents, {
    width: columns - 2,
    now: t,
    selected: ui.agentAddr,
  })
  const agentsOffset = listOffset(
    s.d.agents.findIndex((a) => a.address === ui.agentAddr),
    s.d.agents.length,
    agentsBodyH,
  )
  const sessionsPane = renderSessions(s.sessions, ui.sid, SESSIONS_W - 2)
  const sessionsOffset = listOffset(
    s.sessions.findIndex((x) => x.sid === ui.sid),
    s.sessions.length,
    bodyH,
  )
  const offset = viewOffset(ui, s, bodyH)
  const visible = s.rendered.lines.slice(offset, offset + bodyH)

  const act = (p: Promise<string>) =>
    p.then(
      (status) => set({ status }),
      (err: unknown) =>
        set({ status: `error: ${err instanceof Error ? err.message : String(err)}` }),
    )

  /** Show the view from `line`, and stop following. */
  const scrollTo = (line: number) => set({ scroll: Math.max(0, line), follow: false })

  /** Move the view window by `delta` lines; at the bottom of a live view, follow again. */
  const scrollBy = (u: Ui, cur: Screen, delta: number, live: boolean) => {
    const max = Math.max(0, cur.rendered.lines.length - bodyH)
    const next = Math.min(max, Math.max(0, viewOffset(u, cur, bodyH) + delta))
    const canFollow = u.view === 'log' || u.view === 'sequence'
    if (live && canFollow && next >= max && delta > 0)
      return set({ follow: true, scroll: undefined })
    scrollTo(next)
  }

  const selectMessage = (u: Ui, cur: Screen, id: string) => {
    const line = cur.rendered.ids.indexOf(id)
    const off = viewOffset(u, cur, bodyH)
    set({ msgId: id, follow: false, scroll: line < 0 ? u.scroll : reveal(line, off, bodyH) })
  }

  const openSelected = (u: Ui, cur: Screen) => {
    if (u.focus === 'agents' && u.agentAddr !== undefined)
      return set({ view: 'agent', scroll: undefined, help: false })
    if (u.focus === 'sessions') return set({ focus: 'view' })
    if (cur.current !== undefined)
      return set({ msgId: cur.current, follow: false, view: 'message', scroll: undefined })
  }

  const chooseSession = (u: Ui, cur: Screen, index: number) => {
    const next = cur.sessions[index]
    if (next === undefined) return
    if (next.sid === u.sid) return set({ focus: 'sessions' })
    set({
      sid: next.sid,
      focus: 'sessions',
      msgId: undefined,
      agentAddr: undefined,
      agentFilter: undefined,
      follow: true,
      scroll: undefined,
      ...(u.view === 'agent' || u.view === 'message' ? { view: 'log' as View } : {}),
    })
  }

  const mouse = (u: Ui, cur: Screen, input: string): boolean => {
    let handled = false
    for (const m of input.matchAll(MOUSE_RE)) {
      handled = true
      const button = Number(m[1])
      const x = Number(m[2])
      const y = Number(m[3])
      if (m[4] !== 'M') continue // a release
      const wheel = button === 64 ? -WHEEL_LINES : button === 65 ? WHEEL_LINES : 0
      const click = (button & 3) === 0 && button < 32
      if (!wheel && !click) continue
      // Panes, in terminal rows: the main row (sessions | view) spans 1..mainH with a border
      // row, a title row and body lines from row 3; the agents pane follows with a border row.
      const inMain = y >= 1 && y <= mainH
      const inAgents = y > mainH && y <= mainH + agentsH
      if (inMain && x <= SESSIONS_W) {
        if (wheel) {
          const i = cur.sessions.findIndex((z) => z.sid === u.sid)
          chooseSession(u, cur, clamp(i + Math.sign(wheel), cur.sessions.length))
        } else chooseSession(u, cur, y - 3 + sessionsOffset)
        u = ref.current
        continue
      }
      if (inMain) {
        if (wheel) {
          scrollBy(u, cur, wheel, true)
          u = ref.current
          continue
        }
        const line = y - 3 + viewOffset(u, cur, bodyH)
        const id = cur.rendered.ids[line]
        const focus: Partial<Ui> = u.focus === 'view' ? {} : { focus: 'view' }
        if (id === undefined || u.help) set(focus)
        else if (id === u.msgId && !cur.following && u.view !== 'message') {
          set({ ...focus, view: 'message', scroll: undefined })
        } else {
          set(focus)
          selectMessage(ref.current, cur, id)
        }
        u = ref.current
        continue
      }
      if (inAgents) {
        const list = cur.d.agents.map((a) => a.address)
        if (list.length === 0) continue
        if (wheel) {
          const i = u.agentAddr === undefined ? -1 : list.indexOf(u.agentAddr)
          set({ focus: 'agents', agentAddr: list[clamp(i + Math.sign(wheel), list.length)] })
        } else {
          const addr = list[y - mainH - 2 + agentsOffset]
          if (addr === undefined) set({ focus: 'agents' })
          else if (addr === u.agentAddr && u.focus === 'agents')
            set({ view: 'agent', scroll: undefined, help: false })
          else set({ focus: 'agents', agentAddr: addr })
        }
        u = ref.current
      }
    }
    return handled
  }

  useInput((input, key) => {
    let u = ref.current
    const cur = screen(store, u, viewW, now())
    const session = u.sid === ALL_SESSIONS ? undefined : u.sid

    if (input.includes('[<') && mouse(u, cur, input)) return
    u = ref.current

    if (u.mode.kind === 'confirm') {
      const run = u.mode.run
      set({ mode: { kind: 'normal' }, status: input === 'y' ? '…' : 'cancelled' })
      if (input === 'y') act(run())
      return
    }
    if (u.mode.kind === 'input') {
      const m = u.mode
      if (key.escape) return set({ mode: { kind: 'normal' } })
      if (key.return) {
        set({ mode: { kind: 'normal' } })
        return submit(m.purpose, m.value.trim(), cur, m.reply)
      }
      if (key.backspace || key.delete)
        return set({ mode: { ...m, value: [...m.value].slice(0, -1).join('') } })
      if (!key.ctrl && !key.meta && input !== '') set({ mode: { ...m, value: m.value + input } })
      return
    }
    if (u.help) {
      if (input === '?' || key.escape || input === 'q')
        return set({ help: false, scroll: undefined })
      if (key.pageDown) return scrollBy(u, cur, bodyH - 1, false)
      if (key.pageUp) return scrollBy(u, cur, -(bodyH - 1), false)
      if (key.downArrow) return scrollBy(u, cur, 1, false)
      if (key.upArrow) return scrollBy(u, cur, -1, false)
      return
    }

    if (input === 'q') return exit()
    if (input === '?') return set({ help: true, scroll: 0 })
    if (key.tab) return set({ focus: key.shift ? PREV_FOCUS[u.focus] : NEXT_FOCUS[u.focus] })
    if (key.upArrow || key.downArrow) {
      const delta = key.upArrow ? -1 : 1
      if (u.focus === 'sessions') {
        const i = cur.sessions.findIndex((x) => x.sid === u.sid)
        return chooseSession(u, cur, clamp(i + delta, cur.sessions.length))
      }
      if (u.focus === 'agents') {
        const list = cur.d.agents.map((a) => a.address)
        const i = u.agentAddr === undefined ? -1 : list.indexOf(u.agentAddr)
        return set({ agentAddr: list[clamp(i + delta, list.length)] })
      }
      // A view without selectable lines (matrix, details) scrolls by line.
      if (cur.selectable.length === 0) return scrollBy(u, cur, delta, false)
      const i =
        cur.current === undefined ? cur.selectable.length : cur.selectable.indexOf(cur.current)
      const id = cur.selectable[clamp(i + delta, cur.selectable.length)]
      if (id !== undefined) selectMessage(u, cur, id)
      return
    }
    if (key.pageDown || key.pageUp) {
      const delta = key.pageDown ? bodyH - 1 : -(bodyH - 1)
      scrollBy(u, cur, delta, false)
      // Keep the cursor inside the window: the first or last message that is visible.
      const off = viewOffset(ref.current, cur, bodyH)
      const inWindow = cur.rendered.ids
        .slice(off, off + bodyH)
        .flatMap((id) => (id === undefined ? [] : [id]))
      const pick = key.pageDown ? inWindow.at(-1) : inWindow[0]
      if (pick !== undefined && u.focus === 'view') set({ msgId: pick })
      return
    }
    if (key.home) {
      set({ scroll: 0, follow: false })
      if (cur.selectable[0] !== undefined && u.focus === 'view') set({ msgId: cur.selectable[0] })
      return
    }
    if (key.end) {
      if (u.view === 'log' || u.view === 'sequence') return set({ follow: true, scroll: undefined })
      return scrollBy(u, cur, cur.rendered.lines.length, false)
    }
    const n = Number(input)
    if (Number.isInteger(n) && n >= 1 && n <= VIEWS.length)
      return set({ view: VIEWS[n - 1] ?? 'log', scroll: undefined })
    if (key.return) return openSelected(u, cur)
    if (key.escape) return set({ search: '', agentFilter: undefined, status: 'filters cleared' })
    switch (input) {
      case ' ':
        return set({
          follow: !u.follow,
          scroll: undefined,
          status: u.follow ? 'follow off' : 'follow on',
        })
      case 's':
        return set({
          system: !u.system,
          status: u.system ? 'system lines hidden' : 'system lines shown',
        })
      case 'f': {
        const list = [undefined, ...cur.d.agents.map((a) => a.address)]
        const next = list[(list.indexOf(u.agentFilter) + 1) % list.length]
        return set({
          agentFilter: next,
          status: next === undefined ? 'filter off' : `only messages of ${next}`,
        })
      }
      case '/':
        return set({ mode: { kind: 'input', purpose: 'search', value: u.search } })
      case 'n':
        return set({ mode: { kind: 'input', purpose: 'new', value: '' } })
      case 'm':
        if (session === undefined)
          return set({ status: 'select a session first (tab to sessions)' })
        return set({ mode: { kind: 'input', purpose: 'send', value: '' } })
      case 'a': {
        const m = cur.d.messages.find((x) => x.id === cur.current)
        if (m === undefined) return set({ status: 'select a message first' })
        if (m.from === OPERATOR) return set({ status: 'that is your own message' })
        const reply = { id: m.id, to: m.from }
        return set({ mode: { kind: 'input', purpose: 'send', value: '', reply } })
      }
      case 'c': {
        const rec = session === undefined ? undefined : store.sessions.get(session)
        if (session === undefined || rec === undefined)
          return set({ status: 'select a session first' })
        if (rec.status !== 'open')
          return act(op.reopenSession(session).then(() => `reopened ${session}`))
        return set({
          mode: {
            kind: 'confirm',
            text: `close ${session}? its agents are disconnected (y/n)`,
            run: () => op.closeSession(session).then(() => `closed ${session}`),
          },
        })
      }
      case 'D': {
        if (session === undefined) return set({ status: 'select a session first' })
        if (store.sessions.get(session)?.status !== 'closed')
          return set({ status: 'close the session first (c)' })
        return set({
          mode: {
            kind: 'confirm',
            text: `delete session ${session} and all its messages? (y/n)`,
            run: () => op.deleteSession(session).then(() => `deleted ${session}`),
          },
        })
      }
      case 'k': {
        const target = u.agentAddr === undefined ? undefined : parseAddress(u.agentAddr)
        const row = cur.d.agents.find((a) => a.address === u.agentAddr)
        if (target === undefined || row === undefined)
          return set({ status: 'select an agent first (tab to agents)' })
        if (row.kicked) {
          return set({
            mode: {
              kind: 'confirm',
              text: `allow ${u.agentAddr} back into ${row.sid}? (y/n)`,
              run: () => op.unkick(row.sid, target).then(() => `${u.agentAddr} may join again`),
            },
          })
        }
        return set({
          mode: {
            kind: 'confirm',
            text: `remove ${u.agentAddr} from ${row.sid}? (y/n)`,
            run: () => op.kick(row.sid, target).then(() => `removed ${u.agentAddr}`),
          },
        })
      }
      case 'r': {
        const m = cur.d.messages.find((x) => x.id === cur.current)
        if (m === undefined) return set({ status: 'select a message first' })
        return set({
          mode: {
            kind: 'confirm',
            text: `withdraw message #${m.id} from ${m.from}? (y/n)`,
            run: () =>
              op
                .redact(m.sid, m.id)
                .then((ok) => (ok ? `withdrew #${m.id}` : `#${m.id} is not in ${m.sid}`)),
          },
        })
      }
    }
  })

  function submit(
    purpose: 'send' | 'new' | 'search',
    value: string,
    cur: Screen,
    reply?: { id: string; to: string },
  ) {
    if (purpose === 'search')
      return set({ search: value, status: value === '' ? 'search cleared' : `search: ${value}` })
    if (purpose === 'new') {
      if (!isToken(value)) return set({ status: 'a session name uses a-z, 0-9, _ and -, up to 64' })
      return act(
        op.createSession(value).then(() => {
          set({ sid: value, scroll: undefined })
          return `created ${value}`
        }),
      )
    }
    const session = ref.current.sid === ALL_SESSIONS ? undefined : ref.current.sid
    if (session === undefined || value === '') return
    const m = value.match(/^@(\S+)\s+([\s\S]+)$/)
    let to: To = BROADCAST
    let text = value
    let away: string | undefined
    /** Address one agent of the session. False if there is no such agent. */
    const direct = (address: string) => {
      const hit = cur.d.agents.find((a) => a.address === address)
      const addr = parseAddress(address)
      if (hit === undefined || addr === undefined) return false
      to = addr
      if (!hit.online) away = hit.address
      return true
    }
    if (reply !== undefined) {
      if (!direct(reply.to)) return set({ status: `no agent ${reply.to}` })
    } else if (m !== null) {
      const name = m[1] ?? ''
      const hits = cur.d.agents.filter(
        (a) => a.address === name || a.address.split('@')[0] === name,
      )
      if (hits.length !== 1)
        return set({ status: hits.length === 0 ? `no agent ${name}` : `${name} is ambiguous` })
      if (!direct(hits[0]?.address ?? '')) return
      text = m[2] ?? ''
    }
    if ([...text].length > MAX_TEXT) return set({ status: `too long (max ${MAX_TEXT})` })
    // The hub keeps a message for an agent that left; the agent gets it when it joins again.
    const note = away === undefined ? '' : ` (${away} is away; it gets it when it returns)`
    return act(op.send(session, to, text, reply?.id).then((id) => `sent #${id}${note}`))
  }

  const title = ui.help
    ? 'help'
    : `${VIEWS.indexOf(ui.view) + 1} ${ui.view}: ${ui.sid === ALL_SESSIONS ? 'all sessions' : ui.sid}${
        ui.agentFilter ? `  [${ui.agentFilter}]` : ''
      }${ui.search ? `  [/${ui.search}]` : ''}${s.following ? '  follow ●' : ''}${
        store.link === 'live' ? '' : `  [${store.link}]`
      }`
  const prompt =
    ui.mode.kind === 'input'
      ? `${
          ui.mode.purpose === 'send'
            ? ui.mode.reply === undefined
              ? 'to all (or @name text)'
              : `reply to #${ui.mode.reply.id} from ${ui.mode.reply.to}`
            : ui.mode.purpose === 'new'
              ? 'new session name'
              : 'search'
        } › ${ui.mode.value}▏`
      : ui.mode.kind === 'confirm'
        ? ui.mode.text
        : ui.status

  return (
    <Box flexDirection="column" width={columns} height={rows}>
      <Box flexDirection="row" height={mainH}>
        <Box
          width={SESSIONS_W}
          flexDirection="column"
          borderStyle="single"
          borderColor={ui.focus === 'sessions' ? 'cyan' : 'gray'}
        >
          <Text bold>Sessions</Text>
          {sessionsPane.lines.slice(sessionsOffset, sessionsOffset + bodyH).map((l, i) => (
            <Paint key={sessionsPane.ids[sessionsOffset + i] ?? i} line={l} />
          ))}
        </Box>
        <Box
          flexGrow={1}
          flexDirection="column"
          borderStyle="single"
          borderColor={ui.focus === 'view' ? 'cyan' : 'gray'}
        >
          <Text bold wrap="truncate">
            {title}
          </Text>
          {visible.map((l, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a line's position in the view is its identity
            <Paint key={offset + i} line={l} />
          ))}
        </Box>
      </Box>
      <Box
        height={agentsH}
        flexDirection="column"
        borderStyle="single"
        borderColor={ui.focus === 'agents' ? 'cyan' : 'gray'}
      >
        {agentsPane.lines.slice(agentsOffset, agentsOffset + agentsBodyH).map((l, i) => (
          <Paint key={agentsPane.ids[agentsOffset + i] ?? i} line={l} />
        ))}
      </Box>
      <Text wrap="truncate" color={ui.mode.kind === 'normal' ? 'gray' : 'cyan'}>
        {prompt || ' '}
      </Text>
      <Box height={HINT_H}>
        <Text wrap="wrap" dimColor>
          {hint(ui)}
        </Text>
      </Box>
    </Box>
  )
}

function clamp(i: number, length: number): number {
  return Math.min(length - 1, Math.max(0, i))
}

function Paint({ line }: { line: Line }) {
  // A segment's key is its character offset in the line: unique and stable.
  const offsets = line.reduce<number[]>((acc, _s, i) => {
    acc.push(i === 0 ? 0 : (acc[i - 1] ?? 0) + [...(line[i - 1]?.text ?? '')].length)
    return acc
  }, [])
  return (
    <Text wrap="truncate">
      {line.map((s, i) => (
        <Text
          key={offsets[i]}
          {...(s.color === undefined ? {} : { color: s.color })}
          dimColor={s.dim === true}
          bold={s.bold === true}
          inverse={s.inverse === true}
        >
          {s.text}
        </Text>
      ))}
    </Text>
  )
}
