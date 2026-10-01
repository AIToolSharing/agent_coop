import { formatAddress } from '@coop/core'
import { render } from 'ink-testing-library'
import { beforeEach, describe, expect, test } from 'vitest'
import { App, type OperatorApi } from '../src/app.js'
import { FIX_SID, fixture, NOW } from './fixture.js'
import { storeOf } from './scenario.js'

const KEY = {
  tab: '\t',
  enter: '\r',
  down: '\u001B[B',
  up: '\u001B[A',
  esc: '\u001B',
  pageUp: '\u001B[5~',
  pageDown: '\u001B[6~',
  end: '\u001B[F',
}
/** An SGR mouse report: a left click or a wheel step at a 1-based column and row. */
const click = (x: number, y: number) => `\u001B[<0;${x};${y}M\u001B[<0;${x};${y}m`
const wheel = (dir: 'up' | 'down', x: number, y: number) =>
  `\u001B[<${dir === 'up' ? 64 : 65};${x};${y}M`
const tick = () => new Promise((r) => setTimeout(r, 30))

let calls: string[]
const op: OperatorApi = {
  createSession: async (s) => void calls.push(`create ${s}`),
  closeSession: async (s) => void calls.push(`close ${s}`),
  reopenSession: async (s) => void calls.push(`reopen ${s}`),
  deleteSession: async (s) => void calls.push(`delete ${s}`),
  kick: async (s, t) => void calls.push(`kick ${s} ${formatAddress(t)}`),
  unkick: async (s, t) => void calls.push(`unkick ${s} ${formatAddress(t)}`),
  redact: async (s, id) => {
    calls.push(`redact ${s} ${id}`)
    return true
  },
  send: async (s, to, text, reply_to) => {
    const target = typeof to === 'string' ? to : formatAddress(to)
    calls.push(`send ${s} ${target} ${text}${reply_to === undefined ? '' : ` ↩${reply_to}`}`)
    return '99'
  },
}

function start() {
  const store = storeOf(fixture())
  const r = render(<App store={store} op={op} subscribe={() => () => undefined} now={() => NOW} />)
  const type = async (...keys: string[]) => {
    for (const k of keys) {
      r.stdin.write(k)
      await tick()
    }
  }
  const frame = () => r.lastFrame() ?? ''
  return { ...r, type, frame }
}

/** From the start (focus on the view), select session build-42. */
async function openSession(app: ReturnType<typeof start>) {
  await app.type(KEY.tab, KEY.tab, KEY.down, KEY.tab)
  expect(app.frame()).toContain(`log: ${FIX_SID}`)
}

beforeEach(() => {
  calls = []
})

describe('the TUI', () => {
  test('shows sessions, the log of all traffic, and the keys', () => {
    const app = start()
    const f = app.frame()
    expect(f).toContain('* all traffic')
    expect(f).toContain('build-42')
    expect(f).toContain('1 log: all sessions')
    expect(f).toContain('alice@mac-1 → bob@vps-2   #7 What is th')
    app.unmount()
  })

  test('switches views with the number keys', async () => {
    const app = start()
    await openSession(app)
    await app.type('2')
    expect(app.frame()).toContain('2 sequence')
    expect(app.frame()).toContain('├──')
    await app.type('3')
    expect(app.frame()).toContain('OPEN ASKS (1)')
    await app.type('4')
    expect(app.frame()).toContain('MEDIAN MS UNTIL THE MESSAGE REACHED')
    app.unmount()
  })

  test('sends as the operator, to all or to one agent', async () => {
    const app = start()
    await openSession(app)
    await app.type('m', ...'hello all', KEY.enter)
    await app.type('m', ...'@bob stop', KEY.enter)
    expect(calls).toEqual([`send ${FIX_SID} all hello all`, `send ${FIX_SID} bob@vps-2 stop`])
    expect(app.frame()).toContain('sent #99')
    app.unmount()
  })

  test('a answers the selected message: to its sender, in its thread', async () => {
    const app = start()
    await openSession(app)
    // While following, the newest message (#18, carol to bob) is under the cursor.
    await app.type('a')
    expect(app.frame()).toContain('reply to #18')
    await app.type(...'ask bob first', KEY.enter)
    expect(calls).toEqual([`send ${FIX_SID} carol@mac-3 ask bob first ↩18`])
    expect(app.frame()).toContain('sent #99')
    app.unmount()
  })

  // The hub keeps a message for an agent that left and replays it when the agent returns.
  test('a message to an agent that left is sent, and the operator is told it is away', async () => {
    const store = storeOf(fixture())
    store.apply([{ kind: 'presence', key: `${FIX_SID}.vps-2.bob`, record: undefined }])
    const r = render(
      <App store={store} op={op} subscribe={() => () => undefined} now={() => NOW} />,
    )
    for (const k of [KEY.tab, KEY.tab, KEY.down, KEY.tab, 'm', '@bob hi', KEY.enter]) {
      r.stdin.write(k)
      await tick()
    }
    expect(calls).toEqual([`send ${FIX_SID} bob@vps-2 hi`])
    expect(r.lastFrame()).toContain('bob@vps-2 is away')
    r.unmount()
  })

  test('kicks the selected agent after y', async () => {
    const app = start()
    await openSession(app)
    await app.type(KEY.tab, KEY.down, 'k')
    expect(app.frame()).toContain('remove alice@mac-1 from build-42? (y/n)')
    await app.type('y')
    expect(calls).toEqual([`kick ${FIX_SID} alice@mac-1`])
    app.unmount()
  })

  test('withdraws the selected message after y, and n cancels', async () => {
    const app = start()
    await openSession(app)
    await app.type('r')
    expect(app.frame()).toContain('withdraw message #18')
    await app.type('n')
    expect(calls).toEqual([])
    await app.type(KEY.up, 'r', 'y')
    expect(calls).toEqual([`redact ${FIX_SID} 17`])
    app.unmount()
  })

  test('creates, closes after y, and refuses to delete an open session', async () => {
    const app = start()
    await openSession(app)
    await app.type('D')
    expect(app.frame()).toContain('close the session first')
    await app.type('c')
    expect(app.frame()).toContain(`close ${FIX_SID}? its agents are disconnected`)
    await app.type('y')
    await app.type('n', ...'demo', KEY.enter)
    expect(calls).toEqual([`close ${FIX_SID}`, 'create demo'])
    app.unmount()
  })

  // Found in use: an agent removed by mistake had no way back.
  test('shows a removed agent and lets it back in with k', async () => {
    const store = storeOf(fixture())
    store.apply([
      { kind: 'kick', key: `${FIX_SID}.kick.mac-1.alice`, record: { at: '2026-09-30T12:04:00Z' } },
    ])
    const r = render(
      <App store={store} op={op} subscribe={() => () => undefined} now={() => NOW} />,
    )
    const type = async (...keys: string[]) => {
      for (const k of keys) {
        r.stdin.write(k)
        await tick()
      }
    }
    await type(KEY.tab, KEY.tab, KEY.down, KEY.tab, KEY.tab, KEY.down)
    expect(r.lastFrame()).toContain('removed')
    await type('k')
    expect(r.lastFrame()).toContain('allow alice@mac-1 back into build-42? (y/n)')
    await type('y')
    expect(calls).toEqual([`unkick ${FIX_SID} alice@mac-1`])
    r.unmount()
  })

  test('shows the help with ? and closes it with esc', async () => {
    const app = start()
    await app.type('?')
    expect(app.frame()).toContain('move the focus')
    expect(app.frame()).toContain('close help')
    await app.type(KEY.esc)
    expect(app.frame()).not.toContain('close help')
    expect(app.frame()).toContain('1 log')
    app.unmount()
  })

  test('scrolls with page keys and returns to following with end', async () => {
    const app = start()
    await openSession(app)
    expect(app.frame()).toContain('follow ●')
    await app.type(KEY.pageUp)
    expect(app.frame()).not.toContain('follow ●')
    await app.type(KEY.end)
    expect(app.frame()).toContain('follow ●')
    app.unmount()
  })

  test('a click selects a session, a second click on a message opens it', async () => {
    const app = start()
    // Row 4 of the sessions pane is the second entry: build-42 (row 3 is "all traffic").
    await app.type(click(5, 4))
    expect(app.frame()).toContain(`log: ${FIX_SID}`)
    // The view pane body starts at row 3; its first line is a system line, so click lower.
    const lines = app.frame().split('\n')
    const row = lines.findIndex((l) => l.includes('#7 What is th')) + 1
    expect(row).toBeGreaterThan(0)
    await app.type(click(50, row))
    expect(app.frame()).not.toContain('follow ●')
    await app.type(click(50, row))
    expect(app.frame()).toContain('6 message')
    expect(app.frame()).toContain('#7')
    app.unmount()
  })

  test('the wheel scrolls the view and selects agents', async () => {
    const app = start()
    await openSession(app)
    await app.type(wheel('up', 50, 5))
    expect(app.frame()).not.toContain('follow ●')
    await app.type(wheel('down', 50, 5), wheel('down', 50, 5), wheel('down', 50, 5))
    expect(app.frame()).toContain('follow ●')
    const lines = app.frame().split('\n')
    const agentRow = lines.findIndex((l) => l.includes('bob@vps-2') && l.includes('codex')) + 1
    await app.type(wheel('down', 10, agentRow))
    await app.type(KEY.enter)
    expect(app.frame()).toContain('5 agent')
    app.unmount()
  })

  // Found in the end-to-end run: keys sent faster than a repaint acted on old state.
  test('keys that arrive together act on the latest state', async () => {
    const app = start()
    for (const k of [KEY.tab, KEY.tab, KEY.down, KEY.tab, 'm', 'h', 'i', KEY.enter])
      app.stdin.write(k)
    await tick()
    expect(app.frame()).toContain(`log: ${FIX_SID}`)
    expect(calls).toEqual([`send ${FIX_SID} all hi`])
    app.unmount()
  })

  test('opens the message inspector with enter', async () => {
    const app = start()
    await openSession(app)
    await app.type(KEY.up, KEY.up, KEY.up, KEY.enter)
    const f = app.frame()
    expect(f).toContain('6 message')
    expect(f).toContain('REACHED')
    app.unmount()
  })
})
