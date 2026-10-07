package view

import (
	"strings"
	"time"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

// StaleAsk is how long an ask may wait before it needs the operator.
const StaleAsk = 2 * time.Minute

// Item is one thing that needs the operator: a message for the operator with no answer
// (for_you), a question that waits too long (ask), or an agent that says it is blocked.
type Item struct {
	Kind    string // for_you, ask, blocked
	ID      string
	From    string
	To      string
	Since   string
	Address string
	Note    string
}

// Attention lists what needs the operator, messages first.
func Attention(v *model.Session, now time.Time) []Item {
	var out []Item
	for _, m := range v.ForYou() {
		out = append(out, Item{Kind: "for_you", ID: m.ID, From: m.From})
	}
	for _, q := range v.OpenAsks() {
		if t, ok := parseTime(q.Since); ok && now.Sub(t) >= StaleAsk {
			out = append(out, Item{Kind: "ask", ID: q.ID, From: q.From, To: q.To, Since: q.Since})
		}
	}
	for _, a := range v.AgentList() {
		if a.Online && a.State == "blocked" {
			out = append(out, Item{Kind: "blocked", Address: a.Address, Note: a.Note})
		}
	}
	return out
}

// RenderAttention is one line that counts the items, or nil when nothing needs the operator.
func RenderAttention(items []Item, now time.Time) Line {
	if len(items) == 0 {
		return nil
	}
	count := func(kind string) int {
		n := 0
		for _, it := range items {
			if it.Kind == kind {
				n++
			}
		}
		return n
	}
	var parts []string
	if n := count("for_you"); n > 0 {
		parts = append(parts, itoa(int64(n))+" for you")
	}
	if n := count("ask"); n > 0 {
		oldest := ""
		for _, it := range items {
			if it.Kind == "ask" && (oldest == "" || it.Since < oldest) {
				oldest = it.Since
			}
		}
		plural := ""
		if n > 1 {
			plural = "s"
		}
		parts = append(parts, itoa(int64(n))+" ask"+plural+" waiting "+Age(oldest, now))
	}
	if n := count("blocked"); n > 0 {
		parts = append(parts, itoa(int64(n))+" blocked")
	}
	return Line{Styled("⚑ ", Style{Color: "yellow", Bold: true}), Color(strings.Join(parts, " · "), "yellow"), Dim("   a: next")}
}

// Describe is a short text of one item for the status line.
func Describe(it Item) string {
	switch it.Kind {
	case "for_you":
		return "for you: #" + it.ID + " from " + it.From
	case "ask":
		return it.From + " waits for " + it.To + " (ask #" + it.ID + ")"
	}
	s := it.Address + " is blocked"
	if it.Note != "" {
		s += ": " + it.Note
	}
	return s
}
