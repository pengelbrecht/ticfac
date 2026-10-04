package cli

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
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

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d for a run that ended holding an attempt; stderr %q", code, ExitHeld, stderr.String())
	}
	// What surfaces names WHICH TICK and WHY — the acceptance criterion —
	// and says what moves the hold on, carry and all. The settle command is
	// addressed by the run's own epic id — a placeholder a person would
	// still have to fill in is not a command.
	for _, want := range []string{"nkf try 1 (run dispatch #3)", "settle r-1 nkf 3 ", "attempt_unaddressed", "--carry-work", "settle",
		// The release command names the run whose store carries the attempt
		// (tick qxj): this run's records live under r-1, not under the epic
		// spelling a settle without --run-id opens.
		"--run-id r-1 --release"} {
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

// The settle command the hold alert names is addressed by the run's OWN
// epic id, never a `<epic-id>` placeholder: the alert exists so a person
// can copy one command, and a placeholder is a second thing to look up.
// The run's id spells the epic (`epic-<id>`), so the command reads the
// epic out of it.
func TestWatchHoldAlertNamesTheEpicNotAPlaceholder(t *testing.T) {
	repo := t.TempDir()
	attempt := 2
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC), "epic-2jn", "t1", &attempt, "dispatched", "t1 try 1 dispatched"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-2jn", "t1", &attempt, reconcile.StageRunHeld,
		"attempt_struck_out: the report names no status"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-2jn", "", nil, "run_finished",
		"failed: t1 did not pass"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-2jn"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticfac settle 2jn t1 2 --release") {
		t.Errorf("the alert does not name the settle command addressed by the epic id: %q", stderr.String())
	}
	// A run under the epic spelling is the one run the bare command already
	// addresses: its alert spells no --run-id (tick qxj).
	if strings.Contains(stderr.String(), "--run-id") {
		t.Errorf("the alert adds --run-id to a command that already defaults to this run's store: %q",
			stderr.String())
	}
	if strings.Contains(stderr.String(), "<epic-id>") {
		t.Errorf("the alert still prints a placeholder instead of the epic id: %q", stderr.String())
	}
}

// The close-out's untriaged-findings hold is cleared by triage, so the
// alert names `ticfac triage <epic>` — never `settle`, which releases an
// attempt and would refuse this one.
func TestWatchHoldAlertNamesTriageForAFindingHold(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 3, 0, time.UTC), "epic-2jn", "rrl", nil, reconcile.StageRunHeld,
		"finding_untriaged: 1 finding(s) this run drafted are still waiting for a person"))
	writeFeedEvent(t, repo, "epic-2jn", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "epic-2jn", "", nil, "run_finished",
		"holding: the close-out waits on untriaged findings"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-2jn"}, &stdout, &stderr)
	if code != ExitHeld {
		t.Fatalf("exit code %d, want %d; stderr %q", code, ExitHeld, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticfac triage 2jn") {
		t.Errorf("the finding hold's alert does not name the triage command: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "ticfac settle") {
		t.Errorf("the finding hold's alert names settle, a command that releases an attempt and refuses this hold: %q",
			stderr.String())
	}
}

// The '<tick>#<n>' prefix is the tick's own TRY (tick h58), not the run-wide
// dispatch number the line's `attempt` field carries. The operator's run: 0ju
// was dispatch 1, mrn 2, and w9b 3, 4 and 5 — so dispatch 5 is w9b#3, and a
// prefix that said w9b#5 read as w9b's fifth try.
func TestWatchPrefixShowsTheTicksTry(t *testing.T) {
	repo := t.TempDir()
	at := time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC)
	for i, tick := range []string{"0ju", "mrn", "w9b", "w9b", "w9b"} {
		n := i + 1
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(at.Add(time.Duration(i)*time.Minute), "r-1", tick, &n,
			"dispatched", fmt.Sprintf("dispatch %d", n)))
	}
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(at.Add(time.Hour), "r-1", "", nil, "run_finished", "completed"))

	var stdout, stderr syncBuffer
	if code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d; stderr %q", code, stderr.String())
	}
	for _, want := range []string{"0ju#1 ", "mrn#1 ", "w9b#1 ", "w9b#2 ", "w9b#3 "} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the watch never printed the prefix %q: %q", want, stdout.String())
		}
	}
	for _, unwanted := range []string{"w9b#4", "w9b#5", "mrn#2"} {
		if strings.Contains(stdout.String(), unwanted) {
			t.Errorf("the watch prefixed a line with the run dispatch number %q: %q", unwanted, stdout.String())
		}
	}
}

func TestWatchReportsARunThatEndedOnItsOwn(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-1", "", nil, "budget_set", "the effective budget is $8.00"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-1", "", nil, "run_finished", "completed: every tick closed"))

	var stdout, stderr syncBuffer
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
		var stdout, stderr syncBuffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
}

func TestWatchNamesARunThatNeverRanHere(t *testing.T) {
	// The run's directory stands — a claim made it and released it — but
	// no line was ever written and nothing claims the run now. An id with
	// no directory at all is a different answer (runid_test.go).
	repo := t.TempDir()
	if err := os.MkdirAll(runlife.Dir(repo, "r-none"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-none"}, &stdout, &stderr)
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

	var stdout, stderr syncBuffer
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
	if !strings.Contains(stderr.String(), "HOLDING a1 try 1 (run dispatch #2)") {
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

	var stdout, stderr syncBuffer
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

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	}()

	// The hold the watch joined is reported — on stderr, where a person
	// reads it, without waiting for the run to end first: the alert is the
	// whole point, and it must not wait for the terminal line to say it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "HOLDING a2 try 1 (run dispatch #2)") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "HOLDING a2 try 1 (run dispatch #2)") {
		t.Fatalf("the watch never reported the hold it joined: stderr %q stdout %q", stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "settle r-1 a2 2 ") || !strings.Contains(stderr.String(), "attempt_unaddressed") {
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
func syncWatchMarker(t *testing.T, repo string, stdout *syncBuffer) {
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

// The exit table's failed class (tick bot, epic 2jn's A4): a run whose own
// terminal line says it FAILED — the integrated gate refused the work, a
// worker answered BLOCKED, the run stopped rather than integrating over an
// unproven change — ended the watch with exit 0, indistinguishable from a
// run that closed every tick behind the gate. The failure is in the
// terminal line's own LEADING state word (the runstate word the reconciler
// checkpointed: "failed: nkf did not pass"), so the watch classifies on
// that word — never on the absence of a run_held, which says only that
// nothing waits for a person's release, not that the run succeeded.
func TestWatchExitsFailedWhenTheRunEndedFailed(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 40, 0, 0, time.UTC), "r-1", "nkf", nil,
		reconcile.StageGateFailed, "the integrated gate refused the work: go test failed"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished,
		"failed: nkf did not pass: the integrated gate refused the work"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit code %d, want %d (the failed class) for a run whose own last line says failed; stderr %q", code, exitGeneric, stderr.String())
	}
	// The failed end is said to the person reading, and it is NOT a hold:
	// nothing waits for a release, the work has to be fixed and the epic
	// run again.
	if !strings.Contains(stderr.String(), "ended FAILED") {
		t.Errorf("the failed end is not said to the person reading the stream: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a run that failed holding nothing raised the hold alert: %q", stderr.String())
	}
	// The resume it names is a command a person can paste (tick gtk): the
	// one statusmodel spells for the run's host, addressed by a real id —
	// never a `<epic-id>` placeholder.
	if strings.Contains(stderr.String(), "<epic-id>") {
		t.Errorf("the failed end names a placeholder, not a command: %q", stderr.String())
	}
	if want := statusmodel.ResumeCommand(statusmodel.HostLocal, "r-1"); !strings.Contains(stderr.String(), want) {
		t.Errorf("the failed end does not name the resume %q: %q", want, stderr.String())
	}
	// The terminal line still prints — the last line says why, and the exit
	// code says which class of ending it was.
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

// A run that DIED without its own run_finished — the process erred, panicked
// or was signalled — is the same failed class on the same seam: run_died
// exists precisely so a death never reads as an ordinary success, and the
// watch must not read it as done either (tick bot).
func TestWatchExitsFailedWhenTheRunDied(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunDied,
		"run-epic: the reconciler returned an operational error"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit code %d, want %d (the failed class) for a run that died without its own run_finished; stderr %q", code, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended FAILED") {
		t.Errorf("the death is not said to the person reading the stream: %q", stderr.String())
	}
}

// TestWatchByEpicIDFollowsTheCloudRunTheEpicHasInTheFactory: `ticfac watch
// <epic-id>` follows the run the factory holds for this checkout's project
// when nothing runs here (tick nyi) — the same one command the operator
// uses for a local run, because the run's id alone should not decide which
// host answers. Before the fix the epic id never looked in the factory and
// the watch refused with "no feed".
func TestWatchByEpicIDFollowsTheCloudRunTheEpicHasInTheFactory(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	cloudRun := cloudRunIDOf("e999")

	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	attempt := 1
	feed := feedLine(t, runfeed.NewEvent(at, cloudRun, "t1", &attempt, "dispatched", "t1 try 1 dispatched"))
	feed += feedLine(t, runfeed.NewEvent(at.Add(time.Minute), cloudRun, "", nil,
		reconcile.StageRunFinished, "completed: every tick of epic1 is closed behind the integrated gate"))
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}}
		case request.Path == "/api/runs/"+cloudRun:
			return 200, map[string]any{"run": map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "completed",
			}}
		case request.Path == "/api/runs/"+cloudRun+"/events":
			return 200, map[string]any{
				"run_id": cloudRun, "state": "completed",
				"text": feed, "bytes": len(feed), "total_bytes": len(feed),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-epic1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d watching the factory's run for epic1, want 0:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the stream never printed the run's own terminal word:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), cloudRun) {
		t.Errorf("stderr does not name the resolution from epic id to the factory's run:\n%s", stderr.String())
	}
}

// A watch that reached the factory's run by EPIC id (tick nyi) names the
// clearing commands by that epic, never by the factory's `run_` plus hex id
// it resolved to (tick gtk): the resolved run's source carries the epic the
// operator typed, so the failed end's resume is one a person can paste.
func TestWatchByEpicIDNamesTheResumeByTheEpicNotTheFactoryRunID(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	cloudRun := cloudRunIDOf("e998")

	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	feed := feedLine(t, runfeed.NewEvent(at, cloudRun, "", nil,
		reconcile.StageRunFinished, "failed: t1 did not pass: the integrated gate refused the work"))
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "failed",
			}}}
		case request.Path == "/api/runs/"+cloudRun:
			return 200, map[string]any{"run": map[string]any{
				"run_id": cloudRun, "epic": "epic1", "project": "acme/project", "state": "failed",
			}}
		case request.Path == "/api/runs/"+cloudRun+"/events":
			return 200, map[string]any{
				"run_id": cloudRun, "state": "failed",
				"text": feed, "bytes": len(feed), "total_bytes": len(feed),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "epic-epic1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit %d for the factory's failed run, want %d:\n%s\n%s", code, exitGeneric, stdout.String(), stderr.String())
	}
	if want := statusmodel.ResumeCommand(statusmodel.HostCloud, "epic1"); !strings.Contains(stderr.String(), want) {
		t.Errorf("the failed end does not name the resume %q by the epic:\n%s", want, stderr.String())
	}
	if bad := statusmodel.ResumeCommand(statusmodel.HostCloud, cloudRun); strings.Contains(stderr.String(), bad) {
		t.Errorf("the failed end names the resume by the factory's run id %q:\n%s", bad, stderr.String())
	}
}

// A run whose own terminal line says CANCELLED (tick rix): runstate's
// cancelled word was vocabulary only, but the resume path's already-terminal
// branch writes run_finished for it and the failed classifier deliberately
// classifies only the failed word — so a cancelled end read as done/0 from
// the watch, indistinguishable from a run that closed every tick behind the
// gate. Cancelled is its own class now, with its own code: the run was
// stopped deliberately, the work is neither done nor failed, and nothing is
// held for a person.
func TestWatchExitsCancelledWhenTheRunEndedCancelled(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 40, 0, 0, time.UTC), "r-1", "a1", nil, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished,
		"cancelled: the operator stopped the run: stop requested"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitCancelled {
		t.Fatalf("exit code %d, want %d (the cancelled class) for a run whose own last line says cancelled; stderr %q", code, exitCancelled, stderr.String())
	}
	// The cancelled end is said to the person reading the stream — and it is
	// neither a failure to fix nor a hold to release.
	if !strings.Contains(stderr.String(), "ended CANCELLED") {
		t.Errorf("the cancelled end is not said to the person reading the stream: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "FAILED") {
		t.Errorf("a cancelled run was spoken of as a failure to fix: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a cancelled run raised the hold alert: %q", stderr.String())
	}
	// The terminal line still prints — the last line says why the run was
	// stopped, and the exit code says which class of ending it was.
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

// Every word the run's own terminal line can spell a deliberate stop with
// is the cancelled class — or the decided answer is a word nothing
// recognizes. The resume path's already-terminal branch replays a cancelled
// checkpoint as "the run is already cancelled: …" (not the state-led word),
// and the cloud's own vocabulary spells its deliberate stop "stopped" —
// the word the overview already classifies one run's row by.
func TestWatchClassifiesEveryCancelledWordTheRunWrites(t *testing.T) {
	for detail, why := range map[string]string{
		"cancelled: the operator stopped the run":          "the state-led word a cancelling reconciler writes",
		"the run is already cancelled: operator cancelled": "the resume path's already-terminal replay",
		"stopped: stop requested by the operator":          "the cloud factory's own word for a deliberate stop",
	} {
		repo := t.TempDir()
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished, detail))
		var stdout, stderr syncBuffer
		code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
		if code != exitCancelled {
			t.Errorf("%s: exit code %d, want %d; stderr %q", why, code, exitCancelled, stderr.String())
		}
	}
	// The control the classifier must not swallow: the resume path's
	// already-terminal replay of a COMPLETED run is the done class, and so
	// is the ordinary state-led "completed" word — an over-eager classifier
	// would make every ended run cancelled.
	for _, detail := range []string{
		"completed: every tick closed behind the gate",
		"the run is already completed: every tick closed behind the gate",
	} {
		repo := t.TempDir()
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Date(2026, 9, 27, 12, 41, 4, 0, time.UTC), "r-1", "", nil, reconcile.StageRunFinished, detail))
		var stdout, stderr syncBuffer
		code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
		if code != exitSuccess {
			t.Errorf("a completed run's ending %q exited %d, want %d (done)", detail, code, exitSuccess)
		}
	}
}

// A person's SIGINT stop of a foreground run-epic (tick vqc): the signal
// handler writes run_died for it, and before this tick the watch classified
// every run_died as the failed class — "the work has to be fixed and the
// epic run again" — for a stop a person chose to make. A deliberate stop is
// the cancelled class's own case, the same distinction rix made for the
// run_finished cancelled words: the handler leads the death line with the
// cancelled state word, and the classifier reads the word, never the prose.
func TestWatchExitsCancelledWhenAPersonStoppedTheRun(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 5, 0, 0, time.UTC), "r-1", "a1", nil, "dispatched", "attempt 1 started"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 6, 2, 0, time.UTC), "r-1", "", nil, reconcile.StageRunDied,
		"cancelled: stopped by a signal (interrupt) before the run finished"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitCancelled {
		t.Fatalf("exit code %d, want %d (the cancelled class) for a run a person stopped with Ctrl-C; stderr %q",
			code, exitCancelled, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended CANCELLED") {
		t.Errorf("the deliberate stop is not said to the person reading the stream: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "FAILED") {
		t.Errorf("a person's stop was spoken of as a failure to fix: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a person's stop raised the hold alert: %q", stderr.String())
	}
	// The terminal line still prints — the last line says how the run was
	// stopped, and the exit code says which class of ending it was.
	if !strings.Contains(stdout.String(), "run_died") {
		t.Errorf("the terminal line never printed: %q", stdout.String())
	}
}

// The control for the split (tick vqc): a SIGTERM is the platform saying
// the container is going away — an eviction, a death like a panic, not a
// person's stop — so its death line carries no state word and stays the
// failed class. Only the interrupt is the cancelled class's own case.
func TestWatchKeepsTheEvictionADeath(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 27, 13, 6, 2, 0, time.UTC), "r-1", "", nil, reconcile.StageRunDied,
		"stopped by a signal (terminated) before the run finished; evacuated: pushed the integration branch"))

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit code %d, want %d (the failed class) for a run the platform evicted; stderr %q",
			code, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended FAILED") {
		t.Errorf("the eviction is not said to the person reading the stream: %q", stderr.String())
	}
}

// The death line's two spellings, straight from the handler (tick vqc):
// SIGINT — a person at a terminal — is led by the cancelled state word, the
// word the classifier branches on, exactly as run_finished's details are
// state-led; SIGTERM — the platform's eviction — keeps the plain death
// sentence, because a death is what it is.
func TestSignalStopDetailLeadsOnlyThePersonStopWithTheCancelledWord(t *testing.T) {
	if got := signalStopDetail(syscall.SIGINT); !strings.HasPrefix(got, string(runstate.StateCancelled)+":") {
		t.Errorf("the SIGINT death line %q is not led by the cancelled state word", got)
	}
	if got := signalStopDetail(syscall.SIGTERM); strings.HasPrefix(got, string(runstate.StateCancelled)+":") {
		t.Errorf("the SIGTERM death line %q is led by the cancelled state word — an eviction is a death, not a stop", got)
	}
	if !strings.Contains(signalStopDetail(syscall.SIGINT), "stopped by a signal") {
		t.Errorf("the SIGINT death line lost the sentence that says what happened: %q", signalStopDetail(syscall.SIGINT))
	}
}
