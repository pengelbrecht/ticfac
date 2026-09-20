package reconcile

import (
	"context"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The recorded no-commits rule (tick 19l), at the run: the rule decides
// whether an empty branch is a failure for the ROLE, collect's verdict carries
// it, and the feed line for a collected attempt states the role's own answer
// and the run's verdict SEPARATELY — because one line that read
// "closeout-epic answered failed (no-commits)" over a report that said
// DONE_WITH_CONCERNS sent the pwp run's diagnosis looking for a worker that
// had declared failure. It had not.

// The pwp shape, end to end: the close-out answers DONE_WITH_CONCERNS over an
// empty branch. The rule says an empty close-out branch IS a failure — the
// retro and the learnings it is permitted to write are its deliverable — so
// the run refuses the attempt and leaves the tick open, and the feed line says
// BOTH answers, attributed to the party that gave each.
func TestTheFeedStatesTheRolesAnswerAndTheRunsVerdictSeparately(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "closeout_nocommit"})
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "closeout_nocommit"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s, want failed: a close-out over an empty branch is an undelivered "+
			"deliverable, not a completed one", result.State)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCollect {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedCollect)
	}
	if result.Failure.TickID != "co" {
		t.Fatalf("the refused tick is %s, want co", result.Failure.TickID)
	}

	// The feed line for the collected attempt, where the two answers differ:
	// the role's own status, and the run's verdict, stated as two claims by two
	// parties — and never a sentence that reads as the worker declaring the
	// verdict.
	line := collectedLineFor(t, f, "co")
	if !strings.Contains(line, "the closeout-epic job answered DONE_WITH_CONCERNS") {
		t.Errorf("the collected line does not state the role's own answer separately: %s", line)
	}
	if !strings.Contains(line, "the run's verdict is no-commits (failed)") {
		t.Errorf("the collected line does not state the run's verdict separately: %s", line)
	}
	if strings.Contains(line, "answered failed") || strings.Contains(line, "answered no-commits") {
		t.Errorf("the collected line attributes the run's verdict to the role: %s", line)
	}

	// The rule has teeth: the close-out tick is NOT closed behind an answer
	// whose branch delivered nothing, and the refusal says whose verdict the
	// failure is.
	current, err := f.Tracker.Show(context.Background(), "co")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Error("the close-out tick was closed behind an empty branch: the recorded rule would be a word nobody acts on")
	}
}

// The other half of the same rule: a review whose deliverable is its answer
// collects an empty branch as ready-to-merge — the verdict is the role's rule,
// not an incidental failure — and the run proceeds on the answer. The
// review's untriaged finding no longer refuses the REVIEW's close (tick aqm):
// the review closes and the close-out holds over the finding; what is under
// test here is that collect was not what stopped the review.
func TestAReviewWithNoCommitsIsNotAFailureAtTheRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "review_finding"})
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "review_finding"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedFindingUntriaged {
		t.Fatalf("failure %+v, want the review's untriaged finding holding the close-out — collect must not be the "+
			"thing that refused the review", result.Failure)
	}
	if result.Failure.TickID != "co" {
		t.Fatalf("failure tick %s, want co: the review closed and its finding rides to the close-out",
			result.Failure.TickID)
	}
	if got := f.Tracker.count("close:rv"); got != 1 {
		t.Fatalf("rv was closed %d times, want 1: the review's own answer stands; the finding holds the close-out", got)
	}

	line := collectedLineFor(t, f, "rv")
	if !strings.Contains(line, "the review-epic job answered DONE") {
		t.Errorf("the collected line does not state the role's own answer: %s", line)
	}
	if !strings.Contains(line, "the run's verdict is ready-to-merge (succeeded)") {
		t.Errorf("the review's empty branch did not collect by its own rule: %s", line)
	}
}

// collectedLineFor reads the run's feed and returns the StageCollected detail
// for one tick — the line a non-participant subscriber reads.
func collectedLineFor(t *testing.T, f *fixture, tick string) string {
	t.Helper()
	events, err := runfeed.Read(runfeed.Path(f.Repo.Dir, "r-fixture"))
	if err != nil {
		t.Fatalf("the run left no feed a non-participant can read: %v", err)
	}
	for _, event := range events {
		if event.Stage == StageCollected && event.TickID != nil && *event.TickID == tick {
			return event.Detail
		}
	}
	t.Fatalf("the feed carries no collected line for %s", tick)
	return ""
}

// The review's release is the review's alone: the default an implement-tick
// collects an empty branch under is unchanged, proven here against the same
// fixture the rule was recorded for.
func TestThePlainCollectStillRefusesAnImplementTicksEmptyBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "nocommit"})
	_, result, err := f.run(f.Repo, fixtureOptions{mode: "nocommit"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedCollect {
		t.Fatalf("the run failed as %+v, want %s: the recorded rule released one role, not the commit "+
			"requirement itself", result.Failure, RefusedCollect)
	}
	if result.Failure.TickID != "a1" {
		t.Fatalf("the refused tick is %s, want a1", result.Failure.TickID)
	}
	line := collectedLineFor(t, f, "a1")
	if !strings.Contains(line, "the implement-tick job answered DONE") ||
		!strings.Contains(line, "the run's verdict is no-commits (failed)") {
		t.Errorf("the collected line does not state the two answers separately: %s", line)
	}
}
