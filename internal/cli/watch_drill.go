package cli

// The drill-in views (epic hn6, wave 4 — tick c2u): what the keys open.
// Both are pure render-to-string functions of the model (or the feed) plus
// the pane's width and height — the same property the dashboard frame has,
// and the reason they are pinned headless at a fixed width.
//
//   - the tick view, on enter: one tick's own story — its pipeline, every
//     try with its tier, outcome and the run's own word for why, what the
//     report said and what it changed, the gate evidence on its heads, and
//     the findings it drafted;
//   - the feed view, on e: the whole event feed, newest at the bottom,
//     scrollable — every line through the same one-line form the tail and
//     the stream share.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// renderTickView renders one tick's drill-in: the tick id with its work's
// name and its pipeline cell, one line per try ("try N  <tier>  <outcome>
// <reason>", the run's own next step on the last), the report summary and
// its diff stats (or "report not read" when the report was not read), the
// gate evidence rows for this tick, and the findings it drafted. The footer
// names the way back. Height fits the pane the same way the dashboard's
// does: the footer is always kept, and what does not fit is counted, never
// silently dropped.
func renderTickView(m statusmodel.Model, tickID string, st watchStyles, width, height int) []string {
	content := renderTickContent(m, tickID, st, width)
	footer := st.dim("[esc] back")
	switch {
	case height == 1:
		return []string{footer}
	case height > 0 && len(content)+1 > height:
		avail := height - 2
		if avail < 0 {
			avail = 0
		}
		if avail > len(content) {
			avail = len(content)
		}
		out := append([]string{}, content[:avail]...)
		if rest := len(content) - avail; rest > 0 {
			out = append(out, st.dim(fmt.Sprintf("+%d more", rest)))
		}
		return append(out, footer)
	default:
		return append(content, footer)
	}
}

// renderTickContent is the tick view without its footer, so the height fit
// can count what it drops. Width is the pane's own: every line is cut to it
// — the same pass renderWatchFrame and renderFeedView give theirs — because
// a report summary, try reason or finding title written to the terminal
// unbounded wraps inside a real pane and the redraw draws over its own rows.
// Width 0 means unknown: everything, unbounded, for the caller that knows
// nothing.
func renderTickContent(m statusmodel.Model, tickID string, st watchStyles, width int) []string {
	lines := []string{}
	tick := watchTickOf(m, tickID)
	if tick == nil {
		// The cursor can sit on a tick the plan no longer carries — the run
		// absorbed or replanned between the key and this render.
		lines = append(lines, st.dim(tickID+" is not in the epic's plan any more"))
		return lines
	}
	name := tick.Gloss
	if name == "" {
		name = tick.Title
	}
	lines = append(lines, tickID+"  "+name)
	words := width == 0 || width >= watchWordsFrom
	lines = append(lines, dashPipeline(tick.Pipeline, st, words))

	for i, try := range tick.Tries {
		line := fmt.Sprintf("try %d  %-9s %-11s", try.Try, dashCell(tryTier(try), 9), try.Outcome)
		if try.Reason != nil && *try.Reason != "" {
			line += "  " + *try.Reason
		}
		if i == len(tick.Tries)-1 && try.NextStep != nil && *try.NextStep != "" {
			line += " — next: " + *try.NextStep
		}
		lines = append(lines, line)
	}

	switch {
	case tick.Report == nil:
		lines = append(lines, st.dim("report not read"))
	default:
		if tick.Report.Summary != nil && *tick.Report.Summary != "" {
			lines = append(lines, *tick.Report.Summary)
		}
		if tick.Report.Diff != nil {
			lines = append(lines, fmt.Sprintf("diff: %d files +%d −%d",
				tick.Report.Diff.Files, tick.Report.Diff.Insertions, tick.Report.Diff.Deletions))
		}
	}

	gates := watchTickGates(m, tickID)
	if len(gates) > 0 {
		lines = append(lines, st.dim("gates:"))
		for _, gate := range gates {
			head := ""
			if gate.Head != nil {
				head = *gate.Head
				if len(head) > 8 {
					head = head[:8]
				}
			}
			result := gate.Result
			switch result {
			case "pass":
				result = st.green(result)
			case "fail":
				result = st.red(result)
			}
			lines = append(lines, fmt.Sprintf("%s  %s  %s", dashCell(gate.Check, 16), result, head))
		}
	}
	if len(tick.Findings) > 0 {
		lines = append(lines, st.dim("findings:"))
		for _, finding := range tick.Findings {
			lines = append(lines, finding.Key+"  "+watchFindingGating(finding.Gating))
			lines = append(lines, "  "+finding.Title)
		}
	}
	// Width: no line carries spaces out to the edge it padded to (a trailing
	// space is a column nobody reads), and no line is wider than the pane
	// it was built for — the same two rules the dashboard frame's final pass
	// and the feed view run over theirs.
	for i, line := range lines {
		line = strings.TrimRight(line, " ")
		if width > 0 {
			line = ansi.Truncate(line, width, "")
		}
		lines[i] = line
	}
	return lines
}

// tryTier is one try's tier, or a blank nobody writes over: a try whose
// provenance was never recorded is a tier the view does not invent.
func tryTier(try statusmodel.Try) string {
	if try.Tier == nil {
		return ""
	}
	return *try.Tier
}

// watchFindingGating is a finding's gating verdict as the tri-state the
// model carries: gated, explicitly not, or a draft nobody triaged yet.
func watchFindingGating(gating *bool) string {
	switch {
	case gating == nil:
		return "untriaged"
	case *gating:
		return "gating"
	default:
		return "not gating"
	}
}

// watchTickGates is the gate evidence the run recorded for one tick, in the
// order the records carry it.
func watchTickGates(m statusmodel.Model, tickID string) []statusmodel.Gate {
	out := []statusmodel.Gate{}
	for _, gate := range m.Gates {
		if gate.TickID != nil && *gate.TickID == tickID {
			out = append(out, gate)
		}
	}
	return out
}

// renderFeedView renders the full feed as the drill-in opens it: newest at
// the bottom, scrolled up by `scroll` events, each line through the same
// one-line form the tail and the stream share — with the run's own reason
// and next step beside a refusal, since the model is at hand here. Height
// is the window: how many lines fit the pane, the last of them the feed's
// newest not yet scrolled away. Height 0 means unknown: everything, and the
// terminal scrolls.
func renderFeedView(events []runfeed.Event, tries *runfeed.Tries, model *statusmodel.Model, scroll, width, height int) []string {
	window := height
	if window <= 0 {
		// Unknown height: everything, and nothing scrolled away.
		window = len(events)
		scroll = 0
	}
	end := len(events) - scroll
	if end < 0 {
		end = 0
	}
	start := end - window
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, end-start)
	for _, event := range events[start:end] {
		lines = append(lines, watchEventLineWith(event, tries, model))
	}
	for i, line := range lines {
		line = strings.TrimRight(line, " ")
		if width > 0 {
			line = ansi.Truncate(line, width, "")
		}
		lines[i] = line
	}
	return lines
}

// watchEventLineWith is the one-line form for the surfaces that hold the
// model: a refusal's line names its reason — the run's own detail, cut to
// its first clause — and, when the tick's last try carries a next step, the
// run's own answer to "and then what". The old watchEventLine stays the
// stream's full-detail line, byte for byte.
func watchEventLineWith(event runfeed.Event, tries *runfeed.Tries, model *statusmodel.Model) string {
	t := nilTries(tries)
	detail := event.Detail
	isRefusal := false
	switch event.Stage {
	case reconcile.StageRejected, reconcile.StageGateFailed:
		isRefusal = true
		if clause := watchEventReason(event.Detail); clause != "" {
			detail = clause
		}
	}
	line := fmt.Sprintf("%s %-12s %s: %s", clockOf(event.At), watchEventWho(event, t), event.Stage, detail)
	if isRefusal && model != nil && event.TickID != nil && *event.TickID != "" {
		if tick := watchTickOf(*model, *event.TickID); tick != nil && len(tick.Tries) > 0 {
			if step := tick.Tries[len(tick.Tries)-1].NextStep; step != nil && *step != "" {
				line += " — next: " + *step
			}
		}
	}
	return line
}

// nilTries keeps the line form safe against a caller with no count yet: a
// try nobody counted is a line without the try number, not a panic.
func nilTries(tries *runfeed.Tries) *runfeed.Tries {
	if tries != nil {
		return tries
	}
	empty := runfeed.Tries{}
	return &empty
}

// watchEventReason is a refusal's reason as the line carries it: the
// detail's first clause — up to the first ":" or 120 characters — the
// glance's answer, with the full detail one drill away. A clause is cut at
// a word boundary rather than half-way through one.
func watchEventReason(detail string) string {
	if detail == "" {
		return ""
	}
	if i := strings.IndexByte(detail, ':'); i >= 0 && i <= 120 {
		return strings.TrimRight(detail[:i], " \t")
	}
	return cutWords(detail, 120)
}

// cutWords cuts s to at most `limit` characters, at a word boundary where
// one exists inside the limit — the same contract the status model's own
// detail cut keeps, in characters (runes), never half-way through one.
func cutWords(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	cut := string(runes[:limit])
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ")
}
