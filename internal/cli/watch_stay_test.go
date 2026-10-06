package cli

// Tick 2xk: the dashboard stays open for drill-in after the run has ended.
// watch used to draw one final frame for an ended run and exit at once, so
// enter (the tick drill-in) and e (the whole feed) were reachable only
// while the run lived — and after a run stops or fails is exactly when a
// person wants the per-tick attempts, the reasons and the gate evidence
// (the finding this tick absorbed: `ticfac watch epic-hn6` against a
// stopped run exited 7 immediately, in a pty, one frame and gone).
//
// On a TERMINAL WITH KEYS the live view now stands past the run's own end:
// the end is kept above the block once, the dashboard keeps answering
// j/k/enter/e, and q or Ctrl-C closes it through the run's own ending —
// the same last word and the same exit class the one-frame exit gave,
// never the interrupted class (5), which a caller would read as "the run
// is still going". Keyless — no keyboard, or the attach `ticfac run` and
// `ticfac run --cloud` bring up — the end keeps exiting at once, because a
// watch nobody can close must not hang the command that owns its exit.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// TestWatchStaysOpenForDrillInAfterTheRunEnded is the evidence's own case:
// a run that had ALREADY ended when the watch started, watched on a
// terminal with keys. The old behaviour drew one frame and exited 7 at
// once; the dashboard now stands, enter and e answer, and q closes it with
// the ended run's own class.
func TestWatchStaysOpenForDrillInAfterTheRunEnded(t *testing.T) {
	fakeTerminal(t)
	keys, restored := fakeKeys(t)

	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)

	// The run has already ended — a deliberate stop, the shape the
	// evidence carries: nobody claims it, and its own last word says so.
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(now, runID, "", nil,
		reconcile.StageRunFinished, "stopped: the operator stopped the run"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "50ms", runID}, &stdout, &stderr)
	}()

	// The frame stands, and the end is kept above the block — once.
	watchWaitsFor(t, "the dashboard over an ended run", func() bool {
		return strings.Contains(stdout.String(), "the second tick") &&
			strings.Contains(stdout.String(), "has ended")
	}, &stdout, &stderr)
	if n := strings.Count(stdout.String(), "has ended"); n != 1 {
		t.Errorf("the ended-run hint was kept %d times, want exactly 1:\n%s", n, stdout.String())
	}

	// The watch has NOT exited: the old behaviour returned at once, one
	// frame and exit 7, and drill-in was unreachable.
	select {
	case got := <-code:
		t.Fatalf("the watch exited %d at once for a run that had already ended — the dashboard never stood for drill-in", got)
	default:
	}

	// Drill-in answers on the ended dashboard: enter opens a tick's own
	// story — the attempts, the reasons, the evidence — and it is the
	// cursor's tick, not the first in-flight one (nothing is in flight).
	keys <- watchKeyDown
	keys <- watchKeyEnter
	watchWaitsFor(t, "the tick view", func() bool {
		return strings.Contains(stdout.String(), "[esc] back")
	}, &stdout, &stderr)

	// e opens the whole feed, where the run's own terminal line is.
	keys <- watchKeyEscape
	keys <- watchKeyFeed
	watchWaitsFor(t, "the feed view with the run's last word", func() bool {
		return strings.Contains(stdout.String(), "run_finished: stopped: the operator stopped the run")
	}, &stdout, &stderr)

	// q closes the dashboard through the run's own ending — the stopped
	// run's class (7), the code the one-frame exit gave — never the
	// interrupted class a live watch's q answers with. The feed view's q
	// only comes back down to the dashboard, so the closing q is the
	// dashboard's own.
	keys <- watchKeyEscape
	keys <- watchKeyQuit
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("q never closed the dashboard over the ended run;\nstdout:\n" + stdout.String())
	}
	if got != exitCancelled {
		t.Fatalf("exit code %d, want %d — the ended run's own cancelled class, not the interrupted watch's;\nstderr:\n%s",
			got, exitCancelled, stderr.String())
	}
	if !*restored {
		t.Error("the terminal's raw mode was never restored before the last words")
	}
	// The last word rides the exit the way it always did: after the
	// restore, a plain newline, the buffer's end.
	if !strings.HasSuffix(stdout.String(), "run_finished: stopped: the operator stopped the run\n") {
		t.Errorf("the run's last word is not the buffer's plain-newline end:\n%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "ended CANCELLED") {
		t.Errorf("the end does not say how the run ended:\n%s", stderr.String())
	}
}

// TestWatchStaysOpenWhenTheRunEndsWhileWatched: the same stay for a run
// that ends WHILE the watch stands on it — the moment a person most wants
// the attempts and the gate evidence is the moment the old watch left.
func TestWatchStaysOpenWhenTheRunEndsWhileWatched(t *testing.T) {
	fakeTerminal(t)
	keys, restored := fakeKeys(t)

	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "50ms", runID}, &stdout, &stderr)
	}()

	watchWaitsFor(t, "the live dashboard", func() bool {
		return strings.Contains(stdout.String(), "the second tick")
	}, &stdout, &stderr)

	// The run ends under the watch: the end is kept above the block, and
	// the dashboard stands.
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "completed: every tick closed behind the gate"))
	life.Release("ended")
	watchWaitsFor(t, "the end kept above the block", func() bool {
		return strings.Contains(stdout.String(), "has ended")
	}, &stdout, &stderr)
	select {
	case got := <-code:
		t.Fatalf("the watch exited %d the moment the run ended, before q — drill-in never answered past the end", got)
	default:
	}

	keys <- watchKeyQuit
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("q never closed the dashboard over the ended run")
	}
	if got != 0 {
		t.Fatalf("exit code %d, want 0 for a run that ended clean; stderr:\n%s", got, stderr.String())
	}
	if !*restored {
		t.Error("the terminal's raw mode was never restored before the last words")
	}
	if !strings.HasSuffix(stdout.String(), "run_finished: completed: every tick closed behind the gate\n") {
		t.Errorf("the run's last word is not the buffer's plain-newline end:\n%q", stdout.String())
	}
}

// TestRunAttachDoesNotStayOpenWhenTheRunEnds: the attach `ticfac run` puts
// up is the same live view, but its exit belongs to the run command — the
// pane an agent or a script owns must return when the run does, not wait
// for a q nobody will type. The attach ends with the run even with keys
// attached; the dashboard that stays open is `ticfac watch`'s own answer.
func TestRunAttachDoesNotStayOpenWhenTheRunEnds(t *testing.T) {
	fakeTerminal(t)
	fakeKeys(t)

	now := time.Now()
	repo, home := modelFixture(t, now)
	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)

	life, err := runlife.Claim(repo, "epic-rmod")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- runBody(context.Background(), t, []string{"--repo", repo, "rmod"}, &stdout, &stderr)
	}()
	watchWaitsFor(t, "the attached dashboard", func() bool {
		return strings.Contains(stdout.String(), "attaching") && strings.Contains(stdout.String(), "the second tick")
	}, &stdout, &stderr)

	writeFeedEvent(t, repo, "epic-rmod", runfeed.NewEvent(time.Now(), "epic-rmod", "", nil,
		reconcile.StageRunFinished, "completed: every tick closed behind the gate"))
	life.Release("ended")

	// No key is ever sent: the attach must return on the run's own end.
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the attach never returned when the run ended — it is waiting for a q nobody will type;\nstdout:\n" + stdout.String())
	}
	if got != 0 {
		t.Fatalf("exit code %d, want 0 for a run that ended clean under its attach", got)
	}
}
