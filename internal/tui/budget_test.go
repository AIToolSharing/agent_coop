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
