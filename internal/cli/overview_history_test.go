package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// The bare `ticfac` is glanced at to answer "does anything need me". On the
// operator's machine it listed ~88 old cloud runs as "failed" — the
// factory's whole history, runs of epics resubmitted since, runs of epics
// long closed — and epic-9pd as failed although 9pd is closed in the
// tracker. That buries the one line that needs a person under screens of
// lines that need nobody. So the human view is signal first: a run is
// HISTORY, and collapsed into one summary line, when
//
//   - its epic is closed in this checkout's tracker (the epic is done; the
//     run's stop is part of how it got there, not something to clear), or
//   - a later run of the same epic exists (the later run is the epic's
//     state; the earlier one's stop was answered by the resubmission), or
//   - it finished (failed, done or cancelled) more than a week ago.
//
// Live runs are never history, and held and recently failed runs of open
// epics stay at the top. --json stays complete: every run is listed, with
// its history flag and the reason, and `ticfac --all` renders them all.

// fakeEpicStatus swaps the tracker's epic-status seam for the test: the
// named epics are closed, every other one is open.
func fakeEpicStatus(t *testing.T, closed ...string) {
	t.Helper()
	real := overviewEpicClosed
	t.Cleanup(func() { overviewEpicClosed = real })
	set := map[string]bool{}
	for _, id := range closed {
		set[id] = true
	}
	overviewEpicClosed = func(_ context.Context, _ string, epicID string) (bool, bool) {
		return set[epicID], true
	}
}

// historyFactory serves a factory whose run index holds what an operator's
// factory accumulates: the live and the recent beside a long tail of
// finished runs that need nobody.
func historyFactory(t *testing.T, now time.Time) (attention, history []string) {
	t.Helper()
	at := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(time.RFC3339) }
	runs := []any{}
	add := func(id, epic, state string, startedAgo, endedAgo time.Duration) {
		record := map[string]any{"run_id": id, "epic": epic, "state": state, "started_at": at(startedAgo)}
		if endedAgo > 0 {
			record["ended_at"] = at(endedAgo)
		}
		runs = append(runs, record)
	}
	hex := func(n int) string { return fmt.Sprintf("run_%032x", n) }

	// Attention: the latest, recent failure of an open epic; a live run; a
	// run that finished an hour ago.
	add(hex(1), "opn", "failed", 2*time.Hour, time.Hour)
	add(hex(2), "liv", "running", 30*time.Minute, 0)
	add(hex(3), "rec", "completed", 3*time.Hour, time.Hour)
	attention = []string{hex(1), hex(2), hex(3)}

	// History: an earlier failed run of the same open epic, superseded by
	// hex(1); a recent failure of an epic the tracker says is closed; and
	// twenty runs that finished a month ago.
	add(hex(4), "opn", "failed", 6*time.Hour, 5*time.Hour)
	add(hex(5), "cls", "failed", 2*time.Hour, time.Hour)
	history = []string{hex(4), hex(5)}
	for i := 0; i < 20; i++ {
		state := "failed"
		if i%3 == 0 {
			state = "completed"
		}
		id := hex(100 + i)
		add(id, fmt.Sprintf("o%02d", i), state, 31*24*time.Hour, 30*24*time.Hour)
		history = append(history, id)
	}

	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Path == "/api/runs" {
			return 200, map[string]any{"runs": runs}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	return attention, history
}

func TestTheBareOverviewLeadsWithWhatNeedsAPersonAndCollapsesHistory(t *testing.T) {
	now := time.Now()
	ownRegistry(t)
	repo := t.TempDir()
	attention, history := historyFactory(t, now)
	fakeOverviewGraph(t)
	fakeEpicStatus(t, "cls")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, id := range attention {
		if lineOf(out, id) == "" {
			t.Errorf("%s needs to be seen and is not listed:\n%s", id, out)
		}
	}
	for _, id := range history {
		if lineOf(out, id) != "" {
			t.Errorf("%s is history and still has a line of its own:\n%s", id, out)
		}
	}
	summary := fmt.Sprintf("%d older runs", len(history))
	if !strings.Contains(out, summary) || !strings.Contains(out, "ticfac --all") {
		t.Errorf("the collapsed history is not one summary line naming %q and `ticfac --all`:\n%s", summary, out)
	}
	if strings.Count(strings.TrimSpace(out), "\n")+1 > len(attention)+1 {
		t.Errorf("the overview is %d lines, want %d rows and the one summary line:\n%s",
			strings.Count(strings.TrimSpace(out), "\n")+1, len(attention)+1, out)
	}

	// --all lists every run, history included, each history row saying why.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--repo", repo, "--all"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --all exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	all := stdout.String()
	for _, id := range append(append([]string{}, attention...), history...) {
		if lineOf(all, id) == "" {
			t.Errorf("--all does not list %s:\n%s", id, all)
		}
	}
	if line := lineOf(all, history[0]); !strings.Contains(line, "superseded") {
		t.Errorf("the superseded run's --all line does not say so: %q", line)
	}
	if line := lineOf(all, history[1]); !strings.Contains(line, "closed") {
		t.Errorf("the closed epic's run does not say its epic is closed: %q", line)
	}

	// --json is complete: every run, flagged, attention first.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--repo", repo, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --json exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	var doc overviewModel
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("the overview JSON does not decode: %v\n%s", err, stdout.String())
	}
	if len(doc.Runs) != len(attention)+len(history) {
		t.Fatalf("--json lists %d runs, want every one of %d", len(doc.Runs), len(attention)+len(history))
	}
	isHistory := map[string]bool{}
	for _, id := range history {
		isHistory[id] = true
	}
	seenHistory := false
	for _, run := range doc.Runs {
		if run.History != isHistory[run.RunID] {
			t.Errorf("%s reads history=%v, want %v (%s)", run.RunID, run.History, isHistory[run.RunID], run.HistoryReason)
		}
		if run.History && run.HistoryReason == "" {
			t.Errorf("%s is history with no reason", run.RunID)
		}
		if run.History {
			seenHistory = true
		} else if seenHistory {
			t.Errorf("%s needs attention and is listed after the history", run.RunID)
		}
	}
}

// Item 2 of the same fix: a registration whose checkout is gone is not a run
// anybody can act on — the eight test strays of 2026-09-27 each named a
// deleted go-test temp dir and read "held for a person". The overview
// neither lists it nor keeps it: the registry prunes it on the read.
func TestTheBareOverviewDropsARegistrationWhoseCheckoutIsGone(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no factory: the local half is the question
	ownRegistry(t)
	fakeOverviewGraph(t)
	fakeEpicStatus(t)
	repo := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-checkout")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runregistry.Register("epic-gone", gone); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	if line := lineOf(stdout.String(), "epic-gone"); line != "" {
		t.Errorf("a registration naming a deleted checkout is listed: %q", line)
	}
	if _, err := os.Stat(filepath.Join(runregistry.Dir(), "epic-gone.json")); !os.IsNotExist(err) {
		t.Errorf("the registration naming a deleted checkout was kept (stat: %v)", err)
	}
}
