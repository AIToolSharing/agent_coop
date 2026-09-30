// The Ink shell: panes, keys, prompts. It paints what the views return and calls the operator.
//
// Keys can arrive faster than a repaint (paste, key repeat, scripts). So all UI state is one
// object in a ref: each key reads the latest state and writes the next state at once, and
// anything derived from the state is computed again from that latest state.
import { type Address, BROADCAST, isToken, MAX_TEXT, parseAddress, type To } from '@coop/core'
import { Box, Text, useApp, useInput, useWindowSize } from 'ink'
import { useEffect, useRef, useState } from 'react'
import { ALL_SESSIONS, type Derived, derive, type Store } from './model.js'
import type { ViewOptions } from './views/common.js'
import { renderAgent, renderAgents, renderMessage } from './views/inspect.js'
import type { Line, Rendered } from './views/line.js'
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
  redact(sid: string, id: string): Promise<boolean>
  send(sid: string, to: To, text: string): Promise<string>
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
  | { readonly kind: 'input'; readonly purpose: 'send' | 'new' | 'search'; readonly value: string }
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
  mode: { kind: 'normal' },
  status: '',
}

const SESSIONS_W = 30
const NEXT_FOCUS: Record<Focus, Focus> = { view: 'agents', agents: 'sessions', sessions: 'view' }

interface Screen {
  readonly d: Derived
  readonly sessions: readonly SessionSummary[]
  readonly rendered: Rendered
  /** The message under the cursor: the newest one while following the log or sequence. */
  readonly current: string | undefined
  readonly selectable: readonly string[]
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
  const following = u.follow && (u.view === 'log' || u.view === 'sequence')
  const current = following ? selectable.at(-1) : u.msgId
  return { d, sessions: summarize(store, now), rendered, current, selectable }
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
  const mainH = Math.max(6, rows - agentsH - 4)
  const agentsPane = renderAgents(s.d.agents, {
    width: columns - 2,
    now: t,
    selected: ui.agentAddr,
  })
  const sessionsPane = renderSessions(s.sessions, ui.sid, SESSIONS_W - 2)
  const bodyH = mainH - 3
  const cursorLine = s.rendered.ids.indexOf(s.current)
  const offset =
    ui.follow && cursorLine < 0
      ? Math.max(0, s.rendered.lines.length - bodyH)
      : Math.min(Math.max(0, s.rendered.lines.length - bodyH), Math.max(0, cursorLine - bodyH + 1))
  const visible = s.rendered.lines.slice(offset, offset + bodyH)

  const act = (p: Promise<string>) =>
    p.then(
      (status) => set({ status }),
      (err: unknown) =>
        set({ status: `error: ${err instanceof Error ? err.message : String(err)}` }),
    )

  useInput((input, key) => {
    const u = ref.current
    const cur = screen(store, u, viewW, now())
    const session = u.sid === ALL_SESSIONS ? undefined : u.sid

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
        return submit(m.purpose, m.value.trim(), cur)
      }
      if (key.backspace || key.delete)
        return set({ mode: { ...m, value: [...m.value].slice(0, -1).join('') } })
      if (!key.ctrl && !key.meta && input !== '') set({ mode: { ...m, value: m.value + input } })
      return
    }

    if (input === 'q') return exit()
    if (key.tab) return set({ focus: NEXT_FOCUS[u.focus] })
    if (key.upArrow || key.downArrow) {
      const delta = key.upArrow ? -1 : 1
      if (u.focus === 'sessions') {
        const i = cur.sessions.findIndex((x) => x.sid === u.sid)
        const next = cur.sessions[clamp(i + delta, cur.sessions.length)]
        if (next !== undefined && next.sid !== u.sid) {
          set({
            sid: next.sid,
            msgId: undefined,
            agentAddr: undefined,
            agentFilter: undefined,
            follow: true,
          })
        }
        return
      }
      if (u.focus === 'agents') {
        const list = cur.d.agents.map((a) => a.address)
        const i = u.agentAddr === undefined ? -1 : list.indexOf(u.agentAddr)
        return set({ agentAddr: list[clamp(i + delta, list.length)] })
      }
      if (cur.selectable.length === 0) return
      const i =
        cur.current === undefined ? cur.selectable.length : cur.selectable.indexOf(cur.current)
      return set({ follow: false, msgId: cur.selectable[clamp(i + delta, cur.selectable.length)] })
    }
    const n = Number(input)
    if (Number.isInteger(n) && n >= 1 && n <= VIEWS.length)
      return set({ view: VIEWS[n - 1] ?? 'log' })
    if (key.return) {
      if (u.focus === 'agents' && u.agentAddr !== undefined) return set({ view: 'agent' })
      if (u.focus === 'sessions') return set({ focus: 'view' })
      if (cur.current !== undefined)
        return set({ msgId: cur.current, follow: false, view: 'message' })
      return
    }
    if (key.escape) return set({ search: '', agentFilter: undefined, status: 'filters cleared' })
    switch (input) {
      case ' ':
        return set({ follow: !u.follow, status: u.follow ? 'follow off' : 'follow on' })
      case 's':
        return set({ system: !u.system })
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
      case 'c': {
        const rec = session === undefined ? undefined : store.sessions.get(session)
        if (session === undefined || rec === undefined)
          return set({ status: 'select a session first' })
        return act(
          rec.status === 'open'
            ? op.closeSession(session).then(() => `closed ${session}`)
            : op.reopenSession(session).then(() => `reopened ${session}`),
        )
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
        const sid = cur.d.agents.find((a) => a.address === u.agentAddr)?.sid
        if (target === undefined || sid === undefined)
          return set({ status: 'select an agent first (tab to agents)' })
        return set({
          mode: {
            kind: 'confirm',
            text: `remove ${u.agentAddr} from ${sid}? (y/n)`,
            run: () => op.kick(sid, target).then(() => `removed ${u.agentAddr}`),
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

  function submit(purpose: 'send' | 'new' | 'search', value: string, cur: Screen) {
    if (purpose === 'search')
      return set({ search: value, status: value === '' ? 'search cleared' : `search: ${value}` })
    if (purpose === 'new') {
      if (!isToken(value)) return set({ status: 'a session name uses a-z, 0-9, _ and -, up to 64' })
      return act(
        op.createSession(value).then(() => {
          set({ sid: value })
          return `created ${value}`
        }),
      )
    }
    const session = ref.current.sid === ALL_SESSIONS ? undefined : ref.current.sid
    if (session === undefined || value === '') return
    const m = value.match(/^@(\S+)\s+([\s\S]+)$/)
    let to: To = BROADCAST
    let text = value
    if (m !== null) {
      const name = m[1] ?? ''
      const hits = cur.d.agents.filter(
        (a) => a.address === name || a.address.split('@')[0] === name,
      )
      if (hits.length !== 1)
        return set({ status: hits.length === 0 ? `no agent ${name}` : `${name} is ambiguous` })
      const hit = hits[0]
      const addr = parseAddress(hit?.address ?? '')
      if (hit === undefined || addr === undefined) return
      // A direct message reaches an agent only while it is online (same rule as the hub).
      if (!hit.online) return set({ status: `${hit.address} is offline; it would not get this` })
      to = addr
      text = m[2] ?? ''
    }
    if ([...text].length > MAX_TEXT) return set({ status: `too long (max ${MAX_TEXT})` })
    return act(op.send(session, to, text).then((id) => `sent #${id}`))
  }

  const title = `${VIEWS.indexOf(ui.view) + 1} ${ui.view}: ${ui.sid === ALL_SESSIONS ? 'all sessions' : ui.sid}${
    ui.agentFilter ? `  [${ui.agentFilter}]` : ''
  }${ui.search ? `  [/${ui.search}]` : ''}${ui.follow ? '  follow ●' : ''}`
  const prompt =
    ui.mode.kind === 'input'
      ? `${ui.mode.purpose === 'send' ? 'to all (or @name text)' : ui.mode.purpose === 'new' ? 'new session name' : 'search'} › ${ui.mode.value}▏`
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
          {sessionsPane.lines.slice(0, mainH - 3).map((l, i) => (
            <Paint key={sessionsPane.ids[i] ?? i} line={l} />
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
        {agentsPane.lines.slice(0, agentsH - 2).map((l, i) => (
          <Paint key={agentsPane.ids[i] ?? i} line={l} />
        ))}
      </Box>
      <Text wrap="truncate" color={ui.mode.kind === 'normal' ? 'gray' : 'cyan'}>
        {prompt || ' '}
      </Text>
      <Text wrap="truncate" dimColor>
        1-6 views · tab focus · ↑↓ move · enter open · / search · f filter · s system · space follow
        · m message · n new · c close/reopen · D delete · k kick · r withdraw · q quit
      </Text>
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
