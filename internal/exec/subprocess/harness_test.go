package subprocess

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The test harness: a real repository, a real origin, a real supervisor
// process and a fake runner.
//
// Nothing here is mocked that the executor actually talks to. git is git, the
// supervisor is the shipped binary, and the only thing replaced is the agent —
// because the agent is the one component whose behaviour these tests are not
// about.

// executorBin is the binary under test, built once. The supervisor is this
// same executable re-invoked, so a test that did not build it would be testing
// an executor whose supervisor is the go test binary.
var executorBin string

func TestMain(m *testing.M) {
	root, err := contracts.RepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "locate the module root: %v\n", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "ticfac-exec-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	executorBin = filepath.Join(dir, "ticfac-exec-subprocess")
	build := exec.Command("go", "build", "-o", executorBin, "./cmd/ticfac-exec-subprocess")
	build.Dir = root
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build the executor under test: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type testRepo struct {
	t      *testing.T
	Dir    string
	Origin string
	Base   string

	// Root is the raw t.TempDir() this repository and its origin live under.
	// stopEverything removes it itself, ahead of t.TempDir()'s own cleanup for
	// the same path — see the comment there for why that matters.
	Root string
}

// newRepo makes a repository with one commit and a bare origin to push to.
func newRepo(t *testing.T, name string) *testRepo {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	origin := filepath.Join(root, name+"-origin.git")

	mustRun(t, root, "git", "init", "--quiet", "--bare", "-b", "main", origin)
	mustRun(t, root, "git", "init", "--quiet", "-b", "main", dir)
	mustRun(t, dir, "git", "config", "user.email", "executor@example.com")
	mustRun(t, dir, "git", "config", "user.name", "ticfac test")
	mustRun(t, dir, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "base")
	mustRun(t, dir, "git", "remote", "add", "origin", origin)
	mustRun(t, dir, "git", "push", "--quiet", "origin", "main")

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	return &testRepo{t: t, Dir: resolved, Origin: origin, Base: base, Root: root}
}

func mustRun(t *testing.T, dir string, name string, args ...string) string {
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

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(mustRun(t, dir, "git", args...))
}

// fixture is one executor, its repository and the attempts it started, with
// every process it left behind stopped when the test ends.
type fixture struct {
	t        *testing.T
	Repo     *testRepo
	StateDir string
	// StateRoot is the raw t.TempDir() StateDir was carved from, and is only
	// set when this fixture allocated it itself (opts.stateDir == ""). Two
	// fixtures sharing one caller-supplied state root (the two-repos-same-tick
	// test) must not have either one delete it out from under the other.
	StateRoot string
	Executor  *Executor
	handles   []*JobHandle
}

type fixtureOptions struct {
	mode         string
	status       string
	sleep        string
	runnerArgv   []string
	pushInterval time.Duration
	attempt      int
	guardsOff    map[string]bool
	noRemote     bool
	name         string
	stateDir     string
	model        string
	rolePrompt   string
	writeFile    func(path string, data []byte, perm fs.FileMode) error
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	if opts.name == "" {
		opts.name = "repo"
	}
	repo := newRepo(t, opts.name)
	state := opts.stateDir
	stateRoot := ""
	if state == "" {
		stateRoot = t.TempDir()
		state = filepath.Join(stateRoot, "state")
	}

	argv := opts.runnerArgv
	if len(argv) == 0 {
		argv = fakeRunnerArgv(t, opts)
	}
	remote := "origin"
	if opts.noRemote {
		remote = "no-such-remote"
	}
	interval := opts.pushInterval
	if interval == 0 {
		interval = time.Second
	}
	executor, err := New(Options{
		Repo:           repo.Dir,
		StateDir:       state,
		Runner:         "claude",
		RunnerArgv:     argv,
		Model:          opts.model,
		RolePrompt:     opts.rolePrompt,
		SupervisorArgv: []string{executorBin, "supervise"},
		Remote:         remote,
		Attempt:        opts.attempt,
		PushInterval:   interval,
		guardsOff:      opts.guardsOff,
		writeFile:      opts.writeFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, Repo: repo, StateDir: state, StateRoot: stateRoot, Executor: executor}
	t.Cleanup(f.stopEverything)
	return f
}

// fakeRunnerArgv wraps the fake runner in an `env` so that the mode travels
// with the argv rather than through the test process's environment, which two
// parallel tests would share.
func fakeRunnerArgv(t *testing.T, opts fixtureOptions) []string {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "internal", "exec", "subprocess", "testdata", "fake-runner.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the fake runner is missing: %v", err)
	}
	argv := []string{"/usr/bin/env"}
	if opts.mode != "" {
		argv = append(argv, "FAKE_RUNNER_MODE="+opts.mode)
	}
	if opts.status != "" {
		argv = append(argv, "FAKE_RUNNER_STATUS="+opts.status)
	}
	if opts.sleep != "" {
		argv = append(argv, "FAKE_RUNNER_SLEEP="+opts.sleep)
	}
	return append(argv, "/bin/sh", script, promptPlaceholder)
}

// Start starts one attempt and remembers it, so the fixture can stop it.
func (f *fixture) Start(spec *JobSpec) *JobHandle {
	f.t.Helper()
	handle, err := f.Executor.Start(spec)
	if err != nil {
		f.t.Fatalf("start %s: %v", spec.JobID, err)
	}
	return f.track(handle)
}

// track remembers a handle this fixture is responsible for stopping. Every
// caller of Executor.Start — through the Start wrapper above, or directly
// where a test needs the raw error a guard-off redispatch returns — must run
// its handle through this, or stopEverything never learns the process exists.
func (f *fixture) track(handle *JobHandle) *JobHandle {
	f.handles = append(f.handles, handle)
	return handle
}

// runnerStartedPID pulls the runner's pid out of the ObsStarted detail
// supervisor.go writes: "%s runner, pid %d, worktree %s".
var runnerStartedPID = regexp.MustCompile(`runner, pid (\d+)`)

// trackedPIDs is every process this fixture launched for one handle: the
// supervisor pid the handle itself carries — frozen at the moment Start
// returned it, so a LATER Start over the SAME attempt (a guard-off
// redispatch) cannot make an earlier handle forget its own supervisor by
// overwriting the pid file a fresh store read would otherwise see — plus
// every runner pid the attempt's observation log has ever recorded. The log
// is read rather than the current runner.pid file for the same reason: a
// second supervisor's runner overwrites the first's pid FILE, and the log is
// append-only, so it is the only place the first runner's pid still exists.
func (f *fixture) trackedPIDs(handle *JobHandle) []int {
	local, err := handle.Local()
	if err != nil {
		return nil
	}
	pids := []int{local.PID}
	st := newStore(local.State)
	observations, _ := st.observationsFrom("")
	for _, obs := range observations {
		m := runnerStartedPID.FindStringSubmatch(obs.Detail)
		if m == nil {
			continue
		}
		if pid, err := strconv.Atoi(m[1]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// stopEverything kills every process this fixture ever tracked, and WAITS for
// each to actually exit, before removing the temp directories it allocated
// itself. It is registered with t.Cleanup, which runs cleanups in LIFO order:
// later registrations run first, and newRepo's and newFixture's t.TempDir()
// calls both happen before this is registered in newFixture, so this always
// runs before either of THIS fixture's own TempDir cleanups — a second
// fixture constructed afterward in the same test nests the same way around
// its own pair, never interleaved with this one's.
//
// A kill signal sent and not waited for is not the same fact as the process
// being gone: a supervisor still flushing a write when TempDir's RemoveAll
// starts is "directory not empty", not a passing test. And even a confirmed
// gone process does not guarantee every fd or mapping into these directories
// released in the same instant kill() returned, so the removal below retries
// rather than leaving that race to t.TempDir()'s own single attempt.
func (f *fixture) stopEverything() {
	seen := map[int]bool{}
	var pids []int
	for _, handle := range f.handles {
		for _, pid := range f.trackedPIDs(handle) {
			if pid > 0 && !seen[pid] {
				seen[pid] = true
				pids = append(pids, pid)
			}
		}
	}
	for _, pid := range pids {
		if processAlive(pid) {
			_ = signalGroup(pid, sigKill())
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	removeWithRetry(f.StateRoot)
	removeWithRetry(f.Repo.Root)
}

// removeWithRetry removes a directory this fixture allocated with
// t.TempDir(), ahead of that same t.TempDir()'s own later cleanup, retrying
// "directory not empty" for a bounded time rather than assuming a killed
// process has released everything it touched in that directory the instant
// it stops existing.
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

// spec builds a JobSpec for one tick. It is the SPEC §4.3 illustration's
// shape, with this repository's real base and a write_ref in the branch
// namespace ticks uses for a tick's work.
func (f *fixture) spec(jobID, tick string) *JobSpec {
	return &JobSpec{
		SchemaVersion: SchemaVersion,
		JobID:         jobID,
		Role:          "implement-tick",
		Source: Source{
			Repository: f.Repo.Origin,
			BaseSHA:    f.Repo.Base,
			WriteRef:   "refs/heads/tick/" + tick,
		},
		Capabilities:   Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []Input{{Kind: "tick", ID: tick}},
		OutputSchema:   "ticfac.job-result.implement-tick.v1",
		ArtifactPrefix: "runs/" + jobID + "/",
		Credentials: Credentials{
			Model:  ModelCredential{Shorthand: "issued-by-host"},
			Source: SourceCredential{Shorthand: "write"},
		},
		Limits: Limits{WallSeconds: 60},
	}
}

func (f *fixture) store(handle *JobHandle) *store {
	f.t.Helper()
	local, err := handle.Local()
	if err != nil {
		f.t.Fatal(err)
	}
	return newStore(local.State)
}

// waitFor polls a condition. Every wait in these tests is over a REAL process,
// so the alternative to polling is a sleep long enough to be flaky in the
// other direction.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// waitSettled waits for the supervisor to record that the runner exited. It
// deliberately does NOT inspect while it waits: several of the required tests
// are about an attempt that settles while nobody is looking.
func (f *fixture) waitSettled(handle *JobHandle) {
	f.t.Helper()
	st := f.store(handle)
	waitFor(f.t, "the attempt to settle", 30*time.Second, st.settled)
}

func (f *fixture) inspect(handle *JobHandle) *JobStatus {
	f.t.Helper()
	status, err := f.Executor.Inspect(handle, "")
	if err != nil {
		f.t.Fatalf("inspect: %v", err)
	}
	return status
}

func (f *fixture) collect(handle *JobHandle) *Collection {
	f.t.Helper()
	collected, err := f.Executor.CollectDetail(handle)
	if err != nil {
		f.t.Fatalf("collect: %v", err)
	}
	return collected
}
