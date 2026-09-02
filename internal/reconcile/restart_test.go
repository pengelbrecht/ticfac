package reconcile

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Restart: the reconciler is killed at three points a crash could genuinely
// land on, and the next incarnation starts from a FRESH CLONE holding nothing
// but what is on origin.
//
// The three cuts are the three windows the design is about:
//
//	after the dispatch      — the marker is on origin and a job is running.
//	                          The restart must ADOPT it, never redispatch.
//	after the collection    — the work is collected and nothing is closed.
//	                          The restart must not close twice, and must not
//	                          close without re-establishing the gate.
//	after the gate, before  — the evidence is durable and the tracker still
//	the close                 says open. The restart must close exactly once,
//	                          and must never have closed at the moment of the
//	                          cut: a close recorded before the tracker has it
//	                          is a false close.
func TestARestartFromAFreshCloneNeitherRedispatchesNorFalselyCloses(t *testing.T) {
	cases := []struct {
		name  string
		stage string
	}{
		{"after the dispatch", StageDispatched},
		{"after the collection and before the close", StageCollected},
		{"after the gate and before the close", StageGatePassed},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{})

			// The cut. Nothing after it runs: no deferred cleanup, no close.
			killed := fixtureOptions{stopAfter: stopAt("a1", testCase.stage)}
			_, _, err := f.run(f.Repo, killed)
			killedAfter(t, err, "a1", testCase.stage)

			// The tracker is the authority on closure, and at the cut it must
			// not say a1 is closed — a stage recorded before the tracker has
			// it would be a false close.
			current, err := f.Tracker.Show(context.Background(), "a1")
			if err != nil {
				t.Fatal(err)
			}
			if current.Status == "closed" {
				t.Fatalf("a1 was closed before the run reached its close (cut after %s)", testCase.stage)
			}

			// A fresh clone: everything the next incarnation knows, it reads
			// from origin.
			clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
			restarted, result, err := f.run(clone, fixtureOptions{})
			if err != nil {
				t.Fatalf("the restart did not finish: %v", err)
			}
			if result.State != runstate.StateCompleted {
				t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
			}

			// No duplicate attempt: a1 was dispatched once, by the incarnation
			// that died, and the restart adopted it.
			store, err := runstate.Open(runstate.Options{
				Repo: clone.Dir, Remote: "origin", Branch: restarted.IntegrationBranch(), RunID: restarted.RunID(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Fetch(); err != nil {
				t.Fatal(err)
			}
			attempts, err := store.Attempts()
			if err != nil {
				t.Fatal(err)
			}
			forA1 := 0
			for _, attempt := range attempts {
				if attempt.TickID == "a1" {
					forA1++
				}
			}
			if forA1 != 1 {
				t.Errorf("%d dispatch markers for a1; the restart dispatched it again", forA1)
			}
			if got := restarted.Stages("a1"); contains(got, StageDispatched) {
				t.Errorf("the restart dispatched a1 again: %v", got)
			}
			if got := restarted.Stages("a1"); !contains(got, StageAdopted) && !contains(got, StageSkipped) {
				t.Errorf("the restart neither adopted nor skipped a1: %v", got)
			}

			// No false close, and no double close: exactly one close reached
			// the tracker across both incarnations.
			if got := f.Tracker.count("close:a1"); got != 1 {
				t.Errorf("a1 was closed %d times across the two incarnations", got)
			}
			for _, tick := range []string{"a1", "a2", "b1", "rv", "co"} {
				current, err := f.Tracker.Show(context.Background(), tick)
				if err != nil {
					t.Fatal(err)
				}
				if current.Status != "closed" {
					t.Errorf("after the restart %s is %s", tick, current.Status)
				}
			}
		})
	}
}

// The gate is paid for once. A restart that already has the evidence re-reads
// it rather than running the check again — create-if-absent is what makes that
// true, and a record that could be overwritten would not be evidence.
func TestARestartReusesTheGateEvidenceItAlreadyPaidFor(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageGatePassed)})
	killedAfter(t, err, "a1", StageGatePassed)

	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	restarted, _, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}

	store, err := runstate.Open(runstate.Options{
		Repo: clone.Dir, Remote: "origin", Branch: restarted.IntegrationBranch(), RunID: restarted.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	key := evidenceKey("a1", 1, "tree")
	evidence, ok, err := store.Evidence(key)
	if err != nil || !ok {
		t.Fatalf("the gate's evidence for a1 is not on origin: %v", err)
	}
	if evidence.Result != "pass" {
		t.Errorf("evidence %s is %s", key, evidence.Result)
	}
	// One record, and only one: the restart did not mint a second.
	keys := store.EvidenceKeys()
	seen := 0
	for _, k := range keys {
		if k == key {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("evidence keys %v carry %s %d times", keys, key, seen)
	}
}
