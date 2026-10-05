package gate_test

import (
	"regexp"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/gate"
)

func TestExemptToolsAreCoopsOwnAndToolSearch(t *testing.T) {
	for tool, want := range map[string]bool{
		"mcp__coop__wait":           true,
		"mcp__coop__status":         true,
		"ToolSearch":                true,
		"Bash":                      false,
		"Edit":                      false,
		"mcp__github__create_issue": false,
		// A server that only has coop in its name is not coop.
		"mcp__coopx__wait": false,
		"":                 false,
	} {
		if got := gate.Exempt(tool); got != want {
			t.Errorf("Exempt(%q) = %v, want %v", tool, got, want)
		}
	}
}

// The words that an agent must never see (the same list as in the shim's tests): they would
// tell how the service works.
var forbiddenRE = regexp.MustCompile(`(?i)\b(hub|nats|jetstream|streams?|subjects?|kv|buckets?|consumers?|tokens?|https?|urls?|sse)\b|coop_`)

func TestTextsTellTheAgentWhatToDoAndNameNoPartOfTheService(t *testing.T) {
	for _, g := range []string{"held", "paused", "removed"} {
		text := gate.Text(g)
		if text == "" || forbiddenRE.MatchString(text) {
			t.Errorf("gate %s: %q", g, text)
		}
	}
	if gate.Text("run") != "" || gate.Text("") != "" {
		t.Error("run has a text")
	}
	for _, text := range []string{gate.Released, gate.NoAnswer} {
		if text == "" || forbiddenRE.MatchString(text) {
			t.Errorf("%q", text)
		}
	}
}
