package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The acceptance criterion for the eviction flush (tick ppt), stated as the
// tick states it: a container sent SIGTERM mid-tick pushes its committed work
// and exits well inside the grace window, proven by sending SIGTERM to a
// live run and showing the work on the remote afterwards.
//
// So the test is the whole thing, not a unit of it: a REAL run-epic in a real
// child process (this test binary, re-exec'd), a REAL attempt with real
// uncommitted work in its worktree, a REAL SIGTERM to the child's pid, and the
// assertions read the bare origin the way a replacement container would —
// from the remote, not from the dying disk. The one substitution is the
// tracker: no tk binary exists on CI's PATH, so the seam (newTracker) hands
// the run a one-tick fake and everything above it — the reconciler, the
// executor, the supervisor, the signal handler — is the production code the
// criterion is about.

// The child mode's environment: the marker, and the three paths the child
// needs. Everything else the child reads is the repository it is pointed at.
const (
	evacChildEnv    = "TICFAC_EVAC_CHILD"
	evacRepoEnv     = "TICFAC_EVAC_REPO"
	evacStateEnv    = "TICFAC_EVAC_STATE"
	evacRunnerEnv   = "TICFAC_EVAC_RUNNER"
	evacEpicID      = "evac"
	evacTickID      = "a1"
	evacRunID       = "r-sigterm"
	evacGraceWindow = 15 * time.Minute // the platform's: SIGTERM, then up to this, then SIGKILL
)

func TestSIGTERMToALiveRunEvacuatesItsWorkToTheRemote(t *testing.T) {
	// The child: a real run-epic that never returns from this test — it
	// leaves by the SIGTERM handler's os.Exit, which is the behaviour under
	// test, or by Run's own exit code if the run ends first.
	if os.Getenv(evacChildEnv) != "" {
		evacChild(t)
		return
	}
	if testing.Short() {
		t.Skip("builds the supervisor binary and runs a live run")
	}
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// The supervisor binary the reconciler's executor drives, built rather
	// than found: the child's run-epic refuses to start without one beside it
	// or on PATH, and the one on a developer's PATH belongs to a different
	// build than this tree.
	moduleRoot, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	build := exec.Command("go", "build", "-o",
		filepath.Join(bin, "ticfac-exec-subprocess"), "./cmd/ticfac-exec-subprocess")
	build.Dir = moduleRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ticfac-exec-subprocess: %v\n%s", err, out)
	}

	// The fixture: a repository with one tick's worth of gate, and a bare
	// origin that stands in for the remote a replacement container reads.
	origin := filepath.Join(root, "origin.git")
	mustGit(t, root, "init", "--quiet", "--bare", "-b", "main", origin)
	mustGit(t, origin, "config", "maintenance.auto", "false")
	checkout := filepath.Join(root, "repo")
	mustGit(t, root, "init", "--quiet", "-b", "main", checkout)
	mustGit(t, checkout, "config", "user.email", "evacuation@example.com")
	mustGit(t, checkout, "config", "user.name", "ticfac evacuation test")
	mustGit(t, checkout, "config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(checkout, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The runners.toml a gate needs, in the same shape the reconciler's own
	// harness uses: a roles cell for implement, and a tree command, because a
	// repository whose runners.toml declares no [testing.commands] is refused
	// at construction — there would be no gate to close behind.
	writeFile(t, filepath.Join(checkout, ".tick", "runners.toml"), `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "test -f README.md", description = "the merge carries the work" }
`)
	// The run-state contract's gitignore fragment, which every target
	// repository carries, so the run's exhaust under .ticfac/ is exhaust and
	// not a dirty tree.
	writeFile(t, filepath.Join(checkout, ".gitignore"), strings.Join(runstate.Fragment, "\n")+"\n")
	writeFile(t, filepath.Join(checkout, "README.md"), "# the evacuation fixture\n")
	mustGit(t, checkout, "add", "-A")
	mustGit(t, checkout, "commit", "--quiet", "-m", "base")
	mustGit(t, checkout, "remote", "add", "origin", origin)
	mustGit(t, checkout, "push", "--quiet", "origin", "main")

	// The runner: writes real work into its worktree, commits nothing, and
	// never finishes — a worker the container dies underneath. The only
	// process that can ever carry that work to the remote is the flush.
	stateRoot := filepath.Join(root, "state")
	runnerScript := filepath.Join(root, "fake-runner.sh")
	writeFile(t, runnerScript, `#!/bin/sh
set -u
printf 'uncommitted work of %s\n' "$TICFAC_TICK" > "$TICFAC_WORKTREE/wip-$TICFAC_TICK.txt"
exec sleep 86400
`)

	// The child: this test binary again, running only this test, in child
	// mode. Its PATH carries the supervisor binary, and its runner argv is
	// the fake runner — the one substitution the executor's own escape hatch
	// (subprocess.EnvRunnerArgv) exists for.
	var out bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run", "^TestSIGTERMToALiveRunEvacuatesItsWorkToTheRemote$", "-test.timeout=10m")
	cmd.Env = append(os.Environ(),
		evacChildEnv+"=1",
		evacRepoEnv+"="+checkout,
		evacStateEnv+"="+stateRoot,
		evacRunnerEnv+"="+runnerScript,
		subprocess.EnvRunnerArgv+"="+strings.Join([]string{"/bin/sh", runnerScript, "{{prompt}}"}, "\n"),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	fail := func(format string, args ...any) {
		cmd.Process.Kill()
		args = append(args, out.String())
		t.Fatalf(format+"\nthe child's output:\n%s", args...)
	}

	// The wait is for the WORK, not the dispatch: the attempt record exists
	// before the runner starts, and a SIGTERM that landed between the two
	// would prove nothing about the work the flush exists to save.
	inFlight := func() bool { return evacFileUnder(stateRoot, "wip-"+evacTickID+".txt") }
	deadline := time.Now().Add(90 * time.Second)
	for !inFlight() {
		if !time.Now().Before(deadline) {
			fail("the child never reached an in-flight attempt with work in its worktree")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The eviction: SIGTERM to the main process, the way the platform sends
	// it. What follows is what the criterion names — the work on the remote,
	// and an exit well inside the window.
	signalled := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		fail("could not send SIGTERM to the live run: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-exited:
	case <-time.After(evacGraceWindow):
		fail("the run did not exit inside the platform's %s grace window", evacGraceWindow)
	}
	spent := time.Since(signalled)
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok {
		fail("waiting for the child: %v", waitErr)
	}
	if exitErr.ExitCode() != 143 {
		t.Errorf("the run exited %d, want 143 — the shell's spelling of a SIGTERM stop", exitErr.ExitCode())
	}
	// Well inside the grace window: the flush's bound is thirty seconds and
	// the exit it precedes must not sit anywhere near the SIGKILL behind it.
	if spent > 30*time.Second {
		t.Errorf("the run spent %s between the signal and its exit, past the whole flush budget", spent)
	}

	// Nothing of the child survives the assertions: its supervisor and runner
	// were orphans from the moment it exited, and a fixture that left them
	// running would hand the next test a process holding its directories.
	evacKillAttemptProcesses(t, stateRoot)

	// The work, as the remote holds it: the attempt's write ref carries the
	// flush's snapshot — the commit no timer ever made — and the snapshot
	// carries the uncommitted work itself.
	ref := "refs/heads/ticfac/run-" + evacRunID + "/tick-" + evacTickID + "/attempt-1"
	sha := strings.TrimSpace(gitOut(t, origin, "rev-parse", "--verify", ref))
	if sha == "" {
		t.Fatalf("origin does not hold %s: the flush never pushed the in-flight work\n%s", ref, out.String())
	}
	message := gitOut(t, origin, "log", "--format=%s", "-n", "1", sha)
	if !strings.Contains(message, "evacuation snapshot") {
		t.Errorf("the commit origin holds is %q, not the flush's snapshot of uncommitted work", message)
	}
	wip := gitOut(t, origin, "show", sha+":wip-"+evacTickID+".txt")
	if !strings.Contains(wip, "uncommitted work of "+evacTickID) {
		t.Errorf("the pushed snapshot does not carry the uncommitted work: %q", wip)
	}

	// The checkpoint, as the remote holds it: the run stopped, on this
	// signal, resumable — the sentence a replacement container reads first.
	checkpoint := gitOut(t, origin, "show", "epic/"+evacEpicID+":.ticfac/runs/"+evacRunID+"/checkpoint.json")
	if !strings.Contains(checkpoint, "terminated") {
		t.Errorf("the remote's checkpoint does not name the signal:\n%s", checkpoint)
	}
	if !strings.Contains(checkpoint, `"state": "running"`) {
		t.Errorf("the remote's checkpoint is not a resumable stop:\n%s", checkpoint)
	}
}

// evacChild is the child half: a real run-epic against the fixture the parent
// built, with the tracker seam answered by a one-tick fake. It never returns
// normally — the SIGTERM handler's os.Exit is the path under test — and
// leaves through Run's own exit code if the run ends some other way.
func evacChild(t *testing.T) {
	newTracker = func(string) (reconcile.Tracker, error) {
		return &evacTracker{tick: tk.Tick{
			ID: evacTickID, Title: "the evacuation fixture tick", Status: "open",
			Type: "task", Parent: evacEpicID, Priority: 2,
		}}, nil
	}
	code := Run([]string{
		"run-epic",
		"--repo", os.Getenv(evacRepoEnv),
		"--remote", "origin",
		"--run-id", evacRunID,
		"--state-root", os.Getenv(evacStateEnv),
		"--runner", "claude",
		evacEpicID,
	}, os.Stdout, os.Stderr)
	os.Exit(code)
}

// evacTracker is the one-tick tracker the child runs against: the same five
// answers the reconciler's own harness fake gives, in memory, because the
// child has exactly one tick and one claim and the criterion is not about the
// tracker. In is what the reconciler's relocation asks for — a tracker that
// can be pointed at the tracker worktree — and this one is already pointed
// everywhere.
type evacTracker struct {
	mu   sync.Mutex
	tick tk.Tick
}

func (f *evacTracker) In(string) reconcile.Tracker { return f }

func (f *evacTracker) Graph(_ context.Context, epicID string) (tk.Graph, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return tk.Graph{
		Epic: tk.GraphEpic{ID: epicID, Title: "the evacuation fixture epic"},
		Waves: []tk.GraphWave{{Wave: 1, Parallel: 1, Ready: true, Tasks: []tk.GraphTask{{
			ID: f.tick.ID, Title: f.tick.Title, Status: f.tick.Status, Priority: f.tick.Priority,
			Type: f.tick.Type, AgentReady: f.tick.Status != "closed",
		}}}},
		Stats: tk.GraphStats{TotalTasks: 1, WaveCount: 1},
	}, nil
}

func (f *evacTracker) Show(_ context.Context, _ string) (tk.Tick, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tick, nil
}

func (f *evacTracker) Claim(_ context.Context, _, owner string) (tk.Tick, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick.Status, f.tick.Owner = "in_progress", owner
	return f.tick, nil
}

func (f *evacTracker) Note(_ context.Context, _ string, _ string) (tk.Tick, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tick, nil
}

func (f *evacTracker) Close(_ context.Context, _ string) (tk.Tick, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick.Status, f.tick.ClosedReason = "closed", "closed by ticfac"
	return f.tick, nil
}

// evacFileUnder says whether a file with this name exists anywhere under root.
func evacFileUnder(root, name string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() && entry.Name() == name {
			found = true
		}
		return nil
	})
	return found
}

// evacKillAttemptProcesses stops what the child's attempts left behind, by the
// locks that prove they are its — the same rule the reconciler's own teardown
// follows, for the same reason: on a busy host a saved pid is soon somebody
// else's.
func evacKillAttemptProcesses(t *testing.T, stateRoot string) {
	t.Helper()
	_ = filepath.WalkDir(stateRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "attempt.json" {
			return nil
		}
		if _, err := subprocess.KillLiveProcesses(filepath.Dir(path), 5*time.Second); err != nil {
			t.Logf("stopping what %s left behind: %v", filepath.Dir(path), err)
		}
		return nil
	})
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitOut runs one git command and returns its combined output, failing the
// test on error. Origin is bare, so every assertion runs with --git-dir
// semantics for free: no checkout of the dying container is ever read.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}
