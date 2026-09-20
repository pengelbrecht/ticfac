package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The test harness: a real repository, a real bare origin, the real
// `.ticfac/` run-state store over it, the REAL local subprocess executor with
// its shipped supervisor binary, and a fake runner in place of the agent.
//
// Nothing the reconciler talks to is mocked except the two things these tests
// are not about: the agent, and the tracker binary. The tracker fake is a file
// on disk rather than a map in memory precisely so that a restart "on a fresh
// clone" reads the same tracker the previous incarnation wrote — which is what
// makes "no false close" an assertion instead of a hope.

var executorBin string

func TestMain(m *testing.M) {
	root, err := contracts.RepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "locate the module root: %v\n", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "ticfac-reconcile-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	executorBin = filepath.Join(dir, "ticfac-exec-subprocess")
	build := exec.Command("go", "build", "-o", executorBin, "./cmd/ticfac-exec-subprocess")
	build.Dir = root
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build the executor the reconciler drives: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// ------------------------------------------------------------- tracker ---

// fakeTracker is a tk client's answers, kept in a JSON file so that two
// reconciler incarnations — the one that is killed and the one that restarts —
// see the same tracker, exactly as they would see the same `.tick/` directory.
//
// Every write also MIRRORS the tick's record to `.tick/issues/<id>.json` in the
// checkout the tracker is POINTED AT, because that is what tk does: a tracker
// record is a file in the repository it is about. The reconciler's job is to
// make those files durable, so a fake that only mutated a map would leave the
// thing under test with nothing to commit.
type fakeTracker struct {
	path string

	// mu is shared by the whole family of relocated fakes: they are one
	// tracker, and two of them writing the state file at once is the test's own
	// race and not the reconciler's.
	mu *sync.Mutex

	// dir is the checkout this tracker writes its records into — tk's own
	// `--repo`. Empty until a run points it at one.
	dir string

	// calls counts what was asked of the tracker, so "the tick was closed
	// twice" is a number and not an impression.
	calls map[string]int

	// claims is the tracker's own view of the width (tick 3mp): how many ticks
	// are claimed and not yet closed, and the most that were ever open at once.
	// It is a POINTER so the relocated fakes share one count, the way they
	// share one mutex — they are one tracker, and a width counted per copy
	// would be no width at all.
	claims *claimCount
}

// claimCount is what tk counts: claims open, and the high-water mark. refuseAt
// makes the tracker refuse a claim that would exceed a width, which is the
// guard epic dha hit.
type claimCount struct {
	open     int
	peak     int
	refuseAt int
}

// In is the fake's half of the reconciler's relocation: the same tracker, with
// its records written into another checkout.
func (f *fakeTracker) In(dir string) Tracker {
	return &fakeTracker{path: f.path, mu: f.mu, dir: dir, calls: f.calls, claims: f.claims}
}

type trackerState struct {
	Epic  string             `json:"epic"`
	Waves [][]string         `json:"waves"`
	Ticks map[string]tk.Tick `json:"ticks"`
	Roles map[string]string  `json:"roles"`
	Order []string           `json:"order"`
	// Blocks is the graph-position fact a test sets by hand: which ticks each
	// tick blocks. The real tk derives it from the tick files; the fake has no
	// files to derive it from, so a test that cares states it.
	Blocks map[string][]string `json:"blocks,omitempty"`

	// BlockedBy is the same edge read the other way, and the one tk LAYERS by.
	// A fixture that sets it gets tk's own behaviour instead of the declared
	// Waves: every read re-layers the graph from what is still open, so a
	// blocker closing moves its dependents up a wave exactly as the tracker
	// would — which is the fact tick g50 is about. A fixture that leaves it
	// unset keeps the fixed Waves it declared.
	BlockedBy map[string][]string `json:"blocked_by,omitempty"`
}

func newTracker(t *testing.T, dir string) *fakeTracker {
	t.Helper()
	tracker := &fakeTracker{
		path: filepath.Join(dir, "tracker.json"), mu: &sync.Mutex{},
		calls: map[string]int{}, claims: &claimCount{},
	}
	state := trackerState{
		Epic:  "qeu",
		Waves: [][]string{{"a1", "a2"}, {"b1"}, {"rv", "co"}},
		Ticks: map[string]tk.Tick{},
		Roles: map[string]string{"rv": "review", "co": "closeout"},
		Order: []string{"a1", "a2", "b1", "rv", "co"},
	}
	for _, id := range state.Order {
		state.Ticks[id] = tk.Tick{ID: id, Title: "tick " + id, Status: "open", Type: "task", Parent: "qeu", Priority: 2}
	}
	tracker.write(t, state)
	return tracker
}

func (f *fakeTracker) write(t *testing.T, state trackerState) {
	t.Helper()
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeTracker) load() (trackerState, error) {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		return trackerState{}, err
	}
	var state trackerState
	err = json.Unmarshal(raw, &state)
	return state, err
}

func (f *fakeTracker) save(state trackerState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.path, raw, 0o644)
}

// record is the file tk would leave behind: the tick, as JSON, at
// `.tick/issues/<id>.json` of the checkout this tracker runs in. A tracker
// pointed at nothing writes nothing, which is the fixture's way of saying a
// tracker nobody relocated has no repository to write into.
func (f *fakeTracker) record(tick tk.Tick) error {
	if f.dir == "" {
		return nil
	}
	dir := filepath.Join(f.dir, ".tick", "issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(tick, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, tick.ID+".json"), append(raw, '\n'), 0o644)
}

func (f *fakeTracker) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeTracker) tally(name string) {
	f.mu.Lock()
	f.calls[name]++
	f.mu.Unlock()
}

// waves is the layering this tracker answers with: the declared Waves, unless
// the fixture stated dependency edges, in which case the graph is LAYERED on
// every read the way tk layers it — a tick sits one wave behind the latest
// blocker of its that is still open, and a closed blocker holds nothing back.
//
// This is the fixture's half of tick g50. A tracker whose waves are a constant
// cannot show the defect at all: the whole failure is that wave numbers are a
// reading of a graph at one moment, and a fake that never re-layers agrees
// with a stale plan forever.
func (s trackerState) waves() [][]string {
	if len(s.BlockedBy) == 0 {
		return s.Waves
	}
	wave := map[string]int{}
	for _, id := range s.Order {
		wave[id] = 1
	}
	// One relaxation per tick settles any acyclic graph, and a cycle — which
	// tk does not produce — simply stops moving rather than looping.
	for range s.Order {
		for _, id := range s.Order {
			for _, blocker := range s.BlockedBy[id] {
				if s.Ticks[blocker].Status == "closed" {
					continue
				}
				if behind := wave[blocker] + 1; behind > wave[id] {
					wave[id] = behind
				}
			}
		}
	}
	var out [][]string
	for depth := 1; depth <= len(s.Order); depth++ {
		var layer []string
		for _, id := range s.Order {
			if wave[id] == depth {
				layer = append(layer, id)
			}
		}
		if len(layer) > 0 {
			out = append(out, layer)
		}
	}
	return out
}

func (f *fakeTracker) Graph(_ context.Context, epicID string) (tk.Graph, error) {
	f.tally("graph")
	state, err := f.load()
	if err != nil {
		return tk.Graph{}, err
	}
	graph := tk.Graph{Epic: tk.GraphEpic{ID: epicID, Title: "the fixture epic"}}
	for i, wave := range state.waves() {
		w := tk.GraphWave{Wave: i + 1, Parallel: len(wave), Ready: i == 0}
		for _, id := range wave {
			tick := state.Ticks[id]
			w.Tasks = append(w.Tasks, tk.GraphTask{
				ID: id, Title: tick.Title, Status: tick.Status, Priority: tick.Priority,
				Type: tick.Type, Labels: tick.Labels, Role: state.Roles[id],
				Blocks: state.Blocks[id], BlockedBy: state.BlockedBy[id],
				AgentReady: tick.Status != "closed",
			})
		}
		graph.Waves = append(graph.Waves, w)
	}
	graph.Stats = tk.GraphStats{TotalTasks: len(state.Ticks), WaveCount: len(state.Waves)}
	return graph, nil
}

func (f *fakeTracker) Show(_ context.Context, tickID string) (tk.Tick, error) {
	f.tally("show")
	state, err := f.load()
	if err != nil {
		return tk.Tick{}, err
	}
	tick, ok := state.Ticks[tickID]
	if !ok {
		return tk.Tick{}, fmt.Errorf("no tick %s", tickID)
	}
	return tick, nil
}

func (f *fakeTracker) Claim(_ context.Context, tickID, owner string) (tk.Tick, error) {
	f.tally("claim:" + tickID)
	// tk's own guard, in the fake (tick 3mp): a claim lives until its tick
	// closes, and one beyond the declared width is REFUSED with the typed
	// error the real tk returns for exit 8.
	f.mu.Lock()
	if f.claims.refuseAt > 0 && f.claims.open >= f.claims.refuseAt {
		open := f.claims.open
		f.mu.Unlock()
		return tk.Tick{}, &tk.ErrDispatchWidth{
			Command:  "claim",
			ExitCode: 8,
			Stderr: fmt.Sprintf("wave width %d is full: %d implementer(s) already in flight; claiming %s would make %d",
				f.claims.refuseAt, open, tickID, open+1),
		}
	}
	f.claims.open++
	if f.claims.open > f.claims.peak {
		f.claims.peak = f.claims.open
	}
	f.mu.Unlock()
	return f.mutate(tickID, func(tick *tk.Tick) {
		tick.Status, tick.Owner = "in_progress", owner
	})
}

// peakClaims is the most claims this tracker ever had open at once: the number
// a width assertion reads, rather than the reconciler's own bookkeeping.
func (f *fakeTracker) peakClaims() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims.peak
}

// refuseClaimsBeyond makes this tracker enforce a width, as tk does.
func (f *fakeTracker) refuseClaimsBeyond(width int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims.refuseAt = width
}

func (f *fakeTracker) Note(_ context.Context, tickID, text string) (tk.Tick, error) {
	f.tally("note:" + tickID)
	return f.mutate(tickID, func(tick *tk.Tick) {
		tick.Notes = strings.TrimSpace(tick.Notes + "\n" + text)
	})
}

func (f *fakeTracker) Close(_ context.Context, tickID string) (tk.Tick, error) {
	f.tally("close:" + tickID)
	// The claim ends HERE and nowhere earlier, which is the whole of tick 3mp:
	// a settled attempt still being integrated is still claimed.
	f.mu.Lock()
	if f.claims.open > 0 {
		f.claims.open--
	}
	f.mu.Unlock()
	return f.mutate(tickID, func(tick *tk.Tick) {
		tick.Status, tick.ClosedReason = "closed", "closed by ticfac"
	})
}

func (f *fakeTracker) mutate(tickID string, apply func(*tk.Tick)) (tk.Tick, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.load()
	if err != nil {
		return tk.Tick{}, err
	}
	tick, ok := state.Ticks[tickID]
	if !ok {
		return tk.Tick{}, fmt.Errorf("no tick %s", tickID)
	}
	apply(&tick)
	state.Ticks[tickID] = tick
	if err := f.save(state); err != nil {
		return tick, err
	}
	return tick, f.record(tick)
}

// ---------------------------------------------------------------- repo ---

type testRepo struct {
	Dir    string
	Origin string
	Base   string
}

// newRepo makes a repository with one commit, a declared gate, and a bare
// origin to push to.
func newRepo(t *testing.T, root, name, gate string) *testRepo {
	t.Helper()
	dir := filepath.Join(root, name)
	origin := filepath.Join(root, name+"-origin.git")

	mustRun(t, root, "git", "init", "--quiet", "--bare", "-b", "main", origin)
	// The origin is a forge's stand-in, and a forge does not repack a
	// repository under the pushes it is receiving. A bare repository on disk
	// does: every receive-pack ends by starting a detached `git maintenance
	// run --auto`, which on a git with the geometric default repacks, and its
	// prune-packed removes an object fan-out directory another push is
	// migrating its objects into — "unable to migrate objects to permanent
	// storage", the origin-side twin of tick mel. A single run starts dozens
	// of these. It is the harness's origin that does that, not the run, and
	// the run cannot say otherwise from the pushing end.
	mustRun(t, origin, "git", "config", "maintenance.auto", "false")
	mustRun(t, root, "git", "init", "--quiet", "-b", "main", dir)
	configure(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "README.md"), "# "+name+"\n")
	write(t, filepath.Join(dir, ".tick", "runners.toml"), gate)
	// The run-state contract's gitignore fragment, which every target
	// repository carries: the reconciler writes its run event feed under
	// .ticfac/logs/, and without the fragment that exhaust is a dirty tree
	// a later `git add -A` sweeps up (contracts/ticfac-run-state.json,
	// contracts/run-event-feed.json).
	write(t, filepath.Join(dir, ".gitignore"), strings.Join(runstate.Fragment, "\n")+"\n")
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "base")
	mustRun(t, dir, "git", "remote", "add", "origin", origin)
	mustRun(t, dir, "git", "push", "--quiet", "origin", "main")

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	return &testRepo{Dir: resolved, Origin: origin, Base: base}
}

// clone is the fresh checkout a restarted run reads its state from. A restart
// holding the previous run's working directory would prove nothing: everything
// it needs has to come from origin.
func cloneRepo(t *testing.T, origin, dir string) *testRepo {
	t.Helper()
	mustRun(t, filepath.Dir(dir), "git", "clone", "--quiet", origin, dir)
	configure(t, dir)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &testRepo{Dir: resolved, Origin: origin,
		Base: strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))}
}

func configure(t *testing.T, dir string) {
	t.Helper()
	mustRun(t, dir, "git", "config", "user.email", "reconciler@example.com")
	mustRun(t, dir, "git", "config", "user.name", "ticfac test")
	mustRun(t, dir, "git", "config", "commit.gpgsign", "false")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mustRunAllowingFailure is mustRun for a question whose answer is the exit
// code — `merge-base --is-ancestor` says yes or no, and neither is an error.
func mustRunAllowingFailure(dir, name string, args ...string) bool {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s (in %s): %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// -------------------------------------------------------------- gates ---

// passingGate is the smallest honest gate: it reads the integrated tree.
// The roles block mirrors the shipped implement-tick profile exactly
// (claude/sonnet), so the fixture exercises routing without changing any
// resolved model: the format requires [roles] in every runners.toml, and
// since tick wgi the profile reader validates the whole execution half.
const passingGate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`

// failingGate refuses everything, so that a run's close can be shown to depend
// on it.
const failingGate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "exit 3", description = "always refuses" }
`

// --------------------------------------------------------- the fixture ---

type fixture struct {
	t         *testing.T
	Root      string
	Repo      *testRepo
	Tracker   *fakeTracker
	StateRoot string
	Runner    []string

	// starts counts what the executor was asked to START, per job id. It is
	// the number "the live attempt was redispatched" is, rather than an
	// impression from a journal.
	starts map[string]int

	// specs and dispatches are what the reconciler actually ASKED for, per
	// tick: the JobSpec it issued and the dispatch it built the executor from.
	// A profile that routed nothing and a grade that was not issued are both
	// invisible in a journal.
	specs      map[string]*subprocess.JobSpec
	dispatches map[string]Dispatch

	// handles is every handle Start ever returned, in order. The supervisor
	// pid on a handle is frozen at that moment, which is what a pid FILE is
	// not: a second Start over the same attempt overwrites the file.
	handles []*subprocess.JobHandle

	mu sync.Mutex

	// wrap decorates every executor this fixture builds, for a test that has
	// to see the ORDER the reconciler asks for operations in.
	wrap func(Executor) Executor
}

type fixtureOptions struct {
	gate      string
	mode      string
	guardsOff map[string]bool
	stopAfter func(Event) bool
	repo      *testRepo
	budget    float64
	ceiling   float64

	// pullRequests is the code-hosting surface behind the PR + CI close-out
	// rule (tick 0iz): the fake forge a test that declares the rule supplies.
	// Nil is the honest default — a repo that declares no rule needs no
	// surface, and construction refuses one that does.
	pullRequests forge.PullRequests

	// runID overrides the run id the fixture's runs run under — the fact tick
	// n4h is about: a second run id is a re-run of the epic whose attempt
	// numbers begin again at 1 out of a run-state store of its own.
	runID string

	// gateTimeout overrides the CI wait bound for the admission tests, the
	// way the harness overrides every other cadence: a bound measured in
	// minutes is testable in milliseconds without the bound being a
	// test-only number.
	gateTimeout time.Duration

	// stallWarn overrides the run's stall threshold (tick 7zs), for the
	// same reason as gateTimeout: a threshold measured in minutes is
	// testable in milliseconds without the number being a test-only one.
	// Zero leaves the default.
	stallWarn time.Duration

	// progressProbe overrides how often a live attempt's worktree is walked
	// for the liveness record (tick dh1). The fixture's default is the
	// harness's own cadence rather than the production minute: a probe that
	// never comes round twice in a test's lifetime would make the count the
	// record exists for permanently null.
	progressProbe time.Duration

	// gateHeartbeat overrides how often a running gate says so (tick 9pz),
	// for the same reason as gateTimeout and stallWarn: a cadence measured in
	// minutes is testable in milliseconds without the minute being a
	// test-only number.
	gateHeartbeat time.Duration
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	root := t.TempDir()
	gate := opts.gate
	if gate == "" {
		gate = passingGate
	}
	repo := opts.repo
	if repo == nil {
		repo = newRepo(t, root, "repo", gate)
	}
	f := &fixture{
		t: t, Root: root, Repo: repo, starts: map[string]int{},
		specs: map[string]*subprocess.JobSpec{}, dispatches: map[string]Dispatch{},
		Tracker:   newTracker(t, root),
		StateRoot: filepath.Join(root, "exec-state"),
		Runner:    fakeRunnerArgv(t, opts.mode),
	}
	t.Cleanup(f.teardown)
	return f
}

func fakeRunnerArgv(t *testing.T, mode string) []string {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "internal", "reconcile", "testdata", "fake-runner.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the fake runner is missing: %v", err)
	}
	if mode == "" {
		mode = "report"
	}
	return []string{"/usr/bin/env", "FAKE_RUNNER_MODE=" + mode, "/bin/sh", script, "{{prompt}}"}
}

// options builds a reconciler's options against this fixture. `repo` is the
// checkout the reconciler works in, which a restart replaces with a fresh
// clone while everything else stays where it was.
func (f *fixture) options(repo *testRepo, opts fixtureOptions) Options {
	gateTimeout := 2 * time.Minute
	if opts.gateTimeout > 0 {
		gateTimeout = opts.gateTimeout
	}
	runID := opts.runID
	if runID == "" {
		runID = "r-fixture"
	}
	// Fine against every threshold a test declares and still far coarser than
	// the 20ms poll: the probe walks a worktree and shells out to git, and
	// tying it to the poll here would put twelve parallel fixtures' worth of
	// git processes on the machine and lose races that belong to other tests
	// (settle_identity_test.go has one it already documents).
	progressProbe := opts.progressProbe
	if progressProbe <= 0 {
		progressProbe = 200 * time.Millisecond
	}
	return Options{
		Repo:               repo.Dir,
		Remote:             "origin",
		EpicID:             "qeu",
		RunID:              runID,
		BaseRef:            "HEAD",
		Owner:              "ticfac-test",
		Tracker:            f.Tracker,
		ExecStateRoot:      f.StateRoot,
		GateConfig:         filepath.Join(repo.Dir, ".tick", "runners.toml"),
		GateTimeout:        gateTimeout,
		PollInterval:       20 * time.Millisecond,
		WipeThreshold:      10 * time.Second,
		StepCap:            60 * time.Millisecond,
		WallSeconds:        120,
		BudgetUSD:          opts.budget,
		CeilingUSD:         opts.ceiling,
		PullRequests:       opts.pullRequests,
		StallWarnAfter:     opts.stallWarn,
		ProgressProbeEvery: progressProbe,
		GateHeartbeatEvery: opts.gateHeartbeat,
		Sleep:              func(time.Duration) { time.Sleep(5 * time.Millisecond) },
		guardsOff:          opts.guardsOff,
		stopAfter:          opts.stopAfter,
		NewExecutor:        f.newExecutor,
	}
}

func (f *fixture) newExecutor(d Dispatch) (Executor, Substrate, error) {
	f.mu.Lock()
	f.dispatches[d.TickID] = d
	f.mu.Unlock()

	// The runner comes off the dispatch's profile exactly as the production
	// factory takes it; the argv is the fake runner's, so a routed runner is
	// observable without an agent CLI on the machine.
	runner, model, rolePrompt := "claude", "", ""
	if d.Profile != nil {
		if d.Profile.Runner != "" {
			runner = d.Profile.Runner
		}
		model, rolePrompt = d.Profile.Model, d.Profile.Prompt
	}
	executor, err := subprocess.New(subprocess.Options{
		Repo:           d.Repo,
		StateDir:       d.StateDir,
		Runner:         runner,
		Model:          model,
		RolePrompt:     rolePrompt,
		RunnerArgv:     f.Runner,
		SupervisorArgv: []string{executorBin, "supervise"},
		Remote:         d.Remote,
		Attempt:        d.Attempt,
		Try:            d.Try,
		PushInterval:   time.Second,
		// What the tick's earlier attempts found (tick nvn), forwarded the
		// way the production factories forward it.
		PriorReports: d.PriorReports,
		// What the tick's earlier attempts left PRESERVED (tick pbb),
		// forwarded the same way.
		PriorSnapshots: d.PriorSnapshots,
	})
	if err != nil {
		return nil, Substrate{}, err
	}
	var wrapped Executor = &recordingExecutor{fixture: f, Executor: executor}
	if f.wrap != nil {
		wrapped = f.wrap(wrapped)
	}
	// The fake's substrate: the fake runner is a local process, so the zero
	// Substrate — the same thing the production subprocess factory reports.
	return wrapped, Substrate{}, nil
}

// recordingExecutor remembers every handle so the fixture can stop whatever a
// killed run left behind, and counts starts so "the live attempt was
// redispatched" is a number.
type recordingExecutor struct {
	fixture *fixture
	*subprocess.Executor
}

func (e *recordingExecutor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	e.fixture.mu.Lock()
	e.fixture.starts[spec.JobID]++
	e.fixture.specs[tickOfJob(spec.JobID)] = spec
	e.fixture.mu.Unlock()

	handle, err := e.Executor.Start(spec)
	// Tracked whatever the run does next. A run cut between this return and
	// the reconciler's own record — which is exactly where the restart tests
	// cut it — still leaves a supervisor this fixture is responsible for
	// stopping, and the handle is where its pid is frozen.
	e.fixture.track(handle)
	return handle, err
}

// track remembers a handle this fixture must stop. Every Start goes through it.
func (f *fixture) track(handle *subprocess.JobHandle) {
	if handle == nil {
		return
	}
	f.mu.Lock()
	f.handles = append(f.handles, handle)
	f.mu.Unlock()
}

// tickOfJob reads the tick out of a job id. The reconciler owns the id's shape
// — the executor deliberately does not parse it — so this is the test's reader
// of the test's own fixture, not a second implementation of anything.
func tickOfJob(jobID string) string {
	for _, part := range strings.Split(jobID, "/") {
		if rest, ok := strings.CutPrefix(part, "tick-"); ok {
			return rest
		}
	}
	return jobID
}

// spec is the JobSpec one tick was dispatched with.
func (f *fixture) spec(tick string) *subprocess.JobSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[tick]
}

// dispatch is the Dispatch one tick's executor was built from.
func (f *fixture) dispatch(tick string) Dispatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dispatches[tick]
}

// startCount is how many times the executor was asked to start one job.
func (f *fixture) startCount(jobID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[jobID]
}

// teardown is what t.Cleanup runs: stop everything, then remove the temp root
// this fixture allocated. The two are separate because a test may stop a
// fixture's processes MID-TEST and go on using its directories — A8 kills the
// hung attempt and then clones the fixture's own origin — so only the cleanup
// path removes anything.
func (f *fixture) teardown() {
	f.stopEverything()
	// Ahead of t.TempDir()'s own single attempt, and retried: a process that
	// has just stopped existing has not necessarily released every fd into
	// these directories in the same instant kill() returned.
	removeWithRetry(f.Root)
}

// stopEverything kills every process this fixture's runs left behind and WAITS
// for each to actually be gone. It is idempotent, and safe to call mid-test.
//
// teardown is registered with t.Cleanup, which runs cleanups in LIFO order: newFixture
// takes its t.TempDir() before registering this, so this always runs first. But
// a kill signal SENT is not the same fact as a process being GONE — a
// supervisor mid-push into `repo-origin.git` when t.TempDir()'s RemoveAll
// starts is "directory not empty", not a passing test. That is the flake this
// waits out, and it is the same one internal/exec/subprocess fixed in 39a5259;
// the discipline here is that fix's.
//
// It kills only what an attempt's own LOCKS prove is alive (tick rmc). It used
// to SIGKILL the process group of every pid it could find saved under the
// state root — supervisor.pid, runner.pid, every runner pid an observation log
// had recorded — that kill(pid, 0) called alive. On a host measured reusing
// ~700 pids a second and wrapping at 99999, a pid a test saved a few minutes
// ago is routinely somebody else's by the time a teardown reads it, and the
// teardown then killed that somebody's whole process group. Every process the
// executor starts holds a lock of its own for exactly as long as it lives, so
// what the pid scraping existed to catch — an attempt whose handle the run
// never handed back, the first runner of a guard-off redispatch whose pid file
// the second overwrote — is caught by walking the state root for attempts'
// locks, and nothing that merely inherited a number is.
func (f *fixture) stopEverything() {
	states := f.attemptStates()
	for _, state := range states {
		_, _ = subprocess.KillLiveProcesses(state, 0)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, state := range states {
		_, _ = subprocess.KillLiveProcesses(state, time.Until(deadline))
	}
}

// attemptStates is every attempt state directory this fixture may still have
// processes in: the one on every handle Start returned, and every one under the
// fixture's state root — which catches an attempt whose handle the run never
// handed back, a run cut mid-dispatch by the restart tests' simulated kill.
func (f *fixture) attemptStates() []string {
	f.mu.Lock()
	handles := append([]*subprocess.JobHandle{}, f.handles...)
	f.mu.Unlock()

	seen := map[string]bool{}
	var states []string
	add := func(state string) {
		if state != "" && !seen[state] {
			seen[state] = true
			states = append(states, state)
		}
	}
	for _, handle := range handles {
		if local, err := handle.Local(); err == nil {
			add(local.State)
		}
	}
	_ = filepath.Walk(f.StateRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}
		if info.Name() == "locks" {
			add(filepath.Dir(path))
			return filepath.SkipDir
		}
		if info.Name() == "worktree" {
			return filepath.SkipDir
		}
		return nil
	})
	return states
}

// removeWithRetry removes a directory this fixture allocated with t.TempDir(),
// ahead of that same t.TempDir()'s own later cleanup, retrying "directory not
// empty" for a bounded time.
func removeWithRetry(path string) {
	if path == "" {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := os.RemoveAll(path); err == nil {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// run builds a reconciler and runs it, returning the result and whatever
// stopped it — including the simulated kill, which arrives as errStopped.
func (f *fixture) run(repo *testRepo, opts fixtureOptions) (*Reconciler, *Result, error) {
	f.t.Helper()
	r, err := New(f.options(repo, opts))
	if err != nil {
		return nil, nil, err
	}
	result, err := r.RunProtected(context.Background())
	return r, result, err
}

// RunProtected is Run with the simulated kill caught. A real reconciler has no
// such thing — the process is simply gone — so this exists only where the test
// stands in for the operating system.
func (r *Reconciler) RunProtected(ctx context.Context) (result *Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cut, ok := recovered.(stopped)
			if !ok {
				panic(recovered)
			}
			result, err = nil, &killedAt{Event: cut.At}
		}
	}()
	return r.Run(ctx)
}

type killedAt struct{ Event Event }

func (k *killedAt) Error() string {
	return fmt.Sprintf("the reconciler was killed after %s/%s", k.Event.Tick, k.Event.Stage)
}

// killedAfter reports whether the run was cut at the stage the test asked for.
func killedAfter(t *testing.T, err error, tick, stage string) {
	t.Helper()
	cut, ok := err.(*killedAt)
	if !ok {
		t.Fatalf("the run ended with %v, not with the simulated kill after %s/%s", err, tick, stage)
	}
	if cut.Event.Tick != tick || cut.Event.Stage != stage {
		t.Fatalf("the run was cut after %s/%s, want %s/%s", cut.Event.Tick, cut.Event.Stage, tick, stage)
	}
}

// stopAt cuts the run the first time a tick reaches a stage.
func stopAt(tick, stage string) func(Event) bool {
	done := false
	return func(e Event) bool {
		if done || e.Tick != tick || e.Stage != stage {
			return false
		}
		done = true
		return true
	}
}
