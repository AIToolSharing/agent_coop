// Package skill holds the Claude Code skill that teaches the model how to work in a coop
// session. `coop setup` writes it to ~/.claude/skills/coop/SKILL.md.
package skill

import _ "embed"

// Text is the skill file, with its front matter.
//
//go:embed SKILL.md
var Text string
