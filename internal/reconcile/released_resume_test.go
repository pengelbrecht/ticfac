package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A worker released after its ready-to-merge verdict is never re-judged as
// cancelled on resume (tick o3q).
//
// hol (#141) gave a reported worker a bounded grace to exit before Cancel
// records its durable cancellation, so a release that catches a worker in its
// own tail no longer renames a collected attempt `cancelled` for every later
// reader. The grace is bounded, though: a worker still running PAST it is
// stopped and cancelled all the same — by the run's own release, over a verdict
// the run has already collected — and a restart that re-read the executor
// before its own records collected the attempt again as `cancelled`, rejected
// it, and redid work the run had already judged ready-to-merge.
//
// The resume must read its own verdict FIRST: the collect's ruling is durable
// in the run's records, and a cancellation that followed it cannot override it.
//
// serial: this test states the process environment (LINGER_TICK) for the fake
// runner's lingering worker, and t.Setenv forbids a parallel test.
func TestAReleasedWorkerThatOutlivesTheGraceIsNotRejudgedCancelledOnResume(t *testing.T) {
	shorttest.EndToEnd(t)
	// a1 is the worker that keeps running past the grace: it reports and then
	// lingers on long after it, while the cancel its release issues waits out
	// the grace the fixture runs (the production grace is ten seconds; the
	// harness runs every bound at its own cadence) and records the durable
	// cancellation over the verdict the run has just collected.
	t.Setenv("LINGER_TICK", "a1")
	f := newFixture(t, fixtureOptions{mode: "linger-past-grace"})
	f.settleGrace = 300 * time.Millisecond

	// Incarnation one: a1 is collected ready-to-merge, and its worker — still
	// running past the grace — is released over the verdict the run has just
	// recorded. The cut lands the moment the release is done, before the
	// merge: exactly the window whose restart re-judged the attempt.
	first, _, err := f.run(f.Repo, fixtureOptions{mode: "linger-past-grace", stopAfter: stopAt("a1", StageCleanedUp)})
	killedAfter(t, err, "a1", StageCleanedUp)

	// The incident's state, before the resume: the release recorded a durable
	// cancellation over the collected attempt — a worker that outlived the
	// grace, stopped by the run's own release.
	state, found := findAttemptState(filepath.Join(f.StateRoot, "r-fixture", "a1", "1"))
	if !found {
		t.Fatalf("no attempt state for a1's first attempt under %s", f.StateRoot)
	}
	if _, err := os.Stat(filepath.Join(state, "cancel.json")); err != nil {
		t.Fatalf("the release recorded no durable cancellation over a1's collected attempt: %v", err)
	}

	// And the run's own records hold the verdict it ruled, on origin — the
	// record a resume reads first, the way it reads a recorded rejection.
	store, err := runstate.Open(runstate.Options{
		Repo: f.Repo.Dir, Remote: "origin", Branch: first.IntegrationBranch(), RunID: first.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	history, err := store.CheckpointHistory()
	if err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, checkpoint := range history {
		if strings.Contains(checkpoint.Reason, "is collected (ready-to-merge)") &&
			strings.Contains(checkpoint.Reason, first.attemptName("a1", 1)) {
			recorded = true
		}
	}
	if !recorded {
		t.Error("no checkpoint on origin records a1's collected verdict: a resume has nothing to read before " +
			"the executor's answer, so the release's own cancellation renames the attempt for every later reader")
	}

	// Incarnation two, from a fresh checkout: everything it needs is on origin
	// — the recorded verdict, the attempt's kept branch — and the executor
	// state it re-reads is the state the release left, cancel.json and all.
	restarted := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "restarted"))
	resumed, result, err := f.run(restarted, fixtureOptions{mode: "linger-past-grace"})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s (%s): an attempt the run already judged is finished from its "+
			"recorded verdict, not re-judged as cancelled", result.State, result.Reason)
	}
	for _, id := range []string{"a1", "a2", "b1", "rv", "co"} {
		current, err := f.Tracker.Show(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("%s is %s, want closed", id, current.Status)
		}
	}

	// a1 is integrated FROM ITS RECORDED VERDICT: rejected nowhere on the
	// resume, and its attempt started once — the resume adopts and finishes
	// the attempt it has already judged, it does not dispatch a fresh one over
	// work it would then redo.
	for _, event := range resumed.Journal() {
		if event.Tick == "a1" && event.Stage == StageRejected {
			t.Errorf("a1 was rejected on resume: %s", event.Detail)
		}
	}
	if starts := f.startCount(f.dispatch("a1").JobID); starts != 1 {
		t.Errorf("a1's first attempt was started %d times, want once: a resumed attempt is adopted, never started again", starts)
	}
	for _, event := range resumed.Journal() {
		if event.Tick == "a1" && event.Stage == StageDispatched {
			t.Errorf("a1 was dispatched again on resume (%s): work the run had already judged is finished, not redone",
				event.Detail)
		}
	}
	noted := false
	for _, event := range resumed.Journal() {
		if event.Tick == "a1" && event.Stage == StageResumed &&
			strings.Contains(event.Detail, "the recorded verdict stands") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the resume never said it read a1's recorded verdict over the cancellation: %v", resumed.Stages("a1"))
	}

	// And the work the recorded verdict was about is on the integration
	// branch: the release kept it, the resume merged it.
	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "read"))
	if work := readGitBlob(t, clone.Dir, "origin/epic/qeu", "work-a1.txt"); work == "" {
		t.Error("the integration branch carries none of a1's work: the verdict the resume read recorded a head " +
			"nothing merged")
	}
}
