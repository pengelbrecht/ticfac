package workerview

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// FrameOptions is what one drawn frame fits itself to.
type FrameOptions struct {
	// Title names the worker: "<tick>#<attempt>", and the run.
	Title string
	// Width and Height are the pane's; zero means unbounded.
	Width, Height int
	// Now is the clock the ages are measured against.
	Now time.Time
}

// outputLines is how many lines of a running tool's live output the frame
// shows: enough to see a build or a test run moving, few enough that two
// running tools still leave room for the conversation above them.
const outputLines = 3

// labelWidth pads each item's label so the bodies line up.
const labelWidth = 9

// Render draws the model as a frame: a header (who, what it is doing, the
// heartbeat), the settled conversation newest last, and — below a rule — what
// is in flight: the message the model is streaming and the tools running now
// with their last lines of output. It fits the pane: every line is cut to the
// width, and when the frame is taller than the pane the OLDEST conversation
// lines go first (counted, never silently), because the live part is what a
// watcher is there for.
func Render(m *Model, o FrameOptions) []string {
	header := []string{headerLine(m, o), heartbeatSummary(m.Heartbeat, o.Now)}

	var body []string
	for _, it := range m.Items {
		body = append(body, itemLine(it))
	}

	var live []string
	for _, it := range m.PartialItems() {
		live = append(live, liveLine(it, o.Width))
	}
	for _, run := range m.Running {
		line := fmt.Sprintf("⋯ %-*s %s", labelWidth, run.Name, run.Args)
		if !run.Seen.IsZero() && !o.Now.IsZero() {
			line = fmt.Sprintf("⋯ %-*s %s  %s", labelWidth, run.Name, Age(o.Now.Sub(run.Seen)), run.Args)
		}
		live = append(live, line)
		for _, out := range lastLines(run.Output, outputLines) {
			live = append(live, "  │ "+out)
		}
	}
	if m.Queued > 0 {
		live = append(live, fmt.Sprintf("» %d input(s) queued, placed after the running tool round", m.Queued))
	}
	if m.Ended != "" {
		live = append(live, "■ "+m.Ended)
	}

	rule := strings.Repeat("─", max(min(o.Width, 72), 8))
	lines := append([]string{}, header...)
	lines = append(lines, rule)
	tail := []string{}
	if len(live) > 0 {
		tail = append(tail, rule)
		tail = append(tail, live...)
	}
	if o.Height > 0 {
		room := o.Height - len(lines) - len(tail)
		if room < 1 {
			// A pane too short for the live part keeps the header and the
			// live part's newest lines.
			keep := max(o.Height-len(lines), 0)
			tail = tail[max(len(tail)-keep, 0):]
			body = nil
		} else if len(body) > room {
			dropped := len(body) - room + 1
			body = append([]string{fmt.Sprintf("… %d earlier line(s)", dropped)}, body[dropped:]...)
		}
	}
	lines = append(lines, body...)
	lines = append(lines, tail...)
	if o.Width > 0 {
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "…")
		}
	}
	return lines
}

// headerLine names the worker and says what it is doing now.
func headerLine(m *Model, o FrameOptions) string {
	parts := []string{o.Title}
	if m.ModelName != "" {
		parts = append(parts, m.ModelName)
	}
	parts = append(parts, Doing(m))
	return strings.Join(nonEmpty(parts), " · ")
}

// Doing is one word or phrase for what the worker is doing now.
func Doing(m *Model) string {
	switch {
	case m.Ended != "":
		return "ended"
	case m.State != nil && m.State.Phase == "settled":
		if m.State.ExitCode != nil {
			return fmt.Sprintf("settled (exit %d)", *m.State.ExitCode)
		}
		return "settled"
	case m.Retry != "":
		return "retrying the model call"
	case len(m.Running) > 0:
		names := make([]string, 0, len(m.Running))
		for _, run := range m.Running {
			names = append(names, run.Name)
		}
		return "running " + strings.Join(names, ", ")
	}
	if partial := m.PartialItems(); partial != nil {
		if len(partial) == 0 {
			return "waiting for the model"
		}
		switch partial[len(partial)-1].Kind {
		case KindThinking:
			return "thinking"
		case KindToolCall:
			return "calling " + partial[len(partial)-1].Tool
		default:
			return "writing"
		}
	}
	if m.State != nil && m.State.Phase != "" && m.State.Phase != "conversing" {
		return m.State.Phase
	}
	if m.Live {
		return "working"
	}
	return "idle"
}

// heartbeatSummary is the header's second line: f6o's heartbeat, read at a
// glance.
func heartbeatSummary(h Heartbeat, now time.Time) string {
	parts := []string{}
	switch {
	case h.Commits == 0:
		parts = append(parts, "no commit since attaching")
	case now.IsZero():
		parts = append(parts, plural(h.Commits, "commit"))
	default:
		parts = append(parts, fmt.Sprintf("%s, last %s ago", plural(h.Commits, "commit"), Age(now.Sub(h.LastCommit))))
	}
	parts = append(parts, plural(h.ModelCalls, "model call"), plural(h.ToolCalls, "tool call"))
	if h.LastTool != nil {
		last := "last " + h.LastTool.Tool
		if !h.LastTool.At.IsZero() && !now.IsZero() {
			last += " " + Age(now.Sub(h.LastTool.At)) + " ago"
		}
		parts = append(parts, last)
	}
	if h.InputTokens > 0 || h.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("%s in / %s out tokens", count(h.InputTokens), count(h.OutputTokens)))
	}
	return strings.Join(parts, " · ")
}

// HeartbeatLine is f6o's periodic activity line for a plain stream: the
// counts, their deltas since the previous line, and the last tool call with
// its age. A stuck worker's lines show no new commits and a growing age.
func HeartbeatLine(title string, h Heartbeat, prev *Heartbeat, since time.Duration, now time.Time) string {
	line := title + " heartbeat: " + plural(h.Commits, "commit")
	if prev != nil {
		line += fmt.Sprintf(" (+%d in %s)", h.Commits-prev.Commits, Age(since))
	}
	if !h.LastCommit.IsZero() {
		line += fmt.Sprintf(", last %s ago", Age(now.Sub(h.LastCommit)))
	}
	line += " · " + plural(h.ModelCalls, "model call")
	if prev != nil {
		line += fmt.Sprintf(" (+%d)", h.ModelCalls-prev.ModelCalls)
	}
	if h.LastTool != nil {
		line += " · last tool: " + strings.TrimSpace(h.LastTool.Tool+" "+h.LastTool.Args)
		if !h.LastTool.At.IsZero() {
			line += ", " + Age(now.Sub(h.LastTool.At)) + " ago"
		}
	}
	return line
}

// plural is "1 commit", "2 commits".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// PlainLine is one settled item as a plain stream line: the frame's line,
// with the body cut to a bound a log can carry.
func PlainLine(it Item) string { return ansi.Truncate(itemLine(it), plainWidth, "…") }

// plainWidth bounds a plain line.
const plainWidth = 400

// itemLine is one settled item, one line.
func itemLine(it Item) string {
	switch it.Kind {
	case KindInput:
		return labelled("›", "input", it.Text)
	case KindSteer:
		return labelled("»", "steer", it.Text)
	case KindThinking:
		return labelled("∴", "thinking", it.Text)
	case KindText:
		return labelled("✎", "answer", it.Text)
	case KindToolCall:
		return labelled("▸", it.Tool, it.Args)
	case KindToolResult:
		mark := "✓"
		if it.IsError {
			mark = "✗"
		}
		out := firstLine(it.Text)
		if out == "" {
			out = "(no output)"
		}
		return fmt.Sprintf("  %s %-*s %s", mark, labelWidth-2, it.Tool, oneLine(out))
	case KindNote:
		mark := "·"
		if it.IsError {
			mark = "!"
		}
		return mark + " " + oneLine(it.Text)
	}
	return oneLine(it.Text)
}

// liveLine is one block of the message the model is streaming: its TAIL, so
// the line moves as the model writes.
func liveLine(it Item, width int) string {
	label, text := "", it.Text
	switch it.Kind {
	case KindThinking:
		label = "thinking"
	case KindText:
		label = "answer"
	case KindToolCall:
		label, text = it.Tool, it.Args
	}
	prefix := fmt.Sprintf("… %-*s ", labelWidth, label)
	text = oneLine(text)
	if width > 0 {
		room := width - ansi.StringWidth(prefix)
		if room > 1 && ansi.StringWidth(text) > room {
			text = ansi.TruncateLeft(text, ansi.StringWidth(text)-room+1, "…")
		}
	}
	return prefix + text
}

func labelled(mark, label, text string) string {
	return fmt.Sprintf("%s %-*s %s", mark, labelWidth, label, oneLine(text))
}

// firstLine is a text's first non-blank line, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// lastLines is a text's last n non-blank lines.
func lastLines(s string, n int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimRight(line, " \t\r"); strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func nonEmpty(parts []string) []string {
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// count spells a token count short: 759, 12.3k, 1.2M.
func count(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// Age spells a duration the way a watcher reads it: 4s, 2m10s, 1h04m.
func Age(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
