package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The run-dispatched jobs' allowance (role_allowance.go), driven through the
// two halts of epic-2jn's vqc on 2026-09-27:
//
//  1. vqc attempt 50 conflicted; its resolve job committed a clean resolution
//     and its runner exited 0 without a report. missing-result → the resolve
//     was recorded "failed" → merge_failed → the run stopped.
//  2. A person released attempt 50 with --carry-work; attempt 51 conflicted
//     again and was refused at once — "a resolve-conflict job already ran for
//     this tick" — because the failed resolve still counted.
//
// short: each test is one full fixture run; skipped under -short with the
// rest of the end-to-end suite.

// resolveStartsIn is the job ids the fake runner's resolve workers recorded,
// one per start, in start order.
func resolveStartsIn(t *testing.T, file string) []string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

// filterStage is the stages equal to one stage.
func filterStage(stages []string, stage string) []string {
	var out []string
	for _, s := range stages {
		if s == stage {
			out = append(out, s)
		}
	}
	return out
}

// resolveDecisionsOf is the recorded resolve decisions of one tick.
func resolveDecisionsOf(t *testing.T, r *Reconciler, tick string) []runstate.Decision {
	t.Helper()
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	var out []runstate.Decision
	for _, d := range decisions {
		if d.Role == RoleResolveConflict && d.Request["tick_id"] == tick {
			out = append(out, d)
		}
	}
	return out
}

// HALT 1. A resolve job that committed its resolution and exited without a
// report failed OPERATIONALLY: it never answered, so it does not spend the
// tick's resolve. The run dispatches another resolve job, cut at the
// resolution the first one committed, and the tick integrates without a
// person.
//
// serial: this test states the process environment (CONFLICT_SYNC,
// CONFLICT_TICKS) for the fake runner's workers to synchronise through, and
// t.Setenv forbids a parallel test.
func TestAResolveThatExitsWithoutAReportIsRetriedFromItsCommittedResolution(t *testing.T) {
	conflictSync(t)
	opts := fixtureOptions{mode: "conflict_resolve_noreport", gate: resolveGate}
	f := newFixture(t, opts)
	seedSharedFile(t, f)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a resolve that never answered is not the tick's one resolve",
			result.State, result.Reason)
	}
	current, err := f.Tracker.Show(context.Background(), "a2")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a2 is %s: the conflict was resolved by the retried job", current.Status)
	}

	starts := resolveStartsIn(t, filepath.Join(os.Getenv("CONFLICT_SYNC"), "resolve.starts"))
	if len(starts) != 2 {
		t.Fatalf("the run started %d resolve jobs (%v); one that failed without answering and one retry", len(starts), starts)
	}
	if starts[0] == starts[1] {
		t.Errorf("the retry ran under the failed job's identity %s: each job of the allowance is its own", starts[0])
	}

	decisions := resolveDecisionsOf(t, r, "a2")
	if len(decisions) != 2 {
		t.Fatalf("recorded %d resolve decisions for a2, want the failed job's and the merged one's", len(decisions))
	}
	failed, merged := decisions[0], decisions[1]
	if failed.Response["status"] != "failed" || failed.Response["failure"] != failureOperational {
		t.Errorf("the first resolve is recorded %v/%v, not failed/operational", failed.Response["status"],
			failed.Response["failure"])
	}
	if merged.Response["status"] != "merged" {
		t.Errorf("the second resolve is recorded %v, not merged", merged.Response["status"])
	}
	// The retry started FROM the committed resolution — it did not redo it
	// from the markers: the head the merge was minted from is the head the
	// failed job committed.
	committed, _ := failed.Response["resolve_head"].(string)
	if committed == "" {
		t.Fatal("the failed resolve's committed head is not recorded")
	}
	if got, _ := merged.Response["resolve_head"].(string); got != committed {
		t.Errorf("the merge was minted from %s, not the resolution %s the failed job committed", got, committed)
	}
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
	if shared := readGitBlob(t, clone.Dir, "origin/epic/qeu", "shared-work.txt"); shared != "resolved by the resolve-conflict job" {
		t.Errorf("the shared file on the integration branch is %q, not the resolution", shared)
	}
}

// The operational retries are bounded: a resolve worker that never answers is
// started 1+maxOperationalRetries times per try of the tick, each spent try
// goes to the standing ladder carrying its work, and once the ladder is spent
// the run stops naming every job and a remedy that parses.
//
// serial: t.Setenv, as above.
func TestResolvesThatNeverAnswerAreBoundedAndTheStopNamesEveryOne(t *testing.T) {
	conflictSync(t)
	opts := fixtureOptions{mode: "conflict_resolve_silent", gate: resolveGate}
	f := newFixture(t, opts)
	seedSharedFile(t, f)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: resolves that never answer are bounded, and the bound is a stop", result.State)
	}
	// Each try of the tick gets its own allowance of 1+maxOperationalRetries
	// resolves, and a try whose allowance is spent goes to the standing
	// ladder carrying its work (epic hn6's cloud run: 7uv halted "needs a
	// person" here) — escalated, then the one further try at the ceiling,
	// and only then the stop. Three tries under this fixture's policy.
	const tries = 3
	starts := resolveStartsIn(t, filepath.Join(os.Getenv("CONFLICT_SYNC"), "resolve.starts"))
	if len(starts) != tries*(1+maxOperationalRetries) {
		t.Fatalf("the run started %d resolve jobs (%v), want %d: %d per try over %d tries", len(starts), starts,
			tries*(1+maxOperationalRetries), 1+maxOperationalRetries, tries)
	}
	if n := len(filterStage(r.Stages("a2"), StageRejectedWorkCarried)); n != tries-1 {
		t.Errorf("%d tries were handed to the ladder carrying their work, want %d", n, tries-1)
	}
	if r.failure == nil {
		t.Fatal("the bound stopped the run with no refusal to read")
	}
	for _, want := range []string{"shared-work.txt", "failed without delivering a resolution",
		"tick-a2/resolve-", "-r2", "-r3", "ticfac settle qeu a2 ", "--carry-work",
		// The release the stop names is addressed by the run whose store
		// carries the attempt (tick qxj).
		"--run-id r-fixture --release"} {
		if !strings.Contains(r.failure.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, r.failure.Message)
		}
	}
}

// HALT 2. A person's release of the tick is a decision to try again: a
// resolve recorded BEFORE the release — even one that answered and failed on
// its merits — does not stop the next conflict, which gets a resolve of its
// own.
//
// serial: t.Setenv, as above.
func TestAReleaseOfTheTickStartsItsResolveAllowanceAfresh(t *testing.T) {
	conflictSync(t)
	opts := fixtureOptions{mode: "conflict", gate: resolveGate}
	f := newFixture(t, opts)
	seedSharedFile(t, f)
	st := seedRunBranch(t, f)
	putDecision(t, st, runstate.Decision{
		Decision: 1, Role: RoleResolveConflict,
		Request: map[string]any{"tick_id": "a2", "epic_id": "qeu", "role": RoleResolveConflict,
			"resolve_branch": "ticfac/run-r-fixture/tick-a2/resolve-50"},
		Response: map[string]any{"status": "failed", "conflict_files": []string{"shared-work.txt"}},
	})
	putDecision(t, st, runstate.Decision{
		Decision: 2, Role: settleRole,
		Request: map[string]any{"op": SettleOp, "run_id": "r-fixture", "epic_id": "qeu", "tick_id": "a2",
			"attempt": 50, "job_id": "run-r-fixture/tick-a2/attempt-50", "state": "settled"},
		Response: map[string]any{"settled": true, "released_by": "someone", "disposition": dispositionCarryWork,
			"carry_ref": "refs/heads/ticfac/run-r-fixture/tick-a2/attempt-50", "carry_sha": ""},
	})

	r, result := runSeeded(t, f, opts)
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a release of the tick is a decision to resolve it again",
			result.State, r.failure)
	}
	if got := resolveDecisionsOf(t, r, "a2"); len(got) != 2 || got[1].Response["status"] != "merged" {
		t.Errorf("the conflict after the release was not resolved by a fresh job: %v", got)
	}
}

// A resolve an earlier incarnation recorded as an OPERATIONAL failure does
// not spend the tick's resolve either: the next incarnation that meets the
// conflict dispatches one.
//
// serial: t.Setenv, as above.
func TestARecordedOperationalResolveFailureDoesNotStopTheNextConflict(t *testing.T) {
	conflictSync(t)
	opts := fixtureOptions{mode: "conflict", gate: resolveGate}
	f := newFixture(t, opts)
	seedSharedFile(t, f)
	st := seedRunBranch(t, f)
	putDecision(t, st, runstate.Decision{
		Decision: 1, Role: RoleResolveConflict,
		Request: map[string]any{"tick_id": "a2", "epic_id": "qeu", "role": RoleResolveConflict,
			"resolve_branch": "ticfac/run-r-fixture/tick-a2/resolve-50"},
		Response: map[string]any{"status": "failed", "failure": failureOperational,
			"reason": "missing-result", "conflict_files": []string{"shared-work.txt"}},
	})

	r, result := runSeeded(t, f, opts)
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%v): a resolve that never answered does not spend the tick's resolve",
			result.State, r.failure)
	}
}

// seedRunBranch creates the integration branch the way the run's own start
// does and opens the store a run writes its decisions through.
func seedRunBranch(t *testing.T, f *fixture) *runstate.Store {
	t.Helper()
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	st, err := runstate.Open(runstate.Options{Repo: f.Repo.Dir, Remote: "origin", Branch: "epic/qeu", RunID: "r-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// putDecision lands one seeded decision, stamped and validated as a run's own.
func putDecision(t *testing.T, st *runstate.Store, d runstate.Decision) {
	t.Helper()
	d.Validated = true
	d.RequestedAt, d.AnsweredAt = "2026-09-27T19:00:00Z", "2026-09-27T19:00:00Z"
	d.Provenance = ProvenanceForTest("a2")
	if _, err := st.PutDecision(d); err != nil {
		t.Fatal(err)
	}
}

// runSeeded runs the fixture over the run branch a test seeded.
func runSeeded(t *testing.T, f *fixture, opts fixtureOptions) (*Reconciler, *Result) {
	t.Helper()
	r, err := New(f.options(f.Repo, opts))
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	return r, result
}

// The gate-repair job has the same shape and the same rule: a repair that
// committed its fix and exited without a report did not answer, so it does
// not spend the tick's one repair — the next repair starts from the fix it
// committed, and the tick closes behind a passing gate.
//
// serial: this test states the process environment (REPAIR_SYNC) for the
// fake runner's repair workers to count their starts in, and t.Setenv
// forbids a parallel test.
func TestARepairThatExitsWithoutAReportIsRetriedFromItsCommittedFix(t *testing.T) {
	sync := t.TempDir()
	t.Setenv("REPAIR_SYNC", sync)
	opts := fixtureOptions{mode: "gate_break_repair_noreport", gate: repairGate}
	f := newFixture(t, opts)
	seedStaleReference(t, f)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a repair that never answered is not the tick's one repair",
			result.State, result.Reason)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s: the gate failure was repaired by the retried job", current.Status)
	}
	starts := resolveStartsIn(t, filepath.Join(sync, "repair.starts"))
	if len(starts) != 2 || starts[0] == starts[1] {
		t.Fatalf("the run started repair jobs %v; want one that failed without answering and one retry of its own", starts)
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	var repairs []runstate.Decision
	for _, d := range decisions {
		if d.Role == RoleRepairGate {
			repairs = append(repairs, d)
		}
	}
	if len(repairs) != 2 {
		t.Fatalf("recorded %d repair decisions, want the failed job's and the merged one's", len(repairs))
	}
	if repairs[0].Response["failure"] != failureOperational || repairs[1].Response["status"] != "merged" {
		t.Errorf("the repairs are recorded %v and %v", repairs[0].Response, repairs[1].Response)
	}
	committed, _ := repairs[0].Response["repair_head"].(string)
	if committed == "" || repairs[1].Response["repair_head"] != committed {
		t.Errorf("the merged repair %v is not the fix %s the failed job committed", repairs[1].Response["repair_head"], committed)
	}
}
