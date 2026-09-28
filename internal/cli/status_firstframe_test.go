package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// THE FIRST FOLLOW FRAME WAITS ON NOTHING BUT THE LIVENESS QUESTION.
//
// TestStatusFollowOnAResumedRunDoesNotEndOnThePreviousIncarnationsTerminalLine
// failed under a loaded `make gate` with an EMPTY stdout after 5s: the follow
// had not drawn even its first frame. Before that frame it ran six
// subprocesses one after another — `tk version` and `tk list` for the tick
// labels, then, twice (once for the resume cursor, once for the frame), `git
// worktree list` for the attempt census and `ps` for the liveness answer. On an
// idle host that is ~100ms; on a host where every process spawn costs most of
// a second it is several seconds of a blank screen, for an operator as much as
// for the test. The labels are decoration and the census is never shown by
// the table, so neither may stand between `status --follow` and its first
// frame; and the liveness answer is asked once for the cursor and the first
// frame together.
//
// Reproduced deterministically rather than by load: `tk` and `git` on PATH
// are replaced by shims that take three seconds each before doing the real
// thing, which is the loaded host's spawn cost made exact.
//
// short: one follow over a temp feed, bounded by a few seconds of shimmed tk
func TestTheFirstFollowFrameWaitsOnNoTrackerAndNoCensus(t *testing.T) {
	shims := t.TempDir()
	for _, tool := range []string{"tk", "git"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH, so there is nothing to slow down", tool)
		}
		script := "#!/bin/sh\nsleep 3\nexec " + real + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(shims, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := t.TempDir()
	attempt := 1
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now().Add(-time.Hour), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as run-x/tick-a1/attempt-1"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now().Add(-30*time.Minute), "r-1", "", nil, reconcile.StageRunFinished, "failed: a1 did not pass"))
	life, err := runlife.Claim(repo, "r-1")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	started := time.Now()
	go func() {
		code <- Run([]string{"status", "--repo", repo, "--follow", "--interval", "10ms", "r-1"}, &stdout, &stderr)
	}()
	var firstFrame time.Duration
	for time.Since(started) < 20*time.Second {
		if strings.Contains(stdout.String(), "run r-1:") {
			firstFrame = time.Since(started)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// End the follow: the run finishes, and the follow ends on it.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, reconcile.StageRunFinished, "completed"))
	life.Release("test")
	select {
	case <-code:
	case <-time.After(20 * time.Second):
		t.Error("the follow never ended")
	}

	if firstFrame == 0 {
		t.Fatalf("no first frame in 20s: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	if firstFrame > 2*time.Second {
		t.Fatalf("the first frame took %v with tk and git each costing 3s: the follow waited on the tracker "+
			"(labels) or the attempt census before it drew anything", firstFrame.Round(time.Millisecond))
	}
}
