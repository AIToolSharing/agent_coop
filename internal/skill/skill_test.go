package skill_test

import (
	"os"
	"strings"
	"testing"

	"github.com/AIToolSharing/agent_coop/internal/skill"
)

func TestTheSkillHasFrontMatterAndMatchesThePluginCopy(t *testing.T) {
	if !strings.HasPrefix(skill.Text, "---\nname: coop\n") {
		t.Fatalf("front matter: %q", skill.Text[:40])
	}
	// While the TypeScript plugin exists, the two copies must be the same file.
	b, err := os.ReadFile("../../plugin/skills/coop/SKILL.md")
	if err != nil {
		t.Skip("the plugin copy is gone")
	}
	if string(b) != skill.Text {
		t.Fatal("internal/skill/SKILL.md differs from plugin/skills/coop/SKILL.md")
	}
}
