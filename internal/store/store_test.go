package store_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/store"
	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

const at = "2026-10-02T10:00:00.000Z"

func open(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coop.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// must fails the test through a panic, which rapid and testing both report.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func msg(sid, from, to, text string) wire.Event {
	return wire.Event{Kind: wire.EventMsg, SID: sid, From: from, To: to, Text: text, SentAt: at}
}

func evt(sid, from, kind string) wire.Event {
	a := &wire.Activity{Kind: kind, At: at}
	switch kind {
	case "joined":
		a.Host, a.Cwd, a.Client = "h", "/w", wire.Client{Name: "t", Version: "0"}
	case "left":
		a.Reason = "disconnected"
	case "state":
		a.State = "working"
	case "delivered":
		a.ID, a.Via = "1", "push"
	case "wait_start":
		a.TimeoutS = 5
	case "wait_end":
		a.Result = "message"
	}
	return wire.Event{Kind: wire.EventActivity, SID: sid, From: from, Activity: a}
}

// genEvent draws one event that the wire encoding accepts.
func genEvent(t *rapid.T) wire.Event {
	sid := rapid.SampledFrom([]string{"s1", "s2"}).Draw(t, "sid")
	addr := func(label string) string {
		return rapid.SampledFrom([]string{"alice@mac-1", "bob@vps-2", "carol@mac-3"}).Draw(t, label)
	}
	switch rapid.IntRange(0, 3).Draw(t, "kind") {
	case 0:
		from := rapid.SampledFrom([]string{wire.Operator, addr("from")}).Draw(t, "sender")
		to := rapid.SampledFrom([]string{wire.Broadcast, wire.Operator, addr("to")}).Draw(t, "recipient")
		e := msg(sid, from, to, rapid.StringMatching(`[a-z ]{1,20}`).Draw(t, "text"))
		if rapid.Bool().Draw(t, "reply") {
			e.ReplyTo = "7"
		}
		return e
	case 1:
		return wire.Event{Kind: wire.EventKick, SID: sid, Target: addr("target"), At: at}
	case 2:
		return wire.Event{Kind: wire.EventRedact, SID: sid, ID: "3", At: at}
	}
	kind := rapid.SampledFrom([]string{"joined", "left", "state", "delivered", "wait_start", "wait_end"}).Draw(t, "activity")
	return evt(sid, addr("actor"), kind)
}

// Every appended event gets the next sequence, and a read gives exactly the events of that
// session, range and kinds, in order.
func TestAppendThenReadIsAFilter(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s, _ := open(t)
		var all []wire.Event
		n := rapid.IntRange(0, 40).Draw(rt, "n")
		for i := range n {
			e := genEvent(rt)
			seq := must(s.Append(e, false, nil))
			if seq != int64(i+1) {
				rt.Fatalf("seq %d, want %d", seq, i+1)
			}
			e.Seq = seq
			all = append(all, e)
		}
		if last := must(s.LastSeq()); last != int64(n) {
			rt.Fatalf("LastSeq %d, want %d", last, n)
		}
		sid := rapid.SampledFrom([]string{"s1", "s2"}).Draw(rt, "qsid")
		from := int64(rapid.IntRange(0, n+1).Draw(rt, "from"))
		to := int64(rapid.IntRange(0, n+1).Draw(rt, "to"))
		kinds := rapid.SliceOfDistinct(rapid.SampledFrom([]string{"msg", "kick", "redact", "evt"}), rapid.ID[string]).Draw(rt, "kinds")
		got := must(s.Events(sid, from, to, kinds...))
		var want []wire.Event
		for _, e := range all {
			if e.SID == sid && e.Seq >= from && e.Seq <= to && (len(kinds) == 0 || slices.Contains(kinds, e.Kind)) {
				want = append(want, e)
			}
		}
		if len(got) == 0 && len(want) == 0 {
			return
		}
		if !reflect.DeepEqual(got, want) {
			rt.Fatalf("Events(%s, %d, %d, %v)\n got %+v\nwant %+v", sid, from, to, kinds, got, want)
		}
	})
}

// A redact deletes the newest row. The next event must not get the same sequence: a message
// id is unique for the life of the store, and a resume by id depends on it.
func TestDeleteDoesNotReuseASequence(t *testing.T) {
	s, _ := open(t)
	must(s.Append(msg("s1", "alice@mac-1", wire.Broadcast, "oops"), false, nil))
	if !must(s.Delete("s1", 1)) {
		t.Fatal("delete refused")
	}
	if seq := must(s.Append(msg("s1", "alice@mac-1", wire.Broadcast, "next"), false, nil)); seq != 2 {
		t.Fatalf("seq %d after a delete, want 2", seq)
	}
	if last := must(s.LastSeq()); last != 2 {
		t.Fatalf("LastSeq %d, want 2", last)
	}
	got := must(s.Events("s1", 1, 10))
	if len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("events after delete: %+v", got)
	}
}

func TestDeleteTakesOnlyAMessageOfThatSession(t *testing.T) {
	s, _ := open(t)
	must(s.Append(evt("s1", "alice@mac-1", "joined"), true, nil))             // 1
	must(s.Append(msg("s2", "alice@mac-1", wire.Broadcast, "x"), false, nil)) // 2
	must(s.Append(msg("s1", wire.Operator, wire.Broadcast, "y"), false, nil)) // 3
	for _, c := range []struct {
		sid  string
		seq  int64
		want bool
	}{{"s1", 1, false}, {"s1", 2, false}, {"s1", 99, false}, {"s1", 3, true}, {"s1", 3, false}} {
		if got := must(s.Delete(c.sid, c.seq)); got != c.want {
			t.Errorf("Delete(%s, %d) = %v, want %v", c.sid, c.seq, got, c.want)
		}
	}
}

func TestKnownFollowsTheAppends(t *testing.T) {
	s, _ := open(t)
	me := "alice@mac-1"
	check := func(state, note string, seen int64) {
		t.Helper()
		k, ok := must2(s.KnownAgent("s1", me))
		if !ok || k.State != state || k.Note != note || k.SeenSeq != seen {
			t.Fatalf("known %+v %v, want %s %q %d", k, ok, state, note, seen)
		}
	}
	if _, ok := must2(s.KnownAgent("s1", me)); ok {
		t.Fatal("known before any event")
	}
	must(s.Append(evt("s1", me, "joined"), true, nil))
	check("idle", "", 1)
	must(s.Append(evt("s1", me, "state"), false, &store.Known{State: "working", Note: "x"}))
	check("working", "x", 1)
	must(s.Append(evt("s1", me, "delivered"), true, nil))
	check("working", "x", 3)
	must(s.Append(evt("s1", me, "left"), true, &store.Known{State: "done", Note: ""}))
	check("done", "", 4)
	// A seen sequence never goes backwards.
	must(s.Append(msg("s1", me, wire.Broadcast, "m"), false, nil))
	all := must(s.Known("s1"))
	if len(all) != 1 || all[0].Agent != me || all[0].SeenSeq != 4 {
		t.Fatalf("Known: %+v", all)
	}
}

func must2[T any](v T, ok bool, err error) (T, bool) {
	if err != nil {
		panic(err)
	}
	return v, ok
}

// Every record write gets a revision greater than every revision before it, also across a
// close and open of the store. The operator's feed depends on it to keep the newest value.
func TestRevisionsIncrease(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s, path := open(t)
		var last int64
		seen := func(rev int64, what string) {
			if rev <= last {
				rt.Fatalf("%s: revision %d after %d", what, rev, last)
			}
			last = rev
		}
		sids := []string{"a", "b", "c"}
		ops := rapid.SliceOfN(rapid.IntRange(0, 6), 1, 60).Draw(rt, "ops")
		for i, op := range ops {
			sid := sids[i%len(sids)]
			switch op {
			case 0:
				row, err := s.CreateSession(sid, "", at)
				if errors.Is(err, store.ErrExists) {
					continue
				}
				seen(must(row, err).Revision, "create")
			case 1:
				row, _, err := s.SetStatus(sid, "closed", at)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				seen(must(row, err).Revision, "close")
			case 2:
				row, _, err := s.SetStatus(sid, "open", at)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				seen(must(row, err).Revision, "reopen")
			case 3:
				seen(must(s.Kick(sid, "bob@vps-2", at)), "kick")
			case 4:
				seen(must(s.Unkick(sid, "bob@vps-2")), "unkick")
			case 5:
				seen(must(s.NextRevision()), "presence")
			case 6:
				// A reopen of the store must continue the count, not restart it.
				if err := s.Close(); err != nil {
					rt.Fatal(err)
				}
				s = must(store.Open(path))
				t.Cleanup(func() { _ = s.Close() })
			}
		}
	})
}

func TestSessions(t *testing.T) {
	s, _ := open(t)
	if _, ok, _ := s.Session("x"); ok {
		t.Fatal("session before create")
	}
	row := must(s.CreateSession("x", "Title", at))
	if row.SID != "x" || row.Record.Status != "open" || row.Record.Title != "Title" || row.Record.CreatedAt != at || row.Record.ClosedAt != "" {
		t.Fatalf("created: %+v", row)
	}
	if _, err := s.CreateSession("x", "", at); !errors.Is(err, store.ErrExists) {
		t.Fatalf("second create: %v", err)
	}
	must(s.CreateSession("a", "", at))
	list := must(s.Sessions())
	if len(list) != 2 || list[0].SID != "a" || list[1].SID != "x" {
		t.Fatalf("Sessions: %+v", list)
	}
	if _, _, err := s.SetStatus("nosuch", "closed", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetStatus unknown: %v", err)
	}
	if _, _, err := s.DeleteSession("x"); !errors.Is(err, store.ErrOpen) {
		t.Fatalf("delete open: %v", err)
	}
	closed, changed, err := s.SetStatus("x", "closed", "2026-10-02T11:00:00.000Z")
	if err != nil || !changed || closed.Record.Status != "closed" || closed.Record.ClosedAt != "2026-10-02T11:00:00.000Z" {
		t.Fatalf("close: %+v %v %v", closed, changed, err)
	}
	if _, changed, _ := s.SetStatus("x", "closed", at); changed {
		t.Fatal("a second close counts as a change")
	}
	reopened, changed, _ := s.SetStatus("x", "open", at)
	if !changed || reopened.Record.ClosedAt != "" || reopened.Revision <= closed.Revision {
		t.Fatalf("reopen: %+v %v", reopened, changed)
	}
	// Delete takes the events, the kicks and the known agents of the session with it.
	must(s.Append(msg("x", "alice@mac-1", wire.Broadcast, "m"), false, nil))
	must(s.Append(evt("x", "alice@mac-1", "joined"), true, nil))
	must(s.Kick("x", "bob@vps-2", at))
	must(s.Append(msg("a", "alice@mac-1", wire.Broadcast, "keep"), false, nil))
	must2(s.SetStatus("x", "closed", at))
	rev, kicks, err := s.DeleteSession("x")
	if err != nil || rev <= reopened.Revision || len(kicks) != 1 || kicks[0].Target != "bob@vps-2" || kicks[0].Revision >= rev {
		t.Fatalf("delete: %d %+v %v", rev, kicks, err)
	}
	if _, ok, _ := s.Session("x"); ok {
		t.Fatal("session after delete")
	}
	if got := must(s.Events("x", 1, 100)); len(got) != 0 {
		t.Fatalf("events after delete: %+v", got)
	}
	if got := must(s.Known("x")); len(got) != 0 {
		t.Fatalf("known after delete: %+v", got)
	}
	if got := must(s.Kicks()); len(got) != 0 {
		t.Fatalf("kicks after delete: %+v", got)
	}
	if got := must(s.Events("a", 1, 100)); len(got) != 1 {
		t.Fatalf("other session lost events: %+v", got)
	}
	if _, _, err := s.DeleteSession("x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestKicks(t *testing.T) {
	s, _ := open(t)
	if must(s.Kicked("s", "bob@vps-2")) {
		t.Fatal("kicked before kick")
	}
	r1 := must(s.Kick("s", "bob@vps-2", at))
	if !must(s.Kicked("s", "bob@vps-2")) {
		t.Fatal("not kicked after kick")
	}
	r2 := must(s.Kick("s", "bob@vps-2", "2026-10-02T11:00:00.000Z"))
	kicks := must(s.Kicks())
	if r2 <= r1 || len(kicks) != 1 || kicks[0].At != "2026-10-02T11:00:00.000Z" || kicks[0].Revision != r2 {
		t.Fatalf("kick twice: %+v", kicks)
	}
	r3 := must(s.Unkick("s", "bob@vps-2"))
	if r3 <= r2 || must(s.Kicked("s", "bob@vps-2")) {
		t.Fatal("unkick")
	}
	if r4 := must(s.Unkick("s", "bob@vps-2")); r4 <= r3 {
		t.Fatal("an unkick of an absent kick still gets a revision")
	}
}

func TestTokens(t *testing.T) {
	s, _ := open(t)
	tok := must(s.IssueToken("mac-1", "machine", at))
	name, secret, ok := strings.Cut(tok, ".")
	if !ok || name != "mac-1" || len(secret) < 40 || strings.ContainsAny(secret, "+/=") {
		t.Fatalf("token %q", tok)
	}
	if n, role, ok := must3(s.VerifyToken(tok)); !ok || n != "mac-1" || role != "machine" {
		t.Fatalf("verify: %s %s %v", n, role, ok)
	}
	for _, bad := range []string{"", "mac-1", "mac-1.", ".x", "mac-1.wrong", "Mac-1." + secret, "mac-1." + secret + "x"} {
		if _, _, ok := must3(s.VerifyToken(bad)); ok {
			t.Errorf("verify accepted %q", bad)
		}
	}
	op := must(s.IssueToken("matt", "operator", at))
	if _, role, _ := must3(s.VerifyToken(op)); role != "operator" {
		t.Fatalf("role %s", role)
	}
	// A new token of the same name replaces the old one.
	tok2 := must(s.IssueToken("mac-1", "machine", at))
	if _, _, ok := must3(s.VerifyToken(tok)); ok {
		t.Fatal("old token still valid after replace")
	}
	if _, _, ok := must3(s.VerifyToken(tok2)); !ok {
		t.Fatal("new token invalid")
	}
	if !must(s.TokenValid("mac-1")) {
		t.Fatal("TokenValid")
	}
	if !must(s.RevokeToken("mac-1", "2026-10-02T11:00:00.000Z")) {
		t.Fatal("revoke")
	}
	if must(s.RevokeToken("mac-1", at)) || must(s.RevokeToken("nosuch", at)) {
		t.Fatal("revoke twice or unknown")
	}
	if _, _, ok := must3(s.VerifyToken(tok2)); ok {
		t.Fatal("revoked token valid")
	}
	if must(s.TokenValid("mac-1")) {
		t.Fatal("TokenValid after revoke")
	}
	rows := must(s.Tokens())
	want := []store.TokenRow{
		{Name: "mac-1", Role: "machine", CreatedAt: at, RevokedAt: "2026-10-02T11:00:00.000Z"},
		{Name: "matt", Role: "operator", CreatedAt: at},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("Tokens:\n got %+v\nwant %+v", rows, want)
	}
	if _, err := s.IssueToken("bad name", "machine", at); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := s.IssueToken("x", "root", at); err == nil {
		t.Fatal("bad role accepted")
	}
}

func must3[T, U any](v T, u U, ok bool, err error) (T, U, bool) {
	if err != nil {
		panic(err)
	}
	return v, u, ok
}

// The operator's feed sends the rows as the wire form: subject and payload, byte for byte
// what the encoder gave.
func TestRowsAreTheWireForm(t *testing.T) {
	s, _ := open(t)
	events := []wire.Event{
		msg("s1", "alice@mac-1", "bob@vps-2", "one"),
		evt("s2", "bob@vps-2", "joined"),
		{Kind: wire.EventKick, SID: "s1", Target: "bob@vps-2", At: at},
	}
	for _, e := range events {
		must(s.Append(e, false, nil))
	}
	rows := must(s.Rows(2, 10))
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	for i, r := range rows {
		subject, payload, err := wire.EncodeEvent(events[i+1])
		if err != nil || r.Seq != int64(i+2) || r.Subject != subject || string(r.Payload) != string(payload) {
			t.Fatalf("row %d: %+v, want %s %s", i, r, subject, payload)
		}
	}
	if rows := must(s.Rows(4, 10)); len(rows) != 0 {
		t.Fatalf("rows past the end: %+v", rows)
	}
}

func TestOpenCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "er", "coop.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}
