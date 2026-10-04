package hub

import (
	"strconv"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

// The trace of an agent lives in the hub's memory, so it must have a limit that no agent can
// pass: the log holds the newest traceRing items, in the order they came.
func TestTraceLogKeepsTheNewestItemsInOrder(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 3*traceRing).Draw(rt, "n")
		var l traceLog
		for i := 1; i <= n; i++ {
			l.add(wire.TraceItem{N: int64(i), Kind: wire.TraceToolStart})
		}
		want := min(n, traceRing)
		if len(l.items) != want {
			rt.Fatalf("%d items after %d adds, want %d", len(l.items), n, want)
		}
		for i, it := range l.items {
			if it.N != int64(n-want+i+1) {
				rt.Fatalf("item %d has number %d, want %d", i, it.N, n-want+i+1)
			}
		}
		if l.last() != int64(n) {
			rt.Fatalf("last %d, want %d", l.last(), n)
		}
	})
}

// A hook reports the newest words of the agent again at each tool call. The hub must know a
// repeat also after the words left the log: else old words come back as new ones.
func TestTraceLogKnowsWordsThatLeftTheLog(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		var l traceLog
		var ids []string
		for i := range rapid.IntRange(0, 3*traceSaid).Draw(rt, "says") {
			id := "u" + strconv.Itoa(i)
			ids = append(ids, id)
			l.add(wire.TraceItem{Kind: wire.TraceSay, ID: id})
			// Tool calls between the words push the words from the log.
			for range rapid.IntRange(0, 2).Draw(rt, "calls") * traceRing {
				l.add(wire.TraceItem{Kind: wire.TraceToolStart, ID: id})
			}
		}
		if len(l.said) > traceSaid {
			rt.Fatalf("%d ids, limit %d", len(l.said), traceSaid)
		}
		for i, id := range ids {
			if want := i >= len(ids)-traceSaid; l.has(id) != want {
				rt.Fatalf("has(%s) = %v, want %v (%d ids)", id, l.has(id), want, len(ids))
			}
		}
		if l.has("") || l.has("t-unknown") {
			rt.Fatal("the log knows an id that it did not get")
		}
	})
}

// The record of changed files has a limit too. A file that the agent changes stays in the
// record with the count of its changes; when the record is full, the file with the oldest
// last change goes.
func TestTraceLogCountsChangedFilesWithinItsLimit(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		paths := rapid.SliceOfN(rapid.IntRange(0, traceFiles+50), 0, 3*traceFiles).Draw(rt, "paths")
		var l traceLog
		count := map[string]int{}
		for i, p := range paths {
			path := "f" + strconv.Itoa(p)
			at := strconv.Itoa(1_000_000 + i) // times that sort as text
			_, had := l.files[path]
			if !had {
				count[path] = 0
			}
			count[path]++
			f := l.touch(path, at)
			if f.Path != path || f.At != at || f.Count != count[path] {
				rt.Fatalf("touch %d gave %+v, want %s count %d at %s", i, f, path, count[path], at)
			}
			if len(l.files) > traceFiles {
				rt.Fatalf("%d files, limit %d", len(l.files), traceFiles)
			}
			for other, x := range l.files {
				if x.Count != count[other] {
					rt.Fatalf("%s has count %d, want %d", other, x.Count, count[other])
				}
			}
		}
	})
}
