package view

import (
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

// RenderThreads shows the open asks first (who is blocked on whom), then every conversation
// as a reply tree. Every message is whole.
func RenderThreads(v *model.Session, o Options) Rendered {
	var r Rendered
	add := func(l Line, id string) {
		r.Lines = append(r.Lines, Fit(l, o.Width))
		r.IDs = append(r.IDs, id)
	}
	asks := v.OpenAsks()
	add(Line{Bold("OPEN ASKS (" + itoa(int64(len(asks))) + ")")}, "")
	if len(asks) == 0 {
		add(Line{Dim("  none")}, "")
	}
	for _, q := range asks {
		age := Age(q.Since, o.Now)
		add(Line{
			Styled("  ⏳ "+strings.Repeat(" ", max(0, 4-Width(age)))+age+"  ", Style{Color: "yellow", Inverse: q.ID == o.Selected}),
			Bold(q.From + " → " + q.To),
			Dim("  #" + q.ID),
		}, q.ID)
		for _, line := range Wrap(q.Question.Text, o.Width-10) {
			add(Line{S(strings.Repeat(" ", 10)), S(line)}, "")
		}
	}
	add(Line{S("")}, "")
	add(Line{Bold("THREADS")}, "")
	var walk func(m *model.Msg, depth int)
	walk = func(m *model.Msg, depth int) {
		prefix := ""
		if depth > 0 {
			prefix = strings.Repeat("   ", depth-1) + " └─ "
		}
		head := Line{Dim(prefix), Styled("#"+m.ID+" ", Style{Dim: true, Inverse: m.ID == o.Selected}),
			Styled(m.From, Style{Bold: true, Color: SenderColor(m.From)}), S(" → " + m.To)}
		if m.To == "operator" {
			head = append(head, Styled("  for you", Style{Color: "yellow", Bold: true}))
		}
		add(head, m.ID)
		pad := strings.Repeat(" ", Width(prefix)+2)
		if m.Redacted {
			add(Line{S(pad), Dim("[withdrawn]")}, "")
		} else {
			for _, line := range Wrap(m.Text, o.Width-Width(pad)) {
				add(Line{S(pad), S(line)}, "")
			}
		}
		for _, c := range m.Replies {
			walk(c, depth+1)
		}
	}
	for _, m := range v.Messages() {
		if !MsgVisible(m, o) {
			continue
		}
		if m.ReplyTo != "" && v.Msgs[m.ReplyTo] != nil {
			continue
		}
		walk(m, 0)
	}
	return r
}
