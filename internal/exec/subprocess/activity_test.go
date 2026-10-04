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

// The `pi` runner is the durable Node host (tick hpk), not the pi CLI: it
// writes no session transcript, its conversation is the attempt's own
// SQLite storage (workerconfig.go), and the watch's last-event signal for
// it is the storage file's mtime — never the CLI's session files, which a
// pi transcript on the host must not be mistaken for (tick bgx).
func TestTheDurableRunnersTranscriptSignalIsItsStorageNotTheCliSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvTranscriptHome, home)
	worktree := t.TempDir()
	state := t.TempDir()
	// A pi CLI session transcript exists for the worktree — the old, wrong
	// signal, still on disk where the CLI keeps it.
	writeTranscript(t, "pi", worktree,
		map[string]any{"type": "message", "timestamp": "2026-09-28T07:01:04.886Z",
			"message": map[string]any{"role": "assistant"}})

	record := &attemptRecord{Runner: "pi", Worktree: worktree, State: state}

	// No storage yet: nothing can be read — and the CLI transcript is not
	// read in its place, or the evidence would point an operator at a file
	// the runner never writes.
	if _, ok := lastRunnerEvent(record); ok {
		t.Error("the durable runner answered a transcript event with no storage to read")
	}

	// The storage appears: its mtime is the event, its path names the file.
	storage := filepath.Join(state, fileWorkerStorage)
	if err := os.WriteFile(storage, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ev, ok := lastRunnerEvent(record)
	if !ok {
		t.Fatal("no event with the storage present")
	}
	if ev.Path != storage {
		t.Errorf("event path = %s, want the storage %s", ev.Path, storage)
	}
	if d := time.Since(ev.At); d < 0 || d > time.Minute {
		t.Errorf("event at %s, want the storage's mtime (now, ±1m)", ev.At)
	}

	// An overridden `pi` runner is the CLI escape hatch (TICFAC_RUNNER_ARGV,
	// the tests' fake runner): it keeps the CLI's session transcript, the
	// only pi that still writes one.
	record.RunnerArgv = []string{"pi", "-p"}
	ev, ok = lastRunnerEvent(record)
	if !ok || ev.Path != filepath.Join(TranscriptDir("pi", worktree), "s.jsonl") {
		t.Errorf("overridden runner event = %+v (%t), want the pi CLI session transcript", ev, ok)
	}
}

// The evidence sentence names where a live worker's progress lives: the
// durable runner's storage when it can be read, the storage honestly when
// it cannot, and the CLI transcript for every other runner as before.
func TestEvidenceNamesWhereTheDurableRunnersProgressLives(t *testing.T) {
	now := time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC)
	at := now.Add(-2 * time.Minute)
	storage := filepath.Join("state", string(filepath.Separator), fileWorkerStorage)

	durable := Activity{
		FirstSeenAt:      now.Add(-time.Hour),
		TranscriptSource: sourceStorage,
		Transcript:       TranscriptEvent{At: at, Kind: "storage written", Path: storage},
		HasTranscript:    true,
	}
	if s := durable.Evidence(now); !strings.Contains(s, "its conversation storage was last written 2m0s ago") ||
		!strings.Contains(s, "("+fileWorkerStorage+")") {
		t.Errorf("durable evidence = %s, want the storage and its file named", s)
	}

	durable.HasTranscript = false
	if s := durable.Evidence(now); !strings.Contains(s, "its conversation storage could not be read") {
		t.Errorf("durable evidence without a storage = %s, want the storage named as unreadable", s)
	}

	cli := Activity{
		FirstSeenAt:   now.Add(-time.Hour),
		Transcript:    TranscriptEvent{At: at, Kind: "model output"},
		HasTranscript: true,
	}
	if s := cli.Evidence(now); !strings.Contains(s, "its transcript's last event was 2m0s ago (model output)") {
		t.Errorf("cli evidence = %s, want the session transcript sentence as before", s)
	}
	if s := (Activity{FirstSeenAt: now.Add(-time.Hour)}).Evidence(now); !strings.Contains(s, "no session transcript could be read") {
		t.Errorf("cli evidence without a transcript = %s, want the missing-transcript sentence as before", s)
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
