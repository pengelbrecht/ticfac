package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// A boot that fails because the epic does not exist on the submitted tree
// (ticfac tick rf3). The incident is the first per-tick Cloudflare smoke run:
// the operator submitted a commit carrying an epic, the container cut its
// integration branch from the REMOTE'S DEFAULT BRANCH instead, and the
// tracker read the epic from that cut — where it did not exist. `tk` failed on
// opening `.tick/issues/<epic>.json`, the boot exited 1, and the Run Workflow
// re-booted the container into the identical failure until a person stopped it.
//
// The cut-point half of the tick is PR #30: `--base` is the submitted commit
// and the fold brings the default branch in. This is the OTHER half: a boot
// that reaches this refusal must STOP with it, as a refusal of its own class —
// recorded, durable, and named — because the missing epic is a fact about the
// submission that no replacement container can read a different answer on.

// The acceptance, at the layer that owns it: a run whose epic record is absent
// from the tree the tracker reads does not return an operational error (which a
// caller re-runs, and a cloud supervisor re-boots) — it FAILS, with a refusal
// that names the epic, the record the tree does not carry, and what the
// operator submitted.
func TestAnEpicAbsentFromTheSubmittedTreeStopsTheRunWithItsOwnReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	tracker, _ := newRepoTracker(t, f.Repo.Dir)

	// The tracker carries the epic's WORK but not the epic's own record: the
	// submitted commit holds a tick that names the epic as parent, and no
	// `.tick/issues/qeu.json` — exactly what the smoke run submitted.
	seedTracker(t, f.Repo,
		tk.Tick{ID: "a1", Title: "the tick the run was cut for", Status: "open", Type: "task", Parent: "qeu"},
	)

	opts := f.options(f.Repo, fixtureOptions{})
	opts.Tracker = tracker
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run returned an operational error (%v) — a missing epic is a verdict about the "+
			"submission, not a fault to re-run", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedEpicAbsent {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedEpicAbsent)
	}
	for _, want := range []string{"qeu", ".tick/issues/qeu.json"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, result.Failure.Message)
		}
	}
	if !strings.Contains(result.Failure.Message, "no such file") {
		t.Errorf("the refusal does not carry the tracker's own complaint: %s", result.Failure.Message)
	}

	// Nothing was dispatched, nothing was paid for: the refusal fires at
	// admission, before a tick is claimed.
	for _, event := range r.Journal() {
		if event.Stage == StageDispatched {
			t.Errorf("the run dispatched %s over an epic the submitted tree does not carry", event.Tick)
		}
	}

	// And the refusal is DURABLE: the checkpoint on origin says the run failed
	// and why, so neither a reboot nor a retyped command walks into it blind.
	store := openStore(t, f, r)
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("the run left no checkpoint: %v", err)
	}
	if checkpoint.State != runstate.StateFailed || !strings.Contains(checkpoint.Reason, ".tick/issues/qeu.json") {
		t.Errorf("the checkpoint says %s: %s", checkpoint.State, checkpoint.Reason)
	}
}

// The same stop under SUPERVISION (the way the container runs it): the
// supervisor continues across the stops that need nobody, and a missing epic
// is a person's — resubmitting from a commit that carries the epic is a
// decision, not a retype. The run must halt on the first incarnation, not
// spin through the cap.
func TestASupervisedRunHaltsOnAnEpicAbsentFromTheSubmittedTree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	tracker, _ := newRepoTracker(t, f.Repo.Dir)

	seedTracker(t, f.Repo,
		tk.Tick{ID: "a1", Title: "the tick the run was cut for", Status: "open", Type: "task", Parent: "qeu"},
	)

	opts := f.options(f.Repo, fixtureOptions{})
	opts.Tracker = tracker
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Supervise(context.Background())
	if err != nil {
		t.Fatalf("the supervised run returned an operational error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedEpicAbsent {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedEpicAbsent)
	}
	if len(result.Resumes) != 0 {
		t.Errorf("the supervisor continued %d time(s) across a missing epic — every continuation reaches "+
			"the identical refusal", len(result.Resumes))
	}
	if result.Halt == "" {
		t.Error("the supervisor halted without saying why")
	}
}
