package cli

// The drill-in views (epic hn6, wave 4 — tick c2u), pinned headless: the
// tick view renders one tick's own story out of the status model, the feed
// view renders the whole event feed as a scrollable window, and the refusal
// line names its reason and the run's next step. No pty and no herdr —
// identity styles at a fixed width, the contract bundle's `dashboard` golden
// for the model.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// TestTickViewShowsTriesReasonsAndEvidence: enter on a tick opens its own
// story, out of the contract golden's model — the refused try's reason, the
// run's next step, the report summary and its diff stats, the gate evidence
// on the tick's heads, and the findings it drafted.
func TestTickViewShowsTriesReasonsAndEvidence(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	// The golden's last try of 46x stands in flight and states no next step
	// (the model derives one only behind a refused last try); the run's own
	// next-step word is the one field the test shapes, so the "— next:"
	// rendering is pinned on the golden's own tick.
	if tick := watchTickOf(m, "46x"); tick == nil || len(tick.Tries) == 0 {
		t.Fatal("the dashboard golden carries no 46x tries to drill into")
	} else {
		tick.Tries[len(tick.Tries)-1].NextStep = ptr("retrying (try 3)")
	}

	view := strings.Join(renderTickView(m, "46x", plainStyles(), 100, 0), "\n")
	for _, want := range []string{
		"46x  port sandbox verbs to ticfac",
		"●gate", // the pipeline cell, the same one the dashboard renders
		"try 1", "strong", "gate-failed",
		"attempt 4 of 46x struck out: gofmt drifted in two files",
		"try 2", "frontier", "in-flight",
		"— next: retrying (try 3)",
		"report not read",
		"go  fail  4d8c0e9a",
		"[esc] back",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the tick view does not carry %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "findings:") {
		t.Errorf("the tick view renders a findings section for a tick that drafted none:\n%s", view)
	}

	// 060's story: its report, its gate, its finding.
	view = strings.Join(renderTickView(m, "060", plainStyles(), 100, 0), "\n")
	for _, want := range []string{
		"060  cloud worker early-exit nudge",
		"try 1", "closed",
		"re-prompt wired; claude runs with background tasks off",
		"diff: 11 files +214 −60",
		"go  pass  1a2b3c4d",
		"efdc8f82", // the finding's durable key, the word triage addresses
		"gating",
		"the boot strap leaves the re-prompt off for herdr panes",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the tick view does not carry %q:\n%s", want, view)
		}
	}

	// A tick the plan no longer carries is said, never rendered as a wall of
	// nothing: the cursor can outlive a replan by one key.
	view = strings.Join(renderTickView(m, "zzz", plainStyles(), 100, 0), "\n")
	if !strings.Contains(view, "zzz is not in the epic's plan any more") {
		t.Errorf("a tick gone from the plan renders as something else:\n%s", view)
	}
}

// TestTickViewDrillsIntoAClosedPriorRunTick (tick ihw): enter on a tick the
// NEWEST run never touched — its row comes from the prior run that closed
// it — still tells the whole story: the report summary read from that run's
// own archived report (the reader is keyed by run, tick and attempt), the
// diff read from the merge commit (the close swept the attempt branch),
// and the gate rows that run's own evidence recorded. This is the drill-in
// of a closed prior-run tick through the production reader — the case the
// report reader bound to the current run id answered "report not read"
// for, or another dispatch's report when the numbers collided.
func TestTickViewDrillsIntoAClosedPriorRunTick(t *testing.T) {
	// The repository a closed tick leaves behind: the attempt branch was
	// merged into the integration branch and swept on close, and the run's
	// own tag runstate places at terminal state is what still carries the
	// merge commit for a reader keyed by the prior run's id.
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "config", "user.email", "fixture@example.com")
	gitIn(t, repo, "config", "user.name", "the fixture")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the base the run branched from")
	gitIn(t, repo, "branch", "epic/hpd")
	branch := "ticfac/run-run_aaa/tick-at1/attempt-1"
	gitIn(t, repo, "checkout", "-q", "-b", branch)
	for _, file := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(repo, file), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the attempt's work")
	gitIn(t, repo, "checkout", "-q", "epic/hpd")
	gitIn(t, repo, "merge", "--no-ff", "-q", "--no-edit", "-m",
		"Merge branch '"+branch+"' into epic/hpd\n\nticfac run run_aaa: tick at1 attempt 1", branch)
	gitIn(t, repo, "branch", "-q", "-D", branch)
	gitIn(t, repo, "tag", "ticfac/run-run_aaa")

	// The archived report under the PRIOR run's id — the only run it is
	// ever filed under.
	stateRoot := t.TempDir()
	t.Setenv("TICFAC_EXEC_STATE_DIR", stateRoot)
	dir := filepath.Join(stateRoot, "runs", "run_aaa", "at1", "1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attempt.json"), []byte(`{"tick_id": "at1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"),
		[]byte("# RESULT-at1\n\nThe prior run wired the seam and closed the tick.\n\nSTATUS: DONE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The prior run's durable records: the closed row, the dispatch marker
	// and the gate evidence its own close ran on.
	tick, attempt, executor, tier := "at1", 1, "local-subprocess", "strong"
	prior := statusmodel.Records{
		Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion,
			RunID:         "run_aaa",
			EpicID:        "hpd",
			Sequence:      4,
			State:         "completed",
			Reason:        "at1 closed",
			UpdatedAt:     "2026-09-26T12:10:00Z",
			Ticks:         []runstate.TickState{{TickID: "at1", State: "closed", Attempt: 1}},
		},
		Attempts: []runstate.Attempt{{
			SchemaVersion: runstate.SchemaVersion,
			Attempt:       1,
			TickID:        "at1",
			DispatchedAt:  "2026-09-26T10:00:00Z",
			JobHandle:     map[string]any{"executor": executor},
			Provenance: runstate.Provenance{
				RunID: "run_aaa", TickID: &tick, Attempt: &attempt,
				SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
				Phase:     runstate.PhaseWorker, Executor: &executor, Tier: &tier,
			},
		}},
		Evidence: []runstate.Evidence{{
			SchemaVersion: runstate.SchemaVersion,
			Key:           "gate-at1-1-integrated-go",
			Provenance: runstate.Provenance{
				RunID: "run_aaa", TickID: &tick, Attempt: &attempt,
				SourceRef: "refs/heads/epic/hpd",
				SourceSHA: "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b",
				Phase:     runstate.PhaseIntegrated, Executor: &executor, Tier: &tier,
			},
			Check:      runstate.Check{ID: "go", Kind: "command"},
			StartedAt:  "2026-09-26T11:30:00Z",
			FinishedAt: "2026-09-26T11:55:00Z",
			Result:     "pass",
			Acceptance: "required",
			Output: runstate.Output{Inline: &runstate.InlineOutput{
				Mode: "inline", Stdout: "ok\n", Truncated: false, Redacted: true, MaxBytes: 1024,
			}},
		}},
	}

	model := statusmodel.Build(statusmodel.Sources{
		Now:   time.Date(2026, 9, 27, 5, 30, 0, 0, time.UTC),
		RunID: "run_ccc", Host: statusmodel.HostLocal,
		Graph: &tk.Graph{
			Epic: tk.GraphEpic{ID: "hpd", Title: "the epic the runs worked"},
			Waves: []tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "at1", Title: "the prior run's tick", Gloss: "prior", Status: "closed"},
			}}},
		},
		Records:      &statusmodel.Records{},
		PriorRecords: []statusmodel.Records{prior},
		StandingRead: true,
		Report:       statusmodel.AttemptReports(repo),
	})

	view := strings.Join(renderTickView(model, "at1", plainStyles(), 100, 0), "\n")
	for _, want := range []string{
		"at1  prior",
		"try 1", "closed",
		"The prior run wired the seam and closed the tick.",
		"diff: 2 files +10 −0",
		"go  pass  1a2b3c4d",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the closed prior-run tick's drill-in does not carry %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "report not read") {
		t.Errorf("the closed prior-run tick's report was not read:\n%s", view)
	}
}

// TestTickViewSaysReportNotRead: a tick whose report was not read says so —
// "report not read" is a claim about the reading, not a silence a person
// reads as "the report said nothing".
func TestTickViewSaysReportNotRead(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	view := strings.Join(renderTickView(m, "46x", plainStyles(), 100, 0), "\n")
	if !strings.Contains(view, "report not read") {
		t.Errorf("a tick with no read report does not say so:\n%s", view)
	}
	if strings.Contains(view, "diff:") {
		t.Errorf("a tick with no read report still shows diff stats:\n%s", view)
	}

	// A height too small keeps the footer and counts the rest, never
	// silently dropping lines.
	tall := renderTickView(m, "46x", plainStyles(), 100, 3)
	if len(tall) != 3 || tall[len(tall)-1] != "[esc] back" {
		t.Errorf("a 3-line pane did not keep the footer and count the rest:\n%s", strings.Join(tall, "\n"))
	}
}

// TestTickViewFitsThePane (tick aro): every line the tick drill-in renders
// fits the pane's width, the way renderWatchFrame and renderFeedView already
// cut their lines — a report summary, try reason or finding title written to
// the terminal unbounded wraps inside a real pane, pushes the block down rows
// the redraw's cursor arithmetic does not know about, and the next frame
// draws over the block's own rows. Width 0 stays the unknown-width contract:
// everything, unbounded, for the caller that knows nothing.
func TestTickViewFitsThePane(t *testing.T) {
	t.Parallel()
	const wide = 200
	summary := strings.Repeat("s", wide)
	reason := strings.Repeat("r", wide)
	next := strings.Repeat("n", wide)
	title := strings.Repeat("t", wide)
	m := dashboardFixture()
	if tick := watchTickOf(m, "t2"); tick == nil || len(tick.Tries) < 2 {
		t.Fatal("the dashboard fixture carries no t2 tries to drill into")
	} else {
		tick.Gloss = title
		tick.Tries[1].Reason = ptr(reason)
		tick.Tries[1].NextStep = ptr(next)
		tick.Report = &statusmodel.TickReport{
			Summary: ptr(summary),
			Diff:    &statusmodel.ReportDiff{Files: 3, Insertions: 40, Deletions: 7},
		}
		tick.Findings = []statusmodel.TickFinding{{Key: "efdc8f82", Title: title, Gating: ptr(true)}}
	}

	// The defect's own shape: a 200-cell report summary must not render as a
	// 200-cell line in an 80-column pane — nor any other line over its pane,
	// at either width the finding's family fixes for the drill view.
	for _, width := range []int{80, 47} {
		for i, line := range renderTickView(m, "t2", plainStyles(), width, 0) {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d: the tick view's line %d is %d cells wide: %q", width, i, got, line)
			}
		}
		// The seam is the content builder: any caller holding its width gets
		// a content that fits, footer or not.
		for i, line := range renderTickContent(m, "t2", plainStyles(), width) {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d: the tick content's line %d is %d cells wide: %q", width, i, got, line)
			}
		}
	}

	// The cut keeps the line's start — the part a person scans for — the
	// same way the dashboard's final pass and the feed view cut. The 200-cell
	// summary at an 80-column pane shows its first 80 cells, not a paraphrase.
	view := strings.Join(renderTickView(m, "t2", plainStyles(), 80, 0), "\n")
	if !strings.Contains(view, strings.Repeat("s", 80)) {
		t.Errorf("the report summary lost its start in the cut:\n%s", view)
	}

	// The styles ride along: a styled line is cut by display width, and the
	// escapes neither widen the line nor survive the cut unbalanced.
	for i, line := range renderTickView(m, "t2", ansiWatchStyles(), 47, 0) {
		if got := ansi.StringWidth(line); got > 47 {
			t.Errorf("width 47: the styled tick view's line %d is %d cells wide: %q", i, got, line)
		}
	}

	// Width 0 is the unknown-width contract: everything, unbounded — the
	// caller that knows nothing draws it all and lets the terminal scroll.
	// The summary and the finding title are lines of their own, so they are
	// pinned whole; the reason and next step ride inside a try line.
	content0 := renderTickContent(m, "t2", plainStyles(), 0)
	full := strings.Join(content0, "\n")
	for _, want := range []string{summary, "  " + title} {
		if !slices.Contains(content0, want) {
			t.Errorf("an unknown width truncated the %d-cell line %q:\n%s", wide, want, full)
		}
	}
	if !strings.Contains(full, reason) || !strings.Contains(full, next) {
		t.Errorf("an unknown width truncated the try's reason or next step:\n%s", full)
	}

	// The padding a try line carries for its columns is not drawn out to the
	// edge: a line with nothing after its cells ends at its last cell.
	plain := renderTickContent(m, "t3", plainStyles(), 80)
	for i, line := range plain {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("the tick content's line %d carries trailing padding: %q", i, line)
		}
	}
}

// TestFeedViewScrolls: the full feed opens newest at the bottom and scrolls
// up in whole events — the window of 30 events at height 10 scrolled by 5 is
// the 16th through 25th oldest, nothing older and nothing newer in it.
func TestFeedViewScrolls(t *testing.T) {
	t.Parallel()
	events := make([]runfeed.Event, 0, 30)
	at := time.Date(2026, 9, 28, 19, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		n := i + 1
		events = append(events, runfeed.NewEvent(at.Add(time.Duration(i)*time.Minute), "epic-rmod",
			"t1", &n, reconcile.StageDispatched, fmt.Sprintf("event %d", n)))
	}
	var tries runfeed.Tries
	for _, event := range events {
		tries.Observe(event)
	}

	view := renderFeedView(events, &tries, nil, 5, 100, 10)
	joined := strings.Join(view, "\n")
	if len(view) != 10 {
		t.Errorf("a 10-line window rendered %d lines:\n%s", len(view), joined)
	}
	for _, want := range []string{"event 16", "event 25", "t1#16"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the scrolled window does not carry %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"event 15", "event 26"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("the scrolled window shows %q, outside its 10:\n%s", unwanted, joined)
		}
	}

	// Scroll 0 is the newest end.
	view = renderFeedView(events, &tries, nil, 0, 100, 10)
	joined = strings.Join(view, "\n")
	if !strings.Contains(joined, "event 30") || strings.Contains(joined, "event 20") {
		t.Errorf("an unscrolled feed is not the newest end:\n%s", joined)
	}

	// An unknown height is the whole feed, and a feed shorter than the
	// window is all of it, without padding.
	if got := len(renderFeedView(events, &tries, nil, 0, 100, 0)); got != 30 {
		t.Errorf("an unknown height rendered %d of the feed's 30 lines", got)
	}
	if got := len(renderFeedView(events[:4], &tries, nil, 0, 100, 10)); got != 4 {
		t.Errorf("a feed shorter than the window rendered %d of its 4 lines", got)
	}
}

// TestRejectedEventLineNamesReasonAndNextStep: the first-use bug ("v7z
// rejected x shows no reason and no next step"), pinned. Where the model is
// at hand, a refusal's line names its reason — the run's own detail, cut to
// its first clause — and the run's own next step from the tick's last try;
// without the model, the reason still shows and no next step is invented;
// and the old full-detail line form is unchanged byte for byte.
func TestRejectedEventLineNamesReasonAndNextStep(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	if tick := watchTickOf(m, "t2"); tick == nil || len(tick.Tries) < 2 {
		t.Fatal("the dashboard fixture carries no t2 tries to refuse")
	} else {
		tick.Tries[1].Outcome = statusmodel.TryRejected
		tick.Tries[1].Reason = ptr("no-commits: attempt 2 of t2 left no branch")
		tick.Tries[1].NextStep = ptr("the run will retry or escalate the tier")
	}
	at := time.Date(2026, 9, 28, 19, 4, 0, 0, time.UTC)
	first, second := 1, 2
	dispatched := runfeed.NewEvent(at, "epic-rmod", "t2", &first,
		reconcile.StageDispatched, "t2 try 1 dispatched")
	rejected := runfeed.NewEvent(at.Add(time.Minute), "epic-rmod", "t2", &second,
		reconcile.StageRejected, "no-commits: attempt 2 of t2 left no branch; the work is released")
	var tries runfeed.Tries
	tries.Observe(dispatched)
	tries.Observe(rejected)

	line := watchEventLineWith(rejected, &tries, &m)
	for _, want := range []string{"t2#2", "rejected: no-commits", "— next: the run will retry or escalate the tier"} {
		if !strings.Contains(line, want) {
			t.Errorf("the refused line does not name %q: %q", want, line)
		}
	}
	if strings.Contains(line, "left no branch; the work is released") {
		t.Errorf("the refused line carries the whole detail instead of its first clause: %q", line)
	}

	// No model at hand: the reason still shows, no next step is invented.
	line = watchEventLineWith(rejected, &tries, nil)
	if !strings.Contains(line, "rejected: no-commits") {
		t.Errorf("the refused line lost its reason without the model: %q", line)
	}
	if strings.Contains(line, "— next:") {
		t.Errorf("a next step was invented without the model: %q", line)
	}

	// A gate failure names its reason the same way.
	gateFailed := runfeed.NewEvent(at.Add(2*time.Minute), "epic-rmod", "t2", &second,
		reconcile.StageGateFailed, "the integrated gate did not pass: go test failed")
	line = watchEventLineWith(gateFailed, &tries, &m)
	if !strings.Contains(line, "gate_failed: the integrated gate did not pass") {
		t.Errorf("the gate-failure line does not name its reason: %q", line)
	}

	// Anything else is the old line, byte for byte — the stream path's words.
	if got, want := watchEventLineWith(dispatched, &tries, &m), watchEventLine(dispatched, &tries); got != want {
		t.Errorf("a non-refusal line changed form with the model at hand:\n%q\nwant %q", got, want)
	}
	if got := watchEventLine(rejected, &tries); !strings.Contains(got, "no-commits: attempt 2 of t2 left no branch; the work is released") {
		t.Errorf("the old full-detail line form changed: %q", got)
	}
}

// TestRefusalLineNextStepIsTheEventsOwnTrys (tick 15b): the "— next:" a
// refusal line carries is the next step of the event's OWN try, keyed on the
// event's attempt — try 1's rejection shows try 1's next step, never try 2's.
// An event that names no attempt, or one the tick's tries do not carry,
// invents no next step at all.
func TestRefusalLineNextStepIsTheEventsOwnTrys(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	tick := watchTickOf(m, "t2")
	if tick == nil || len(tick.Tries) < 2 {
		t.Fatal("the dashboard fixture carries no t2 tries to refuse")
	}
	// Both tries carry a next step of their own, so a line that borrows the
	// wrong try's is caught by the words it shows, not by a silence.
	tick.Tries[0].NextStep = ptr("try 1's own next step")
	tick.Tries[1].NextStep = ptr("try 2's own next step")

	at := time.Date(2026, 9, 28, 19, 4, 0, 0, time.UTC)
	first, second, unseen := 1, 2, 7
	events := []runfeed.Event{
		runfeed.NewEvent(at, "epic-rmod", "t2", &first,
			reconcile.StageRejected, "gofmt drifted in two files"),
		runfeed.NewEvent(at.Add(time.Minute), "epic-rmod", "t2", &second,
			reconcile.StageRejected, "no-commits: attempt 2 of t2 left no branch"),
		runfeed.NewEvent(at.Add(2*time.Minute), "epic-rmod", "t2", nil,
			reconcile.StageRejected, "a refusal that names no attempt"),
		runfeed.NewEvent(at.Add(3*time.Minute), "epic-rmod", "t2", &unseen,
			reconcile.StageGateFailed, "the integrated gate did not pass: go"),
	}
	var tries runfeed.Tries
	for _, event := range events {
		tries.Observe(event)
	}

	// The line form: each refusal carries its own try's next step, and an
	// attempt nobody's try states — nil, or one the tick never saw — carries
	// none rather than the last try's.
	for i, tc := range []struct {
		event runfeed.Event
		want  string
		none  bool
	}{
		{events[0], "— next: try 1's own next step", false},
		{events[1], "— next: try 2's own next step", false},
		{events[2], "", true},
		{events[3], "", true},
	} {
		line := watchEventLineWith(tc.event, &tries, &m)
		switch {
		case tc.none && strings.Contains(line, "— next:"):
			t.Errorf("line %d invents a next step for an attempt no try states: %q", i, line)
		case !tc.none && !strings.Contains(line, tc.want):
			t.Errorf("line %d does not carry its own try's next step %q: %q", i, tc.want, line)
		}
		if strings.Contains(line, "try 2's own next step") && tc.event != events[1] {
			t.Errorf("line %d borrows try 2's next step: %q", i, line)
		}
	}

	// The feed view, the surface the finding named: an older refusal line in
	// the rendered feed carries its own try's next step, not the newest one's.
	view := strings.Join(renderFeedView(events, &tries, &m, 0, 200, 0), "\n")
	if !strings.Contains(view, "— next: try 1's own next step") {
		t.Errorf("the feed view's try-1 refusal lost its own next step:\n%s", view)
	}
	if strings.Count(view, "try 2's own next step") != 1 {
		t.Errorf("the feed view's try-2 next step appears anywhere but its own line:\n%s", view)
	}
}

// TestWatchEventReasonIsTheFirstClause: the clause the refusal line names is
// the detail up to the first ":" — or its first 120 characters, cut at a
// word boundary, when the detail carries no colon to cut at.
func TestWatchEventReasonIsTheFirstClause(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		detail string
		want   string
	}{
		{"verdict-led", "no-commits: the branch is empty", "no-commits"},
		{"sentence with a colon", "attempt 4 of 46x struck out: gofmt drifted in two files", "attempt 4 of 46x struck out"},
		{"no colon, short", "the integrated gate refused attempt 4 of 46x (go)", "the integrated gate refused attempt 4 of 46x (go)"},
		{"no colon, long", strings.Repeat("word ", 40), strings.TrimRight(strings.Repeat("word ", 24), " ")}, // the boundary falls on a space: 24 whole words
		{"empty", "", ""},
	} {
		if got := watchEventReason(tc.detail); got != tc.want {
			t.Errorf("%s: watchEventReason = %q, want %q", tc.name, got, tc.want)
		}
	}
}
