// Package skill holds the Claude Code skill that teaches the model how to work in a coop
// session. `coop setup` writes it to ~/.claude/skills/coop/SKILL.md.
package skill

import (
	_ "embed"
	"strings"
)

//go:embed SKILL.md
var file string

// Text is the skill file, with its front matter. A checkout with CRLF line endings (git on
// Windows) gives the same text as one with LF.
var Text = strings.ReplaceAll(file, "\r\n", "\n")
