import { fc, test } from '@fast-check/vitest'
import { describe, expect } from 'vitest'
import {
  buildPresenceKey,
  buildSessionsKey,
  buildSubject,
  formatAddress,
  isAgentName,
  isToken,
  parseAddress,
  parsePresenceKey,
  parseSessionsKey,
  parseSubject,
} from '../src/index.js'
import { address, subject, token } from './arb.js'

describe('subjects', () => {
  test.prop([subject])('parseSubject inverts buildSubject', (s) => {
    expect(parseSubject(buildSubject(s))).toEqual(s)
  })

  test.prop([fc.string()])(
    'parseSubject never throws and only accepts canonical subjects',
    (raw) => {
      const s = parseSubject(raw)
      if (s !== undefined) expect(buildSubject(s)).toBe(raw)
    },
  )

  test.prop([token, token, fc.constantFrom('operator', 'all')])(
    'a subject with a reserved agent name is rejected',
    (sid, machine, agent) => {
      expect(parseSubject(`coop.${sid}.msg.${machine}.${agent}`)).toBeUndefined()
    },
  )
})

describe('addresses', () => {
  test.prop([address])('parseAddress inverts formatAddress', (a) => {
    expect(parseAddress(formatAddress(a))).toEqual(a)
  })

  test.prop([fc.string()])('parseAddress only accepts canonical addresses', (raw) => {
    const a = parseAddress(raw)
    if (a !== undefined) expect(formatAddress(a)).toBe(raw)
  })
})

describe('KV keys', () => {
  test.prop([token])('a session key round trips', (sid) => {
    const k = { kind: 'session' as const, sid }
    expect(parseSessionsKey(buildSessionsKey(k))).toEqual(k)
  })

  test.prop([token, address])(
    'a kick key round trips and is never a session key',
    (sid, target) => {
      const k = { kind: 'kick' as const, sid, target }
      const key = buildSessionsKey(k)
      expect(parseSessionsKey(key)).toEqual(k)
      expect(isToken(key)).toBe(false)
    },
  )

  test.prop([token, address])('a presence key round trips', (sid, agent) => {
    expect(parsePresenceKey(buildPresenceKey({ sid, agent }))).toEqual({ sid, agent })
  })
})

describe('name rules', () => {
  test.each([
    ['operator', false],
    ['all', false],
    ['a.b', false],
    ['Alice', false],
    ['', false],
    ['x'.repeat(65), false],
    ['x'.repeat(64), true],
    ['alice_2-b', true],
  ])('isAgentName(%j) is %s', (name, ok) => {
    expect(isAgentName(name)).toBe(ok)
  })
})
