package reconcile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// semanticGate passes on any tree that carries the work, unless the tree
// carries semantic.txt — main's change that merges cleanly as text and breaks
// the epic as code — without the repair's ci-fix.txt beside it.
const semanticGate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null && { test ! -f semantic.txt || test -f ci-fix.txt; }", description = "the merge carries the work, and the base's change agrees with it" }
`

// EPIC-6IN's go race failure (run 36395039473, commit 70fa1e71), made
// deterministic. It was never flaky: the run-start fold of main (28385b75)
// merged main's edit of contracts/status-model.json beside the epic's re-cut
// contract bundle — clean as text, a stale digest as code — and nothing gated
// the fold, so CI went red on it and the next worker (dz1) was cut from the
// broken tree.
//
// Here: the run closes wave one and stops; main then lands a change that
// merges cleanly and breaks the gate; the resumed run folds it at start. The
// fold must be GATED, the failure answered by the repair job, and the next
// worker (b1) cut from the repaired tree — never from the red one.
func TestARunStartFoldThatBreaksTheGateIsRepairedBeforeAnyWorkerBuildsOnIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: semanticGate, mode: "land_repair",
		stopAfter: stopAt("a2", StageClosed)})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "land_repair", stopAfter: stopAt("a2", StageClosed)})
	killedAfter(t, err, "a2", StageClosed)
	if d := f.dispatch("b1"); d.TickID != "" {
		t.Fatalf("b1 was dispatched before the cut; the test needs it to come after the fold")
	}

	broke := mergeMain(t, f, "semantic.txt", "a change to main that merges cleanly and breaks the epic\n",
		"a PR on main the epic does not agree with")

	restart := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restart"))
	r, result, err := f.run(restart, fixtureOptions{mode: "land_repair"})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s (%+v): a fold that breaks the gate is the repair job's", result.State,
			result.Failure)
	}
	if !onOrigin(f, broke, "epic/qeu") {
		t.Fatal("main's change was never folded into the epic branch")
	}
	d := f.dispatch("b1")
	if d.TickID == "" {
		t.Fatal("b1 was never dispatched")
	}
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "cat-file", "-e", d.BaseSHA+":ci-fix.txt") {
		t.Errorf("b1 was cut from %s, a tree without the repair: the worker was handed the fold's red tree",
			short(d.BaseSHA))
	}
	failed := ""
	for _, e := range r.Journal() {
		if e.Stage == StageGateFailed && strings.Contains(e.Detail, "fold") {
			failed = e.Detail
		}
	}
	if failed == "" {
		t.Errorf("no gate failure on the fold is in the feed: the fold was never gated; journal stages: %v",
			r.Stages(""))
	}
}
