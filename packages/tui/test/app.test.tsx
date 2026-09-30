import { formatAddress } from '@coop/core'
import { render } from 'ink-testing-library'
import { beforeEach, describe, expect, test } from 'vitest'
import { App, type OperatorApi } from '../src/app.js'
import { FIX_SID, fixture, NOW } from './fixture.js'
import { storeOf } from './scenario.js'

const KEY = { tab: '\t', enter: '\r', down: '\u001B[B', up: '\u001B[A', esc: '\u001B' }
const tick = () => new Promise((r) => setTimeout(r, 30))

let calls: string[]
const op: OperatorApi = {
  createSession: async (s) => void calls.push(`create ${s}`),
  closeSession: async (s) => void calls.push(`close ${s}`),
  reopenSession: async (s) => void calls.push(`reopen ${s}`),
  deleteSession: async (s) => void calls.push(`delete ${s}`),
  kick: async (s, t) => void calls.push(`kick ${s} ${formatAddress(t)}`),
  redact: async (s, id) => {
    calls.push(`redact ${s} ${id}`)
    return true
  },
  send: async (s, to, text) => {
    calls.push(`send ${s} ${typeof to === 'string' ? to : formatAddress(to)} ${text}`)
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
    expect(app.frame()).toContain('MEDIAN DELIVERY')
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
    expect(app.frame()).toContain('withdraw message #17')
    await app.type('n')
    expect(calls).toEqual([])
    await app.type(KEY.up, 'r', 'y')
    expect(calls).toEqual([`redact ${FIX_SID} 16`])
    app.unmount()
  })

  test('creates, closes, and refuses to delete an open session', async () => {
    const app = start()
    await openSession(app)
    await app.type('D')
    expect(app.frame()).toContain('close the session first')
    await app.type('c')
    await app.type('n', ...'demo', KEY.enter)
    expect(calls).toEqual([`close ${FIX_SID}`, 'create demo'])
    app.unmount()
  })

  test('opens the message inspector with enter', async () => {
    const app = start()
    await openSession(app)
    await app.type(KEY.up, KEY.up, KEY.up, KEY.enter)
    const f = app.frame()
    expect(f).toContain('6 message')
    expect(f).toContain('DELIVERY')
    app.unmount()
  })
})
