package model

import (
	"math"
	"sort"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// TraceMax is how many trace items the model keeps for one agent: as many as the hub keeps.
const TraceMax = 300

// Trace is what one agent did at its terminal lately, as the hooks of Claude Code reported
// it: tool calls, words of the agent, prompts. It lives in the hub's memory, not in the
// session's record. The updates may arrive in any order and more than once: the items are
// kept by their number.
type Trace struct {
	// Items are the newest items, oldest first.
	Items []wire.TraceItem
	// Files are the files that the agent changed, by path.
	Files map[string]wire.TraceFile
	// Branch is the git branch of the agent's project directory, or "".
	Branch string
	// top is the highest item number of the update that gave Branch.
	top int64
}

// merge takes one update in.
func (t *Trace) merge(u *wire.TraceUpdate) {
	var top int64
	for _, it := range u.Items {
		top = max(top, it.N)
		i := sort.Search(len(t.Items), func(i int) bool { return t.Items[i].N >= it.N })
		if i < len(t.Items) && t.Items[i].N == it.N {
			continue
		}
		t.Items = append(t.Items, wire.TraceItem{})
		copy(t.Items[i+1:], t.Items[i:])
		t.Items[i] = it
	}
	if over := len(t.Items) - TraceMax; over > 0 {
		t.Items = append(t.Items[:0], t.Items[over:]...)
	}
	if top >= t.top {
		t.Branch, t.top = u.Branch, top
	}
	for _, f := range u.Files {
		if old, ok := t.Files[f.Path]; ok && old.Count >= f.Count {
			continue
		}
		if t.Files == nil {
			t.Files = map[string]wire.TraceFile{}
		}
		t.Files[f.Path] = f
	}
}

// Running gives the tool calls that started and did not end, oldest first. A prompt, or
// words that end a turn, end every tool call before them: a call that the person at the
// terminal stopped reports no end.
func (t *Trace) Running() []wire.TraceItem {
	var open []wire.TraceItem
	for _, it := range t.Items {
		switch {
		case it.Kind == wire.TraceToolStart:
			open = append(open, it)
		case it.Kind == wire.TraceToolEnd:
			for i := range open {
				if open[i].ID == it.ID {
					open = append(open[:i], open[i+1:]...)
					break
				}
			}
		case it.Kind == wire.TracePrompt || it.Kind == wire.TraceSay && it.Final:
			open = nil
		}
	}
	return open
}

// FileList gives the changed files, the one with the newest change first.
func (t *Trace) FileList() []wire.TraceFile {
	out := make([]wire.TraceFile, 0, len(t.Files))
	for _, f := range t.Files {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Act is one trace item with the agent that it is of.
type Act struct {
	Agent *Agent
	Item  wire.TraceItem
}

// Activity gives the trace items of every agent of the view, in the order the hub got them.
func (v *Session) Activity() []Act {
	var out []Act
	for _, a := range v.Agents {
		for _, it := range a.Trace.Items {
			out = append(out, Act{Agent: a, Item: it})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item.N < out[j].Item.N })
	return out
}

// trace gives the trace of the agent with this presence key, and makes it at the first use.
// Every view of the agent shares it.
func (s *Store) trace(key string) *Trace {
	t := s.traces[key]
	if t == nil {
		t = &Trace{}
		s.traces[key] = t
	}
	return t
}

// applyTrace takes one trace update in. An update of an older run of the hub is dropped. The
// first update of a newer run drops every trace: that hub started with none.
func (s *Store) applyTrace(u *wire.TraceUpdate) {
	k, ok := wire.ParsePresenceKey(u.Key)
	if !ok || u.Boot < s.boot {
		return
	}
	if u.Boot > s.boot {
		s.boot = u.Boot
		for _, t := range s.traces {
			*t = Trace{}
		}
		for _, v := range s.views {
			v.Version++
		}
		s.all.Version++
	}
	s.trace(u.Key).merge(u)
	for _, v := range s.viewsOf(k.SID) {
		v.agent(k.SID, k.Agent.String(), math.MaxInt64)
		v.Version++
	}
}
