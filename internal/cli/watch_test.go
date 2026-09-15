package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// `ticfac watch` is the consumer the run event feed was built for (tick 0z0):
// the feed says a run ended holding an attempt, and nothing read it — the
// operator noticed by looking. Watch subscribes like `events --follow`, and
// when the run stops holding something for a person it SAYS SO to a human:
// which tick, which attempt, why, and the command that moves it on. Its exit
// code is the condition an orchestrator waits on, so the stall becomes an
// answer instead of silence.

func TestWatchSurfacesARunThatEndedHoldingAnAttempt(t *testing.T) {
	repo := t.TempDir()
	attempt := 3
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-1", "nkf", &attempt, "dispatched", "attempt 3 started as run-x/tick-nkf/attempt-3"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-1", "nkf", &attempt, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 4, 0, time.UTC), "r-1", "", nil, "run_finished",
		"failed: nkf did not pass"))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d for a run that ended holding an attempt; stderr %q", code, ExitHeld, stderr.String())
	}
	// What surfaces names WHICH TICK and WHY — the acceptance criterion —
	// and says what moves the hold on, carry and all.
	for _, want := range []string{"nkf", "attempt 3", "attempt_unaddressed", "--carry-work", "settle"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the alert does not name %q: %q", want, stderr.String())
		}
	}
	// The events themselves are still printed, so a person watching sees the
	// whole run, and the hold is visible on stdout too.
	if !strings.Contains(stdout.String(), reconcile.StageRunHeld) {
		t.Errorf("the held line never printed on stdout: %q", stdout.String())
	}
}

func TestWatchReportsARunThatEndedOnItsOwn(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-1", "", nil, "budget_set", "the effective budget is $8.00"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-1", "", nil, "run_finished", "completed: every tick closed"))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d for a run that ended without holding anything; stderr %q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a run that held nothing raised the hold alert: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

func TestWatchNeedsExactlyOneRunID(t *testing.T) {
	for _, args := range [][]string{{"watch"}, {"watch", "a", "b"}, {"watch", ""}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
}

func TestWatchNamesARunThatNeverRanHere(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"watch", "--repo", t.TempDir(), "r-none"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("a run with no feed and no live claim was watched without complaint")
	}
	if !strings.Contains(stderr.String(), "no feed for run r-none") {
		t.Errorf("stderr %q does not say what a missing feed means", stderr.String())
	}
}

func TestWatchFollowsUntilTheRunEnds(t *testing.T) {
	repo := t.TempDir()
	attempt := 2
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "dispatched", "attempt 2 started"))

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	}()

	// The watch prints the line that stands, then follows. No interval is
	// named anywhere: the lines arrive because the subscription is open, and
	// this waits on the CONDITION (the printed line), never on a guess.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "dispatched") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "dispatched") {
		t.Fatalf("the standing line never printed: %q", stdout.String())
	}
	// The terminal line ends the watch on its own: a watcher must not outlive
	// the run it watches.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, reconcile.StageRunHeld, "attempt_unaddressed: nobody can say whether it is running"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: a1 did not pass"))

	var got int
	select {
	case got = <-code:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != ExitHeld {
		t.Fatalf("exit code %d, want %d", got, ExitHeld)
	}
	if !strings.Contains(stderr.String(), "HOLDING tick a1") {
		t.Errorf("the alert does not name the held tick: %q", stderr.String())
	}
}
