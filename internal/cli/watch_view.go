package cli

// The dashboard half of `ticfac` watch (epic ymf, tick ugm): the status
// model drawn as the redesigned dashboard the operator asked for — the one
// in docs/design/watch-redesign-2026-10.md — answering, in the order a
// person asks:
//
//  1. does anything need me? (the needs-you box first, with the exact
//     clearing command, when yes; the quiet answer beside the health line
//     when no)
//  2. is it healthy, and how far along? (one line: health, N of M done,
//     ETA)
//  3. what is happening right now? (the epic's phase track with a
//     you-are-here marker, then the ticks grouped by state — NOW / DONE /
//     UP NEXT / HELD — each with its plain-language status word and, for a
//     running worker, a live one-line excerpt of what it is doing)
//
// and then the run's latest sentences and the key hints. The words come
// from the model the whole epic shares: the status words and the tick
// groups from internal/statusmodel (tick lck), the worker excerpts from its
// activity fields (tick 93n), the latest sentences from the feed mapping
// (tick 47j), and the cost line from the metered-only rule (tick b13). The
// renderer derives nothing the model carries.
//
// Everything here is a pure function of the model plus the pane's width and
// height: nothing reads a file, spawns a process or measures a host, so the
// frames are pinned byte-for-byte against golden files at the sizes the
// tick names (watch_view_test.go), and the wiring — the redraw loop, the
// keep lines, the exit codes — stays in watch.go and watch_block_test.go.
// Width 0 means unknown: no column drops, no truncation, no height limit;
// the caller that knows nothing draws everything and lets the terminal
// scroll.

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// The frame's fixed geometry: the caps the row columns carry, the floors at
// which a column gives up its place entirely, and the widths the rules fill
// when the pane's own width is unknown.
const (
	dashStatusCols  = 44 // the status word plus its inline exception
	dashWhatCols    = 32 // the work's own name
	dashExcerptCols = 36 // a running worker's live excerpt, quoted
	dashTimeCols    = 7
	// The row's columns drop as the pane narrows, in the order the design
	// gives up detail: the worker's excerpt first (the status word already
	// says what is happening), then the work's own name, then the elapsed
	// time — the identity and the status word never go.
	dashExcerptFrom = 64 // below this the excerpt column joins the row no more
	dashWhatFrom    = 48 // below this the WHAT column joins the row no more
	dashTimeFrom    = 40 // below this the elapsed column joins the row no more
	// The threshold at which the drill-in view's pipeline cell spells its
	// stages out in words instead of one glyph per stage — the same words
	// the dashboard used to carry in a column of its own.
	watchWordsFrom = 100
	// The least a column keeps while it is shown at all: below these the
	// column drops rather than renders unreadable.
	dashWhatFloor    = 8
	dashExcerptFloor = 10
	// The width the rules fill when the pane does not say how wide it is.
	dashRuleWidth = 100
	// The tail's shape: two readable sentences under the latest rule.
	dashRecentEvents = 2
	// The key hints the tail seats at the pane's right (the design's own).
	dashKeyHints = "[enter] details  [e] all events  [q] quit"
)

// watchStyles is the frame's small style set, injected so a test reads the
// words with the identity set and the colours with the ANSI one — the frame
// must be as assertable as it is readable. Six styles cover the frame: dim
// for what is far from now or quiet by design (pending track steps, the
// quiet needs-you answer, secondary text), amber for what is in flight or
// wants a look, red for what is wrong or held for a person (the needs-you
// box the most prominent thing on screen when it stands), green for what
// passed or finished healthy, cyan for the identities and key hints a
// person scans for, bold for what leads (the section headers). Four hues,
// no background fills: colour carries meaning, never decoration (tick 5ba).
type watchStyles struct {
	dim   func(string) string
	amber func(string) string
	red   func(string) string
	green func(string) string
	cyan  func(string) string
	bold  func(string) string
}

// identityWatchStyles is the identity style set: every string comes back
// exactly as it was, the frame's words with no escape codes — the set a
// pipe, a log, a NO_COLOR terminal or a dumb one gets.
func identityWatchStyles() watchStyles {
	id := func(s string) string { return s }
	return watchStyles{dim: id, amber: id, red: id, green: id, cyan: id, bold: id}
}

// ansiWatchStyles is the terminal's style set: plain ANSI 16-colour codes,
// no colorprofile detection, because the live view is only ever drawn on a
// TTY (the pipe path streams plain lines instead), and these are the codes
// every terminal a watch runs on answers — the names a terminal theme
// maps to its own hues.
func ansiWatchStyles() watchStyles {
	wrap := func(code string) func(string) string {
		return func(s string) string {
			if s == "" {
				return ""
			}
			return "\x1b[" + code + "m" + s + "\x1b[0m"
		}
	}
	return watchStyles{
		dim:   wrap("2"),
		amber: wrap("33"),
		red:   wrap("31"),
		green: wrap("32"),
		cyan:  wrap("36"),
		bold:  wrap("1"),
	}
}

// watchStylesForTerminal is the style set the live view draws with: the
// ANSI set, unless the environment said the terminal shows no colour —
// NO_COLOR set to anything (the convention: any non-empty value), or a
// TERM=dumb terminal — in which case the identity set, because a terminal
// that cannot show a hue still shows every word, in the same layout:
// colour never changes widths (tick 5ba).
func watchStylesForTerminal() watchStyles {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return identityWatchStyles()
	}
	return ansiWatchStyles()
}

// renderWatchFrame renders one frame of the dashboard: the identity line
// and its rule, the needs-you boxes when anything needs a person, the
// health line, the phase track with its you-are-here marker, the ticks
// grouped by state, the cost line, and the latest sentences under their
// rule with the key hints. `selected` is the tick id whose row renders with
// a "▸" marker — the drill-in cursor the keys move; "" for none.
func renderWatchFrame(m statusmodel.Model, st watchStyles, width, height int, selected string) []string {
	base := dashboardHeadline(m, st, width)
	boxes := dashboardNeedsBoxes(m, st, width)
	foot := dashboardTail(m, st, width)
	// The head in two variants: full (identity, the needs-you boxes, the
	// health line, the phase track) and minimal (identity, the boxes, the
	// health line) — the track is the first of the head's own lines the
	// height fit gives up, because a pane crowded with holds still owes
	// its rows a seat before it owes the map one.
	headFull := make([]string, 0, len(base)+8)
	headFull = append(headFull, base[0], frameRule(width))
	for _, box := range boxes {
		headFull = append(headFull, box...)
	}
	headFull = append(headFull, base[1:]...)
	headMin := make([]string, 0, len(base))
	headMin = append(headMin, base[0], frameRule(width))
	for _, box := range boxes {
		headMin = append(headMin, box...)
	}
	headMin = append(headMin, base[1:1+dashboardHealthLineCount(m, width)]...)
	middle := func(spaced bool, level, maxRows int) []string {
		mid, _ := dashboardMiddle(m, st, width, selected, spaced, level, maxRows)
		return mid
	}
	// The blank line between the phase track and the first group (tick
	// h3s): one, and only when the head ends with the track — a model
	// without a track ends its head with the blank the headline already
	// puts there, and a second would read as an empty band.
	spacer := func(head []string, spaced bool, mid []string) []string {
		if !spaced || len(mid) == 0 || len(head) == 0 || head[len(head)-1] == "" {
			return mid
		}
		return append([]string{""}, mid...)
	}
	assemble := func(head []string, spaced bool, level, maxRows int) []string {
		out := append([]string{}, head...)
		if level >= 0 {
			out = append(out, spacer(head, spaced, middle(spaced, level, maxRows))...)
		}
		return append(out, foot...)
	}

	// The height fit, in the order the frame gives things up: the UP NEXT
	// group collapses first — its "then:" line, then the group itself
	// (tick h3s: the queue is what a person glances at, not what they
	// watch, so it is the cheapest thing to lose, and losing it is what
	// lets the five scenarios keep their spacing and their rows at 80x24)
	// — then the spacing between the groups yields, then the DONE rows
	// fold into their group's header, then rows trim from the bottom with
	// one "+N more" at the fold (the drill-in cursor's row never among the
	// trimmed), then the phase track drops so the rows keep their seat —
	// and only then the middle dropped entirely. A pane shorter than even
	// the minimal head keeps as much of the head as fits, whole needs-you
	// boxes only. Fitting never reorders what it keeps.
	if height <= 0 {
		return finishFrame(assemble(headFull, true, 0, -1), width)
	}
	_, totalRows := dashboardMiddle(m, st, width, selected, false, 3, 0)
	trim := func(head []string) ([]string, bool) {
		for k := totalRows - 1; k >= 0; k-- {
			if frame := assemble(head, false, 3, k); len(frame) <= height {
				return frame, true
			}
		}
		return nil, false
	}
	for _, c := range []struct {
		head   []string
		spaced bool
		level  int
	}{
		{headFull, true, 0}, {headFull, true, 1}, {headFull, true, 2},
		{headFull, false, 0}, {headFull, false, 1}, {headFull, false, 2}, {headFull, false, 3},
		{headMin, false, 0}, {headMin, false, 1}, {headMin, false, 2}, {headMin, false, 3},
		{headFull, false, -1}, {headMin, false, -1},
	} {
		if frame := assemble(c.head, c.spaced, c.level, -1); len(frame) <= height {
			return finishFrame(frame, width)
		}
		if c.level == 3 {
			if frame, ok := trim(c.head); ok {
				return finishFrame(frame, width)
			}
		}
	}
	return finishFrame(fitFrameHead(base, boxes, width, height), width)
}

// finishFrame is the frame's final pass: no line carries spaces out to the
// edge it padded to (a trailing space is a column nobody reads), and no
// line is wider than the pane it was built for — width is honest even after
// styling, so a frame never wraps into the rows below it.
func finishFrame(frame []string, width int) []string {
	for i, line := range frame {
		line = strings.TrimRight(line, " ")
		if width > 0 {
			line = ansi.Truncate(line, width, "")
		}
		frame[i] = line
	}
	return frame
}

// frameRule is the rule under the identity line: the frame's own divider,
// run to the pane's edge.
func frameRule(width int) string {
	w := width
	if w <= 0 {
		w = dashRuleWidth
	}
	return strings.Repeat("━", w)
}

// dashboardHeadline is the frame's first questions: the run's identity with
// its liveness, the needs-you answer beside the health summary, and the
// phase track with its you-are-here marker. The bare `ticfac` overview
// reuses exactly these lines (everything from the second on), so their
// assembly lives here, named, and not inside the watch alone. The needs-you
// boxes are NOT part of the headline: the overview lists the holds' own
// lines separately (dashboardAttentionLines), and the watch inserts the
// boxes between the identity line and the health line itself.
func dashboardHeadline(m statusmodel.Model, st watchStyles, width int) []string {
	out := make([]string, 0, 6)
	out = append(out, dashboardIdentityLine(m, st, width))
	out = append(out, dashboardHealthLines(m, st, width)...)
	out = append(out, "")
	out = append(out, dashboardTrack(m, st, width)...)
	return out
}

// dashboardIdentityLine is the frame's first line: the epic's own title
// with its id, and, at the pane's right, the provenance — the config the
// run routes under, where it lives, and whether it is going, with the run's
// own elapsed time when the model measured one.
func dashboardIdentityLine(m statusmodel.Model, st watchStyles, width int) string {
	identity := m.EpicID
	if m.EpicTitle != nil && *m.EpicTitle != "" {
		identity = *m.EpicTitle + " (" + m.EpicID + ")"
	}
	parts := make([]string, 0, 3)
	if m.RunConfig != nil && *m.RunConfig != "" {
		parts = append(parts, *m.RunConfig)
	}
	parts = append(parts, m.Host)
	state := dashboardStateWord(m)
	if elapsed, ok := watchRunElapsed(m); ok {
		state += " " + humanDuration(elapsed)
	}
	parts = append(parts, state)
	return dashSeat(identity, st.dim(strings.Join(parts, " · ")), width)
}

// dashboardStateWord is the header's one word for whether the run is going:
// running while it is, and the run's own terminal answer when it is not —
// failed, stopped, done — because that is the question the reader is
// actually asking (hn6, tick gmo). A run whose records state no ending and
// no liveness is "not alive".
func dashboardStateWord(m statusmodel.Model) string {
	if m.Liveness.Alive {
		return "running"
	}
	switch m.Lifecycle.Phase {
	case statusmodel.PhaseFailed:
		return "failed"
	case statusmodel.PhaseCancelled:
		return "stopped"
	case statusmodel.PhaseDone, statusmodel.PhaseMerge:
		return "done"
	}
	return "not alive"
}

// dashboardHealthLineCount is how many lines the health answer takes at the
// given width — one when it seats beside the quiet answer (or when a hold
// owns the quiet), two when the pane splits them.
func dashboardHealthLineCount(m statusmodel.Model, width int) int {
	if dashboardNeedsSomebody(m) {
		return 1
	}
	if width <= 0 {
		return 1
	}
	// The widths come from the identity set — styling never changes them,
	// and the count must not depend on the terminal's palette.
	if width-ansi.StringWidth("Needs you: nothing")-ansi.StringWidth(dashboardHealthSummary(m, identityWatchStyles())) >= 1 {
		return 1
	}
	return 2
}

// dashboardHealthLines is the second question — is it healthy, and how far
// along — as one line: the health verdict, the ticks closed over the total,
// and the approximate time left, each only where the model measured it.
// Nothing needs a person and the quiet answer is seated at the line's left;
// a hold stands in its own box above (the first question is the first
// thing), so the health line here carries the health alone.
func dashboardHealthLines(m statusmodel.Model, st watchStyles, width int) []string {
	health := dashboardHealthSummary(m, st)
	if dashboardNeedsSomebody(m) {
		return []string{health}
	}
	quiet := st.dim("Needs you: nothing")
	if width <= 0 {
		return []string{quiet + "    " + health}
	}
	if gap := width - ansi.StringWidth(quiet) - ansi.StringWidth(health); gap >= 1 {
		return []string{quiet + strings.Repeat(" ", gap) + health}
	}
	// The pane cannot seat both, and the needs-you answer is an answer, not
	// a decoration: it keeps a line of its own — first.
	return []string{quiet, health}
}

// dashboardHealthSummary is the health line: the verdict, the progress
// count, the ETA — the words the model carries, joined in the design's
// order.
func dashboardHealthSummary(m statusmodel.Model, st watchStyles) string {
	parts := make([]string, 0, 3)
	if v := dashVerdict(m, st); v != "" {
		parts = append(parts, v)
	}
	if m.Progress.Ticks != nil && m.Progress.Ticks.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d done", m.Progress.Ticks.Closed, m.Progress.Ticks.Total))
	}
	if m.Remaining != nil {
		parts = append(parts, "~"+humanDuration(m.Remaining.ApproximateSeconds)+" left")
	}
	return strings.Join(parts, " · ")
}

// dashboardNeedsBoxes is the first question's answer when it is yes: one
// box per hold, each naming what is held and the one command that clears
// it — the most prominent thing on screen while it stands. The box's lines
// are wrapped inside the box's width, never truncated: a clearing command a
// person cannot read whole is no command (tick 9um).
func dashboardNeedsBoxes(m statusmodel.Model, st watchStyles, width int) [][]string {
	boxes := [][]string{}
	for _, a := range m.Attention {
		if !a.NeedsPerson {
			continue
		}
		boxes = append(boxes, dashboardNeedsBox(a, st, width))
	}
	return boxes
}

// dashboardNeedsBox draws one hold's box: the what as the box's first line
// ("Needs you: …"), the clearing command beneath it ("clear with: …"),
// wrapped to the pane's inner width where the pane says one, and the whole
// box red — with the announcement bold, the most prominent thing on screen.
func dashboardNeedsBox(a statusmodel.Attention, st watchStyles, width int) []string {
	inner := -1 // unknown width: no wrapping, one line per thought
	if width > 0 {
		inner = width - 4 // "│ " and " │"
		if inner < 1 {
			inner = 1
		}
	}
	lines := dashWrapWords(a.What, "Needs you: ", inner)
	if a.UnblockCommand != nil && *a.UnblockCommand != "" {
		lines = append(lines, dashWrapWords(*a.UnblockCommand, "clear with: ", inner)...)
	}
	// The box is as wide as its widest line, to the pane's own width at
	// most — the inner wrap above already guaranteed the fit.
	boxW := 6 // "┌──┐" at the least: a box a terminal can draw
	for _, line := range lines {
		if w := 4 + ansi.StringWidth(line); w > boxW {
			boxW = w
		}
	}
	out := make([]string, 0, len(lines)+2)
	out = append(out, st.red("┌"+strings.Repeat("─", boxW-2)+"┐"))
	for i, line := range lines {
		if i == 0 {
			// The announcement leads the box, bold: the one line a person
			// scanning the screen reads first.
			line = st.bold(line)
		}
		out = append(out, st.red("│ "+dashPad(line, boxW-4)+" │"))
	}
	out = append(out, st.red("└"+strings.Repeat("─", boxW-2)+"┘"))
	return out
}

// dashboardTrack is the epic's phase track — the one epic-level line the
// design draws, with the you-are-here marker beneath the step the epic is
// in. The track and the marker index are the model's own (tick lck); the
// renderer lays them out and never re-derives them. Done steps are dim,
// the current step bold: the marker carries the position, the words carry
// the names, and no symbol needs a legend.
func dashboardTrack(m statusmodel.Model, st watchStyles, width int) []string {
	track := m.Lifecycle.Track
	if len(track) == 0 {
		return nil
	}
	segments := make([]string, 0, len(track))
	columns := make([]int, 0, len(track))
	column := 0
	for i, step := range track {
		columns = append(columns, column)
		name := dashTrackName(step.Label)
		if step.State == statusmodel.PhaseStateActive {
			segments = append(segments, st.bold(name))
		} else {
			// Done and pending alike are dim: the marker says where the
			// epic is, and a step's own state word would need a legend.
			segments = append(segments, st.dim(name))
		}
		column += ansi.StringWidth(name)
		if i < len(track)-1 {
			column += ansi.StringWidth(dashTrackSeparator)
		}
	}
	here := m.Lifecycle.Here
	if here < 0 {
		here = 0
	}
	if here >= len(track) {
		here = len(track) - 1
	}
	marker := st.cyan("▲ here")
	if m.Lifecycle.Wave != nil {
		marker += st.dim(fmt.Sprintf(" (wave %d of %d)", m.Lifecycle.Wave.Active, m.Lifecycle.Wave.Total))
	}
	// The track is the widest fixed line the frame carries, and a pane
	// narrower than it truncates the line — but the marker slides left to
	// stay visible, still pointing at the track's right end.
	column = columns[here]
	trackLine := strings.Join(segments, dashTrackSeparator)
	if width > 0 {
		trackLine = ansi.Truncate(trackLine, width, "…")
		if column+ansi.StringWidth(marker) > width && column > 0 {
			column = max(0, width-ansi.StringWidth(marker))
		}
	}
	pad := ""
	if column > 0 {
		pad = strings.Repeat(" ", column)
	}
	return []string{trackLine, pad + marker}
}

// dashTrackSeparator is the line between the track's steps, in the design's
// own spacing.
const dashTrackSeparator = " ──── "

// dashTrackName is a track step's label as a person reads it: the model's
// vocabulary is the contract (and stays lowercase on the wire), the frame
// capitalises the first letter.
func dashTrackName(label string) string {
	if label == "" {
		return label
	}
	r := []rune(label)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}

// dashboardNeedsSomebody says whether anything in the model's attention list
// needs a person — the fact that decides between the quiet answer and the
// boxes above the health line.
func dashboardNeedsSomebody(m statusmodel.Model) bool {
	for _, a := range m.Attention {
		if a.NeedsPerson {
			return true
		}
	}
	return false
}

// dashboardAttentionLines is the holds' own lines, one per hold, the shape
// the bare `ticfac` overview lists beneath the headline — the same holds
// the watch draws as boxes. Nothing needs a person and there is no line at
// all.
func dashboardAttentionLines(m statusmodel.Model, st watchStyles, width int) []string {
	lines := []string{}
	for _, a := range m.Attention {
		if !a.NeedsPerson {
			continue
		}
		lines = append(lines, dashboardHoldLines(a, st, width)...)
	}
	return lines
}

// dashHoldIndent is the hanging indent the continuation lines of a wrapped
// hold carry: small enough to leave a narrow pane room, big enough to read
// as one thought continued.
const dashHoldIndent = "  "

// dashboardHoldLines is one hold's lines: the announcement with its clearing
// command as the one line wherever the pane seats it — and where it does
// not, wrapped under the announcement instead of truncated (tick 9um): the
// what flows on with a hanging indent, and the command that clears the hold
// keeps every one of its words on lines of its own, because a clearing
// command a person cannot read whole is no command. The whole line is red
// and bold — needs-you is the most prominent thing on screen when
// non-empty (tick 5ba), never one accent among others.
func dashboardHoldLines(a statusmodel.Attention, st watchStyles, width int) []string {
	line := "needs you: " + a.What
	if a.UnblockCommand != nil && *a.UnblockCommand != "" {
		line += " — " + *a.UnblockCommand
	}
	if width <= 0 || ansi.StringWidth(line) <= width {
		return []string{st.red(st.bold(line))}
	}
	wrapped := dashWrapWords(a.What, "needs you: ", width)
	if a.UnblockCommand != nil && *a.UnblockCommand != "" {
		wrapped = append(wrapped, dashWrapWords(*a.UnblockCommand, dashHoldIndent, width)...)
	}
	lines := make([]string, 0, len(wrapped))
	for _, l := range wrapped {
		lines = append(lines, st.red(st.bold(l)))
	}
	return lines
}

// dashWrapWords flows text into lines that fit width cells: the first line
// carries first, every later one the hanging indent, breaking on spaces and
// keeping every word whole — a word with no room left on a line starts the
// next one, and a word wider than a whole line is cut where it stands
// rather than allowed to overflow, so the pane's width holds and no
// character is lost. Width 0 or less means unknown: one line, everything.
func dashWrapWords(text, first string, width int) []string {
	if width <= 0 {
		return []string{first + text}
	}
	out := []string{}
	cur := first
	room := width - ansi.StringWidth(first)
	started := false // the line carries at least one word of text
	newline := func() {
		out = append(out, cur)
		cur = dashHoldIndent
		room = width - len(dashHoldIndent)
		started = false
	}
	for _, w := range strings.Fields(text) {
		need := ansi.StringWidth(w)
		if started {
			need++ // the space between words
		}
		if need <= room {
			if started {
				cur += " "
			}
			cur += w
			room -= need
			started = true
			continue
		}
		newline()
		for ansi.StringWidth(w) > room && room >= 1 {
			chunk := ansi.Truncate(w, room, "")
			if chunk == "" {
				break // a symbol wider than the room rides whole
			}
			out = append(out, cur+chunk)
			w = ansi.TruncateLeft(w, room, "")
		}
		if w != "" {
			cur += w
			room -= ansi.StringWidth(w)
			started = true
		}
	}
	if !started && cur != first {
		return out // nothing left to carry on the indented line
	}
	return append(out, cur)
}

// dashSection is one group of the middle: the group's name, the tick ids it
// holds in the model's own order, and whether the header counts them.
type dashSection struct {
	name  string
	ids   []string
	count bool
}

// dashboardMiddle is everything between the headline and the latest rule:
// the ticks grouped by state (NOW / DONE / UP NEXT / HELD), the census note
// when one cannot be taken, and the cost line. When spaced, one blank line
// separates each adjacent pair of groups (tick h3s) — never before the
// first rendered group, and never around the census note or the cost line,
// which are not groups. The level folds detail as the pane's height
// demands — 0 the whole groups, 1 the UP NEXT line without its "then:"
// line, 2 the UP NEXT group gone entirely, 3 the DONE rows folded into
// their header — and maxRows caps the NOW and HELD rows the frame keeps,
// counting what it drops in one "+N more" line at the fold. The rows are
// the model's own groups (tick lck) rendered in the groups' own order; the
// renderer re-derives nothing.
func dashboardMiddle(m statusmodel.Model, st watchStyles, width int, selected string, spaced bool, level, maxRows int) ([]string, int) {
	if m.Waves == nil {
		return []string{st.dim("the epic's shape could not be read (the tracker did not answer)")}, 0
	}
	// The tick each group id names. A model without groups (an older one,
	// before tick lck's field) renders its rows in plan order under no
	// header; the honest models always carry the groups the contract names.
	ticks := map[string]*statusmodel.Tick{}
	order := []string{}
	for wi := range *m.Waves {
		wave := &(*m.Waves)[wi]
		for ti := range wave.Ticks {
			t := &wave.Ticks[ti]
			ticks[t.TickID] = t
			order = append(order, t.TickID)
		}
	}
	sections := []dashSection{
		{name: "NOW"},
		{name: "DONE", count: true},
		{name: "UP NEXT", count: true},
		{name: "HELD", count: true},
	}
	if g := m.Groups; g != nil {
		sections[0].ids, sections[1].ids, sections[2].ids, sections[3].ids = g.Now, g.Done, g.UpNext, g.Held
	} else {
		sections = []dashSection{{ids: order}}
	}

	// The rows' sources, and the row table's ids in group order (the UP
	// NEXT group is a collapsed line, not a table).
	sources := map[string]dashRowSource{}
	rowIDs := []string{}
	var workers []statusmodel.Worker
	if m.Workers != nil {
		workers = *m.Workers
	}
	for _, section := range sections {
		for _, id := range section.ids {
			if t, ok := ticks[id]; ok {
				if _, seen := sources[id]; !seen {
					sources[id] = dashboardRowSource(t, workers, st)
				}
				if section.name != "UP NEXT" {
					rowIDs = append(rowIDs, id)
				}
			}
		}
	}
	cols := dashRowColumns(rowIDs, sources, width)

	// The drill-in cursor's row is never trimmed away: a person who moved
	// it must see where it sits, whatever the pane's height. The fold's cap
	// extends to include it, so the rows shown are always the cap's prefix
	// of the group order.
	capRows := maxRows
	if maxRows >= 0 && selected != "" {
		for i, id := range rowIDs {
			if id == selected {
				if i >= capRows {
					capRows = i + 1
				}
				break
			}
		}
	}

	out := make([]string, 0, 12)
	shown, total := 0, 0
	lastRowsEnd := -1
	// gap is the one blank line that separates two adjacent groups (tick
	// h3s) — only where a group already stands, so the first group on the
	// frame meets the phase track's own blank instead of a doubled one.
	gap := func() {
		if spaced && len(out) > 0 {
			out = append(out, "")
		}
	}
	for _, section := range sections {
		if section.name == "UP NEXT" {
			if len(section.ids) > 0 && level < 2 {
				gap()
				out = append(out, dashboardUpNext(section, sources, m, st, width, level)...)
			}
			continue
		}
		if len(section.ids) == 0 {
			continue
		}
		if section.name == "DONE" && level >= 3 {
			// Folded: the header alone counts what the rows would say.
			gap()
			out = append(out, st.bold(dashSectionHeader(section, sources)))
			continue
		}
		rows := make([]string, 0, len(section.ids))
		for _, id := range section.ids {
			src, ok := sources[id]
			if !ok {
				continue
			}
			total++
			if capRows >= 0 && shown >= capRows {
				continue // the fold counts this one
			}
			rows = append(rows, dashboardRowLine(src, cols, selected == id, st))
			shown++
		}
		if len(rows) == 0 {
			continue // a section whose rows the fold dropped: no header alone
		}
		gap()
		out = append(out, st.bold(dashSectionHeader(section, sources)))
		out = append(out, rows...)
		lastRowsEnd = len(out)
	}
	if capRows >= 0 && total > shown {
		// The fold counts what it dropped, standing where the last shown
		// row stands — never after the census note or the cost line.
		more := st.dim(fmt.Sprintf("+%d more", total-shown))
		if lastRowsEnd >= 0 {
			out = append(out[:lastRowsEnd], append([]string{more}, out[lastRowsEnd:]...)...)
		} else {
			out = append(out, more)
		}
	}
	if m.Workers == nil {
		out = append(out, st.dim("workers: no census could be taken"))
	}
	if cost := dashCost(m, st); cost != "" {
		out = append(out, cost)
	}
	return out, total
}

// dashSectionHeader is one group's header line: the group's name, and, for
// the groups the design counts, how many ticks it holds. The DONE count is
// the same number the health line states beside it — the epic's real ticks,
// which the progress counts count (the contract pins a duplicate out of
// total and closed) — so a duplicate's row stands in DONE, dimmed and naming
// the tick its work belongs to, without turning "4 of 4 done" into
// "DONE (5)": one number for one question, said the same way twice on the
// frame (tick t0y, folding h4u). The other counted groups keep counting
// their rows: their headers describe the group they sit over, and a
// duplicate parked outside DONE is still a row the group holds.
func dashSectionHeader(section dashSection, sources map[string]dashRowSource) string {
	if section.name == "" {
		return "" // the no-groups fallback carries no header
	}
	if section.count {
		seen := map[string]bool{}
		n := 0
		for _, id := range section.ids {
			src, ok := sources[id]
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			if section.name == "DONE" && src.duplicate {
				continue // not separate work, and the DONE count counts work
			}
			n++
		}
		return fmt.Sprintf("%s (%d)", section.name, n)
	}
	return section.name
}

// dashboardUpNext is the UP NEXT group's collapsed form: one line — the
// header with the next ticks' names seated after it, folded with an ellipsis
// where the pane ends — and, when the epic's track still has steps ahead of
// the marker, a dim "then:" line naming them, indented under the items. The
// group is always collapsed: the queue is what a person glances at, not
// what they watch. Level 1 drops the "then:" line — the first thing the
// height fit gives up (tick h3s); level 2 drops the group entirely, which
// the caller handles and never reaches here.
func dashboardUpNext(section dashSection, sources map[string]dashRowSource, m statusmodel.Model, st watchStyles, width, level int) []string {
	header := dashSectionHeader(section, sources)
	out := []string{st.bold(header)}
	items := make([]string, 0, len(section.ids))
	for _, id := range section.ids {
		src, ok := sources[id]
		if !ok {
			continue
		}
		if src.what == "" {
			items = append(items, ansi.Strip(src.id))
		} else {
			items = append(items, ansi.Strip(src.id)+" "+src.what)
		}
	}
	avail := -1
	if width > 0 {
		avail = width - ansi.StringWidth(header) - 3
	}
	if text := dashFoldItems(items, avail); text != "" {
		out[0] += "   " + text
	}
	if level >= 1 {
		// The fold keeps the header and its items but drops the "then:"
		// line (tick h3s): the steps ahead are the track's own news.
		return out
	}
	if then := dashThenLine(m, st); then != "" {
		out = append(out, strings.Repeat(" ", ansi.StringWidth(header)+3)+then)
	}
	return out
}

// dashFoldItems joins the UP NEXT items with " · ", keeping as many as the
// width seats and folding the rest under one ellipsis. Unknown width keeps
// every item, and a list the pane seats whole needs no fold.
func dashFoldItems(items []string, avail int) string {
	if len(items) == 0 {
		return ""
	}
	if avail <= 0 {
		return strings.Join(items, " · ")
	}
	text := ""
	for i, item := range items {
		sep := ""
		if i > 0 {
			sep = " · "
		}
		next := text + sep + item
		if ansi.StringWidth(next) <= avail {
			text = next
			continue
		}
		// This item does not fit. Nothing shown yet and it is the only
		// item: show its start. Otherwise the fold stands for the rest.
		if i == len(items)-1 && text != "" {
			return text + " …"
		}
		if text == "" {
			return dashCell(item, max(0, avail-2)) + " …"
		}
		return text + " …"
	}
	return text
}

// dashThenLine is the "then:" line under the UP NEXT items: the track steps
// still ahead of the marker, in the track's own order and words. A run with
// nothing ahead of it — a merged epic — says nothing.
func dashThenLine(m statusmodel.Model, st watchStyles) string {
	here := m.Lifecycle.Here
	if here < 0 {
		here = 0
	}
	ahead := []string{}
	for i := here + 1; i < len(m.Lifecycle.Track); i++ {
		if m.Lifecycle.Track[i].State != statusmodel.PhaseStateDone {
			ahead = append(ahead, dashTrackName(m.Lifecycle.Track[i].Label))
		}
	}
	if len(ahead) == 0 {
		return ""
	}
	return st.dim("then: " + strings.Join(ahead, " → "))
}

// dashRowSource is one tick's raw cells before the table's columns are
// sized: the id as the row prints it (a "+" when the run absorbed it), the
// work's own name, the status word with its inline exception, the tick's
// elapsed time, and — for a running worker — the live one-line excerpt of
// what it is doing now (tick 93n), or the run's own word for the stage it
// is driving when no worker stands (a gate is the run's process, not a
// worker's). A duplicate is not work the epic owes: its name says the tick
// it is a copy of, and its row renders dimmed whole.
type dashRowSource struct {
	id        string
	what      string
	status    string
	elapsed   string
	excerpt   string
	duplicate bool
}

// dashboardRowSource builds one tick's raw cells.
func dashboardRowSource(t *statusmodel.Tick, workers []statusmodel.Worker, st watchStyles) dashRowSource {
	id := t.TickID
	if t.Absorbed {
		id = "+" + id
	}
	what := t.Gloss
	if what == "" {
		what = t.Title
	}
	if t.DuplicateOf != nil && *t.DuplicateOf != "" {
		what = "duplicate of " + *t.DuplicateOf
	}
	// The word and its exception are one cell, but the colour reads the
	// word alone — the exception rides the word's hue, never its own. The
	// note travels in its compact spelling: the row is the glance surface,
	// and 'attempt 2 · escalated' says what the model's own note says in the
	// cells a row owes it (tick az1).
	word := t.Status
	if t.Exception != nil && *t.Exception != "" {
		word += " (" + statusmodel.CompactException(*t.Exception) + ")"
	}
	elapsed := ""
	if t.DurationSeconds != nil {
		elapsed = humanDuration(*t.DurationSeconds)
	}
	return dashRowSource{
		id:        st.cyan(id),
		what:      what,
		status:    dashboardStatusColor(t.Status, st)(word),
		elapsed:   elapsed,
		excerpt:   dashExcerptCell(t, workers, st),
		duplicate: t.DuplicateOf != nil,
	}
}

// dashboardStatusColor is the row's status word's colour, read from the
// word alone — the exception rides the word's hue: red for what is wrong or
// held for a person, green for what finished, amber for what is in flight,
// dim for what waits its turn — the palette's own semantics (tick 5ba), one
// hue per cell.
func dashboardStatusColor(status string, st watchStyles) func(string) string {
	switch {
	case strings.HasPrefix(status, statusmodel.WordHeldPrefix),
		strings.HasPrefix(status, statusmodel.WordFailedPrefix),
		status == statusmodel.WordFailed:
		return st.red
	case status == statusmodel.WordMerged, status == statusmodel.WordDone:
		return st.green
	case status == statusmodel.WordClaimed, status == statusmodel.WordWritingCode,
		status == statusmodel.WordReviewing, status == statusmodel.WordClosingOut,
		status == statusmodel.WordTesting, status == statusmodel.WordMerging,
		status == statusmodel.WordWaitingForCI:
		return st.amber
	case status == statusmodel.WordUpNext, strings.HasPrefix(status, statusmodel.WordWaitingPrefix):
		return st.dim
	}
	return func(s string) string { return s }
}

// dashboardRowLine renders one row: the drill-in cursor in the indent's
// first column, the id, and the columns the width kept, padded so the rows
// align. A duplicate renders dimmed whole: the row keeps its place and its
// history, but nothing about it is the frontier's business.
func dashboardRowLine(src dashRowSource, cols dashRowCols, selected bool, st watchStyles) string {
	mark := "  "
	if selected {
		mark = "▸ "
	}
	var row strings.Builder
	row.WriteString(mark)
	row.WriteString(dashPad(src.id, cols.idW))
	if cols.showWhat {
		row.WriteString("  " + dashPad(dashCell(src.what, cols.whatW), cols.whatW))
	}
	row.WriteString("  " + dashPad(dashCell(src.status, cols.statusW), cols.statusW))
	if cols.showTime {
		row.WriteString("  " + dashPad(dashCell(src.elapsed, cols.timeW), cols.timeW))
	}
	if cols.showEx {
		row.WriteString("  " + dashCell(src.excerpt, cols.excerptW))
	}
	line := row.String()
	if src.duplicate {
		// Dimmed whole: the accents it carried are stripped before the dim
		// covers it, so the dim is one span over the whole row and never an
		// accent's reset away from the rest of it.
		line = st.dim(ansi.Strip(line))
	}
	return line
}

// dashRowCols is the row table's column widths, sized once for the frame
// from the rows it carries.
type dashRowCols struct {
	idW      int
	whatW    int
	statusW  int
	timeW    int
	excerptW int
	showWhat bool
	showTime bool
	showEx   bool
}

// dashRowColumns sizes the row table's columns to the widest content each
// carries — capped at the layout's maxima — and decides which columns the
// pane's width keeps: the excerpt first to go (the status word already says
// what is happening), then the work's own name, then the elapsed time.
//
// What is left after the fixed columns is the NAME's before it is the
// excerpt's (tick az1): the work's own title takes it to its natural width,
// and the excerpt claims only what remains — so a pane that cannot seat
// both keeps the thing a person scans a row for and gives up the worker's
// live action. The status column's own claim is the note in its shortened
// spelling (statusmodel.CompactException), the excerpt's is what the title
// leaves it, and neither column renders below its floor: below it the
// column drops, its separator returning to the title. Width 0 means
// unknown: every column with content, no dropping.
func dashRowColumns(rowIDs []string, sources map[string]dashRowSource, width int) dashRowCols {
	cols := dashRowCols{idW: 3}
	statusNat, whatNat, timeNat, exNat := 0, 0, 0, 0
	for _, id := range rowIDs {
		src, ok := sources[id]
		if !ok {
			continue
		}
		cols.idW = max(cols.idW, ansi.StringWidth(src.id))
		statusNat = max(statusNat, ansi.StringWidth(src.status))
		whatNat = max(whatNat, ansi.StringWidth(src.what))
		timeNat = max(timeNat, ansi.StringWidth(src.elapsed))
		exNat = max(exNat, ansi.StringWidth(src.excerpt))
	}
	cols.statusW = min(statusNat, dashStatusCols)
	if width <= 0 {
		cols.whatW = min(whatNat, dashWhatCols)
		cols.timeW = min(timeNat, dashTimeCols)
		cols.excerptW = min(exNat, dashExcerptCols)
		cols.showWhat, cols.showTime, cols.showEx = whatNat > 0, timeNat > 0, exNat > 0
		return cols
	}
	cols.timeW = min(timeNat, dashTimeCols)
	// The columns join the row only where the width both crosses their
	// threshold and can seat them beside what is already there — a column
	// that would be truncated by the pane's edge is not a column, it is a
	// lie. The elapsed time seats before the name and the excerpt; the name
	// then takes what is left before the excerpt claims any of it.
	used := 2 + cols.idW + 2 + cols.statusW
	cols.showTime = width >= dashTimeFrom && timeNat > 0 && used+2+cols.timeW <= width
	if cols.showTime {
		used += 2 + cols.timeW
	}
	cols.showWhat = width >= dashWhatFrom && whatNat > 0
	cols.showEx = width >= dashExcerptFrom && exNat > 0
	room := width - used
	if cols.showWhat {
		room -= 2
	}
	if cols.showEx {
		room -= 2
	}
	switch {
	case cols.showWhat && cols.showEx:
		// Title-first (tick az1): the name takes the room to its own natural
		// width, and the excerpt claims what is left of it. Below its floor
		// the excerpt drops — it is the column that yields — and its
		// separator returns to the name, which keeps the room it freed.
		what := min(whatNat, max(room, 0))
		ex := min(exNat, room-what)
		if ex < dashExcerptFloor {
			cols.showEx = false
			room += 2
			ex = 0
			what = min(whatNat, room)
			if what < dashWhatFloor {
				cols.showWhat = false
				what = 0
			}
		}
		cols.whatW, cols.excerptW = what, ex
	case cols.showWhat:
		what := min(whatNat, room)
		if what < dashWhatFloor {
			cols.showWhat = false
			what = 0
		}
		cols.whatW = what
	case cols.showEx:
		ex := min(exNat, room)
		if ex < dashExcerptFloor {
			cols.showEx = false
			ex = 0
		}
		cols.excerptW = ex
	}
	return cols
}

// dashExcerptCell is the row's last column: a running worker's live excerpt
// in quotes — its latest tool action, else its latest turn's sentence with
// the role prefix stripped — or, when no worker stands, the run's own word
// for the stage it is driving (a running gate). Nothing measured is
// silence, never a guess.
func dashExcerptCell(t *statusmodel.Tick, workers []statusmodel.Worker, st watchStyles) string {
	if excerpt := dashWorkerExcerpt(t, workers); excerpt != "" {
		return st.dim("\"" + dashCell(excerpt, dashExcerptCols-2) + "\"")
	}
	if stage, state := liveStageOf(t.Pipeline); stage == statusmodel.StageGate && state == statusmodel.StageStateActive {
		return st.dim("gate running")
	}
	return ""
}

// dashWorkerExcerpt is one tick's standing worker's own latest sentence:
// the activity's last action first — the tool call the worker last made —
// else its last turn's summary, the role prefix a reader does not need
// stripped. Empty when no worker stands for the tick's current attempt or
// neither source says anything.
func dashWorkerExcerpt(t *statusmodel.Tick, workers []statusmodel.Worker) string {
	if t.Attempt == nil {
		return ""
	}
	for i := range workers {
		w := &workers[i]
		if w.TickID != t.TickID || w.Attempt != *t.Attempt {
			continue
		}
		if w.Activity != nil && w.Activity.LastAction != nil && *w.Activity.LastAction != "" {
			return *w.Activity.LastAction
		}
		if w.LastTurn != nil && *w.LastTurn != "" {
			return strings.TrimPrefix(strings.TrimPrefix(*w.LastTurn, "assistant: "), "user: ")
		}
	}
	return ""
}

// dashboardTail is the frame's last words: the feed shrunk to the two
// readable sentences the design names (tick 47j), under the latest rule,
// with the key hints seated at the pane's right. A pure-mechanic stage is
// skipped rather than shown, so the tail reaches back past it for a line
// actually worth two of an operator's lines — and the try a line's
// "<tick>#<n>" prefix names is counted from the model's own whole try
// histories (modelTries, tick s71), the same number the [e] feed says.
func dashboardTail(m statusmodel.Model, st watchStyles, width int) []string {
	ruleWidth := width
	if ruleWidth <= 0 {
		ruleWidth = dashRuleWidth
	}
	hint := st.cyan(dashKeyHints)
	tries := modelTries(m)
	events := make([]string, 0, dashRecentEvents)
	for i := len(m.Recent) - 1; i >= 0 && len(events) < dashRecentEvents; i-- {
		sentence, ok := feedSentenceFor(m.Recent[i])
		if !ok {
			continue
		}
		events = append([]string{dashLatestLineStyled(m.Recent[i], sentence, tries, st)}, events...)
	}
	out := []string{""}
	if len(events) == 0 {
		return append(out, st.bold(dashRule(ruleWidth, hint)))
	}
	last := len(events) - 1
	if placed, ok := dashTailSeat(events[last], hint, width); ok {
		events[last] = placed
		out = append(out, st.bold(dashRule(ruleWidth, "")))
		return append(out, events...)
	}
	// The line and the hints cannot share the pane: the hints keep their
	// own line rather than eating the event's words.
	if pad := width - ansi.StringWidth(hint); width > 0 && pad > 0 {
		events = append(events, strings.Repeat(" ", pad)+hint)
	} else {
		events = append(events, hint)
	}
	out = append(out, st.bold(dashRule(ruleWidth, "")))
	return append(out, events...)
}

// dashLatestLineStyled is one latest-sentence line: the clock and the tick's
// own id or try (the same "who" the raw feed line carries), followed by the
// event's readable sentence — the timestamp dim, the tick's id cyan.
func dashLatestLineStyled(event runfeed.Event, sentence string, tries *runfeed.Tries, st watchStyles) string {
	who := watchEventWho(event, tries)
	if event.TickID != nil && *event.TickID != "" {
		who = st.cyan(who)
	}
	return fmt.Sprintf("%s  %s  %s", st.dim(clockOf(event.At)), who, sentence)
}

// dashRule is the latest section's rule: "─ latest " run to the pane's edge,
// with `right` seated over the dashes when there is one to seat.
func dashRule(width int, right string) string {
	left := "─ latest "
	if right == "" {
		return left + strings.Repeat("─", max(0, width-len(left)))
	}
	dashes := width - len(left) - ansi.StringWidth(right)
	if dashes < 1 {
		dashes = 1
	}
	return left + strings.Repeat("─", dashes) + right
}

// dashTailSeat places `right` at the pane's right edge beside the tail's
// last line when the two fit, and says so when they do not.
func dashTailSeat(line, right string, width int) (string, bool) {
	if width <= 0 {
		return line + "  " + right, true
	}
	gap := width - ansi.StringWidth(line) - ansi.StringWidth(right)
	if gap < 1 {
		return "", false
	}
	return line + strings.Repeat(" ", gap) + right, true
}

// fitFrameHead keeps as much of the frame's head as the pane's height
// holds, whole needs-you boxes only: a hold whose clearing command is cut
// off by the fold is the defect the width wrap prevents (tick 9um), so what
// the frame shows of a hold is all of it, and a hold that does not fit is
// not shown at all. The base head (identity, health, track) comes first;
// only when it alone outgrows the pane does the cut slice into it.
func fitFrameHead(base []string, boxes [][]string, width, height int) []string {
	out := make([]string, 0, len(base)+8)
	out = append(out, base[0], frameRule(width))
	for _, box := range boxes {
		if height-len(out) < len(box) {
			break // a hold shown without all its lines is no hold at all
		}
		out = append(out, box...)
	}
	rest := height - len(out)
	if rest > len(base)-1 {
		rest = len(base) - 1
	}
	if rest < 0 {
		rest = 0
	}
	return append(out, base[1:1+rest]...)
}

// dashSeat places `right` at the pane's right edge beside `left`, truncating
// `left` when the pane cannot seat both: the left keeps its start — the
// identity a person scans for — and the right keeps its end — the answer.
func dashSeat(left, right string, width int) string {
	if right == "" {
		return left
	}
	if width <= 0 {
		return left + "    " + right
	}
	if gap := width - ansi.StringWidth(left) - ansi.StringWidth(right); gap >= 1 {
		return left + strings.Repeat(" ", gap) + right
	}
	room := width - ansi.StringWidth(right) - 2
	if room < 1 {
		return right
	}
	return ansi.Truncate(left, room, "…") + "  " + right
}

// dashVerdict is the health headline (hn6 rule 3): a word a person reads —
// green "● healthy", amber "● degraded: …" or red "● stopped: …" — with what
// the run got past by itself riding in brackets as calm, never as alarms.
// Raw counters are the reader's drill-in, not the headline's words.
func dashVerdict(m statusmodel.Model, st watchStyles) string {
	v := m.Health.Verdict
	var head string
	switch v.State {
	case statusmodel.VerdictHealthy:
		head = st.green("● healthy")
	case statusmodel.VerdictDegraded:
		// The summary the builder spells already carries its own "degraded: "
		// prefix (degradedCause), so the renderer prefixes the state only
		// where the summary does not — one vocabulary, never a doubled one
		// ("degraded: degraded: …", the cross-renderer defect h7w pins). An
		// empty summary reads the bare word, exactly as stopped's does.
		switch {
		case v.Summary == "":
			head = st.amber("● degraded")
		case strings.HasPrefix(v.Summary, "degraded:"):
			head = st.amber("● " + v.Summary)
		default:
			head = st.amber("● degraded: " + v.Summary)
		}
	case statusmodel.VerdictStopped:
		if v.Summary == "" {
			head = st.red("● stopped")
		} else {
			head = st.red("● stopped: " + v.Summary)
		}
	default:
		head = "● " + v.State
	}
	if len(v.Recovered) > 0 {
		items := make([]string, 0, len(v.Recovered))
		for _, r := range v.Recovered {
			if r.Seconds != nil {
				items = append(items, fmt.Sprintf("%s %s", r.What, humanDuration(*r.Seconds)))
			} else {
				items = append(items, fmt.Sprintf("%s ×%d", r.What, r.Count))
			}
		}
		head += " (recovered: " + strings.Join(items, ", ") + ")"
	}
	return head
}

// dashCost is the run's cost line (tick b13): what is METERED, and nothing
// else. The three cases the tick names:
//
//   - nothing, when there is no metered cost — no fabricated $0.00, no
//     "not metered" recital, and no config name floating on an empty line;
//   - the metered cost, when there is one — each measured river's number,
//     the whole story still in the model's own lines and `status --json`,
//     with their coverage bases beside them;
//   - on a run whose jobs lease the claude-sub subscription, the leased
//     label and the account's shared 5h/7d window utilization — a run that
//     pays a flat subscription has no wallet number, and its windows are
//     what the operator watches.
//
// The config still leads the line (tick tda) whenever the line has
// something to say: the spend is read beside the config that spent it. A
// measured zero is silence here — an exact $0.00 is no cost — and an
// unmetered river is silence too, never a recital of what nobody measured.
func dashCost(m statusmodel.Model, st watchStyles) string {
	money := make([]string, 0, len(m.Cost.Lines))
	for _, line := range m.Cost.Lines {
		if line.Metered && line.USD != nil && *line.USD > 0 {
			money = append(money, fmt.Sprintf("%s $%.2f", dashCostLabel(line.Source), *line.USD))
		}
	}
	sub := ""
	if s := m.Cost.Subscription; s != nil && s.Label != "" {
		sub = dashSubscription(*s)
	}
	switch {
	case len(money) == 0 && sub == "":
		return ""
	case len(money) == 0:
		return dashCostPrefix(m) + sub
	case sub == "":
		return dashCostPrefix(m) + "cost " + strings.Join(money, " · ")
	default:
		return dashCostPrefix(m) + "cost " + strings.Join(money, " · ") + " · " + sub
	}
}

// dashCostPrefix is the config name the cost line leads with (tick tda) —
// the spend is read beside the config that spent it — empty for a run that
// selected no config.
func dashCostPrefix(m statusmodel.Model) string {
	if m.RunConfig == nil {
		return ""
	}
	return "config " + *m.RunConfig + " · "
}

// dashSubscription is the leased subscription's segment: its label, then the
// utilization the factory's proxy last saw for each window it has answered
// for — "MAX1 · 34% of 5h · 8% of 7d" (the shape the design names). A window
// the proxy has not answered for yet is absent, never "0%": the absence of
// a measurement is not a measurement of zero.
func dashSubscription(sub statusmodel.CostSubscription) string {
	parts := []string{sub.Label}
	if sub.FiveHour != nil {
		parts = append(parts, fmt.Sprintf("%d%% of 5h", percentOf(*sub.FiveHour)))
	}
	if sub.SevenDay != nil {
		parts = append(parts, fmt.Sprintf("%d%% of 7d", percentOf(*sub.SevenDay)))
	}
	return strings.Join(parts, " · ")
}

// percentOf renders a utilization fraction as the whole percent a person
// reads: 0.34 is 34% of the window, and a fraction of a percent rounds to
// the nearest whole — the windows are big enough that the remainder is
// smaller than the number's own noise.
func percentOf(fraction float64) int {
	return int(math.Round(fraction * 100))
}

// dashCostLabel is the source's own name as the tick spells it: the rivers
// the model counts, and the raw source word for anything a newer model
// grows — the label is a word a person reads, never an enum.
func dashCostLabel(source string) string {
	switch source {
	case statusmodel.CostSourceWorkersAI:
		return "Workers AI"
	case statusmodel.CostSourceClaude:
		return "claude"
	case statusmodel.CostSourcePiLocal:
		return "pi (local)"
	case statusmodel.CostSourceDecisions:
		return "decisions"
	case statusmodel.CostSourceOther:
		return "other"
	}
	return source
}

// modelTries counts each tick's tries from the MODEL's own try histories
// (tick s71) — the whole durable record the waves carry — and never from the
// recent tail alone: the tail is a two-line window, and a tick on its
// third try whose window carried only its third attempt counted #1 there,
// disagreeing with the model's own rows and with the [e] feed, which count
// the whole run. The window is still observed beside the histories — a
// line whose tick the waves do not state still names its own try — and
// observing both is harmless: a number seen twice counts once.
func modelTries(m statusmodel.Model) *runfeed.Tries {
	tries := &runfeed.Tries{}
	if m.Waves != nil {
		for wi := range *m.Waves {
			for ti := range (*m.Waves)[wi].Ticks {
				tick := &(*m.Waves)[wi].Ticks[ti]
				for _, try := range tick.Tries {
					id, attempt := tick.TickID, try.Attempt
					tries.Observe(runfeed.Event{TickID: &id, Attempt: &attempt})
				}
			}
		}
	}
	for _, e := range m.Recent {
		tries.Observe(e)
	}
	return tries
}

// liveStageOf is the cell's first stage that is not done — the one stage the
// tick is in, or the first one ahead of it — with that stage's own state.
// An all-done cell answers empty with done.
func liveStageOf(cell []statusmodel.PipelineStage) (string, string) {
	for _, stage := range cell {
		if stage.State != statusmodel.StageStateDone {
			return stage.Stage, stage.State
		}
	}
	return "", statusmodel.StageStateDone
}

// dashPipeline is the drill-in view's pipeline cell (hn6 rule 1): the stops
// one tick passes through, filling left to right — done stages plain (the
// last one's ✓ green), the live one amber with "●", a refusal red with "✗",
// and every stage behind the live one folded into one dim "…". A pane
// narrower than the words gets one glyph per stage instead; the stage list
// is the tick's own, so a review or close-out row shows its own stops.
func dashPipeline(stages []statusmodel.PipelineStage, st watchStyles, words bool) string {
	if len(stages) == 0 {
		return ""
	}
	if words {
		return dashPipelineWords(stages, st)
	}
	return dashPipelineGlyphs(stages, st)
}

func dashPipelineWords(stages []statusmodel.PipelineStage, st watchStyles) string {
	parts := make([]string, 0, len(stages))
	live := false
	for i, stage := range stages {
		if live {
			break
		}
		switch stage.State {
		case statusmodel.StageStateDone:
			name := stage.Stage
			if i == len(stages)-1 {
				// A tick that finished its last stage says so at the
				// cell's end, in the hue of finished things:
				// "merged ✓" / "closed ✓".
				name += " " + st.green("✓")
			}
			parts = append(parts, name)
		case statusmodel.StageStateActive:
			// The live stage is amber — in flight, the one thing to watch.
			live = true
			parts = append(parts, st.amber("●"+stage.Stage))
			if i < len(stages)-1 {
				parts = append(parts, st.dim("…"))
			}
		case statusmodel.StageStateFailed:
			live = true
			parts = append(parts, st.red("✗"+stage.Stage))
			if i < len(stages)-1 {
				parts = append(parts, st.dim("…"))
			}
		default:
			live = true
			parts = append(parts, st.dim("…"))
		}
	}
	return strings.Join(parts, " ▸ ")
}

func dashPipelineGlyphs(stages []statusmodel.PipelineStage, st watchStyles) string {
	parts := make([]string, 0, len(stages))
	for _, stage := range stages {
		switch stage.State {
		case statusmodel.StageStateDone:
			parts = append(parts, st.green("✓"))
		case statusmodel.StageStateActive:
			parts = append(parts, st.amber("●"))
		case statusmodel.StageStateFailed:
			parts = append(parts, st.red("✗"))
		default:
			parts = append(parts, st.dim("○"))
		}
	}
	return strings.Join(parts, " ")
}

// watchRunElapsed reads the run's whole span off the model's own field —
// the span was once derived here, from the earliest try stamp to
// generated_at, and the derivation was the defect (tick e6g): it counted
// past a finished run, and no other surface could share it because no
// contract field carried it. The model measures and clamps it now; the
// second return says whether the model stated one at all — an elapsed
// nobody measured is an elapsed nobody prints.
func watchRunElapsed(m statusmodel.Model) (int64, bool) {
	if m.Progress.RunElapsedSeconds == nil {
		return 0, false
	}
	return *m.Progress.RunElapsedSeconds, true
}

// humanDuration is a person's clock for the frame's timers: seconds under a
// minute, minutes under an hour, hours and minutes under a day, days and
// hours past that. Rounded for glancing, because the exact numbers live in
// `status --json`.
func humanDuration(seconds int64) string {
	switch d := time.Duration(seconds) * time.Second; {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int64(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int64(d.Minutes()))
	case d < 24*time.Hour:
		h, m := int64(d.Hours()), int64(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int64(d.Hours()) / 24
		return fmt.Sprintf("%dd%dh", days, int64(d.Hours())%24)
	}
}

// dashCell is one cell's content, kept inside its column: a cell wider than
// its column keeps its start and says it was cut, because a column that
// silently ate its neighbours is a table a person cannot read.
func dashCell(s string, cols int) string {
	if s == "" || ansi.StringWidth(s) <= cols {
		return s
	}
	return ansi.Truncate(s, cols, "…")
}

// dashPad pads one cell to its column's width by display width, so the
// columns stay aligned however their neighbours are styled.
func dashPad(s string, cols int) string {
	if gap := cols - ansi.StringWidth(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}
