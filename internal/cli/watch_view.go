package cli

// The renderer half of `ticfac watch` (tick 89m): one frame of the status
// model (ticfac.status.v1, tick 6dh), rendered for a person — attention
// first, then health, then the whole epic compressed by distance from now.
//
// The lessons this shape is taken from are named in the tick: Claude Code's
// todo list (the whole plan always visible; an item never moves, only its
// mark changes), gh run watch (refresh in place; compactness when tall) and
// BuildKit / docker compose (fixed rows with live timers; log lines above,
// status below) — and the failures it refuses: Turbo's full-screen TUI lost
// running tasks among finished ones and taught its users to fall back to
// streams, so this renderer writes NO alternate screen, assumes NO pane
// size, and keeps every row at a fixed position: a tick never moves, its
// mark changes.
//
// Everything here is a pure function of the model plus the pane's width and
// height: nothing reads a file, spawns a process or measures a host, so the
// content is pinned in watch_view_test.go and the wiring (the redraw loop,
// the keep lines, the exit codes) in watch_block_test.go.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// The silence grades: a worker's runner has been quiet for a while, and the
// timer a person glances at is already graded — plain while turns are
// happening, amber when the quiet has lasted long enough to look at (the
// stall warning's own neighbourhood: a worker thinking hard legitimately
// commits nothing for a while, so amber is a look, not an alarm), red when
// it has outlasted the wall clock's neighbourhood and something is probably
// wrong. Silence is a reason to look, never a verdict (tick b1t).
const (
	silenceAmberAfter = 10 * time.Minute
	silenceRedAfter   = 30 * time.Minute
)

// The pane widths at which columns drop — the whole point of "something
// simple that doesn't assume a pane size" (tick 89m): a narrow pane drops
// detail rather than wrapping rows into an unreadable tangle, and it drops
// it in a fixed order — the last turn first, the model next — so a row's
// identity never goes. Zero means unknown: everything shows.
const (
	watchShowLastTurnFrom = 100 // the last runner turn joins the row
	watchShowModelFrom    = 64  // the model name joins the tier
)

// watchStyles is the frame's small style set, injected so a test reads the
// words with the identity set and the colours with the ANSI one — the frame
// must be as assertable as it is readable. Four styles cover the frame:
// dim for what is far from now, amber for what wants a look, red for what
// is wrong, bold for what leads.
type watchStyles struct {
	dim   func(string) string
	amber func(string) string
	red   func(string) string
	bold  func(string) string
}

// ansiWatchStyles is the terminal's style set: plain ANSI, no colorprofile
// detection, because the live view is only ever drawn on a TTY (the pipe
// path streams plain lines instead), and these are the codes every terminal
// a watch runs on answers.
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
		bold:  wrap("1"),
	}
}

// renderWatchFrame renders one frame of the model for a pane width columns
// wide and height rows tall. Zero means unknown: no column drops, no
// truncation, no height limit — the caller that knows nothing draws
// everything and lets the terminal scroll.
//
// The sections, in the order the tick's questions are asked:
//
//   - attention, ONLY when a person is needed, naming the unblocking command
//   - the run's identity, liveness, health and degraded sources
//   - the lifecycle as the progress bar, with elapsed and cost
//   - the run's wait, with how long it has waited
//   - the epic: done waves one line each, the active wave one fixed row per
//     tick, upcoming waves one line each
func renderWatchFrame(m statusmodel.Model, st watchStyles, width, height int) []string {
	// The body, in the waves the tracker itself ordered — rows never
	// reorder — with the active wave's rows marked out so a short pane can
	// keep them first and count everything else.
	body := watchBody(m, st, width)
	running := watchRunning(m)

	head := append(watchAttentionLines(m, st), watchHealthLine(m, st))
	head = append(head, watchProgressLines(m, st, width)...)
	head = append(head, watchWaitLine(m, st))

	frame := fitWatchFrame(head, body, running, height, st)

	// Width: the columns were dropped while building the rows (above); what
	// is left is keeping every line inside the pane, however wide the
	// detail wanted to be.
	if width > 0 {
		for i, line := range frame {
			frame[i] = ansi.Truncate(line, width, "")
		}
	}
	return frame
}

// watchBodyLine is one line of the epic's body, marked by whether it is one
// of the ACTIVE wave's fixed rows (which a short pane keeps first) or one of
// the dim wave lines it counts instead, and carrying the name a more-line
// counts it under.
type watchBodyLine struct {
	line   string
	name   string
	active bool
}

// watchBody lays the epic out in the tracker's own wave order: done waves
// one line each, the active wave's header and one fixed row per tick,
// upcoming waves one line each. An absorbed tick is a marked new row; an
// unreadable tracker is said, never silently skipped.
func watchBody(m statusmodel.Model, st watchStyles, width int) []watchBodyLine {
	body := []watchBodyLine{}
	for _, wave := range wavesOf(m) {
		switch wave.State {
		case statusmodel.WaveActive:
			body = append(body, watchBodyLine{
				line:   st.amber(fmt.Sprintf("wave %d · active", wave.Wave)),
				active: true,
			})
			for _, tick := range wave.Ticks {
				body = append(body, watchBodyLine{line: watchTickRow(m, tick, st, width), active: true})
			}
		case statusmodel.WaveDone:
			body = append(body, watchBodyLine{
				line: st.dim(fmt.Sprintf("wave %d · done · %s: %s",
					wave.Wave, watchPlural(len(wave.Ticks), "tick"), watchTickIDs(wave.Ticks))),
				name: fmt.Sprintf("wave %d", wave.Wave),
			})
		default:
			body = append(body, watchBodyLine{
				line: st.dim(fmt.Sprintf("wave %d · %s: %s",
					wave.Wave, watchPlural(len(wave.Ticks), "tick"), watchTickIDs(wave.Ticks))),
				name: fmt.Sprintf("wave %d", wave.Wave),
			})
		}
	}
	if m.Waves == nil {
		// An unreadable tracker is a fact the frame must say, never a
		// silence a reader could mistake for "no waves".
		body = append(body, watchBodyLine{
			line: st.dim("the epic's shape could not be read (the tracker did not answer)"),
			name: "shape",
		})
	}
	return body
}

// wavesOf is the model's waves, nil's honest answer included: a null waves
// list and an empty one are different claims, and the renderer must not
// conflate them either.
func wavesOf(m statusmodel.Model) []statusmodel.Wave {
	if m.Waves == nil {
		return nil
	}
	return *m.Waves
}

// watchAttentionLines is the first question — does anything need me — and it
// is asked FIRST: one line per thing a person must do, each naming the one
// command that moves it on. Nothing needs a person and the section is empty:
// the frame leads with the run's identity instead.
func watchAttentionLines(m statusmodel.Model, st watchStyles) []string {
	lines := []string{}
	for _, a := range m.Attention {
		if !a.NeedsPerson {
			continue
		}
		line := fmt.Sprintf("! needs you: %s", a.What)
		if a.UnblockCommand != nil && *a.UnblockCommand != "" {
			line += " — " + st.bold("move it on: "+*a.UnblockCommand)
		}
		lines = append(lines, st.amber(line))
	}
	return lines
}

// watchHealthLine is the second question — is it healthy — on one line: the
// run's identity, its liveness, how long since its last word, the health
// counts the feed typed, and every source that could not be read, in red,
// because a renderer that silently skipped a degraded source would be a
// renderer that looked healthy while guessing.
func watchHealthLine(m statusmodel.Model, st watchStyles) string {
	life := "not alive"
	if m.Liveness.Alive {
		life = "alive"
	}
	line := fmt.Sprintf("epic %s · run %s · %s · %s", m.EpicID, m.RunID, m.Host, life)
	if m.Liveness.LastEventAgeSeconds != nil && *m.Liveness.LastEventAgeSeconds >= 0 {
		line += fmt.Sprintf(" · last word %s ago", humanDuration(*m.Liveness.LastEventAgeSeconds))
	}
	if h := m.Health; h != (statusmodel.Health{}) {
		var counts []string
		for _, c := range []struct {
			n    int
			what string
		}{
			{h.RemoteRetries, "remote retry"},
			{h.Interventions, "intervention"},
			{h.StallWarnings, "stall warning"},
			{h.WallClocksFired, "wall clock"},
		} {
			if c.n > 0 {
				counts = append(counts, watchPlural(c.n, c.what))
			}
		}
		line += " · " + strings.Join(counts, ", ")
	}
	if len(m.Degraded) > 0 {
		line += " · " + st.red("degraded: "+strings.Join(m.Degraded, ", "))
	}
	return line
}

// watchProgressLines is the third question — how far along — as the tick
// shaped it: per epic, the lifecycle as the progress bar (plan, the waves
// with their k/n, review, close-out, ci, merge), with the run's elapsed and
// its cost beside it. Phase states are the model's own vocabulary, never
// parsed prose; the elapsed is measured from the earliest dispatch the
// records state, the cost from the records' own usage — a number nobody
// measured is a number that lies.
//
// A pane the one line does not fit gets it as TWO — the bar alone, then the
// detail — rather than a bar whose cost was truncated away: elapsed and
// cost are facts the acceptance names, and a narrow pane drops COLUMNS,
// never the answers.
func watchProgressLines(m statusmodel.Model, st watchStyles, width int) []string {
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
		var segment string
		switch state {
		case statusmodel.PhaseStateDone:
			segment = "✓ " + name
		case statusmodel.PhaseStateActive:
			segment = st.bold("● " + name)
		default:
			segment = st.dim("○ " + name)
		}
		segments = append(segments, segment)
	}
	bar := strings.Join(segments, " ─ ")
	suffix := ""
	if m.Progress.Ticks != nil {
		suffix += fmt.Sprintf(" · %d/%d ticks", m.Progress.Ticks.Closed, m.Progress.Ticks.Total)
	}
	if elapsed, ok := watchRunElapsed(m); ok {
		suffix += " · elapsed " + humanDuration(elapsed)
	}
	suffix += fmt.Sprintf(" · cost $%.2f recorded (%s)", m.Cost.RecordedUSD, watchPlural(m.Cost.Attempts, "attempt"))

	one := bar + suffix
	if width == 0 || ansi.StringWidth(one) <= width {
		return []string{one}
	}
	return []string{bar, strings.TrimPrefix(suffix, " · ")}
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

// watchRunElapsed measures the whole run from the records the model carries:
// the earliest dispatch any tick's try history states. The second return
// says whether a moment was stated at all — "elapsed 0s" for a run with no
// readable dispatch would be a number nobody measured.
func watchRunElapsed(m statusmodel.Model) (int64, bool) {
	earliest := time.Time{}
	for _, wave := range wavesOf(m) {
		for _, tick := range wave.Ticks {
			for _, try := range tick.Tries {
				at, err := time.Parse(time.RFC3339, try.DispatchedAt)
				if err != nil {
					continue
				}
				if earliest.IsZero() || at.Before(earliest) {
					earliest = at
				}
			}
		}
	}
	if earliest.IsZero() {
		return 0, false
	}
	now, err := time.Parse(time.RFC3339, m.GeneratedAt)
	if err != nil || now.Before(earliest) {
		return 0, false
	}
	return int64(now.Sub(earliest).Round(time.Second).Seconds()), true
}

// watchWaitLine is the run's own header above its ticks: what it waits on,
// and since when. A wait a watcher watches (workers, CI) reads the same as
// a wait a person must move — the attention line above is what says which
// is which; this line says what the run is stopped on.
func watchWaitLine(m statusmodel.Model, st watchStyles) string {
	if m.WaitsOn == nil {
		return ""
	}
	line := fmt.Sprintf("waiting on %s: %s", m.WaitsOn.Kind, m.WaitsOn.What)
	if m.WaitsOn.Since != nil {
		if at, err := time.Parse(time.RFC3339, *m.WaitsOn.Since); err == nil {
			if now, err2 := time.Parse(time.RFC3339, m.GeneratedAt); err2 == nil && !now.Before(at) {
				line += fmt.Sprintf(" (since %s)", humanDuration(int64(now.Sub(at).Round(time.Second).Seconds())))
			}
		}
	}
	return st.dim(line)
}

// watchRunning is the tick ids whose attempts are live — the count a pane
// too short for the active rows collapses to.
func watchRunning(m statusmodel.Model) []string {
	running := []string{}
	for _, wave := range wavesOf(m) {
		for _, tick := range wave.Ticks {
			if tick.State == "dispatched" || tick.State == "reported" {
				running = append(running, tick.TickID)
			}
		}
	}
	return running
}

// watchTickIDs is one wave line's id list, in the tracker's own order.
func watchTickIDs(ticks []statusmodel.Tick) string {
	ids := make([]string, 0, len(ticks))
	for _, tick := range ticks {
		ids = append(ids, tick.TickID)
	}
	return strings.Join(ids, " ")
}

// watchTickRow is one fixed row of the active wave: the tick's identity, its
// state as the records state it, its whole try history as marks (a failed
// try then a running one), its elapsed, its worker's silence and last turn,
// and the tier and model working it. An absorbed tick is a marked new row —
// the epic's shape changed mid-run, and a renderer hides that from nobody.
func watchTickRow(m statusmodel.Model, tick statusmodel.Tick, st watchStyles, width int) string {
	id := tick.TickID
	if tick.Absorbed {
		id = "+" + id
	}
	row := fmt.Sprintf("  %s %s", id, tick.State)

	if len(tick.Tries) > 0 {
		marks := ""
		for _, try := range tick.Tries {
			switch try.Outcome {
			case statusmodel.TryClosed:
				marks += "+"
			case statusmodel.TryGateFailed, statusmodel.TryRejected:
				marks += st.red("x")
			case statusmodel.TryInFlight:
				marks += st.amber("*")
			case statusmodel.TryReported:
				marks += "r"
			default: // dispatched: out, but nothing is wrong with it
				marks += "o"
			}
		}
		row += " " + marks
	}
	if tick.ElapsedSeconds != nil {
		row += " elapsed " + humanDuration(*tick.ElapsedSeconds)
	}
	worker := watchWorkerOf(m, tick)
	if worker != nil && worker.SilenceSeconds != nil {
		quiet := "quiet " + humanDuration(*worker.SilenceSeconds)
		switch silenceGrade(*worker.SilenceSeconds) {
		case 1:
			quiet = st.amber(quiet)
		case 2:
			quiet = st.red(quiet)
		}
		row += " " + quiet
	}
	if tick.Tier != nil {
		if watchShowModel(width) && tick.Model != nil {
			row += fmt.Sprintf(" %s/%s", *tick.Tier, *tick.Model)
		} else {
			row += " " + *tick.Tier
		}
	}
	if worker != nil && watchShowLastTurn(width) && worker.LastTurn != nil {
		row += " · last: " + *worker.LastTurn
	}
	if tick.Title != "" && watchShowModel(width) && tick.State == "ready" {
		// A row that says nothing but "ready" is a row a person cannot
		// place; the title rides it when the pane can afford the words.
		row += " · " + tick.Title
	}
	return row
}

// watchShowLastTurn and watchShowModel are the pane's column drops, in the
// tick's fixed order: the last turn first, the model next. Zero is unknown
// and shows everything.
func watchShowLastTurn(width int) bool { return width == 0 || width >= watchShowLastTurnFrom }
func watchShowModel(width int) bool    { return width == 0 || width >= watchShowModelFrom }

// watchWorkerOf is the live worker row-mate for one tick, matched by the
// attempt identity both carry — never by order, and never guessed.
func watchWorkerOf(m statusmodel.Model, tick statusmodel.Tick) *statusmodel.Worker {
	workers := []statusmodel.Worker{}
	if m.Workers != nil {
		workers = *m.Workers
	}
	for i := range workers {
		if workers[i].TickID != tick.TickID {
			continue
		}
		if tick.Attempt != nil && workers[i].Attempt != *tick.Attempt {
			continue
		}
		return &workers[i]
	}
	return nil
}

// silenceGrade is the worker-silence timer's grade: 0 plain, 1 amber, 2 red.
func silenceGrade(seconds int64) int {
	switch d := time.Duration(seconds) * time.Second; {
	case d >= silenceRedAfter:
		return 2
	case d >= silenceAmberAfter:
		return 1
	default:
		return 0
	}
}

// fitWatchFrame fits the frame to the pane's height the way the tick
// describes: the whole epic when it fits; a pane too short for that keeps
// the active rows first and counts everything else as "+N more" (naming
// what was dropped); a pane too short even for the active rows collapses to
// "+N running"; and a pane too short for anything keeps the head, attention
// first, because that is the first question. Fitting never reorders the
// rows it keeps — it drops lines and says so.
func fitWatchFrame(head []string, body []watchBodyLine, running []string, height int, st watchStyles) []string {
	// The wait line is optional ("" when the run waits on nothing), and an
	// empty head line is not a row the pane owes space to.
	kept := make([]string, 0, len(head))
	for _, line := range head {
		if line != "" {
			kept = append(kept, line)
		}
	}
	head = kept

	if height <= 0 || len(head)+len(body) <= height {
		out := append([]string{}, head...)
		for _, b := range body {
			out = append(out, b.line)
		}
		return out
	}

	// The active rows, and the names of everything that is not one.
	var active, rest, restNames []string
	for _, b := range body {
		if b.active {
			active = append(active, b.line)
		} else {
			rest = append(rest, b.line)
			restNames = append(restNames, b.name)
		}
	}

	take := height - len(head)
	switch {
	case take >= len(active)+1:
		// The active rows fit with a line to count the rest under.
		out := append(append([]string{}, head...), active...)
		sort.Strings(restNames)
		return append(out, st.dim(fmt.Sprintf("+%d more (%s)", len(rest), strings.Join(restNames, ", "))))
	case take >= 2:
		// Too short for the active rows: collapse to one line that says
		// what is running, and nothing is reordered to do it.
		out := append([]string{}, head...)
		if len(running) > 0 {
			return append(out, fmt.Sprintf("+%d running (%s)", len(running), strings.Join(running, ", ")))
		}
		return append(out, fmt.Sprintf("+%d more", len(active)+len(rest)))
	default:
		// Too short even to collapse: the head, attention first, is the
		// whole frame.
		if height < len(head) {
			return head[:height]
		}
		return head
	}
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

// plural is the one-word plural the frame's counts read naturally with.
func watchPlural(n int, what string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", what)
	}
	return fmt.Sprintf("%d %ss", n, what)
}
