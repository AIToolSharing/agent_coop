package shim

// The inbox routes each incoming item to exactly one place, in this order:
//  1. an open ask that waits for this reply,
//  2. an open wait whose filter matches,
//  3. a push into the session (channel), when push is on,
//  4. the queue, for inbox and later wait calls.
// Notices (removed, closed, reopened, withdrawn) follow the same order, without step 1.
// A peer_left notice only ends the asks and the filtered waits on that peer. It goes nowhere
// else, so that presence changes never wake an agent that did not wait for that peer.
//
// The inbox does no I/O and starts no timer. The caller gives it the push and the delivery
// report as functions, and limits each wait with its own timer (see pending.stop).

import (
	"slices"
	"strings"
	"sync"
)

// How a message reached the agent, for the delivery report.
const (
	viaPush = "push"
	viaPull = "pull"
	viaAsk  = "ask"
)

// defaultCap is the queue size when inboxOptions.cap is zero.
const defaultCap = 1000

// item is a message or a notice. Exactly one field is set.
type item struct {
	msg    *message
	notice *notice
}

// from gives the sender of a message, and "" for a notice.
func (it item) from() string {
	if it.msg != nil {
		return it.msg.From
	}
	return ""
}

type inboxOptions struct {
	push bool
	// onPush pushes one item into the session. An error keeps the item in the queue.
	onPush func(item) error
	// onDelivered, when set, is called once per message when it reaches the agent, with the
	// route it took (push, pull or ask). The tests observe the routing with it.
	onDelivered func(id, via string)
	// cap is the queue size; the oldest items go first when it is full. Zero means 1000.
	cap int
}

// askResult ends an ask: the answer, or the asked peer left. A timeout is the zero value.
type askResult struct {
	answer   *message
	peerLeft bool
}

// pending is an open wait or ask. Its result arrives once on result().
type pending[T any] struct {
	c chan T
	// unregister removes the wait from the inbox. It gives false when the wait has already
	// ended, so that its result is in c.
	unregister func() bool
}

func (p *pending[T]) result() <-chan T { return p.c }

// stop ends the wait. It gives the result and true when the wait ended before stop.
func (p *pending[T]) stop() (T, bool) {
	if p.unregister() {
		var zero T
		return zero, false
	}
	return <-p.c, true
}

// resolved makes a pending that already has its result.
func resolved[T any](r T) *pending[T] {
	c := make(chan T, 1)
	c <- r
	return &pending[T]{c: c, unregister: func() bool { return false }}
}

type waiter struct {
	from string // "" waits for any item
	c    chan []item
}

type asker struct {
	from string
	c    chan askResult
}

type inbox struct {
	o inboxOptions

	mu      sync.Mutex
	queue   []item
	waiters []*waiter
	asks    map[string]*asker
	// drops counts the items that the full queue dropped.
	drops int
}

func newInbox(o inboxOptions) *inbox {
	if o.cap <= 0 {
		o.cap = defaultCap
	}
	return &inbox{o: o, asks: map[string]*asker{}}
}

// unread gives the number of queued items.
func (b *inbox) unread() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue)
}

// dropped gives the number of items that the full queue dropped.
func (b *inbox) dropped() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.drops
}

// accept routes one item. The caller gives items one at a time, in the order they arrive.
func (b *inbox) accept(it item) {
	b.mu.Lock()
	if it.notice != nil && it.notice.Kind == noticePeerLeft {
		b.peerLeft(it)
		b.mu.Unlock()
		return
	}
	if m := it.msg; m != nil && m.ReplyTo != "" {
		if a, ok := b.asks[m.ReplyTo]; ok && a.from == m.From {
			delete(b.asks, m.ReplyTo)
			a.c <- askResult{answer: m}
			b.mu.Unlock()
			b.delivered(viaAsk, it)
			return
		}
	}
	for i, w := range b.waiters {
		if w.from == "" || it.notice != nil || matchesPeer(w.from, it.from()) {
			b.waiters = slices.Delete(b.waiters, i, i+1)
			w.c <- []item{it}
			b.mu.Unlock()
			b.delivered(viaPull, it)
			return
		}
	}
	b.mu.Unlock()
	if b.o.push {
		if err := b.o.onPush(it); err == nil {
			b.delivered(viaPush, it)
			return
		}
		// The push failed (the session closes, or it is busy). The queue keeps the item.
	}
	b.mu.Lock()
	b.queue = append(b.queue, it)
	if n := len(b.queue); n > b.o.cap {
		b.drops += n - b.o.cap
		b.queue = slices.Clone(b.queue[n-b.o.cap:])
	}
	b.mu.Unlock()
}

// take gives everything queued, oldest first. The queue is then empty.
func (b *inbox) take() []item {
	b.mu.Lock()
	items := b.queue
	b.queue = nil
	b.mu.Unlock()
	b.delivered(viaPull, items...)
	return items
}

// wait starts a wait for the next item from peer from ("" for any peer). A notice always
// matches. A queued item that matches ends the wait at once.
func (b *inbox) wait(from string) *pending[[]item] {
	b.mu.Lock()
	i := slices.IndexFunc(b.queue, func(it item) bool {
		return from == "" || it.notice != nil || matchesPeer(from, it.from())
	})
	if i >= 0 {
		it := b.queue[i]
		b.queue = slices.Delete(b.queue, i, i+1)
		b.mu.Unlock()
		b.delivered(viaPull, it)
		return resolved([]item{it})
	}
	w := &waiter{from: from, c: make(chan []item, 1)}
	b.waiters = append(b.waiters, w)
	b.mu.Unlock()
	return &pending[[]item]{c: w.c, unregister: func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		i := slices.Index(b.waiters, w)
		if i < 0 {
			return false
		}
		b.waiters = slices.Delete(b.waiters, i, i+1)
		return true
	}}
}

// expectReply starts a wait for the reply to message id from peer from (an address, or
// operator). A queued reply ends the wait at once.
func (b *inbox) expectReply(id, from string) *pending[askResult] {
	b.mu.Lock()
	i := slices.IndexFunc(b.queue, func(it item) bool {
		return it.msg != nil && it.msg.ReplyTo == id && it.msg.From == from
	})
	if i >= 0 {
		m := b.queue[i].msg
		b.queue = slices.Delete(b.queue, i, i+1)
		b.mu.Unlock()
		b.o.onDelivered(m.ID, viaAsk)
		return resolved(askResult{answer: m})
	}
	a := &asker{from: from, c: make(chan askResult, 1)}
	b.asks[id] = a
	b.mu.Unlock()
	return &pending[askResult]{c: a.c, unregister: func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.asks[id] != a {
			return false
		}
		delete(b.asks, id)
		return true
	}}
}

// peerLeft ends every ask and every filtered wait on the peer of notice it. The caller holds
// the lock.
func (b *inbox) peerLeft(it item) {
	peer := it.notice.Peer
	if peer == "" {
		return
	}
	for id, a := range b.asks {
		if matchesPeer(a.from, peer) {
			delete(b.asks, id)
			a.c <- askResult{peerLeft: true}
		}
	}
	b.waiters = slices.DeleteFunc(b.waiters, func(w *waiter) bool {
		if w.from == "" || !matchesPeer(w.from, peer) {
			return false
		}
		w.c <- []item{it}
		return true
	})
}

func (b *inbox) delivered(via string, items ...item) {
	if b.o.onDelivered == nil {
		return
	}
	for _, it := range items {
		if it.msg != nil {
			b.o.onDelivered(it.msg.ID, via)
		}
	}
}

// matchesPeer reports whether a message from from passes the filter. A filter `name` matches
// `name@` any machine; a filter `name@machine` matches only itself. An empty from (a notice)
// never matches.
func matchesPeer(filter, from string) bool {
	if from == "" {
		return false
	}
	if strings.Contains(filter, "@") {
		return from == filter
	}
	name, _, _ := strings.Cut(from, "@")
	return name == filter
}
