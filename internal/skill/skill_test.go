package skill_test

import (
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/skill"
)

func TestTheSkillHasFrontMatter(t *testing.T) {
	if !strings.HasPrefix(skill.Text, "---\nname: coop\ndescription: ") {
		t.Fatalf("front matter: %q", skill.Text[:40])
	}
	if !strings.Contains(skill.Text, "status") || !strings.Contains(skill.Text, "send") {
		t.Fatal("the skill names no tools")
	}
}
