package subprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The stuck watch's parts (tick wv2): the process table, the transcripts, and
// the decision — and, end to end, the local supervisor watching a real
// runner process whose CPU and silence are real.

func TestParseProcsReadsBothPsDialects(t *testing.T) {
	procs := ParseProcs([]byte("    1     0 267:05.16\n  150     1   2:10.49\n 7000   150 1-02:03:04\n 7001 7000 00:00:07\n garbage\n"))
	want := map[int]time.Duration{
		1:    267*time.Minute + 5160*time.Millisecond,
		150:  2*time.Minute + 10490*time.Millisecond,
		7000: 26*time.Hour + 3*time.Minute + 4*time.Second,
		7001: 7 * time.Second,
	}
	if len(procs) != len(want) {
		t.Fatalf("parsed %d rows, want %d: %+v", len(procs), len(want), procs)
	}
	for _, p := range procs {
		if want[p.PID] != p.CPU {
			t.Errorf("pid %d cpu = %s, want %s", p.PID, p.CPU, want[p.PID])
		}
	}
}

func TestTreeCPUWalksByPidFromTheRoot(t *testing.T) {
	procs := []Proc{
		{PID: 100, PPID: 1, CPU: time.Hour},          // the shell
		{PID: 101, PPID: 100, CPU: 30 * time.Second}, // the agent
		{PID: 102, PPID: 101, CPU: 5 * time.Second},  // a tool
		{PID: 103, PPID: 102, CPU: 7 * time.Second},  // the tool's child
		{PID: 200, PPID: 1, CPU: time.Hour},          // somebody else's
	}
	if cpu, n := TreeCPU(procs, 100, 2); cpu != 12*time.Second || n != 2 {
		t.Errorf("tools under the shell = %s over %d, want 12s over 2 (the agent itself excluded)", cpu, n)
	}
	if cpu, n := TreeCPU(procs, 101, 1); cpu != 12*time.Second || n != 2 {
		t.Errorf("tools under the runner = %s over %d, want 12s over 2", cpu, n)
	}
}

func writeTranscript(t *testing.T, kind, cwd string, lines ...map[string]any) {
	t.Helper()
	dir := TranscriptDir(kind, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		raw, _ := json.Marshal(l)
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The two layouts, as the installed harnesses write them (pi 0.85.1's
// session-manager.js; Claude Code's ~/.claude/projects), and the event kinds
// read out of each.
func TestTranscriptsOfPiAndClaudeAreReadWhereTheHarnessWritesThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvTranscriptHome, home)
	cwd := t.TempDir()
	real, _ := filepath.EvalSymlinks(cwd)

	if got, want := TranscriptDir("pi", cwd), filepath.Join(home, ".pi", "agent", "sessions",
		"--"+strings.ReplaceAll(strings.TrimPrefix(real, "/"), "/", "-")+"--"); got != want {
		t.Errorf("pi dir = %s, want %s", got, want)
	}
	if got := TranscriptDir("claude", "/Users/x/.herdr/worktrees/a-b"); got != filepath.Join(home, ".claude", "projects", "-Users-x--herdr-worktrees-a-b") {
		t.Errorf("claude dir = %s", got)
	}
	if TranscriptDir("codex", cwd) != "" {
		t.Error("codex has a transcript dir, but its layout is not read")
	}

	writeTranscript(t, "pi", cwd,
		map[string]any{"type": "session", "timestamp": "2026-09-28T06:43:07.734Z"},
		map[string]any{"type": "message", "timestamp": "2026-09-28T07:01:04.886Z",
			"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "thinking"}, map[string]any{"type": "toolCall"}}}})
	ev, ok := LastTranscriptEvent("pi", cwd)
	if !ok || ev.Kind != "tool call started" || !ev.ToolInFlight || ev.At.Format(time.RFC3339) != "2026-09-28T07:01:04Z" {
		t.Errorf("pi last event = %+v (%t), want a tool call started at 07:01:04", ev, ok)
	}

	writeTranscript(t, "claude", cwd,
		map[string]any{"type": "assistant", "timestamp": "2026-09-28T08:39:19.791Z",
			"message": map[string]any{"content": []any{map[string]any{"type": "tool_use"}}}},
		map[string]any{"type": "user", "timestamp": "2026-09-28T08:39:25.000Z",
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result"}}}},
		map[string]any{"type": "bridge-session"})
	ev, ok = LastTranscriptEvent("claude", cwd)
	if !ok || ev.Kind != "tool result" || ev.ToolInFlight {
		t.Errorf("claude last event = %+v (%t), want a tool result, the undated line skipped", ev, ok)
	}
}

// The decision, on a hand clock: quiet for the window → nudge; quiet a window
// past the nudge → stop; activity after the nudge clears it.
func TestDecideStuck(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	after := 15 * time.Minute
	s := &ActivityState{FirstSeenAt: t0}
	quiet := Activity{FirstSeenAt: t0}
	if step := DecideStuck(s, quiet, t0.Add(14*time.Minute), after); step != StuckNone {
		t.Fatalf("14m quiet = %v, want none", step)
	}
	if step := DecideStuck(s, quiet, t0.Add(15*time.Minute), after); step != StuckNudge {
		t.Fatalf("15m quiet = %v, want nudge", step)
	}
	s.StuckNudgedAt = t0.Add(15 * time.Minute)
	if step := DecideStuck(s, quiet, t0.Add(29*time.Minute), after); step != StuckNone {
		t.Fatalf("14m after the nudge = %v, want none", step)
	}
	if step := DecideStuck(s, quiet, t0.Add(30*time.Minute), after); step != StuckStop {
		t.Fatalf("15m after the nudge = %v, want stop", step)
	}
	answered := quiet
	answered.Transcript, answered.HasTranscript = TranscriptEvent{At: t0.Add(20 * time.Minute), Kind: "model output"}, true
	s2 := &ActivityState{FirstSeenAt: t0, StuckNudgedAt: t0.Add(15 * time.Minute)}
	if step := DecideStuck(s2, answered, t0.Add(30*time.Minute), after); step != StuckNone || !s2.StuckNudgedAt.IsZero() {
		t.Fatalf("answered after the nudge = %v (nudge %v), want none and the nudge cleared", step, s2.StuckNudgedAt)
	}
	busy := quiet
	busy.CPUAt = t0.Add(29 * time.Minute)
	if step := DecideStuck(&ActivityState{FirstSeenAt: t0}, busy, t0.Add(30*time.Minute), after); step != StuckNone {
		t.Fatalf("a tool that used CPU a minute ago = %v, want none", step)
	}
}

func TestTheCPUMarkMovesOnlyOnRealUse(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	window := 15 * time.Minute
	s := &ActivityState{FirstSeenAt: t0}
	s.ObserveCPU(10*time.Second, t0.Add(time.Minute), window)
	if !s.CPUMarkAt.Equal(t0) {
		t.Errorf("the first sample dated the mark %v, want the baseline %v: a first sample is not activity", s.CPUMarkAt, t0)
	}
	s.ObserveCPU(12*time.Second, t0.Add(2*time.Minute), window)
	if !s.CPUMarkAt.Equal(t0) {
		t.Errorf("2s of CPU in 15m moved the mark: an idle helper's tick is not progress")
	}
	s.ObserveCPU(20*time.Second, t0.Add(3*time.Minute), window)
	if !s.CPUMarkAt.Equal(t0.Add(3 * time.Minute)) {
		t.Errorf("10s of CPU did not move the mark")
	}
}

// End to end, the local supervisor: a runner whose tool burns CPU is not
// stuck; when the tool goes idle and nothing else moves, the runner is
// interrupted and re-prompted as stuck — once — and the re-prompted run
// finishes.
func TestALocalRunnerIsNotStuckWhileItsToolIsBusyAndIsRepromptedWhenItHangs(t *testing.T) {
	window := 1500 * time.Millisecond
	f := newFixture(t, fixtureOptions{mode: "busy_then_hang", stuckAfter: window, env: []string{"FAKE_RUNNER_BUSY=4"}})
	handle := f.Start(f.spec("run-wv2/tick-bsy/attempt-1", "bsy"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s, want ready-to-merge: the re-prompted run finishes\n%s", collected.Verdict,
			formatObservations(status.Observations))
	}
	var nudges, restarts []Observation
	var nudgedAt, startedAt time.Time
	for _, o := range status.Observations {
		switch {
		case IsStuckNudge(o):
			nudges = append(nudges, o)
			nudgedAt, _ = time.Parse(time.RFC3339, o.At)
		case o.Kind == ObsStarted && strings.Contains(o.Detail, "re-prompted as stuck"):
			restarts = append(restarts, o)
		case o.Kind == ObsStarted && startedAt.IsZero():
			startedAt, _ = time.Parse(time.RFC3339, o.At)
		}
	}
	if len(nudges) != 1 || len(restarts) != 1 {
		t.Fatalf("stuck nudges %d, restarts %d, want 1 and 1:\n%s", len(nudges), len(restarts), formatObservations(status.Observations))
	}
	// The busy phase is 4s against a 1.5s window: a nudge before it ended
	// would be the watch mistaking a working tool for a stuck one.
	if nudgedAt.Sub(startedAt) < 4*time.Second {
		t.Errorf("the runner was nudged %s after it started, inside its 4s busy phase", nudgedAt.Sub(startedAt))
	}
	if !strings.Contains(nudges[0].Detail, "tool process(es) last used CPU") {
		t.Errorf("the nudge's evidence does not name the tool CPU: %s", nudges[0].Detail)
	}
}

// A runner that hangs again after its stuck nudge is stopped and settled as
// stuck: a runner error that says so.
func TestALocalRunnerThatStaysStuckIsStopped(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang", stuckAfter: 1500 * time.Millisecond})
	handle := f.Start(f.spec("run-wv2/tick-hng/attempt-1", "hng"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	if status.State != StateFailed || !strings.Contains(lastObservationDetail(status), "stopped as stuck") {
		t.Fatalf("state %s, want failed as stuck:\n%s", status.State, formatObservations(status.Observations))
	}
	var stops int
	// The last observation is inspect's own sentence about the state; the
	// supervisor's record of the stop is before it.
	for _, o := range status.Observations[:len(status.Observations)-1] {
		if IsStuckStop(o) {
			stops++
		}
	}
	if stops != 1 {
		t.Errorf("stuck stops = %d, want 1:\n%s", stops, formatObservations(status.Observations))
	}
	collected := f.collect(handle)
	if collected.Result.FailureClass != FailureRunnerError || !strings.Contains(collected.Message, "stuck") {
		t.Errorf("collect = %q / %q, want a runner error that says the runner was stopped as stuck",
			collected.Result.FailureClass, collected.Message)
	}
}

func lastObservationDetail(status *JobStatus) string {
	if len(status.Observations) == 0 {
		return ""
	}
	return status.Observations[len(status.Observations)-1].Detail
}

// The whole-tail read the dashboard's activity window answers from (hn6,
// tick ltg): every dated line's stamp — the moments the worker was seen
// doing something — and the LAST tool call as one bounded line, in both
// harnesses' own block spellings.
func TestReadTranscriptEventsAnswersTheWindowAndTheLastToolCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvTranscriptHome, home)
	cwd := t.TempDir()

	writeTranscript(t, "pi", cwd,
		map[string]any{"type": "session", "timestamp": "2026-09-28T06:43:07.734Z"},
		map[string]any{"type": "message", "timestamp": "2026-09-28T06:44:00.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "toolCall", "name": "bash", "arguments": map[string]any{"command": "ls -la"}},
				map[string]any{"type": "toolCall", "name": "bash",
					"arguments": map[string]any{"command": "go test ./internal/reconcile"}},
			}}})
	events, ok := ReadTranscriptEvents(home, "pi", cwd)
	if !ok {
		t.Fatal("the transcript stands and the tail reader answered nothing")
	}
	if len(events.Events) != 2 {
		t.Errorf("the tail read %d dated events, want 2: every dated line is a moment", len(events.Events))
	}
	if at, err := time.Parse(time.RFC3339Nano, "2026-09-28T06:44:00.000Z"); err != nil || !events.Events[1].Equal(at) {
		t.Errorf("the events read %v, want the lines' own stamps in file order", events.Events)
	}
	// The LAST tool call of the last message that carries one — the second
	// block, not the first.
	if events.LastToolCall != "bash: go test ./internal/reconcile" {
		t.Errorf("the last tool call is %q, want the tool's own name plus its first argument", events.LastToolCall)
	}
	if at, err := time.Parse(time.RFC3339Nano, "2026-09-28T06:44:00.000Z"); err != nil || !events.LastToolAt.Equal(at) {
		t.Errorf("the last tool call is stamped %v, want its own line's stamp", events.LastToolAt)
	}

	// Claude Code's own spellings: tool_use blocks, the arguments in "input".
	writeTranscript(t, "claude", cwd,
		map[string]any{"type": "assistant", "timestamp": "2026-09-28T08:39:19.791Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go vet ./..."}},
			}}})
	events, ok = ReadTranscriptEvents(home, "claude", cwd)
	if !ok || events.LastToolCall != "Bash: go vet ./..." {
		t.Errorf("the claude tail read %q (ok %t), want the tool_use block's own name and first argument",
			events.LastToolCall, ok)
	}

	// The line stays bounded however long the argument was.
	long := strings.Repeat("word ", 40)
	writeTranscript(t, "pi", cwd,
		map[string]any{"type": "message", "timestamp": "2026-09-28T09:00:00.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "toolCall", "name": "bash", "arguments": map[string]any{"command": long}},
			}}})
	events, _ = ReadTranscriptEvents(home, "pi", cwd)
	if len([]rune(events.LastToolCall)) > 80 {
		t.Errorf("the last tool call line is %d runes, want it bounded to 80", len([]rune(events.LastToolCall)))
	}
	if !strings.HasPrefix(events.LastToolCall, "bash: ") || !strings.HasSuffix(events.LastToolCall, "…") {
		t.Errorf("the bounded line %q lost the tool's name or the cut's ellipsis", events.LastToolCall)
	}

	// A tool call whose first argument is not text (a list) carries the tool's
	// name alone: never a guessed argument.
	writeTranscript(t, "pi", cwd,
		map[string]any{"type": "message", "timestamp": "2026-09-28T09:01:00.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "toolCall", "name": "todoWrite",
					"arguments": map[string]any{"todos": []any{map[string]any{"id": "1"}}}},
			}}})
	events, _ = ReadTranscriptEvents(home, "pi", cwd)
	if events.LastToolCall != "todoWrite" {
		t.Errorf("the last tool call is %q, want the tool's name alone for a non-text first argument", events.LastToolCall)
	}

	// Nothing to read: a working directory with no session at all.
	if _, ok := ReadTranscriptEvents(home, "pi", t.TempDir()); ok {
		t.Error("a worktree with no transcript answered events")
	}
}
