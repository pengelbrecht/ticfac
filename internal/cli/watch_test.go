package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
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

// The resumed-run case (ticfac tick usx): the feed is append-only per RUN
// ID, so a resumed run appends to a file a previous, failed incarnation of
// the same run id already ended — and the previous incarnation ended
// HOLDING, which is the worst case: a watch that replays the standing feed
// from offset zero reads that run_finished FIRST, exits at once and raises
// the hold alert for a hold the release already settled. That is the same
// defect `events --follow` carried (ticfac tick 55i), in the command built
// to be alerted by it. The resumed run is LIVE — it claims the run — so the
// watch joins the CURRENT incarnation: the previous ending is history, and
// only the current incarnation's own terminal line ends the watch.
func TestWatchOnAResumedRunDoesNotExitOnThePreviousIncarnationsTerminalLine(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	// The previous incarnation: it held tick a1 for a person, and said so
	// on its way to failing — the exact line the old watch replayed as if it
	// were happening now.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as run-x/tick-a1/attempt-1"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: a1 did not pass"))

	// The resumed run is in flight: this process claims it, the way the
	// real second incarnation's own process does at startup.
	life, err := runlife.Claim(repo, "r-1")
	if err != nil {
		t.Fatalf("claim the resumed run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	}()

	// The subscription is live, and it joined the current incarnation: the
	// resumed run's first line reaches the watch, and the previous
	// incarnation's ending does not end it first. This waits on the CONDITION
	// (the watch printing the resumed run's own line), never on a guess about
	// time — on the old code the watch has already returned and this never
	// comes true.
	syncWatchMarker(t, repo, &stdout)
	select {
	case got := <-code:
		t.Fatalf("the watch returned %d before the resumed run said anything — it exited on the previous incarnation's ending; stderr %q", got, stderr.String())
	default:
	}

	// The resumed run ends its own way, and the watch ends on THAT line.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "completed: every tick closed behind the gate"))
	var got int
	select {
	case got = <-code:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never returned after the resumed run ended")
	}
	if got != 0 {
		t.Fatalf("exit code %d, want 0 for a resumed run that ended clean; stderr %q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("the watch raised the hold alert for the hold the previous incarnation already had settled: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "failed: a1 did not pass") {
		t.Errorf("the watch replayed the previous incarnation's terminal line: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "every tick closed behind the gate") {
		t.Errorf("the current incarnation's own terminal line never printed: %q", stdout.String())
	}
}

// The decided run_held semantics (ticfac tick usx): a watch started while the
// run is ALREADY holding an attempt reports the hold it joined — the line
// landed before the watch did, so starting from now would miss the one line
// the command exists for. The run is live here, so this is the joined hold of
// the CURRENT incarnation, not the replayed hold of a previous one.
func TestWatchStartedWhileTheRunHoldsReportsTheHoldItJoined(t *testing.T) {
	repo := t.TempDir()
	first := 1
	// The previous incarnation failed cleanly — no hold of its own — so
	// anything the watch says about a hold can only come from the current
	// incarnation.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &first, "dispatched", "attempt 1 started as run-x/tick-a1/attempt-1"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: attempt 1 of a1 is missing-result"))

	// The current incarnation: live, and already holding before the watch
	// starts.
	second := 2
	life, err := runlife.Claim(repo, "r-1")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a2", &second, "dispatched", "attempt 2 started as run-x/tick-a2/attempt-2"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a2", &second, reconcile.StageRunHeld,
		"attempt_unaddressed: nobody can say whether the attempt is running"))

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	}()

	// The hold the watch joined is reported — on stderr, where a person
	// reads it, without waiting for the run to end first: the alert is the
	// whole point, and it must not wait for the terminal line to say it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "HOLDING tick a2") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "HOLDING tick a2") {
		t.Fatalf("the watch never reported the hold it joined: stderr %q stdout %q", stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "attempt 2") || !strings.Contains(stderr.String(), "attempt_unaddressed") {
		t.Errorf("the alert does not name the held attempt and why: %q", stderr.String())
	}

	// And the run's own last word still ends the watch, holding.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: a2 did not pass"))
	var got int
	select {
	case got = <-code:
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != ExitHeld {
		t.Fatalf("exit code %d, want %d for a run that ended holding an attempt; stderr %q", got, ExitHeld, stderr.String())
	}
	if !strings.Contains(stdout.String(), reconcile.StageRunHeld) {
		t.Errorf("the held line never printed on stdout: %q", stdout.String())
	}
	// The hold reported is the CURRENT incarnation's, from the current
	// incarnation's lines: the previous incarnation's ending is not replayed
	// into the stream the watch is now following (on the old code this line
	// is what fails — the joined hold read as a replayed history).
	if strings.Contains(stdout.String(), "failed: attempt 1 of a1 is missing-result") {
		t.Errorf("the watch replayed the previous incarnation's terminal line: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "attempt 1 started as run-x/tick-a1/attempt-1") {
		t.Errorf("the watch replayed the previous incarnation's dispatch line: %q", stdout.String())
	}
}

// syncWatchMarker proves the watch's subscription is live and joined the
// current incarnation BEFORE anything is asserted about what it did not
// replay: the resumed run writes a marker, the watch prints it, and only a
// watch that did not exit on the previous incarnation's ending can. It
// waits on that condition, never on a guessed interval.
func syncWatchMarker(t *testing.T, repo string, stdout *bytes.Buffer) {
	t.Helper()
	for i := 0; i < 40; i++ {
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Now(), "r-1", "a2", nil, "resumed", fmt.Sprintf("subscription sync marker %d", i)))
		deadline := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			if strings.Contains(stdout.String(), "subscription sync marker") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("the watch never showed a sync marker; it did not join the resumed run's feed: %q", stdout.String())
}
