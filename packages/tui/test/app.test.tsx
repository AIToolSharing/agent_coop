import { formatAddress } from '@coop/core'
import { render } from 'ink-testing-library'
import { beforeEach, describe, expect, test } from 'vitest'
import { App, complete, type OperatorApi } from '../src/app.js'
import { derive } from '../src/model.js'
import { FIX_SID, fixture, NOW } from './fixture.js'
import { storeOf } from './scenario.js'

const KEY = {
  tab: '\t',
  enter: '\r',
  altEnter: '\u001B\r',
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

function start(store = storeOf(fixture())) {
  const r = render(<App store={store} op={op} subscribe={() => () => undefined} now={() => NOW} />)
  const type = async (...keys: string[]) => {
    for (const k of keys) {
      r.stdin.write(k)
      await tick()
    }
  }
  const frame = () => r.lastFrame() ?? ''
  return { ...r, type, frame, store }
}

/** From the start: focus the sidebar, move to build-42, show it. The main pane has the focus. */
async function openSession(app: ReturnType<typeof start>) {
  await app.type(KEY.tab, KEY.down, KEY.enter)
  expect(app.frame()).toContain(`coop · ${FIX_SID} · transcript`)
}

/** The 1-based terminal row of the first frame line that contains `text`. */
function rowOf(app: ReturnType<typeof start>, text: string): number {
  const row =
    app
      .frame()
      .split('\n')
      .findIndex((l) => l.includes(text)) + 1
  expect(row).toBeGreaterThan(0)
  return row
}

beforeEach(() => {
  calls = []
})

describe('the TUI', () => {
  test('shows the sidebar, the transcript of all traffic, and the hints', () => {
    const app = start()
    const f = app.frame()
    expect(f).toContain('SESSIONS')
    expect(f).toContain('all traffic')
    expect(f).toContain('build-42')
    expect(f).toContain('coop · all sessions · transcript')
    expect(f).toContain('alice@mac-1 → bob@vps-2')
    expect(f).toContain('What is the shape of GET /users?')
    expect(f).toContain('m message · r reply · a attention')
    app.unmount()
  })

  test('the sidebar: enter shows a session, moving points, enter on an agent opens it', async () => {
    const app = start()
    await openSession(app)
    expect(app.frame()).toContain('AGENTS · build-42')
    // Down past docs (a session row: pointing at it changes nothing) to bob.
    await app.type(KEY.tab, KEY.down, KEY.down, KEY.down)
    expect(app.frame()).toContain(`coop · ${FIX_SID} · transcript`)
    await app.type(KEY.enter)
    expect(app.frame()).toContain('agent bob@vps-2 · esc back')
    expect(app.frame()).toContain('directory')
    await app.type(KEY.esc)
    expect(app.frame()).toContain(`coop · ${FIX_SID} · transcript`)
    app.unmount()
  })

  test('views: 1 2 3, and :seq for the sequence diagram', async () => {
    const app = start()
    await openSession(app)
    await app.type('2')
    expect(app.frame()).toContain('OPEN ASKS (1)')
    await app.type('3')
    expect(app.frame()).toContain('MEDIAN MS UNTIL THE MESSAGE REACHED')
    await app.type(':', ...'seq', KEY.enter)
    expect(app.frame()).toContain(`${FIX_SID} · sequence`)
    expect(app.frame()).toContain('├──')
    await app.type(':', ...'seq', KEY.enter)
    expect(app.frame()).toContain(`${FIX_SID} · transcript`)
    await app.type('1')
    expect(app.frame()).toContain('What is the shape of GET /users?')
    app.unmount()
  })

  test('composes to all, to one agent with tab, and with @name', async () => {
    const app = start()
    await openSession(app)
    await app.type('m')
    expect(app.frame()).toContain('to all (tab: next) › ')
    await app.type(...'hello all', KEY.enter)
    await app.type('m', KEY.tab)
    expect(app.frame()).toContain('to alice@mac-1 (tab: next) › ')
    await app.type(...'hi alice', KEY.enter)
    await app.type('m', ...'@bob stop', KEY.enter)
    expect(calls).toEqual([
      `send ${FIX_SID} all hello all`,
      `send ${FIX_SID} alice@mac-1 hi alice`,
      `send ${FIX_SID} bob@vps-2 stop`,
    ])
    expect(app.frame()).toContain('sent #99')
    app.unmount()
  })

  test('r replies to the selected message, to its sender, in its thread', async () => {
    const app = start()
    await openSession(app)
    // While following, the newest message (#18, carol to bob) is under the cursor.
    await app.type('r')
    expect(app.frame()).toContain('reply to #18 from carol@mac-3 › ')
    await app.type(...'ask bob first', KEY.enter)
    expect(calls).toEqual([`send ${FIX_SID} carol@mac-3 ask bob first ↩18`])
    app.unmount()
  })

  test('alt+enter adds a line; a paste with line breaks does not send', async () => {
    const app = start()
    await openSession(app)
    await app.type('m', ...'line one', KEY.altEnter, ...'line two', KEY.enter)
    expect(calls).toEqual([`send ${FIX_SID} all line one\nline two`])
    await app.type('m', 'first\r\nsecond')
    expect(calls).toHaveLength(1)
    expect(app.frame()).toContain('second▏')
    await app.type(KEY.enter)
    expect(calls[1]).toBe(`send ${FIX_SID} all first\nsecond`)
    app.unmount()
  })

  test('a walks through what needs the operator', async () => {
    const app = start()
    await openSession(app)
    expect(app.frame()).toContain('⚑ 1 for you · 1 ask waiting 4m · 1 blocked')
    await app.type('a')
    expect(app.frame()).toContain('for you: #17 from bob@vps-2')
    await app.type('a')
    expect(app.frame()).toContain('carol@mac-3 waits for bob@vps-2 (ask #18)')
    await app.type('a')
    expect(app.frame()).toContain('bob@vps-2 is blocked: waiting for CI')
    expect(app.frame()).toContain('agent bob@vps-2 · esc back')
    app.unmount()
  })

  test('commands: new, close with confirmation, delete needs a closed session', async () => {
    const app = start()
    await openSession(app)
    await app.type(':', ...'delete', KEY.enter)
    expect(app.frame()).toContain('close the session first')
    await app.type(':', ...'close', KEY.enter)
    expect(app.frame()).toContain(`close ${FIX_SID}? its agents are disconnected`)
    await app.type('y')
    await app.type(':', ...'bogus', KEY.enter)
    expect(app.frame()).toContain('unknown command :bogus')
    // A new session is shown at once; the store learns of it from the feed.
    await app.type(':', ...'new demo', KEY.enter)
    expect(app.frame()).toContain('coop · demo · transcript')
    expect(calls).toEqual([`close ${FIX_SID}`, 'create demo'])
    app.unmount()
  })

  test('commands: tab completes the command and the agent; kick asks first', async () => {
    const app = start()
    await openSession(app)
    await app.type(':', ...'ki', KEY.tab)
    expect(app.frame()).toContain(':kick ▏')
    await app.type('b', KEY.tab)
    expect(app.frame()).toContain(':kick bob@vps-2▏')
    await app.type(KEY.enter)
    expect(app.frame()).toContain(`remove bob@vps-2 from ${FIX_SID}? (y/n)`)
    await app.type('y')
    expect(calls).toEqual([`kick ${FIX_SID} bob@vps-2`])
    const d = derive(app.store, FIX_SID)
    expect(complete('', d.agents)).toBe('')
    expect(complete('cl', d.agents)).toBe('close ')
    expect(complete('filter a', d.agents)).toBe('filter alice@mac-1')
    app.unmount()
  })

  // Found in use: an agent removed by mistake had no way back.
  test('commands: allow lets a removed agent back in', async () => {
    const store = storeOf(fixture())
    store.apply([
      { kind: 'kick', key: `${FIX_SID}.kick.mac-1.alice`, record: { at: '2026-09-30T12:04:00Z' } },
    ])
    const app = start(store)
    await openSession(app)
    expect(app.frame()).toContain('removed')
    await app.type(':', ...'allow alice', KEY.enter)
    expect(app.frame()).toContain(`allow alice@mac-1 back into ${FIX_SID}? (y/n)`)
    await app.type('y')
    expect(calls).toEqual([`unkick ${FIX_SID} alice@mac-1`])
    app.unmount()
  })

  test(':withdraw asks first; n cancels, y withdraws', async () => {
    const app = start()
    await openSession(app)
    await app.type(':', ...'withdraw', KEY.enter)
    expect(app.frame()).toContain('withdraw message #18 from carol@mac-3? (y/n)')
    await app.type('n')
    expect(app.frame()).toContain('cancelled')
    await app.type(':', ...'withdraw #7', KEY.enter, 'y')
    expect(calls).toEqual([`redact ${FIX_SID} 7`])
    app.unmount()
  })

  test('shows the help with ? and closes it with esc', async () => {
    const app = start()
    await app.type('?')
    expect(app.frame()).toContain('COMMANDS')
    expect(app.frame()).toContain('help · esc back')
    await app.type(KEY.esc)
    expect(app.frame()).not.toContain('help · esc back')
    expect(app.frame()).toContain('all sessions · transcript')
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

  test('a click on the sidebar shows a session; a second click on a message opens it', async () => {
    const app = start()
    // Sidebar rows start at terminal row 2: SESSIONS, all traffic, build-42.
    await app.type(click(5, 4))
    expect(app.frame()).toContain(`coop · ${FIX_SID} · transcript`)
    const row = rowOf(app, 'alice@mac-1 → bob@vps-2')
    await app.type(click(60, row))
    expect(app.frame()).not.toContain('follow ●')
    await app.type(click(60, row))
    expect(app.frame()).toContain('message #7 · esc back')
    expect(app.frame()).toContain('REACHED')
    app.unmount()
  })

  test('the wheel scrolls the transcript', async () => {
    const app = start()
    await openSession(app)
    await app.type(wheel('up', 60, 6))
    expect(app.frame()).not.toContain('follow ●')
    await app.type(wheel('down', 60, 6), wheel('down', 60, 6), wheel('down', 60, 6))
    expect(app.frame()).toContain('follow ●')
    app.unmount()
  })

  // Found in the end-to-end run: keys sent faster than a repaint acted on old state.
  test('keys that arrive together act on the latest state', async () => {
    const app = start()
    for (const k of [KEY.tab, KEY.down, KEY.enter, 'm', 'h', 'i', KEY.enter]) app.stdin.write(k)
    await tick()
    expect(app.frame()).toContain(`coop · ${FIX_SID} · transcript`)
    expect(calls).toEqual([`send ${FIX_SID} all hi`])
    app.unmount()
  })

  test('enter opens the message details; esc closes them', async () => {
    const app = start()
    await openSession(app)
    await app.type(KEY.up, KEY.up, KEY.enter)
    expect(app.frame()).toContain('message #16 · esc back')
    expect(app.frame()).toContain('REACHED')
    await app.type(KEY.esc)
    expect(app.frame()).toContain(`coop · ${FIX_SID} · transcript`)
    app.unmount()
  })

  test('[ hides and shows the sidebar', async () => {
    const app = start()
    await app.type('[')
    expect(app.frame()).not.toContain('SESSIONS')
    await app.type('[')
    expect(app.frame()).toContain('SESSIONS')
    app.unmount()
  })

  // The hub keeps a message for an agent that left and replays it when the agent returns.
  test('a message to an agent that left is sent, and the operator is told it is away', async () => {
    const store = storeOf(fixture())
    store.apply([{ kind: 'presence', key: `${FIX_SID}.vps-2.bob`, record: undefined }])
    const app = start(store)
    await openSession(app)
    await app.type('m', ...'@bob hi', KEY.enter)
    expect(calls).toEqual([`send ${FIX_SID} bob@vps-2 hi`])
    expect(app.frame()).toContain('bob@vps-2 is away')
    app.unmount()
  })
})
