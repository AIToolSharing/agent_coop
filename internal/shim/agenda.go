package shim

// The orchestrator's scheduler. An orchestrator gets messages from the user and from each
// agent of the session. A push for each one would stop its work again and again. So its
// messages and notices go to the queue, and it gets one short nudge instead: "your agenda has
// new items". It gets no second nudge until it reads the agenda, and never two within
// nudgeGap. The agenda tool gives the queue in the order to handle it.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// defaultNudgeGap is the shortest time between two nudges of the orchestrator.
const defaultNudgeGap = time.Minute

// nudger decides when the orchestrator gets a nudge. It sends at most one until the agenda
// is read, and never two within gap.
type nudger struct {
	gap  time.Duration
	now  func() time.Time
	fire func() // sends the nudge; it reads what is queued at that time

	mu          sync.Mutex
	outstanding bool // a nudge was sent and the agenda was not read since
	last        time.Time
	timer       *time.Timer
}

// queued says that an item went to the queue.
func (n *nudger) queued() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.outstanding || n.timer != nil {
		return
	}
	if wait := n.gap - n.now().Sub(n.last); wait > 0 && !n.last.IsZero() {
		n.timer = time.AfterFunc(wait, func() {
			n.mu.Lock()
			n.timer = nil
			n.mu.Unlock()
			n.queued()
		})
		return
	}
	n.outstanding, n.last = true, n.now()
	go n.fire()
}

// read says that the orchestrator read its agenda: the next item may nudge it again.
func (n *nudger) read() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.outstanding = false
}

// stop ends a nudge that waits for its time.
func (n *nudger) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
}

// nudgeText says what waits on the agenda, in one line.
func nudgeText(items []item) string {
	user, agents, notices := 0, 0, 0
	for _, it := range items {
		switch {
		case it.msg != nil && it.msg.From == wire.Operator:
			user++
		case it.msg != nil:
			agents++
		default:
			notices++
		}
	}
	var parts []string
	if user > 0 {
		parts = append(parts, fmt.Sprintf("%d from the user", user))
	}
	if agents > 0 {
		parts = append(parts, fmt.Sprintf("%d from agents", agents))
	}
	if notices > 0 {
		parts = append(parts, fmt.Sprintf("%d notices", notices))
	}
	return fmt.Sprintf("Your agenda has new items (%s). Finish your current step, then call agenda.", strings.Join(parts, ", "))
}

type agendaIn struct {
	WaitS int `json:"wait_s"`
}

// agendaView is the queue in the order to handle it.
type agendaView struct {
	// FromUser are the messages of the user.
	FromUser []message `json:"from_user"`
	// Questions are the messages of agents that wait for your answer now.
	Questions []message `json:"questions"`
	// Messages are the other messages.
	Messages []message    `json:"messages"`
	Notices  []noticeView `json:"notices"`
	// WaitingOnYou are the agents that wait for an answer from you.
	WaitingOnYou []string `json:"waiting_on_you"`
	// Blocked are the agents that said they are blocked, with their notes.
	Blocked []string `json:"blocked"`
	Dropped int      `json:"dropped,omitempty"`
}

// agenda gives everything that waits for the orchestrator. With wait_s, it waits that long
// for a first item when nothing waits.
func (s *shim) agenda(ctx context.Context, in agendaIn) (any, error) {
	client, inbox, l, err := s.joined()
	if err != nil {
		return nil, err
	}
	items := inbox.take()
	if len(items) == 0 && in.WaitS > 0 {
		p := inbox.wait("")
		if first, ok := await(ctx, p, s.timeout(in.WaitS)); ok {
			items = append(first, inbox.take()...)
		} else if first, ended := p.stop(); ended {
			items = append(first, inbox.take()...)
		}
	}
	if s.nudge != nil {
		s.nudge.read()
	}
	v := agendaView{FromUser: []message{}, Questions: []message{}, Messages: []message{}, Notices: []noticeView{}, WaitingOnYou: []string{}, Blocked: []string{}, Dropped: inbox.dropped()}
	waiting := map[string]bool{}
	if view, err := client.view(ctx); err == nil {
		for _, p := range view.Peers {
			if p.Online && p.WaitingOn == l.me {
				waiting[p.Name] = true
				v.WaitingOnYou = append(v.WaitingOnYou, p.Name)
			}
			if p.Online && p.State == "blocked" {
				v.Blocked = append(v.Blocked, strings.TrimSpace(p.Name+": "+p.Note))
			}
		}
	}
	// An agent that waits on you asked its question last: its newest message is the question.
	question := map[string]string{}
	for _, it := range items {
		if it.msg != nil && waiting[it.msg.From] {
			question[it.msg.From] = it.msg.ID
		}
	}
	for _, it := range items {
		switch {
		case it.notice != nil:
			v.Notices = append(v.Notices, noticeView{*it.notice, noticeText(*it.notice)})
		case it.msg.From == wire.Operator:
			v.FromUser = append(v.FromUser, *it.msg)
		case question[it.msg.From] == it.msg.ID:
			v.Questions = append(v.Questions, *it.msg)
		default:
			v.Messages = append(v.Messages, *it.msg)
		}
	}
	return v, nil
}

// sendNudge pushes one line that says what waits on the agenda. Without the channel there is
// no push: the orchestrator then calls agenda with wait_s.
func (s *shim) sendNudge() {
	s.mu.Lock()
	b := s.inbox
	s.mu.Unlock()
	if b == nil || !s.o.Push {
		return
	}
	items := b.peek()
	if len(items) == 0 {
		s.nudge.read()
		return
	}
	if err := s.push(s.ctx, nudgeText(items), map[string]string{"kind": "notice", "notice": "agenda", "items": fmt.Sprint(len(items))}); err != nil {
		s.logf("coop: nudge: %v", err)
	}
}
