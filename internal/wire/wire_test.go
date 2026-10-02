package wire_test

import (
	"encoding/json"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

var token = rapid.StringMatching(`^[a-z0-9_-]{1,64}$`)

// An agent name: a token that is not a reserved word.
func agentName() *rapid.Generator[string] {
	return token.Filter(func(s string) bool { return s != "operator" && s != "all" })
}

func address() *rapid.Generator[wire.Address] {
	return rapid.Custom(func(t *rapid.T) wire.Address {
		return wire.Address{Agent: agentName().Draw(t, "agent"), Machine: token.Draw(t, "machine")}
	})
}

func TestAddressRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		a := address().Draw(rt, "a")
		got, ok := wire.ParseAddress(a.String())
		if !ok || got != a {
			rt.Fatalf("%q -> %v %v", a.String(), got, ok)
		}
	})
}

func TestAddressRejectsReservedNamesAndBadShapes(t *testing.T) {
	for _, s := range []string{"operator@mac", "all@mac", "alice", "alice@", "@mac", "a@b@c", "Alice@mac", "alice@m.ac", ""} {
		if _, ok := wire.ParseAddress(s); ok {
			t.Errorf("%q: accepted", s)
		}
	}
}

func TestSubjectRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := wire.Subject{Kind: rapid.SampledFrom([]string{"msg", "evt", "ops"}).Draw(rt, "kind"), SID: token.Draw(rt, "sid")}
		if s.Kind != "ops" {
			s.From = address().Draw(rt, "from")
		}
		got, ok := wire.ParseSubject(wire.BuildSubject(s))
		if !ok || got != s {
			rt.Fatalf("%q -> %+v %v", wire.BuildSubject(s), got, ok)
		}
	})
}

func TestKeysRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p := wire.PresenceKey{SID: token.Draw(rt, "sid"), Agent: address().Draw(rt, "agent")}
		if got, ok := wire.ParsePresenceKey(wire.BuildPresenceKey(p)); !ok || got != p {
			rt.Fatalf("presence %q -> %+v %v", wire.BuildPresenceKey(p), got, ok)
		}
		k := wire.SessionsKey{Kind: "kick", SID: p.SID, Target: p.Agent}
		if got, ok := wire.ParseSessionsKey(wire.BuildSessionsKey(k)); !ok || got != k {
			rt.Fatalf("kick %q -> %+v %v", wire.BuildSessionsKey(k), got, ok)
		}
		s := wire.SessionsKey{Kind: "session", SID: p.SID}
		if got, ok := wire.ParseSessionsKey(wire.BuildSessionsKey(s)); !ok || got != s {
			rt.Fatalf("session %q -> %+v %v", wire.BuildSessionsKey(s), got, ok)
		}
	})
}

func TestKeyExamplesFromTheTypeScriptFixture(t *testing.T) {
	k, ok := wire.ParseSessionsKey("build-42.kick.vps-2.bob")
	if !ok || k.Kind != "kick" || k.SID != "build-42" || k.Target != (wire.Address{Agent: "bob", Machine: "vps-2"}) {
		t.Fatalf("%+v %v", k, ok)
	}
	p, ok := wire.ParsePresenceKey("build-42.mac-1.alice")
	if !ok || p.SID != "build-42" || p.Agent != (wire.Address{Agent: "alice", Machine: "mac-1"}) {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := wire.ParsePresenceKey("build-42.mac-1.operator"); ok {
		t.Fatal("operator is not an agent")
	}
}

// A random valid event of each kind.
func event() *rapid.Generator[wire.Event] {
	iso := rapid.SampledFrom([]string{"2026-09-30T12:00:00.000Z", "2026-09-30T12:00:10.120Z", "2026-10-01T03:21:00Z"})
	id := rapid.StringMatching(`^[1-9][0-9]{0,6}$`)
	text := rapid.StringN(1, 200, -1)
	return rapid.Custom(func(t *rapid.T) wire.Event {
		e := wire.Event{Seq: rapid.Int64Range(1, 1<<40).Draw(t, "seq"), SID: token.Draw(t, "sid")}
		switch rapid.IntRange(0, 3).Draw(t, "kind") {
		case 0:
			e.Kind = wire.EventMsg
			if rapid.Bool().Draw(t, "op") {
				e.From = wire.Operator
			} else {
				e.From = address().Draw(t, "from").String()
			}
			switch rapid.IntRange(0, 2).Draw(t, "to") {
			case 0:
				e.To = wire.Broadcast
			case 1:
				e.To = wire.Operator
			default:
				e.To = address().Draw(t, "to").String()
			}
			e.Text = text.Draw(t, "text")
			if rapid.Bool().Draw(t, "reply") {
				e.ReplyTo = id.Draw(t, "reply_to")
			}
			e.SentAt = iso.Draw(t, "sent_at")
		case 1:
			e.Kind = wire.EventKick
			e.Target = address().Draw(t, "target").String()
			e.At = iso.Draw(t, "at")
		case 2:
			e.Kind = wire.EventRedact
			e.ID = id.Draw(t, "id")
			e.At = iso.Draw(t, "at")
		default:
			e.Kind = wire.EventActivity
			e.From = address().Draw(t, "from").String()
			a := wire.Activity{At: iso.Draw(t, "at")}
			switch rapid.IntRange(0, 5).Draw(t, "akind") {
			case 0:
				a.Kind = "joined"
				a.Host = token.Draw(t, "host")
				a.Cwd = "/src/" + token.Draw(t, "cwd")
				a.Client = wire.Client{Name: "claude-code", Version: "2.1"}
			case 1:
				a.Kind = "left"
				a.Reason = rapid.SampledFrom([]string{"disconnected", "kicked", "revoked", "closed"}).Draw(t, "reason")
			case 2:
				a.Kind = "state"
				a.State = rapid.SampledFrom([]string{"working", "blocked", "done", "idle"}).Draw(t, "state")
				if rapid.Bool().Draw(t, "note") {
					a.Note = text.Draw(t, "note")
				}
			case 3:
				a.Kind = "delivered"
				a.ID = id.Draw(t, "id")
				a.Via = rapid.SampledFrom([]string{"push", "pull", "ask"}).Draw(t, "via")
			case 4:
				a.Kind = "wait_start"
				if rapid.Bool().Draw(t, "from?") {
					if rapid.Bool().Draw(t, "op?") {
						a.From = wire.Operator
					} else {
						a.From = address().Draw(t, "wfrom").String()
					}
				}
				if rapid.Bool().Draw(t, "reply?") {
					a.ReplyTo = id.Draw(t, "wreply")
				}
				a.TimeoutS = rapid.IntRange(1, 600).Draw(t, "timeout")
			default:
				a.Kind = "wait_end"
				a.Result = rapid.SampledFrom([]string{"message", "timeout", "cancelled"}).Draw(t, "result")
			}
			e.Activity = &a
		}
		return e
	})
}

func TestEventEncodeDecodeRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		e := event().Draw(rt, "event")
		subject, payload, err := wire.EncodeEvent(e)
		if err != nil {
			rt.Fatal(err)
		}
		got, ok := wire.DecodeEvent(subject, payload, e.Seq)
		if !ok {
			rt.Fatalf("decode failed: %s %s", subject, payload)
		}
		if got.Activity != nil && e.Activity != nil {
			if *got.Activity != *e.Activity {
				rt.Fatalf("activity %+v != %+v", *got.Activity, *e.Activity)
			}
			got.Activity, e.Activity = nil, nil
		}
		if got != e {
			rt.Fatalf("%+v != %+v (subject %s payload %s)", got, e, subject, payload)
		}
	})
}

func TestDecodeMatchesTheTypeScriptWireFormat(t *testing.T) {
	// Lines as the TS hub publishes them (toWire in packages/core/src/schema.ts).
	cases := []struct {
		subject, payload string
		want             wire.Event
	}{
		{"coop.build-42.msg.mac-1.alice", `{"to":"bob@vps-2","text":"What is the shape of GET /users?","sent_at":"2026-09-30T12:00:20.000Z"}`,
			wire.Event{Kind: wire.EventMsg, Seq: 7, SID: "build-42", From: "alice@mac-1", To: "bob@vps-2", Text: "What is the shape of GET /users?", SentAt: "2026-09-30T12:00:20.000Z"}},
		{"coop.build-42.ops", `{"kind":"msg","to":"all","text":"Please run the tests before you say done","sent_at":"2026-09-30T12:00:50.000Z"}`,
			wire.Event{Kind: wire.EventMsg, Seq: 7, SID: "build-42", From: "operator", To: "all", Text: "Please run the tests before you say done", SentAt: "2026-09-30T12:00:50.000Z"}},
		{"coop.build-42.ops", `{"kind":"redact","id":"14","at":"2026-09-30T12:00:45.000Z"}`,
			wire.Event{Kind: wire.EventRedact, Seq: 7, SID: "build-42", ID: "14", At: "2026-09-30T12:00:45.000Z"}},
		{"coop.build-42.ops", `{"kind":"kick","target":"bob@vps-2","at":"2026-09-30T12:01:00.000Z"}`,
			wire.Event{Kind: wire.EventKick, Seq: 7, SID: "build-42", Target: "bob@vps-2", At: "2026-09-30T12:01:00.000Z"}},
	}
	for _, c := range cases {
		got, ok := wire.DecodeEvent(c.subject, []byte(c.payload), 7)
		if !ok || got != c.want {
			t.Errorf("%s %s\n got %+v\nwant %+v", c.subject, c.payload, got, c.want)
		}
	}
	got, ok := wire.DecodeEvent("coop.build-42.evt.vps-2.bob", []byte(`{"kind":"delivered","id":"4","via":"pull","at":"2026-09-30T12:00:14.000Z"}`), 6)
	if !ok || got.Kind != wire.EventActivity || got.From != "bob@vps-2" || got.Activity.Kind != "delivered" || got.Activity.ID != "4" || got.Activity.Via != "pull" {
		t.Fatalf("%+v %v", got, ok)
	}
	for _, bad := range []struct{ subject, payload string }{
		{"coop.build-42.msg.mac-1.alice", `{"to":"bob@vps-2","sent_at":"x"}`},
		{"coop.build-42.ops", `{"kind":"what"}`},
		{"nope.build-42.ops", `{"kind":"redact","id":"1","at":"2026-09-30T12:00:45.000Z"}`},
		{"coop.build-42.evt.mac-1.alice", `not json`},
	} {
		if _, ok := wire.DecodeEvent(bad.subject, []byte(bad.payload), 1); ok {
			t.Errorf("accepted %s %s", bad.subject, bad.payload)
		}
	}
}

func TestAdminEventDecoding(t *testing.T) {
	ev, err := wire.ParseAdminEvent([]byte(`{"kind":"event","seq":12,"subject":"coop.build-42.ops","payload":"{\"kind\":\"redact\",\"id\":\"14\",\"at\":\"2026-09-30T12:00:45.000Z\"}"}`))
	if err != nil || ev.Kind != "event" || ev.Event == nil || ev.Event.Kind != wire.EventRedact || ev.Event.Seq != 12 || ev.Event.ID != "14" {
		t.Fatalf("%+v %v", ev, err)
	}
	s, err := wire.ParseAdminEvent([]byte(`{"kind":"session","session":"docs","revision":3,"record":{"status":"closed","created_at":"2026-09-30T11:50:00.000Z","closed_at":"2026-09-30T11:59:00.000Z"}}`))
	if err != nil || s.Session == nil || s.Session.SID != "docs" || s.Session.Revision != 3 || s.Session.Record == nil || s.Session.Record.Status != "closed" || s.Session.Record.ClosedAt != "2026-09-30T11:59:00.000Z" {
		t.Fatalf("%+v %v", s, err)
	}
	gone, err := wire.ParseAdminEvent([]byte(`{"kind":"presence","key":"build-42.vps-2.bob","revision":9,"record":null}`))
	if err != nil || gone.Presence == nil || gone.Presence.Record != nil || gone.Presence.Key != "build-42.vps-2.bob" {
		t.Fatalf("%+v %v", gone, err)
	}
	p, err := wire.ParseAdminEvent([]byte(`{"kind":"presence","key":"build-42.mac-3.carol","revision":10,"record":{"host":"mac-3","cwd":"/src/app","client":{"name":"claude-code","version":"2.1"},"state":"working","note":"users.ts","joined_at":"2026-09-30T12:00:00.000Z","queued":0,"waiting":{"on":"bob@vps-2","reply_to":"18","since":"2026-09-30T12:01:00.020Z"}}}`))
	if err != nil || p.Presence.Record == nil || p.Presence.Record.Waiting == nil || p.Presence.Record.Waiting.On != "bob@vps-2" || p.Presence.Record.Client.Name != "claude-code" {
		t.Fatalf("%+v %v", p, err)
	}
	k, err := wire.ParseAdminEvent([]byte(`{"kind":"kick","key":"build-42.kick.vps-2.bob","revision":2,"record":{"at":"2026-09-30T12:01:00.000Z"}}`))
	if err != nil || k.Kick == nil || k.Kick.Record == nil || k.Kick.Record.At == "" {
		t.Fatalf("%+v %v", k, err)
	}
	snap, err := wire.ParseAdminEvent([]byte(`{"kind":"snapshot","bucket":"presence"}`))
	if err != nil || snap.Kind != "snapshot" || snap.Bucket != "presence" {
		t.Fatalf("%+v %v", snap, err)
	}
	if _, err := wire.ParseAdminEvent([]byte(`{"kind":"other"}`)); err == nil {
		t.Fatal("unknown kind accepted")
	}
	// A record round trip keeps the JSON field names the hub uses.
	var rec wire.PresenceRecord
	raw := `{"host":"mac-1","cwd":"/x","client":{"name":"c","version":"1"},"state":"idle","joined_at":"2026-09-30T12:00:00.000Z","queued":2}`
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(rec)
	if string(out) != raw {
		t.Fatalf("record json changed:\n%s\n%s", out, raw)
	}
}
