package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// workersOf is nil's honest count: a null workers list and an empty one are
// different claims, and a test that counted them the same would pass on the
// one it means to refuse.
func workersOf(model statusmodel.Model) int {
	if model.Workers == nil {
		return 0
	}
	return len(*model.Workers)
}

// `ticfac status` reports, for each in-flight attempt, how long since its
// branch last moved and its worktree last changed (tick 7zs) — the Phase 3
// run was alive and true for 55 minutes while its worker produced nothing
// for 40 of them, and a person caught it by reading the pane.

// statusFixture is a repo with one standing attempt: branch tip committed
// three hours ago, worktree's newest file written two hours ago. The run
// named by the branch is whatever claims it — these tests claim the test
// process, which is alive.
func statusFixture(t *testing.T, now time.Time) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_DATE="+now.Add(-3*time.Hour).UTC().Format(time.RFC3339),
			"GIT_COMMITTER_DATE="+now.Add(-3*time.Hour).UTC().Format(time.RFC3339))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "--quiet", "-b", "main")
	git("config", "user.email", "status@example.com")
	git("config", "user.name", "status test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "--quiet", "-m", "base")
	git("branch", "ticfac/run-r-status/tick-a1/attempt-1")
	worktree := filepath.Join(t.TempDir(), "wt-a1")
	cmd := exec.Command("git", "worktree", "add", "--quiet", worktree, "ticfac/run-r-status/tick-a1/attempt-1")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(worktree, "work-a1.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The checked-out base file carries the commit's age, as it does in a
	// real worktree — it was written by whoever made the base commit.
	if err := os.Chtimes(filepath.Join(worktree, "base.txt"), now.Add(-3*time.Hour), now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(worktree, "work-a1.txt"), now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Every run that ever ran here left its log directory — the claim made
	// it — which is how `status` knows r-status names a run rather than an
	// id nobody found (runid.go). A test that never claims still stands in
	// a checkout the run ran in.
	if err := os.MkdirAll(runlife.Dir(repo, "r-status"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

// Both gaps are on the text surface, both on the JSON surface, and they are
// the same numbers: a watcher reading either surface sees what was measured,
// not two stories about it.
func TestStatusReportsTheGapOfEveryInFlightAttempt(t *testing.T) {
	t.Parallel()
	repo := statusFixture(t, time.Now())
	life, err := runlife.Claim(repo, "r-status")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	var out syncBuffer
	if code := Run([]string{"status", "--repo", repo, "r-status"}, &out, &syncBuffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "a1 (run dispatch #1)") {
		t.Errorf("the in-flight attempt is not named:\n%s", text)
	}
	if !strings.Contains(text, "last moved 3h0m") {
		t.Errorf("the branch gap is not the measured one:\n%s", text)
	}
	if !strings.Contains(text, "last changed 2h0m") {
		t.Errorf("the worktree gap is not the measured one:\n%s", text)
	}

	out.Reset()
	if code := Run([]string{"status", "--repo", repo, "--json", "r-status"}, &out, &syncBuffer{}); code != 0 {
		t.Fatalf("a live run exited %d in --json: %s", code, out.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil {
		t.Fatalf("the JSON model does not decode: %v\n%s", err, out.String())
	}
	if model.SchemaVersion != statusmodel.SchemaVersion || model.RunID != "r-status" || model.Host != statusmodel.HostLocal {
		t.Errorf("the model does not name itself: version %d, run %q, host %q",
			model.SchemaVersion, model.RunID, model.Host)
	}
	if model.Workers == nil || len(*model.Workers) != 1 {
		t.Fatalf("the JSON reports %d in-flight workers, want 1:\n%s", workersOf(model), out.String())
	}
	w := (*model.Workers)[0]
	if w.TickID != "a1" || w.Attempt != 1 {
		t.Errorf("the worker reads as %s#%d, want a1#1", w.TickID, w.Attempt)
	}
	if w.BranchIdleSeconds == nil || *w.BranchIdleSeconds < 2*3600 || *w.BranchIdleSeconds > 4*3600 {
		t.Errorf("the JSON branch gap is %+v, want ~3h", w.BranchIdleSeconds)
	}
	if w.WorktreeIdleSeconds == nil || *w.WorktreeIdleSeconds < 1*3600 || *w.WorktreeIdleSeconds > 3*3600 {
		t.Errorf("the JSON worktree gap is %+v, want ~2h", w.WorktreeIdleSeconds)
	}
}

// The gap never changes the verdict: the exit code stays liveness's answer
// alone, so a watcher looping over it behaves exactly as before — a dead run
// with a stalled attempt still exits 1, and its gaps are still reported,
// which is precisely when a person needs them.
func TestTheGapChangesNoVerdict(t *testing.T) {
	t.Parallel()
	repo := statusFixture(t, time.Now())

	var out syncBuffer
	code := Run([]string{"status", "--repo", repo, "r-status"}, &out, &syncBuffer{})
	if code != 1 {
		t.Fatalf("a dead run exited %d: the gap must not change the verdict, only report beside it", code)
	}
	if !strings.Contains(out.String(), "not_running") {
		t.Errorf("the liveness answer is not the headline:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "a1 (run dispatch #1)") {
		t.Errorf("a dead run's standing attempt is not reported — the leftover worktree is exactly the case the gaps exist for:\n%s", out.String())
	}
}

// The wall clock FIRING is on the status surface for an in-flight attempt
// (tick q1e): the run writes one typed feed line when a bound fires
// (wall_feed_test.go, in reconcile), and `ticfac status` — the surface a
// watcher reads while the run polls on — reports it beside the attempt's
// gaps, from the line's own stage and identity, never from its prose, and
// never from being the last thing the feed said.
func TestStatusReportsTheWallClockFiringOfAnInFlightAttempt(t *testing.T) {
	t.Parallel()
	repo := statusFixture(t, time.Now())
	runID := "r-status"
	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	// The run's own firing line, four minutes old, through the same writer
	// the reconciler feeds with — then a LATER run-level line, so the firing
	// is not the last event a status read could lean on.
	firedAt := time.Now().UTC().Add(-4 * time.Minute).Truncate(time.Second)
	detail := "the wall clock of 3600s fired 4m0s ago and attempt 1 of a1 has not settled: " +
		"the executor is stopping it — the interrupt was delivered but the agent has not exited"
	one := 1
	feed := runfeed.Open(repo, runID)
	if err := feed.Append(runfeed.NewEvent(firedAt, runID, "a1", &one,
		reconcile.StageWallClock, detail)); err != nil {
		t.Fatalf("append the firing line: %v", err)
	}
	if err := feed.Append(runfeed.NewEvent(firedAt.Add(3*time.Minute), runID, "", nil,
		reconcile.StageBudgetSet, "the effective budget for this run is $1.00")); err != nil {
		t.Fatalf("append the later line: %v", err)
	}

	var out syncBuffer
	if code := Run([]string{"status", "--repo", repo, runID}, &out, &syncBuffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "wall clock fired 4m") {
		t.Errorf("the attempt line does not report the firing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), detail) {
		t.Errorf("the attempt line does not carry the run's own words about what the executor saw:\n%s", out.String())
	}

	out.Reset()
	if code := Run([]string{"status", "--repo", repo, "--json", runID}, &out, &syncBuffer{}); code != 0 {
		t.Fatalf("a live run exited %d in --json: %s", code, out.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil {
		t.Fatalf("the JSON model does not decode: %v\n%s", err, out.String())
	}
	if model.Workers == nil || len(*model.Workers) != 1 {
		t.Fatalf("the JSON reports %d in-flight workers, want 1", workersOf(model))
	}
	w := (*model.Workers)[0]
	if w.WallClock == nil {
		t.Fatalf("the JSON reports no firing for an attempt whose wall clock fired:\n%s", out.String())
	}
	if w.TickID != "a1" || w.Attempt != 1 {
		t.Errorf("the JSON's worker reads as %s#%d, want a1#1", w.TickID, w.Attempt)
	}
	if fired, err := time.Parse(time.RFC3339, w.WallClock.FiredAt); err != nil || !fired.Equal(firedAt) {
		t.Errorf("the JSON's firing reads %s, want %s", w.WallClock.FiredAt, firedAt)
	}
	if w.WallClock.Detail != detail {
		t.Errorf("the JSON's firing detail does not carry the run's own line:\n got %q", w.WallClock.Detail)
	}
	if model.Health.WallClocksFired != 1 {
		t.Errorf("the JSON's health counts %d firings, want 1", model.Health.WallClocksFired)
	}
}

// The stall warning's prose NAMES the wall clock — "the wall clock of 3600s
// is still the bound" — and none of that is a firing. The status surface
// reports the typed fact alone: a watcher who reads a firing where the run
// said only "still the bound" is sent at a stop that has not happened, and
// the acceptance for this tick says neither surface is derived by matching
// observation prose.
func TestStatusReportsNoFiringFromProseAlone(t *testing.T) {
	t.Parallel()
	repo := statusFixture(t, time.Now())
	runID := "r-status"
	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })
	one := 1
	if err := runfeed.Open(repo, runID).Append(runfeed.NewEvent(
		time.Now().UTC(), runID, "a1", &one, reconcile.StageStallWarned,
		"attempt 1 of a1 is alive but has produced nothing durable for 15m0s: ... — "+
			"the wall clock of 3600s is still the bound")); err != nil {
		t.Fatalf("append the stall line: %v", err)
	}

	var out syncBuffer
	if code := Run([]string{"status", "--repo", repo, runID}, &out, &syncBuffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	if strings.Contains(out.String(), "wall clock fired") {
		t.Errorf("prose that names the wall clock reported a firing:\n%s", out.String())
	}
}

// Status names a tick "id (label)" (ticfac q90): a person reading a run should
// not have to look up three-letter ids. Not parallel — it swaps the label
// lookup, and parallel tests only resume once the serial ones are done.
func TestStatusNamesAnAttemptByItsLabel(t *testing.T) {
	repo := statusFixture(t, time.Now())
	life, err := runlife.Claim(repo, "r-status")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	real := tickLabels
	t.Cleanup(func() { tickLabels = real })
	tickLabels = func(context.Context, string) map[string]string {
		return map[string]string{"a1": "wire the door"}
	}

	var out syncBuffer
	if code := Run([]string{"status", "--repo", repo, "r-status"}, &out, &syncBuffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "a1 (wire the door) (run dispatch #1)") {
		t.Errorf("the attempt is not named by its label:\n%s", out.String())
	}
}

// A tracker that cannot be read costs the labels, never the line.
func TestTickLabelsOnAnUnreadableTrackerIsEmpty(t *testing.T) {
	t.Parallel()
	if got := tickLabels(context.Background(), t.TempDir()); len(got) != 0 {
		t.Errorf("labels from a directory with no tracker: %v", got)
	}
	if got := tickRef(nil, "a1"); got != "a1" {
		t.Errorf("tickRef with no labels = %q", got)
	}
}

// TestStatusByEpicIDAnswersTheCloudRunTheEpicHasInTheFactory: `ticfac status
// <epic-id>` — the id the operator has, after `ticfac run <epic> --cloud`
// started the run there — answers the run the factory holds for this
// checkout's project when nothing runs here (tick nyi). Before the fix the
// epic id never looked in the factory at all: the command answered the
// local pidfile probe's "not running here" and stopped.
func TestStatusByEpicIDAnswersTheCloudRunTheEpicHasInTheFactory(t *testing.T) {
	stubCloudTk(t)
	repo, _, _ := setupCloudRepo(t, true)
	cloudRun := cloudRunIDOf("f888")

	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	feed := feedLine(t, runfeed.NewEvent(at, cloudRun, "t1", nil,
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
	code := Run([]string{"status", "--repo", repo, "epic-epic1"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d for a finished cloud run, want 1 (not alive):\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run "+cloudRun+": not alive") {
		t.Errorf("stdout does not answer for the factory's run:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), cloudRun) || !strings.Contains(stderr.String(), "epic-epic1") {
		t.Errorf("stderr does not name the resolution from epic id to the factory's run:\n%s", stderr.String())
	}
}

// The resumed-run case, on the surface that missed it (tick 4nq): the feed
// is append-only per RUN ID, so a resumed local run appends to a file a
// previous, failed incarnation already ended with a terminal line. A frame
// that scans the whole standing feed for ANY terminal line marks the run
// `ended` and `status --follow` stops following at the first frame, exit 0,
// while the run is alive and dispatching — the same defect `ticfac watch`
// carried until tick usx gave it cursor protection. A LIVE run claims the
// follow: the previous incarnation's ending is history, and only a terminal
// line the CURRENT incarnation writes ends it.
func TestStatusFollowOnAResumedRunDoesNotEndOnThePreviousIncarnationsTerminalLine(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	// The previous incarnation: it dispatched tick a1 and failed on it —
	// the exact line the old frame scanned as if it were happening now.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now().Add(-time.Hour), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as run-x/tick-a1/attempt-1"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now().Add(-30*time.Minute), "r-1", "", nil, reconcile.StageRunFinished, "failed: a1 did not pass"))

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
		code <- Run([]string{"status", "--repo", repo, "--follow", "--interval", "10ms", "r-1"}, &stdout, &stderr)
	}()

	// The follow joined the CURRENT incarnation: the resumed run writes a
	// marker and the table renders it — a follow that ended on the previous
	// incarnation's run_finished at the first frame can never render a line
	// the current incarnation writes. Waits on that condition, never on a
	// guessed interval.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Now(), "r-1", "a1", &attempt, "dispatched", "resumed incarnation dispatch marker"))
		if strings.Contains(stdout.String(), "resumed incarnation dispatch marker") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "resumed incarnation dispatch marker") {
		select {
		case got := <-code:
			t.Fatalf("the follow ended %d at the first frame on the previous incarnation's terminal line, while the run is alive and dispatching; stderr %q", got, stderr.String())
		default:
			t.Fatalf("the follow never rendered the current incarnation's line: %q", stdout.String())
		}
	}
	select {
	case got := <-code:
		t.Fatalf("the follow returned %d while the run is still alive and dispatching; stderr %q", got, stderr.String())
	default:
	}

	// The resumed run ends its own way, and the follow ends on THAT line.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, reconcile.StageRunFinished, "completed: every tick closed behind the gate"))
	select {
	case got := <-code:
		if got != 0 {
			t.Fatalf("exit code %d, want 0 for a resumed run that ended clean while followed; stderr %q", got, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the follow never ended on the current incarnation's own terminal line")
	}
}

// The other side of the cursor (tick 4nq): a run no live process claims is
// answered by its standing feed — that IS the run's own last word, and the
// follow ends on it at the first frame rather than following an open-ended
// silence. A run about to be resumed has not claimed yet; its previous
// ending was the truth until the resume.
func TestStatusFollowOnAnUnclaimedRunEndsOnItsOwnTerminalLine(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now().Add(-time.Hour), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as run-x/tick-a1/attempt-1"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now().Add(-30*time.Minute), "r-1", "", nil, reconcile.StageRunFinished, "failed: a1 did not pass"))

	var stdout, stderr syncBuffer
	var code int
	done := make(chan struct{})
	go func() {
		defer close(done)
		code = Run([]string{"status", "--repo", repo, "--follow", "--interval", "10ms", "r-1"}, &stdout, &stderr)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the follow never ended on an unclaimed run's own terminal line")
	}
	if code != 0 {
		t.Fatalf("exit code %d, want 0 — ended is the surface's answer, never a verdict about the work; stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "failed: a1 did not pass") {
		t.Errorf("the frame does not carry the run's own last word: %q", stdout.String())
	}
}
