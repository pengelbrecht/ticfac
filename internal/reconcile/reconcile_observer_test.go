package reconcile

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// TestReviewH0fAnOperatorObservingARunEndsNothing is Phase 3 acceptance A1,
// as the acceptance criterion is written: a fixture epic runs TO COMPLETION
// under the real reconciler while a concurrent loop does all three things an
// operator does to a live run, in the run's own checkout — `git fetch`, the
// exact reads of `ticfac findings` (open the store, fetch, list the drafts),
// and reading the run's branch.
//
// The two runstate-level tests this builds on race a BARE store
// (TestAnOperatorFetchingInTheRunsCheckoutEndsNothing, plain git fetch vs one
// store; TestASecondTicfacProcessDoesNotEndTheRun, store vs store). Neither
// drives a reconciler, so neither exercised the run that actually died in the
// pwp review: it was killed by its OWN next fetch failing while an operator
// observed it — a fatal error only a running reconciler turns into a dead
// run. This test is that shape, and it is the acceptance criterion's, not a
// proxy: the run must come back completed, and every observation must have
// actually happened at least once or the pass is vacuous.
//
// The shape is the reviewer's, recovered from the h0f attempt
// (runs/epic-9pd/h0f/repro/reconcile_observer_test.go.txt), minus its
// environment gates: the repro gated each observation behind OBS_* variables
// to bisect which one killed a run; a committed test runs all three, because
// the criterion is all three at once and a gate that defaults off is a test
// that gates skip.
func TestReviewH0fAnOperatorObservingARunEndsNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t, fixtureOptions{})
	dir := f.Repo.Dir

	// The run this fixture starts, addressed the way an operator addresses
	// it: the same checkout, the same remote, the same integration branch and
	// run id the reconciler derived (epic qeu, run r-fixture).
	const branch = "epic/qeu"
	const runID = "r-fixture"

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var fetches, findings, branchReads, loops, findingsErrs atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}

			// One: `git fetch origin`, in the run's own checkout. Before the
			// FETCH_HEAD and refmap fixes (tick wdb) this clobbered the head
			// the store resolved, emptied its view of the run's own state,
			// and ended the run on conflict_exists.
			//
			// Through the harness's runner, so this loop starts no
			// background maintenance of its own (tick qsn): what it is here
			// to race is the REFS a plain fetch writes — FETCH_HEAD and the
			// remote-tracking ref — and a few thousand detached repacks of
			// the fixture's object store are not that. The maintenance a
			// real operator's fetch would start is tick mel's subject, and
			// maintenance_test.go holds it down against the run's own git.
			if err := harnessCommand("git", "-C", dir, "fetch", "--quiet", "origin").run(""); err == nil {
				fetches.Add(1)
			}

			// Two: `ticfac findings` — exactly what the command does
			// (internal/cli's openFindingsStore): open a store for THIS run in this
			// checkout, fetch, list the drafts. Before the per-instance peek
			// ref (review h0f finding 1) this raced the live run on one ref's
			// lock, and the loser's exit ended the run. A fetch error before
			// the run has created its branch is the operator asking too
			// early, not a defect: counted, not fatal.
			if store, err := runstate.Open(runstate.Options{Repo: dir, Remote: "origin", Branch: branch, RunID: runID}); err == nil {
				if _, err := store.Fetch(); err == nil {
					if _, err := store.Findings(); err == nil {
						findings.Add(1)
					} else {
						findingsErrs.Add(1)
					}
				} else {
					findingsErrs.Add(1)
				}
			}

			// Three: read the branch — the log of what the run has landed so
			// far, the way an operator watching a run does.
			if err := harnessCommand("git", "-C", dir, "log", "--oneline", "-5", "origin/"+branch).run(""); err == nil {
				branchReads.Add(1)
			}

			time.Sleep(time.Millisecond)
			loops.Add(1)
		}
	}()

	_, result, err := f.run(f.Repo, fixtureOptions{})
	close(stop)
	wg.Wait()

	t.Logf("observer: %d loops, %d fetches, %d findings lists, %d branch reads, %d findings errors",
		loops.Load(), fetches.Load(), findings.Load(), branchReads.Load(), findingsErrs.Load())

	if err != nil {
		t.Fatalf("the run ended under observation: %v\n"+
			"An operator fetching, listing findings and reading the branch of a live run "+
			"must never end it. See Phase 3 acceptance A1, review h0f finding 2.", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s under observation: %s\n"+
			"An operator fetching, listing findings and reading the branch of a live run "+
			"must never end it. See Phase 3 acceptance A1, review h0f finding 2.",
			result.State, result.Reason)
	}

	// The observations must have happened, or the pass is vacuous: a loop that
	// never once fetched, listed or read cannot say the run survived any of
	// the three. Before the run creates its branch the first two can fail, so
	// the claim is "at least once", not "always".
	if fetches.Load() == 0 {
		t.Fatal("the observer never fetched in the run's checkout: the run was not observed at all")
	}
	if findings.Load() == 0 {
		t.Fatal("the observer never listed the run's findings: the run was not observed at all")
	}
	if branchReads.Load() == 0 {
		t.Fatal("the observer never read the run's branch: the run was not observed at all")
	}
}
