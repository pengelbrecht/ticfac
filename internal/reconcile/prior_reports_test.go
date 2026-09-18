package reconcile

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Tick n4h's acceptance: the starting-blind case one level up.
//
// nvn's discovery walked <ExecStateRoot>/<runID>/<tick>/<n>, so a
// re-dispatched attempt saw only the attempts of its OWN run — and a re-run
// of the epic under a NEW run id re-dispatches the same ticks with attempt
// numbers that begin again at 1 out of a run-state store of its own, so none
// of the previous run's archived reports reached the new run's prompt. The
// discovery has to be keyed by the tick, not by the run id.
func TestARunUnderANewRunIDIsOfferedThePreviousRunsReports(t *testing.T) {
	t.Parallel()

	first := fixtureOptions{mode: "blocked-first", runID: "r-first"}
	f := newFixture(t, first)

	// Incarnation one, under r-first: attempt 1 of a1 answers BLOCKED with
	// nothing committed, and the run stops on the refusal. The tick stays
	// open — which is what makes the re-run dispatch it again.
	_, result, err := f.run(f.Repo, first)
	if err != nil {
		t.Fatalf("the first run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the first run ended %s; the blocked attempt should have refused it", result.State)
	}

	// The report the first run archived for a1: under the FIRST run's id,
	// where the second run has to find it.
	firstState, found := findAttemptState(filepath.Join(f.StateRoot, "r-first", "a1", "1"))
	if !found {
		t.Fatal("the first run left no attempt state for a1")
	}
	report := filepath.Join(firstState, subprocess.FileReportArchive)
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("attempt 1 of a1's report did not survive at %s: %v", report, err)
	}
	if !strings.Contains(string(raw), "STATUS: "+subprocess.StatusBlocked) {
		t.Fatalf("the archived report is not a1's BLOCKED report:\n%s", string(raw))
	}

	// A run OLDER still, fabricated by hand where its executor would have
	// left it: a report for THIS tick under another run id, a report for
	// ANOTHER tick under another run id, and a report whose attempt record
	// names a different tick — the same-named directory of a run whose
	// attempt was genuinely not this tick. The second run is offered the
	// first, never the second, and never the third: the discoverer is keyed
	// by the tick the attempt RECORD names, not by the directory it sits in.
	fabricated := func(run, tick, attempt, tickID, status string) string {
		state := filepath.Join(f.StateRoot, run, tick, attempt)
		if err := os.MkdirAll(state, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(state, "attempt.json"),
			`{"schema_version":1,"tick_id":"`+tickID+`","issued_at":"2020-01-01T00:00:00Z"}`)
		path := filepath.Join(state, subprocess.FileReportArchive)
		write(t, path, "# "+tickID+"\n\nA fabricated predecessor.\n\nSTATUS: "+status+"\n")
		return path
	}
	thisTick := fabricated("r-previous", "a1", "1", "a1", subprocess.StatusDone)
	otherTick := fabricated("r-previous", "a2", "1", "a2", subprocess.StatusDone)
	fabricated("r-previous", "a1", "2", "not-a1", subprocess.StatusDone)

	// The re-run: the same checkout, the same state root, a NEW run id — a
	// fresh run-state store on origin, and attempt numbers that begin again
	// at 1. Before n4h that made attempt 1 of the new run a FIRST attempt by
	// every fact it could see, and both previous runs' analysis invisible.
	second := fixtureOptions{mode: "blocked-first", runID: "r-second"}
	_, result, err = f.run(f.Repo, second)
	if err != nil {
		t.Fatalf("the re-run under the new run id did not finish: %v", err)
	}
	// blocked-first keys on the attempt number, and the new run's numbering
	// restarted at 1 — so its own attempt 1 blocks the same way, and this
	// failure is itself evidence the re-run really is a run made again.
	if result.State != runstate.StateFailed {
		t.Fatalf("the re-run ended %s: its own attempt 1 should have blocked as the first run's did", result.State)
	}

	// The dispatch the new run made for a1 offers the previous runs' reports:
	// the first run's real one and the fabricated run's — two runs, one tick.
	redispatch := f.dispatch("a1")
	if redispatch.Attempt != 1 {
		t.Fatalf("the new run dispatched a1 as attempt %d, want 1: the numbering restart is what makes the previous run invisible to a run-scoped discovery", redispatch.Attempt)
	}
	if got := len(redispatch.PriorReports); got != 2 {
		t.Fatalf("the dispatch of a1 under the new run id carries %d prior reports, want the two previous runs': %+v",
			got, redispatch.PriorReports)
	}
	prior := map[string]subprocess.PriorReport{}
	for _, one := range redispatch.PriorReports {
		prior[one.Run] = one
	}
	if p := prior["r-first"]; p.Attempt != 1 || p.Path != report || p.Status != subprocess.StatusBlocked {
		t.Errorf("the first run's report is offered as %+v, want its attempt 1 (%s, %s)",
			p, subprocess.StatusBlocked, report)
	}
	if p := prior["r-previous"]; p.Attempt != 1 || p.Path != thisTick || p.Status != subprocess.StatusDone {
		t.Errorf("the fabricated run's report is offered as %+v, want its attempt 1 (%s)", p, thisTick)
	}
	for _, one := range redispatch.PriorReports {
		if one.Path == otherTick {
			t.Errorf("a2's report was offered to a1: the discovery is not keyed by the tick (%+v)", one)
		}
	}

	// And the prompt the worker was actually handed names both — each with the
	// run that produced it, newest first: the first run's report is the more
	// recent analysis, and a worker pressed for time must meet it first.
	rendered := renderedPrompt(t, filepath.Join(f.StateRoot, "r-second", "a1", strconv.Itoa(redispatch.Attempt)))
	for _, want := range []string{report, thisTick, "run r-first attempt 1", "run r-previous attempt 1",
		"STATUS: " + subprocess.StatusBlocked} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the new run's prompt does not carry %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, otherTick) {
		t.Errorf("the new run's prompt names another tick's report: the discovery is not keyed by the tick\n%s", rendered)
	}
	if firstAt, otherAt := strings.Index(rendered, "run r-first attempt 1"), strings.Index(rendered, "run r-previous attempt 1"); firstAt > otherAt {
		t.Errorf("the prompt does not render the runs newest first (r-first at %d, r-previous at %d):\n%s",
			firstAt, otherAt, rendered)
	}
	if strings.Contains(rendered, "run r-previous attempt 2") {
		t.Errorf("the prompt names the fabricated attempt whose record is another tick's:\n%s", rendered)
	}
}

// Tick nvn's acceptance: a re-dispatched attempt is shown what its
// predecessors found.
//
// Split from tick 35h, which made an attempt's prose report survive teardown
// (report.md beside the attempt record) — the precondition. This is the use:
// the ticks pwp run re-dispatched a close-out five times and each attempt
// re-derived the same impasse, because none was told where the last one's
// conclusions were.
//
// The whole run, through the real executor: attempt 1 of a1 answers BLOCKED
// with nothing committed, is rejected, and the same run re-dispatches as
// attempt 2 — whose rendered prompt must name attempt 1's archived report,
// its status line, and the framing that makes it evidence to verify rather
// than instructions to follow.
func TestASecondAttemptIsShownWhatItsPredecessorFound(t *testing.T) {
	t.Parallel()

	blocked := fixtureOptions{mode: "blocked-first"}
	f := newFixture(t, blocked)

	// Incarnation one: attempt 1 of a1 answers BLOCKED with nothing
	// committed, and the run stops on the refusal — the rejection is what is
	// durable on origin, and it is what the next incarnation redispatches
	// from. This is the pwp shape: every re-dispatch is a run made again
	// under the same run id.
	_, result, err := f.run(f.Repo, blocked)
	if err != nil {
		t.Fatalf("the first incarnation did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the first incarnation ended %s; the blocked attempt should have refused it", result.State)
	}

	// The re-dispatch: the same checkout, the same run id, a new attempt
	// number — and a prompt that names what its predecessor found.
	_, result, err = f.run(f.Repo, blocked)
	if err != nil {
		t.Fatalf("the re-dispatch did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the re-dispatch ended %s: %s", result.State, result.Reason)
	}

	// Where this run put each attempt of a1 — the layout the dispatch walks.
	attemptDir := func(attempt int) string {
		return filepath.Join(f.StateRoot, "r-fixture", "a1", strconv.Itoa(attempt))
	}

	// The predecessor's archived report: it must exist, because the prompt is
	// about to send a worker to read it, and a path that dangles is the same
	// starting blind with extra steps.
	first, found := findAttemptState(attemptDir(1))
	if !found {
		t.Fatal("attempt 1 of a1 left no attempt state for the dispatch to find")
	}
	report := filepath.Join(first, subprocess.FileReportArchive)
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("attempt 1's report did not survive teardown at %s: %v", report, err)
	}
	if !strings.Contains(string(raw), "STATUS: "+subprocess.StatusBlocked) {
		t.Fatalf("the archived report is not attempt 1's BLOCKED report:\n%s", string(raw))
	}

	// The dispatch the run actually made for attempt 2 names it.
	if got := len(f.dispatches["a1"].PriorReports); got != 1 {
		t.Fatalf("the dispatch of a1's second attempt carries %d prior reports, want 1", got)
	}
	prior := f.dispatches["a1"].PriorReports[0]
	if prior.Attempt != 1 || prior.Path != report || prior.Status != subprocess.StatusBlocked {
		t.Errorf("the prior report the dispatch carries is %+v, want attempt 1's %s (%s)",
			prior, subprocess.StatusBlocked, report)
	}

	// And the prompt the worker was actually handed — read out of the
	// executor's own state, not out of the dispatch — names it too.
	rendered := renderedPrompt(t, attemptDir(2))
	if !strings.Contains(rendered, report) {
		t.Errorf("attempt 2's rendered prompt does not name attempt 1's report (%s):\n%s", report, rendered)
	}
	if !strings.Contains(rendered, "STATUS: "+subprocess.StatusBlocked) {
		t.Errorf("attempt 2's rendered prompt does not carry attempt 1's status line:\n%s", rendered)
	}
	if !strings.Contains(rendered, "verify, not instructions to follow") {
		t.Errorf("attempt 2's rendered prompt does not frame the predecessors as analysis to verify:\n%s", rendered)
	}

	// The first attempt's own prompt carried no such section: it had nothing
	// to inherit, and a header over an empty list would teach every worker to
	// skip the section it later needs.
	if firstPrompt := renderedPrompt(t, attemptDir(1)); strings.Contains(firstPrompt, "Prior attempts") {
		t.Errorf("attempt 1's prompt carries a Prior attempts section:\n%s", firstPrompt)
	}

	// A predecessor list scoped to the TICK: a2's own first dispatch (numbered
	// 3 — attempt numbers are the run's, not the tick's) must not be shown
	// a1's report — a worker handed another tick's analysis is handed a
	// question it cannot answer.
	other := f.dispatches["a2"]
	if len(other.PriorReports) != 0 {
		t.Errorf("a2's dispatch carries prior reports (%+v): the predecessor list is not scoped to the tick", other.PriorReports)
	}
	if otherPrompt := renderedPrompt(t, filepath.Join(f.StateRoot, "r-fixture", "a2", strconv.Itoa(other.Attempt))); strings.Contains(otherPrompt, report) {
		t.Errorf("a2's attempt %d was shown a1's report: the predecessor list is not scoped to the tick", other.Attempt)
	}
}
