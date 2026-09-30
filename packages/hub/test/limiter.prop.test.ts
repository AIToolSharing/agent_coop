import { fc, test } from '@fast-check/vitest'
import { expect } from 'vitest'
import { RateLimiter } from '../src/limiter.js'

// Any schedule of calls: the number of allowed calls up to time t is at most
// burst + perSecond * t / 1000, and a key never borrows from another key.
test.prop([
  fc.integer({ min: 1, max: 50 }),
  fc.double({ min: 0.1, max: 100, noNaN: true }),
  fc.array(fc.tuple(fc.integer({ min: 0, max: 2000 }), fc.constantFrom('a', 'b')), {
    maxLength: 300,
  }),
])('never allows more than burst plus refill', (burst, perSecond, steps) => {
  let t = 0
  const lim = new RateLimiter(burst, perSecond, () => t)
  const allowed = { a: 0, b: 0 }
  for (const [dt, key] of steps) {
    t += dt
    if (lim.take(key)) allowed[key]++
    expect(allowed[key]).toBeLessThanOrEqual(burst + (perSecond * t) / 1000 + 1e-9)
  }
})

test.prop([fc.integer({ min: 1, max: 50 })])(
  'a fresh key allows exactly burst calls at once',
  (burst) => {
    const lim = new RateLimiter(burst, 1, () => 0)
    const results = Array.from({ length: burst + 3 }, () => lim.take('k'))
    expect(results.filter(Boolean).length).toBe(burst)
  },
)
