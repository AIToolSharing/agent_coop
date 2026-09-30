import { fc, test } from '@fast-check/vitest'
import { describe, expect } from 'vitest'
import {
  ApiMessage,
  type BusEvent,
  deliveryFor,
  isVisible,
  sameAddress,
  toApiMessage,
} from '../src/index.js'
import { address, busEvent, msgEvent } from './arb.js'

const never = () => false
const always = () => true

describe('delivery rules', () => {
  test.prop([msgEvent, address])(
    'a message is delivered iff sent by another to all or to me',
    (e, me) => {
      const fromMe = typeof e.from !== 'string' && sameAddress(e.from, me)
      const toMe = e.to === 'all' || (typeof e.to !== 'string' && sameAddress(e.to, me))
      expect(deliveryFor(e, me, never)).toBe(!fromMe && toMe ? 'message' : undefined)
    },
  )

  test.prop([msgEvent, address])('my own message is never delivered to me', (e, me) => {
    expect(deliveryFor({ ...e, from: me }, me, always)).toBeUndefined()
  })

  test.prop([msgEvent, address, address, address])(
    'a direct message between two others is neither delivered to nor visible to me',
    (e, a, b, me) => {
      fc.pre(!sameAddress(a, me) && !sameAddress(b, me))
      const dm: BusEvent = { ...e, from: a, to: b }
      expect(deliveryFor(dm, me, always)).toBeUndefined()
      expect(isVisible(dm, me)).toBe(false)
    },
  )

  test.prop([busEvent, address])('only messages are visible', (e, me) => {
    if (e.kind !== 'msg') expect(isVisible(e, me)).toBe(false)
  })

  test.prop([busEvent, address])('activity events are never delivered', (e, me) => {
    if (e.kind === 'evt') expect(deliveryFor(e, me, always)).toBeUndefined()
  })

  test.prop([address, address, fc.integer({ min: 1 })])(
    'a kick is a notice only for its target',
    (target, me, seq) => {
      const e: BusEvent = { kind: 'kick', seq, sid: 's', target, at: '2026-09-30T00:00:00.000Z' }
      expect(deliveryFor(e, me, never)).toBe(sameAddress(target, me) ? 'notice' : undefined)
    },
  )

  test.prop([address, fc.boolean()])(
    'a redact is a notice only if I got the message',
    (me, got) => {
      const e: BusEvent = {
        kind: 'redact',
        seq: 9,
        sid: 's',
        id: '3',
        at: '2026-09-30T00:00:00.000Z',
      }
      expect(deliveryFor(e, me, (id) => got && id === '3')).toBe(got ? 'notice' : undefined)
    },
  )
})

describe('API form', () => {
  test.prop([msgEvent])('toApiMessage always gives a valid ApiMessage', (e) => {
    expect(ApiMessage.safeParse(toApiMessage(e)).success).toBe(true)
  })
})
