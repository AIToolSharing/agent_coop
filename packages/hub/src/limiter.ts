/**
 * Token bucket per key. Each key starts full with `burst` tokens and gets `perSecond` tokens back
 * per second, up to `burst`. `take` spends one token or returns false.
 */
export class RateLimiter {
  private readonly buckets = new Map<string, { tokens: number; at: number }>()

  constructor(
    private readonly burst: number,
    private readonly perSecond: number,
    private readonly now: () => number = Date.now,
  ) {}

  take(key: string): boolean {
    const t = this.now()
    const b = this.buckets.get(key) ?? { tokens: this.burst, at: t }
    const tokens = Math.min(this.burst, b.tokens + ((t - b.at) / 1000) * this.perSecond)
    const ok = tokens >= 1
    this.buckets.set(key, { tokens: ok ? tokens - 1 : tokens, at: t })
    return ok
  }
}
