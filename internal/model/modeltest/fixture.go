// Package modeltest holds the fixture the model and view tests share.
package modeltest

import (
	"fmt"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

const SID = "build-42"

var (
	Alice = wire.Address{Agent: "alice", Machine: "mac-1"}
	Bob   = wire.Address{Agent: "bob", Machine: "vps-2"}
	Carol = wire.Address{Agent: "carol", Machine: "mac-3"}
	t0    = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
)

// Now is the clock of the fixture: five minutes after its first event.
var Now = t0.Add(5 * time.Minute)

// At gives the ISO time s seconds after the fixture's first event.
func At(s float64) string {
	return t0.Add(time.Duration(s * float64(time.Second))).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// Fixture is the session of packages/tui/test/fixture.ts: Alice asks Bob, Bob answers, Carol
// broadcasts, the operator steps in, a message is withdrawn, one question stays open.
func Fixture() []model.Update {
	client := wire.Client{Name: "claude-code", Version: "2.1"}
	var seq int64
	var updates []model.Update
	ev := func(e wire.Event) string {
		seq++
		e.Seq = seq
		e.SID = SID
		updates = append(updates, model.Update{Event: &e})
		return fmt.Sprint(seq)
	}
	act := func(from wire.Address, a wire.Activity) {
		ev(wire.Event{Kind: wire.EventActivity, From: from.String(), Activity: &a})
	}
	msg := func(from, to, text, replyTo, sentAt string) string {
		return ev(wire.Event{Kind: wire.EventMsg, From: from, To: to, Text: text, ReplyTo: replyTo, SentAt: sentAt})
	}
	updates = append(updates,
		model.Update{Session: &wire.SessionUpdate{SID: SID, Record: &wire.SessionRecord{Status: "open", CreatedAt: At(-60)}}},
		model.Update{Session: &wire.SessionUpdate{SID: "docs", Record: &wire.SessionRecord{Status: "closed", CreatedAt: At(-600), ClosedAt: At(-60)}}},
	)
	act(Alice, wire.Activity{Kind: "joined", Host: "mac-1", Cwd: "/src/app", Client: client, At: At(0)})
	act(Bob, wire.Activity{Kind: "joined", Host: "vps-2", Cwd: "/srv/api", Client: wire.Client{Name: "codex", Version: "0.9"}, At: At(2)})
	act(Carol, wire.Activity{Kind: "joined", Host: "mac-3", Cwd: "/src/app", Client: client, At: At(3)})
	plan := msg(Carol.String(), "all", "I take src/users.ts and the tests", "", At(10))
	act(Alice, wire.Activity{Kind: "delivered", ID: plan, Via: "push", At: At(10.12)})
	act(Bob, wire.Activity{Kind: "delivered", ID: plan, Via: "pull", At: At(14)})
	q := msg(Alice.String(), Bob.String(), "What is the shape of GET /users?", "", At(20))
	act(Alice, wire.Activity{Kind: "wait_start", From: Bob.String(), ReplyTo: q, TimeoutS: 300, At: At(20.05)})
	act(Bob, wire.Activity{Kind: "delivered", ID: q, Via: "pull", At: At(21)})
	act(Bob, wire.Activity{Kind: "state", State: "working", Note: "answering alice", At: At(21.5)})
	a := msg(Bob.String(), Alice.String(), "{ id: number, name: string, email: string }", q, At(30))
	act(Alice, wire.Activity{Kind: "delivered", ID: a, Via: "ask", At: At(30.09)})
	act(Alice, wire.Activity{Kind: "wait_end", Result: "message", At: At(30.1)})
	wrong := msg(Carol.String(), Bob.String(), "the password is hunter2", "", At(40))
	ev(wire.Event{Kind: wire.EventRedact, ID: wrong, At: At(45)})
	op := msg(wire.Operator, "all", "Please run the tests before you say done", "", At(50))
	msg(Bob.String(), wire.Operator, "Will do; CI is running", op, At(55))
	q2 := msg(Carol.String(), Bob.String(), "Can I change the users table?", "", At(60))
	act(Carol, wire.Activity{Kind: "wait_start", From: Bob.String(), ReplyTo: q2, TimeoutS: 300, At: At(60.02)})
	act(Bob, wire.Activity{Kind: "state", State: "blocked", Note: "waiting for CI", At: At(70)})

	presence := func(a wire.Address, state, note string, waiting *wire.Waiting) model.Update {
		return model.Update{Presence: &wire.PresenceUpdate{
			Key:    wire.BuildPresenceKey(wire.PresenceKey{SID: SID, Agent: a}),
			Record: &wire.PresenceRecord{Host: a.Machine, Cwd: "/src/app", Client: client, State: state, Note: note, JoinedAt: At(0), Waiting: waiting},
		}}
	}
	updates = append(updates,
		presence(Alice, "working", "parser", nil),
		presence(Bob, "blocked", "waiting for CI", nil),
		presence(Carol, "working", "users.ts", &wire.Waiting{On: Bob.String(), ReplyTo: q2, Since: At(60.02)}),
	)
	return updates
}
