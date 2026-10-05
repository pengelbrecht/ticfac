package statusmodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The per-worker activity window (epic hn6, wave 2 — tick ltg): the workers
// panel's sparkline, its last tool action and the stuck nudges the run sent,
// read from the runner's own session transcript and the feed's typed lines.
// Everything here is headless: a fake home the transcript fixture writes
// under, no herdr, no real ~/.pi — the transcript is the runner's own file
// shape (pi's, and Claude Code's for the claude mapping) placed where the
// harness keeps it.

// writeSessionTranscript writes one dated line into the transcript directory
// the given harness kind keeps for a working directory, under home — the
// same placement writeTranscript in internal/exec/subprocess pins, stated
// here so this package's fixtures cannot drift from the layout the reader
// walks.
func writeSessionTranscript(t *testing.T, home, kind, cwd string, lines ...map[string]any) {
	t.Helper()
	t.Setenv(subprocess.EnvTranscriptHome, home)
	dir := subprocess.TranscriptDir(kind, cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, line := range lines {
		raw, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// transcriptStamp renders one fixture stamp the way the harnesses write
// theirs, so the reader parses exactly what production reads.
func transcriptStamp(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}

// TestActivityBucketsTranscriptEvents: the production reader answers a
// worker's moments from its runner's own transcript, and the model buckets
// them into the window it states — ten one-minute counts of the events in
// (now-600s, now], oldest first, with the last tool call as the one-line
// action a person reads beside the sparkline.
func TestActivityBucketsTranscriptEvents(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	// A pi transcript for the standing attempt's worktree: events at
	// now-700s (outside the window), now-95s and now-90s (the ninth-oldest
	// minute bucket) and now-30s (the newest), the last one a tool call.
	writeSessionTranscript(t, home, "pi", worktree,
		map[string]any{"type": "session", "timestamp": transcriptStamp(testNow.Add(-700 * time.Second))},
		map[string]any{"type": "message", "timestamp": transcriptStamp(testNow.Add(-95 * time.Second)),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "toolCall", "name": "bash", "arguments": map[string]any{"command": "ls -la"}},
			}}},
		map[string]any{"type": "message", "timestamp": transcriptStamp(testNow.Add(-90 * time.Second)),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "reading the layout"},
			}}},
		map[string]any{"type": "message", "timestamp": transcriptStamp(testNow.Add(-30 * time.Second)),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "toolCall", "name": "bash",
					"arguments": map[string]any{"command": "go test ./internal/reconcile"}},
			}}},
	)

	src := runningEpicSources()
	src.Activity = TranscriptActivity(home)
	src.Standing[0].Worktree = worktree
	model := Build(src)

	if model.Workers == nil || len(*model.Workers) != 1 {
		t.Fatalf("the census read one standing attempt and the model says %+v", model.Workers)
	}
	activity := (*model.Workers)[0].Activity
	if activity == nil {
		t.Fatal("the worker carried a transcript and its activity reads null")
	}
	if activity.WindowSeconds != 600 {
		t.Errorf("the activity window is %ds, want the 600s the model states", activity.WindowSeconds)
	}
	want := []int{0, 0, 0, 0, 0, 0, 0, 0, 2, 1}
	if len(activity.Buckets) != len(want) {
		t.Fatalf("the activity carries %d buckets, want %d one-minute counts", len(activity.Buckets), len(want))
	}
	for i := range want {
		if activity.Buckets[i] != want[i] {
			t.Errorf("bucket %d counts %d events, want %d (oldest first; -95s and -90s in bucket 8, -30s in bucket 9, -700s outside the window)",
				i, activity.Buckets[i], want[i])
		}
	}
	if activity.LastAction == nil || *activity.LastAction != "bash: go test ./internal/reconcile" {
		t.Errorf("the last action is %+v, want the LAST tool call's own name and first argument", activity.LastAction)
	}
	wantAt := testNow.Add(-30 * time.Second).UTC().Format(time.RFC3339)
	if activity.LastActionAt == nil || *activity.LastActionAt != wantAt {
		t.Errorf("the last action's stamp is %+v, want the tool call's own %s", activity.LastActionAt, wantAt)
	}
	if activity.Nudges != 0 {
		t.Errorf("the run sent %d stuck nudges, want none in this feed", activity.Nudges)
	}
}

// TestActivityRedactsTheLastAction: the last_action the model carries is
// rendered on the dashboard and shipped off-host with the phone snapshot,
// so a credential in the tool call's argument must not survive the read
// (tick ghh). The redaction is the transcript reader's own — this test
// holds the whole pipeline to it: transcript → model.
func TestActivityRedactsTheLastAction(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	writeSessionTranscript(t, home, "pi", worktree,
		map[string]any{"type": "message", "timestamp": transcriptStamp(testNow.Add(-30 * time.Second)),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "toolCall", "name": "bash",
					"arguments": map[string]any{"command": "GH_TOKEN=ghp_0123456789abcdefghijklmnopqrstuv go test ./internal/reconcile"}},
			}}})

	src := runningEpicSources()
	src.Activity = TranscriptActivity(home)
	src.Standing[0].Worktree = worktree
	model := Build(src)

	if model.Workers == nil || len(*model.Workers) != 1 || (*model.Workers)[0].Activity == nil {
		t.Fatalf("the census read one standing attempt with a transcript, the model says %+v", model.Workers)
	}
	activity := (*model.Workers)[0].Activity
	if activity.LastAction == nil {
		t.Fatal("the worker's transcript carries a tool call and its last action reads null")
	}
	if strings.Contains(*activity.LastAction, "ghp_0123456789") {
		t.Errorf("the last action %q carries the token whole — the model ships off-host", *activity.LastAction)
	}
	if want := "bash: GH_TOKEN=<redacted> go test ./internal/reconcile"; *activity.LastAction != want {
		t.Errorf("the last action is %q, want %q", *activity.LastAction, want)
	}
}

// TestActivityCountsNudgesWithoutATranscript: the nudge count is the feed's
// own typed lines, counted for the worker whether or not a transcript could
// be read — a worker the run nudged but whose harness keeps nothing this
// machine can read still shows the nudge, with empty buckets rather than a
// guessed window.
func TestActivityCountsNudgesWithoutATranscript(t *testing.T) {
	home := t.TempDir() // no transcript under it anywhere
	src := runningEpicSources()
	src.Activity = TranscriptActivity(home)
	three := 3
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-5*time.Minute), "epic-2jn", "6dh", &three,
		reconcile.StageStuckNudged, "6dh try 2 appears stuck: no activity for 15m — it was nudged in its own session"))
	model := Build(src)

	activity := (*model.Workers)[0].Activity
	if activity == nil {
		t.Fatal("the run nudged this worker once and its activity reads null: the nudge is a fact the panel must show")
	}
	if activity.Nudges != 1 {
		t.Errorf("the worker was nudged %d times, want the feed's one stuck_nudged line for its (tick, attempt)", activity.Nudges)
	}
	if activity.WindowSeconds != 600 {
		t.Errorf("the activity window is %ds, want the 600s the model states even with nothing measured", activity.WindowSeconds)
	}
	if len(activity.Buckets) != 0 {
		t.Errorf("a worker with no transcript carries buckets %v, want the empty list: nothing was measured", activity.Buckets)
	}
	if activity.LastAction != nil || activity.LastActionAt != nil {
		t.Errorf("a worker with no transcript carries a last action (%v at %v), want null", activity.LastAction, activity.LastActionAt)
	}
}

// TestActivityIsNullWithNothingToSay: a worker nothing measured and nobody
// nudged states null activity and no handle — the honest not-measured, not
// a zeroed shape that renders as "quiet since forever".
func TestActivityIsNullWithNothingToSay(t *testing.T) {
	src := runningEpicSources()
	src.Activity = TranscriptActivity(t.TempDir())
	model := Build(src)

	worker := (*model.Workers)[0]
	if worker.Activity != nil {
		t.Errorf("the worker's activity is %+v, want null: no transcript, no nudges", *worker.Activity)
	}
	if worker.Handle != nil {
		t.Errorf("the worker's handle is %q, want null: the attempt record's job handle names no agent, pane or name",
			*worker.Handle)
	}
}

// TestTheHandleReaderNamesTheWorkerTheMarkerCannot (zl1): the attempt
// marker's job handle never carries a worker's name — it is cut before the
// start, and its existence is the dispatch's compare-and-swap — so the
// panel's handle comes from the injected reader, the executor's own record
// on this machine. The reader's answer wins over the durable record's copy
// (it is the LIVE worker the panel points a person at, and the record is the
// frozen dispatch-time one), and the record still answers where the reader
// says nothing: a record that names the worker is read, never ignored.
func TestTheHandleReaderNamesTheWorkerTheMarkerCannot(t *testing.T) {
	durable := map[string]any{
		"executor": "herdr",
		"handle":   map[string]any{"agent_name": "tick-6dh-a2"},
	}

	// The reader answers: its name for the live worker wins, even over a
	// durable record that spells one.
	src := runningEpicSources()
	src.Activity = nil
	for i, a := range src.Records.Attempts {
		if a.TickID == "6dh" && a.Attempt == 3 {
			src.Records.Attempts[i].JobHandle = durable
		}
	}
	src.Handle = func(tickID string, attempt int) *string {
		name := "tick-6dh-a3"
		return &name
	}
	worker := (*Build(src).Workers)[0]
	if worker.Handle == nil || *worker.Handle != "tick-6dh-a3" {
		t.Errorf("the handle reads %+v, want the reader's name for the live worker", worker.Handle)
	}

	// The reader answers nothing: the durable record's own copy answers.
	src.Handle = func(tickID string, attempt int) *string { return nil }
	worker = (*Build(src).Workers)[0]
	if worker.Handle == nil || *worker.Handle != "tick-6dh-a2" {
		t.Errorf("the handle reads %+v, want the durable record's own name", worker.Handle)
	}

	// No reader at all: the record answers, as before the reader existed.
	src.Handle = nil
	worker = (*Build(src).Workers)[0]
	if worker.Handle == nil || *worker.Handle != "tick-6dh-a2" {
		t.Errorf("the handle reads %+v, want the durable record's own name", worker.Handle)
	}
}

// TestActivityCarriesTheWorkersExecutorHandle: the handle is the executor's
// own name for the worker, read off the attempt record's job handle map —
// a herdr agent name or pane id, wherever the record spells them, and null
// when nothing names the worker (the local-subprocess marker, and the
// cloud marker, carry no name a person can find the worker by).
func TestActivityCarriesTheWorkersExecutorHandle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle map[string]any
		want   string
	}{
		{
			name:   "herdr's agent name, in the nested handle object",
			handle: map[string]any{"executor": "herdr", "handle": map[string]any{"agent_name": "tick-6dh-a3", "pane_id": "p7"}},
			want:   "tick-6dh-a3",
		},
		{
			name:   "herdr's pane id, when no agent name rode the handle",
			handle: map[string]any{"executor": "herdr", "handle": map[string]any{"pane_id": "p7"}},
			want:   "p7",
		},
		{
			name:   "the bare spellings, wherever a record carries them",
			handle: map[string]any{"agent": "ag-1"},
			want:   "ag-1",
		},
		{
			name:   "a pane key",
			handle: map[string]any{"pane": "pane-9"},
			want:   "pane-9",
		},
		{
			name:   "a name key",
			handle: map[string]any{"name": "worker-1"},
			want:   "worker-1",
		},
		{
			name:   "the reconciler's own marker carries no name",
			handle: map[string]any{"executor": "local-subprocess", "write_ref": "refs/heads/ticfac/run-epic-2jn/tick-6dh/attempt-3"},
			want:   "",
		},
	} {
		src := runningEpicSources()
		src.Activity = nil
		for i, a := range src.Records.Attempts {
			if a.TickID == "6dh" && a.Attempt == 3 {
				src.Records.Attempts[i].JobHandle = tc.handle
			}
		}
		model := Build(src)
		worker := (*model.Workers)[0]
		if tc.want == "" {
			if worker.Handle != nil {
				t.Errorf("%s: the handle reads %q, want null", tc.name, *worker.Handle)
			}
			continue
		}
		if worker.Handle == nil || *worker.Handle != tc.want {
			t.Errorf("%s: the handle reads %+v, want %q", tc.name, worker.Handle, tc.want)
		}
	}
}

// TestActivityReadsAClaudeTranscriptByItsKind: the mapping keys on the
// harness KIND the attempt record names — the executor's own record in the
// dispatch's state directory, which spells the agent kind it launched
// (herdr's `kind`, the local supervisor's `runner`), read through the
// Sources.Runner seam. This is the live close-out's own shape (tick 5uq):
// the durable attempt's provenance names model "opus" and executor "herdr"
// — the spellings a real record carries (.ticfac/runs/epic-hn6/attempts/
// 29.json) — neither naming claude, so before the kind was read the
// worker's activity read null: the panel showed an empty sparkline for the
// only worker of the close-out while its Claude Code transcript existed.
func TestActivityReadsAClaudeTranscriptByItsKind(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	writeSessionTranscript(t, home, "claude", worktree,
		map[string]any{"type": "assistant", "timestamp": transcriptStamp(testNow.Add(-20 * time.Second)),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go vet ./..."}},
			}}})

	stateRoot := t.TempDir()
	t.Setenv(EnvExecStateDir, stateRoot)
	// The executor's own attempt record, the shape herdr leaves it in: the
	// kind is the agent kind it launched, spelled as a live record spells
	// it.
	writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 3,
		`{"tick_id": "6dh", "attempt": 3, "agent_name": "tick-6dh-a3", "kind": "claude", "model": "opus"}`)

	src := runningEpicSources()
	src.Activity = TranscriptActivity(home)
	src.Runner = WorkerRunner("epic-2jn")
	src.Standing[0].Worktree = worktree
	// The durable record's own spellings, copied from a real attempt record:
	// the bare alias review and closeout route on, and the executor.
	for i := range src.Records.Attempts {
		if src.Records.Attempts[i].TickID == "6dh" && src.Records.Attempts[i].Attempt == 3 {
			src.Records.Attempts[i].Provenance.Model = tickPtr("opus")
			src.Records.Attempts[i].Provenance.Executor = tickPtr("herdr")
		}
	}
	model := Build(src)

	activity := (*model.Workers)[0].Activity
	if activity == nil {
		t.Fatal("the worker's executor record names the claude kind and its activity reads null: the mapping never fired")
	}
	if activity.LastAction == nil || *activity.LastAction != "Bash: go vet ./..." {
		t.Errorf("the last action is %+v, want the claude tool call's own name and first argument", activity.LastAction)
	}
	if got := activity.Buckets[9]; got != 1 {
		t.Errorf("the newest bucket counts %d events, want the one claude tool call", got)
	}
}

// TestActivityPrefersTheLiveKindOverTheDurableModel: the executor's own
// record is the LIVE word, so its kind wins over the durable record's
// model spelling wherever the two disagree — the same precedence the
// handle seam holds (zl1). A worker whose state record names the claude
// kind reads claude's layout even though its durable model spells a
// provider-qualified id that reads pi's.
func TestActivityPrefersTheLiveKindOverTheDurableModel(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	writeSessionTranscript(t, home, "claude", worktree,
		map[string]any{"type": "assistant", "timestamp": transcriptStamp(testNow.Add(-20 * time.Second)),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go vet ./..."}},
			}}})

	stateRoot := t.TempDir()
	t.Setenv(EnvExecStateDir, stateRoot)
	writeAttemptRecord(t, stateRoot, "epic-2jn", "6dh", 3,
		`{"tick_id": "6dh", "kind": "claude"}`)

	// The durable model keeps the fixture's own spelling — a
	// provider-qualified id whose family reads pi.
	src := runningEpicSources()
	src.Activity = TranscriptActivity(home)
	src.Runner = WorkerRunner("epic-2jn")
	src.Standing[0].Worktree = worktree
	model := Build(src)

	activity := (*model.Workers)[0].Activity
	if activity == nil {
		t.Fatal("the executor's own record names the claude kind and the durable model must not overrule it")
	}
	if activity.LastAction == nil || *activity.LastAction != "Bash: go vet ./..." {
		t.Errorf("the last action is %+v, want the claude tool call the live kind's layout reads", activity.LastAction)
	}
}

// TestActivityReadsAClaudeTranscriptByTheDurableModelsFamily: where no
// executor record answered, the durable attempt's own spelling addresses
// the seam, and a claude-FAMILY model reads claude's layout — the same
// aliases the runner config recognises and the cost river reads (cost.go).
// The bare "opus" is the spelling every real claude attempt record carries
// (.ticfac/runs/epic-hn6/attempts/29.json: provenance.model "opus"), and
// the activity fixture used to spell it "claude-opus-5", a string no real
// record carries — so the mapping never fired for a real record and a live
// claude worker's activity read null (tick 5uq).
func TestActivityReadsAClaudeTranscriptByTheDurableModelsFamily(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	writeSessionTranscript(t, home, "claude", worktree,
		map[string]any{"type": "assistant", "timestamp": transcriptStamp(testNow.Add(-20 * time.Second)),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go vet ./..."}},
			}}})

	src := runningEpicSources()
	src.Activity = TranscriptActivity(home)
	src.Standing[0].Worktree = worktree
	// The durable record's own spelling, copied from a real attempt record:
	// the bare alias the runner config routes review and closeout on.
	for i := range src.Records.Attempts {
		if src.Records.Attempts[i].TickID == "6dh" && src.Records.Attempts[i].Attempt == 3 {
			src.Records.Attempts[i].Provenance.Model = tickPtr("opus")
		}
	}
	model := Build(src)

	activity := (*model.Workers)[0].Activity
	if activity == nil {
		t.Fatal("the durable record names the claude alias opus and the activity reads null: the mapping never fired")
	}
	if activity.LastAction == nil || *activity.LastAction != "Bash: go vet ./..." {
		t.Errorf("the last action is %+v, want the claude tool call's own name and first argument", activity.LastAction)
	}
	if got := activity.Buckets[9]; got != 1 {
		t.Errorf("the newest bucket counts %d events, want the one claude tool call", got)
	}
}
