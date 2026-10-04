package cli

// The wiring half of the live watch (tick 89m): the frame that
// watch_view_test.go pins the content of is REDRAWN IN PLACE on a terminal —
// no alternate screen, scrollback and copy keep working — and the same
// command, on a pipe, keeps the plain one-line-per-event stream it has
// always been (the refusal `events --follow` settled: a stream for machines
// and logs, a glance for people).
//
// The terminal is faked at the seams (watchIsTerminal, watchTerminalSize)
// because a test buffer is not a tty and never will be: what is pinned here
// is the wiring — the frame renders from the model, redraws with cursor
// movement, keeps a line for the events worth remembering ABOVE the block,
// ends on the run's own last word, and exits 3 when what it ended holding
// is a person's to move.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// fakeTerminal makes every writer a 100x30 terminal for one test — and the
// keyboard keyless, because a test has none whatever the host's stdin is
// attached to.
func fakeTerminal(t *testing.T) {
	t.Helper()
	realTTY, realSize := watchIsTerminal, watchTerminalSize
	t.Cleanup(func() { watchIsTerminal, watchTerminalSize = realTTY, realSize })
	watchIsTerminal = func(io.Writer) bool { return true }
	watchTerminalSize = func(io.Writer) (int, int, bool) { return 100, 30, true }
	realKeys := watchAttachKeys
	t.Cleanup(func() { watchAttachKeys = realKeys })
	watchAttachKeys = func(context.Context) (<-chan string, func(), bool) { return nil, nil, false }
}

// fakeKeys replaces the keyboard seam with a channel the test feeds and a
// restore the test can watch, so the key wiring is driven headless the way
// the reducer is.
func fakeKeys(t *testing.T) (chan string, *bool) {
	t.Helper()
	real := watchAttachKeys
	t.Cleanup(func() { watchAttachKeys = real })
	keys := make(chan string, 8)
	restored := new(bool)
	watchAttachKeys = func(context.Context) (<-chan string, func(), bool) {
		return keys, func() { *restored = true }, true
	}
	return keys, restored
}

// watchWaitsFor polls a condition on a short interval until it holds or the
// deadline passes: a condition wait over an observable (what the watch has
// printed), never a guess about the work's timing.
func watchWaitsFor(t *testing.T, what string, holds func() bool, stdout, stderr *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if holds() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s;\nstdout:\n%s\nstderr:\n%s", what, stdout.String(), stderr.String())
}

// threeWaveGraph is the fixture tracker's answer: three waves, the first
// closed, the second the frontier (two ticks), the third behind it.
func threeWaveGraph() *tk.Graph {
	return &tk.Graph{Waves: []tk.GraphWave{
		{Wave: 1, Tasks: []tk.GraphTask{{ID: "t1", Title: "the first tick", Status: "closed"}}},
		{Wave: 2, Tasks: []tk.GraphTask{
			{ID: "t2", Title: "the second tick", Status: "open"},
			{ID: "t3", Title: "the third tick", Status: "open"},
		}},
		{Wave: 3, Tasks: []tk.GraphTask{
			{ID: "t4", Title: "the fourth tick", Status: "open"},
			{ID: "t5", Title: "the fifth tick", Status: "open"},
		}},
	}}
}

// fakeTheTracker swaps the epicGraph seam for one test.
func fakeTheTracker(t *testing.T, graph *tk.Graph) {
	t.Helper()
	real := epicGraph
	t.Cleanup(func() { epicGraph = real })
	epicGraph = func(context.Context, string, string) *tk.Graph { return graph }
}

// TestWatchOnATerminalRendersTheEpicInPlace: the whole epic, always visible,
// redrawn in place — the lifecycle bar with the wave the run is in, the
// active wave's fixed rows, the done and upcoming waves one line each — and
// the events worth remembering kept above the block. The run's own last
// word ends the watch, and the exit code stays the stream's contract.
func TestWatchOnATerminalRendersTheEpicInPlace(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)
	fakeTerminal(t)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	// The frame renders from the model: the phase bar names the wave the run
	// is in, and the frontier's ticks hold fixed rows.
	watchWaitsFor(t, "the phase bar", func() bool {
		return strings.Contains(stdout.String(), "◐ waves 2/3")
	}, &stdout, &stderr)
	watchWaitsFor(t, "the frontier's rows", func() bool {
		out := stdout.String()
		return strings.Contains(out, " t2    the second tick") && strings.Contains(out, " t3    the third tick")
	}, &stdout, &stderr)
	// Every tick of the epic is a fixed row in the dashboard's table, and
	// the whole epic is in every frame — the done wave's ticks as much as
	// the upcoming ones.
	watchWaitsFor(t, "the whole epic's rows", func() bool {
		out := stdout.String()
		return strings.Contains(out, " t1    the first tick") && strings.Contains(out, " t4    the fourth tick") &&
			strings.Contains(out, " t5    the fifth tick")
	}, &stdout, &stderr)
	// Redrawn in place, not appended: the cursor moves back over the frame.
	watchWaitsFor(t, "an in-place redraw", func() bool {
		return strings.Contains(stdout.String(), "\x1b[J")
	}, &stdout, &stderr)
	if strings.Count(stdout.String(), "◐ waves 2/3") < 2 {
		t.Errorf("the frame was never redrawn in place:\n%s", stdout.String())
	}

	// The feed is no longer replayed as lines above the block: an event
	// worth remembering — a gate failure — lands in the frame's own
	// two-line tail (epic hn6 rule 6), and the block below it keeps working.
	two := 2
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "t2", &two,
		reconcile.StageGateFailed, "the integrated gate refused: go test failed"))
	watchWaitsFor(t, "the event in the tail", func() bool {
		return strings.Contains(stdout.String(), "gate_failed: the integrated gate refused")
	}, &stdout, &stderr)

	// The run's own last word ends the watch.
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "completed: every tick closed behind the gate"))
	life.Release("ended")
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != 0 {
		t.Fatalf("exit code %d for a run that ended clean; stderr:\n%s", got, stderr.String())
	}
	// The last word is in the scrollback for a person who comes back late.
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the run's own last word never printed:\n%s", stdout.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a clean run raised the hold alert:\n%s", stderr.String())
	}
}

// TestWatchOnATerminalEndsHoldingForAPerson: a hold is the whole point of
// the command — while the run holds, the attention line leads every frame
// and the alert is kept above the block once; when the run ends still
// holding, the exit code is the one a script waits on (3) and the final
// alert names what and the command that moves it on.
func TestWatchOnATerminalEndsHoldingForAPerson(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)
	fakeTerminal(t)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	two := 2
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "t2", &two,
		reconcile.StageRunHeld, "attempt_unaddressed: nobody can say whether the attempt is running"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	// Attention first: the hold leads the frame while the run is live.
	watchWaitsFor(t, "the attention line", func() bool {
		return strings.Contains(stdout.String(), "needs you: attempt_unaddressed")
	}, &stdout, &stderr)
	// The alert is kept above the block, once — not once per frame.
	watchWaitsFor(t, "the kept alert", func() bool {
		return strings.Count(stdout.String(), "move it on: ticfac settle rmod t2 2") == 1
	}, &stdout, &stderr)

	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "failed: t2 did not pass"))
	life.Release("ended holding")
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != ExitHeld {
		t.Fatalf("exit code %d, want %d for a run that ended holding for a person", got, ExitHeld)
	}
	if !strings.Contains(stderr.String(), "ticfac settle rmod t2 2 --release") ||
		!strings.Contains(stderr.String(), "attempt_unaddressed") {
		t.Errorf("the final alert does not name what and the command:\n%s", stderr.String())
	}
}

// TestWatchOnATerminalFollowsACloudRun: the live view is the same for a run
// the Workflow hosts — the model gathers from the factory's own records and
// feed, and the run record's own state is the liveness answer — and the
// watch ends on the run's last word there too.
func TestWatchOnATerminalFollowsACloudRun(t *testing.T) {
	runID := "run_62c289d1e6f4a2b3c4d"
	repo := t.TempDir()
	execTestCmd(t, repo, "git", "init", "--quiet", "-b", "main")
	execTestCmd(t, repo, "git", "config", "user.email", "status@example.com")
	execTestCmd(t, repo, "git", "config", "user.name", "status test")

	at := "2026-09-27T04:08:08Z"
	feedText := strings.Join([]string{
		`{"schema_version":1,"at":"` + at + `","run_id":"` + runID + `","tick_id":"t1","attempt":1,"stage":"dispatched","detail":"t1 try 1 dispatched"}`,
		`{"schema_version":1,"at":"` + at + `","run_id":"` + runID + `","stage":"run_finished","detail":"completed: every tick closed"}`,
		"",
	}, "\n")

	var mu sync.Mutex
	state := "running"
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{
				"run_id": runID, "epic": "cld", "state": "running",
			}}}
		case request.Path == "/api/runs/"+runID:
			mu.Lock()
			current := state
			mu.Unlock()
			return 200, map[string]any{"run": map[string]any{
				"run_id": runID, "epic": "cld", "state": current,
			}}
		case request.Path == "/api/runs/"+runID+"/events":
			return 200, map[string]any{
				"run_id": runID, "state": "running",
				"text": feedText, "bytes": len(feedText), "total_bytes": len(feedText),
			}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	fakeTheTracker(t, &tk.Graph{Waves: []tk.GraphWave{{
		Wave:  1,
		Tasks: []tk.GraphTask{{ID: "t1", Title: "the one tick", Status: "open"}},
	}}})
	fakeTerminal(t)

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	watchWaitsFor(t, "the cloud frame", func() bool {
		out := stdout.String()
		return strings.Contains(out, "cloud · alive") && strings.Contains(out, " t1    the one tick")
	}, &stdout, &stderr)

	// The Workflow's record is the liveness answer: it says completed, the
	// run's own last word is terminal, and the watch ends on them.
	mu.Lock()
	state = "completed"
	mu.Unlock()
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the cloud run ended")
	}
	if got != 0 {
		t.Fatalf("exit code %d for a cloud run that ended clean; stderr:\n%s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the cloud run's own last word never printed:\n%s", stdout.String())
	}
}

// TestWatchOnATerminalEndsFailed: the exit code is the contract a script
// waits on, and a run that ended in its own failure is the failed class
// (1), not the done class (tick bot) — the same classification the pipe and
// --json answer with, so the two paths cannot disagree about one ending.
// The hold keeps precedence (the test above): a run that failed HOLDING an
// attempt still exits 3, because a person can move that before anything
// else matters.
func TestWatchOnATerminalEndsFailed(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)
	fakeTerminal(t)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	// The durable words a failed run leaves: the checkpoint the reconciler
	// wrote on its last state change, and the terminal feed line that says
	// why it stopped.
	setCheckpointState(t, repo, runID, "failed")
	two := 2
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "t2", &two,
		reconcile.StageGateFailed, "the integrated gate refused: go test failed"))
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "failed: t2 did not pass"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	life.Release("ended failed")
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != exitGeneric {
		t.Fatalf("exit code %d, want %d (the failed class) for a run that ended failed; stderr:\n%s", got, exitGeneric, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended FAILED") {
		t.Errorf("the failed end is not said to the person reading the block:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a run that failed holding nothing raised the hold alert:\n%s", stderr.String())
	}
	// The last word is still in the scrollback below the final frame.
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the run's own last word never printed:\n%s", stdout.String())
	}
}

// The live view's cancelled end (tick rix): the same block a failed run
// ends with must answer a deliberately stopped run with the cancelled class
// (7), classified from the run's own durable words — the checkpoint's
// cancelled state word and the terminal feed line — so the live path and
// the pipe answer one deliberate stop with the same word and code.
func TestWatchOnATerminalEndsCancelled(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)
	fakeTerminal(t)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	// The durable words a cancelled run leaves: the cancelled checkpoint —
	// the word nothing writes today, which is why the class was latent —
	// and the terminal feed line in the resume path's own shape.
	setCheckpointState(t, repo, runID, "cancelled")
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "the run is already cancelled: operator cancelled"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	life.Release("ended cancelled")
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != exitCancelled {
		t.Fatalf("exit code %d, want %d (the cancelled class) for a run that ended cancelled; stderr:\n%s", got, exitCancelled, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended CANCELLED") {
		t.Errorf("the cancelled end is not said to the person reading the block:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "FAILED") || strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a cancelled run was spoken of as a failure or a hold:\n%s", stderr.String())
	}
	// The last word is still in the scrollback below the final frame.
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the run's own last word never printed:\n%s", stdout.String())
	}
}

// The live view's deliberate stop (tick vqc): a run a person stopped
// with Ctrl-C has NO terminal checkpoint — the process died mid-flight —
// so the block classifies the end from the feed's last word alone: the
// signal death line led by the cancelled state word, the same class the
// pipe and --json answer with, never the failed class a real death reads
// as.
func TestWatchOnATerminalEndsCancelledWhenAPersonStoppedTheRun(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)
	fakeTerminal(t)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	// The checkpoint stays the fixture's "running": a person's stop leaves
	// no terminal checkpoint, so this end is classified from the feed line
	// alone — the shape the SIGINT handler writes.
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunDied, "cancelled: stopped by a signal (interrupt) before the run finished"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	life.Release("stopped by a person")
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != exitCancelled {
		t.Fatalf("exit code %d, want %d (the cancelled class) for a run a person stopped; stderr:\n%s", got, exitCancelled, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ended CANCELLED") {
		t.Errorf("the deliberate stop is not said to the person reading the block:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "FAILED") || strings.Contains(stderr.String(), "HOLDING") {
		t.Errorf("a person's stop was spoken of as a failure or a hold:\n%s", stderr.String())
	}
	// The last word is still in the scrollback below the final frame.
	if !strings.Contains(stdout.String(), "run_died") {
		t.Errorf("the run's own last word never printed:\n%s", stdout.String())
	}
}

// setCheckpointState rewrites the run's durable checkpoint state: the word
// the reconciler writes on its last state change, read by the model's
// lifecycle the frames render from.
func setCheckpointState(t *testing.T, repo, runID, state string) {
	t.Helper()
	path := filepath.Join(repo, ".ticfac", "runs", runID, "checkpoint.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the checkpoint: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the checkpoint: %v", err)
	}
	doc["state"] = state
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal the checkpoint: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write the checkpoint: %v", err)
	}
}

// TestWatchOnATerminalTakesKeys: on a terminal the dashboard answers the
// keyboard (epic hn6 wave 4). j selects the plan's first row, enter opens
// that tick's drill view, esc comes back down, e opens the whole feed —
// lines the dashboard's two-line tail never showed — and q on the dashboard
// ends the watch exactly as SIGINT does today: the same words, the same
// exit code. Raw mode is restored on the way out, whatever ended the watch.
func TestWatchOnATerminalTakesKeys(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)
	runID := "epic-rmod"

	fakeTheTracker(t, threeWaveGraph())
	t.Setenv("HOME", home)
	fakeTerminal(t)
	keys, restored := fakeKeys(t)

	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	watchWaitsFor(t, "the dashboard", func() bool {
		return strings.Contains(stdout.String(), "◐ waves 2/3")
	}, &stdout, &stderr)

	// j moves the cursor onto the plan's first row.
	keys <- "j"
	watchWaitsFor(t, "the cursor on t1", func() bool {
		return strings.Contains(stdout.String(), "▸t1")
	}, &stdout, &stderr)

	// enter opens the tick's drill view.
	keys <- "enter"
	watchWaitsFor(t, "the tick view", func() bool {
		return strings.Contains(stdout.String(), "[esc] back")
	}, &stdout, &stderr)

	// esc comes back, and the frame under the cursor is the dashboard's
	// again — drawn after the drill view's last frame.
	keys <- "esc"
	watchWaitsFor(t, "back on the dashboard", func() bool {
		out := stdout.String()
		return strings.LastIndex(out, "◐ waves 2/3") > strings.LastIndex(out, "[esc] back")
	}, &stdout, &stderr)

	// e opens the whole feed: the standing feed's own lines, not the tail's
	// two — the retry line is the feed's oldest and the tail never shows it.
	keys <- "e"
	watchWaitsFor(t, "the feed view", func() bool {
		return strings.Contains(stdout.String(), "origin refused the fetch; retry 1")
	}, &stdout, &stderr)

	// esc comes back once more, and q on the dashboard ends the watch the
	// way SIGINT does: the run is still going here, so the interrupted
	// answer — exit 1, the words that say so — is the contract.
	keys <- "esc"
	watchWaitsFor(t, "back on the dashboard again", func() bool {
		out := stdout.String()
		return strings.LastIndex(out, "◐ waves 2/3") > strings.LastIndex(out, "origin refused the fetch")
	}, &stdout, &stderr)
	keys <- "q"
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatalf("q on the dashboard never ended the watch;\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if got != 1 {
		t.Errorf("exit code %d, want 1 for a watch ended by q on a live run; stderr:\n%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "the watch was interrupted") {
		t.Errorf("q did not end the watch the way SIGINT words it:\n%s", stderr.String())
	}
	if !*restored {
		t.Error("the terminal's raw mode was never restored on the way out")
	}
}

// TestWatchKeyedFramesEndCRLF (tick r3x): with the keyboard attached the
// terminal is raw — the newline's carriage return is gone for the whole
// device, stdout included — so every line the live block writes ends \r\n,
// and the run's own last word, written AFTER the watch restored the
// terminal, ends plain \n again. A last word that rode raw mode out of the
// watch would staircase exactly like the frames did.
//
// short: in-process fixture over a temp checkout, like the block tests
// above; the real-terminal proof of the same fact lives in the pty test.
func TestWatchKeyedFramesEndCRLF(t *testing.T) {
	fakeTerminal(t)
	_, restored := fakeKeys(t)

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

	watchWaitsFor(t, "the first frame", func() bool {
		return strings.Contains(stdout.String(), "◐ waves 2/3")
	}, &stdout, &stderr)

	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "completed: every tick closed behind the gate"))
	life.Release("ended")
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	if got != 0 {
		t.Fatalf("exit code %d for a run that ended clean; stderr:\n%s", got, stderr.String())
	}
	if !*restored {
		t.Error("the terminal's raw mode was never restored before the last words")
	}

	out := stdout.String()
	// The last word ends the buffer, and it ends with a plain \n — written
	// after the restore, the way cooked output is written. Everything the
	// block wrote before it — frames and kept alerts — ends \r\n: the
	// carriage, written by hand while the terminal was raw.
	if !strings.HasSuffix(out, "run_finished: completed: every tick closed behind the gate\n") {
		t.Errorf("the run's last word is not the buffer's plain-newline end:\n%q", out)
	}
	lastBare := -1
	for i := 0; i < len(out); i++ {
		if out[i] == '\n' && (i == 0 || out[i-1] != '\r') {
			lastBare = i
		}
	}
	if lastBare != len(out)-1 {
		t.Errorf("the block wrote a bare newline while the keyboard held raw mode — the staircase:\n%q",
			out[max(0, lastBare-120):min(len(out), lastBare+1)])
	}
}

// TestWatchKeepsInsertedLinesInsideThePane (tick r3x): the lines kept ABOVE
// the block — the attention alert, a failed reading — are cut to the pane's
// width, because a kept line that wraps pushes the block down rows the
// redraw's cursor arithmetic does not know about, and the next frame would
// draw over the block's own rows.
//
// short: in-process fixture over a temp checkout, like the block tests
// above.
func TestWatchKeepsInsertedLinesInsideThePane(t *testing.T) {
	fakeTerminal(t)

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

	// The hold the other tests use: its alert line is ~180 cells, far past
	// the pane's 100 columns.
	two := 2
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "t2", &two,
		reconcile.StageRunHeld, "attempt_unaddressed: nobody can say whether the attempt is running"))

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "120ms", runID}, &stdout, &stderr)
	}()

	watchWaitsFor(t, "the kept alert", func() bool {
		return strings.Contains(stdout.String(), "! ticfac watch: run epic-rmod is holding for a person")
	}, &stdout, &stderr)

	writeFeedEvent(t, repo, runID, runfeed.NewEvent(time.Now(), runID, "", nil,
		reconcile.StageRunFinished, "completed: every tick closed behind the gate"))
	life.Release("ended")
	var got int
	select {
	case got = <-code:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch never returned after the run ended")
	}
	// The hold the scenario stands on keeps the precedence the exit table
	// gives it: the run ended holding, and the code is the held class.
	if got != ExitHeld {
		t.Fatalf("exit code %d, want %d (the held class) for a run that ended holding; stderr:\n%s", got, ExitHeld, stderr.String())
	}

	out := stdout.String()
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if w := ansi.StringWidth(ansi.Strip(line)); w > 100 {
			t.Errorf("line %d is %d cells wide in a 100-column pane: %q", i, w, ansi.Strip(line))
		}
	}
}
