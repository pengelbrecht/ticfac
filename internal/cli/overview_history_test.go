package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runregistry"
	"github.com/pengelbrecht/ticfac/internal/tk"
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
func historyFactory(t *testing.T, now time.Time) (attention, history []string, requests *[]cloudFactoryRequest) {
	t.Helper()
	at := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(time.RFC3339) }
	runs := []any{}
	project := ""
	add := func(id, epic, state string, startedAgo, endedAgo time.Duration) {
		record := map[string]any{"run_id": id, "epic": epic, "state": state, "started_at": at(startedAgo)}
		if project != "" {
			record["project"] = project
		}
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
	// twenty runs that finished a month ago — nineteen of another project the
	// factory also hosts (the operator's factory runs ticks' PR reviews), and
	// one of this checkout's own (see historyRepo).
	add(hex(4), "opn", "failed", 6*time.Hour, 5*time.Hour)
	add(hex(5), "cls", "failed", 2*time.Hour, time.Hour)
	history = []string{hex(4), hex(5)}
	for i := 0; i < 20; i++ {
		state := "failed"
		if i%3 == 0 {
			state = "completed"
		}
		id := hex(100 + i)
		project = "other/proj"
		if i == ownOldRun {
			project = ""
		}
		add(id, fmt.Sprintf("o%02d", i), state, 31*24*time.Hour, 30*24*time.Hour)
		project = ""
		history = append(history, id)
	}

	endpoint, requests := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		if request.Path == "/api/runs" {
			return 200, map[string]any{"runs": runs}
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	return attention, history, requests
}

// ownOldRun is the one old run of the history fixture that is THIS
// checkout's: its record alone cannot say whether it holds a person (its
// epic's records might), so even the human view gathers it before it ages
// into history.
const ownOldRun = 19

// historyRepo is a checkout whose origin names the GitHub project acme/project
// — so the factory's runs of other projects are told apart from its own — and
// is a real bare repository beside it, so a records fetch fails fast instead
// of reaching the network.
func historyRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "acme", "project.git")
	repo := filepath.Join(root, "repo")
	execTestCmd(t, root, "git", "init", "--quiet", "--bare", bare)
	execTestCmd(t, root, "git", "init", "--quiet", repo)
	execTestCmd(t, repo, "git", "remote", "add", "origin", "file://"+bare)
	return repo
}

// feedReadsOf names the runs whose feed the factory was asked for.
func feedReadsOf(requests []cloudFactoryRequest) map[string]bool {
	read := map[string]bool{}
	for _, request := range requests {
		if rest, ok := strings.CutPrefix(request.Path, "/api/runs/"); ok {
			if id, ok := strings.CutSuffix(rest, "/events"); ok {
				read[id] = true
			}
		}
	}
	return read
}

// countGraphReads swaps the tracker seam for one that records which epics
// were read, answering the same fake graph fakeOverviewGraph answers.
func countGraphReads(t *testing.T) func() map[string]bool {
	t.Helper()
	var mu sync.Mutex
	read := map[string]bool{}
	real := epicGraph
	t.Cleanup(func() { epicGraph = real })
	epicGraph = func(_ context.Context, _ string, epicID string) *tk.Graph {
		mu.Lock()
		read[epicID] = true
		mu.Unlock()
		return fakeGraph()
	}
	return func() map[string]bool {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]bool{}
		for k, v := range read {
			out[k] = v
		}
		return out
	}
}

// The overview is glanced at, so it must be quick, and on the operator's
// machine it took 41s: every one of ~90 cloud runs got a full status model —
// its feed read from the factory, its epic's graph from the tracker, its PR
// from the forge — including the ~70 the screen then collapsed into one
// line. A history run needs none of that to be recognised as history: its
// record says it finished, when, and for which epic. The human view reads
// nothing more for it; --json and --all, which show it, still read it all.
func TestTheBareOverviewReadsNothingMoreForAHistoryRun(t *testing.T) {
	now := time.Now()
	ownRegistry(t)
	repo := historyRepo(t)
	attention, history, requests := historyFactory(t, now)
	fakeOverviewGraph(t)
	graphReads := countGraphReads(t)
	fakeEpicStatus(t, "cls")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--repo", repo}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	feeds := feedReadsOf(*requests)
	for _, id := range history {
		if id == fmt.Sprintf("run_%032x", 100+ownOldRun) {
			continue // this checkout's own: gathered, then aged into history
		}
		if feeds[id] {
			t.Errorf("the human view read the feed of history run %s", id)
		}
	}
	for _, id := range attention {
		if !feeds[id] {
			t.Errorf("the human view did not read the feed of %s, which it lists", id)
		}
	}
	graphs := graphReads()
	for _, epic := range []string{"cls"} {
		if graphs[epic] {
			t.Errorf("the human view read the tracker graph of epic %s, whose only run is history", epic)
		}
	}

	// --json lists every run with its full model, so it reads them all.
	*requests = (*requests)[:0]
	stdout.Reset()
	if code := Run([]string{"--repo", repo, "--json"}, &stdout, &stderr); code != exitSuccess {
		t.Fatalf("the bare overview --json exits %d:\n%s\n%s", code, stdout.String(), stderr.String())
	}
	feeds = feedReadsOf(*requests)
	for _, id := range append(append([]string{}, attention...), history...) {
		if !feeds[id] {
			t.Errorf("--json did not read the feed of %s", id)
		}
	}
}

func TestTheBareOverviewLeadsWithWhatNeedsAPersonAndCollapsesHistory(t *testing.T) {
	now := time.Now()
	ownRegistry(t)
	repo := historyRepo(t)
	attention, history, _ := historyFactory(t, now)
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
	// Every listed run's row is its first line plus the dashboard headline
	// its gathered model carries (tick 3rc); the history is still one
	// summary line. Counting the rows' first lines — the headline lines are
	// indented under them — the screen holds exactly the attention rows and
	// the summary.
	rows, summaries := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "  ") {
			continue // a row's dashboard headline (tick 3rc)
		}
		if strings.Contains(line, "older run") && strings.Contains(line, "not shown") {
			summaries++
			continue
		}
		rows++
	}
	if rows != len(attention) || summaries != 1 {
		t.Errorf("the overview holds %d rows and %d summary lines, want %d rows and one summary line:\n%s",
			rows, summaries, len(attention), out)
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

	// History rows print exactly one line each even though --all gathers
	// them in full: no dashboard headline under a history row (tick 3rc).
	for _, id := range history {
		if lineOf(all, id) == "" {
			continue // listed at all is asserted above
		}
		if after := lineAfter(all, id); strings.HasPrefix(after, "  ") {
			t.Errorf("%s is history and carries a dashboard headline under its line: %q", id, after)
		}
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
