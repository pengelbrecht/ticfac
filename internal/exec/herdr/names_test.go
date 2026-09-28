package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// Agent names are herdr's to police, and herdr refuses a second agent under a
// name another pane holds (agent_name_taken). The harness's default agent
// routes answer any name for any pane, which is how the stall of epic-6in got
// past the suite: the repair job of 4i8 attempt 3 was launched under the
// implement attempt's own name while that attempt's pane was still there, and
// the run halted on a gate failure it had a repair for.
//
// namedAgents re-routes the fake's agent surface onto a registry that holds
// herdr's rule: one agent per name, and agent.start over a name another pane
// holds is agent_name_taken, naming the holder.

type namedAgent struct {
	pane, workspace, status string
}

type namedAgents struct {
	h *harness

	mu     sync.Mutex
	agents map[string]*namedAgent // by name
	starts []string               // the names agent.start was asked for, in order
	closes []string               // the panes pane.close took, in order
	// startHook, when set, answers an agent.start before the registry does:
	// a non-empty code is returned as that error.
	startHook func(name, pane string, n int) (code, msg string)
}

func withNamedAgents(h *harness) *namedAgents {
	na := &namedAgents{h: h, agents: map[string]*namedAgent{}}
	s := h.server
	s.Route(herdtest.MethodAgentStart, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Name   string `json:"name"`
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(req.Params, &p)
		na.mu.Lock()
		na.starts = append(na.starts, p.Name)
		n := len(na.starts)
		hook := na.startHook
		na.mu.Unlock()
		if hook != nil {
			if code, msg := hook(p.Name, p.PaneID, n); code != "" {
				return herdtest.RespondErr(w, req.ID, code, msg)
			}
		}
		na.mu.Lock()
		if holder, ok := na.agents[p.Name]; ok && holder.pane != p.PaneID {
			na.mu.Unlock()
			return herdtest.RespondErr(w, req.ID, client.CodeAgentNameTaken, fmt.Sprintf(
				"agent name %s is already used; candidates: pane_id=%s workspace_id=%s status=%s",
				p.Name, holder.pane, holder.workspace, holder.status))
		}
		na.agents[p.Name] = &namedAgent{pane: p.PaneID, workspace: workspaceOfPane(p.PaneID), status: "idle"}
		na.mu.Unlock()
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_started",
			"agent": map[string]any{
				"pane_id": p.PaneID, "workspace_id": workspaceOfPane(p.PaneID), "agent_status": "idle",
				"name": p.Name, "interactive_ready": true, "agent_session": nil,
			},
			"argv": []string{"claude"},
		})
	})
	info := func(req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(req.Params, &p)
		na.mu.Lock()
		agent, ok := na.agents[p.Target]
		var copy namedAgent
		if ok {
			copy = *agent
		}
		na.mu.Unlock()
		if !ok {
			return herdtest.RespondErr(w, req.ID, client.CodeAgentNotFound, "agent "+p.Target+" not found")
		}
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_info",
			"agent": map[string]any{
				"pane_id": copy.pane, "workspace_id": copy.workspace, "agent_status": copy.status,
				"name": p.Target, "interactive_ready": true, "agent_session": nil,
			},
		})
	}
	s.Route(herdtest.MethodAgentGet, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return info(req, w)
	})
	s.Route(herdtest.MethodAgentWait, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		return info(req, w)
	})
	s.Route(herdtest.MethodAgentPrompt, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal(req.Params, &p)
		na.mu.Lock()
		agent, ok := na.agents[p.Target]
		if ok {
			agent.status = "working"
		}
		na.mu.Unlock()
		if !ok {
			return herdtest.RespondErr(w, req.ID, client.CodeAgentNotFound, "agent "+p.Target+" not found")
		}
		return info(req, w)
	})
	s.Route(herdtest.MethodPaneClose, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(req.Params, &p)
		na.mu.Lock()
		na.closes = append(na.closes, p.PaneID)
		for name, agent := range na.agents {
			if agent.pane == p.PaneID {
				delete(na.agents, name)
			}
		}
		na.mu.Unlock()
		return herdtest.RespondJSON(w, req.ID, map[string]any{"type": "ok"})
	})
	return na
}

func workspaceOfPane(pane string) string {
	ws, _, _ := strings.Cut(pane, ":")
	return ws
}

// hold registers an agent the test put there — a pane some other job left.
func (na *namedAgents) hold(name, pane, status string) {
	na.mu.Lock()
	defer na.mu.Unlock()
	na.agents[name] = &namedAgent{pane: pane, workspace: workspaceOfPane(pane), status: status}
}

// foreignWorkspace puts a workspace in the fake herdr that no attempt of
// this executor created, holding `branch`.
func (na *namedAgents) foreignWorkspace(id, branch string) {
	h := na.h
	h.mu.Lock()
	defer h.mu.Unlock()
	h.workspaces[id] = harnessWorkspace{path: filepath.Join(h.t.TempDir(), id), branch: branch, label: id}
}

func (na *namedAgents) setStatus(name, status string) {
	na.mu.Lock()
	defer na.mu.Unlock()
	if agent, ok := na.agents[name]; ok {
		agent.status = status
	}
}

func (na *namedAgents) paneCloses() []string {
	na.mu.Lock()
	defer na.mu.Unlock()
	return append([]string(nil), na.closes...)
}

func (na *namedAgents) startedNames() []string {
	na.mu.Lock()
	defer na.mu.Unlock()
	return append([]string(nil), na.starts...)
}

// roleSpec is the spec the reconciler builds for a role job of one attempt:
// its own job id, its own write ref, the same tick and attempt number.
func (h *harness) roleSpec(jobID, role, tick string) *subprocess.JobSpec {
	spec := h.spec(jobID, tick)
	spec.Role = role
	return spec
}

// THE STALL, reproduced: implement attempt 3 of t1 is collected with its pane
// still up, its gate fails, and the repair job of the same attempt is started.
// Before the fix the repair was named for the ATTEMPT — tick-t1-a3, the name
// the implement attempt's agent still held — and herdr refused it.
func TestTheRepairJobOfAnAttemptLaunchesUnderItsOwnName(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	na := withNamedAgents(h)
	h.ex.opts.Attempt = 3

	implement, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, implement, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(implement); err != nil {
		t.Fatal(err)
	}
	// The implement attempt's agent is idle in its pane: the release did not
	// take it (the lagging-interrupt teardown refusal), exactly as in epic-6in.

	repair, err := h.ex.Start(h.roleSpec("run-harness/tick-t1/repair-3", "plan-repair", "t1"))
	if err != nil {
		t.Fatalf("the repair job could not be started while the implement attempt's pane is still up: %v", err)
	}
	resolve, err := h.ex.Start(h.roleSpec("run-harness/tick-t1/resolve-3-r2", "resolve-conflict", "t1"))
	if err != nil {
		t.Fatalf("the resolve job's second try could not be started beside them: %v", err)
	}

	names := map[string]string{}
	for what, handle := range map[string]*subprocess.JobHandle{
		"implement": implement, "repair": repair, "resolve": resolve} {
		l, err := local(handle)
		if err != nil {
			t.Fatal(err)
		}
		names[what] = l.AgentName
	}
	want := map[string]string{
		"implement": "tick-t1-a3", "repair": "tick-t1-a3-repair", "resolve": "tick-t1-a3-resolve-r2"}
	for what, name := range want {
		if names[what] != name {
			t.Errorf("the %s job's herdr agent is named %q, want %q", what, names[what], name)
		}
	}
	if closes := na.paneCloses(); len(closes) != 0 {
		t.Errorf("starting the role jobs closed panes %v: nothing stood in their way to close", closes)
	}

	h.mu.Lock()
	labels := map[string]bool{}
	for _, ws := range h.workspaces {
		labels[ws.label] = true
	}
	h.mu.Unlock()
	for _, name := range want {
		if !labels[name] {
			t.Errorf("no workspace is labelled %q: a job's workspace is labelled for the job (%v)", name, labels)
		}
	}
}

// short: pure name derivation, no herdr
func TestJobAgentNamesAreJobScopedAndLegal(t *testing.T) {
	spec := func(jobID, tick string) *subprocess.JobSpec {
		return &subprocess.JobSpec{JobID: jobID, Inputs: []subprocess.Input{{Kind: "tick", ID: tick}}}
	}
	cases := []struct {
		jobID, tick string
		attempt     int
		want        string
	}{
		{"run-epic-6in/tick-4i8/attempt-3", "4i8", 3, "tick-4i8-a3"},
		{"run-epic-6in/tick-4i8/repair-3", "4i8", 3, "tick-4i8-a3-repair"},
		{"run-epic-6in/tick-4i8/repair-3-r2", "4i8", 3, "tick-4i8-a3-repair-r2"},
		{"run-epic-6in/tick-4i8/resolve-3", "4i8", 3, "tick-4i8-a3-resolve"},
		{"run-epic-6in/tick-4i8/resolve-3-r3", "4i8", 3, "tick-4i8-a3-resolve-r3"},
		{"run-epic-6in/base-fold-2", "6in", 2, "tick-6in-fold-2"},
	}
	seen := map[string]string{}
	for _, c := range cases {
		got := jobAgentName(spec(c.jobID, c.tick), c.attempt)
		if got != c.want {
			t.Errorf("jobAgentName(%s) = %q, want %q", c.jobID, got, c.want)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s share the herdr name %q", c.jobID, other, got)
		}
		seen[got] = c.jobID
	}
	// An unknown shape is still the JOB's: two job ids never share a name.
	a := jobAgentName(spec("run-x/tick-t1/review-1", "t1"), 1)
	b := jobAgentName(spec("run-x/tick-t1/closeout-1", "t1"), 1)
	if a == b || a == agentName("t1", 1) {
		t.Errorf("unknown-shape job ids got %q and %q (attempt name %q): each job needs a name of its own",
			a, b, agentName("t1", 1))
	}
	// The budget is spent on the tick id, never on the job's discriminator.
	long := strings.Repeat("x", 40)
	for _, jobID := range []string{"run-r/tick-" + long + "/repair-12-r3", "run-r/tick-" + long + "/resolve-12"} {
		name := jobAgentName(spec(jobID, long), 12)
		if len(name) > 32 {
			t.Errorf("jobAgentName(%s) = %q: herdr names are at most 32 characters", jobID, name)
		}
		if !strings.HasSuffix(name, jobSuffix(jobID, 12)) {
			t.Errorf("jobAgentName(%s) = %q lost its job discriminator %q", jobID, name, jobSuffix(jobID, 12))
		}
	}
	// And the reclaimer still reads the TICK off a job-scoped label.
	for label, tick := range map[string]string{
		"tick-4i8-a3-repair": "4i8", "tick-4i8-a3-resolve-r2": "4i8", "tick-6in-fold-2": "6in",
		"tick-t1-a1-0a1b2c": "t1", "tick-a-b-a12": "a-b",
	} {
		if got, _ := tickOfFacts("", "", label); got != tick {
			t.Errorf("tickOfFacts(label %q) = %q, want the tick %q", label, got, tick)
		}
	}
}

// A name held by a SETTLED job of this run — a pane a teardown missed — is
// closed and the launch retried once. Only the pane goes: the stale job's
// workspace and worktree stay for the teardown that owns them.
func TestANameHeldByASettledJobOfThisRunIsFreedAndTheLaunchRetried(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	na := withNamedAgents(h)
	na.foreignWorkspace("w9", "ticfac/run-harness-earlier/tick-t1/attempt-1")
	na.hold("tick-t1-a1", "w9:p1", "idle")

	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("a name held by a settled job of this run stopped the launch: %v", err)
	}
	if closes := na.paneCloses(); len(closes) != 1 || closes[0] != "w9:p1" {
		t.Errorf("pane.close took %v, want exactly the stale holder's pane w9:p1", closes)
	}
	if starts := na.startedNames(); len(starts) != 2 {
		t.Errorf("agent.start was asked %d times (%v), want the refused launch and ONE retry", len(starts), starts)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("the stale holder's workspace was removed (%v): only its pane is this launch's to close", removed)
	}
	l, _ := local(handle)
	if l.AgentName != "tick-t1-a1" {
		t.Errorf("the launch took the name %q, want its own tick-t1-a1", l.AgentName)
	}
}

// A name held by a WORKING job of this run, or by a pane outside the run, is
// refused with a classified reason naming the holder — and nothing is closed.
func TestANameHeldByALiveOrForeignAgentIsRefusedAndNotClosed(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, c := range []struct {
		what, branch, status string
	}{
		{"a working job of this run", "ticfac/run-harness-earlier/tick-t1/attempt-1", "working"},
		{"a job of this run blocked on a person", "ticfac/run-harness-earlier/tick-t1/attempt-1", "blocked"},
		{"a pane outside the run", "feature/somebody-elses", "idle"},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := newHarness(t, harnessOptions{})
			na := withNamedAgents(h)
			na.foreignWorkspace("w9", c.branch)
			na.hold("tick-t1-a1", "w9:p1", c.status)

			_, err := h.start("t1")
			refusal, ok := subprocess.AsRefusal(err)
			if !ok || refusal.Reason != RefusedAgentNameTaken {
				t.Fatalf("the launch answered %v, want the classified %s refusal", err, RefusedAgentNameTaken)
			}
			if !strings.Contains(refusal.Message, "w9:p1") {
				t.Errorf("the refusal does not name the holder's pane: %s", refusal.Message)
			}
			if closes := na.paneCloses(); len(closes) != 0 {
				t.Errorf("pane.close took %v: %s is not this launch's to close", closes, c.what)
			}
		})
	}
}

// A taken name held by this attempt's OWN pane is an earlier launch of it that
// landed behind a lost reply: adopted, never launched twice.
func TestANameHeldByThisAttemptsOwnPaneIsAdopted(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	na := withNamedAgents(h)
	na.startHook = func(name, pane string, n int) (string, string) {
		if n == 1 {
			// The launch lands; its answer does not.
			na.hold(name, pane, "idle")
			return client.CodeAgentNameTaken, "agent name " + name + " is already used; candidates: pane_id=" + pane
		}
		return "", ""
	}
	handle, err := h.start("t1")
	if err != nil {
		t.Fatalf("a launch that landed behind its own refusal was not adopted: %v", err)
	}
	if starts := na.startedNames(); len(starts) != 1 {
		t.Errorf("agent.start was asked %d times (%v): the landed launch is adopted, not repeated", len(starts), starts)
	}
	if closes := na.paneCloses(); len(closes) != 0 {
		t.Errorf("pane.close took %v: this attempt's own agent is not a stale holder", closes)
	}
	if l, _ := local(handle); l.AgentName != "tick-t1-a1" {
		t.Errorf("adopted under %q, want tick-t1-a1", l.AgentName)
	}
}

// The RESUME of epic-6in: the pre-fix build left the repair job's record
// launched-never-confirmed under the implement attempt's name. A restarted run
// re-Starts that job: nothing ever ran in its pane and no prompt was ever
// sent, so it is relaunched in its own workspace under its job's own name —
// never answered from the implement attempt's agent, and never closing it.
func TestAStrandedLaunchRefusedOverAnotherPanesNameIsRelaunchedUnderItsJobsName(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	na := withNamedAgents(h)
	h.ex.opts.Attempt = 3
	implement, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	implementLocal, _ := local(implement)

	// The pre-fix build's repair launch: refused, its record left behind. (The
	// refusal is scripted as a plain one: this build would resolve a real
	// agent_name_taken on the spot, which is the point of the fix.)
	spec := h.roleSpec("run-harness/tick-t1/repair-3", "plan-repair", "t1")
	na.startHook = func(name, pane string, n int) (string, string) {
		if n == 2 {
			return herdtest.CodeInvalidRequest, "the pre-fix build's refused launch (agent name tick-t1-a3 is " +
				"already used; candidates: pane_id=" + implementLocal.PaneID + ")"
		}
		return "", ""
	}
	if _, err := h.ex.Start(spec); err == nil {
		t.Fatal("the scripted refusal did not refuse")
	}
	dir := h.ex.stateDirFor(spec.JobID, 3)
	st := h.ex.storeAt(dir)
	record, err := st.readAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if record.LaunchConfirmed {
		t.Fatal("the refused launch was recorded confirmed")
	}
	// Exactly the record epic-6in's repair-3 carries: the ATTEMPT's name.
	record.AgentName = "tick-t1-a3"
	if err := st.writeAttempt(record); err != nil {
		t.Fatal(err)
	}
	na.startHook = nil

	handle, err := h.ex.Start(spec)
	if err != nil {
		t.Fatalf("the stranded repair launch was not resumed: %v", err)
	}
	l, _ := local(handle)
	if l.AgentName != "tick-t1-a3-repair" || l.PaneID != record.PaneID || l.WorkspaceID != record.WorkspaceID {
		t.Errorf("resumed as %s in pane %s (workspace %s), want tick-t1-a3-repair in its own pane %s (workspace %s)",
			l.AgentName, l.PaneID, l.WorkspaceID, record.PaneID, record.WorkspaceID)
	}
	if closes := na.paneCloses(); len(closes) != 0 {
		t.Errorf("pane.close took %v: the implement attempt's agent is not the repair's to close", closes)
	}
	if prompt, err := os.ReadFile(filepath.Join(dir, filePrompt)); err != nil || len(prompt) == 0 {
		t.Errorf("the relaunched repair has no worker prompt recorded: %v", err)
	}
	status, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != subprocess.StateRunning {
		t.Errorf("the relaunched repair inspects as %s, want running", status.State)
	}
}

// THE LEAK: the reconciler releases a collected worker with revoke-then-
// dispose in the same second, and herdr's status lags the interrupt. The
// dispose read `working` and refused, and the attempt's pane outlived the run.
// The interrupt this executor delivered is given a grace to land.
func TestDisposeAfterTheInterruptWaitsForItToLand(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.ex.opts.InterruptGrace = 5 * time.Second
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	h.setStatus("working") // the worker reported and kept going
	if _, err := h.ex.Cancel(handle); err != nil {
		t.Fatal(err)
	}
	statusFile := h.statusFile
	done := make(chan error, 1)
	go func() {
		time.Sleep(400 * time.Millisecond) // herdr catches up with the ctrl+c
		done <- os.WriteFile(statusFile, []byte("idle"), 0o644)
	}()
	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "collected: the worker is released", KeepBranch: true})
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatalf("the release teardown refused an agent whose interrupt had not landed yet: %v", err)
	}
	if removed := h.removals(); len(removed) != 1 {
		t.Errorf("worktree.remove saw %v, want the attempt's workspace", removed)
	}
}

// The grace is a grace, not a waiver: an agent still working when it runs out
// is refused exactly as before.
func TestDisposeStillRefusesAnAgentWorkingPastTheInterruptGrace(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	h.ex.opts.InterruptGrace = 500 * time.Millisecond
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	h.doWork(t, handle, "STATUS: DONE")
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatal(err)
	}
	h.setStatus("working")
	if _, err := h.ex.Cancel(handle); err != nil {
		t.Fatal(err)
	}
	err = h.ex.Dispose(handle, subprocess.DisposeOptions{Reason: "collected", KeepBranch: true})
	var refusal *subprocess.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != subprocess.RefusedLive {
		t.Fatalf("dispose answered %v, want the live refusal once the grace ran out", err)
	}
	if removed := h.removals(); len(removed) != 0 {
		t.Errorf("worktree.remove saw %v over a working agent", removed)
	}
}
