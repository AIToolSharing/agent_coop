package shim

import (
	"sync/atomic"
	"testing"
	"time"
)

// The rules of the scheduler: one nudge until the agenda is read, and never two within the
// gap, however many items arrive.
func TestTheNudgerSendsOneNudgeUntilReadAndKeepsTheGap(t *testing.T) {
	const gap = 200 * time.Millisecond
	var fired atomic.Int32
	var at []time.Time
	done := make(chan struct{}, 10)
	n := &nudger{gap: gap, now: time.Now, fire: func() {
		at = append(at, time.Now())
		fired.Add(1)
		done <- struct{}{}
	}}
	defer n.stop()
	for range 50 {
		n.queued()
	}
	<-done
	time.Sleep(gap + 50*time.Millisecond)
	if fired.Load() != 1 {
		t.Fatalf("%d nudges before the agenda was read, want 1", fired.Load())
	}
	// Read at once: the next item waits for the gap.
	n.read()
	n.queued()
	n.queued()
	select {
	case <-done:
	case <-time.After(5 * gap):
		t.Fatal("no second nudge after the gap")
	}
	if d := at[1].Sub(at[0]); d < gap {
		t.Fatalf("two nudges %v apart, gap %v", d, gap)
	}
	time.Sleep(gap)
	if fired.Load() != 2 {
		t.Fatalf("%d nudges, want 2", fired.Load())
	}
}
