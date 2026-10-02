package hub

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Any schedule of calls: the number of allowed calls up to time t is at most
// burst + perSecond * t, and a key never borrows from another key.
func TestLimiterNeverAllowsMoreThanBurstPlusRefill(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		burst := rapid.IntRange(1, 50).Draw(rt, "burst")
		perSecond := rapid.Float64Range(0.1, 100).Draw(rt, "perSecond")
		var clock time.Time
		lim := newLimiter(Limit{Burst: burst, PerSecond: perSecond}, func() time.Time { return clock })
		allowed := map[string]int{}
		steps := rapid.IntRange(0, 300).Draw(rt, "steps")
		for range steps {
			dt := rapid.IntRange(0, 2000).Draw(rt, "dt")
			key := rapid.SampledFrom([]string{"a", "b"}).Draw(rt, "key")
			clock = clock.Add(time.Duration(dt) * time.Millisecond)
			if lim.take(key) {
				allowed[key]++
			}
			limit := float64(burst) + perSecond*clock.Sub(time.Time{}).Seconds() + 1e-9
			if float64(allowed[key]) > limit {
				rt.Fatalf("key %s: %d allowed, limit %.3f", key, allowed[key], limit)
			}
		}
	})
}

func TestLimiterAFreshKeyAllowsExactlyBurstCallsAtOnce(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		burst := rapid.IntRange(1, 50).Draw(rt, "burst")
		lim := newLimiter(Limit{Burst: burst, PerSecond: 1}, func() time.Time { return time.Time{} })
		n := 0
		for range burst + 3 {
			if lim.take("k") {
				n++
			}
		}
		if n != burst {
			rt.Fatalf("%d allowed, want %d", n, burst)
		}
	})
}
