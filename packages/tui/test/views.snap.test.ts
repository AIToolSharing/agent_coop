// The exact layout of each view matters to the operator, so it is pinned by snapshots.
import { describe, expect, test } from 'vitest'
import { derive } from '../src/model.js'
import { attention, renderAttention } from '../src/views/attention.js'
import type { ViewOptions } from '../src/views/common.js'
import { renderAgent, renderMessage } from '../src/views/inspect.js'
import { plain, plain as plainLine, type Rendered } from '../src/views/line.js'
import { renderMatrix } from '../src/views/matrix.js'
import { renderSequence } from '../src/views/sequence.js'
import { renderSidebar, summarize } from '../src/views/sidebar.js'
import { renderThreads } from '../src/views/threads.js'
import { renderTranscript } from '../src/views/transcript.js'
import { FIX_SID, fixture, NOW } from './fixture.js'
import { storeOf } from './scenario.js'

const text = (r: Rendered) => `\n${r.lines.map((l) => plain(l).trimEnd()).join('\n')}\n`
const store = storeOf(fixture())
const d = derive(store, FIX_SID)
const o: ViewOptions = { width: 110, now: NOW, system: true }

describe('views of the fixture session', () => {
  test('2 sequence', () => expect(text(renderSequence(d, o))).toMatchSnapshot())
  test('3 threads', () => expect(text(renderThreads(d, o))).toMatchSnapshot())
  test('4 matrix', () => expect(text(renderMatrix(d, o))).toMatchSnapshot())
  test('5 agent', () => expect(text(renderAgent(d, 'bob@vps-2', o))).toMatchSnapshot())
  test('6 message', () => expect(text(renderMessage(d, '7', o))).toMatchSnapshot())
  test('transcript', () => expect(text(renderTranscript(d, o))).toMatchSnapshot())
  test('transcript, narrow, without system lines', () =>
    expect(text(renderTranscript(d, { ...o, width: 60, system: false }))).toMatchSnapshot())
  test('sidebar', () =>
    expect(
      text(renderSidebar(summarize(store, NOW), d.agents, { sid: FIX_SID, cursor: 2 }, 30, NOW)),
    ).toMatchSnapshot())
  test('attention', () => {
    const items = attention(d, NOW)
    expect(items.map((i) => i.kind)).toEqual(['for_you', 'ask', 'blocked'])
    expect(plainLine(renderAttention(items, NOW) ?? [])).toBe(
      '⚑ 1 for you · 1 ask waiting 4m · 1 blocked   a: next',
    )
  })
})
