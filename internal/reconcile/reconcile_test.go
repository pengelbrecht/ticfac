package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The acceptance criterion, as one test: a fixture epic — three ticks in two
// waves, plus the EPIC-SKELETON's review and closeout — completes end to end
// with a fake runner.

func TestAFixtureEpicCompletesEndToEnd(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	for _, tick := range []string{"a1", "a2", "b1", "rv", "co"} {
		current, err := f.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("tick %s is %s, not closed", tick, current.Status)
		}
		if got := f.Tracker.count("close:" + tick); got != 1 {
			t.Errorf("tick %s was closed %d times", tick, got)
		}
	}

	// The order per tick is the contract: nothing is closed before its gate,
	// and nothing is cleaned up before its close.
	for _, tick := range []string{"a1", "b1", "co"} {
		assertOrder(t, r.Stages(tick),
			StageClaimed, StageDispatched, StageCollected, StageIntegrated, StageGatePassed, StageClosed, StageCleanedUp)
	}
}

// EPIC-SKELETON: review and closeout are jobs like any other, and they go
// LAST — after every tick they are about.
func TestReviewAndCloseoutAreDispatchedLast(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, _, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var dispatched []string
	for _, event := range r.Journal() {
		if event.Stage == StageDispatched {
			dispatched = append(dispatched, event.Tick)
		}
	}
	want := []string{"a1", "a2", "b1", "rv", "co"}
	if strings.Join(dispatched, ",") != strings.Join(want, ",") {
		t.Fatalf("dispatch order %v, want %v", dispatched, want)
	}
}

// Every state change leaves a checkpoint, and every dispatch leaves an attempt
// record. Both are on ORIGIN, because a local commit is not durable.
func TestTheRunLeavesACheckpointAndAnAttemptRecordPerDispatch(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, _, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}

	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: r.IntegrationBranch(), RunID: r.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}

	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("no checkpoint on origin: %v", err)
	}
	if checkpoint.State != runstate.StateCompleted {
		t.Errorf("the durable checkpoint says %s", checkpoint.State)
	}
	if checkpoint.Sequence < 5 {
		t.Errorf("the run wrote %d checkpoints; a checkpoint per state change is more than that", checkpoint.Sequence)
	}
	for _, ts := range checkpoint.Ticks {
		if ts.State != "closed" {
			t.Errorf("the checkpoint says %s is %s", ts.TickID, ts.State)
		}
	}

	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 5 {
		t.Fatalf("%d attempt records for five dispatches", len(attempts))
	}
	for _, attempt := range attempts {
		if attempt.JobHandle == nil {
			t.Errorf("attempt %d records no job handle", attempt.Attempt)
		}
		if attempt.Provenance.RunID != r.RunID() {
			t.Errorf("attempt %d names run %s", attempt.Attempt, attempt.Provenance.RunID)
		}
	}

	// The gate's evidence is on origin too, one record per check per attempt
	// that MERGED something, fingerprinted to what it evaluated. The review is
	// dispatched read-only and integrates nothing, so there is nothing for an
	// integrated gate to be about: what stands behind its close is the
	// validated role-result envelope, recorded as a decision.
	keys := store.EvidenceKeys()
	want := []string{"gate-a1-1-tree", "gate-a2-2-tree", "gate-b1-3-tree", "gate-co-5-tree"}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Fatalf("evidence keys %v, want %v", keys, want)
	}
	for _, key := range keys {
		evidence, ok, err := store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("evidence %s: %v", key, err)
		}
		if evidence.Result != "pass" {
			t.Errorf("evidence %s is %s", key, evidence.Result)
		}
		fingerprint := Fingerprint{
			"source_sha":              evidence.Provenance.SourceSHA,
			"integration_ref":         deref(evidence.Provenance.IntegrationRef),
			"context_manifest_digest": deref(evidence.Provenance.ContextManifestDigest),
			"profile_digest":          deref(evidence.Provenance.ProfileDigest),
		}
		if !fingerprint.Complete() {
			t.Errorf("evidence %s cannot say what it evaluated: %v", key, fingerprint)
		}
	}
}

// A failing gate is what stops a close. The tick stays open, the run fails, and
// the message says which check refused — not "the run broke".
func TestAFailingGateStopsTheCloseAndSaysWhichCheckRefused(t *testing.T) {
	f := newFixture(t, fixtureOptions{gate: failingGate})
	r, result, err := f.run(f.Repo, fixtureOptions{gate: failingGate})
	if err != nil {
		t.Fatalf("the run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Fatal("a tick was closed behind a gate that did not pass")
	}
	stages := r.Stages("a1")
	if !contains(stages, StageGateFailed) {
		t.Fatalf("stages %v do not record the gate failure", stages)
	}
	if contains(stages, StageClosed) || contains(stages, StageCleanedUp) {
		t.Fatalf("stages %v close or clean up after a failing gate", stages)
	}
	if !strings.Contains(result.Reason, "a1") {
		t.Errorf("the run's reason does not name the tick that failed: %q", result.Reason)
	}
}

// The boundary the executor enforces is REPORTED by the reconciler and refuses
// the merge: an attempt that wrote tracker state does not reach the integration
// branch, whatever its report said.
func TestABoundaryViolationIsReportedAndNeverMerged(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "boundary"})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "boundary"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s", result.State)
	}
	stages := r.Stages("a1")
	if !contains(stages, StageRejected) {
		t.Fatalf("stages %v do not report the boundary violation", stages)
	}
	if contains(stages, StageIntegrated) {
		t.Fatal("an attempt that wrote under an authority that is not its own reached the integration branch")
	}
	var reported string
	for _, event := range r.Journal() {
		if event.Stage == StageRejected {
			reported = event.Detail
		}
	}
	if !strings.Contains(reported, ".tick/") {
		t.Errorf("the boundary report does not name what was written: %q", reported)
	}
}

// A run that is already terminal is not run again. Replaying a finished run's
// checkpoint must neither redispatch nor reclose anything.
func TestATerminalRunIsNotRunAgain(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	if _, _, err := f.run(f.Repo, fixtureOptions{}); err != nil {
		t.Fatal(err)
	}
	closes := f.Tracker.count("close:a1")

	second := cloneRepo(t, f.Repo.Origin, f.Root+"/second")
	r, result, err := f.run(second, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the replay ended %s", result.State)
	}
	if got := f.Tracker.count("close:a1"); got != closes {
		t.Errorf("the replay closed a1 again: %d closes, was %d", got, closes)
	}
	if len(r.Journal()) != 1 || r.Journal()[0].Stage != StageRunFinished {
		t.Errorf("the replay did work: %+v", r.Journal())
	}
}

func assertOrder(t *testing.T, stages []string, want ...string) {
	t.Helper()
	at := 0
	for _, stage := range stages {
		if at < len(want) && stage == want[at] {
			at++
		}
	}
	if at != len(want) {
		t.Errorf("stages %v do not contain %v in order (matched %d)", stages, want, at)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
