// The Ink shell: panes, keys, prompts. It paints what the views return and calls the operator.
import { type Address, BROADCAST, isToken, MAX_TEXT, parseAddress, type To } from '@coop/core'
import { Box, Text, useApp, useInput, useWindowSize } from 'ink'
import { useEffect, useMemo, useState } from 'react'
import { ALL_SESSIONS, derive, type Store } from './model.js'
import type { ViewOptions } from './views/common.js'
import { renderAgent, renderAgents, renderMessage } from './views/inspect.js'
import type { Line, Rendered } from './views/line.js'
import { renderLog } from './views/log.js'
import { renderMatrix } from './views/matrix.js'
import { renderSequence } from './views/sequence.js'
import { renderSessions, summarize } from './views/sessions.js'
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

const SESSIONS_W = 30

export function App({ store, subscribe, op, now = Date.now }: AppProps) {
  const { exit } = useApp()
  const { columns, rows } = useWindowSize()
  const [version, setVersion] = useState(store.version)
  const [sid, setSid] = useState(ALL_SESSIONS)
  const [view, setView] = useState<View>('log')
  const [focus, setFocus] = useState<Focus>('view')
  const [msgId, setMsgId] = useState<string | undefined>()
  const [agentAddr, setAgentAddr] = useState<string | undefined>()
  const [follow, setFollow] = useState(true)
  const [system, setSystem] = useState(true)
  const [agentFilter, setAgentFilter] = useState<string | undefined>()
  const [search, setSearch] = useState('')
  const [mode, setMode] = useState<Mode>({ kind: 'normal' })
  const [status, setStatus] = useState('')

  useEffect(() => subscribe(() => setVersion(store.version)), [store, subscribe])
  // A clock tick keeps ages and waits current.
  useEffect(() => {
    const t = setInterval(() => setVersion((v) => v + 0), 1000)
    return () => clearInterval(t)
  }, [])

  const t = now()
  // biome-ignore lint/correctness/useExhaustiveDependencies: version marks store changes
  const d = useMemo(() => derive(store, sid), [store, sid, version])
  // biome-ignore lint/correctness/useExhaustiveDependencies: version marks store changes
  const sessions = useMemo(() => summarize(store, t), [store, version, Math.floor(t / 1000)])

  const agentsH = Math.min(8, Math.max(3, d.agents.length + 2))
  const mainH = Math.max(6, rows - agentsH - 4)
  const viewW = Math.max(20, columns - SESSIONS_W - 4)
  const opts: ViewOptions = {
    width: viewW,
    now: t,
    agent: agentFilter,
    search,
    system,
    selected: view === 'agent' ? agentAddr : msgId,
  }
  const rendered: Rendered = (() => {
    switch (view) {
      case 'log':
        return renderLog(d, opts)
      case 'sequence':
        return renderSequence(d, opts)
      case 'threads':
        return renderThreads(d, opts)
      case 'matrix':
        return renderMatrix(d, opts)
      case 'agent':
        return renderAgent(d, agentAddr, opts)
      case 'message':
        return renderMessage(d, msgId, opts)
    }
  })()
  const agentsPane = renderAgents(d.agents, { ...opts, width: columns - 2, selected: agentAddr })
  const sessionsPane = renderSessions(sessions, sid, SESSIONS_W - 2)

  const selectable = rendered.ids.flatMap((id, i) => (id === undefined ? [] : [{ id, i }]))
  // In follow mode the cursor sits on the newest message.
  const current = follow && (view === 'log' || view === 'sequence') ? selectable.at(-1)?.id : msgId
  const cursorLine = rendered.ids.indexOf(current)
  const bodyH = mainH - 3
  const offset =
    follow && cursorLine < 0
      ? Math.max(0, rendered.lines.length - bodyH)
      : Math.min(Math.max(0, rendered.lines.length - bodyH), Math.max(0, cursorLine - bodyH + 1))
  const visible = rendered.lines.slice(offset, offset + bodyH)

  const selectedSession = sid === ALL_SESSIONS ? undefined : sid
  const moveMsg = (delta: number) => {
    if (selectable.length === 0) return
    const i = selectable.findIndex((s) => s.id === current)
    const next =
      selectable[
        Math.min(selectable.length - 1, Math.max(0, (i < 0 ? selectable.length : i) + delta))
      ]
    setFollow(false)
    setMsgId(next?.id)
  }
  const moveAgent = (delta: number) => {
    const list = d.agents.map((a) => a.address)
    if (list.length === 0) return
    const i = agentAddr === undefined ? -1 : list.indexOf(agentAddr)
    setAgentAddr(list[Math.min(list.length - 1, Math.max(0, i + delta))])
  }
  const moveSession = (delta: number) => {
    const i = sessions.findIndex((s) => s.sid === sid)
    const next = sessions[Math.min(sessions.length - 1, Math.max(0, i + delta))]
    if (next !== undefined) {
      setSid(next.sid)
      setMsgId(undefined)
      setAgentAddr(undefined)
      setAgentFilter(undefined)
      setFollow(true)
    }
  }
  const act = (p: Promise<string>) =>
    p.then(setStatus, (err: unknown) =>
      setStatus(`error: ${err instanceof Error ? err.message : String(err)}`),
    )

  useInput((input, key) => {
    if (mode.kind === 'confirm') {
      if (input === 'y') act(mode.run())
      else setStatus('cancelled')
      setMode({ kind: 'normal' })
      return
    }
    if (mode.kind === 'input') {
      if (key.escape) return setMode({ kind: 'normal' })
      if (key.return) {
        setMode({ kind: 'normal' })
        return submit(mode.purpose, mode.value.trim())
      }
      if (key.backspace || key.delete)
        return setMode({ ...mode, value: [...mode.value].slice(0, -1).join('') })
      if (!key.ctrl && !key.meta && input !== '') setMode({ ...mode, value: mode.value + input })
      return
    }

    if (input === 'q') return exit()
    if (key.tab)
      return setFocus(focus === 'sessions' ? 'view' : focus === 'view' ? 'agents' : 'sessions')
    if (key.upArrow || key.downArrow) {
      const delta = key.upArrow ? -1 : 1
      if (focus === 'sessions') return moveSession(delta)
      if (focus === 'agents') return moveAgent(delta)
      return moveMsg(delta)
    }
    const n = Number(input)
    if (Number.isInteger(n) && n >= 1 && n <= VIEWS.length) return setView(VIEWS[n - 1] ?? 'log')
    if (key.return) {
      if (focus === 'agents' && agentAddr !== undefined) return setView('agent')
      if (focus === 'sessions') return setFocus('view')
      if (current !== undefined) {
        setMsgId(current)
        setFollow(false)
        return setView('message')
      }
      return
    }
    if (key.escape) {
      setSearch('')
      setAgentFilter(undefined)
      return setStatus('filters cleared')
    }
    switch (input) {
      case ' ':
        setFollow(!follow)
        return setStatus(follow ? 'follow off' : 'follow on')
      case 's':
        return setSystem(!system)
      case 'f': {
        const list = [undefined, ...d.agents.map((a) => a.address)]
        const next = list[(list.indexOf(agentFilter) + 1) % list.length]
        setAgentFilter(next)
        return setStatus(next === undefined ? 'filter off' : `only messages of ${next}`)
      }
      case '/':
        return setMode({ kind: 'input', purpose: 'search', value: search })
      case 'n':
        return setMode({ kind: 'input', purpose: 'new', value: '' })
      case 'm':
        if (selectedSession === undefined) return setStatus('select a session first')
        return setMode({ kind: 'input', purpose: 'send', value: '' })
      case 'c': {
        if (selectedSession === undefined) return setStatus('select a session first')
        const s = store.sessions.get(selectedSession)
        if (s === undefined) return
        return act(
          s.status === 'open'
            ? op.closeSession(selectedSession).then(() => `closed ${selectedSession}`)
            : op.reopenSession(selectedSession).then(() => `reopened ${selectedSession}`),
        )
      }
      case 'D': {
        if (selectedSession === undefined) return setStatus('select a session first')
        if (store.sessions.get(selectedSession)?.status !== 'closed') {
          return setStatus('close the session first (c)')
        }
        return setMode({
          kind: 'confirm',
          text: `delete session ${selectedSession} and all its messages? (y/n)`,
          run: () => op.deleteSession(selectedSession).then(() => `deleted ${selectedSession}`),
        })
      }
      case 'k': {
        const target = agentAddr === undefined ? undefined : parseAddress(agentAddr)
        const s = d.agents.find((a) => a.address === agentAddr)?.sid
        if (target === undefined || s === undefined)
          return setStatus('select an agent first (tab to agents)')
        return setMode({
          kind: 'confirm',
          text: `remove ${agentAddr} from ${s}? (y/n)`,
          run: () => op.kick(s, target).then(() => `removed ${agentAddr}`),
        })
      }
      case 'r': {
        const m = d.messages.find((x) => x.id === current)
        if (m === undefined) return setStatus('select a message first')
        return setMode({
          kind: 'confirm',
          text: `withdraw message #${m.id} from ${m.from}? (y/n)`,
          run: () =>
            op
              .redact(m.sid, m.id)
              .then((ok) => (ok ? `withdrew #${m.id}` : `#${m.id} is not in ${m.sid}`)),
        })
      }
    }
  })

  function submit(purpose: 'send' | 'new' | 'search', value: string) {
    if (purpose === 'search') {
      setSearch(value)
      return setStatus(value === '' ? 'search cleared' : `search: ${value}`)
    }
    if (purpose === 'new') {
      if (!isToken(value)) return setStatus('a session name uses a-z, 0-9, _ and -, up to 64')
      return act(
        op.createSession(value).then(() => {
          setSid(value)
          return `created ${value}`
        }),
      )
    }
    if (selectedSession === undefined || value === '') return
    const m = value.match(/^@(\S+)\s+([\s\S]+)$/)
    let to: To = BROADCAST
    let text = value
    if (m !== null) {
      const name = m[1] ?? ''
      const hits = d.agents.filter((a) => a.address === name || a.address.split('@')[0] === name)
      if (hits.length !== 1)
        return setStatus(hits.length === 0 ? `no agent ${name}` : `${name} is ambiguous`)
      const addr = parseAddress(hits[0]?.address ?? '')
      if (addr === undefined) return
      to = addr
      text = m[2] ?? ''
    }
    if ([...text].length > MAX_TEXT) return setStatus(`too long (max ${MAX_TEXT})`)
    return act(op.send(selectedSession, to, text).then((id) => `sent #${id}`))
  }

  const title = `${VIEWS.indexOf(view) + 1} ${view}: ${sid === ALL_SESSIONS ? 'all sessions' : sid}${
    agentFilter ? `  [${agentFilter}]` : ''
  }${search ? `  [/${search}]` : ''}${follow ? '  follow ●' : ''}`
  const prompt =
    mode.kind === 'input'
      ? `${mode.purpose === 'send' ? 'to all (or @name text)' : mode.purpose === 'new' ? 'new session name' : 'search'} › ${mode.value}▏`
      : mode.kind === 'confirm'
        ? mode.text
        : status

  void version
  return (
    <Box flexDirection="column" width={columns} height={rows}>
      <Box flexDirection="row" height={mainH}>
        <Box
          width={SESSIONS_W}
          flexDirection="column"
          borderStyle="single"
          borderColor={focus === 'sessions' ? 'cyan' : 'gray'}
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
          borderColor={focus === 'view' ? 'cyan' : 'gray'}
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
        borderColor={focus === 'agents' ? 'cyan' : 'gray'}
      >
        {agentsPane.lines.slice(0, agentsH - 2).map((l, i) => (
          <Paint key={agentsPane.ids[i] ?? i} line={l} />
        ))}
      </Box>
      <Text wrap="truncate" color={mode.kind === 'normal' ? 'gray' : 'cyan'}>
        {prompt || ' '}
      </Text>
      <Text wrap="truncate" dimColor>
        1-6 views · tab focus · ↑↓ move · enter open · / search · f filter · s system · space follow
        · m message · n new · c close/reopen · D delete · k kick · r withdraw · q quit
      </Text>
    </Box>
  )
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
