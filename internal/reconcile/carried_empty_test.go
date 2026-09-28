package reconcile

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A carried attempt that adds nothing delivers the carried work (tick isp).
//
// epic-2jn, 2026-09-27: eih's first attempt was held with real commits and a
// person released it with --carry-work. The next attempt was cut FROM those
// commits, found the work already done, and correctly added nothing. The
// collect measured "commits beyond the base" from the base the attempt was
// cut from — the carried head — found none, and rejected it as no-commits.
// Nothing merged the carried work, and every later attempt was cut from the
// same carried head again: dispatch, no-commits, resume, forever, until a
// person merged the carried commit by hand.
//
// For a carried attempt "beyond the base" means beyond the base the ORIGINAL
// attempt was cut from: an attempt that adds nothing still delivers the
// carried commits, which then merge and gate like any other attempt's — and
// when the carried work is already on the integration branch the attempt is
// already contained, never no-commits.

// releaseCarryingA1 drives a1 to a held attempt with committed work and
// releases it with --carry-work, returning the released attempt's ref and the
// head the next attempt will be cut from.
func releaseCarryingA1(t *testing.T, f *fixture) (releasedRef, releasedHead string) {
	t.Helper()
	return releaseCarrying(t, f, "hang")
}

// releaseCarrying is releaseCarryingA1 with the held attempt's runner mode
// named: the mode decides WHAT the released attempt committed.
func releaseCarrying(t *testing.T, f *fixture, mode string) (releasedRef, releasedHead string) {
	t.Helper()
	f.Runner = fakeRunnerArgv(t, mode)
	_, _, err := f.run(f.Repo, fixtureOptions{mode: mode, stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	releasedRef, _, releasedHead = waitHeldWork(t, f, "a1")
	f.stopEverything()

	_, held, err := f.run(f.Repo, fixtureOptions{mode: "hang"})
	if err != nil {
		t.Fatalf("the run that found the lost attempt errored: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the lost attempt was not held: %+v", held.Failure)
	}
	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := settler.SettleCarry(context.Background(), "a1", 1, "an operator")
	if err != nil {
		t.Fatalf("settle a1's lost attempt carrying its work: %v", err)
	}
	if !settled.Carried || settled.CarrySHA != releasedHead {
		t.Fatalf("the settlement is %+v, want the work carried at %s", settled, releasedHead)
	}
	return releasedRef, releasedHead
}

// a1Carried asserts a1's closing attempt was cut from the carried work and was
// neither rejected nor redispatched on the way to its close.
func a1Carried(t *testing.T, f *fixture, r *Reconciler, releasedHead string) {
	t.Helper()
	spec := f.spec("a1")
	if spec == nil {
		t.Fatal("a1 was never dispatched after the release")
	}
	if spec.Source.BaseSHA != releasedHead {
		t.Fatalf("the carried attempt was cut from %s, want the released work at %s", spec.Source.BaseSHA, releasedHead)
	}
	stages := r.Stages("a1")
	if !contains(stages, StageCarried) {
		t.Errorf("a1's stages %v do not record the carry", stages)
	}
	if contains(stages, StageRejected) || contains(stages, StageRedispatched) {
		t.Errorf("the carried attempt that added nothing was rejected or redispatched: %v", stages)
	}
	if !contains(stages, StageGatePassed) {
		t.Errorf("a1 was closed without the per-tick gate: %v", stages)
	}
}

// The release carries the work, the next attempt adds nothing, and the tick
// closes: the carried commits are collected as the attempt's delivery, merged
// into the integration branch and gated.
func TestACarriedAttemptThatAddsNothingMergesTheCarriedWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, releasedHead := releaseCarryingA1(t, f)
	if containsCommit(t, f, releasedHead, "origin/epic/qeu") {
		t.Fatal("the carried work is already on the integration branch; this fixture proves nothing")
	}

	f.Runner = fakeRunnerArgv(t, "a1-adds-nothing")
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after the carrying release did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the run ended %s (%+v) with a1 not closed: a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	a1Carried(t, f, r, releasedHead)
	merged := false
	for _, event := range r.Journal() {
		if event.Tick == "a1" && event.Stage == StageIntegrated && strings.HasPrefix(event.Detail, "merged ") {
			merged = true
		}
	}
	if !merged {
		t.Errorf("the carried work was not merged by the run: a1's stages %v", r.Stages("a1"))
	}
	if !containsCommit(t, f, releasedHead, "origin/epic/qeu") {
		t.Fatal("the carried work never reached the integration branch")
	}
}

// The carried work is already on the integration branch — a person merged it
// by hand, as the operator did on epic-2jn — and the next attempt adds
// nothing: that attempt is already contained, gated on the epic head and
// closed, never refused as no-commits.
func TestACarriedAttemptWhoseWorkIsAlreadyIntegratedIsAlreadyContained(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	releasedRef, releasedHead := releaseCarryingA1(t, f)

	// A person merges the released work into the integration branch by hand.
	dir := t.TempDir()
	mustRun(t, dir, "git", "clone", "--quiet", "--branch", "epic/qeu", f.Repo.Origin, dir)
	configure(t, dir)
	mustRun(t, dir, "git", "fetch", "--quiet", f.Repo.Dir, releasedRef+":refs/heads/carried")
	mustRun(t, dir, "git", "merge", "--no-ff", "--no-edit", "carried")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")
	if !containsCommit(t, f, releasedHead, "origin/epic/qeu") {
		t.Fatal("the hand merge did not put the carried work on the integration branch; this fixture proves nothing")
	}

	f.Runner = fakeRunnerArgv(t, "a1-adds-nothing")
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after the hand merge did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the run ended %s (%+v) with a1 not closed: a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	a1Carried(t, f, r, releasedHead)
	contained := false
	for _, event := range r.Journal() {
		if event.Tick == "a1" && event.Stage == StageIntegrated &&
			strings.Contains(event.Detail, fmt.Sprintf("%s is already contained", short(releasedHead))) {
			contained = true
		}
	}
	if !contained {
		t.Errorf("no %q line for a1's carried head: %v", "already contained", r.Stages("a1"))
	}
}

// A carried attempt that added nothing and was then REJECTED — here its worker
// asked for a person — is spent only while its delivery is not integrated. The
// live epic-2jn stall ended exactly so: a person merged the carried commit by
// hand. The resume must then see the carried head on the integration branch
// and finish the attempt from there (gate, close), not dispatch yet another
// attempt from the same carried head.
func TestARejectedCarriedAttemptWhoseWorkAPersonMergedIsIntegrated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	releasedRef, releasedHead := releaseCarryingA1(t, f)

	blocked := fakeRunnerArgv(t, "a1-adds-nothing")
	blocked = append([]string{blocked[0], "FAKE_RUNNER_A1_STATUS=BLOCKED"}, blocked[1:]...)
	f.Runner = blocked
	_, refused, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the refusing run did not finish: %v", err)
	}
	if refused.Failure == nil || refused.Failure.Reason != RefusedNeedsHuman || refused.Failure.TickID != "a1" {
		t.Fatalf("the run failed as %+v, want %s for a1", refused.Failure, RefusedNeedsHuman)
	}
	marker := attemptMarker(t, f, "a1", 2)
	if marker.ResumedFrom == nil {
		t.Fatalf("a1's attempt 2 was not the carried one: %+v", marker)
	}
	if head := originHeadOf(t, f, branchOf(marker.WriteRef)); head != releasedHead {
		t.Fatalf("the carried attempt's branch on origin is %q, want the carried head %s", head, releasedHead)
	}

	// A person merges the carried work by hand.
	dir := t.TempDir()
	mustRun(t, dir, "git", "clone", "--quiet", "--branch", "epic/qeu", f.Repo.Origin, dir)
	configure(t, dir)
	mustRun(t, dir, "git", "fetch", "--quiet", f.Repo.Dir, releasedRef+":refs/heads/carried")
	mustRun(t, dir, "git", "merge", "--no-ff", "--no-edit", "carried")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/epic/qeu")

	f.Runner = fakeRunnerArgv(t, "report")
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the resumed run ended %s (%+v) with a1 not closed: a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	stages := r.Stages("a1")
	if contains(stages, StageDispatched) || contains(stages, StageRedispatched) {
		t.Errorf("a carried attempt whose work is already integrated was dispatched over: %v", stages)
	}
	if !contains(stages, StageGatePassed) {
		t.Errorf("a1 was closed without the per-tick gate: %v", stages)
	}
}

// A run killed after it merged a carried attempt's delivery resumes to the
// close: the attempt's own branch adds nothing to its dispatched base, and the
// resume still sees the carried head it merged — it is not collected a second
// time (the worker was released at the collect, so a second collect reads a
// worktree the run removed itself), it is gated and closed.
func TestACarriedDeliveryMergedBeforeAKillIsFinishedOnResume(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, releasedHead := releaseCarryingA1(t, f)

	f.Runner = fakeRunnerArgv(t, "a1-adds-nothing")
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageIntegrated)})
	killedAfter(t, err, "a1", StageIntegrated)
	if !containsCommit(t, f, releasedHead, "origin/epic/qeu") {
		t.Fatal("the killed run never merged the carried work; this fixture proves nothing")
	}

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted || !contains(result.Closed, "a1") {
		t.Fatalf("the resumed run ended %s (%+v) with a1 not closed: a1's stages %v",
			result.State, result.Failure, r.Stages("a1"))
	}
	stages := r.Stages("a1")
	if contains(stages, StageRejected) || contains(stages, StageDispatched) {
		t.Errorf("the merged carried delivery was rejected or dispatched over on resume: %v", stages)
	}
	if !contains(stages, StageGatePassed) {
		t.Errorf("a1 was closed without the per-tick gate: %v", stages)
	}
}
