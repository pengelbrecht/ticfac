package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The base-branch fold, as the Phase 1 gate run found it missing: an epic's
// integration branch is cut from the base branch once and diverges from it the
// moment either side writes. Both sides write TRACKER RECORDS — the run claims
// and closes on the integration branch, people file ticks on the base — so a
// reconciler that never folds the base back in plans from a tracker that is
// missing whatever arrived since the fork. Run gate-cia-3 refused with "epic
// cia has no dispatchable tick" while `tk graph` on main listed two.
//
// These tests use a tracker whose records ARE files in the checkout it is
// pointed at, because that is what tk is: what the fold has to make true is
// that a record which arrived on the base branch is a record the next graph
// read lists.

// ------------------------------------------------------------ the tracker ---

// repoTracker is the tracker with nothing between it and `.tick/`: every read
// is a read of `.tick/issues/*.json` in the checkout it runs in, and every
// write is a write of one of those files. It is the fixture's stand-in for tk
// itself, including tk's merge drivers.
type repoTracker struct {
	dir     string
	drivers map[string]string
}

// In is tk's `--repo`: the same tracker, reading and writing another checkout.
func (t *repoTracker) In(dir string) Tracker {
	clone := *t
	clone.dir = dir
	return &clone
}

// MergeDrivers is how the reconciler learns to merge this tracker's records
// the way the tracker itself would.
func (t *repoTracker) MergeDrivers() (map[string]string, error) { return t.drivers, nil }

func (t *repoTracker) records() (map[string]tk.Tick, error) {
	paths, err := filepath.Glob(filepath.Join(t.dir, ".tick", "issues", "*.json"))
	if err != nil {
		return nil, err
	}
	out := make(map[string]tk.Tick, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var tick tk.Tick
		if err := json.Unmarshal(raw, &tick); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[tick.ID] = tick
	}
	return out, nil
}

func (t *repoTracker) Graph(_ context.Context, epicID string) (tk.Graph, error) {
	records, err := t.records()
	if err != nil {
		return tk.Graph{}, err
	}
	var ids []string
	for id, tick := range records {
		if tick.Parent == epicID && tick.Status != "closed" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	graph := tk.Graph{Epic: tk.GraphEpic{ID: epicID, Title: "the fixture epic"}}
	wave := tk.GraphWave{Wave: 1, Parallel: len(ids), Ready: true}
	for _, id := range ids {
		tick := records[id]
		wave.Tasks = append(wave.Tasks, tk.GraphTask{
			ID: id, Title: tick.Title, Status: tick.Status, Priority: 2,
			Role: tick.Role, AgentReady: true,
		})
	}
	if len(wave.Tasks) > 0 {
		graph.Waves = append(graph.Waves, wave)
	}
	graph.Stats = tk.GraphStats{TotalTasks: len(records), WaveCount: len(graph.Waves)}
	return graph, nil
}

func (t *repoTracker) Show(_ context.Context, tickID string) (tk.Tick, error) {
	records, err := t.records()
	if err != nil {
		return tk.Tick{}, err
	}
	tick, ok := records[tickID]
	if !ok {
		return tk.Tick{}, fmt.Errorf("no tick %s in %s", tickID, t.dir)
	}
	return tick, nil
}

func (t *repoTracker) Claim(_ context.Context, tickID, owner string) (tk.Tick, error) {
	return t.mutate(tickID, func(tick *tk.Tick) { tick.Status, tick.Owner = "in_progress", owner })
}

func (t *repoTracker) Note(_ context.Context, tickID, text string) (tk.Tick, error) {
	return t.mutate(tickID, func(tick *tk.Tick) {
		tick.Notes = strings.TrimSpace(tick.Notes + "\n" + text)
	})
}

func (t *repoTracker) Close(_ context.Context, tickID string) (tk.Tick, error) {
	return t.mutate(tickID, func(tick *tk.Tick) {
		tick.Status, tick.ClosedReason = "closed", "closed by ticfac"
	})
}

func (t *repoTracker) mutate(tickID string, apply func(*tk.Tick)) (tk.Tick, error) {
	tick, err := t.Show(context.Background(), tickID)
	if err != nil {
		return tk.Tick{}, err
	}
	apply(&tick)
	return tick, writeRecord(t.dir, tick)
}

// writeRecord puts a tracker record where tk keeps it:
// `.tick/issues/<id>.json` of the repository it is about.
func writeRecord(dir string, tick tk.Tick) error {
	issues := filepath.Join(dir, ".tick", "issues")
	if err := os.MkdirAll(issues, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(tick, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(issues, tick.ID+".json"), append(raw, '\n'), 0o644)
}

// gitAttributes is what a repository tk manages declares, and the only way git
// reaches a merge driver: by the NAME on the path it is merging.
const gitAttributes = `.tick/issues/*.json merge=tick
.tick/activity/activity.jsonl merge=tick-activity
`

// newRepoTracker builds the tracker and the fake tk its merge drivers run as.
// The log is where git's own invocations are recorded, so a test can say what
// ran rather than inferring it from a file that happened to merge.
func newRepoTracker(t *testing.T, dir string) (*repoTracker, string) {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "internal", "reconcile", "testdata", "fake-tk.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the fake tk is missing: %v", err)
	}
	log := filepath.Join(t.TempDir(), "merge-drivers.log")
	driver := func(command string) string {
		return fmt.Sprintf("FAKE_TK_LOG='%s' /bin/sh '%s' %s %%O %%A %%B %%P", log, script, command)
	}
	return &repoTracker{dir: dir, drivers: map[string]string{
		"tick":          driver("merge-file"),
		"tick-activity": driver("merge-activity"),
	}}, log
}

// ------------------------------------------------------------ the fixtures ---

// seedTracker puts an epic and its ticks on the base branch, with the
// `.gitattributes` that makes tk's drivers reachable, and pushes them.
func seedTracker(t *testing.T, repo *testRepo, ticks ...tk.Tick) {
	t.Helper()
	write(t, filepath.Join(repo.Dir, ".gitattributes"), gitAttributes)
	for _, tick := range ticks {
		if err := writeRecord(repo.Dir, tick); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "the tracker's records")
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
}

// forkIntegrationBranch is the moment the two sides start to diverge: the
// integration branch is cut from the base branch, and everything either side
// writes afterwards is what the fold has to bring back together.
func forkIntegrationBranch(t *testing.T, repo *testRepo, branch string) {
	t.Helper()
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
}

// commitOnIntegrationBranch writes on the integration branch as another actor
// would — an earlier wave of this epic, or a previous run — through a clone, so
// that nothing about the reconciler's own checkout is what makes the test pass.
func commitOnIntegrationBranch(t *testing.T, f *fixture, branch, path, content, message string) {
	t.Helper()
	dir := filepath.Join(f.Root, "other-side")
	if _, err := os.Stat(dir); err != nil {
		cloneRepo(t, f.Repo.Origin, dir)
	}
	mustRun(t, dir, "git", "fetch", "--quiet", "origin", branch)
	mustRun(t, dir, "git", "checkout", "--quiet", "-B", "side", "FETCH_HEAD")
	writeUnder(t, dir, path, content)
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", message)
	mustRun(t, dir, "git", "push", "--quiet", "origin", "side:refs/heads/"+branch)
}

// commitOnBase is the other side: what arrives on the base branch after the
// integration branch forked.
func commitOnBase(t *testing.T, repo *testRepo, path, content, message string) {
	t.Helper()
	writeUnder(t, repo.Dir, path, content)
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", message)
	mustRun(t, repo.Dir, "git", "push", "--quiet", "origin", "main")
}

func writeUnder(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, full, content)
}

// showOnOrigin reads one path at the head of a branch as ORIGIN has it, which
// is the only copy that counts.
func showOnOrigin(t *testing.T, f *fixture, branch, path string) string {
	t.Helper()
	return mustRun(t, f.Repo.Origin, "git", "show", refFor(branch)+":"+path)
}

// ----------------------------------------------------------- the criteria ---

// The acceptance criterion, and the incident: a tick filed on the base branch
// after the integration branch forked is planned and dispatched by the next
// run. The base is read from the EPIC's own `base_branch` — this run is told
// `--base HEAD`, which names no branch at all.
func TestATickFiledOnTheBaseAfterTheForkIsDispatchedByTheNextRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	tracker, _ := newRepoTracker(t, f.Repo.Dir)

	seedTracker(t, f.Repo,
		tk.Tick{ID: "qeu", Title: "the epic", Status: "open", Type: "epic", BaseBranch: "main"},
		tk.Tick{ID: "a1", Title: "the tick the run was cut for", Status: "open", Type: "task", Parent: "qeu"},
	)
	forkIntegrationBranch(t, f.Repo, "epic/qeu")

	// The follow-up somebody files while the run is under way. It exists on
	// main and nowhere else.
	commitOnBase(t, f.Repo, ".tick/issues/n1.json", recordJSON(t,
		tk.Tick{ID: "n1", Title: "the follow-up filed on main", Status: "open", Type: "task", Parent: "qeu"}),
		"file n1 on main")

	opts := f.options(f.Repo, fixtureOptions{})
	opts.Tracker = tracker
	opts.BaseRef = "HEAD"
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	var dispatched []string
	for _, event := range r.Journal() {
		if event.Stage == StageDispatched {
			dispatched = append(dispatched, event.Tick)
		}
	}
	if strings.Join(dispatched, ",") != "a1,n1" {
		t.Fatalf("the run dispatched %v; n1 was filed on main after %s forked and the run never saw it",
			dispatched, r.IntegrationBranch())
	}
	if strings.Join(result.Closed, ",") != "a1,n1" {
		t.Errorf("the run closed %v, want both ticks", result.Closed)
	}

	// And the fold itself is on origin: the integration branch carries main.
	head := strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", "refs/heads/main"))
	if !mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", head, refFor(r.IntegrationBranch())) {
		t.Errorf("%s does not contain main at %s", r.IntegrationBranch(), short(head))
	}
}

// Both sides wrote `.tick/activity/activity.jsonl` — the run appended to it on
// the integration branch, somebody else appended to it on main — and the fold
// resolves it through tk's own driver rather than through git's line merge,
// which would conflict on two appends at the same end of the file.
//
// The graph is empty on purpose: every tick of this epic is closed, so the run
// refuses for want of anything to dispatch AFTER the fold has landed. What is
// under test is the merge, and a fixture that also dispatched two jobs would
// take a hundred times as long to say the same thing.
func TestBothSidesOfTheActivityLogMergeThroughTheTrackersOwnDriver(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	tracker, driverLog := newRepoTracker(t, f.Repo.Dir)

	seedTracker(t, f.Repo,
		tk.Tick{ID: "qeu", Title: "the epic", Status: "open", Type: "epic"},
		tk.Tick{ID: "a1", Title: "already done", Status: "closed", Type: "task", Parent: "qeu"},
	)
	commitOnBase(t, f.Repo, ".tick/activity/activity.jsonl",
		`{"at":"1","what":"the epic was planned"}`+"\n", "the activity both sides start from")
	forkIntegrationBranch(t, f.Repo, "epic/qeu")

	// Two appends at the same end of the same file: a conflict for git, a
	// union for tk.
	commitOnIntegrationBranch(t, f, "epic/qeu", ".tick/activity/activity.jsonl",
		`{"at":"1","what":"the epic was planned"}`+"\n"+`{"at":"2","what":"a1 was closed by the run"}`+"\n",
		"the run's own activity")
	commitOnBase(t, f.Repo, ".tick/activity/activity.jsonl",
		`{"at":"1","what":"the epic was planned"}`+"\n"+`{"at":"3","what":"a note filed on main"}`+"\n",
		"activity filed on main")

	opts := f.options(f.Repo, fixtureOptions{})
	opts.Tracker = tracker
	opts.BaseRef = "main"
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RunProtected(context.Background()); err == nil || !strings.Contains(err.Error(), "no dispatchable tick") {
		t.Fatalf("the run ended with %v, want the refusal of an epic whose every tick is closed", err)
	}

	merged := showOnOrigin(t, f, r.IntegrationBranch(), ".tick/activity/activity.jsonl")
	for _, line := range []string{"the epic was planned", "a1 was closed by the run", "a note filed on main"} {
		if !strings.Contains(merged, line) {
			t.Errorf("the merged activity log lost %q:\n%s", line, merged)
		}
	}
	if strings.Contains(merged, "<<<<<<<") {
		t.Errorf("the activity log was merged by git rather than by the tracker's driver:\n%s", merged)
	}

	// And it was the DRIVER that did it, with git's four arguments — the
	// mistake the manifest's argv exists to prevent is a driver invoked with
	// three of them.
	raw, err := os.ReadFile(driverLog)
	if err != nil {
		t.Fatalf("git ran no merge driver at all: %v", err)
	}
	var invocations []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "merge-activity ") {
			invocations = append(invocations, line)
		}
	}
	if len(invocations) == 0 {
		t.Fatalf("merge-activity was never invoked; git ran:\n%s", raw)
	}
	for _, invocation := range invocations {
		if got := len(strings.Fields(invocation)); got != 5 {
			t.Errorf("merge-activity was invoked with %d arguments, want four (%%O %%A %%B %%P): %s",
				got-1, invocation)
		}
	}
}

// A source file both sides changed is a REFUSAL, with a reason of its own.
// Skipping the fold would put the run back where the gate run found it —
// planning from a tracker that is missing ticks — and resolving the conflict is
// a role job's decision (resolve-conflict, Phase 2), not a merge this
// reconciler may invent.
func TestASourceConflictRefusesTheRunWithItsOwnReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	tracker, _ := newRepoTracker(t, f.Repo.Dir)

	seedTracker(t, f.Repo,
		tk.Tick{ID: "qeu", Title: "the epic", Status: "open", Type: "epic"},
		tk.Tick{ID: "a1", Title: "the tick the run was cut for", Status: "open", Type: "task", Parent: "qeu"},
	)
	forkIntegrationBranch(t, f.Repo, "epic/qeu")

	commitOnIntegrationBranch(t, f, "epic/qeu", "README.md", "# the run's line\n", "the run's edit")
	commitOnBase(t, f.Repo, "README.md", "# main's line\n", "main's edit")

	opts := f.options(f.Repo, fixtureOptions{})
	opts.Tracker = tracker
	opts.BaseRef = "main"
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run ended with %v, want a refusal it recorded", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedBaseRefresh {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedBaseRefresh)
	}
	if !strings.Contains(result.Failure.Message, "README.md") {
		t.Errorf("the refusal does not say which file conflicts: %s", result.Failure.Message)
	}

	// Nothing was dispatched and nothing was pushed: a fold that did not
	// happen must not look like one that did.
	for _, event := range r.Journal() {
		if event.Stage == StageDispatched {
			t.Errorf("the run dispatched %s over a base branch it could not fold in", event.Tick)
		}
	}
	// The branch moved — the refusal's own checkpoint landed on it, because
	// that is where `.ticfac/` lives — but it does not carry main: a fold that
	// conflicted must not look like one that happened.
	base := strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor("main")))
	if mustRunAllowingFailure(f.Repo.Origin, "git", "merge-base", "--is-ancestor", base, refFor("epic/qeu")) {
		t.Errorf("epic/qeu carries main at %s although the fold conflicted", short(base))
	}

	// And the refusal is DURABLE: a checkpoint on origin says the run failed
	// and why, so the next incarnation does not walk into the same merge.
	store := openStore(t, f, r)
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("the run left no checkpoint: %v", err)
	}
	if checkpoint.State != runstate.StateFailed || !strings.Contains(checkpoint.Reason, "README.md") {
		t.Errorf("the checkpoint says %s: %s", checkpoint.State, checkpoint.Reason)
	}
}

// A base branch this remote does not have is not a refusal: `--base HEAD` is
// the default, and a run cut from a commit has nothing to fold. What it must
// not be is silent.
func TestARunWhoseBaseIsNotABranchSaysSoAndRunsOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	var said string
	for _, event := range r.Journal() {
		if event.Stage == StageRefreshed {
			said = event.Detail
		}
	}
	if !strings.Contains(said, "HEAD") {
		t.Errorf("the run did not record why it folded nothing in: %q", said)
	}
}

func recordJSON(t *testing.T, tick tk.Tick) string {
	t.Helper()
	raw, err := json.MarshalIndent(tick, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + "\n"
}
