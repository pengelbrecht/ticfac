package subprocess

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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
	return &testRepo{t: t, Dir: resolved, Origin: origin, Base: base}
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
	Executor *Executor
	handles  []*JobHandle
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
	if state == "" {
		state = filepath.Join(t.TempDir(), "state")
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
	f := &fixture{t: t, Repo: repo, StateDir: state, Executor: executor}
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
	f.handles = append(f.handles, handle)
	return handle
}

// stopEverything kills every process a handle in this fixture named, and WAITS
// for each to actually exit before returning. It is registered with
// t.Cleanup, which runs cleanups in LIFO order, so it runs before the
// t.TempDir() cleanups registered earlier in newRepo/newFixture — but a kill
// signal sent and not waited for is not the same fact as the process being
// gone: a supervisor still flushing a write when TempDir's RemoveAll starts
// is "directory not empty", not a passing test. Waiting here is what makes
// the ordering promise real rather than merely likely.
func (f *fixture) stopEverything() {
	for _, handle := range f.handles {
		local, err := handle.Local()
		if err != nil {
			continue
		}
		st := newStore(local.State)
		pids := []int{st.runnerPID(), st.supervisorPID()}
		for _, pid := range pids {
			if pid > 0 && processAlive(pid) {
				_ = signalGroup(pid, sigKill())
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for _, pid := range pids {
			for pid > 0 && processAlive(pid) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
		}
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
