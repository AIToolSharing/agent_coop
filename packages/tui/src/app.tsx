// The Ink shell: a sidebar (sessions, then the agents of the shown session), a main pane
// (transcript, threads, insights, or the sequence diagram), an attention line, a composer, and
// a command line. It paints what the views return and calls the operator.
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
import { type AgentRow, ALL_SESSIONS, type Derived, derive, type Store } from './model.js'
import { type AttentionItem, attention, describe, renderAttention } from './views/attention.js'
import type { ViewOptions } from './views/common.js'
import { renderAgent, renderMessage } from './views/inspect.js'
import { fitLine, type Line, type Rendered, seg } from './views/line.js'
import { renderMatrix } from './views/matrix.js'
import { renderSequence } from './views/sequence.js'
import {
  renderSidebar,
  type SessionSummary,
  type Sidebar,
  sidebarWidth,
  summarize,
} from './views/sidebar.js'
import { renderThreads } from './views/threads.js'
import { renderTranscript } from './views/transcript.js'

/** The operator actions the TUI needs. The admin client has them. */
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

const VIEWS = ['transcript', 'threads', 'insights', 'sequence'] as const
type View = (typeof VIEWS)[number]
type Focus = 'sidebar' | 'main'
type Overlay =
  | { readonly kind: 'message'; readonly id: string }
  | { readonly kind: 'agent'; readonly address: string }
  | { readonly kind: 'help' }
type Mode =
  | { readonly kind: 'normal' }
  | {
      readonly kind: 'compose'
      /** `all`, or an address. */
      readonly to: string
      readonly reply: { readonly id: string; readonly to: string } | undefined
      readonly value: string
    }
  | { readonly kind: 'command'; readonly value: string }
  | { readonly kind: 'search'; readonly value: string }
  | { readonly kind: 'confirm'; readonly text: string; readonly run: () => Promise<string> }

interface Ui {
  readonly sid: string
  readonly view: View
  readonly focus: Focus
  readonly sidebar: boolean
  /** The sidebar row under the cursor. */
  readonly cursor: number | undefined
  readonly msgId: string | undefined
  readonly agentAddr: string | undefined
  readonly overlay: Overlay | undefined
  readonly follow: boolean
  readonly system: boolean
  readonly agentFilter: string | undefined
  readonly search: string
  /** First visible line of the main pane when the operator scrolled; undefined follows the cursor. */
  readonly scroll: number | undefined
  readonly mode: Mode
  readonly status: string
  /** Where `a` stands in the attention items. */
  readonly attn: number
}

const INITIAL: Ui = {
  sid: ALL_SESSIONS,
  view: 'transcript',
  focus: 'main',
  sidebar: true,
  cursor: undefined,
  msgId: undefined,
  agentAddr: undefined,
  overlay: undefined,
  follow: true,
  system: true,
  agentFilter: undefined,
  search: '',
  scroll: undefined,
  mode: { kind: 'normal' },
  status: '',
  attn: 0,
}

const WHEEL_LINES = 3
/** SGR mouse reports, as Ink hands them to useInput (the leading escape is stripped). */
const MOUSE_RE = /\[<(\d+);(\d+);(\d+)([Mm])/g

const COMMANDS = [
  'new',
  'close',
  'reopen',
  'delete',
  'kick',
  'allow',
  'withdraw',
  'filter',
  'sys',
  'seq',
  'help',
  'quit',
] as const

interface Screen {
  readonly d: Derived
  readonly sessions: readonly SessionSummary[]
  readonly sidebar: Sidebar
  /** Columns of the sidebar, border included; 0 when hidden. */
  readonly sidebarW: number
  readonly mainW: number
  readonly main: Rendered
  readonly attn: Line | undefined
  readonly items: readonly AttentionItem[]
  /** The message under the cursor: the newest one while following. */
  readonly current: string | undefined
  readonly selectable: readonly string[]
  readonly following: boolean
}

function screen(store: Store, u: Ui, columns: number, now: number): Screen {
  const d = derive(store, u.sid)
  const sessions = summarize(store, now)
  const sidebarW = u.sidebar
    ? sidebarWidth(sessions, d.agents, Math.max(22, Math.floor(columns / 3)))
    : 0
  const sidebar = renderSidebar(
    sessions,
    d.agents,
    { sid: u.sid, cursor: u.focus === 'sidebar' ? u.cursor : undefined },
    Math.max(1, sidebarW - 1),
    now,
  )
  const mainW = Math.max(20, columns - sidebarW)
  const items = attention(d, now)
  const attn = renderAttention(items, now)
  const opts: ViewOptions = {
    width: mainW,
    now,
    agent: u.agentFilter,
    search: u.search,
    system: u.system,
    selected: u.msgId,
  }
  const main = (() => {
    const o = u.overlay
    if (o?.kind === 'help') return helpLines(mainW)
    if (o?.kind === 'message') return renderMessage(d, o.id, opts)
    if (o?.kind === 'agent') return renderAgent(d, o.address, opts)
    switch (u.view) {
      case 'transcript':
        return renderTranscript(d, opts)
      case 'threads':
        return renderThreads(d, opts)
      case 'insights':
        return renderMatrix(d, opts)
      case 'sequence':
        return renderSequence(d, opts)
    }
  })()
  const selectable =
    u.overlay === undefined ? main.ids.flatMap((id) => (id === undefined ? [] : [id])) : []
  const following =
    u.follow && (u.view === 'transcript' || u.view === 'sequence') && u.overlay === undefined
  const current = following ? selectable.at(-1) : u.msgId
  return {
    d,
    sessions,
    sidebar,
    sidebarW,
    mainW,
    main,
    attn,
    items,
    current,
    selectable,
    following,
  }
}

/** The first visible line of the main pane. */
function viewOffset(u: Ui, s: Screen, bodyH: number): number {
  const max = Math.max(0, s.main.lines.length - bodyH)
  if (s.following) return max
  if (u.scroll !== undefined) return Math.min(max, Math.max(0, u.scroll))
  const line = s.main.ids.indexOf(s.current)
  if (line < 0) return 0
  return Math.min(max, Math.max(0, line - bodyH + 1))
}

/** The first visible row of a list, so that row `index` stays in view. */
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
  ['MOVE', ''],
  ['tab', 'focus the sidebar or the main pane'],
  ['↑ ↓  j k', 'move: in the sidebar over sessions and agents, in the main pane over messages'],
  ['enter', 'open what the cursor is on: a session, an agent, a message'],
  ['esc', 'close the details or the help; clear the search and the agent filter'],
  ['pgup pgdn home end', 'scroll the main pane; end follows the newest message again'],
  ['space', 'follow the newest message on or off'],
  ['[', 'hide or show the sidebar'],
  ['1 2 3', 'views: transcript, threads, insights'],
  ['mouse', 'click selects, a second click opens, the wheel scrolls'],
  ['WRITE', ''],
  ['m', 'message the session; tab picks the target; "@agent text" also works'],
  ['r', 'reply to the selected message, to its sender, in its thread'],
  ['alt+enter', 'a new line in the composer'],
  ['a', 'go to the next thing that needs you: a message for you, a stale ask, a blocked agent'],
  ['/', 'search in the message text; an empty search clears'],
  ['COMMANDS  (: then tab completes)', ''],
  [':new <name>', 'create a session'],
  [':close  :reopen  :delete', 'the shown session; delete needs a closed session'],
  [':kick <agent>  :allow <agent>', 'remove an agent from the session; let it back in'],
  [':withdraw [#id]', 'withdraw a message (default: the selected one)'],
  [':filter [agent]', 'only messages of one agent; without a name: off'],
  [':sys  :seq', 'system lines on or off; the sequence diagram on or off'],
  [':help  :quit', 'this help; quit'],
]

function helpLines(width: number): Rendered {
  const lines: Line[] = HELP.map(([key, text]) =>
    text === ''
      ? fitLine([seg(key, { bold: true })], width)
      : fitLine([seg(`  ${key.padEnd(30)}`, { color: 'cyan' }), seg(text)], width),
  )
  return { lines, ids: lines.map(() => undefined) }
}

function hint(u: Ui): string {
  switch (u.mode.kind) {
    case 'compose':
      return 'enter send · alt+enter new line · tab target · esc cancel'
    case 'command':
      return 'enter run · tab complete · esc cancel'
    case 'search':
      return 'enter search · esc cancel'
    case 'confirm':
      return 'y yes · n no'
    case 'normal':
      break
  }
  if (u.overlay !== undefined) return 'esc back · ↑↓ scroll · ? help · q quit'
  if (u.focus === 'sidebar')
    return '↑↓ move · enter open · [ hide · tab main pane · : command · ? help · q quit'
  return '↑↓ enter esc · m message · r reply · a attention · / search · : command · tab sidebar · ? help · q quit'
}

/** Complete a command line: the command word, or an agent name for the commands that take one. */
export function complete(value: string, agents: readonly AgentRow[]): string {
  const m = value.match(/^(\S*)(\s+(\S*))?$/)
  if (m === null) return value
  const [, word = '', rest, arg] = m
  if (rest === undefined) {
    const hits = COMMANDS.filter((c) => c.startsWith(word))
    if (hits.length === 1) return `${hits[0]} `
    return hits.length === 0 ? value : commonPrefix(hits)
  }
  if (word === 'kick' || word === 'allow' || word === 'filter') {
    const hits = agents
      .map((a) => a.address)
      .filter((x) => x.startsWith(arg ?? '') || x.split('@')[0]?.startsWith(arg ?? ''))
    if (hits.length === 1) return `${word} ${hits[0]}`
    return hits.length === 0 ? value : `${word} ${commonPrefix(hits)}`
  }
  return value
}

function commonPrefix(xs: readonly string[]): string {
  let p = xs[0] ?? ''
  for (const x of xs) while (!x.startsWith(p)) p = p.slice(0, -1)
  return p
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

  const t = now()
  const s = screen(store, ui, columns, t)
  const promptH = ui.mode.kind === 'compose' ? Math.min(4, ui.mode.value.split('\n').length) : 1
  const bodyH = Math.max(4, rows - 1 - promptH - 1)
  const mainBodyH = Math.max(1, bodyH - (s.attn === undefined ? 0 : 1))
  /** The sidebar row the cursor is on: where it was put, else the shown session's row. */
  const cursorOf = (u: Ui, cur: Screen) =>
    u.cursor ?? cur.sidebar.rows.findIndex((r) => r?.kind === 'session' && r.sid === u.sid)
  const sideIndex = cursorOf(ui, s)
  const sideOffset = listOffset(sideIndex, s.sidebar.lines.length, bodyH)
  const offset = viewOffset(ui, s, mainBodyH)
  const visible = s.main.lines.slice(offset, offset + mainBodyH)

  const act = (p: Promise<string>) =>
    p.then(
      (status) => set({ status }),
      (err: unknown) =>
        set({ status: `error: ${err instanceof Error ? err.message : String(err)}` }),
    )

  /** Show the main pane from `line`, and stop following. */
  const scrollTo = (line: number) => set({ scroll: Math.max(0, line), follow: false })

  /** Move the main pane by `delta` lines; at the bottom of a live view, follow again. */
  const scrollBy = (u: Ui, cur: Screen, delta: number, live: boolean) => {
    const max = Math.max(0, cur.main.lines.length - mainBodyH)
    const next = Math.min(max, Math.max(0, viewOffset(u, cur, mainBodyH) + delta))
    const canFollow = u.overlay === undefined && (u.view === 'transcript' || u.view === 'sequence')
    if (live && canFollow && next >= max && delta > 0)
      return set({ follow: true, scroll: undefined })
    scrollTo(next)
  }

  const selectMessage = (u: Ui, cur: Screen, id: string) => {
    const line = cur.main.ids.indexOf(id)
    const off = viewOffset(u, cur, mainBodyH)
    set({ msgId: id, follow: false, scroll: line < 0 ? u.scroll : reveal(line, off, mainBodyH) })
  }

  /** Show `sid`: the cursor and the selection start over. */
  const chooseSession = (u: Ui, sid: string, cursor: number | undefined) => {
    if (sid === u.sid) return set({ cursor })
    set({
      sid,
      cursor,
      msgId: undefined,
      agentAddr: undefined,
      agentFilter: undefined,
      overlay: undefined,
      follow: true,
      scroll: undefined,
      attn: 0,
    })
  }

  /**
   * Move the sidebar cursor to the next row with content, in `dir`. The cursor only points;
   * enter (or a click) shows a session or opens an agent, so browsing never switches the pane.
   */
  const moveSidebar = (u: Ui, cur: Screen, dir: 1 | -1) => {
    const rows = cur.sidebar.rows
    let i = cursorOf(u, cur)
    for (let step = 0; step < rows.length; step++) {
      const next = Math.min(rows.length - 1, Math.max(0, i + dir))
      if (next === i) return
      i = next
      const row = rows[i]
      if (row === undefined) continue
      return set({ cursor: i, ...(row.kind === 'agent' ? { agentAddr: row.address } : {}) })
    }
  }

  const openSidebarRow = (u: Ui, cur: Screen, index: number) => {
    const row = cur.sidebar.rows[index]
    if (row === undefined) return set({ focus: 'sidebar', cursor: index })
    if (row.kind === 'session') {
      chooseSession(u, row.sid, index)
      return set({ focus: 'main' })
    }
    set({
      cursor: index,
      agentAddr: row.address,
      overlay: { kind: 'agent', address: row.address },
      scroll: undefined,
    })
  }

  const openCurrent = (u: Ui, cur: Screen) => {
    if (u.overlay !== undefined) return
    if (u.focus === 'sidebar') return openSidebarRow(u, cur, cursorOf(u, cur))
    if (cur.current !== undefined) {
      set({
        msgId: cur.current,
        follow: false,
        overlay: { kind: 'message', id: cur.current },
        scroll: undefined,
      })
    }
  }

  const compose = (to: string, reply?: { id: string; to: string }) =>
    set({ mode: { kind: 'compose', to, reply, value: '' } })

  const nextAttention = (u: Ui, cur: Screen) => {
    if (cur.items.length === 0) return set({ status: 'nothing needs you right now' })
    const item = cur.items[u.attn % cur.items.length]
    if (item === undefined) return
    set({ attn: u.attn + 1, status: describe(item) })
    if (item.kind === 'blocked') {
      return set({
        agentAddr: item.address,
        overlay: { kind: 'agent', address: item.address },
        scroll: undefined,
      })
    }
    const view: View = u.view === 'insights' || u.view === 'sequence' ? 'transcript' : u.view
    set({ view, overlay: undefined })
    selectMessage(ref.current, screen(store, ref.current, columns, now()), item.id)
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
      // Row 1 is the title; the body starts at row 2. The sidebar takes the first columns.
      const inBody = y >= 2 && y <= 1 + bodyH
      if (!inBody) continue
      if (u.sidebar && x <= cur.sidebarW) {
        if (wheel) moveSidebar(u, cur, wheel > 0 ? 1 : -1)
        else {
          const index = y - 2 + sideOffset
          const row = cur.sidebar.rows[index]
          if (row?.kind === 'agent' && u.agentAddr === row.address && u.focus === 'sidebar') {
            openSidebarRow(u, cur, index)
          } else if (row?.kind === 'session') {
            chooseSession(u, row.sid, index)
            set({ focus: 'sidebar' })
          } else if (row?.kind === 'agent')
            set({ focus: 'sidebar', cursor: index, agentAddr: row.address })
          else set({ focus: 'sidebar' })
        }
        u = ref.current
        continue
      }
      if (wheel) {
        scrollBy(u, cur, wheel, true)
        u = ref.current
        continue
      }
      const line = y - 2 - (cur.attn === undefined ? 0 : 1) + viewOffset(u, cur, mainBodyH)
      const id = cur.main.ids[line]
      const focus: Partial<Ui> = u.focus === 'main' ? {} : { focus: 'main' }
      if (id === undefined || u.overlay !== undefined) set(focus)
      else if (id === u.msgId && !cur.following) {
        set({ ...focus, overlay: { kind: 'message', id }, scroll: undefined })
      } else {
        set(focus)
        selectMessage(ref.current, cur, id)
      }
      u = ref.current
    }
    return handled
  }

  useInput((input, key) => {
    let u = ref.current
    const cur = screen(store, u, columns, now())
    const session = u.sid === ALL_SESSIONS ? undefined : u.sid

    if (input.includes('[<') && mouse(u, cur, input)) return
    u = ref.current

    if (u.mode.kind === 'confirm') {
      const run = u.mode.run
      set({ mode: { kind: 'normal' }, status: input === 'y' ? '…' : 'cancelled' })
      if (input === 'y') act(run())
      return
    }
    if (u.mode.kind !== 'normal') {
      const m = u.mode
      if (key.escape) return set({ mode: { kind: 'normal' } })
      if (key.return && key.meta && m.kind === 'compose') {
        return set({ mode: { ...m, value: `${m.value}\n` } })
      }
      if (key.return) {
        set({ mode: { kind: 'normal' } })
        if (m.kind === 'compose') return submit(m, cur)
        if (m.kind === 'command') return command(m.value.trim(), ref.current, cur)
        const value = m.value.trim()
        return set({ search: value, status: value === '' ? 'search cleared' : `search: ${value}` })
      }
      if (key.tab) {
        if (m.kind === 'command')
          return set({ mode: { ...m, value: complete(m.value, cur.d.agents) } })
        if (m.kind === 'compose' && m.reply === undefined) {
          const targets = [BROADCAST, ...cur.d.agents.map((a) => a.address)]
          const i = targets.indexOf(m.to)
          return set({ mode: { ...m, to: targets[(i + 1) % targets.length] ?? BROADCAST } })
        }
        return
      }
      if (key.backspace || key.delete) {
        return set({ mode: { ...m, value: [...m.value].slice(0, -1).join('') } })
      }
      if (!key.ctrl && !key.meta && input !== '') {
        // A paste arrives as one chunk; its line breaks stay line breaks in the composer.
        const text = input.replace(/\r\n?/g, '\n')
        return set({
          mode: { ...m, value: m.value + (m.kind === 'compose' ? text : text.replace(/\n/g, ' ')) },
        })
      }
      return
    }

    if (input === 'q') return exit()
    if (input === '?')
      return set({ overlay: u.overlay?.kind === 'help' ? undefined : { kind: 'help' }, scroll: 0 })
    if (key.escape) {
      if (u.overlay !== undefined) return set({ overlay: undefined, scroll: undefined })
      return set({ search: '', agentFilter: undefined, status: 'filters cleared' })
    }
    if (key.tab) {
      return set({
        focus: u.focus === 'main' ? 'sidebar' : 'main',
        cursor: u.focus === 'main' ? cursorOf(u, cur) : u.cursor,
      })
    }
    if (input === '[') return set({ sidebar: !u.sidebar, focus: u.sidebar ? 'main' : u.focus })
    const down = key.downArrow || input === 'j'
    const up = key.upArrow || input === 'k'
    if (down || up) {
      const delta = up ? -1 : 1
      if (u.overlay !== undefined) return scrollBy(u, cur, delta, false)
      if (u.focus === 'sidebar') return moveSidebar(u, cur, delta)
      // A view without selectable lines (insights) scrolls by line.
      if (cur.selectable.length === 0) return scrollBy(u, cur, delta, false)
      const i =
        cur.current === undefined ? cur.selectable.length : cur.selectable.indexOf(cur.current)
      const id = cur.selectable[Math.min(cur.selectable.length - 1, Math.max(0, i + delta))]
      if (id !== undefined) selectMessage(u, cur, id)
      return
    }
    if (key.pageDown || key.pageUp) {
      const delta = key.pageDown ? mainBodyH - 1 : -(mainBodyH - 1)
      scrollBy(u, cur, delta, false)
      // Keep the cursor inside the window: the first or last message that is visible.
      const off = viewOffset(ref.current, cur, mainBodyH)
      const inWindow = cur.main.ids
        .slice(off, off + mainBodyH)
        .flatMap((id) => (id === undefined ? [] : [id]))
      const pick = key.pageDown ? inWindow.at(-1) : inWindow[0]
      if (pick !== undefined && u.overlay === undefined) set({ msgId: pick })
      return
    }
    if (key.home) {
      set({ scroll: 0, follow: false })
      if (cur.selectable[0] !== undefined && u.overlay === undefined)
        set({ msgId: cur.selectable[0] })
      return
    }
    if (key.end) {
      if (u.overlay === undefined && (u.view === 'transcript' || u.view === 'sequence')) {
        return set({ follow: true, scroll: undefined })
      }
      return scrollBy(u, cur, cur.main.lines.length, false)
    }
    if (key.return) return openCurrent(u, cur)
    if (u.overlay !== undefined) return
    const n = Number(input)
    if (Number.isInteger(n) && n >= 1 && n <= 3)
      return set({ view: VIEWS[n - 1] ?? 'transcript', scroll: undefined })
    switch (input) {
      case ' ':
        return set({
          follow: !u.follow,
          scroll: undefined,
          status: u.follow ? 'follow off' : 'follow on',
        })
      case '/':
        return set({ mode: { kind: 'search', value: u.search } })
      case ':':
        return set({ mode: { kind: 'command', value: '' } })
      case 'a':
        return nextAttention(u, cur)
      case 'm':
        if (session === undefined) return set({ status: 'pick a session first (tab, then ↑↓)' })
        return compose(BROADCAST)
      case 'r': {
        const m = cur.d.messages.find((x) => x.id === cur.current)
        if (m === undefined) return set({ status: 'select a message first' })
        if (m.from === OPERATOR) return set({ status: 'that is your own message' })
        return compose(m.from, { id: m.id, to: m.from })
      }
    }
  })

  /** Send what the composer holds. */
  function submit(m: Extract<Mode, { kind: 'compose' }>, cur: Screen) {
    const session = ref.current.sid === ALL_SESSIONS ? undefined : ref.current.sid
    const value = m.value.trim()
    if (session === undefined || value === '') return
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
    const legacy =
      m.to === BROADCAST && m.reply === undefined ? value.match(/^@(\S+)\s+([\s\S]+)$/) : null
    if (m.reply !== undefined) {
      if (!direct(m.reply.to)) return set({ status: `no agent ${m.reply.to}` })
    } else if (legacy !== null) {
      const name = legacy[1] ?? ''
      const hits = cur.d.agents.filter(
        (a) => a.address === name || a.address.split('@')[0] === name,
      )
      if (hits.length !== 1)
        return set({ status: hits.length === 0 ? `no agent ${name}` : `${name} is ambiguous` })
      if (!direct(hits[0]?.address ?? '')) return
      text = legacy[2] ?? ''
    } else if (m.to !== BROADCAST && !direct(m.to)) return set({ status: `no agent ${m.to}` })
    if ([...text].length > MAX_TEXT) return set({ status: `too long (max ${MAX_TEXT})` })
    // The hub keeps a message for an agent that left; the agent gets it when it joins again.
    const note = away === undefined ? '' : ` (${away} is away; it gets it when it returns)`
    return act(op.send(session, to, text, m.reply?.id).then((id) => `sent #${id}${note}`))
  }

  /** Run a `:` command. */
  function command(line: string, u: Ui, cur: Screen) {
    const [cmd = '', ...rest] = line.split(/\s+/)
    const arg = rest.join(' ')
    const session = u.sid === ALL_SESSIONS ? undefined : u.sid
    const rec = session === undefined ? undefined : store.sessions.get(session)
    const needSession = () => set({ status: 'pick a session first (tab, then ↑↓)' })
    const confirm = (text: string, run: () => Promise<string>) =>
      set({ mode: { kind: 'confirm', text, run } })
    /** An agent of the shown session, by address or bare name; default the sidebar's agent. */
    const agentOf = (name: string) => {
      const want = name === '' ? u.agentAddr : name
      if (want === undefined) return undefined
      const hits = cur.d.agents.filter(
        (a) => a.address === want || a.address.split('@')[0] === want,
      )
      return hits.length === 1 ? hits[0] : undefined
    }
    switch (cmd) {
      case 'new':
        if (!isToken(arg))
          return set({ status: 'usage: :new <name>  (a-z, 0-9, _ and -, up to 64)' })
        return act(
          op.createSession(arg).then(() => {
            chooseSession(ref.current, arg, undefined)
            return `created ${arg}`
          }),
        )
      case 'close':
        if (session === undefined || rec === undefined) return needSession()
        if (rec.status !== 'open') return set({ status: `${session} is already closed` })
        return confirm(`close ${session}? its agents are disconnected (y/n)`, () =>
          op.closeSession(session).then(() => `closed ${session}`),
        )
      case 'reopen':
        if (session === undefined || rec === undefined) return needSession()
        if (rec.status === 'open') return set({ status: `${session} is open` })
        return act(op.reopenSession(session).then(() => `reopened ${session}`))
      case 'delete':
        if (session === undefined || rec === undefined) return needSession()
        if (rec.status !== 'closed') return set({ status: 'close the session first (:close)' })
        return confirm(`delete ${session} and all its messages? (y/n)`, () =>
          op.deleteSession(session).then(() => `deleted ${session}`),
        )
      case 'kick':
      case 'allow': {
        if (session === undefined) return needSession()
        const a = agentOf(arg)
        const target = a === undefined ? undefined : parseAddress(a.address)
        if (a === undefined || target === undefined) {
          return set({
            status: arg === '' ? `usage: :${cmd} <agent>` : `no agent ${arg} in ${session}`,
          })
        }
        if (cmd === 'allow') {
          if (!a.kicked) return set({ status: `${a.address} is not removed` })
          return confirm(`allow ${a.address} back into ${session}? (y/n)`, () =>
            op.unkick(session, target).then(() => `${a.address} may join again`),
          )
        }
        if (a.kicked) return set({ status: `${a.address} is already removed` })
        return confirm(`remove ${a.address} from ${session}? (y/n)`, () =>
          op.kick(session, target).then(() => `removed ${a.address}`),
        )
      }
      case 'withdraw': {
        const id = arg === '' ? cur.current : arg.replace(/^#/, '')
        const m = cur.d.messages.find((x) => x.id === id)
        if (m === undefined)
          return set({ status: arg === '' ? 'select a message first' : `no message ${arg}` })
        return confirm(`withdraw message #${m.id} from ${m.from}? (y/n)`, () =>
          op
            .redact(m.sid, m.id)
            .then((ok) => (ok ? `withdrew #${m.id}` : `#${m.id} is not in ${m.sid}`)),
        )
      }
      case 'filter': {
        if (arg === '') return set({ agentFilter: undefined, status: 'filter off' })
        const a = agentOf(arg)
        if (a === undefined) return set({ status: `no agent ${arg}` })
        return set({ agentFilter: a.address, status: `only messages of ${a.address}` })
      }
      case 'sys':
        return set({
          system: !u.system,
          status: u.system ? 'system lines hidden' : 'system lines shown',
        })
      case 'seq':
        return set({ view: u.view === 'sequence' ? 'transcript' : 'sequence', scroll: undefined })
      case 'help':
        return set({ overlay: { kind: 'help' }, scroll: 0 })
      case 'quit':
      case 'q':
        return exit()
      case '':
        return
      default:
        return set({ status: `unknown command :${cmd} (tab lists them)` })
    }
  }

  const title = (() => {
    const o = ui.overlay
    const where = ui.sid === ALL_SESSIONS ? 'all sessions' : ui.sid
    const link = store.link === 'live' ? '' : `  [${store.link}]`
    if (o?.kind === 'help') return `coop · help · esc back${link}`
    if (o?.kind === 'message') return `coop · ${where} · message #${o.id} · esc back${link}`
    if (o?.kind === 'agent') return `coop · ${where} · agent ${o.address} · esc back${link}`
    return `coop · ${where} · ${ui.view}${ui.agentFilter ? `  [${ui.agentFilter}]` : ''}${
      ui.search ? `  [/${ui.search}]` : ''
    }${s.following ? '  follow ●' : ''}${link}`
  })()

  const promptLines: string[] = (() => {
    const m = ui.mode
    if (m.kind === 'compose') {
      const label =
        m.reply === undefined
          ? `to ${m.to} (tab: next)`
          : `reply to #${m.reply.id} from ${m.reply.to}`
      const lines = m.value.split('\n')
      const shown = lines.slice(-4)
      return shown.map(
        (l, i) =>
          `${i === 0 ? `${label} › ` : ' '.repeat(label.length + 3)}${l}${i === shown.length - 1 ? '▏' : ''}`,
      )
    }
    if (m.kind === 'command') return [`:${m.value}▏`]
    if (m.kind === 'search') return [`search › ${m.value}▏`]
    if (m.kind === 'confirm') return [m.text]
    return [ui.status]
  })()

  return (
    <Box flexDirection="column" width={columns} height={rows}>
      <Text bold wrap="truncate">
        {title}
      </Text>
      <Box flexDirection="row" height={bodyH}>
        {ui.sidebar && (
          <Box
            width={s.sidebarW}
            flexDirection="column"
            borderStyle="single"
            borderTop={false}
            borderBottom={false}
            borderLeft={false}
            borderColor={ui.focus === 'sidebar' ? 'cyan' : 'gray'}
          >
            {s.sidebar.lines.slice(sideOffset, sideOffset + bodyH).map((l, i) => (
              <Paint key={s.sidebar.ids[sideOffset + i] ?? `s${sideOffset + i}`} line={l} />
            ))}
          </Box>
        )}
        <Box flexGrow={1} flexDirection="column">
          {s.attn === undefined ? null : <Paint key="attention" line={fitLine(s.attn, s.mainW)} />}
          {visible.map((l, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a line's position in the pane is its identity
            <Paint key={offset + i} line={l} />
          ))}
        </Box>
      </Box>
      {promptLines.map((l, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: a prompt line's position is its identity
        <Text key={i} wrap="truncate" color={ui.mode.kind === 'normal' ? 'gray' : 'cyan'}>
          {l || ' '}
        </Text>
      ))}
      <Text wrap="truncate" dimColor>
        {hint(ui)}
      </Text>
    </Box>
  )
}

function Paint({ line }: { line: Line }) {
  // A segment's key is its character offset in the line: unique and stable, once the empty
  // segments are gone (an empty one would share its offset with the next).
  const segs = line.filter((s) => s.text !== '')
  const offsets = segs.reduce<number[]>((acc, _s, i) => {
    acc.push(i === 0 ? 0 : (acc[i - 1] ?? 0) + [...(segs[i - 1]?.text ?? '')].length)
    return acc
  }, [])
  return (
    <Text wrap="truncate">
      {segs.map((s, i) => (
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
