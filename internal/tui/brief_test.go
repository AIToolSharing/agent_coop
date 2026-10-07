package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
	"github.com/AIToolSharing/agent_coop/internal/model/modeltest"
	"github.com/AIToolSharing/agent_coop/internal/wire"
)

func TestParseBrief(t *testing.T) {
	for arg, want := range map[string]struct {
		every time.Duration
		off   bool
		err   bool
	}{
		"":           {},
		"off":        {off: true},
		"every 10m":  {every: 10 * time.Minute},
		"every 2h":   {every: 2 * time.Hour},
		"every 15":   {every: 15 * time.Minute},
		"every 0m":   {err: true},
		"every soon": {err: true},
		"every 25h":  {err: true},
		"now":        {err: true},
		"every":      {err: true},
	} {
		every, off, err := parseBrief(arg)
		if every != want.every || off != want.off || (err != nil) != want.err {
			t.Errorf("%q: %v %v %v, want %+v", arg, every, off, err, want)
		}
	}
}

// The brief reads the agents with their states and the newest messages, oldest first. A
// longer text is cut at its start, so that the newest messages stay.
func TestBriefInputHasTheAgentsAndTheNewestMessagesOldestFirst(t *testing.T) {
	store := model.New()
	for _, u := range modeltest.Fixture() {
		store.Apply(u)
	}
	in := briefInput(store.View(modeltest.SID), 3)
	for _, want := range []string{
		"Session build-42.",
		modeltest.Alice.String() + ": online, working",
		modeltest.Bob.String() + ": online, blocked, gate run, note: waiting for CI",
		"waits on " + modeltest.Bob.String(),
		"Can I change the users table?",
	} {
		if !strings.Contains(in, want) {
			t.Errorf("the input lacks %q:\n%s", want, in)
		}
	}
	if strings.Contains(in, "I take src/users.ts") {
		t.Errorf("the input has an old message beyond the limit:\n%s", in)
	}
	if strings.Index(in, "Please run the tests") > strings.Index(in, "Can I change the users table?") {
		t.Errorf("the messages are not oldest first:\n%s", in)
	}
	// Many long messages: the text is cut at the start.
	long := strings.Repeat("x", 1900)
	for i := range 40 {
		store.Apply(model.Update{Event: &wire.Event{Kind: wire.EventMsg, Seq: int64(1000 + i), SID: modeltest.SID, From: modeltest.Alice.String(), To: wire.Broadcast, Text: long, SentAt: modeltest.At(float64(100 + i))}})
	}
	in = briefInput(store.View(modeltest.SID), briefMessages)
	if len(in) > briefMaxBytes+len("[…]") || !strings.HasPrefix(in, "[…]") || !strings.HasSuffix(in, long+"\n") {
		t.Fatalf("cut input: %d bytes, starts %q, ends %q", len(in), in[:10], in[len(in)-10:])
	}
}
