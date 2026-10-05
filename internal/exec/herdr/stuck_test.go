package herdr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/herdtest"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The stuck watch (tick wv2): activity, not time, decides whether a herdr
// agent is stuck. Every test drives a fake herdr, a fake process table and
// a fake harness transcript on a fake clock.

const stuckWindow = 60 * time.Second // well inside the harness spec's 300s wall

// stuckRig is a harness with the watch's inputs under the test's control.
type stuckRig struct {
	*harness
	clock *wallClock
	local *herdrHandle

	mu      sync.Mutex
	prompts []string
	toolCPU time.Duration
}

func newStuckRig(t *testing.T, kind string, startAt time.Time) (*stuckRig, *subprocess.JobHandle) {
	t.Helper()
	t.Setenv(subprocess.EnvTranscriptHome, t.TempDir())
	h := newHarness(t, harnessOptions{kind: kind})
	r := &stuckRig{harness: h, clock: &wallClock{t: startAt}}
	h.ex.now = r.clock.now
	h.ex.opts.StuckAfter = stuckWindow
	// The process table: the pane's shell (100), the agent (101), and one
	// tool process under the agent (102) whose CPU the test sets.
	h.ex.opts.Procs = func() ([]subprocess.Proc, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return []subprocess.Proc{
			{PID: 100, PPID: 1, CPU: time.Second},
			{PID: 101, PPID: 100, CPU: 40 * time.Second},
			{PID: 102, PPID: 101, CPU: r.toolCPU},
		}, nil
	}
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	local, err := local(handle)
	if err != nil {
		t.Fatal(err)
	}
	r.local = local
	h.setStatus("working")
	// The checkout's files carry REAL mtimes, stamped while the harness was
	// set up; the fake clock must not trail them, or a worktree would look
	// "changed in the future" on a slow host.
	r.clock.mu.Lock()
	if real := time.Now().UTC(); real.After(r.clock.t) {
		r.clock.t = real
	}
	r.clock.mu.Unlock()
	h.server.Route(herdtest.MethodPaneProcessInfo, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "pane_process_info",
			"process_info": map[string]any{"pane_id": p.PaneID, "shell_pid": 100,
				"foreground_processes": []any{map[string]any{"pid": 101, "name": kind}}},
		})
	})
	h.server.Route(herdtest.MethodAgentPrompt, func(t *testing.T, req herdtest.Request, w *herdtest.ConnWriter) error {
		var p struct {
			Target string `json:"target"`
			Text   string `json:"text"`
		}
		_ = json.Unmarshal(req.Params, &p)
		r.mu.Lock()
		r.prompts = append(r.prompts, p.Text)
		r.mu.Unlock()
		return herdtest.RespondJSON(w, req.ID, map[string]any{
			"type": "agent_prompted",
			"agent": map[string]any{"pane_id": "w1:p1", "agent_status": "working", "name": p.Target,
				"interactive_ready": true, "agent_session": nil},
		})
	})
	return r, handle
}

func (r *stuckRig) setToolCPU(d time.Duration) {
	r.mu.Lock()
	r.toolCPU = d
	r.mu.Unlock()
}

func (r *stuckRig) promptsContaining(s string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.prompts {
		if strings.Contains(p, s) {
			out = append(out, p)
		}
	}
	return out
}

// transcriptEvent appends one claude-shaped event, stamped on the fake clock.
func (r *stuckRig) transcriptEvent(t *testing.T, kind string, content string) {
	t.Helper()
	dir := subprocess.TranscriptDir(kind, r.local.Worktree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := map[string]any{"type": "assistant", "timestamp": r.clock.now().UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": content}}}}
	raw, _ := json.Marshal(line)
	f, err := os.OpenFile(filepath.Join(dir, "session.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\n", raw)
}

func (r *stuckRig) inspect(t *testing.T, handle *subprocess.JobHandle) *subprocess.JobStatus {
	t.Helper()
	status, err := r.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func observationsWhere(obs []subprocess.Observation, pred func(subprocess.Observation) bool) []subprocess.Observation {
	var out []subprocess.Observation
	for _, o := range obs {
		if pred(o) {
			out = append(out, o)
		}
	}
	return out
}

// [A1] A quiet transcript over a busy tool is not stuck; the same tool gone
// idle is. The agent's last transcript event is a tool call with no result
// (a long test suite): while the tool processes burn CPU nothing happens,
// and once they stop, the agent is nudged after the window — the evidence
// naming the in-flight tool call and the idle CPU.
func TestABusyToolIsNotStuckButTheSameToolGoneIdleIs(t *testing.T) {
	shorttest.EndToEnd(t)
	r, handle := newStuckRig(t, "claude", time.Now().UTC())
	r.transcriptEvent(t, "claude", "tool_use")

	for i := 1; i <= 15; i++ { // 150s of a busy tool, well past the 60s window
		r.clock.advance(10 * time.Second)
		r.setToolCPU(time.Duration(i) * 8 * time.Second)
		if status := r.inspect(t, handle); status.State != subprocess.StateRunning {
			t.Fatalf("state = %s while the tool is busy, want running", status.State)
		}
	}
	if got := r.promptsContaining("appear stuck"); len(got) != 0 {
		t.Fatalf("a worker whose tool process is burning CPU was nudged as stuck: %q", got)
	}

	// The tool goes idle: CPU stops advancing.
	var nudged *subprocess.JobStatus
	for i := 0; i < 8 && nudged == nil; i++ {
		r.clock.advance(10 * time.Second)
		status := r.inspect(t, handle)
		if len(r.promptsContaining("appear stuck")) > 0 {
			nudged = status
		}
	}
	if nudged == nil {
		t.Fatal("the tool went idle with the transcript quiet and nothing moving, and the agent was never nudged")
	}
	st := r.ex.storeAt(r.local.State)
	obs, _ := st.observationsFrom("")
	nudges := observationsWhere(obs, subprocess.IsStuckNudge)
	if len(nudges) != 1 {
		t.Fatalf("stuck-nudge observations = %d, want 1:\n%s", len(nudges), formatObs(obs))
	}
	for _, want := range []string{"tool call started, with no result yet", "tool process(es) last used CPU", "worktree last changed"} {
		if !strings.Contains(nudges[0].Detail, want) {
			t.Errorf("the nudge's evidence %q does not name %q", nudges[0].Detail, want)
		}
	}
}

// [A2]+[A3] Quiet on every signal: nudged once with the evidence, then — still
// quiet a window later — interrupted with the harness's own key, snapshotted,
// closed, and settled failed as stuck; collect reads it as a runner error
// that says so.
func TestAStuckAgentIsNudgedOnceThenStopped(t *testing.T) {
	shorttest.EndToEnd(t)
	r, handle := newStuckRig(t, "claude", time.Now().UTC())
	r.transcriptEvent(t, "claude", "thinking")
	if err := os.WriteFile(filepath.Join(r.local.Worktree, "wip.txt"), []byte("half done\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.clock.advance(stuckWindow + 5*time.Second) // a margin for the files the test itself writes
	if status := r.inspect(t, handle); status.State != subprocess.StateRunning {
		t.Fatalf("state = %s at the nudge, want running", status.State)
	}
	if got := r.promptsContaining("appear stuck"); len(got) != 1 {
		t.Fatalf("stuck nudges = %d, want exactly 1", len(got))
	}
	r.clock.advance(stuckWindow / 2)
	if status := r.inspect(t, handle); status.State != subprocess.StateRunning {
		t.Fatalf("state = %s half a window after the nudge, want running", status.State)
	}
	if got := r.promptsContaining("appear stuck"); len(got) != 1 {
		t.Fatalf("stuck nudges = %d after half a window, want still 1: one nudge, never a stream", len(got))
	}
	if closes := r.paneCloses(); len(closes) != 0 {
		t.Fatalf("the pane was closed %v before the window after the nudge had passed", closes)
	}

	r.clock.advance(stuckWindow/2 + time.Second)
	status := r.inspect(t, handle)
	if status.State != subprocess.StateFailed || !status.Terminal {
		t.Fatalf("state = %s (terminal %t) a window after the nudge with nothing moving, want failed", status.State, status.Terminal)
	}
	if detail := lastDetail(status.Observations); !strings.Contains(detail, "stopped as stuck") {
		t.Errorf("the settlement reads %q, want it to say the agent was stopped as stuck", detail)
	}
	sent := r.server.SendKeysCalls()
	if len(sent) == 0 || strings.Join(sent[len(sent)-1].Keys, " ") != "esc" {
		t.Errorf("send_keys = %+v, want pi's own interrupt, esc", sent)
	}
	if closes := r.paneCloses(); len(closes) != 1 {
		t.Errorf("pane.close calls = %v, want one", closes)
	}
	st := r.ex.storeAt(r.local.State)
	if _, ok := st.wipSnapshot(); !ok {
		t.Error("the stuck agent's pane was closed with no wip snapshot taken first")
	}
	obs, _ := st.observationsFrom("")
	if stops := observationsWhere(obs, subprocess.IsStuckStop); len(stops) != 1 {
		t.Errorf("stuck-stop observations = %d, want 1:\n%s", len(stops), formatObs(obs))
	}

	// Durable: herdr gone quiet still reads the stop.
	r.server.Close()
	if status := r.inspect(t, handle); status.State != subprocess.StateFailed {
		t.Errorf("state = %s after herdr went quiet, want failed from the executor's own record", status.State)
	}
	collected, err := r.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Result.FailureClass != subprocess.FailureRunnerError || !strings.Contains(collected.Message, "stuck") {
		t.Errorf("collect = %q / %q, want a runner error that says the worker was stopped as stuck",
			collected.Result.FailureClass, collected.Message)
	}
}

// Activity after the nudge clears it: the agent answered, so the window
// starts again, and a later silence earns a nudge of its own rather than a
// stop.
func TestActivityAfterTheNudgeClearsIt(t *testing.T) {
	shorttest.EndToEnd(t)
	r, handle := newStuckRig(t, "claude", time.Now().UTC())
	r.transcriptEvent(t, "claude", "text")
	r.clock.advance(stuckWindow + 5*time.Second) // a margin for the files the test itself writes
	r.inspect(t, handle)
	if got := r.promptsContaining("appear stuck"); len(got) != 1 {
		t.Fatalf("stuck nudges = %d, want 1", len(got))
	}
	r.clock.advance(20 * time.Second)
	r.transcriptEvent(t, "claude", "tool_use") // it answered
	r.clock.advance(stuckWindow - 10*time.Second)
	if status := r.inspect(t, handle); status.State != subprocess.StateRunning {
		t.Fatalf("state = %s after the agent answered the nudge, want running", status.State)
	}
	if closes := r.paneCloses(); len(closes) != 0 {
		t.Fatalf("an agent that answered its nudge was closed: %v", closes)
	}
	r.clock.advance(stuckWindow)
	r.inspect(t, handle)
	if got := r.promptsContaining("appear stuck"); len(got) != 2 {
		t.Fatalf("stuck nudges = %d after a second silence, want 2", len(got))
	}
	if closes := r.paneCloses(); len(closes) != 0 {
		t.Fatalf("a second silence was stopped without its own nudge: %v", closes)
	}
}

// [A5] A large uncommitted change on a branch that has not moved for the
// interval is asked to be committed — once per interval, and never while the
// agent is merely thinking about a small one.
func TestALargeUncommittedChangeIsAskedToBeCommittedOncePerInterval(t *testing.T) {
	shorttest.EndToEnd(t)
	// The fake clock starts 40 minutes after the base commit and the
	// checkout, so the branch has "not moved" for 40 minutes.
	r, handle := newStuckRig(t, "claude", time.Now().UTC().Add(40*time.Minute))
	for i := 0; i < subprocess.WipNudgeFiles+2; i++ {
		name := filepath.Join(r.local.Worktree, fmt.Sprintf("new-%d.go", i))
		if err := os.WriteFile(name, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.transcriptEvent(t, "claude", "tool_use") // busy: never stuck here
	r.inspect(t, handle)
	wip := r.promptsContaining("Commit your work in progress")
	if len(wip) != 1 {
		t.Fatalf("commit-WIP nudges = %d, want 1", len(wip))
	}
	st := r.ex.storeAt(r.local.State)
	obs, _ := st.observationsFrom("")
	if got := observationsWhere(obs, subprocess.IsWipNudge); len(got) != 1 || !strings.Contains(got[0].Detail, "uncommitted") {
		t.Fatalf("wip-nudge observations = %v, want one naming the uncommitted change", got)
	}
	for i := 0; i < 5; i++ {
		r.clock.advance(20 * time.Second)
		r.transcriptEvent(t, "claude", "tool_use")
		r.inspect(t, handle)
	}
	if wip := r.promptsContaining("Commit your work in progress"); len(wip) != 1 {
		t.Fatalf("commit-WIP nudges = %d within one interval, want still 1", len(wip))
	}
}
