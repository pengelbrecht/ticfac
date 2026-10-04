package cli

// The dashboard over an epic's WHOLE state (hn6, tick gmo; re-cut to Build's
// own answer by tick qwb): two earlier runs closed ticks, a duplicate
// promotion stands closed, a held tick stays held, and the newest run failed
// at boot before touching anything. The frame must show the closed ticks
// with the last run's tier, time and attempts, count them in the progress
// bar, dim the duplicate row with the tick the work belongs to — and say in
// the header that the newest run failed, because the run section is the
// newest run's even when the epic's rows are not.
//
// The fixture is the SAME story the model suite builds
// (internal/statusmodel/build_epic_test.go), fed through statusmodel.Build:
// a renderer test cannot import another package's test fixtures, so the
// sources are mirrored here shape for shape — and the golden is rendered
// from Build's own answer, never hand-assembled beside it, because the
// first hand-built model answered "needs you: nothing" beside the hold run
// bbb left on at3 and pinned that lie byte for byte (tick qwb): whatever
// the model derives, the golden renders.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// epicTestNow mirrors the model suite's own clock (statusmodel's testNow):
// the two fixtures must generate the same model, so they freeze the same now.
var epicTestNow = time.Date(2026, 9, 27, 5, 30, 0, 0, time.UTC)

// epicAttemptMarker mirrors the model suite's attemptMarker: the minimal
// durable dispatch marker, with the fields the model reads — tier, model,
// executor — spelled the same way so both suites build one answer.
func epicAttemptMarker(n int, tickID, at, tier, model, executor string) runstate.Attempt {
	return runstate.Attempt{
		SchemaVersion: runstate.SchemaVersion,
		Attempt:       n,
		TickID:        tickID,
		DispatchedAt:  at,
		JobHandle:     map[string]any{"executor": executor},
		Provenance: runstate.Provenance{
			RunID:     "epic-2jn",
			TickID:    &tickID,
			Attempt:   &n,
			SourceRef: "refs/heads/epic/2jn",
			SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
			Phase:     runstate.PhaseWorker,
			Executor:  &executor,
			Role:      ptr("implement-tick"),
			Tier:      &tier,
			Model:     &model,
		},
	}
}

// epicEvidence mirrors the model suite's evidence helper: one gate's own
// record on the head it passed, with the span the row's TIME column reads.
func epicEvidence(key, check, tickID string, attempt int, result, phase, sourceSHA, startedAt, finishedAt string) runstate.Evidence {
	return runstate.Evidence{
		SchemaVersion: runstate.SchemaVersion,
		Key:           key,
		Provenance: runstate.Provenance{
			RunID:          "epic-2jn",
			TickID:         &tickID,
			Attempt:        &attempt,
			SourceRef:      "refs/heads/epic/2jn",
			SourceSHA:      sourceSHA,
			IntegrationRef: ptr("refs/heads/epic/2jn"),
			Phase:          runstate.Phase(phase),
			Executor:       ptr("local-subprocess"),
			Tier:           ptr("strong"),
		},
		Check:      runstate.Check{ID: check, Kind: "command"},
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Result:     result,
		Acceptance: "required",
		Output: runstate.Output{Inline: &runstate.InlineOutput{
			Mode: "inline", Stdout: "ok\n", Truncated: false, Redacted: true, MaxBytes: 1024,
		}},
	}
}

// epicStateSources is the epic across runs, as the model's own suite states
// it (build_epic_test.go): the tracker's graph with the closed tasks and the
// duplicate promotion, the two earlier runs' records and feeds — run bbb
// held at3 for a person and nobody answered — and the newest run, which
// failed at boot before touching anything, seeding every planned tick
// "ready" and carrying one standing dispatch.
func epicStateSources() statusmodel.Sources {
	dupNote := "2026-10-01 04:52 - ticfac run run_aaa: created by absorbing finding f519\n" +
		"2026-10-01 06:47 - ticfac run run_bbb: closed as a duplicate of at2. Both were promoted from " +
		"finding f519 — at2 by run run_aaa, this one by run run_bbb — and a finding is one tick, so the " +
		"later promotion is closed; the work is t2's"
	graph := &tk.Graph{
		Epic: tk.GraphEpic{ID: "hpd", Title: "the epic the runs worked"},
		Waves: []tk.GraphWave{
			{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "at1", Title: "the first tick", Gloss: "first", Status: "closed",
					ClosedAt: "2026-09-26T12:00:00Z"},
				{ID: "at2", Title: "the second tick", Gloss: "second", Status: "closed",
					ClosedAt: "2026-09-27T09:00:00Z"},
				{ID: "at3", Title: "the held tick", Gloss: "third", Status: "open"},
				{ID: "dup", Title: "the duplicate promotion", Gloss: "dupe", Status: "closed", Notes: dupNote},
			}},
			{Wave: 2, Tasks: []tk.GraphTask{
				{ID: "at4", Title: "the upcoming tick", Gloss: "fourth", Status: "open"},
				{ID: "at5", Title: "the third closed tick", Gloss: "fifth", Status: "closed",
					ClosedAt: "2026-09-27T09:40:00Z"},
			}},
		},
	}
	runA := statusmodel.Records{
		Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion,
			RunID:         "run_aaa",
			EpicID:        "hpd",
			Sequence:      4,
			State:         "completed",
			Reason:        "t2 closed",
			UpdatedAt:     "2026-09-26T12:10:00Z",
			Ticks:         []runstate.TickState{{TickID: "at1", State: "closed", Attempt: 1}},
		},
		Attempts: []runstate.Attempt{
			epicAttemptMarker(1, "at1", "2026-09-26T10:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
			epicAttemptMarker(2, "at2", "2026-09-26T10:30:00Z", "strong", "claude-opus-5", "local-subprocess"),
		},
		Evidence: []runstate.Evidence{
			epicEvidence("gate-t1-1-go", "go", "at1", 1, "pass", "integrated", "0fc09212",
				"2026-09-26T11:30:00Z", "2026-09-26T11:55:00Z"),
		},
	}
	runB := statusmodel.Records{
		Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion,
			RunID:         "run_bbb",
			EpicID:        "hpd",
			Sequence:      12,
			State:         "failed",
			Reason:        "the container was evicted mid-run",
			UpdatedAt:     "2026-09-27T09:20:00Z",
			Ticks: []runstate.TickState{
				{TickID: "at2", State: "closed", Attempt: 2},
				{TickID: "at3", State: "rejected", Attempt: 12},
				{TickID: "at5", State: "closed", Attempt: 3},
			},
		},
		Attempts: []runstate.Attempt{
			epicAttemptMarker(1, "at2", "2026-09-27T06:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
			epicAttemptMarker(2, "at2", "2026-09-27T07:00:00Z", "frontier", "claude-opus-5", "local-subprocess"),
			epicAttemptMarker(12, "at3", "2026-09-27T08:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
			epicAttemptMarker(3, "at5", "2026-09-27T09:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
		},
		Evidence: []runstate.Evidence{
			epicEvidence("gate-t2-2-go", "go", "at2", 2, "pass", "integrated", "0fc09212",
				"2026-09-27T08:30:00Z", "2026-09-27T08:55:00Z"),
			epicEvidence("gate-t5-3-go", "go", "at5", 3, "pass", "integrated", "0fc09212",
				"2026-09-27T09:10:00Z", "2026-09-27T09:35:00Z"),
		},
	}
	priorFeeds := map[string][]runfeed.Event{
		"run_aaa": {
			{SchemaVersion: 1, At: "2026-09-26T11:00:00Z", RunID: "run_aaa",
				TickID: ptr("at2"), Attempt: ptr(2), Stage: reconcile.StageRunHeld,
				Detail: reconcile.RefusedRejectedWork + ": attempt 2 of at2 was rejected with commits nothing merged"},
			{SchemaVersion: 1, At: "2026-09-26T11:20:00Z", RunID: "run_aaa", TickID: nil, Attempt: nil,
				Stage: reconcile.StageResumed, Detail: "resumed after the person released the attempt"},
		},
		"run_bbb": {
			{SchemaVersion: 1, At: "2026-09-27T08:10:00Z", RunID: "run_bbb",
				TickID: ptr("at3"), Attempt: ptr(12), Stage: reconcile.StageRunHeld,
				Detail: reconcile.RefusedRejectedWork + ": attempt 12 of at3 was rejected with commits nothing merged"},
		},
	}
	now := epicTestNow.Add(48 * time.Hour)
	records := &statusmodel.Records{
		Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion,
			RunID:         "run_ccc",
			EpicID:        "hpd",
			Sequence:      2,
			State:         "failed",
			Reason:        "failed: the orchestrator could not claim the epic's width",
			UpdatedAt:     now.Add(-30 * time.Minute).Format(time.RFC3339),
			Ticks: []runstate.TickState{
				{TickID: "at1", State: "ready"},
				{TickID: "at2", State: "ready"},
				{TickID: "at3", State: "ready"},
				{TickID: "dup", State: "ready"},
				{TickID: "at4", State: "dispatched", Attempt: 1},
			},
		},
		Attempts: []runstate.Attempt{
			epicAttemptMarker(1, "at4", now.Add(-60*time.Minute).Format(time.RFC3339),
				"strong", "@cf/zai-org/glm-5.3", "cloudflare-sandbox"),
		},
	}
	idle := runprogress.Duration(5 * time.Minute)
	standing := []runprogress.Attempt{
		{
			TickID: "at4", Attempt: 1,
			Branch:     "refs/heads/ticfac/run-run_ccc/tick-t4/attempt-1",
			Worktree:   "/worktrees/run_ccc/tick-t4/attempt-1",
			BranchIdle: &idle, WorktreeIdle: &idle,
		},
	}
	feed := []runfeed.Event{
		{SchemaVersion: 1, At: now.Add(-90 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_ccc",
			Stage: "run_started", Detail: "the run started"},
		{SchemaVersion: 1, At: now.Add(-60 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_ccc",
			TickID: ptr("at4"), Attempt: ptr(1), Stage: "dispatched", Detail: "at4 try 1 dispatched"},
		{SchemaVersion: 1, At: now.Add(-30 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_ccc",
			Stage: "run_finished", Detail: "failed: the orchestrator could not claim the epic's width"},
	}
	return statusmodel.Sources{
		Now:          now,
		RunID:        "run_ccc",
		Host:         statusmodel.HostCloud,
		Graph:        graph,
		Records:      records,
		PriorRecords: []statusmodel.Records{runA, runB},
		PriorFeeds:   priorFeeds,
		Feed:         feed,
		Standing:     standing,
		StandingRead: true,
		Liveness: statusmodel.LivenessInput{
			Alive: false, State: "failed",
			Reason: "the Workflow's own record says failed, so the run has ended",
			Source: "workflow-record",
		},
	}
}

// epicStateFixture is the epic across runs, as the MODEL derives it for the
// newest run's frame — Build's own answer on the story the model suite
// pins, so the golden below can only state what the model states.
func epicStateFixture() statusmodel.Model {
	return statusmodel.Build(epicStateSources())
}

// TestDashboardEpicStateGolden: the dashboard over the fixture epic's whole
// state renders at width 120 byte for byte against the pinned testdata file
// (epic hn6, tick gmo). The frame is the acceptance's renderer half: the
// closed ticks the two earlier runs closed carry their rows, the progress
// bar counts them, the duplicate stands dimmed, and the header says the
// newest run failed.
func TestDashboardEpicStateGolden(t *testing.T) {
	t.Parallel()
	m := epicStateFixture()
	frame := renderWatchFrame(m, plainStyles(), 120, 0, "")
	got := strings.Join(frame, "\n") + "\n"
	path := filepath.Join("testdata", "watch_dashboard_epic_state.txt")
	if *updateGoldens {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (regenerate with -update): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("the epic-state dashboard does not match %s byte for byte:\n--- got ---\n%s\n--- want ---\n%s",
			path, got, want)
	}
}

// TestDashboardEpicStateAnnouncesTheHold (tick qwb): the story carries a
// hold run bbb left on at3 that nobody answered — the model's own Build
// yields an attention entry for it — so the frame can never answer
// "needs you: nothing" beside it. P2: a hold shows in the header with the
// one command that clears it, and the quiet answer belongs to the frames
// where it is true. The hand-built fixture this test replaced answered
// quiet beside the hold and pinned it; rendered from Build's output, the
// golden cannot lie about what the model states.
func TestDashboardEpicStateAnnouncesTheHold(t *testing.T) {
	t.Parallel()
	m := epicStateFixture()
	if len(m.Attention) == 0 {
		t.Fatalf("the fixture's model states no attention entry beside run bbb's hold on at3: %+v", m.WaitsOn)
	}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "needs you: nothing") {
		t.Errorf("the frame answers nothing-needs-you beside run bbb's standing hold on at3:\n%s", joined)
	}
	for _, want := range []string{
		"needs you: run run_bbb held at3 for a person",
		`ticfac settle hpd at3 12 --run-id run_bbb --release "<who>"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the frame does not carry %q:\n%s", want, joined)
		}
	}
}

// TestDashboardDuplicateRowIsDimmedAndNamed: a duplicate keeps its row (rows
// never move) but reads dim, with the tick its work belongs to in the WHAT
// column — the fact a person reads without drilling in. A plain tick is
// never dimmed for its state.
func TestDashboardDuplicateRowIsDimmedAndNamed(t *testing.T) {
	t.Parallel()
	m := epicStateFixture()
	frame := renderWatchFrame(m, plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "dup   duplicate of at2") {
		t.Errorf("the duplicate row does not name the tick the work belongs to:\n%s", joined)
	}
	coloured := renderWatchFrame(m, ansiWatchStyles(), 0, 0, "")
	var dupLine string
	for _, line := range coloured {
		if strings.Contains(line, "duplicate of at2") {
			dupLine = line
			break
		}
	}
	if dupLine == "" {
		t.Fatalf("the duplicate row is missing:\n%s", strings.Join(coloured, "\n"))
	}
	if !strings.HasPrefix(dupLine, "\x1b[2m") || !strings.HasSuffix(dupLine, "\x1b[0m") {
		t.Errorf("the duplicate row is not dimmed whole: %q", dupLine)
	}
	// The closed neighbour beside it is not dimmed: dimming is the
	// duplicate's own marker, not a rendering of closed.
	for _, line := range coloured {
		if strings.Contains(line, " at1 ") && strings.Contains(line, "merged") {
			if strings.HasPrefix(line, "\x1b[2m") {
				t.Errorf("the closed tick at1's row is dimmed like a duplicate: %q", line)
			}
			break
		}
	}
}
