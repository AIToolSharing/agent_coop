package hub

import (
	"sync"
	"time"
)

// limiter is a token bucket per key. Each key starts full with burst tokens and gets perSecond
// tokens back per second, up to burst. take spends one token or gives false.
type limiter struct {
	burst, perSecond float64
	now              func() time.Time

	mu      sync.Mutex
	buckets map[string]bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(l Limit, now func() time.Time) *limiter {
	return &limiter{burst: float64(l.Burst), perSecond: l.PerSecond, now: now, buckets: map[string]bucket{}}
}

func (l *limiter) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = bucket{tokens: l.burst, at: t}
	}
	tokens := min(l.burst, b.tokens+t.Sub(b.at).Seconds()*l.perSecond)
	allowed := tokens >= 1
	if allowed {
		tokens--
	}
	l.buckets[key] = bucket{tokens: tokens, at: t}
	return allowed
}
