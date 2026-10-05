package cli

// The dashboard half of `ticfac watch` (epic hn6, wave 3 — tick u5n): the
// status model drawn as a dashboard, not a feed. A world-class status answers,
// in the order a person asks — does anything need me, is it healthy, how far
// along is it, what is happening now — and everything else is drill-in
// behind a key.
//
// The shape is the one the hn6 spec fixed, borrowed from the tools it names:
// k9s/btop's stable resource table (one row per tick, in plan order; a row
// never moves, only its cells change), GitHub Actions' per-job stage pipeline
// (the PIPELINE cell fills left to right), Temporal/Argo's durable phase (the
// phase bar under the headline), and Vercel/Linear's calm headline (a health
// verdict, a needs-you answer that is quiet when it is "nothing").
//
// Everything here is a pure function of the model plus the pane's width and
// height: nothing reads a file, spawns a process or measures a host, so the
// whole frame is pinned byte-for-byte against the contract bundle's
// `dashboard` golden at the widths the tick names (watch_view_test.go), and
// the wiring — the redraw loop, the keep lines, the exit codes — stays in
// watch.go and watch_block_test.go. Width 0 means unknown: no column drops,
// no truncation, no height limit; the caller that knows nothing draws
// everything and lets the terminal scroll.

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// The pane widths at which columns drop, in the tick's fixed order: the
// pipeline's words first, then the tier and attempts, then the work's own
// name — so a narrow pane drops detail rather than wrapping rows into an
// unreadable tangle, and a row's identity never goes.
const (
	watchWordsFrom = 100 // the pipeline cell spells its stages out in full
	watchTierFrom  = 64  // the TIER and ATTEMPTS columns join the row
	watchWhatFrom  = 48  // the WHAT column joins the row
)

// The frame's fixed geometry: the progress bar's cells, the table's column
// caps, the workers panel's columns, and the width the recent rule fills
// when the pane's own width is unknown. The table's columns are sized to
// their content up to these caps (tick r3x); the workers panel keeps its
// fixed columns.
const (
	watchBarCells     = 24
	dashTickCols      = 5 // the identity column's floor: the spec's own layout
	dashWhatCols      = 32
	dashTierCols      = 10
	dashPipeWordsCols = 32
	dashPipeGlyphCols = 12
	dashTimeCols      = 7
	dashWorkerCols    = 46
	dashActivityCols  = 16
	dashRecentRule    = 100
	dashRecentEvents  = 2
)

// watchStyles is the frame's small style set, injected so a test reads the
// words with the identity set and the colours with the ANSI one — the frame
// must be as assertable as it is readable. Six styles cover the frame: dim
// for what is far from now or quiet by design (closed groups, provenance,
// secondary text), amber for what is in flight or wants a look, red for
// what is wrong or held for a person (with bold, the most prominent thing
// on screen when it stands), green for what passed or finished healthy,
// cyan for the identities and key hints a person scans for, bold for what
// leads (the section headers). Four hues, no background fills: colour
// carries meaning, never decoration (tick 5ba).
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

// renderWatchFrame renders one frame of the dashboard: the headline (the
// run's identity, its progress with the health verdict, the phase bar with
// the needs-you answer), one fixed row per tick in plan order, the live
// workers with their activity, the CI and cost lines, and the two-line tail
// of recent events. `selected` is the tick id whose row renders with a "▸"
// marker — the drill-in cursor the keys move; "" for none.
func renderWatchFrame(m statusmodel.Model, st watchStyles, width, height int, selected string) []string {
	head := dashboardHeadline(m, st, width)
	holds := dashboardAttentionBlocks(m, st, width)
	for _, block := range holds {
		head = append(head, block...)
	}
	tableHead, rows := dashboardTable(m, st, width, selected)
	workers := dashboardWorkers(m, st, width)
	cost := dashboardCICost(m, st, width)
	tail := dashboardTail(m, st, width)

	frame := fitDashboard(head, holds, tableHead, rows, workers, cost, tail, height, st)

	// Width: the columns were dropped while building the rows (above); what
	// is left is keeping every line inside the pane, however wide the
	// detail wanted to be — and no line carries spaces out to the edge it
	// padded to, because a trailing space is a column nobody reads.
	for i, line := range frame {
		line = strings.TrimRight(line, " ")
		if width > 0 {
			line = ansi.Truncate(line, width, "")
		}
		frame[i] = line
	}
	return frame
}

// dashboardHeadline is the frame's first three header lines: the run's
// identity with its liveness, the progress bar with elapsed, ETA and the
// health verdict, and the phase bar with the needs-you answer at its right.
// The bare `ticfac` overview reuses exactly these lines, so their assembly
// lives here, named, and not inside the watch alone. A pane too narrow to
// seat the needs-you answer beside the phase bar reflows it onto a fourth
// line — the answer is never dropped for a decoration.
func dashboardHeadline(m statusmodel.Model, st watchStyles, width int) []string {
	identity := m.EpicID
	if m.EpicTitle != nil && *m.EpicTitle != "" {
		identity += " " + *m.EpicTitle
	}
	life := "not alive"
	if m.Liveness.Alive {
		life = "alive"
	} else if m.Lifecycle.Phase == statusmodel.PhaseFailed || m.Lifecycle.Phase == statusmodel.PhaseCancelled {
		// The run's own terminal word, when its lifecycle carried one: a
		// failed run is not merely "not alive" — the header says what the
		// run said it ended as, because that is the question the reader is
		// actually asking (hn6, tick gmo).
		life = m.Lifecycle.Phase
	}
	head := []string{
		// The provenance at the pane's right — the host, the executor, the
		// liveness word — is secondary text: dim, never decoration.
		dashSeat(identity, st.dim(m.Host+" · "+life), width),
		dashProgressLine(m, st, width),
	}
	return append(head, dashPhaseLine(m, st, width)...)
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

// dashProgressLine is the second question — how far along — as the tick
// shaped it: the progress bar, the ticks closed over the total, the elapsed,
// the approximate time left, and the health verdict beside them. The bar is
// the header's answer to "how far along" (A1): a line the pane cannot seat
// whole gives way at the verdict's summary — truncated with an ellipsis,
// never the whole bar dropped (g94) — and the bar gives way entirely only
// when the pane cannot seat it beside its counts, or cannot seat even a
// readable stub of the verdict beside them: answers over decoration.
func dashProgressLine(m statusmodel.Model, st watchStyles, width int) string {
	mid := ""
	if m.Progress.Ticks != nil {
		mid = fmt.Sprintf("%d/%d ticks", m.Progress.Ticks.Closed, m.Progress.Ticks.Total)
	}
	if elapsed, ok := watchRunElapsed(m); ok {
		if mid != "" {
			mid += " · "
		}
		mid += humanDuration(elapsed)
	}
	if m.Remaining != nil {
		if mid != "" {
			mid += " · "
		}
		mid += "ETA ~" + humanDuration(m.Remaining.ApproximateSeconds)
	}
	verdict := dashVerdict(m, st)
	join := func(head string) string {
		if head == "" {
			return verdict
		}
		if verdict == "" {
			return head
		}
		return head + " · " + verdict
	}
	if m.Progress.Ticks == nil || m.Progress.Ticks.Total <= 0 {
		return join(mid)
	}
	filled := m.Progress.Ticks.Closed * watchBarCells / m.Progress.Ticks.Total
	if filled < 0 {
		filled = 0
	}
	if filled > watchBarCells {
		filled = watchBarCells
	}
	seat := strings.Repeat("█", filled) + strings.Repeat("░", watchBarCells-filled) + " " + mid
	line := join(seat)
	if width <= 0 || ansi.StringWidth(line) <= width {
		return line
	}
	// The pane cannot seat the whole line. The verdict's summary is the
	// elastic part: the bar keeps its seat and the summary is cut with an
	// ellipsis, so a stopped run's sentence-long summary no longer evicts
	// the bar from a 120-column pane (g94). Truncation from the left keeps
	// the verdict's state word — "● stopped: …" — so the cut lands on the
	// detail, never on the answer.
	if ansi.StringWidth(seat) > width {
		return join(mid)
	}
	if room := width - ansi.StringWidth(seat) - 3; room >= dashVerdictStubCells {
		return seat + " · " + ansi.Truncate(verdict, room, "…")
	}
	// The bar leaves no room for even a readable stub of the verdict: the
	// answers keep the width and the bar waits for a wider pane.
	return join(mid)
}

// dashVerdictStubCells is the least a truncated verdict needs to stay
// readable beside the bar: the marker, a space, the state word's first
// letter and the ellipsis — "● s…". Below that the bar is stealing the
// answer's width, and the answer wins.
const dashVerdictStubCells = 4

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

// dashPhaseLine is the lifecycle as the phase bar: plan, the waves with their
// k/n, review, close-out, ci, merge — each phase marked ✓ done, ◐ active or
// ○ pending — with the needs-you answer at the pane's right. Nothing needs a
// person and the answer reads dim and quiet ("needs you: nothing"); a hold
// has its own line below, so the bar's right side stays empty rather than
// repeating it.
func dashPhaseLine(m statusmodel.Model, st watchStyles, width int) []string {
	segments := make([]string, 0, len(statusmodel.Phases))
	for _, phase := range statusmodel.Phases {
		state := statusmodel.PhaseStatePending
		for _, ps := range m.Lifecycle.Phases {
			if ps.Phase == phase {
				state = ps.State
				break
			}
		}
		name := watchPhaseName(phase)
		if phase == statusmodel.PhaseWaves && state == statusmodel.PhaseStateActive && m.Lifecycle.Wave != nil {
			name = fmt.Sprintf("waves %d/%d", m.Lifecycle.Wave.Active, m.Lifecycle.Wave.Total)
		}
		switch state {
		case statusmodel.PhaseStateDone:
			// A phase that finished says so in the hue of finished things —
			// the mark carries the colour, the name stays plain: colour
			// carries meaning, sparingly.
			segments = append(segments, st.green("✓")+" "+name)
		case statusmodel.PhaseStateActive:
			segments = append(segments, st.amber("◐ "+name))
		default:
			segments = append(segments, st.dim("○ "+name))
		}
	}
	phases := strings.Join(segments, " ")
	if dashboardNeedsSomebody(m) {
		return []string{phases}
	}
	quiet := st.dim("needs you: nothing")
	if width <= 0 {
		return []string{phases + "    " + quiet}
	}
	if gap := width - ansi.StringWidth(phases) - ansi.StringWidth(quiet); gap >= 2 {
		return []string{phases + strings.Repeat(" ", gap) + quiet}
	}
	// The pane cannot seat both, and the needs-you answer is an answer, not
	// a decoration: it keeps a line of its own.
	pad := ""
	if p := width - ansi.StringWidth(quiet); p > 0 {
		pad = strings.Repeat(" ", p)
	}
	return []string{phases, pad + quiet}
}

// dashboardNeedsSomebody says whether anything in the model's attention list
// needs a person — the fact that decides between the quiet right side and
// the amber lines below the phase bar.
func dashboardNeedsSomebody(m statusmodel.Model) bool {
	for _, a := range m.Attention {
		if a.NeedsPerson {
			return true
		}
	}
	return false
}

// dashboardAttentionLines is the first question — does anything need me —
// asked in the header, one red+bold line per hold, each naming the one command
// that moves it on: the most prominent thing on screen while it stands.
// Nothing needs a person and there is no line at all: the phase bar's quiet
// right side is the whole answer. A line the pane cannot seat is wrapped
// under its announcement, never truncated (tick 9um).
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

// dashboardAttentionBlocks is the holds' own lines one block per hold: the
// shape the height fit needs, so a short pane drops a hold whole and never
// the half of one (fitDashboard).
func dashboardAttentionBlocks(m statusmodel.Model, st watchStyles, width int) [][]string {
	blocks := [][]string{}
	for _, a := range m.Attention {
		if !a.NeedsPerson {
			continue
		}
		blocks = append(blocks, dashboardHoldLines(a, st, width))
	}
	return blocks
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

// dashRow is one tick row of the table: the rendered line, the tick it
// carries, and whether that tick is closed — the fact the height fit
// collapses contiguous runs of.
type dashRow struct {
	line   string
	tickID string
	closed bool
}

// dashCells is one tick row's cells before the table's columns are sized:
// each cell already carries its styling and its own cap, the column widths
// come from the widest content the table actually carries — the header
// label included — and the render pads to the widths that sizing produced.
type dashCells struct {
	lead      string // the mark column: the cursor, a child's indent under it
	id        string
	what      string
	tier      string
	pipe      string
	time      string
	attempts  string
	tickID    string
	closed    bool
	duplicate bool // the tick is a duplicate: its row renders dimmed whole
}

// render lays the cells out under the sized columns: one space between
// columns, every cell padded to its column's width. Trailing padding is
// left for the frame's trim. A child's indent ("  └ ") is three cells wider
// than the one-cell mark column its parent carries, and the WHAT cell gives
// those cells up — truncated and padded to what is left of the column — so
// the columns after it sit under the header's labels beside the parent
// rows, never three cells right of them (tick cg4).
func (c dashCells) render(showWhat, showTier bool, idW, whatW, tierW, pipeW, timeW, attemptsW int) string {
	whatCols := whatW
	if extra := ansi.StringWidth(c.lead) - 1; extra > 0 {
		whatCols -= extra
	}
	var row strings.Builder
	row.WriteString(c.lead)
	row.WriteString(dashPad(c.id, idW))
	if showWhat {
		row.WriteString(" " + dashPad(dashCell(c.what, whatCols), whatCols))
	}
	if showTier {
		row.WriteString(" " + dashPad(c.tier, tierW))
	}
	row.WriteString(" " + dashPad(c.pipe, pipeW))
	row.WriteString(" " + dashPad(c.time, timeW))
	if showTier {
		row.WriteString(" " + dashPad(c.attempts, attemptsW))
	}
	return row.String()
}

// dashboardTable is the epic as the spec's fixed table: one row per tick, in
// plan order (the waves in the tracker's own order, its order inside each
// wave), children indented under the row named by parent_tick_id, a selected
// row marked with "▸", absorbed ticks marked with a "+" before their id. An
// unreadable tracker is said, never silently skipped: a frame that showed an
// empty table would look like an empty epic.
//
// The columns are sized to their content: every column is as wide as the
// widest cell it carries — the header label included — capped at the
// layout's own maximum so one long cell cannot eat the pane (tick r3x: the
// header laid its columns across the full pane with huge gaps no cell
// filled). The identity column keeps the spec's five-cell floor.
func dashboardTable(m statusmodel.Model, st watchStyles, width int, selected string) ([]string, []dashRow) {
	showWhat := width == 0 || width >= watchWhatFrom
	showTier := width == 0 || width >= watchTierFrom
	words := width == 0 || width >= watchWordsFrom
	pipeCap := dashPipeWordsCols
	if !words {
		pipeCap = dashPipeGlyphCols
	}

	cells := make([]dashCells, 0, 16)
	if m.Waves != nil {
		for _, wave := range *m.Waves {
			for _, tick := range wave.Ticks {
				cells = append(cells, dashTickCells(tick, st, showWhat, showTier, words, pipeCap, selected))
			}
		}
	}
	idW, whatW, tierW, pipeW, timeW, attemptsW := dashColumnWidths(cells, showWhat, showTier, pipeCap)

	head := " " + dashPad("TICK", idW)
	if showWhat {
		head += " " + dashPad("WHAT", whatW)
	}
	if showTier {
		head += " " + dashPad("TIER", tierW)
	}
	head += " " + dashPad("PIPELINE", pipeW)
	head += " " + dashPad("TIME", timeW)
	if showTier {
		head += " " + dashPad("ATTEMPTS", attemptsW)
	}
	// The section header leads in bold (tick 5ba) — trimmed first, so the
	// padding a column nobody fills ends in is not sealed inside the bold
	// span the frame's trailing-space trim then cannot see.
	head = st.bold(strings.TrimRight(head, " "))

	if m.Waves == nil {
		return []string{"", head}, []dashRow{{
			line: st.dim("the epic's shape could not be read (the tracker did not answer)"),
		}}
	}
	rows := make([]dashRow, 0, len(cells))
	for _, c := range cells {
		line := c.render(showWhat, showTier, idW, whatW, tierW, pipeW, timeW, attemptsW)
		if c.duplicate {
			// Dimmed whole: the row keeps its place (rows never move) and its
			// history, but nothing about it is the frontier's business — the
			// accents it carried are stripped before the dim covers it, so the
			// dim is one span over the whole row and never an accent's reset
			// away from the rest of it.
			line = st.dim(ansi.Strip(line))
		}
		rows = append(rows, dashRow{
			line:   line,
			tickID: c.tickID,
			closed: c.closed,
		})
	}
	return []string{"", head}, rows
}

// dashColumnWidths sizes the table's columns to the widest cell each carries
// — the header label included — capped at the layout's maxima (the WHAT,
// TIER and PIPELINE cells already carry theirs from dashCell). Measured by
// display width, so styled cells size their columns honestly.
func dashColumnWidths(cells []dashCells, showWhat, showTier bool, pipeCap int) (idW, whatW, tierW, pipeW, timeW, attemptsW int) {
	idW, whatW, tierW = dashTickCols, len("WHAT"), len("TIER")
	pipeW, timeW, attemptsW = len("PIPELINE"), len("TIME"), len("ATTEMPTS")
	for i := range cells {
		c := &cells[i]
		idW = max(idW, ansi.StringWidth(c.id))
		pipeW = max(pipeW, ansi.StringWidth(c.pipe))
		timeW = max(timeW, ansi.StringWidth(c.time))
		if showWhat {
			whatW = max(whatW, ansi.StringWidth(c.what))
		}
		if showTier {
			tierW = max(tierW, ansi.StringWidth(c.tier))
			attemptsW = max(attemptsW, ansi.StringWidth(c.attempts))
		}
	}
	if whatW > dashWhatCols {
		whatW = dashWhatCols
	}
	if tierW > dashTierCols {
		tierW = dashTierCols
	}
	if pipeW > pipeCap {
		pipeW = pipeCap
	}
	if timeW > dashTimeCols {
		timeW = dashTimeCols
	}
	return idW, whatW, tierW, pipeW, timeW, attemptsW
}

// dashTickCells is one row of the table's raw cells: the tick's identity (a
// "▸" when the drill-in cursor is on it, a "+" when the run absorbed it, a
// " └ " when it is another tick's child), its work's name, its current tier,
// its pipeline cell, its duration and its attempts. A duplicate is not work
// the epic owes: its WHAT says the tick it is a copy of and its row renders
// dimmed whole. The row's POSITION never depends on any of these — only its
// cells change.
func dashTickCells(tick statusmodel.Tick, st watchStyles, showWhat, showTier, words bool, pipeCap int, selected string) dashCells {
	id := tick.TickID
	if tick.Absorbed {
		id = "+" + id
	}
	mark := " "
	if selected != "" && selected == tick.TickID {
		mark = "▸"
	}
	lead := mark
	if tick.ParentTickID != nil {
		// A child row indents under its parent — the mark column still
		// comes first, so the cursor never shifts a row's id.
		lead = "  └" + mark
	}
	what := ""
	if showWhat {
		what = tick.Gloss
		if what == "" {
			what = tick.Title
		}
		// A duplicate is not work the epic owes — the row is its own tick's
		// history, but the WHAT column says what it is a copy of, so a
		// person scanning the table reads the fact without drilling in.
		if tick.DuplicateOf != nil && *tick.DuplicateOf != "" {
			what = "duplicate of " + *tick.DuplicateOf
		}
		what = dashCell(what, dashWhatCols)
	}
	tier := ""
	if showTier && tick.Tier != nil {
		tier = dashCell(*tick.Tier, dashTierCols)
	}
	pipe := dashCell(dashPipeline(tick.Pipeline, st, words), pipeCap)
	timeCell := ""
	if tick.DurationSeconds != nil {
		timeCell = dashCell(humanDuration(*tick.DurationSeconds), dashTimeCols)
	}
	attempts := ""
	if showTier {
		attempts = dashAttempts(tick)
	}
	return dashCells{
		lead: lead, id: st.cyan(id), what: what, tier: tier, pipe: pipe,
		time: timeCell, attempts: attempts, tickID: tick.TickID,
		closed:    tick.State == "closed",
		duplicate: tick.DuplicateOf != nil,
	}
}

// dashPipeline is the row's pipeline cell (hn6 rule 1): the stops one tick
// passes through, filling left to right — done stages plain (the last one's
// ✓ green), the live one amber with "●", a refusal red with "✗", and every
// stage behind the live one folded into one dim "…". A pane narrower than
// the words (below watchWordsFrom) gets one glyph per stage instead; the
// stage list is the tick's own, so a review or close-out row shows its own
// stops.
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

// dashAttempts is the row's try count, with "⤴" when the current attempt
// runs a different tier than the first — the fact a person reads as "the run
// re-cut this one higher".
func dashAttempts(t statusmodel.Tick) string {
	s := fmt.Sprintf("%d", len(t.Tries))
	if len(t.Tries) > 0 {
		first, last := "", ""
		if t.Tries[0].Tier != nil {
			first = *t.Tries[0].Tier
		}
		if t.Tries[len(t.Tries)-1].Tier != nil {
			last = *t.Tries[len(t.Tries)-1].Tier
		}
		if first != last {
			s += " ⤴"
		}
	}
	return s
}

// dashboardWorkers is the per-agent panel (hn6 rule 5): one row per live
// worker with its model, executor and handle, its activity sparkline, its
// last action with its age, and an amber "nudged ×N" when the run has nudged
// it as stuck. The buckets scale to the window's own maximum; a window with
// nothing in it reads as dots. A census this machine cannot take — a cloud
// run's workers are elsewhere — is said, not faked.
func dashboardWorkers(m statusmodel.Model, st watchStyles, width int) []string {
	if m.Workers == nil {
		return []string{"", st.dim("workers run in the cloud — not visible from here")}
	}
	workers := *m.Workers
	if len(workers) == 0 {
		return nil
	}
	lastInline := width == 0 || width >= watchWordsFrom
	slim := width > 0 && width < watchTierFrom

	activity := "ACTIVITY"
	for i := range workers {
		if a := workers[i].Activity; a != nil && a.WindowSeconds > 0 {
			activity = fmt.Sprintf("ACTIVITY (%s)", humanDuration(int64(a.WindowSeconds)))
			break
		}
	}
	head := dashPad("WORKERS", dashWorkerCols) + dashPad(activity, dashActivityCols)
	if lastInline {
		head += "LAST"
	}
	// The panel's own section header leads in bold, like the table's —
	// trimmed first, so no padding is sealed inside the span.
	lines := []string{"", st.bold(strings.TrimRight(head, " "))}
	for i := range workers {
		w := &workers[i]
		tick := dashWorkerTick(m, *w)
		row := dashPad(dashCell(dashWorkerIdentity(tick, w, slim, st), dashWorkerCols), dashWorkerCols)
		spark := ""
		if w.Activity != nil {
			spark = dashSparkline(w.Activity.Buckets)
		}
		row += dashPad(spark, dashActivityCols-1) + " "
		last := dashWorkerLast(m, st, w)
		if lastInline {
			if last != "" {
				lines = append(lines, row+last)
			} else {
				lines = append(lines, strings.TrimRight(row, " "))
			}
		} else {
			lines = append(lines, strings.TrimRight(row, " "))
			if last != "" {
				lines = append(lines, "    "+last)
			}
		}
	}
	return lines
}

// dashWorkerIdentity is the worker row's first cell: the tick id — cyan,
// the identity a person scans for — then the model, the executor and the
// handle, joined with " · ": provenance, dim. A slim pane keeps only the
// word a person uses to FIND the worker on the machine — the handle —
// because that is the one a glance is for.
func dashWorkerIdentity(tick *statusmodel.Tick, w *statusmodel.Worker, slim bool, st watchStyles) string {
	handle := ""
	if w.Handle != nil {
		handle = *w.Handle
	}
	if slim {
		switch {
		case handle != "":
			return st.cyan(w.TickID) + "  " + handle
		case tick != nil && tick.Executor != nil:
			return st.cyan(w.TickID) + "  " + st.dim(*tick.Executor)
		case tick != nil && tick.Model != nil:
			return st.cyan(w.TickID) + "  " + st.dim(shortModel(*tick.Model))
		}
		return st.cyan(w.TickID)
	}
	parts := []string{}
	if tick != nil && tick.Model != nil {
		parts = append(parts, shortModel(*tick.Model))
	}
	if tick != nil && tick.Executor != nil {
		parts = append(parts, *tick.Executor)
	}
	if handle != "" {
		parts = append(parts, handle)
	}
	if len(parts) == 0 {
		return st.cyan(w.TickID)
	}
	return st.cyan(w.TickID) + "  " + st.dim(strings.Join(parts, " · "))
}

// dashWorkerTick is the worker's own tick — matched by the attempt identity
// both carry, never by order, and never guessed — or nil when no tick of the
// model's waves answers for it.
func dashWorkerTick(m statusmodel.Model, w statusmodel.Worker) *statusmodel.Tick {
	if m.Waves == nil {
		return nil
	}
	for _, wave := range *m.Waves {
		for i := range wave.Ticks {
			t := &wave.Ticks[i]
			if t.TickID != w.TickID {
				continue
			}
			if t.Attempt != nil && *t.Attempt != w.Attempt {
				continue
			}
			return t
		}
	}
	return nil
}

// dashWorkerLast is the worker's last tool action with its age, and the
// nudge count when the run has nudged it — a stuck worker is visible, not
// silent (hn6 rule 5).
func dashWorkerLast(m statusmodel.Model, st watchStyles, w *statusmodel.Worker) string {
	last := ""
	if w.Activity != nil && w.Activity.LastAction != nil && *w.Activity.LastAction != "" {
		last = *w.Activity.LastAction
		if w.Activity.LastActionAt != nil {
			if age, ok := modelAge(m, *w.Activity.LastActionAt); ok {
				last += fmt.Sprintf(" (%s ago)", humanDuration(age))
			}
		}
	}
	if w.Activity != nil && w.Activity.Nudges > 0 {
		nudged := st.amber(fmt.Sprintf("nudged ×%d", w.Activity.Nudges))
		if last == "" {
			last = nudged
		} else {
			last += "  " + nudged
		}
	}
	return last
}

// dashSparkline is the activity window as one glyph per bucket, scaled to
// the window's own maximum — ▁▂▃▄▅▆▇█ — and a dot per bucket when nothing
// happened in the window at all.
func dashSparkline(buckets []int) string {
	if len(buckets) == 0 {
		return ""
	}
	max := 0
	for _, v := range buckets {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return strings.Repeat("·", len(buckets))
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	out := make([]rune, 0, len(buckets))
	for _, v := range buckets {
		idx := (v*len(levels) - 1) / max
		if idx < 0 {
			idx = 0
		}
		if idx >= len(levels) {
			idx = len(levels) - 1
		}
		out = append(out, levels[idx])
	}
	return string(out)
}

// dashboardCICost is the CI line and the cost line — one line when the pane
// seats both, two when it does not (hn6 rules 7 and 8): the forge's checks
// on the epic PR's head, per check, and the run's spend per source, honest
// about what nothing measured.
func dashboardCICost(m statusmodel.Model, st watchStyles, width int) []string {
	ci := dashCI(m, st)
	if github := dashGitHub(m, st); github != "" {
		ci += " · " + github
	}
	cost := dashCost(m, st)
	if width <= 0 {
		return []string{"", ci + "    " + cost}
	}
	if gap := width - ansi.StringWidth(ci) - ansi.StringWidth(cost); gap >= 2 {
		return []string{"", ci + strings.Repeat(" ", gap) + cost}
	}
	return []string{"", ci, cost}
}

// dashCI is the forge's answer on the epic PR's head, per check: ✓ a check
// that completed green (in green), ✗ red (in red), ◐ with its age a check
// still running (in amber). No PR yet and the line says so, dimly — a run
// that has not opened its PR is a fact, not a silence.
func dashCI(m statusmodel.Model, st watchStyles) string {
	if m.CI == nil || m.CI.PR == nil {
		return st.dim("CI: no PR yet")
	}
	parts := make([]string, 0, len(m.CI.Checks))
	for _, c := range m.CI.Checks {
		switch {
		case c.Status == "completed" && c.Conclusion == "success":
			parts = append(parts, c.Name+" "+st.green("✓"))
		case c.Status == "completed" && c.Conclusion == "failure":
			parts = append(parts, c.Name+" "+st.red("✗"))
		case c.Status == "completed":
			parts = append(parts, c.Name+" "+c.Conclusion)
		default:
			if age, ok := modelAge(m, c.StartedAt); ok {
				parts = append(parts, c.Name+" "+st.amber("◐ "+humanDuration(age)))
			} else {
				parts = append(parts, c.Name+" "+st.amber("◐"))
			}
		}
	}
	line := fmt.Sprintf("CI #%d", m.CI.PR.Number)
	if len(parts) > 0 {
		line += " " + strings.Join(parts, " · ")
	}
	return line
}

// dashGitHub is the run's traffic with its forge beside the CI it reads
// (tick rlp): its pushes and its failed GitHub attempts, counted from the
// run's own typed lines, dim when nothing failed. A run that neither pushed
// nor failed says nothing — a zero is not news.
func dashGitHub(m statusmodel.Model, st watchStyles) string {
	h := m.Health
	var counts []string
	if h.Pushes > 0 {
		counts = append(counts, watchPlural(h.Pushes, "push"))
	}
	if n := h.GitHubErrors.Total(); n > 0 {
		return strings.Join(append(counts, st.amber(watchPlural(n, "GitHub error"))), ", ")
	}
	return st.dim(strings.Join(counts, ", "))
}

// dashCost is the run's spend per source (hn6 rule 7): a metered line's
// measured number, and "not metered" — dim, secondary text — where nothing
// measured, never a fabricated $0.00, because an unmetered line wearing a
// number is a lie with a decimal point. No lines at all and the whole cost
// says so.
func dashCost(m statusmodel.Model, st watchStyles) string {
	if len(m.Cost.Lines) == 0 {
		return st.dim("cost not metered")
	}
	parts := make([]string, 0, len(m.Cost.Lines))
	for _, line := range m.Cost.Lines {
		if line.Metered && line.USD != nil {
			parts = append(parts, fmt.Sprintf("%s $%.2f", dashCostLabel(line.Source), *line.USD))
		} else {
			parts = append(parts, dashCostLabel(line.Source)+" "+st.dim("not metered"))
		}
	}
	return "cost " + strings.Join(parts, " · ")
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
// recent tail alone: the tail is a five-line window, and a tick on its
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

// dashboardTail is the feed shrunk to the two-line tail the spec names (hn6
// rule 6), under a bold "─ recent" rule, with the key hint at the pane's
// right in cyan. The lines are the feed's own words — the same one-line form
// the stream path prints, through the styled variant that carries the
// palette: the timestamp dim, the tick's own id cyan, a refusal red — and
// the try a line's "<tick>#<n>" prefix names is counted from the model's own
// whole try histories (modelTries, tick s71), the same number the rows and
// the [e] feed say — never from the five-line window the tail itself is.
func dashboardTail(m statusmodel.Model, st watchStyles, width int) []string {
	ruleWidth := width
	if ruleWidth <= 0 {
		ruleWidth = dashRecentRule
	}
	hint := st.cyan("[e] events  [enter] tick")
	tries := modelTries(m)
	start := len(m.Recent) - dashRecentEvents
	if start < 0 {
		start = 0
	}
	events := make([]string, 0, dashRecentEvents)
	for _, e := range m.Recent[start:] {
		events = append(events, watchEventLineStyled(e, tries, st))
	}
	if len(events) == 0 {
		return []string{st.bold(dashRule(ruleWidth, hint))}
	}
	last := len(events) - 1
	if placed, ok := dashTailSeat(events[last], hint, width); ok {
		events[last] = placed
		return append([]string{st.bold(dashRule(ruleWidth, ""))}, events...)
	}
	// The line and the hint cannot share the pane: the hint keeps its own
	// line rather than eating the event's words.
	if pad := width - ansi.StringWidth(hint); width > 0 && pad > 0 {
		events = append(events, strings.Repeat(" ", pad)+hint)
	} else {
		events = append(events, hint)
	}
	return append([]string{st.bold(dashRule(ruleWidth, ""))}, events...)
}

// dashRule is the tail's section rule: "─ recent" run to the pane's edge,
// with `right` seated over the dashes when there is one to seat.
func dashRule(width int, right string) string {
	left := "─ recent "
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

// fitDashboard fits the frame to the pane's height the way the tick
// describes. The headline and the tail are always kept — the questions and
// the last words — and the middle is compressed in place: contiguous runs of
// CLOSED rows collapse into one dim "✓ N closed" line each, so no row moves
// relative to another, and whatever still does not fit is counted by one
// "+N more" at the fold. Fitting never reorders what it keeps.
func fitDashboard(head []string, holds [][]string, tableHead []string, rows []dashRow, workers, cost, tail []string, height int, st watchStyles) []string {
	middle := func(rows []dashRow) []string {
		lines := append([]string{}, tableHead...)
		for _, r := range rows {
			lines = append(lines, r.line)
		}
		return append(append(lines, workers...), cost...)
	}
	joined := func(rows []dashRow) []string {
		out := append([]string{}, head...)
		out = append(out, middle(rows)...)
		return append(out, tail...)
	}

	full := joined(rows)
	if height <= 0 || len(full) <= height {
		return full
	}
	if full = joined(dashCollapseClosed(rows, st)); len(full) <= height {
		return full
	}
	budget := height - len(head) - len(tail)
	if budget <= 0 {
		// A pane shorter than the headline and the tail keeps the headline
		// — attention rides in it — and lets the terminal scroll.
		if height < len(head) {
			return fitHead(head, holds, height)
		}
		return head
	}
	// Still too tall: keep what fits and count the rest. The count is
	// lines, not ticks, because everything below the fold is equally
	// unseen.
	avail := budget - 1
	mid := middle(dashCollapseClosed(rows, st))
	if avail > len(mid) {
		avail = len(mid)
	}
	out := append(append([]string{}, head...), mid[:avail]...)
	out = append(out, st.dim(fmt.Sprintf("+%d more", len(mid)-avail)))
	return append(out, tail...)
}

// fitHead keeps as much of the headline as the pane's height holds, whole
// attention blocks only: a hold whose clearing command is cut off by the
// fold is the defect the width wrap prevents (tick 9um), so what the frame
// shows of a hold is all of it, and a hold that does not fit is not shown
// at all. The fixed headline lines (identity, progress, phase bar) come
// first; only when they alone outgrow the pane does the cut slice into
// them, as it always did when no hold is in the frame.
func fitHead(head []string, holds [][]string, height int) []string {
	fixed := len(head)
	for _, block := range holds {
		fixed -= len(block)
	}
	if height <= fixed {
		return head[:height]
	}
	out := head[:fixed]
	room := height - fixed
	for _, block := range holds {
		if room < len(block) {
			break
		}
		out = append(out, block...)
		room -= len(block)
	}
	return out
}

// dashCollapseClosed folds one contiguous run of closed rows into a single
// dim line, in place: the collapsed line stands where the run's first row
// stood, so the rows around it never move relative to one another.
func dashCollapseClosed(rows []dashRow, st watchStyles) []dashRow {
	out := make([]dashRow, 0, len(rows))
	run := 0
	flush := func() {
		if run > 0 {
			out = append(out, dashRow{line: st.dim(fmt.Sprintf("✓ %d closed", run))})
			run = 0
		}
	}
	for _, r := range rows {
		if r.closed {
			run++
			continue
		}
		flush()
		out = append(out, r)
	}
	flush()
	return out
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

// shortModel is the model's own name for a person — the last segment of the
// id a gateway routes by — because the row is for glancing, and the full
// route string is what `status --json` is for.
func shortModel(model string) string {
	if rest, ok := strings.CutPrefix(model, "cloudflare-workers-ai/"); ok {
		model = rest
	}
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		model = model[idx+1:]
	}
	return model
}

// modelAge measures a stamp against the model's own `now`, so a renderer
// never does clock arithmetic against a wall it cannot see. False when
// either moment is missing or will not parse — an age nobody measured is an
// age nobody prints.
func modelAge(m statusmodel.Model, stamp string) (int64, bool) {
	if stamp == "" {
		return 0, false
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return 0, false
	}
	now, err := time.Parse(time.RFC3339, m.GeneratedAt)
	if err != nil {
		return 0, false
	}
	age := int64(now.Sub(at).Round(time.Second).Seconds())
	if age < 0 {
		return 0, false
	}
	return age, true
}

// watchPhaseName is the phase's name as a person reads it: the model's
// vocabulary is the contract (and stays ASCII on the wire), the frame says
// "close-out" the way the tick and the operator do.
func watchPhaseName(phase string) string {
	if phase == statusmodel.PhaseCloseout {
		return "close-out"
	}
	return phase
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

// watchPlural is the one-word plural the frame's counts read naturally with.
// A word ending in consonant+y pluralizes as "ies" — "remote retry" reads
// "remote retries", never the first-use bug's "remote retrys" — and one
// ending in "sh" as "es": "pushes", never "pushs".
func watchPlural(n int, what string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", what)
	}
	switch {
	case strings.HasSuffix(what, "sh"):
		return fmt.Sprintf("%d %ses", n, what)
	case strings.HasSuffix(what, "y") && len(what) >= 2 && !strings.ContainsAny(what[len(what)-2:len(what)-1], "aeiouy"):
		return fmt.Sprintf("%d %sies", n, what[:len(what)-1])
	}
	return fmt.Sprintf("%d %ss", n, what)
}
