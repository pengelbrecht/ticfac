package reconcile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Tick pbb's acceptance, end to end through the real executor.
//
// The Phase 3 story, played out: attempt 1 of a1 hangs past its wall clock
// holding real, uncommitted work; the bound fires and stops it; the run
// rejects the attempt as missing-result — and the teardown that follows the
// rejection, which is where a stopped attempt's uncommitted work used to die
// unrecorded, now preserves it. The attempt is still not ready-to-merge, but
// its work is recoverable after teardown at a recorded ref no branch owns.
//
// And the use (the nvn shape, applied to work rather than to reports): the
// re-dispatched attempt 2 is POINTED AT that preserved work — its dispatch
// carries the snapshot, its prompt names it, framed as material to read and
// never to merge — instead of starting blind over work a predecessor already
// did.
func TestAStoppedAttemptSPreservedWorkReachesTheNextAttempt(t *testing.T) {
	t.Parallel()

	wip := fixtureOptions{mode: "wallwip"}
	f := newFixture(t, wip)
	opts := f.options(f.Repo, wip)
	// A bound of real seconds — the supervisor's own timer is what fires —
	// and the reconciler's settlement deadline (bound + wipe threshold) sits
	// well behind it, so the attempt settles from the stop, not from the
	// deadline.
	opts.WallSeconds = 10

	// Incarnation one: attempt 1 of a1 hangs holding uncommitted work, the
	// wall clock stops it, and the run refuses over the stopped attempt —
	// the refusal whose teardown is the last place the work can die.
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the first incarnation did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the first incarnation ended %s; the stopped attempt should have refused it", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCollect {
		t.Fatalf("the refusal is %+v, want %s: a stopped attempt with nothing committed is refused by its collect",
			result.Failure, RefusedCollect)
	}

	attemptDir := func(attempt int) string {
		return filepath.Join(f.StateRoot, "r-fixture", "a1", strconv.Itoa(attempt))
	}
	state1, found := findAttemptState(attemptDir(1))
	if !found {
		t.Fatal("attempt 1 of a1 left no attempt state for anyone to find")
	}

	// The attempt is still not ready-to-merge: its persisted result is a
	// failure in the closed vocabulary — a worker stopped at its bound with
	// nothing committed collects as no-commits, failed — and the run's own
	// feed says the bound fired, so nobody mistakes the stop for the
	// worker's own exit.
	raw, err := os.ReadFile(filepath.Join(state1, "result.json"))
	if err != nil {
		t.Fatalf("attempt 1 left no persisted result: %v", err)
	}
	var settled subprocess.JobResult
	if err := json.Unmarshal(raw, &settled); err != nil {
		t.Fatalf("attempt 1's result does not parse: %v", err)
	}
	if settled.Outcome != subprocess.OutcomeFailed {
		t.Errorf("attempt 1's result is %s, want %s: a worker stopped at its wall clock with nothing committed is not ready-to-merge",
			settled.Outcome, subprocess.OutcomeFailed)
	}
	var wallFired bool
	for _, event := range r.Journal() {
		if event.Stage == StageWallClock && event.Tick == "a1" {
			wallFired = true
		}
	}
	if !wallFired {
		t.Error("the run's feed never said a1's wall clock fired: the stop the snapshot preserves was never observed")
	}

	// The uncommitted file is recoverable AFTER TEARDOWN: the worktree is
	// gone — the force-remove that used to be the last anyone saw of the
	// work — and the snapshot survives it, on a ref no branch owns.
	if _, err := os.Stat(filepath.Join(state1, "worktree")); !os.IsNotExist(err) {
		t.Fatalf("attempt 1's worktree survived its teardown: %v", err)
	}
	raw, err = os.ReadFile(filepath.Join(state1, subprocess.FileWIPSnapshot))
	if err != nil {
		t.Fatalf("attempt 1's teardown recorded nowhere that its uncommitted work was preserved: %v", err)
	}
	var snap subprocess.WIPSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil || snap.Ref == "" || snap.Commit == "" {
		t.Fatalf("attempt 1's wip snapshot record does not parse or is empty: %s (%v)", raw, err)
	}
	if got := gitShow(t, f.Repo.Dir, snap.Commit+":wip-a1.txt"); got != "uncommitted work of a1" {
		t.Errorf("the file the wall clock interrupted reads %q out of the snapshot, want the work as it stood", got)
	}
	if atRef := gitOut(t, f.Repo.Dir, "rev-parse", snap.Ref); strings.TrimSpace(atRef) != snap.Commit {
		t.Errorf("the recorded ref %s points at %s, want the recorded commit %s", snap.Ref, atRef, snap.Commit)
	}
	if !strings.HasPrefix(snap.Ref, "refs/ticfac/wip/") {
		t.Errorf("the snapshot lives on %s, not on a wip ref of its own", snap.Ref)
	}
	if branches := strings.TrimSpace(gitOut(t, f.Repo.Dir, "branch", "--all", "--contains", snap.Commit)); branches != "" {
		t.Errorf("the snapshot commit is reachable from branches (%s): never evidence of completion, never merged", branches)
	}

	// Incarnation two: the re-dispatch, pointed at the preserved work.
	_, result, err = f.run(f.Repo, wip)
	if err != nil {
		t.Fatalf("the re-dispatch did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the re-dispatch ended %s: %s", result.State, result.Reason)
	}

	// The dispatch the run actually made for attempt 2 carries the snapshot.
	dispatch := f.dispatch("a1")
	if dispatch.Attempt <= 1 {
		t.Fatalf("the fixture holds attempt %d's dispatch: the re-dispatch never happened", dispatch.Attempt)
	}
	if got := len(dispatch.PriorSnapshots); got != 1 {
		t.Fatalf("attempt %d of a1 carries %d prior snapshots, want 1: the preserved work of the stopped attempt did not reach the re-dispatch",
			dispatch.Attempt, got)
	}
	prior := dispatch.PriorSnapshots[0]
	if prior.Attempt != 1 || prior.Ref != snap.Ref || prior.Commit != snap.Commit {
		t.Errorf("the prior snapshot the dispatch carries is %+v, want attempt 1's %s (%s)",
			prior, snap.Ref, snap.Commit)
	}

	// And the prompt the worker was actually handed — read out of the
	// executor's own state, not out of the dispatch — names it, framed as
	// material: never evidence of completion, never to merge.
	rendered := renderedPrompt(t, attemptDir(2))
	if !strings.Contains(rendered, snap.Ref) {
		t.Errorf("attempt 2's rendered prompt does not name attempt 1's preserved work (%s):\n%s", snap.Ref, rendered)
	}
	if !strings.Contains(rendered, "never to merge") || !strings.Contains(rendered, "NOT evidence of completion") {
		t.Errorf("attempt 2's prompt does not frame the snapshot as material rather than a verdict:\n%s", rendered)
	}

	// The first attempt's own prompt carried no such section: it had nothing
	// to inherit, and a header over an empty list would teach every worker
	// to skip the section it later needs.
	if firstPrompt := renderedPrompt(t, state1); strings.Contains(firstPrompt, "preserved work") {
		t.Errorf("attempt 1's prompt carries a preserved-work section:\n%s", firstPrompt)
	}

	// Scoped to the tick, exactly as the reports are: a worker handed
	// another tick's work is handed a question it cannot answer.
	if other := f.dispatch("a2"); len(other.PriorSnapshots) != 0 {
		t.Errorf("a2's dispatch carries prior snapshots (%+v): the predecessor list is not scoped to the tick",
			other.PriorSnapshots)
	}
}

// gitOut runs one git command and returns its output, failing the test.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := harnessCommand("git", args...).output(dir)
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// gitShow reads one revision out of the repository.
func gitShow(t *testing.T, dir, rev string) string {
	t.Helper()
	return strings.TrimSpace(gitOut(t, dir, "show", rev))
}
