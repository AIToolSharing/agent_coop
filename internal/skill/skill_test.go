package skill_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/skill"
)

// The agents only talk with each other: the skill names no person who watches the session or
// takes part in it.
func TestTheSkillNamesNoWatcher(t *testing.T) {
	if m := regexp.MustCompile(`(?i)\b(operator|watch\w*|monitor\w*|terminal ui|the user sees)\b`).FindString(skill.Text); m != "" {
		t.Fatalf("the skill says %q", m)
	}
}

func TestTheSkillHasFrontMatter(t *testing.T) {
	if !strings.HasPrefix(skill.Text, "---\nname: coop\ndescription: ") {
		t.Fatalf("front matter: %q", skill.Text[:40])
	}
	if !strings.Contains(skill.Text, "status") || !strings.Contains(skill.Text, "send") {
		t.Fatal("the skill names no tools")
	}
}
