package hub

import (
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/wire"
	"pgregory.net/rapid"
)

// The gate lets an agent work in exactly two cases: a known agent whose gate is run, and an
// agent that did not join yet in a session that does not hold new agents. A removed agent
// never works. This is the rule that the hook's answer depends on.
func TestGateOfLetsAnAgentWorkOnlyWhenNothingStopsIt(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		in := gateInput{
			Kicked:  rapid.Bool().Draw(rt, "kicked"),
			Known:   rapid.Bool().Draw(rt, "known"),
			Gate:    rapid.SampledFrom([]string{wire.GateRun, wire.GateHeld, wire.GatePaused}).Draw(rt, "gate"),
			Hold:    rapid.Bool().Draw(rt, "hold"),
			Trusted: rapid.Bool().Draw(rt, "trusted"),
		}
		got := gateOf(in)
		// An orchestrator is not held at its first join, but a pause or a stop holds it too.
		mayWork := !in.Kicked && (in.Known && in.Gate == wire.GateRun || !in.Known && (!in.Hold || in.Trusted))
		if (got == wire.GateRun) != mayWork {
			rt.Fatalf("gateOf(%+v) = %q, may work = %v", in, got, mayWork)
		}
		if in.Kicked && got != GateRemoved {
			rt.Fatalf("gateOf(%+v) = %q, want removed", in, got)
		}
		if got != GateRemoved && !wire.IsGate(got) {
			rt.Fatalf("gateOf(%+v) = %q is not a gate", in, got)
		}
	})
}
