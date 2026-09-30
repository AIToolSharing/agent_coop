// The exact layout of each view matters to the operator, so it is pinned by snapshots.
import { describe, expect, test } from 'vitest'
import { derive } from '../src/model.js'
import type { ViewOptions } from '../src/views/common.js'
import { renderAgent, renderAgents, renderMessage } from '../src/views/inspect.js'
import { plain, type Rendered } from '../src/views/line.js'
import { renderLog } from '../src/views/log.js'
import { renderMatrix } from '../src/views/matrix.js'
import { renderSequence } from '../src/views/sequence.js'
import { renderSessions, summarize } from '../src/views/sessions.js'
import { renderThreads } from '../src/views/threads.js'
import { FIX_SID, fixture, NOW } from './fixture.js'
import { storeOf } from './scenario.js'

const text = (r: Rendered) => `\n${r.lines.map((l) => plain(l).trimEnd()).join('\n')}\n`
const store = storeOf(fixture())
const d = derive(store, FIX_SID)
const o: ViewOptions = { width: 110, now: NOW, system: true }

describe('views of the fixture session', () => {
  test('1 log', () => expect(text(renderLog(d, o))).toMatchSnapshot())
  test('2 sequence', () => expect(text(renderSequence(d, o))).toMatchSnapshot())
  test('3 threads', () => expect(text(renderThreads(d, o))).toMatchSnapshot())
  test('4 matrix', () => expect(text(renderMatrix(d, o))).toMatchSnapshot())
  test('5 agent', () => expect(text(renderAgent(d, 'bob@vps-2', o))).toMatchSnapshot())
  test('6 message', () => expect(text(renderMessage(d, '7', o))).toMatchSnapshot())
  test('agents pane', () => expect(text(renderAgents(d.agents, o))).toMatchSnapshot())
  test('sessions pane', () =>
    expect(text(renderSessions(summarize(store, NOW), FIX_SID, 28))).toMatchSnapshot())
})
