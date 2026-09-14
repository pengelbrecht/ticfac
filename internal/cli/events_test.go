package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The subscription surface of the run event feed (tick u9l): `ticfac events`
// is how a NON-PARTICIPANT learns a run finished, and these tests are the
// acceptance criterion in its smallest form — a feed read without polling a
// durable record or guessing an interval, an identity on every printed line,
// and a follow that delivers each line as it lands.

func writeFeedEvent(t *testing.T, repo, runID string, event runfeed.Event) {
	t.Helper()
	if err := runfeed.Open(repo, runID).Append(event); err != nil {
		t.Fatal(err)
	}
}

func TestEventsPrintsTheFeedThatStands(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as job-7f2a"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC), "r-1", "", nil, "run_finished", "completed"))

	var stdout, stderr bytes.Buffer
	code := Run([]string{"events", "--repo", repo, "r-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("printed %d lines, want the 2 the feed carries: %q", len(lines), stdout.String())
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["run_id"] != "r-1" || first["tick_id"] != "a1" || first["attempt"] != float64(1) {
		t.Errorf("the first printed line lost its identity: %v", first)
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &last); err != nil {
		t.Fatal(err)
	}
	if last["stage"] != "run_finished" {
		t.Errorf("the terminal line prints stage %v", last["stage"])
	}
}

func TestEventsNamesARunThatHasNotWritten(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"events", "--repo", t.TempDir(), "r-none"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("a run with no feed printed nothing and exited 0")
	}
	if !strings.Contains(stderr.String(), "no feed for run r-none") {
		t.Errorf("stderr %q does not say what a missing feed means", stderr.String())
	}
}

func TestEventsNeedsExactlyOneRunID(t *testing.T) {
	for _, args := range [][]string{{"events"}, {"events", "a", "b"}, {"events", ""}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
}

func TestEventsFollowDeliversEachLineAsItLands(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as job-7f2a"))

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"events", "--repo", repo, "--follow", "r-1"}, &stdout, &stderr)
	}()

	// The follow prints the line that stands, then the line that lands while
	// it is open. No interval is named anywhere in this contract: the
	// subscriber subscribes, and the lines arrive.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(stdout.String(), "\n") >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "completed"))
	for time.Now().Before(deadline) {
		if strings.Count(stdout.String(), "\n") >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lines := strings.Count(stdout.String(), "\n"); lines < 2 {
		t.Fatalf("the follow printed %d lines; the second never landed: %q", lines, stdout.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the follow never delivered the terminal line: %q", stdout.String())
	}
	// The subscription ends on interrupt in production; this test leaves it
	// open deliberately — --follow not exiting is what --follow means.
	select {
	case got := <-code:
		t.Fatalf("--follow exited %d on its own", got)
	default:
	}
}

func TestEventsFollowRefusesAMalformedFeed(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(runfeed.Path(repo, "r-1")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runfeed.Path(repo, "r-1"), []byte("not a line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"events", "--repo", repo, "--follow", "r-1"}, &stdout, &stderr); code == 0 {
		t.Fatal("a malformed feed was followed without complaint")
	}
}
