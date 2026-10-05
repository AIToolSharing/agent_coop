package hub

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

// pickHub is a hub with live connections and no store: pickLocked reads only those.
func pickHub(clock *time.Time, conns ...*Conn) *Hub {
	h := &Hub{conns: map[string]*Conn{}, opt: Options{Now: func() time.Time { return *clock }}}
	for _, c := range conns {
		c.key = wire.BuildPresenceKey(wire.PresenceKey{SID: c.sid, Agent: c.me})
		h.conns[c.key] = c
	}
	return h
}

func conn(sid, agent, machine, state, gate string) *Conn {
	return &Conn{sid: sid, me: wire.Address{Agent: agent, Machine: machine}, state: state, gate: gate}
}

// The pick is the reference rule: of the live peers of the session other than me that are not
// busy, the one on the machine with the fewest working agents, then the one picked longest ago,
// then the first by key.
func TestPickLockedIsTheLeastLoadedFreePeer(t *testing.T) {
	states := []string{"working", "blocked", "done", "idle"}
	gates := []string{wire.GateRun, wire.GateHeld, wire.GatePaused}
	rapid.Check(t, func(rt *rapid.T) {
		var clock time.Time
		n := rapid.IntRange(0, 8).Draw(rt, "n")
		var conns []*Conn
		for i := range n {
			c := conn(
				rapid.SampledFrom([]string{"s1", "s2"}).Draw(rt, fmt.Sprint("sid", i)),
				fmt.Sprint("a", i),
				rapid.SampledFrom([]string{"m1", "m2", "m3"}).Draw(rt, fmt.Sprint("machine", i)),
				rapid.SampledFrom(states).Draw(rt, fmt.Sprint("state", i)),
				rapid.SampledFrom(gates).Draw(rt, fmt.Sprint("gate", i)),
			)
			c.picked = time.Time{}.Add(time.Duration(rapid.IntRange(0, 3).Draw(rt, fmt.Sprint("picked", i))) * time.Second)
			conns = append(conns, c)
		}
		h := pickHub(&clock, conns...)
		var me *wire.Address
		if n > 0 && rapid.Bool().Draw(rt, "agent sends") {
			me = &conns[rapid.IntRange(0, n-1).Draw(rt, "me")].me
		}
		// The reference.
		load := map[string]int{}
		for _, c := range conns {
			if c.state == "working" {
				load[c.me.Machine]++
			}
		}
		var want []*Conn
		for _, c := range conns {
			if c.sid == "s1" && (me == nil || c.me != *me) && c.gate == wire.GateRun && c.state != "working" && c.state != "blocked" {
				want = append(want, c)
			}
		}
		slices.SortFunc(want, func(a, b *Conn) int {
			if d := load[a.me.Machine] - load[b.me.Machine]; d != 0 {
				return d
			}
			if d := a.picked.Compare(b.picked); d != 0 {
				return d
			}
			return slices.Compare([]string{a.key}, []string{b.key})
		})
		clock = time.Time{}.Add(time.Hour)
		got, err := h.pickLocked("s1", me)
		if len(want) == 0 {
			if err == nil {
				rt.Fatalf("picked %s, want an error", got.me)
			}
			return
		}
		if err != nil || got != want[0] {
			rt.Fatalf("picked %v (%v), want %s", got, err, want[0].me)
		}
		if !got.picked.Equal(clock) {
			rt.Fatalf("the pick did not record its time")
		}
	})
}

func TestPickLockedTakesTurnsOnEqualLoad(t *testing.T) {
	var clock time.Time
	h := pickHub(&clock, conn("s", "a", "m1", "idle", wire.GateRun), conn("s", "b", "m2", "idle", wire.GateRun), conn("s", "c", "m3", "done", wire.GateRun))
	var got []string
	for range 6 {
		clock = clock.Add(time.Second)
		c, err := h.pickLocked("s", nil)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c.me.Agent)
	}
	if want := []string{"a", "b", "c", "a", "b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("picks %v, want %v", got, want)
	}
}

func TestPickLockedPrefersTheMachineWithFewerWorkingAgents(t *testing.T) {
	var clock time.Time
	// m1 runs a working agent in another session; its idle agent loses to the idle agent of m2.
	h := pickHub(&clock,
		conn("s", "a", "m1", "idle", wire.GateRun),
		conn("other", "x", "m1", "working", wire.GateRun),
		conn("s", "b", "m2", "idle", wire.GateRun),
	)
	c, err := h.pickLocked("s", nil)
	if err != nil || c.me.Agent != "b" {
		t.Fatalf("picked %v (%v), want b", c, err)
	}
}

func TestPickLockedSkipsBusyHeldPausedOfflineAndMe(t *testing.T) {
	var clock time.Time
	me := wire.Address{Agent: "me", Machine: "m1"}
	h := pickHub(&clock,
		conn("s", "me", "m1", "idle", wire.GateRun),
		conn("s", "w", "m2", "working", wire.GateRun),
		conn("s", "k", "m2", "blocked", wire.GateRun),
		conn("s", "h", "m3", "idle", wire.GateHeld),
		conn("s", "p", "m3", "idle", wire.GatePaused),
		conn("elsewhere", "e", "m3", "idle", wire.GateRun),
	)
	_, err := h.pickLocked("s", &me)
	if err == nil {
		t.Fatal("picked a peer, want none")
	}
	// The peers are in the order of their keys: session, machine, agent.
	want := "no peer in this session can take work now; peers: k@m2 (blocked), w@m2 (working), h@m3 (held), p@m3 (paused)"
	if err.Error() != want {
		t.Fatalf("error %q\nwant  %q", err, want)
	}
	if _, err := h.pickLocked("empty", nil); err == nil || err.Error() != "no peer in this session can take work now; peers: none" {
		t.Fatalf("empty session: %v", err)
	}
	// An agent that left is not live: only the one in the session is picked.
	free := conn("s", "f", "m3", "done", wire.GateRun)
	h.conns[wire.BuildPresenceKey(wire.PresenceKey{SID: "s", Agent: free.me})] = free
	if c, err := h.pickLocked("s", &me); err != nil || c != free {
		t.Fatalf("picked %v (%v), want f@m3", c, err)
	}
}
