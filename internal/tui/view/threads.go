package view

import (
	"strconv"
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

// Threads shows the open asks first (who is blocked on whom), then every conversation as a
// reply tree. Every message is whole. The blocks are built again when the session changes
// (a reply goes under its parent, so the list is not append-only); they render on demand.
// ponytail: the build walks every message, without wrapping; make it incremental when a
// session of 50 000 messages shows a pause on each new message in the threads view.
type Threads struct {
	sid     string
	agent   string
	version int
	blocks  []Block
}

// Render gives the buffer of v.
func (t *Threads) Render(v *model.Session, o Options) Buffer {
	if t.sid == v.SID && t.agent == o.Agent && t.version == v.Version && t.blocks != nil {
		return Buffer{Blocks: t.blocks}
	}
	t.sid, t.agent, t.version = v.SID, o.Agent, v.Version
	t.blocks = nil
	asks := v.OpenAsks()
	t.blocks = append(t.blocks, Heading("OPEN ASKS ("+strconv.Itoa(len(asks))+")"))
	if len(asks) == 0 {
		t.blocks = append(t.blocks, Block{Render: func(o Options) []Line { return []Line{Fit(Line{Dim("  none")}, o.Width)} }})
	}
	for _, q := range asks {
		t.blocks = append(t.blocks, askBlock(q))
	}
	t.blocks = append(t.blocks, Blank(), Heading("THREADS"))
	var walk func(m *model.Msg, depth int)
	walk = func(m *model.Msg, depth int) {
		t.blocks = append(t.blocks, threadBlock(m, depth))
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
	return Buffer{Blocks: t.blocks}
}

func askBlock(q *model.Ask) Block {
	return Block{ID: q.ID, Text: strings.ToLower(q.Question.Text), Render: func(o Options) []Line {
		age := Age(q.Since, o.Now)
		lines := []Line{Fit(Line{
			Styled("  ⏳ "+strings.Repeat(" ", max(0, 4-Width(age)))+age+"  ", Style{Color: "yellow", Inverse: q.ID == o.Selected}),
			Bold(q.From + " → " + q.To),
			Dim("  #" + q.ID),
		}, o.Width)}
		for _, line := range Wrap(q.Question.Text, o.Width-10) {
			lines = append(lines, Fit(Line{S(strings.Repeat(" ", 10)), S(line)}, o.Width))
		}
		return lines
	}}
}

func threadBlock(m *model.Msg, depth int) Block {
	prefix := ""
	if depth > 0 {
		prefix = strings.Repeat("   ", depth-1) + " └─ "
	}
	pad := strings.Repeat(" ", Width(prefix)+2)
	return Block{ID: m.ID, Text: strings.ToLower(m.Text), Render: func(o Options) []Line {
		lines := []Line{Fit(Line{Dim(prefix), Styled("#"+m.ID+" ", Style{Dim: true, Inverse: m.ID == o.Selected}),
			Bold(m.From), S(" → " + m.To)}, o.Width)}
		for _, line := range Wrap(m.Text, o.Width-Width(pad)) {
			lines = append(lines, Fit(Line{S(pad), S(line)}, o.Width))
		}
		return lines
	}}
}
