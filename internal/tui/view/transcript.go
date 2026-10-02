package view

import (
	"strings"

	"github.com/AIToolSharing/agent_coop/internal/model"
)

const indent = "  "

// MsgVisible applies the agent and search filters to a message. A broadcast involves every
// agent of the session.
func MsgVisible(m *model.Msg, o Options) bool {
	if o.Agent != "" && m.From != o.Agent && m.To != o.Agent && m.To != "all" {
		return false
	}
	if o.Search != "" && !strings.Contains(strings.ToLower(m.Text), strings.ToLower(o.Search)) {
		return false
	}
	return true
}

func itemVisible(it model.Item, o Options) bool {
	if it.Msg != nil {
		return MsgVisible(it.Msg, o)
	}
	return o.System && (o.Agent == "" || it.Sys.Who == o.Agent)
}

type transcriptKey struct {
	sid    string
	width  int
	agent  string
	search string
	system bool
}

// Transcript renders a session as a conversation and keeps its output between repaints. A
// new item extends the output; a change to what was already shown (model.Session.Reshaped)
// starts it over. So a repaint costs the new items, not the whole session.
type Transcript struct {
	key      transcriptKey
	version  int
	reshaped int
	all      bool
	loc      string
	n        int
	lines    []Line
	ids      []string
	day      string
	burst    []model.Item
	burstAt  int
}

// Render gives the lines of v. The result is shared with the cache: do not change it.
func (t *Transcript) Render(v *model.Session, o Options) Rendered {
	key := transcriptKey{v.SID, o.Width, o.Agent, o.Search, o.System}
	loc := o.loc().String()
	if t.key != key || t.reshaped != v.Reshaped || t.n > len(v.Timeline) || t.loc != loc {
		*t = Transcript{key: key, reshaped: v.Reshaped, all: v.SID == model.AllSessions, loc: loc, burstAt: -1}
	} else if t.version == v.Version {
		return Rendered{Lines: t.lines, IDs: t.ids}
	}
	t.version = v.Version
	// Reopen the trailing burst: its line is drawn again once it is complete.
	if t.burstAt >= 0 {
		t.lines = t.lines[:t.burstAt]
		t.ids = t.ids[:t.burstAt]
		t.burstAt = -1
	}
	for ; t.n < len(v.Timeline); t.n++ {
		it := v.Timeline[t.n]
		if !itemVisible(it, o) {
			continue
		}
		day := Day(it.At, o.loc())
		if day != t.day {
			t.flush(o)
			t.day = day
			rule := strings.Repeat("─", max(0, o.Width-Width(day)-4))
			t.add(Line{Dim("── " + day + " " + rule)}, "", o)
		}
		if it.Sys != nil {
			t.burst = append(t.burst, it)
			continue
		}
		t.flush(o)
		t.message(v, it.Msg, o)
	}
	if len(t.burst) > 0 {
		// Draw the open burst now; keep it, so that the next system item extends it.
		t.burstAt = len(t.lines)
		t.add(t.burstLine(o), "", o)
	}
	if len(t.lines) == 0 {
		return Note("no messages yet", o.Width)
	}
	return Rendered{Lines: t.lines, IDs: t.ids}
}

func (t *Transcript) add(l Line, id string, o Options) {
	t.lines = append(t.lines, Fit(l, o.Width))
	t.ids = append(t.ids, id)
}

// flush writes the pending system items as one dim line and forgets them.
func (t *Transcript) flush(o Options) {
	if len(t.burst) == 0 {
		return
	}
	t.add(t.burstLine(o), "", o)
	t.burst = t.burst[:0]
}

// burstLine folds the pending system items into one line.
func (t *Transcript) burstLine(o Options) Line {
	parts := make([]string, 0, len(t.burst))
	for _, it := range t.burst {
		who := it.Sys.Who
		if t.all {
			who = it.SID + "/" + who
		}
		parts = append(parts, who+" "+it.Sys.Text)
	}
	return Line{Dim(Clock(t.burst[0].At, o.loc()) + "  · "), Dim(strings.Join(parts, " · "))}
}

// message writes the header, the quote of the message it answers, and the whole body.
func (t *Transcript) message(v *model.Session, m *model.Msg, o Options) {
	t.add(Header(m, o, t.all), m.ID, o)
	if m.ReplyTo != "" {
		quote := "↩ #" + m.ReplyTo
		if p := v.Msgs[m.ReplyTo]; p != nil {
			text := p.Text
			if p.Redacted {
				text = "[withdrawn]"
			}
			quote += " " + p.From + ": " + text
		}
		t.add(Line{S(indent), Styled(quote, Style{Color: "cyan", Dim: true})}, "", o)
	}
	if m.Redacted {
		t.add(Line{S(indent), Dim("[withdrawn]")}, "", o)
		return
	}
	for _, line := range Wrap(m.Text, o.Width-len(indent)) {
		t.add(Line{S(indent), S(line)}, "", o)
	}
}

// Header is the first line of a message: time, sender and target; the id and the reply count
// on the right.
func Header(m *model.Msg, o Options, all bool) Line {
	right := Line{Dim("#" + m.ID)}
	if n := len(m.Replies); n > 0 {
		right = append(right, Color("  ↳"+itoa(int64(n)), "cyan"))
	}
	left := Line{Dim(Clock(m.SentAt, o.loc())), S("  ")}
	if all {
		left = append(left, Dim("["+m.SID+"] "))
	}
	left = append(left, Styled(m.From, Style{Bold: true, Color: SenderColor(m.From)}), S(" → "), S(m.To))
	if m.To == "operator" {
		left = append(left, Styled("  for you", Style{Color: "yellow", Bold: true}))
	}
	used := Width(Plain(left)) + Width(Plain(right))
	out := append(Line{}, left...)
	out = append(out, S(strings.Repeat(" ", max(1, o.Width-used))))
	return append(out, right...)
}
