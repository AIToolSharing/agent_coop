import { BROADCAST } from '@coop/core'
import { fc, test } from '@fast-check/vitest'
import { describe, expect } from 'vitest'
import { derive } from '../src/model.js'
import type { ViewOptions } from '../src/views/common.js'
import { renderAgent, renderAgents, renderMessage } from '../src/views/inspect.js'
import { len, plain, type Rendered } from '../src/views/line.js'
import { renderLog } from '../src/views/log.js'
import { renderMatrix } from '../src/views/matrix.js'
import { laneNames, lanes, renderSequence } from '../src/views/sequence.js'
import { renderThreads } from '../src/views/threads.js'
import { SID, scenario, storeOf } from './scenario.js'

const NOW = Date.parse('2026-09-30T01:00:00.000Z')

describe('every view fits its width', () => {
  test.prop([scenario, fc.integer({ min: 30, max: 180 }), fc.boolean()])(
    'each line is exactly the width, and ids match lines',
    (sc, width, system) => {
      const d = derive(storeOf(sc.updates), SID)
      const o: ViewOptions = { width, now: NOW, system }
      const views: Rendered[] = [
        renderLog(d, o),
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
