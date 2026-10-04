package hub

import (
	"sort"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// The trace: what the agents do at their terminals. The hooks of Claude Code report it. The
// hub keeps the newest part of it in memory, per agent, and gives it only to the operator's
// feeds. It is not in the store: a hub that starts again has no trace.

const (
	traceRing   = 300 // items that the hub keeps for one agent
	traceFiles  = 200 // changed files that the hub keeps for one agent
	traceAgents = 100 // agents that the hub keeps a trace for
	traceSaid   = 64  // ids of words that the hub keeps for one agent, to know a repeat
)

// TraceRequest is one report of a hook.
type TraceRequest struct {
	Agent  string
	Branch string
	Items  []wire.TraceItem
}

// traceLog is the trace of one agent.
type traceLog struct {
	items  []wire.TraceItem // oldest first, at most traceRing
	files  map[string]wire.TraceFile
	branch string
	// said holds the ids of the newest say items, oldest first. A hook reports the newest
	// words again at each tool call, because Claude Code writes them to its transcript late.
	// The ids outlive the items: words that left the log must not come back as new.
	said []string
}

// has reports whether the log got a say item with this id lately.
func (l *traceLog) has(id string) bool {
	for _, x := range l.said {
		if x == id {
			return true
		}
	}
	return false
}

// add puts one item at the end and drops the oldest item when the log is full.
func (l *traceLog) add(it wire.TraceItem) {
	if len(l.items) >= traceRing {
		l.items = append(l.items[:0], l.items[len(l.items)-traceRing+1:]...)
	}
	l.items = append(l.items, it)
	if it.Kind == wire.TraceSay && it.ID != "" {
		if len(l.said) >= traceSaid {
			l.said = append(l.said[:0], l.said[len(l.said)-traceSaid+1:]...)
		}
		l.said = append(l.said, it.ID)
	}
}

// touch records one more change of a file and gives the record. When the log is full of
// files, the one that was changed the longest time ago goes.
func (l *traceLog) touch(path, at string) wire.TraceFile {
	if l.files == nil {
		l.files = map[string]wire.TraceFile{}
	}
	f, known := l.files[path]
	if !known && len(l.files) >= traceFiles {
		oldest := ""
		for p, x := range l.files {
			if oldest == "" || x.At < l.files[oldest].At || x.At == l.files[oldest].At && p < oldest {
				oldest = p
			}
		}
		delete(l.files, oldest)
	}
	f = wire.TraceFile{Path: path, Count: f.Count + 1, At: at}
	l.files[path] = f
	return f
}

// last is the number of the newest item, or 0.
func (l *traceLog) last() int64 {
	if len(l.items) == 0 {
		return 0
	}
	return l.items[len(l.items)-1].N
}

// update is the whole log as one feed event.
func (l *traceLog) update(boot int64, key string) wire.TraceUpdate {
	u := wire.TraceUpdate{Boot: boot, Key: key, Branch: l.branch, Items: l.items}
	for _, f := range l.files {
		u.Files = append(u.Files, f)
	}
	sort.Slice(u.Files, func(i, j int) bool { return u.Files[i].Path < u.Files[j].Path })
	return u
}

type traceOut struct {
	Kind string `json:"kind"`
	wire.TraceUpdate
}

// traceLogLocked gives the log of an agent, and makes it at the first report. When the hub
// keeps too many logs, the one with the oldest newest item, of an agent that is not in a
// session, goes.
func (h *Hub) traceLogLocked(key string) *traceLog {
	if l := h.traces[key]; l != nil {
		return l
	}
	if len(h.traces) >= traceAgents {
		oldest := ""
		for k, l := range h.traces {
			if _, live := h.conns[k]; live {
				continue
			}
			if oldest == "" || l.last() < h.traces[oldest].last() {
				oldest = k
			}
		}
		delete(h.traces, oldest)
	}
	l := &traceLog{}
	h.traces[key] = l
	return l
}

// Trace takes a report of what an agent did at its terminal. The agent must be in the session.
// The operator's feeds get the new items; no agent gets them.
func (h *Hub) Trace(machine, sid string, req TraceRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.traceLim.take(machine) {
		return errf("rate_limited", "too many updates")
	}
	c, err := h.requireConnLocked(machine, sid, req.Agent)
	if err != nil {
		return err
	}
	l := h.traceLogLocked(c.key)
	at := h.now()
	out := wire.TraceUpdate{Boot: h.boot, Key: c.key}
	for _, it := range req.Items {
		if it.Kind == wire.TraceSay && it.ID != "" && l.has(it.ID) {
			// Two tool calls of one turn report the same words.
			continue
		}
		h.traceN++
		it.N, it.At = h.traceN, at
		l.add(it)
		out.Items = append(out.Items, it)
		if it.Kind == wire.TraceToolEnd && !it.Failed && it.File != "" {
			out.Files = append(out.Files, l.touch(it.File, at))
		}
	}
	if len(out.Items) == 0 {
		return nil
	}
	if req.Branch != "" {
		l.branch = req.Branch
	}
	out.Branch = l.branch
	h.feedAll("trace", traceOut{Kind: "trace", TraceUpdate: out}, "")
	return nil
}

// dropTraceLocked forgets the trace of one agent.
func (h *Hub) dropTraceLocked(key string) { delete(h.traces, key) }

// dropSessionTraceLocked forgets the trace of each agent of a session.
func (h *Hub) dropSessionTraceLocked(sid string) {
	for key := range h.traces {
		if k, ok := wire.ParsePresenceKey(key); ok && k.SID == sid {
			delete(h.traces, key)
		}
	}
}

// traceDumpLocked gives every log as feed events, in key order.
func (h *Hub) traceDumpLocked() []feedItem {
	keys := make([]string, 0, len(h.traces))
	for k := range h.traces {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]feedItem, 0, len(keys))
	for _, k := range keys {
		u := h.traces[k].update(h.boot, k)
		// The feed writes the event after the mutex is free: it needs its own copy.
		u.Items = append([]wire.TraceItem(nil), u.Items...)
		out = append(out, feedItem{event: "trace", data: traceOut{Kind: "trace", TraceUpdate: u}})
	}
	return out
}
