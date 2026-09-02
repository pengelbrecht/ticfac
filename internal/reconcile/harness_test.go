package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
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
type fakeTracker struct {
	path string
	mu   sync.Mutex

	// calls counts what was asked of the tracker, so "the tick was closed
	// twice" is a number and not an impression.
	calls map[string]int
}

type trackerState struct {
	Epic  string             `json:"epic"`
	Waves [][]string         `json:"waves"`
	Ticks map[string]tk.Tick `json:"ticks"`
	Roles map[string]string  `json:"roles"`
	Order []string           `json:"order"`
}

func newTracker(t *testing.T, dir string) *fakeTracker {
	t.Helper()
	tracker := &fakeTracker{path: filepath.Join(dir, "tracker.json"), calls: map[string]int{}}
	state := trackerState{
		Epic:  "qeu",
		Waves: [][]string{{"a1", "a2"}, {"b1"}, {"rv", "co"}},
		Ticks: map[string]tk.Tick{},
		Roles: map[string]string{"rv": "review", "co": "closeout"},
		Order: []string{"a1", "a2", "b1", "rv", "co"},
	}
	for _, id := range state.Order {
		state.Ticks[id] = tk.Tick{ID: id, Title: "tick " + id, Status: "open", Type: "task", Parent: "qeu"}
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

func (f *fakeTracker) Graph(_ context.Context, epicID string) (tk.Graph, error) {
	f.tally("graph")
	state, err := f.load()
	if err != nil {
		return tk.Graph{}, err
	}
	graph := tk.Graph{Epic: tk.GraphEpic{ID: epicID, Title: "the fixture epic"}}
	for i, wave := range state.Waves {
		w := tk.GraphWave{Wave: i + 1, Parallel: len(wave), Ready: i == 0}
		for _, id := range wave {
			tick := state.Ticks[id]
			w.Tasks = append(w.Tasks, tk.GraphTask{
				ID: id, Title: tick.Title, Status: tick.Status, Priority: 2,
				Role: state.Roles[id], AgentReady: tick.Status != "closed",
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
	return f.mutate(tickID, func(tick *tk.Tick) {
		tick.Status, tick.Owner = "in_progress", owner
	})
}

func (f *fakeTracker) Note(_ context.Context, tickID, text string) (tk.Tick, error) {
	f.tally("note:" + tickID)
	return f.mutate(tickID, func(tick *tk.Tick) {
		tick.Notes = strings.TrimSpace(tick.Notes + "\n" + text)
	})
}

func (f *fakeTracker) Close(_ context.Context, tickID string) (tk.Tick, error) {
	f.tally("close:" + tickID)
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
	return tick, f.save(state)
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
	mustRun(t, root, "git", "init", "--quiet", "-b", "main", dir)
	configure(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "README.md"), "# "+name+"\n")
	write(t, filepath.Join(dir, ".tick", "runners.toml"), gate)
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
const passingGate = `version = 2

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`

// failingGate refuses everything, so that a run's close can be shown to depend
// on it.
const failingGate = `version = 2

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
	return Options{
		Repo:          repo.Dir,
		Remote:        "origin",
		EpicID:        "qeu",
		RunID:         "r-fixture",
		BaseRef:       "HEAD",
		Owner:         "ticfac-test",
		Tracker:       f.Tracker,
		ExecStateRoot: f.StateRoot,
		GateConfig:    filepath.Join(repo.Dir, ".tick", "runners.toml"),
		GateTimeout:   2 * time.Minute,
		PollInterval:  20 * time.Millisecond,
		WipeThreshold: 10 * time.Second,
		StepCap:       60 * time.Millisecond,
		WallSeconds:   120,
		BudgetUSD:     opts.budget,
		CeilingUSD:    opts.ceiling,
		Sleep:         func(time.Duration) { time.Sleep(5 * time.Millisecond) },
		guardsOff:     opts.guardsOff,
		stopAfter:     opts.stopAfter,
		NewExecutor:   f.newExecutor,
	}
}

func (f *fixture) newExecutor(d Dispatch) (Executor, error) {
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
		PushInterval:   time.Second,
	})
	if err != nil {
		return nil, err
	}
	var wrapped Executor = &recordingExecutor{fixture: f, Executor: executor}
	if f.wrap != nil {
		wrapped = f.wrap(wrapped)
	}
	return wrapped, nil
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
// Two half-measures are deliberately avoided. The runner is NOT in its
// supervisor's process group — the executor puts each in a group of its own —
// so killing supervisors alone leaves runners writing. And the pid FILES are
// not the whole population: a redispatch over a live attempt (the
// never_redispatch_live negative control) starts a second supervisor over the
// same state directory, which overwrites `runner.pid`, so the first runner
// exists only in the append-only observation log and on the handle the first
// Start returned.
func (f *fixture) stopEverything() {
	pids := f.leftoverPIDs()
	for _, pid := range pids {
		if processAlive(pid) {
			_ = syscallKillGroup(pid)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// leftoverPIDs is every process this fixture may still have running, from three
// sources that each catch what the others miss:
//
//   - the supervisor pid frozen on every handle Start returned, which a later
//     Start over the same attempt cannot overwrite;
//   - every `supervisor.pid` and `runner.pid` under this fixture's state root,
//     which catches an attempt whose handle the run never handed back — a run
//     cut mid-dispatch by the restart tests' simulated kill;
//   - every runner pid an attempt's observation log has EVER recorded, which is
//     the only place a runner survives its pid file being overwritten.
func (f *fixture) leftoverPIDs() []int {
	f.mu.Lock()
	handles := append([]*subprocess.JobHandle{}, f.handles...)
	f.mu.Unlock()

	seen := map[int]bool{}
	var pids []int
	add := func(pid int) {
		if pid > 0 && !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}

	for _, handle := range handles {
		if local, err := handle.Local(); err == nil {
			add(local.PID)
		}
	}
	_ = filepath.Walk(f.StateRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		switch filepath.Base(path) {
		case "supervisor.pid", "runner.pid":
			var pid int
			if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); scanErr == nil {
				add(pid)
			}
		case "observations.jsonl":
			for _, match := range runnerStartedPID.FindAllStringSubmatch(string(raw), -1) {
				if pid, convErr := strconv.Atoi(match[1]); convErr == nil {
					add(pid)
				}
			}
		}
		return nil
	})
	return pids
}

// runnerStartedPID pulls a runner's pid out of the `started` observation the
// supervisor writes: "%s runner, pid %d, worktree %s".
var runnerStartedPID = regexp.MustCompile(`runner, pid (\d+)`)

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
