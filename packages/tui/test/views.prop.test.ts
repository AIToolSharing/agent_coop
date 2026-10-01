import { BROADCAST, OPERATOR } from '@coop/core'
import { fc, test } from '@fast-check/vitest'
import { describe, expect } from 'vitest'
import { derive } from '../src/model.js'
import { attention } from '../src/views/attention.js'
import type { ViewOptions } from '../src/views/common.js'
import { renderAgent, renderAgents, renderMessage } from '../src/views/inspect.js'
import { len, plain, type Rendered } from '../src/views/line.js'
import { renderLog } from '../src/views/log.js'
import { renderMatrix } from '../src/views/matrix.js'
import { laneNames, lanes, renderSequence } from '../src/views/sequence.js'
import { renderSidebar, summarize } from '../src/views/sidebar.js'
import { renderThreads } from '../src/views/threads.js'
import { renderTranscript } from '../src/views/transcript.js'
import { SID, scenario, storeOf } from './scenario.js'

const NOW = Date.parse('2026-09-30T01:00:00.000Z')

describe('every view fits its width', () => {
  test.prop([scenario, fc.integer({ min: 30, max: 180 }), fc.boolean()])(
    'each line is exactly the width, and ids match lines',
    (sc, width, system) => {
      const store = storeOf(sc.updates)
      const d = derive(store, SID)
      const o: ViewOptions = { width, now: NOW, system }
      const views: Rendered[] = [
        renderLog(d, o),
        renderTranscript(d, o),
        renderSidebar(summarize(store, NOW), d.agents, { sid: SID, cursor: 1 }, width, NOW),
        renderSequence(d, o),
        renderThreads(d, o),
        renderMatrix(d, o),
        renderAgent(d, d.agents[0]?.address, o),
        renderMessage(d, d.messages[0]?.id, o),
        renderAgents(d.agents, o),
      ]
      for (const v of views) {
        expect(v.ids.length).toBe(v.lines.length)
        for (const l of v.lines) expect(len(plain(l))).toBeLessThanOrEqual(width)
      }
    },
  )
})

describe('transcript', () => {
  test.prop([scenario, fc.integer({ min: 30, max: 180 }), fc.boolean()])(
    'every visible message has exactly one selectable line, in stream order',
    (sc, width, system) => {
      const d = derive(storeOf(sc.updates), SID)
      const ids = renderTranscript(d, { width, now: NOW, system }).ids.filter(
        (x) => x !== undefined,
      )
      expect(ids).toEqual(d.messages.map((m) => m.id))
    },
  )

  test.prop([scenario])('what needs the operator is for the operator, stale, or blocked', (sc) => {
    const d = derive(storeOf(sc.updates), SID)
    const now = Date.parse('2026-09-30T02:00:00.000Z')
    for (const item of attention(d, now)) {
      if (item.kind === 'for_you') {
        const m = d.messages.find((x) => x.id === item.id)
        expect(m?.to).toBe(OPERATOR)
        expect(d.messages.some((x) => x.from === OPERATOR && x.reply_to === item.id)).toBe(false)
      } else if (item.kind === 'ask') {
        expect(d.openAsks.some((q) => q.id === item.id)).toBe(true)
      } else {
        expect(d.agents.find((a) => a.address === item.address)).toMatchObject({
          online: true,
          state: 'blocked',
        })
      }
    }
  })
})

describe('sequence diagram', () => {
  test.prop([scenario, fc.integer({ min: 60, max: 180 })])(
    'each direct message has its arrow head next to the target lane, pointing at it',
    (sc, width) => {
      const d = derive(storeOf(sc.updates), SID)
      const L = lanes(laneNames(d), width)
      const r = renderSequence(d, { width, now: NOW })
      r.ids.forEach((id, i) => {
        const m = d.messages.find((x) => x.id === id)
        if (m === undefined || m.to === BROADCAST) return
        const a = L.names.indexOf(m.from)
        const b = L.names.indexOf(m.to)
        const xa = L.x[a] ?? 0
        const xb = L.x[b] ?? 0
        if (Math.abs(xb - xa) < 3) return
        const row = [...plain(r.lines[i] ?? [])]
        if (xb > xa) {
          expect(row[xb - 1]).toBe('►')
          expect(row[xa]).toBe('├')
        } else {
          expect(row[xb + 1]).toBe('◄')
          expect(row[xa]).toBe('┤')
        }
      })
    },
  )

  test.prop([scenario, fc.integer({ min: 60, max: 180 })])(
    'every visible message appears once, in stream order',
    (sc, width) => {
      const d = derive(storeOf(sc.updates), SID)
      const ids = renderSequence(d, { width, now: NOW }).ids.filter((x) => x !== undefined)
      const expected = d.messages.map((m) => m.id)
      expect(ids).toEqual(expected)
    },
  )
})
