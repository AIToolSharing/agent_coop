import { fc, test } from '@fast-check/vitest'
import { describe, expect } from 'vitest'
import {
  type BusEvent,
  decode,
  decodeBusEvent,
  encode,
  MAX_TEXT,
  MsgPayload,
  PresenceRecord,
  SessionRecord,
  TokenRecord,
  toWire,
} from '../src/index.js'
import { busEvent, presenceRecord, sessionRecord, tokenRecord } from './arb.js'

function withoutSeq(e: BusEvent) {
  const { seq: _seq, ...rest } = e
  return rest
}

describe('bus events', () => {
  test.prop([busEvent])('decodeBusEvent inverts toWire', (e) => {
    const w = toWire(withoutSeq(e))
    expect(decodeBusEvent(w.subject, w.data, e.seq)).toEqual(e)
  })

  test.prop([fc.string(), fc.uint8Array(), fc.integer({ min: 1 })])(
    'decodeBusEvent never throws on junk',
    (subject, data, seq) => {
      decodeBusEvent(subject, data, seq)
    },
  )

  test('a payload with a from field is rejected: the sender only comes from the subject', () => {
    const data = new TextEncoder().encode(
      JSON.stringify({ to: 'all', text: 'hi', sent_at: '2026-09-30T00:00:00.000Z', from: 'x@y' }),
    )
    expect(decodeBusEvent('coop.s.msg.m.a', data, 1)).toBeUndefined()
  })
})

describe('records', () => {
  test.prop([sessionRecord])('SessionRecord round trips', (r) => {
    expect(decode(SessionRecord, encode(SessionRecord, r))).toEqual(r)
  })
  test.prop([presenceRecord])('PresenceRecord round trips', (r) => {
    expect(decode(PresenceRecord, encode(PresenceRecord, r))).toEqual(r)
  })
  test.prop([tokenRecord])('TokenRecord round trips', (r) => {
    expect(decode(TokenRecord, encode(TokenRecord, r))).toEqual(r)
  })
})

describe('text limits', () => {
  const at = '2026-09-30T00:00:00.000Z'
  test.each([
    ['', false],
    ['x', true],
    ['x'.repeat(MAX_TEXT), true],
    ['x'.repeat(MAX_TEXT + 1), false],
  ])('text of length %#', (text, ok) => {
    expect(MsgPayload.safeParse({ to: 'all', text, sent_at: at }).success).toBe(ok)
  })
})
