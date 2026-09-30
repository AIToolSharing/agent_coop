import { ErrorCode } from '@coop/core'
import { fc, test } from '@fast-check/vitest'
import { expect } from 'vitest'
import { errorText, UNREACHABLE } from '../src/client.js'
import { FORBIDDEN_RE } from '../src/server.js'

test('401 and server errors get fixed texts; the service text never passes through', () => {
  const leaky = { error: 'unauthorized' as const, message: 'missing or invalid token' }
  expect(errorText(401, leaky)).toBe('not in a session: this machine is not allowed to join')
  expect(errorText(429, { error: 'rate_limited', message: 'too many joins' })).toContain('too many')
  expect(errorText(503, { error: 'unavailable', message: 'internal error' })).toBe(UNREACHABLE)
  expect(errorText(404, undefined)).toBe(UNREACHABLE)
})

test.prop([
  fc.constantFrom(401, 500, 502, 503),
  fc.constantFrom(...ErrorCode.options),
  fc.string(),
])('fixed texts never contain a forbidden word', (status, error, message) => {
  expect(FORBIDDEN_RE.test(errorText(status, { error, message: `${message} token stream` }))).toBe(
    false,
  )
})
