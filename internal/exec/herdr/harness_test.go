package herdr

// The test harness: a real repository, a bare origin, and the canonical fake
// herdr server (internal/herd/herdtest) on a real unix socket, speaking the
// real wire protocol through the production client. The only thing faked is
// herdr's side of it: the routes below do what herdr does — create a REAL git
// worktree for worktree.create, remove it for worktree.remove, resolve agent
// status from the files the fake agent writes — so the durable evidence the
// executor collects is real, and the only replaced component is the agent
// itself, exactly as the local executor's harness replaces the runner.

import (
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
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
)

// testRepo is a repository with one commit and a bare origin to push to.
type testRepo struct {
	t      *testing.T
	Dir    string
	Origin string
	Base   string
	Root   string
}

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

// writeTickRecord puts one real tick record in the repository, the way the
// ticks tracker would hold it: the unit of work the worker prompt points at.
func writeTickRecord(t *testing.T, repo *testRepo, tickID, description string) {
	t.Helper()
	dir := filepath.Join(repo.Dir, ".tick", "issues")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"id": tickID, "title": "harness tick", "description": description,
		"status": "open", "type": "task",
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tickID+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo.Dir, "git", "add", ".tick")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "tick record "+tickID)
}

// harness is one herdr executor against one fake herdr that does herdr's real
// side effects on a real repository.
type harness struct {
	t      *testing.T
	repo   *testRepo
	server *herdtest.Server
	ex     *Executor
	state  string // the per-attempt scratch: prompt, status, interrupt files

	mu         sync.Mutex
	workspaces map[string]string // workspace id -> worktree path
	removed    []string          // workspace ids worktree.remove accepted
	last       *subprocess.JobHandle
	spawn      bool   // agent.start launches the fake agent process
	mode       string // the fake agent's mode
	agentCmd   *exec.Cmd

	promptFile    string
	statusFile    string
	interruptFile string
}

type harnessOptions struct {
	spawnAgent bool
	agentMode  string
	kind       string
	args       []string
	// serverVersion and serverProtocol, when set, are what the fake's ping
	// handshake advertises; zero keeps the canonical 0.8.2 / protocol 20
	// reply. A non-default pair also answers WITHOUT a capabilities block,
	// which is how a test proves an operation demands no capability of the
	// server — and a pair below the stop surface's floor is how a test
	// dispatches against an older herdr.
	serverProtocol int
	serverVersion  string
}

// newHarness wires the executor to a fake herdr whose worktree.create and
// worktree.remove really do it, whose agent resolution is driven by the
// status file the fake agent (or the test) writes.
func newHarness(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	repo := newRepo(t, "repo")
	root := t.TempDir()
	state := filepath.Join(root, "agent")

	h := &harness{
		t: t, repo: repo, state: state, workspaces: map[string]string{},
		spawn: opts.spawnAgent, mode: opts.agentMode,
		promptFile:    filepath.Join(state, "prompt.txt"),
		statusFile:    filepath.Join(state, "status"),
		interruptFile: filepath.Join(state, "interrupt"),
	}
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	// The tick the prompt points at, in the base the attempt branches from.
	writeTickRecord(t, repo, "t1",
		"Create the file hello.txt containing exactly the word hello, and commit it.")
	repo.Base = strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))

	s := herdtest.New(t, herdtest.Config{
		Version:  opts.serverVersion,
		Protocol: opts.serverProtocol,
	})

	// worktree.create: herdr creates the worktree AND opens a workspace.
	s.Route(herdtest.MethodWorktreeCreate, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Cwd    string  `json:"cwd"`
			Branch *string `json:"branch"`
			Base   *string `json:"base"`
			Label  *string `json:"label"`
			Focus  bool    `json:"focus"`
		}
		_ = json.Unmarshal(req.Params, &p)
		branch := "tick/unknown"
		if p.Branch != nil {
			branch = *p.Branch
		}
		base := "HEAD"
		if p.Base != nil {
			base = *p.Base
		}
		path := filepath.Join(root, "worktrees", strings.ReplaceAll(branch, "/", "-"))
		if out, err := runGitErr(repo.Dir, "worktree", "add", "--quiet", "-b", branch, path, base); err != nil {
			return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "worktree.create: "+out)
		}
		h.mu.Lock()
		ws := fmt.Sprintf("w%d", len(h.workspaces)+len(h.removed)+1)
		h.workspaces[ws] = path
		h.mu.Unlock()
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "worktree_created",
			"workspace": map[string]any{
				"workspace_id": ws, "label": strings.TrimPrefix(branch, "tick/"), "agent_status": "unknown",
			},
			"tab":       map[string]any{"tab_id": ws + ":t1", "workspace_id": ws, "agent_status": "unknown"},
			"root_pane": map[string]any{"pane_id": ws + ":p1", "workspace_id": ws, "tab_id": ws + ":t1", "agent_status": "unknown"},
			"worktree":  map[string]any{"path": path, "label": branch, "branch": branch},
		})
	})

	// worktree.remove: herdr closes the workspace and removes the worktree —
	// WITHOUT Force, exactly as the real one refuses a dirty worktree.
	s.Route(herdtest.MethodWorktreeRemove, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			WorkspaceID string `json:"workspace_id"`
			Force       bool   `json:"force"`
		}
		_ = json.Unmarshal(req.Params, &p)
		h.mu.Lock()
		path, ok := h.workspaces[p.WorkspaceID]
		delete(h.workspaces, p.WorkspaceID)
		h.mu.Unlock()
		if !ok {
			return herdtest.RespondErr(w, req.ID, herdtest.CodeWorkspaceNotFound, "workspace "+p.WorkspaceID+" not found")
		}
		args := []string{"worktree", "remove"}
		if p.Force {
			args = append(args, "--force")
		}
		if out, err := runGitErr(repo.Dir, append(args, path)...); err != nil {
			return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "worktree.remove: "+out)
		}
		runGitErr(repo.Dir, "worktree", "prune")
		h.mu.Lock()
		h.removed = append(h.removed, p.WorkspaceID)
		h.mu.Unlock()
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "worktree_removed", "workspace_id": p.WorkspaceID, "path": path, "forced": p.Force,
		})
	})

	// agent.start: launch the fake agent process when the harness is told to,
	// and report the agent ready either way.
	s.Route(herdtest.MethodAgentStart, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Name   string   `json:"name"`
			Kind   string   `json:"kind"`
			PaneID string   `json:"pane_id"`
			Args   []string `json:"args"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if h.spawn {
			root, err := contracts.RepoRoot()
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(root, "internal", "exec", "herdr", "testdata", "fake-agent.sh")
			cmd := exec.Command("/bin/sh", script, worktreeOf(h, req), h.promptFile, h.statusFile, h.interruptFile, h.mode)
			cmd.Dir = worktreeOf(h, req)
			logFile, err := os.OpenFile(filepath.Join(h.state, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "agent.start: "+err.Error())
			}
			cmd.Stdout, cmd.Stderr = logFile, logFile
			if err := cmd.Start(); err != nil {
				return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "agent.start: "+err.Error())
			}
			h.mu.Lock()
			h.agentCmd = cmd
			h.mu.Unlock()
			go func() { _ = cmd.Wait() }()
		}
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_started",
			"agent": map[string]any{
				"pane_id": p.PaneID, "agent_status": "idle",
				"name": p.Name, "interactive_ready": true, "agent_session": nil,
			},
			"argv": append([]string{p.Kind}, p.Args...),
		})
	})

	// agent.prompt: herdr delivers the text to the pane.
	s.Route(herdtest.MethodAgentPrompt, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Target string `json:"target"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if err := os.WriteFile(h.promptFile, []byte(p.Text), 0o644); err != nil {
			return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "agent.prompt: "+err.Error())
		}
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_prompted",
			"agent": map[string]any{
				"pane_id": "w1:p1", "agent_status": h.currentStatus(),
				"name": p.Target, "interactive_ready": true,
				"agent_session": herdtest.AgentSessionJSON("sess-h"),
			},
		})
	})

	// agent.get / agent.wait: the agent's status, from the file the fake
	// agent writes (or the test seeds).
	agentInfo := func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_info",
			"agent": map[string]any{
				"pane_id": "w1:p1", "agent_status": h.currentStatus(),
				"name": p.Target, "interactive_ready": true, "agent_session": nil,
			},
		})
	}
	s.Route(herdtest.MethodAgentGet, agentInfo)
	s.Route(herdtest.MethodAgentWait, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Target    string   `json:"target"`
			Until     []string `json:"until"`
			TimeoutMs *uint64  `json:"timeout_ms"`
		}
		_ = json.Unmarshal(req.Params, &p)
		// herdr's wait is server-owned and event-driven; the fake polls the
		// status file, which is the same fact herdr's event stream carries.
		budget := 5 * time.Second
		if p.TimeoutMs != nil {
			budget = time.Duration(*p.TimeoutMs) * time.Millisecond
		}
		deadline := time.Now().Add(budget)
		for {
			status := h.currentStatus()
			for _, want := range p.Until {
				if want == status {
					return agentInfo(t, req, w)
				}
			}
			if !time.Now().Before(deadline) {
				return herdtest.RespondErr(w, req.ID, "timeout", "agent did not reach "+strings.Join(p.Until, ","))
			}
			time.Sleep(25 * time.Millisecond)
		}
	})

	// agent.send_keys: the interrupt lands as a file the fake agent polls.
	s.Route(herdtest.MethodAgentSendKeys, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Target string   `json:"target"`
			Keys   []string `json:"keys"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if err := os.WriteFile(h.interruptFile, []byte(strings.Join(p.Keys, " ")), 0o644); err != nil {
			return herdtest.RespondErr(w, req.ID, herdtest.CodeInvalidRequest, "agent.send_keys: "+err.Error())
		}
		return s.Builtin(herdtest.MethodAgentSendKeys)(t, req, w)
	})

	h.server = s
	stateDir := filepath.Join(root, "state")
	ex, err := New(Options{
		Repo:       repo.Dir,
		StateDir:   stateDir,
		SocketPath: s.Path(),
		Kind:       opts.kind,
		Args:       opts.args,
		Remote:     "origin",
		Attempt:    1,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The harness's patience budgets are short: the pane-busy retry and the
	// readiness poll must be exercised without a 60-second default behind
	// them, and the confirm wait — which a dispatch with no visible work
	// burns in full — is a second, ample for the fake agent to reach
	// `working` and short enough not to stall the suite.
	ex.opts.StartupTimeout = 3 * time.Second
	ex.opts.ConfirmTimeout = 1 * time.Second
	h.ex = ex
	return h
}

// worktreeOf resolves the worktree the agent.start pane belongs to. The
// harness has one workspace per start, so the LAST one is the agent's.
func worktreeOf(h *harness, req herdtest.Request) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := ""
	for _, p := range h.workspaces {
		path = p
	}
	return path
}

// removals lists the workspace ids worktree.remove accepted, in order — the
// harness's own record, because the route is a test-local replacement.
func (h *harness) removals() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.removed...)
}

// currentStatus is the fake agent's status, idle before it ever reported.
func (h *harness) currentStatus() string {
	raw, err := os.ReadFile(h.statusFile)
	if err != nil {
		return "idle"
	}
	status := strings.TrimSpace(string(raw))
	if status == "" {
		return "idle"
	}
	return status
}

// setStatus seeds the fake agent's status, for tests that drive the agent's
// lifecycle without a process behind it.
func (h *harness) setStatus(status string) {
	h.t.Helper()
	if err := os.WriteFile(h.statusFile, []byte(status), 0o644); err != nil {
		h.t.Fatal(err)
	}
}

// spec builds a JobSpec for one tick, the shape the reconciler's jobSpec
// builds: the tick's branch in the ticfac namespace, the tick as input.
func (h *harness) spec(jobID, tick string) *subprocess.JobSpec {
	return &subprocess.JobSpec{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         jobID,
		Role:          "implement-tick",
		Source: subprocess.Source{
			Repository: h.repo.Origin,
			BaseSHA:    h.repo.Base,
			WriteRef:   "refs/heads/ticfac/" + jobID,
		},
		Capabilities:   subprocess.Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []subprocess.Input{{Kind: "tick", ID: tick}, {Kind: "epic", ID: "harness"}},
		OutputSchema:   "ticfac.job-result.implement-tick.v1",
		ArtifactPrefix: "runs/" + jobID + "/",
		Credentials: subprocess.Credentials{
			Model: subprocess.ModelCredential{Shorthand: "issued-by-host"},
			Source: subprocess.SourceCredential{Grant: &subprocess.SourceGrant{
				Issuer: "host", Grade: "write", WriteRefPrefix: "refs/heads/ticfac/",
			}},
		},
		Limits: subprocess.Limits{WallSeconds: 300},
	}
}

// start runs one Start for a spec built the way the reconciler builds one,
// under the harness's attempt number.
func (h *harness) start(tick string) (*subprocess.JobHandle, error) {
	handle, err := h.ex.Start(h.spec(fmt.Sprintf("run-harness/tick-%s/attempt-%d", tick, h.ex.opts.Attempt), tick))
	if err == nil {
		h.last = handle
	}
	return handle, err
}

// waitForOr polls a condition and reports whether it was reached, so a
// caller can dump its own diagnostics before failing.
func waitForOr(t *testing.T, what string, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// dumpAgent prints the fake agent's log and status, for diagnosing a run the
// harness cannot explain.
func (h *harness) dumpAgent(t *testing.T) {
	t.Helper()
	if raw, err := os.ReadFile(filepath.Join(h.state, "agent.log")); err == nil {
		t.Logf("agent.log:\n%s", raw)
	}
	t.Logf("agent status: %q, prompt delivered: %t", h.currentStatus(), fileExists(h.promptFile))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runGitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
