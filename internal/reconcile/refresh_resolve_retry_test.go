package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The base fold's resolve under the same allowance as a tick's
// (role_allowance.go): a fold resolve that failed without answering — its
// runner exited without a report, as vqc attempt 50's resolve did on
// epic-2jn — does not spend the fold's one resolve.

// A fold resolve that committed its resolution and exited without a report is
// retried from that resolution, and the run continues.
//
// serial: this test states the process environment (CONFLICT_SYNC) for the
// fake runner's resolve workers to count their starts in, and t.Setenv
// forbids a parallel test.
func TestABaseFoldResolveThatExitsWithoutAReportIsRetriedFromItsResolution(t *testing.T) {
	t.Setenv("CONFLICT_SYNC", t.TempDir())
	opts := fixtureOptions{mode: "conflict_resolve_noreport"}
	f := newFixture(t, opts)
	_, mainHead := baseFoldConflict(t, f)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a fold resolve that never answered is not the fold's one resolve",
			result.State, result.Failure)
	}
	starts := resolveStartsIn(t, filepath.Join(os.Getenv("CONFLICT_SYNC"), "resolve.starts"))
	if len(starts) != 2 || starts[0] == starts[1] {
		t.Fatalf("the run started fold resolves %v; want one that failed without answering and one retry of its own", starts)
	}
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", mainHead, refFor("epic/qeu")) {
		t.Errorf("epic/qeu does not carry main at %s after the retried fold", short(mainHead))
	}
	decisions := baseFoldDecisions(t, r)
	if len(decisions) != 2 {
		t.Fatalf("recorded %d fold resolves, want the failed job's and the merged one's", len(decisions))
	}
	failed, merged := decisions[0], decisions[1]
	if failed.Response["status"] != "failed" || failed.Response["failure"] != failureOperational {
		t.Errorf("the first fold resolve is recorded %v/%v, not failed/operational",
			failed.Response["status"], failed.Response["failure"])
	}
	committed, _ := failed.Response["resolve_head"].(string)
	if committed == "" {
		t.Fatal("the failed fold resolve's committed head is not recorded")
	}
	if merged.Response["status"] != "merged" || merged.Response["resolve_head"] != committed {
		t.Errorf("the fold was not finished from the resolution %s the failed job committed: %v",
			short(committed), merged.Response)
	}
}

// Fold resolves that never answer are bounded: 1+maxOperationalRetries starts,
// then the stop naming every job.
//
// serial: t.Setenv, as above.
func TestBaseFoldResolvesThatNeverAnswerAreBoundedAndTheStopNamesEveryOne(t *testing.T) {
	t.Setenv("CONFLICT_SYNC", t.TempDir())
	opts := fixtureOptions{mode: "conflict_resolve_silent"}
	f := newFixture(t, opts)
	baseFoldConflict(t, f)

	_, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed || result.Failure == nil || result.Failure.Reason != RefusedBaseRefresh {
		t.Fatalf("the run ended %s (%+v), want the %s stop", result.State, result.Failure, RefusedBaseRefresh)
	}
	starts := resolveStartsIn(t, filepath.Join(os.Getenv("CONFLICT_SYNC"), "resolve.starts"))
	if len(starts) != 1+maxOperationalRetries {
		t.Fatalf("the run started %d fold resolves (%v), want %d", len(starts), starts, 1+maxOperationalRetries)
	}
	for _, want := range []string{"deps.txt", "failed without delivering a resolution",
		"base-fold-1-", "base-fold-2-", "base-fold-3-"} {
		if !strings.Contains(result.Failure.Message, want) {
			t.Errorf("the stop does not name %q: %s", want, result.Failure.Message)
		}
	}
}
