package cli

// The renderer half of `ticfac watch` (tick 89m): the live view renders the
// status model (ticfac.status.v1, tick 6dh) as one frame — attention first,
// then health, then the whole epic compressed by distance from now — and
// this file pins the frame's CONTENT. The wiring tests (watch_block_test.go)
// pin that the frame is redrawn in place; these pin what the frame says,
// because the frame is what a person actually reads.
//
// The renderer is a pure function of the model: nothing here reads a file,
// spawns a process or measures a host, so a renderer defect fails here
// without a live run, and a wiring defect cannot hide behind content.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// plainStyles is the identity style set: every line comes back exactly as it
// was built, so a content assertion reads the words and not the escape
// codes. The colour tests use ansiWatchStyles() instead.
func plainStyles() watchStyles {
	id := func(s string) string { return s }
	return watchStyles{dim: id, amber: id, red: id, bold: id}
}

// ptr is the one-line pointer helper the model fixtures lean on.
func ptr[T any](v T) *T { return &v }

// watchModelFixture is one mid-run epic as the model states it: wave 1 done
// (t1), wave 2 active with one dispatched tick (t2, try 2 in flight after a
// gate-failed try, a live worker with a two-minute silence) and one ready
// tick (t3), wave 3 upcoming (t4, t5). The run is alive, waits on its
// workers, and nothing needs a person.
func watchModelFixture() statusmodel.Model {
	t1a, t2a := 1, 2
	try := 1
	elapsed := int64(31 * 60)
	silence := int64(2 * 60)
	lastTurn := "assistant: read"
	tier, workerModel, executor := "strong", "@cf/zai-org/glm-5.3", "local-subprocess"
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "epic-rmod",
		EpicID:        "rmod",
		Host:          statusmodel.HostLocal,
		GeneratedAt:   "2026-09-27T05:00:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: true, State: "alive",
			LastEventAgeSeconds: ptr(int64(120)),
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseWaves,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateActive},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStatePending},
			},
			Wave: &statusmodel.WaveRef{Active: 2, Total: 3},
		},
		Progress: statusmodel.Progress{
			Ticks: &statusmodel.TickProgress{Total: 5, Closed: 2, Open: 3},
			Waves: &statusmodel.WaveProgress{Total: 3, Done: 1, Active: 2},
		},
		Waves: &[]statusmodel.Wave{
			{Wave: 1, State: statusmodel.WaveDone, Ticks: []statusmodel.Tick{{
				TickID: "t1", Title: "the first tick", State: "closed",
				Try: &try, Attempt: &t1a,
				Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
					DispatchedAt: "2026-09-27T02:00:00Z"}},
			}}},
			{Wave: 2, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{
				{
					TickID: "t2", Title: "the second tick", State: "dispatched",
					Try: &t2a, Attempt: &t2a,
					Tries: []statusmodel.Try{
						{Try: 1, Attempt: 1, Outcome: statusmodel.TryGateFailed,
							DispatchedAt: "2026-09-27T04:00:00Z"},
						{Try: 2, Attempt: 2, Outcome: statusmodel.TryInFlight,
							DispatchedAt: "2026-09-27T04:29:00Z"},
					},
					Tier: &tier, Model: &workerModel, Executor: &executor,
					ElapsedSeconds: &elapsed,
				},
				{TickID: "t3", Title: "the third tick", State: "ready"},
			}},
			{Wave: 3, State: statusmodel.WaveUpcoming, Ticks: []statusmodel.Tick{
				{TickID: "t4", Title: "the fourth tick", State: "ready"},
				{TickID: "t5", Title: "the fifth tick", State: "ready"},
			}},
		},
		Workers: &[]statusmodel.Worker{{
			TickID: "t2", Attempt: 2,
			SilenceSeconds: &silence, LastTurn: &lastTurn,
		}},
		WaitsOn: &statusmodel.Wait{
			Kind: statusmodel.WaitWorkers, What: "1 in-flight attempt(s)",
		},
		Attention: []statusmodel.Attention{},
		Cost:      statusmodel.Cost{RecordedUSD: 0.02, Attempts: 2, Basis: "usage recorded on decision records"},
	}
}

// TestTheFrameAnswersAttentionFirst: the first question — does anything need
// me — is the first line, only when the answer is yes, and it names the one
// command that moves the hold on. A frame with nothing to need a person
// starts with the run's identity instead.
func TestTheFrameAnswersAttentionFirst(t *testing.T) {
	t.Parallel()
	m := watchModelFixture()

	// Nothing needs a person: no attention line at all, and the frame leads
	// with the run's identity.
	plain := renderWatchFrame(m, plainStyles(), 0, 0)
	if len(plain) == 0 {
		t.Fatal("the frame is empty")
	}
	if strings.Contains(plain[0], "needs you") {
		t.Errorf("a run that needs nobody raised the attention line: %q", plain[0])
	}
	if !strings.Contains(plain[0], "epic rmod") || !strings.Contains(plain[0], "run epic-rmod") {
		t.Errorf("the frame's first line does not name the run: %q", plain[0])
	}

	// A hold only a person releases: first line, the what and the command.
	m.Attention = []statusmodel.Attention{{
		Kind:           statusmodel.WaitHeldForPerson,
		What:           "attempt_unaddressed: nobody can say whether the attempt is running",
		NeedsPerson:    true,
		UnblockCommand: ptr(`ticfac settle rmod t2 2 --release "<who>"`),
	}}
	frame := renderWatchFrame(m, plainStyles(), 0, 0)
	if !strings.Contains(frame[0], "needs you") ||
		!strings.Contains(frame[0], "attempt_unaddressed: nobody can say whether the attempt is running") ||
		!strings.Contains(frame[0], `ticfac settle rmod t2 2 --release "<who>"`) {
		t.Errorf("the attention line does not name what and the command:\n%s", strings.Join(frame, "\n"))
	}
	// Attention is amber, so a person glancing at a busy terminal sees it.
	coloured := renderWatchFrame(m, ansiWatchStyles(), 0, 0)
	if !strings.Contains(coloured[0], "\x1b[33m") {
		t.Errorf("the attention line is not amber:\n%q", coloured[0])
	}
}

// TestTheFrameRendersTheLifecycleAsAProgressBar: per epic, the lifecycle —
// plan, the waves with their k/n, review, close-out, ci, merge — as one
// progress bar, with the run's elapsed and its cost beside it.
func TestTheFrameRendersTheLifecycleAsAProgressBar(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(watchModelFixture(), plainStyles(), 0, 0)
	joined := strings.Join(frame, "\n")
	for _, want := range []string{
		"✓ plan", "● waves 2/3", "○ review", "○ close-out", "○ ci", "○ merge",
		"2/5 ticks", "elapsed 3h", "cost $0.02 recorded (2 attempts)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the progress bar does not carry %q:\n%s", want, joined)
		}
	}
}

// TestTheFrameCompressesTheEpicByDistance: the whole epic is always visible —
// done waves one line each, the active wave expanded to one fixed row per
// tick, upcoming waves one line each — and the order never changes: a tick
// that moves forward does so by its mark changing, not by its row moving.
func TestTheFrameCompressesTheEpicByDistance(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(watchModelFixture(), plainStyles(), 0, 0)
	var wave1, active, wave3 int
	var t2row, t3row, w1, w3 int
	for _, line := range frame {
		switch {
		case strings.Contains(line, "wave 1") && strings.Contains(line, "done"):
			wave1++
			w1 = len(line)
		case strings.Contains(line, "wave 2") && strings.Contains(line, "active"):
			active++
		case strings.Contains(line, "wave 3"):
			wave3++
			w3 = len(line)
		case strings.HasPrefix(line, "  t2"):
			t2row++
		case strings.HasPrefix(line, "  t3"):
			t3row++
		}
	}
	if wave1 != 1 || w1 == 0 {
		t.Errorf("a done wave is not one line each (saw %d): %v", wave1, frame)
	}
	if wave3 != 1 || w3 == 0 {
		t.Errorf("an upcoming wave is not one line each (saw %d): %v", wave3, frame)
	}
	if active != 1 || t2row != 1 || t3row != 1 {
		t.Errorf("the active wave is not one fixed row per tick (header %d, t2 %d, t3 %d):\n%s",
			active, t2row, t3row, strings.Join(frame, "\n"))
	}
	// Rows never reorder: wave 1's line stands before the active wave's
	// header, which stands before its rows, which stand before wave 3.
	orders := map[string]int{}
	for i, line := range frame {
		switch {
		case strings.Contains(line, "wave 1"):
			orders["wave1"] = i
		case strings.Contains(line, "wave 2"):
			orders["wave2"] = i
		case strings.HasPrefix(line, "  t2"):
			orders["t2"] = i
		case strings.Contains(line, "wave 3"):
			orders["wave3"] = i
		}
	}
	if !(orders["wave1"] < orders["wave2"] && orders["wave2"] < orders["t2"] && orders["t2"] < orders["wave3"]) {
		t.Errorf("the waves are not in the tracker's own order: %v\n%s", orders, strings.Join(frame, "\n"))
	}
	// A re-render of the same model puts every id on the same line index:
	// positions are stable, because a frame that jumps around cannot be
	// read at a glance.
	again := renderWatchFrame(watchModelFixture(), plainStyles(), 0, 0)
	if len(again) != len(frame) {
		t.Fatalf("two renders of one model differ in height: %d then %d", len(frame), len(again))
	}
	for i := range frame {
		if frame[i] != again[i] {
			t.Errorf("line %d moved between renders: %q then %q", i, frame[i], again[i])
		}
	}
}

// TestTheFrameShowsTryHistoryAsMarks: a tick's row carries its whole try
// history as marks — a failed try then a running one — and an absorbed tick
// appears as a marked new row, not a surprise.
func TestTheFrameShowsTryHistoryAsMarks(t *testing.T) {
	t.Parallel()
	m := watchModelFixture()
	frame := renderWatchFrame(m, plainStyles(), 0, 0)
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "dispatched x*") {
		t.Errorf("t2's row does not show a failed try then a running one:\n%s", joined)
	}
	// The failed mark is red and the running one amber: the history a
	// person glances at is colour-graded too.
	coloured := renderWatchFrame(m, ansiWatchStyles(), 0, 0)
	cJoined := strings.Join(coloured, "\n")
	if !strings.Contains(cJoined, "\x1b[31mx\x1b[0m") {
		t.Errorf("the failed try's mark is not red:\n%s", cJoined)
	}
	if !strings.Contains(cJoined, "\x1b[33m*\x1b[0m") {
		t.Errorf("the running try's mark is not amber:\n%s", cJoined)
	}

	// An absorbed tick: a marked new row.
	m = watchModelFixture()
	(*m.Waves)[1].Ticks[1].Absorbed = true
	(*m.Waves)[1].Ticks[1].State = "dispatched"
	frame = renderWatchFrame(m, plainStyles(), 0, 0)
	joined = strings.Join(frame, "\n")
	if !strings.Contains(joined, "+t3 ") {
		t.Errorf("an absorbed tick is not a marked new row:\n%s", joined)
	}
}

// TestTheFrameCarriesTheWorkerColumns: the active wave's row carries the
// state, the elapsed, the tier and model, and the worker's silence and last
// turn — the facts a person reads to answer "what is happening now".
func TestTheFrameCarriesTheWorkerColumns(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(watchModelFixture(), plainStyles(), 0, 0)
	joined := strings.Join(frame, "\n")
	for _, want := range []string{
		"elapsed 31m", "quiet 2m", "strong/@cf/zai-org/glm-5.3", "last: assistant: read",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the active row does not carry %q:\n%s", want, joined)
		}
	}
}

// TestTheFrameGradesSilenceAmberThenRed: a worker's silence is a signal —
// plain while the runner is taking turns, amber when it has been quiet a
// while, red when it has been quiet too long. A number a person must
// interpret is a number the frame already interpreted.
func TestTheFrameGradesSilenceAmberThenRed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		silence int64
		grade   int
	}{
		{silence: 5 * 60, grade: 0},
		{silence: 10 * 60, grade: 1},
		{silence: 29 * 60, grade: 1},
		{silence: 30 * 60, grade: 2},
		{silence: 45 * 60, grade: 2},
	} {
		if got := silenceGrade(tc.silence); got != tc.grade {
			t.Errorf("silenceGrade(%ds) = %d, want %d", tc.silence, got, tc.grade)
		}
	}

	render := func(silence int64, st watchStyles) string {
		m := watchModelFixture()
		(*m.Workers)[0].SilenceSeconds = &silence
		return strings.Join(renderWatchFrame(m, st, 0, 0), "\n")
	}
	if got := render(5*60, ansiWatchStyles()); strings.Contains(got, "quiet 5m\x1b[0m") {
		t.Errorf("a five-minute silence is graded, want plain:\n%s", got)
	}
	if got := render(5*60, plainStyles()); !strings.Contains(got, "quiet 5m") {
		t.Errorf("the silence is not shown at all:\n%s", got)
	}
	if got := render(15*60, ansiWatchStyles()); !strings.Contains(got, "\x1b[33mquiet 15m\x1b[0m") {
		t.Errorf("a fifteen-minute silence is not amber:\n%s", got)
	}
	if got := render(45*60, ansiWatchStyles()); !strings.Contains(got, "\x1b[31mquiet 45m\x1b[0m") {
		t.Errorf("a forty-five-minute silence is not red:\n%s", got)
	}
}

// TestTheFrameDropsColumnsWhenNarrow: the pane's width decides the columns —
// the last turn goes first, the model next — so a narrow pane drops detail
// rather than wrapping into an unreadable tangle.
func TestTheFrameDropsColumnsWhenNarrow(t *testing.T) {
	t.Parallel()
	wide := strings.Join(renderWatchFrame(watchModelFixture(), plainStyles(), 120, 0), "\n")
	mid := strings.Join(renderWatchFrame(watchModelFixture(), plainStyles(), 90, 0), "\n")
	narrow := strings.Join(renderWatchFrame(watchModelFixture(), plainStyles(), 60, 0), "\n")

	if !strings.Contains(wide, "last: assistant: read") || !strings.Contains(wide, "@cf/zai-org/glm-5.3") {
		t.Errorf("a wide pane does not show every column:\n%s", wide)
	}
	if strings.Contains(mid, "last: assistant: read") {
		t.Errorf("a 90-column pane still shows the last turn:\n%s", mid)
	}
	if !strings.Contains(mid, "@cf/zai-org/glm-5.3") {
		t.Errorf("a 90-column pane dropped the model too early:\n%s", mid)
	}
	if strings.Contains(narrow, "@cf/zai-org/glm-5.3") {
		t.Errorf("a 60-column pane still shows the model:\n%s", narrow)
	}
	if !strings.Contains(narrow, "strong") {
		t.Errorf("a 60-column pane dropped the tier as well as the model:\n%s", narrow)
	}
	// Whatever the width, the identity of a row never goes: a pane that
	// cannot say WHICH tick is running says nothing.
	for _, width := range []int{90, 60, 40} {
		joined := strings.Join(renderWatchFrame(watchModelFixture(), plainStyles(), width, 0), "\n")
		if !strings.Contains(joined, "t2") {
			t.Errorf("a %d-column frame does not name the running tick:\n%s", width, joined)
		}
	}
}

// TestTheFrameReflowsTheBarWhenNarrow: a pane the one progress line does
// not fit gets the bar alone with the detail under it, rather than a bar
// whose cost was truncated away — elapsed and cost are answers, not
// columns, and a narrow pane drops detail it can reflow, never answers it
// was asked for.
func TestTheFrameReflowsTheBarWhenNarrow(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(watchModelFixture(), plainStyles(), 100, 0)
	var bar, detail string
	for _, line := range frame {
		switch {
		case strings.Contains(line, "✓ plan"):
			bar = line
		case strings.Contains(line, "cost $0.02"):
			detail = line
		}
	}
	if bar == "" {
		t.Fatalf("the bar is missing from the frame:\n%s", strings.Join(frame, "\n"))
	}
	if strings.Contains(bar, "cost") {
		t.Errorf("the bar line still carries the cost at a width it does not fit:\n%s", bar)
	}
	if detail == "" || !strings.Contains(detail, "elapsed 3h") || !strings.Contains(detail, "cost $0.02 recorded (2 attempts)") {
		t.Errorf("the detail line does not carry elapsed and cost:\n%s", strings.Join(frame, "\n"))
	}
}

// TestTheFrameTruncatesToThePane: no line is wider than the pane — width is
// honest even after styling, so a frame never wraps into the rows below it.
func TestTheFrameTruncatesToThePane(t *testing.T) {
	t.Parallel()
	for _, width := range []int{80, 40, 20} {
		frame := renderWatchFrame(watchModelFixture(), ansiWatchStyles(), width, 0)
		for i, line := range frame {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("line %d is %d cells wide in a %d-column pane: %q", i, got, width, line)
			}
		}
	}
}

// TestTheFrameFitsThePaneHeight: more rows than the pane is tall keeps the
// active rows first and says "+N more"; a short pane collapses to "+N
// running"; attention always survives, because it is the first question.
func TestTheFrameFitsThePaneHeight(t *testing.T) {
	t.Parallel()
	m := watchModelFixture()
	full := renderWatchFrame(m, plainStyles(), 0, 0)

	// No height known (0): everything, the whole epic.
	if len(renderWatchFrame(m, plainStyles(), 0, 0)) != len(full) {
		t.Error("an unknown height changed the frame")
	}
	// Tall enough: everything, no more-line.
	if got := renderWatchFrame(m, plainStyles(), 0, len(full)+3); len(got) != len(full) || strings.Contains(strings.Join(got, "\n"), "+") {
		t.Errorf("a tall pane did not show the whole epic:\n%s", strings.Join(got, "\n"))
	}

	// One line short: active rows kept, the dropped waves counted.
	short := renderWatchFrame(m, plainStyles(), 0, len(full)-1)
	joined := strings.Join(short, "\n")
	if !strings.Contains(joined, "  t2") || !strings.Contains(joined, "  t3") {
		t.Errorf("a short pane dropped an active row:\n%s", joined)
	}
	if !strings.Contains(joined, "+2 more (wave 1, wave 3)") {
		t.Errorf("a short pane does not count what it dropped:\n%s", joined)
	}
	if len(short) != len(full)-1 {
		t.Errorf("a pane %d tall rendered %d lines", len(full)-1, len(short))
	}

	// Short: collapse to "+N running".
	collapsed := renderWatchFrame(m, plainStyles(), 0, 5)
	joined = strings.Join(collapsed, "\n")
	if !strings.Contains(joined, "+1 running") || !strings.Contains(joined, "t2") {
		t.Errorf("a short pane did not collapse to +N running:\n%s", joined)
	}

	// Attention survives every cut.
	m.Attention = []statusmodel.Attention{{
		Kind: statusmodel.WaitHeldForPerson, What: "the run holds t2 for a person",
		NeedsPerson:    true,
		UnblockCommand: ptr(`ticfac settle rmod t2 2 --release "<who>"`),
	}}
	attention := strings.Join(renderWatchFrame(m, plainStyles(), 0, 2), "\n")
	if !strings.Contains(attention, "needs you") {
		t.Errorf("a two-line pane dropped the attention line:\n%s", attention)
	}
}

// TestTheFrameSaysWhatTheRunWaitsOn: under the header, one line names the
// run's wait and how long it has waited — the "what the run waits on and
// since when" a person reads first.
func TestTheFrameSaysWhatTheRunWaitsOn(t *testing.T) {
	t.Parallel()
	m := watchModelFixture()
	m.WaitsOn = &statusmodel.Wait{
		Kind:           statusmodel.WaitHeldForPerson,
		What:           "attempt 2 of t2 struck out: the refusal the run recorded",
		Since:          ptr("2026-09-27T04:45:00Z"),
		NeedsPerson:    true,
		UnblockCommand: ptr(`ticfac settle rmod t2 2 --release "<who>"`),
	}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0), "\n")
	if !strings.Contains(joined, "waiting on held-for-person: attempt 2 of t2 struck out") {
		t.Errorf("the wait line does not name the wait:\n%s", joined)
	}
	if !strings.Contains(joined, "since 15m") {
		t.Errorf("the wait line does not say for how long:\n%s", joined)
	}
}

// TestTheFrameNamesDegradedSources: a source that could not be read is said,
// on the frame, in red — a renderer that silently skipped it would be a
// renderer that looked healthy while guessing.
func TestTheFrameNamesDegradedSources(t *testing.T) {
	t.Parallel()
	m := watchModelFixture()
	m.Degraded = []string{"tracker"}
	m.Waves = nil
	m.Progress = statusmodel.Progress{}
	m.Lifecycle = statusmodel.Lifecycle{Phase: statusmodel.PhaseWaves}
	joined := strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0), "\n")
	if !strings.Contains(joined, "\x1b[31mdegraded: tracker\x1b[0m") {
		t.Errorf("a degraded tracker is not said in red:\n%s", joined)
	}
	if !strings.Contains(joined, "could not be read") {
		t.Errorf("an unreadable epic shape shows nothing at all:\n%s", joined)
	}
}

// TestHumanDurationRoundsForAPerson: the frame's timers are for glancing,
// so they read like a person reads a clock.
func TestHumanDurationRoundsForAPerson(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{45, "45s"},
		{59, "59s"},
		{60, "1m"},
		{31 * 60, "31m"},
		{3600, "1h"},
		{3 * 3600, "3h"},
		{3*3600 + 60, "3h1m"},
		{25 * 3600, "1d1h"},
	} {
		if got := humanDuration(tc.seconds); got != tc.want {
			t.Errorf("humanDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}
