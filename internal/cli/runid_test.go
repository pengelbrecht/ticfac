package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// The operator's first try of the CLI: `ticfac run 6in` starts run epic-6in,
// and `ticfac watch 6in`, `ticfac status 6in` and `ticfac events 6in` then
// answered "no feed for run 6in", "run 6in: not_running" — confident
// negatives about a spelling nobody resolved, while epic-6in was ALIVE.
// Every command that takes a run id takes the epic id too, through one
// resolver (runid.go), and an id no spelling names is said to be exactly
// that, with the command that lists the runs there are.

// liveEpicRun claims run epic-<epic> in a fresh checkout as this (alive)
// test process and writes one dispatched line to its feed: the shape of a
// run `ticfac run <epic>` started and that is working.
func liveEpicRun(t *testing.T, epic string) string {
	t.Helper()
	repo := t.TempDir()
	runID := "epic-" + epic
	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim run %s: %v", runID, err)
	}
	t.Cleanup(func() { life.Release("test") })
	attempt := 1
	writeFeedEvent(t, repo, runID, runfeed.NewEvent(
		time.Now(), runID, "a1", &attempt, "dispatched", "attempt 1 started"))
	return repo
}

func TestStatusByBareEpicIDAnswersTheLiveEpicRun(t *testing.T) {
	repo := liveEpicRun(t, "e6in")

	var stdout, stderr syncBuffer
	code := Run([]string{"status", "--repo", repo, "e6in"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status e6in exited %d while run epic-e6in is alive:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "run epic-e6in: alive") {
		t.Errorf("status e6in does not answer for the live run epic-e6in:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "not_running") {
		t.Errorf("status e6in claimed not_running for a live run:\n%s\n%s", stdout.String(), stderr.String())
	}
}

func TestEventsByBareEpicIDPrintsTheLiveEpicRunsFeed(t *testing.T) {
	repo := liveEpicRun(t, "e6in")

	var stdout, stderr syncBuffer
	code := Run([]string{"events", "--repo", repo, "e6in"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("events e6in exited %d while run epic-e6in has a feed: %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"run_id":"epic-e6in"`) || !strings.Contains(stdout.String(), "dispatched") {
		t.Errorf("events e6in does not print epic-e6in's feed:\n%s", stdout.String())
	}
}

func TestWatchByBareEpicIDFollowsTheLiveEpicRun(t *testing.T) {
	repo := liveEpicRun(t, "e6in")

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "e6in"}, &stdout, &stderr)
	}()
	// The watch joins the live run; the run's own terminal line ends it.
	// This waits on the condition, never on a guessed interval: the watch
	// is subscribed once it has said what it joined, or it has returned.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(code) == 0 && !strings.Contains(stdout.String(), "dispatched") {
		time.Sleep(10 * time.Millisecond)
	}
	if len(code) == 1 {
		t.Fatalf("watch e6in returned %d at once while run epic-e6in is alive: %q", <-code, stderr.String())
	}
	writeFeedEvent(t, repo, "epic-e6in", runfeed.NewEvent(
		time.Now(), "epic-e6in", "", nil, reconcile.StageRunFinished, "completed: every tick closed"))

	select {
	case got := <-code:
		if got != 0 {
			t.Fatalf("watch e6in exited %d on a run that completed: %q", got, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch e6in never ended on epic-e6in's terminal line")
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the watch never showed epic-e6in's terminal line:\n%s", stdout.String())
	}
}

// A run literally named what was typed is that run: the exact spelling wins
// over the epic's run when both exist.
func TestARunNamedExactlyAsTypedWinsOverTheEpicSpelling(t *testing.T) {
	repo := t.TempDir()
	writeFeedEvent(t, repo, "x7q", runfeed.NewEvent(time.Now(), "x7q", "", nil, "budget_set", "the exact run"))
	writeFeedEvent(t, repo, "epic-x7q", runfeed.NewEvent(time.Now(), "epic-x7q", "", nil, "budget_set", "the epic's run"))

	if got := resolveRunArg(repo, "x7q"); got.RunID != "x7q" || !got.Known {
		t.Errorf("x7q resolved to %+v, want the run named exactly x7q", got)
	}
	if got := resolveRunArg(repo, "epic-x7q"); got.RunID != "epic-x7q" || !got.Known {
		t.Errorf("epic-x7q resolved to %+v, want run epic-x7q", got)
	}
	if err := os.RemoveAll(runlife.Dir(repo, "x7q")); err != nil {
		t.Fatal(err)
	}
	if got := resolveRunArg(repo, "x7q"); got.RunID != "epic-x7q" || !got.Known {
		t.Errorf("with no run named x7q, x7q resolved to %+v, want the epic's run epic-x7q", got)
	}
}

// An id no spelling names is answered as that — both spellings named, the
// overview offered — on every command, and never as "not running".
func TestAnIDNoSpellingNamesSaysSoAndPointsAtTheOverview(t *testing.T) {
	repo := t.TempDir()
	for _, command := range []string{"status", "events", "watch"} {
		var stdout, stderr syncBuffer
		code := Run([]string{command, "--repo", repo, "nq0"}, &stdout, &stderr)
		if code != 1 {
			t.Errorf("%s nq0 exited %d for an id nothing names, want 1:\n%s\n%s", command, code, stdout.String(), stderr.String())
		}
		said := stderr.String()
		for _, want := range []string{"no run answers to nq0", "nq0 and epic-nq0", "`ticfac`"} {
			if !strings.Contains(said, want) {
				t.Errorf("%s nq0: stderr does not say %q:\n%s", command, want, said)
			}
		}
		if strings.Contains(stdout.String()+said, "not_running") || strings.Contains(said, "no feed for run") {
			t.Errorf("%s nq0 answered a verdict about a run nobody found:\n%s\n%s", command, stdout.String(), said)
		}
	}
}

func TestEpicIDOfArgStripsTheRunIDsPrefixOnce(t *testing.T) {
	for arg, want := range map[string]string{"6in": "6in", "epic-6in": "6in", "epic-": ""} {
		if got := epicIDOfArg(arg); got != want {
			t.Errorf("epicIDOfArg(%q) = %q, want %q", arg, got, want)
		}
	}
	if got := runSpellings("epic-6in"); len(got) != 1 || got[0] != "epic-6in" {
		t.Errorf("runSpellings(epic-6in) = %v, want only epic-6in (never epic-epic-6in)", got)
	}
}
