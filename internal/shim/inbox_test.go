package shim

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"pgregory.net/rapid"
)

// The ports of packages/mcp/test/inbox.prop.test.ts, and three more properties.

var (
	genPeer   = rapid.SampledFrom([]string{"alice@m1", "bob@m2", "bob@m3", "carol@m1"})
	genFilter = rapid.SampledFrom([]string{"alice@m1", "bob@m2", "bob@m3", "carol@m1", "alice", "bob", "carol", "dave"})
)

func msgItem(i int, from, replyTo string) item {
	return item{msg: &message{
		ID:      fmt.Sprint(i + 1),
		From:    from,
		To:      "all",
		Text:    fmt.Sprintf("t%d", i),
		ReplyTo: replyTo,
		SentAt:  "2026-09-30T00:00:00.000Z",
	}}
}

func leftItem(peer string) item {
	return item{notice: &notice{Kind: noticePeerLeft, Peer: peer, At: "2026-09-30T00:00:00.000Z"}}
}

type delivery struct{ id, via string }

type harness struct {
	inbox     *inbox
	pushed    []item
	delivered []delivery
	// reject makes the next push fail when it is true.
	reject bool
}

func newHarness(push bool, cap int) *harness {
	h := &harness{}
	h.inbox = newInbox(inboxOptions{
		push: push,
		cap:  cap,
		onPush: func(it item) error {
			if h.reject {
				return errors.New("transport closed")
			}
			h.pushed = append(h.pushed, it)
			return nil
		},
		onDelivered: func(id, via string) { h.delivered = append(h.delivered, delivery{id, via}) },
	})
	return h
}

func ids(items []item) []string {
	out := []string{}
	for _, it := range items {
		if it.msg != nil {
			out = append(out, it.msg.ID)
		}
	}
	return out
}

func deliveries(items []item, via string) []delivery {
	out := []delivery{}
	for _, it := range items {
		out = append(out, delivery{it.msg.ID, via})
	}
	return out
}

// ended reports whether a pending wait has a result, without a wait.
func ended[T any](p *pending[T]) (T, bool) {
	select {
	case r := <-p.result():
		return r, true
	default:
		var zero T
		return zero, false
	}
}

func messages(t *rapid.T, minLen int) []item {
	froms := rapid.SliceOfN(genPeer, minLen, 50).Draw(t, "froms")
	items := make([]item, len(froms))
	for i, f := range froms {
		items[i] = msgItem(i, f, "")
	}
	return items
}

func TestPullModeTakeReturnsEveryMessageOnceInOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		h := newHarness(false, 0)
		msgs := messages(t, 0)
		for _, m := range msgs {
			h.inbox.accept(m)
		}
		if h.inbox.unread() != len(msgs) {
			t.Fatalf("unread %d, want %d", h.inbox.unread(), len(msgs))
		}
		got := h.inbox.take()
		if !slices.Equal(ids(got), ids(msgs)) {
			t.Fatalf("take %v, want %v", ids(got), ids(msgs))
		}
		if len(h.inbox.take()) != 0 || len(h.pushed) != 0 {
			t.Fatal("a second take or a push gave items")
		}
		if !slices.Equal(h.delivered, deliveries(msgs, viaPull)) {
			t.Fatalf("delivered %v", h.delivered)
		}
	})
}

func TestPushModePushesEveryMessageOnceInOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		h := newHarness(true, 0)
		msgs := messages(t, 0)
		for _, m := range msgs {
			h.inbox.accept(m)
		}
		if !slices.Equal(ids(h.pushed), ids(msgs)) {
			t.Fatalf("pushed %v, want %v", ids(h.pushed), ids(msgs))
		}
		if h.inbox.unread() != 0 {
			t.Fatalf("unread %d", h.inbox.unread())
		}
		if !slices.Equal(h.delivered, deliveries(msgs, viaPush)) {
			t.Fatalf("delivered %v", h.delivered)
		}
	})
}

func TestReplyGoesToTheAskOnlyWhenItComesFromTheAskedPeer(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		push := rapid.Bool().Draw(t, "push")
		asked := genPeer.Draw(t, "asked")
		sender := genPeer.Draw(t, "sender")
		h := newHarness(push, 0)
		answer := h.inbox.expectReply("7", asked)
		reply := msgItem(98, sender, "7")
		h.inbox.accept(reply)
		r, done := ended(answer)
		if sender == asked {
			if !done || r.answer != reply.msg || r.peerLeft {
				t.Fatalf("the ask got %+v (ended %v)", r, done)
			}
			if len(h.pushed) != 0 || h.inbox.unread() != 0 {
				t.Fatal("the answer also went elsewhere")
			}
			if !slices.Equal(h.delivered, []delivery{{reply.msg.ID, viaAsk}}) {
				t.Fatalf("delivered %v", h.delivered)
			}
			return
		}
		if done {
			t.Fatalf("a reply from %s ended an ask to %s", sender, asked)
		}
		if len(h.pushed)+h.inbox.unread() != 1 {
			t.Fatal("the reply went nowhere, or twice")
		}
		want := []delivery{}
		if push {
			want = append(want, delivery{reply.msg.ID, viaPush})
		}
		if !slices.Equal(h.delivered, want) {
			t.Fatalf("delivered %v, want %v", h.delivered, want)
		}
	})
}

func TestWaitFromTakesTheFirstQueuedMessageFromThatPeer(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		h := newHarness(false, 0)
		msgs := messages(t, 1)
		filter := genFilter.Draw(t, "filter")
		for _, m := range msgs {
			h.inbox.accept(m)
		}
		var first *item
		for i := range msgs {
			if matchesPeer(filter, msgs[i].msg.From) {
				first = &msgs[i]
				break
			}
		}
		got, done := ended(h.inbox.wait(filter))
		if first == nil {
			if done {
				t.Fatalf("wait(%s) got %v", filter, ids(got))
			}
			if h.inbox.unread() != len(msgs) {
				t.Fatal("the queue changed")
			}
			return
		}
		if !done || len(got) != 1 || got[0].msg != first.msg {
			t.Fatalf("wait(%s) got %v, want %s", filter, ids(got), first.msg.ID)
		}
		if h.inbox.unread() != len(msgs)-1 {
			t.Fatalf("unread %d, want %d", h.inbox.unread(), len(msgs)-1)
		}
	})
}

// An open wait gets the first item that matches its filter; the other items go on as usual.
func TestOpenWaitGetsTheFirstMatchingMessage(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		push := rapid.Bool().Draw(t, "push")
		filter := genFilter.Draw(t, "filter")
		h := newHarness(push, 0)
		w := h.inbox.wait(filter)
		msgs := messages(t, 1)
		for _, m := range msgs {
			h.inbox.accept(m)
		}
		got, done := ended(w)
		i := slices.IndexFunc(msgs, func(it item) bool { return matchesPeer(filter, it.msg.From) })
		if i < 0 {
			if done {
				t.Fatalf("wait(%s) got %v", filter, ids(got))
			}
			return
		}
		if !done || len(got) != 1 || got[0].msg != msgs[i].msg {
			t.Fatalf("wait(%s) got %v, want %s", filter, ids(got), msgs[i].msg.ID)
		}
		if !slices.Contains(h.delivered, delivery{msgs[i].msg.ID, viaPull}) || len(h.delivered) != 1+len(h.pushed) {
			t.Fatalf("delivered %v", h.delivered)
		}
		if len(h.pushed)+h.inbox.unread() != len(msgs)-1 {
			t.Fatal("an item went nowhere, or twice")
		}
	})
}

func TestNoticeEndsAFilteredWait(t *testing.T) {
	h := newHarness(true, 0)
	w := h.inbox.wait("bob")
	closed := item{notice: &notice{Kind: noticeClosed, At: "2026-09-30T00:00:00.000Z"}}
	h.inbox.accept(closed)
	got, done := ended(w)
	if !done || len(got) != 1 || got[0].notice != closed.notice {
		t.Fatalf("got %+v (ended %v)", got, done)
	}
	if len(h.pushed) != 0 || len(h.delivered) != 0 {
		t.Fatal("a notice was pushed or reported")
	}
}

func TestStopGivesAResultThatCameFirst(t *testing.T) {
	h := newHarness(false, 0)
	w := h.inbox.wait("")
	m := msgItem(0, "alice@m1", "")
	h.inbox.accept(m)
	got, done := w.stop()
	if !done || len(got) != 1 || got[0].msg != m.msg {
		t.Fatalf("stop gave %v (ended %v)", ids(got), done)
	}
	a := h.inbox.expectReply("5", "bob@m2")
	if _, done := a.stop(); done {
		t.Fatal("stop of an open ask says it ended")
	}
	h.inbox.accept(msgItem(1, "bob@m2", "5"))
	if h.inbox.unread() != 1 {
		t.Fatal("a reply to a stopped ask must go to the queue")
	}
}

// Every item whose push fails stays in the queue, in order; take then delivers it as a pull.
func TestRejectedPushNeverLosesAnItem(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		h := newHarness(true, 0)
		msgs := messages(t, 1)
		var queued, pushed []item
		for i, m := range msgs {
			h.reject = rapid.Bool().Draw(t, fmt.Sprintf("reject%d", i))
			h.inbox.accept(m)
			if h.reject {
				queued = append(queued, m)
			} else {
				pushed = append(pushed, m)
			}
		}
		if !slices.Equal(ids(h.pushed), ids(pushed)) {
			t.Fatalf("pushed %v, want %v", ids(h.pushed), ids(pushed))
		}
		if !slices.Equal(h.delivered, deliveries(pushed, viaPush)) {
			t.Fatalf("delivered %v", h.delivered)
		}
		if h.inbox.unread() != len(queued) {
			t.Fatalf("unread %d, want %d", h.inbox.unread(), len(queued))
		}
		h.delivered = nil
		if got := h.inbox.take(); !slices.Equal(ids(got), ids(queued)) {
			t.Fatalf("take %v, want %v", ids(got), ids(queued))
		}
		if !slices.Equal(h.delivered, deliveries(queued, viaPull)) {
			t.Fatalf("delivered %v", h.delivered)
		}
	})
}

// peer_left ends every ask and every filtered wait on that peer, and nothing else. It is never
// pushed, queued or reported.
func TestPeerLeftEndsOnlyAsksAndWaitsOnThatPeer(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		push := rapid.Bool().Draw(t, "push")
		h := newHarness(push, 0)
		askedPeers := rapid.SliceOfN(genPeer, 0, 5).Draw(t, "asks")
		waitFilters := rapid.SliceOfN(genFilter, 0, 5).Draw(t, "waits")
		anyWaits := rapid.IntRange(0, 2).Draw(t, "anyWaits")
		left := genPeer.Draw(t, "left")

		asks := make([]*pending[askResult], len(askedPeers))
		for i, p := range askedPeers {
			asks[i] = h.inbox.expectReply(fmt.Sprint(i+1), p)
		}
		waits := make([]*pending[[]item], len(waitFilters))
		for i, f := range waitFilters {
			waits[i] = h.inbox.wait(f)
		}
		anys := make([]*pending[[]item], anyWaits)
		for i := range anys {
			anys[i] = h.inbox.wait("")
		}
		notice := leftItem(left)
		h.inbox.accept(notice)

		for i, p := range askedPeers {
			r, done := ended(asks[i])
			if matchesPeer(p, left) != done {
				t.Fatalf("ask to %s, %s left: ended %v", p, left, done)
			}
			if done && (!r.peerLeft || r.answer != nil) {
				t.Fatalf("ask to %s ended with %+v", p, r)
			}
		}
		for i, f := range waitFilters {
			got, done := ended(waits[i])
			if matchesPeer(f, left) != done {
				t.Fatalf("wait(%s), %s left: ended %v", f, left, done)
			}
			if done && (len(got) != 1 || got[0].notice != notice.notice) {
				t.Fatalf("wait(%s) ended with %+v", f, got)
			}
		}
		for _, w := range anys {
			if _, done := ended(w); done {
				t.Fatal("a wait for any message ended on peer_left")
			}
		}
		if len(h.pushed) != 0 || h.inbox.unread() != 0 || len(h.delivered) != 0 {
			t.Fatalf("peer_left went elsewhere: pushed %d, unread %d, delivered %v", len(h.pushed), h.inbox.unread(), h.delivered)
		}
	})
}

// The queue keeps the newest cap items, in order, and counts the items it drops.
func TestQueueNeverExceedsItsCapAndCountsDrops(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(1, 10).Draw(t, "cap")
		// A push that always fails queues like pull mode does.
		push := rapid.Bool().Draw(t, "push")
		h := newHarness(push, size)
		h.reject = true
		msgs := messages(t, 0)
		for i, m := range msgs {
			h.inbox.accept(m)
			if h.inbox.unread() > size {
				t.Fatalf("unread %d > cap %d", h.inbox.unread(), size)
			}
			if want := max(0, i+1-size); h.inbox.dropped() != want {
				t.Fatalf("dropped %d, want %d", h.inbox.dropped(), want)
			}
		}
		want := msgs[max(0, len(msgs)-size):]
		if got := h.inbox.take(); !slices.Equal(ids(got), ids(want)) {
			t.Fatalf("take %v, want %v", ids(got), ids(want))
		}
	})
}

func TestBareNameMatchesEveryMachineFullNameOnlyItself(t *testing.T) {
	name := rapid.StringMatching(`^[a-z]{1,5}$`)
	rapid.Check(t, func(t *rapid.T) {
		a, m1, m2 := name.Draw(t, "a"), name.Draw(t, "m1"), name.Draw(t, "m2")
		if !matchesPeer(a, a+"@"+m1) {
			t.Fatal("a bare name must match every machine")
		}
		if matchesPeer(a+"@"+m1, a+"@"+m2) != (m1 == m2) {
			t.Fatal("a full name must match only itself")
		}
		if matchesPeer(a, "") {
			t.Fatal("an item without a sender must not match")
		}
	})
}
