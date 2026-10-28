package tui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/model/modeltest"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// traffic adds n messages with a state change each, as a long session would.
func traffic(store *model.Store, from, n int) {
	agents := []wire.Address{modeltest.Alice, modeltest.Bob, modeltest.Carol}
	for i := from; i < from+n; i++ {
		seq := int64(1000 + 2*i)
		a := agents[i%3]
		at := modeltest.At(float64(100 + i))
		text := fmt.Sprintf("message %d: %s", i, strings.Repeat("lorem ipsum dolor sit amet ", 1+i%6))
		store.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: seq, SID: modeltest.SID, From: a.String(), To: "all", Text: text, SentAt: at}})
		store.Apply(model.Update{Event: &wire.Event{Kind: wire.EventActivity, Seq: seq + 1, SID: modeltest.SID, From: a.String(), Activity: &wire.Activity{Kind: "state", State: "working", Note: "step", At: at}}})
	}
}

// frames repaints n times while one message arrives per repaint, and gives the mean cost.
func frames(h *harness, n, from int) (perFrame time.Duration, allocPerFrame uint64) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	for i := 0; i < n; i++ {
		traffic(h.app.store, from+i, 1)
		h.app.Update(tickMsg(time.Now()))
		_ = h.app.View()
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	return elapsed / time.Duration(n), (after.TotalAlloc - before.TotalAlloc) / uint64(n)
}

// The budget of the plan: 5000 messages, a repaint well under 2 ms, and a repaint whose cost
// does not grow with the history.
func TestRepaintCostIsIndependentOfTheHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("budget test")
	}
	h := start(t, nil)
	h.app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	h.openSession()
	traffic(h.app.store, 0, 1000)
	small, smallAlloc := frames(h, 300, 1000)
	traffic(h.app.store, 1300, 3700)
	large, largeAlloc := frames(h, 300, 5000)
	t.Logf("repaint with ~1000 messages: %v, %d B; with ~5000 messages: %v, %d B", small, smallAlloc, large, largeAlloc)
	if large > 2*time.Millisecond && !raceEnabled {
		t.Errorf("a repaint with 5000 messages takes %v, budget 2 ms", large)
	}
	if largeAlloc > 2*smallAlloc+64*1024 {
		t.Errorf("allocation per repaint grew with the history: %d B at 1000 messages, %d B at 5000", smallAlloc, largeAlloc)
	}
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	t.Logf("heap in use after 5300 messages: %d MB", m.HeapInuse/1048576)
	if m.HeapInuse > 100*1048576 {
		t.Errorf("heap in use %d MB, budget 100 MB", m.HeapInuse/1048576)
	}
}

// Found in use: the TUI got slow in large sessions. Every reply re-rendered the whole
// transcript, and the threads view and the agent details rendered the whole session on every
// frame. Each of these frames costs the visible lines only now.
func TestRepaintCostInEveryView(t *testing.T) {
	if testing.Short() {
		t.Skip("budget test")
	}
	cases := []struct {
		name  string
		setup func(h *harness)
		frame func(h *harness, i int)
		// rebuilds says that the view walks the session again on each new message (the
		// threads view: a reply goes under its parent). Its time is bounded, its allocation
		// grows with the history.
		rebuilds bool
	}{
		{"a reply to the first message", nil, func(h *harness, i int) {
			seq := int64(900000 + i)
			h.app.store.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: seq, SID: modeltest.SID, From: modeltest.Bob.String(), To: modeltest.Alice.String(), Text: "re", ReplyTo: "1000", SentAt: modeltest.At(float64(9000 + i))}})
		}, false},
		{"the threads view", func(h *harness) { h.keys("2") }, nil, true},
		{"the preview of an agent", func(h *harness) { h.keys("tab", "down", "down", "enter") }, nil, false},
		{"a search that misses", nil, func(h *harness, i int) { h.keys("/", "+zzz", "enter") }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, nil)
			h.app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
			h.openSession()
			traffic(h.app.store, 0, 1000)
			if c.setup != nil {
				c.setup(h)
			}
			measure := func(n, from int) (time.Duration, uint64) {
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				start := time.Now()
				for i := 0; i < n; i++ {
					if c.frame != nil {
						c.frame(h, from+i)
					} else {
						traffic(h.app.store, from+i, 1)
					}
					h.app.Update(tickMsg(time.Now()))
					_ = h.app.View()
				}
				runtime.ReadMemStats(&after)
				return time.Since(start) / time.Duration(n), (after.TotalAlloc - before.TotalAlloc) / uint64(n)
			}
			small, smallAlloc := measure(100, 1000)
			traffic(h.app.store, 1200, 3800)
			large, largeAlloc := measure(100, 5000)
			t.Logf("%s with ~1000 messages: %v, %d B; with ~5000 messages: %v, %d B", c.name, small, smallAlloc, large, largeAlloc)
			if large > 2*time.Millisecond && !raceEnabled {
				t.Errorf("a frame with 5000 messages takes %v, budget 2 ms", large)
			}
			if !c.rebuilds && largeAlloc > 2*smallAlloc+64*1024 {
				t.Errorf("allocation per frame grew with the history: %d B at 1000 messages, %d B at 5000", smallAlloc, largeAlloc)
			}
		})
	}
}
