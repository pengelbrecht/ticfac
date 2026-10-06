package subprocess

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

// The claude layout, as the installed harness writes it (Claude Code's
// ~/.claude/projects), and the event kinds read out of it. The pi CLI's
// layout went with the herdr pi kind (epic 43y, tick uxi): the durable
// runner named "pi" writes no session transcript at all, so kind pi has
// no transcript dir — nothing under it can be mistaken for the durable
// host's signal.
func TestClaudeTranscriptsAreReadWhereTheHarnessWritesThem(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvTranscriptHome, home)
	cwd := t.TempDir()

	if got := TranscriptDir("claude", "/Users/x/.herdr/worktrees/a-b"); got != filepath.Join(home, ".claude", "projects", "-Users-x--herdr-worktrees-a-b") {
		t.Errorf("claude dir = %s", got)
	}
	if TranscriptDir("codex", cwd) != "" {
		t.Error("codex has a transcript dir, but its layout is not read")
	}
	// The deleted pi kind's layout is gone with it: the durable runner named
	// pi must not be answered from session files it never writes.
	if got := TranscriptDir("pi", cwd); got != "" {
		t.Errorf("pi transcript dir = %s, want none: the durable pi runner's conversation is its storage, and the pi CLI's layout left with the herdr pi kind", got)
	}

	writeTranscript(t, "claude", cwd,
		map[string]any{"type": "assistant", "timestamp": "2026-09-28T08:39:19.791Z",
			"message": map[string]any{"content": []any{map[string]any{"type": "tool_use"}}}},
		map[string]any{"type": "user", "timestamp": "2026-09-28T08:39:25.000Z",
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result"}}}},
		map[string]any{"type": "bridge-session"})
	ev, ok := LastTranscriptEvent("claude", cwd)
	if !ok || ev.Kind != "tool result" || ev.ToolInFlight {
		t.Errorf("claude last event = %+v (%t), want a tool result, the undated line skipped", ev, ok)
	}
}

// The `pi` runner is the durable Node host (tick hpk), not the pi CLI: it
// writes no session transcript, its conversation is the attempt's own
// SQLite storage (workerconfig.go), and the watch's last-event signal for
// it is the storage file's mtime. The pi CLI session files it must never be
// mistaken for went with the herdr pi kind (epic 43y, tick uxi): TranscriptDir
// knows no pi layout any more, so nothing under ~/.pi can be read as the
// durable runner's evidence (tick bgx).
//
// The record's RunnerArgv is the RESOLVED argv — what the supervisor runs,
// rendered at Start and never empty — and not the launch-time override the
// durable predicate is decided on (tick rpw): the record carries the
// override as its own flag, and a test that fakes the record the way the
// supervisor reads it must use that shape, or it certifies the defect it
// hides.
func TestTheDurableRunnersTranscriptSignalIsItsStorageNotTheCliSessions(t *testing.T) {
	worktree := t.TempDir()
	state := t.TempDir()

	// The production shape, as executor.go records it: the RESOLVED argv,
	// which is never empty, and the override flag unset because this launch
	// came from the runner table.
	record := &attemptRecord{
		Runner: "pi", Worktree: worktree, State: state,
		RunnerArgv: []string{"node", "--experimental-strip-types",
			"--import", filepath.Join("harness", "runtime", "register.mjs"),
			filepath.Join("harness", "src", "local", "main.ts"),
			"--config", filepath.Join(state, "worker.json"),
			"--message", "do the job"},
	}
	if !durableAttempt(record) {
		t.Error("a table-launched durable attempt read back with its resolved argv is not durable")
	}

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
	// the tests' fake runner). It keeps no transcript signal at all now:
	// the pi CLI's layout left with the herdr pi kind, so the watch answers
	// nothing rather than pointing an operator at a file it no longer knows
	// how to find — the other signals (the process table, the storage) are
	// what an overridden runner has. The override is the RECORD's flag: an
	// override replaces the whole invocation, so nothing of the table's —
	// not the durable storage, which belongs to the table's argv — is read
	// through one, whatever argv it happens to spell.
	record.RunnerArgvOverride = true
	if durableAttempt(record) {
		t.Error("an overridden runner is not the table's durable runner")
	}
	if ev, ok := lastRunnerEvent(record); ok {
		t.Errorf("overridden runner event = %+v, want none: the pi CLI session layout is gone with the herdr pi kind", ev)
	}
}

// The record's half of the durable predicate (tick rpw): the supervisor
// reads an attempt back, not the launch, so what decides is the record's
// own override flag — never its RunnerArgv, which is the resolved argv and
// never empty. An attempt recorded before the flag is not an override: the
// executor that wrote the record and the supervisor that reads it are the
// same binary, and a table launch is the default a bare record spells.
func TestDurableAttemptIsDecidedOnTheRecordsOverrideFlag(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		if durableAttempt(&attemptRecord{Runner: name}) {
			t.Errorf("the %s runner is not durable", name)
		}
	}
	if durableAttempt(&attemptRecord{Runner: "no-such-runner"}) {
		t.Error("an unknown runner is not durable")
	}
	// The durable runner is durable by name, with the argv the supervisor
	// runs recorded beside it — the shape the defect hid in.
	durable := &attemptRecord{Runner: "pi", RunnerArgv: []string{"node", "main.ts"}}
	if !durableAttempt(durable) {
		t.Error("the durable runner with its resolved argv recorded is not durable")
	}
}

// pi-durable opens its SQLite with journal_mode=WAL (wal_autocheckpoint=1000),
// so a live conversation's commits land in worker.sqlite-wal and the main
// file's mtime stays put until a checkpoint (tick lmt): a worker that has
// been thinking or reading for a long time must not read as quiet because
// only the main file was asked. The signal is the newer of the two files.
func TestTheStorageSignalCountsTheWalCommitsBesideTheMainFile(t *testing.T) {
	state := t.TempDir()
	storage := filepath.Join(state, fileWorkerStorage)
	wal := storage + "-wal"
	setMtime := func(path string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}

	checkedOut := time.Now().Add(-time.Hour)  // the last checkpoint, long ago
	streaming := time.Now().Add(-time.Minute) // commits going to the WAL

	// A quiesced database: only the main file, checkpointed long ago. Its
	// mtime is the signal, as before.
	setMtime(storage, checkedOut)
	ev, ok := LastStorageEvent(state)
	if !ok {
		t.Fatal("no event with the storage present")
	}
	if !ev.At.Equal(checkedOut) || ev.Path != storage {
		t.Errorf("event = %s @ %s, want the main file %s @ %s", ev.Path, ev.At, storage, checkedOut)
	}

	// The worker streams: commits land in the WAL, the main file stays put.
	// The WAL's mtime must be the signal, or the worker reads as quiet for
	// the whole window and is nudged and stopped while it works.
	setMtime(wal, streaming)
	ev, ok = LastStorageEvent(state)
	if !ok {
		t.Fatal("no event with the WAL present")
	}
	if !ev.At.Equal(streaming) {
		t.Errorf("event at %s, want the WAL's mtime %s: a streaming worker is not quiet", ev.At, streaming)
	}
	if ev.Path != wal {
		t.Errorf("event path = %s, want the WAL %s: the evidence must name the file that moved", ev.Path, wal)
	}

	// A checkpoint between rounds moves the main file past the WAL: the
	// newer of the two is the signal, whichever wrote it.
	afterCheckpoint := time.Now()
	setMtime(storage, afterCheckpoint)
	ev, ok = LastStorageEvent(state)
	if !ok {
		t.Fatal("no event after the checkpoint")
	}
	if !ev.At.Equal(afterCheckpoint) || ev.Path != storage {
		t.Errorf("event = %s @ %s, want the main file %s @ %s after the checkpoint", ev.Path, ev.At, storage, afterCheckpoint)
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

// superviseSlowStartArg is the helper mode: Supervise, with every runner held
// for slowStartHold after it starts and before its supervisor first looks at
// it — where a loaded host held tick onv's gate: the fsynced pid file and the
// announcements between the runner's start and the watch loop.
const superviseSlowStartArg = "__supervise_slow_start__"

const slowStartHold = 2 * time.Second

func superviseSlowStart(args []string) int {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state", "", "")
	if err := fs.Parse(args); err != nil || *state == "" {
		fmt.Fprintf(os.Stderr, "supervise helper: --state is required\n")
		return 2
	}
	runnerStarted = func([]string) { time.Sleep(slowStartHold) }
	if err := Supervise(*state); err != nil {
		fmt.Fprintf(os.Stderr, "supervise helper: %v\n", err)
		return 1
	}
	return 0
}

// The same runner, with its supervisor held past the 1.5s window between the
// runner's start and the first look — tick onv's gate, made to happen every
// time. The watch's baseline was dated before the hold, so the first look
// read "quiet for the window" over tool CPU it had never watched and nudged
// at once, 2s into the 4s busy phase. And the re-prompted runner, which
// reports and exits during its own hold, was looked at once more before its
// exit was collected — select picks among ready cases at random — and nudged
// as stuck a second time.
func TestASlowStartingSupervisorDoesNotNudgeABusyRunner(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "busy_then_hang", stuckAfter: 1500 * time.Millisecond,
		env: []string{"FAKE_RUNNER_BUSY=4"}, supervisorArgv: []string{os.Args[0], superviseSlowStartArg}})
	handle := f.Start(f.spec("run-onv/tick-slw/attempt-1", "slw"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	if collected := f.collect(handle); collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s, want ready-to-merge\n%s", collected.Verdict, formatObservations(status.Observations))
	}
	var nudges, restarts int
	var nudgedAt, startedAt time.Time
	for _, o := range status.Observations {
		switch {
		case IsStuckNudge(o):
			nudges++
			nudgedAt, _ = time.Parse(time.RFC3339, o.At)
		case o.Kind == ObsStarted && strings.Contains(o.Detail, "re-prompted as stuck"):
			restarts++
		case o.Kind == ObsStarted && startedAt.IsZero():
			startedAt, _ = time.Parse(time.RFC3339, o.At)
		}
	}
	if nudges != 1 || restarts != 1 {
		t.Fatalf("stuck nudges %d, restarts %d, want 1 and 1:\n%s", nudges, restarts, formatObservations(status.Observations))
	}
	// The start is announced after the hold, so the busy phase has 2s of its
	// 4s left when the watch begins; a nudge at the first look is 0-1s after
	// the announcement (the stamps are whole seconds), one after the tool
	// went quiet 3-4s.
	if nudgedAt.Sub(startedAt) < 2*time.Second {
		t.Errorf("the runner was nudged %s after its start was announced, inside its busy phase:\n%s",
			nudgedAt.Sub(startedAt), formatObservations(status.Observations))
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
// doing something — and the LAST tool call as one bounded line, read from
// Claude Code's layout and block spellings — the one harness that still
// writes a session transcript since the pi CLI went (epic 43y, tick uxi).
func TestReadTranscriptEventsAnswersTheWindowAndTheLastToolCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvTranscriptHome, home)
	cwd := t.TempDir()

	writeTranscript(t, "claude", cwd,
		map[string]any{"type": "session", "timestamp": "2026-09-28T06:43:07.734Z"},
		map[string]any{"type": "assistant", "timestamp": "2026-09-28T06:44:00.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "bash", "input": map[string]any{"command": "ls -la"}},
				map[string]any{"type": "tool_use", "name": "bash",
					"input": map[string]any{"command": "go test ./internal/reconcile"}},
			}}})
	events, ok := ReadTranscriptEvents(home, "claude", cwd)
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
	writeTranscript(t, "claude", cwd,
		map[string]any{"type": "assistant", "timestamp": "2026-09-28T09:00:00.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "bash", "input": map[string]any{"command": long}},
			}}})
	events, _ = ReadTranscriptEvents(home, "claude", cwd)
	if len([]rune(events.LastToolCall)) > 80 {
		t.Errorf("the last tool call line is %d runes, want it bounded to 80", len([]rune(events.LastToolCall)))
	}
	if !strings.HasPrefix(events.LastToolCall, "bash: ") || !strings.HasSuffix(events.LastToolCall, "…") {
		t.Errorf("the bounded line %q lost the tool's name or the cut's ellipsis", events.LastToolCall)
	}

	// A tool call whose first argument is not text (a list) carries the tool's
	// name alone: never a guessed argument.
	writeTranscript(t, "claude", cwd,
		map[string]any{"type": "assistant", "timestamp": "2026-09-28T09:01:00.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "todoWrite",
					"input": map[string]any{"todos": []any{map[string]any{"id": "1"}}}},
			}}})
	events, _ = ReadTranscriptEvents(home, "claude", cwd)
	if events.LastToolCall != "todoWrite" {
		t.Errorf("the last tool call is %q, want the tool's name alone for a non-text first argument", events.LastToolCall)
	}

	// Nothing to read: a working directory with no session at all.
	if _, ok := ReadTranscriptEvents(home, "claude", t.TempDir()); ok {
		t.Error("a worktree with no transcript answered events")
	}
}

// The LAST tool call line is what the dashboard renders and the phone
// snapshot ships off-host (tick ghh): a command's first argument can carry
// a credential — an exported token, an Authorization header, a URL with a
// secret in it — and the line a person reads must not state it. Every shape
// a worker plausibly types is redacted to <redacted> at the reader, the one
// producer every renderer reads, and the redaction happens BEFORE the
// 80-rune bound so a cut line can never carry half a secret.
func TestReadTranscriptEventsRedactsCredentialsFromTheLastToolCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvTranscriptHome, home)
	cwd := t.TempDir()

	cases := []struct {
		command string
		want    string
	}{
		{
			// An inline assignment in front of the command, unquoted.
			command: "GH_TOKEN=ghp_0123456789abcdefghijklmnopqrstuv go test ./internal/reconcile",
			want:    "bash: GH_TOKEN=<redacted> go test ./internal/reconcile",
		},
		{
			// An exported variable, quoted value.
			command: `export ANTHROPIC_AUTH_TOKEN="sk-ant-api03-0123456789abcdefghijklmnopqrstuvwxyz"`,
			want:    "bash: export ANTHROPIC_AUTH_TOKEN=<redacted>",
		},
		{
			// An Authorization header, bearer scheme spelled or not.
			command: `curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9abcdef" https://example.com/v1`,
			want:    `bash: curl -H "Authorization: <redacted>" https://example.com/v1`,
		},
		{
			// A URL query parameter named for a credential.
			command: "curl 'https://api.example.com/v1/messages?api_key=0123456789abcdef&other=1'",
			want:    "bash: curl 'https://api.example.com/v1/messages?api_key=<redacted>&other=1'",
		},
		{
			// Userinfo with a password in it.
			command: "git clone https://operator:hunter2@example.com/ticfac/ticfac.git",
			want:    "bash: git clone https://<redacted>@example.com/ticfac/ticfac.git",
		},
		{
			// A flag whose value is a token, space form; and a known token
			// literal anywhere in the line.
			command: "gh auth login --with-token ghp_0123456789abcdefghijklmnopqrstuv",
			want:    "bash: gh auth login --with-token <redacted>",
		},
		{
			// Redaction happens before the bound: the token's 200 runes must
			// not push the tail out of the line, and no cut may carry them.
			command: "MY_API_KEY=" + strings.Repeat("k", 200) + " echo done",
			want:    "bash: MY_API_KEY=<redacted> echo done",
		},
		{
			// A named credential nested inside another assignment's value —
			// the value is rescanned, never left as someone else's value.
			command: "kubectl create secret generic s --from-literal=password=hunter2",
			want:    "bash: kubectl create secret generic s --from-literal=password=<redacted>",
		},
		{
			// curl's -u user:password — a credential whose flag says nothing.
			command: "curl -u admin:hunter2 https://example.com",
			want:    "bash: curl -u <redacted> https://example.com",
		},
		{
			// The redaction is about credentials, not about every value: an
			// ordinary command line states itself whole — a bare --key names a
			// sort key as often as a credential, and a variable whose name
			// merely mentions one stays readable.
			command: "npx vitest run status-model --key nonexistent",
			want:    "bash: npx vitest run status-model --key nonexistent",
		},
		{
			command: "go test ./internal/reconcile -count=1",
			want:    "bash: go test ./internal/reconcile -count=1",
		},
	}
	for i, c := range cases {
		writeTranscript(t, "claude", cwd,
			map[string]any{"type": "assistant", "timestamp": "2026-10-04T09:00:00.000Z",
				"message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "name": "bash",
						"input": map[string]any{"command": c.command}},
				}}})
		events, ok := ReadTranscriptEvents(home, "claude", cwd)
		if !ok {
			t.Fatalf("case %d: the transcript stands and the tail reader answered nothing", i)
		}
		if events.LastToolCall != c.want {
			t.Errorf("case %d: the last tool call is %q, want %q — a credential reached the dashboard line",
				i, events.LastToolCall, c.want)
		}
	}

	// Claude Code's own spelling answers through the same redaction: the seam
	// is the line the reader builds, not one harness's blocks.
	writeTranscript(t, "claude", cwd,
		map[string]any{"type": "assistant", "timestamp": "2026-10-04T09:01:00.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash",
					"input": map[string]any{"command": "GITHUB_TOKEN=gho_0123456789abcdefghijklmnopqrstu gh pr view 1"}},
			}}})
	events, ok := ReadTranscriptEvents(home, "claude", cwd)
	if !ok || events.LastToolCall != "Bash: GITHUB_TOKEN=<redacted> gh pr view 1" {
		t.Errorf("the claude tail read %q (ok %t), want the tool_use line with the credential redacted",
			events.LastToolCall, ok)
	}
}
