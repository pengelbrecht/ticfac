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
