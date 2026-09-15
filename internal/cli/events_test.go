package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
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

	// The subscription must be live before the line that matters lands, or
	// the follower's cursor is taken after it and the test races the command
	// instead of driving it. The sync waits on the observable — a marker
	// line's delivery — and the markers that land before the cursor was
	// taken are simply not shown, which is the from-now property itself.
	syncMarkerFeed(t, repo, &stdout)

	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "completed"))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "run_finished") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Fatalf("the follow never delivered the terminal line: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "dispatched") {
		t.Errorf("the follow replayed the standing feed's dispatched line: %q", stdout.String())
	}
	// The subscription ends on interrupt in production; this test leaves it
	// open deliberately — --follow not exiting is what --follow means.
	select {
	case got := <-code:
		t.Fatalf("--follow exited %d on its own", got)
	default:
	}
}

// The resumed-run case the from-now cursor exists for (ticfac tick 55i): a
// resumed run appends to the same feed a previous, failed incarnation of the
// same run id already ended with run_finished. A follower that replays that
// line stops on it immediately, reporting a failure that already happened and
// is no longer true — so --follow subscribes from now, and only the CURRENT
// run's terminal line reaches the follower. --from-start is the explicit
// replay, for a subscriber that wants the whole history and knows what it is
// asking for.
func TestEventsFollowSeesOnlyTheCurrentRunsTerminalEvent(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	// The earlier incarnation: it dispatched, failed, and said so.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as job-7f2a"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: attempt 1 of a1 is missing-result"))

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"events", "--repo", repo, "--follow", "r-1"}, &stdout, &stderr)
	}()

	// Live before anything that matters lands (see the first test).
	syncMarkerFeed(t, repo, &stdout)

	// The resumed run, same run id, same feed: it carries on and ends its own
	// way.
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "settled", "the rejected attempt was released by an operator"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "completed: every tick closed behind the gate"))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "every tick closed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var terminals []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["stage"] == "run_finished" {
			detail, _ := event["detail"].(string)
			terminals = append(terminals, detail)
		}
	}
	if len(terminals) != 1 {
		t.Fatalf("the follower saw %d terminal lines (%v), want exactly the current run's one: %q",
			len(terminals), terminals, stdout.String())
	}
	if !strings.Contains(terminals[0], "completed") {
		t.Errorf("the follower saw terminal %q — an earlier incarnation's, not the current run's", terminals[0])
	}
	select {
	case got := <-code:
		t.Fatalf("--follow exited %d on its own", got)
	default:
	}
}

// syncMarkerFeed waits for a follower to be live: a marker line is appended
// every so often, and the FIRST delivery of any marker is the observable that
// the follower has taken its cursor and is reading — markers appended before
// the cursor was taken are exactly the lines a from-now subscription does not
// show. It fails the test if the follower never goes live, because every
// assertion after it depends on the subscription, not on luck.
func syncMarkerFeed(t *testing.T, repo string, stdout *bytes.Buffer) {
	t.Helper()
	for i := 0; i < 40; i++ {
		writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
			time.Now(), "r-1", "a1", nil, "resumed", fmt.Sprintf("subscription sync marker %d", i)))
		deadline := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			if strings.Contains(stdout.String(), "subscription sync marker") {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("the follower never showed a sync marker; the subscription is not live: %q", stdout.String())
}

// --from-start is the explicit replay: the standing feed first, terminal
// events of earlier incarnations included, then each line as it lands.
func TestEventsFollowFromStartReplaysTheStandingFeed(t *testing.T) {
	repo := t.TempDir()
	attempt := 1
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "a1", &attempt, "dispatched", "attempt 1 started as job-7f2a"))
	writeFeedEvent(t, repo, "r-1", runfeed.NewEvent(
		time.Now(), "r-1", "", nil, "run_finished", "failed: attempt 1 of a1 is missing-result"))

	var stdout, stderr bytes.Buffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"events", "--repo", repo, "--follow", "--from-start", "r-1"}, &stdout, &stderr)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(stdout.String(), "\n") >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lines := strings.Count(stdout.String(), "\n"); lines < 2 {
		t.Fatalf("--from-start printed %d lines, want the standing feed's 2: %q", lines, stdout.String())
	}
	if !strings.Contains(stdout.String(), "missing-result") {
		t.Errorf("--from-start did not replay the earlier incarnation's terminal line: %q", stdout.String())
	}
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
	// --from-start: the malformed line is in the STANDING feed, and a
	// from-now cursor would never read it — the replay is how a follower
	// meets a feed an earlier writer left unreadable.
	if code := Run([]string{"events", "--repo", repo, "--follow", "--from-start", "r-1"}, &stdout, &stderr); code == 0 {
		t.Fatal("a malformed feed was followed without complaint")
	}
}
