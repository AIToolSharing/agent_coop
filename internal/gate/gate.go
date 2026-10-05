// Package gate holds what the agent's side of the operator's gate needs: which tool calls
// pass with no check, and the texts that tell an agent what its gate means. The hub decides the
// gate; `coop hook` asks for it before each tool call of an agent that `coop claude` started.
package gate

import (
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/wire"
)

// Removed is the hub's answer for an agent that the operator removed.
const Removed = "removed"

// Exempt reports whether a tool call passes with no check: a tool of the coop server itself,
// so that a held agent can wait and talk, and ToolSearch, which loads those tools in Claude
// Code. Claude Code names a tool of the MCP server coop `mcp__coop__<tool>`.
func Exempt(tool string) bool {
	return tool == "ToolSearch" || strings.HasPrefix(tool, "mcp__coop__")
}

// wait tells the agent where to stay while it may not work.
const wait = "Call the coop `wait` tool and wait there. " +
	"You continue when a notice says that you are released. A task that comes with the release is in the same result."

// Text tells an agent what its gate means. It is the reason of a denied tool call and the
// text of a notice. It gives "" for run.
func Text(gate string) string {
	switch gate {
	case wire.GateHeld:
		return "The user holds you: you may not work yet. Do not call other tools. " + wait
	case wire.GatePaused:
		return "The user paused you. Stop your work now and do not call other tools. " + wait
	case Removed:
		return "The user removed you from the shared session and stopped your work. Do not call tools. " +
			"Say that the operator stopped you, then stop."
	}
	return ""
}

// Released is the text of the notice that ends a hold or a pause.
const Released = "The user released you. You may work now."
