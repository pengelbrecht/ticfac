package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A retried job that is handed a committed resolution (or fix) and finds
// nothing left to change settles with its branch AT that handed commit — and
// that branch reaches origin only if the supervisor's final push lands, which
// is best-effort: a push the remote refuses, or one that fails under load, is
// a log note and nothing more. #82's retry test failed exactly so in CI
// (run 36349904786): "fetch the resolve-conflict job's branch
// ticfac/run-r-fixture/base-fold-2-…: couldn't find remote ref". The mint
// fetched the retry's own branch for a head it already had — the resolution
// the failed job committed, durable on THAT job's branch.
//
// These tests take the supervisor's push away deterministically: origin
// refuses the retry's ref outright, so its branch never reaches origin.
//
// short: each test is one full fixture run; skipped under -short with the
// rest of the end-to-end suite.

// refuseRefsMatching makes the fixture's origin refuse every update of a ref
// matching the shell pattern.
func refuseRefsMatching(t *testing.T, origin, pattern string) {
	t.Helper()
	hook := filepath.Join(origin, "hooks", "update")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		pattern + ")\n" +
		"\techo 'the fixture refuses this ref' >&2\n" +
		"\texit 1\n" +
		"\t;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// serial: this test states the process environment (CONFLICT_SYNC) for the
// fake runner's resolve workers, and t.Setenv forbids a parallel test.
func TestABaseFoldRetryWhoseBranchNeverReachesOriginFinishesFromTheHandedResolution(t *testing.T) {
	t.Setenv("CONFLICT_SYNC", t.TempDir())
	opts := fixtureOptions{mode: "conflict_resolve_noreport"}
	f := newFixture(t, opts)
	_, mainHead := baseFoldConflict(t, f)
	refuseRefsMatching(t, f.Repo.Origin, "refs/heads/ticfac/run-r-fixture/base-fold-2-*")

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a retry handed a durable resolution does not need its own branch on origin",
			result.State, result.Failure)
	}
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", mainHead, refFor("epic/qeu")) {
		t.Errorf("epic/qeu does not carry main at %s after the retried fold", short(mainHead))
	}
	decisions := baseFoldDecisions(t, r)
	if len(decisions) != 2 || decisions[1].Response["resolve_head"] != decisions[0].Response["resolve_head"] {
		t.Errorf("the fold was not finished from the resolution the failed job committed: %v", decisions)
	}
}

// serial: t.Setenv (CONFLICT_SYNC, CONFLICT_TICKS), as above.
func TestAResolveRetryWhoseBranchNeverReachesOriginFinishesFromTheHandedResolution(t *testing.T) {
	conflictSync(t)
	opts := fixtureOptions{mode: "conflict_resolve_noreport", gate: resolveGate}
	f := newFixture(t, opts)
	seedSharedFile(t, f)
	refuseRefsMatching(t, f.Repo.Origin, "refs/heads/ticfac/run-r-fixture/tick-a2/resolve-*-r2")

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a retry handed a durable resolution does not need its own branch on origin",
			result.State, result.Reason)
	}
	decisions := resolveDecisionsOf(t, r, "a2")
	if len(decisions) != 2 || decisions[1].Response["status"] != "merged" ||
		decisions[1].Response["resolve_head"] != decisions[0].Response["resolve_head"] {
		t.Errorf("the conflict was not finished from the resolution the failed job committed: %v", decisions)
	}
}

// serial: t.Setenv (REPAIR_SYNC), as above.
func TestARepairRetryWhoseBranchNeverReachesOriginFinishesFromTheHandedFix(t *testing.T) {
	t.Setenv("REPAIR_SYNC", t.TempDir())
	opts := fixtureOptions{mode: "gate_break_repair_noreport", gate: repairGate}
	f := newFixture(t, opts)
	seedStaleReference(t, f)
	refuseRefsMatching(t, f.Repo.Origin, "refs/heads/ticfac/run-r-fixture/tick-a1/repair-*-r2")

	_, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a retry handed a durable fix does not need its own branch on origin",
			result.State, result.Reason)
	}
}
