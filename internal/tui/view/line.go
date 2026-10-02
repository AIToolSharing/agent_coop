// Package view renders the model into lines of styled segments. The renderers are pure: the
// shell paints the segments and decides what is visible. Messages are never cut: a message
// body is wrapped to the width, whole. Only names in narrow columns are shortened.
package view

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Style is what a segment looks like. Color is a lipgloss color name or an ANSI index.
type Style struct {
	Color   string
	Dim     bool
	Bold    bool
	Inverse bool
}

// Seg is a run of text with one style.
type Seg struct {
	Text  string
	Style Style
}

// Line is a list of segments. A rendered line is fitted to the width.
type Line []Seg

// Rendered is a renderer's output: the lines, and per line the message id it stands for, or
// an empty string. Only one line per message carries the id (the header line).
type Rendered struct {
	Lines []Line
	IDs   []string
}

// Options are what every renderer takes.
type Options struct {
	Width int
	Now   time.Time
	// Loc is the operator's time zone; nil means time.Local.
	Loc *time.Location
	// Agent, when set, keeps only messages from or to this agent, and its system lines.
	Agent string
	// Search, when set, keeps only messages whose text contains it (any case).
	Search string
	// System shows joins, leaves, states and waits.
	System bool
	// Selected is the message under the cursor.
	Selected string
}

func (o Options) loc() *time.Location {
	if o.Loc != nil {
		return o.Loc
	}
	return time.Local
}

func S(text string) Seg                { return Seg{Text: text} }
func Dim(text string) Seg              { return Seg{Text: text, Style: Style{Dim: true}} }
func Bold(text string) Seg             { return Seg{Text: text, Style: Style{Bold: true}} }
func Color(text, color string) Seg     { return Seg{Text: text, Style: Style{Color: color}} }
func Styled(text string, st Style) Seg { return Seg{Text: text, Style: st} }

// Width is the number of terminal cells s takes.
func Width(s string) int { return ansi.StringWidth(s) }

// Plain is the text of a line without styles.
func Plain(l Line) string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}

// OneLine replaces line breaks and tabs, which would break the layout.
func OneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	return strings.ReplaceAll(s, "\t", " ")
}

// Cut shortens s to width cells, with an ellipsis when it was longer, then pads it.
func Cut(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = OneLine(s)
	if Width(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", max(0, width-Width(s)))
}

// Fit cuts a line to width cells and pads it with spaces.
func Fit(l Line, width int) Line {
	out := make(Line, 0, len(l)+1)
	left := width
	for _, s := range l {
		if left <= 0 {
			break
		}
		text := OneLine(s.Text)
		w := Width(text)
		if w <= left {
			out = append(out, Seg{Text: text, Style: s.Style})
			left -= w
			continue
		}
		out = append(out, Seg{Text: ansi.Truncate(text, left, "…"), Style: s.Style})
		left = 0
	}
	if left > 0 {
		out = append(out, S(strings.Repeat(" ", left)))
	}
	return out
}

// Wrap breaks text into lines of at most width cells. Words stay whole when they fit; a
// longer word is broken. Line breaks in the text stay line breaks. Never empty.
func Wrap(text string, width int) []string {
	width = max(1, width)
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\t", "  ")
	var out []string
	for _, line := range strings.Split(ansi.Wrap(text, width, ""), "\n") {
		// ansi.Wrap keeps a leading space with the word after it; cut what is still too wide.
		for Width(line) > width {
			head := ansi.Cut(line, 0, width)
			rest := ansi.Cut(line, Width(head), Width(line))
			if head == "" || rest == line {
				break
			}
			out = append(out, head)
			line = rest
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func parseTime(iso string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, iso)
	return t, err == nil
}

// Clock is HH:MM:SS of an ISO time, in the operator's zone.
func Clock(iso string, loc *time.Location) string {
	t, ok := parseTime(iso)
	if !ok {
		if len(iso) >= 19 {
			return iso[11:19]
		}
		return iso
	}
	return t.In(loc).Format("15:04:05")
}

// Stamp is YYYY-MM-DD HH:MM:SS of an ISO time, in the operator's zone.
func Stamp(iso string, loc *time.Location) string {
	t, ok := parseTime(iso)
	if !ok {
		return iso
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

// Day is YYYY-MM-DD of an ISO time, in the operator's zone.
func Day(iso string, loc *time.Location) string {
	t, ok := parseTime(iso)
	if !ok {
		if len(iso) >= 10 {
			return iso[:10]
		}
		return iso
	}
	return t.In(loc).Format("2006-01-02")
}

// Age is a short age: 42s, 3m, 2h, 5d.
func Age(iso string, now time.Time) string {
	t, ok := parseTime(iso)
	if !ok {
		return "?"
	}
	s := int64(max(0, now.Sub(t).Round(time.Second)) / time.Second)
	switch {
	case s < 60:
		return itoa(s) + "s"
	case s < 3600:
		return itoa(s/60) + "m"
	case s < 86400:
		return itoa(s/3600) + "h"
	}
	return itoa(s/86400) + "d"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// StateColor is the color of an agent state.
func StateColor(state string) string {
	switch state {
	case "working":
		return "green"
	case "blocked":
		return "red"
	case "done":
		return "blue"
	}
	return "gray"
}

// SenderColor marks the operator's own messages.
func SenderColor(from string) string {
	if from == "operator" {
		return "yellow"
	}
	return ""
}

// Note is a one-line placeholder, fitted like every other line.
func Note(text string, width int) Rendered {
	return Rendered{Lines: []Line{Fit(Line{Dim(text)}, width)}, IDs: []string{""}}
}
