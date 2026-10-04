package cli

// The dashboard over an epic's WHOLE state (hn6, tick gmo): two earlier runs
// closed ticks, a duplicate promotion stands closed, a held tick stays held,
// and the newest run failed at boot before touching anything. The frame must
// show the closed ticks with the last run's tier, time and attempts, count
// them in the progress bar, dim the duplicate row with the tick the work
// belongs to — and say in the header that the newest run failed, because the
// run section is the newest run's even when the epic's rows are not.
//
// The fixture mirrors the model suite's (internal/statusmodel/
// build_epic_test.go) shape so the two read as one story; the model is built
// by hand here because a renderer test cannot import another package's test
// fixtures, only its answer.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// epicStateFixture is the epic across runs, as the model states it for the
// newest run's frame.
func epicStateFixture() statusmodel.Model {
	strong, frontier := "strong", "frontier"
	t2Attempt, t5Attempt, t4Attempt := 2, 3, 1
	t2Duration, t1Duration := int64(10500), int64(6900)
	t4Live := int64(3600)
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "run_ccc",
		EpicID:        "hpd",
		Host:          statusmodel.HostCloud,
		GeneratedAt:   "2026-09-29T05:30:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: false, State: "failed",
			Reason: "the Workflow's own record says failed, so the run has ended",
			Source: "workflow-record",
			LastEvent: &runfeed.Event{
				SchemaVersion: 1, At: "2026-09-29T05:00:00Z", RunID: "run_ccc",
				Stage:  "run_finished",
				Detail: "failed: the orchestrator could not claim the epic's width",
			},
			LastEventAgeSeconds: ptr(int64(1800)),
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseFailed,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateActive},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStatePending},
			},
			Wave: &statusmodel.WaveRef{Active: 1, Total: 2},
		},
		Progress: statusmodel.Progress{
			Ticks: &statusmodel.TickProgress{Total: 5, Closed: 3, Open: 2},
			Waves: &statusmodel.WaveProgress{Total: 2, Done: 0, Active: 1},
		},
		EpicTitle: ptr("the epic the runs worked"),
		Recent: []runfeed.Event{
			{SchemaVersion: 1, At: "2026-09-29T04:30:00Z", RunID: "run_ccc",
				TickID: ptr("t4"), Attempt: ptr(t4Attempt), Stage: "dispatched",
				Detail: "t4 try 1 dispatched"},
			{SchemaVersion: 1, At: "2026-09-29T05:00:00Z", RunID: "run_ccc",
				Stage:  "run_finished",
				Detail: "failed: the orchestrator could not claim the epic's width"},
		},
		Waves: &[]statusmodel.Wave{
			{Wave: 1, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{
				{
					TickID: "t1", Title: "the first tick", Gloss: "first",
					State: "closed",
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStateDone},
					},
					DurationSeconds: &t1Duration,
					Try:             ptr(1), Attempt: ptr(1),
					Tries: []statusmodel.Try{{
						Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
						DispatchedAt: "2026-09-26T10:00:00Z", Tier: &strong}},
					Tier: &strong,
				},
				{
					TickID: "t2", Title: "the second tick", Gloss: "second",
					State: "closed",
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStateDone},
					},
					DurationSeconds: &t2Duration,
					Try:             ptr(2), Attempt: &t2Attempt,
					Tries: []statusmodel.Try{
						{Try: 1, Attempt: 1, Outcome: statusmodel.TryReported,
							DispatchedAt: "2026-09-27T06:00:00Z", Tier: &strong},
						{Try: 2, Attempt: 2, Outcome: statusmodel.TryClosed,
							DispatchedAt: "2026-09-27T07:00:00Z", Tier: &frontier},
					},
					Tier: &frontier,
				},
				{
					TickID: "t3", Title: "the held tick", Gloss: "third",
					State: "rejected", Attempt: ptr(12), Try: ptr(1),
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateFailed},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
					},
					DurationSeconds: &t4Live,
					Tries: []statusmodel.Try{{
						Try: 1, Attempt: 12, Outcome: statusmodel.TryRejected,
						DispatchedAt: "2026-09-27T08:00:00Z", Tier: &strong,
						Reason: ptr("attempt_held: attempt 12 of t3 is struck out: only a person releases it"),
						// A refusal the failed newest run did not leave: the
						// next step is the resume, the same command the run
						// header names (the model derivation's own word).
						NextStep: ptr("nothing of that run is working — ticfac run hpd --cloud takes it up"),
					}},
					Tier: &strong,
				},
				{
					TickID: "dup", Title: "the duplicate promotion", Gloss: "dupe",
					State: "closed", DuplicateOf: ptr("t2"),
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStateDone},
					},
					Tries: []statusmodel.Try{}, Tier: nil,
				},
			}},
			{Wave: 2, State: statusmodel.WaveUpcoming, Ticks: []statusmodel.Tick{
				{
					TickID: "t4", Title: "the upcoming tick", Gloss: "fourth",
					State: "dispatched", Attempt: &t4Attempt, Try: ptr(1),
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateActive},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
					},
					DurationSeconds: &t4Live,
					Tries: []statusmodel.Try{{
						Try: 1, Attempt: 1, Outcome: statusmodel.TryInFlight,
						DispatchedAt: "2026-09-29T04:30:00Z", Tier: &strong}},
					Tier:           &strong,
					Model:          ptr("@cf/zai-org/glm-5.3"),
					Executor:       ptr("cloudflare-sandbox"),
					ElapsedSeconds: &t4Live,
				},
				{
					TickID: "t5", Title: "the third closed tick", Gloss: "fifth",
					State: "closed", Attempt: &t5Attempt, Try: ptr(1),
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStateDone},
					},
					DurationSeconds: ptr(int64(2100)),
					Tries: []statusmodel.Try{{
						Try: 1, Attempt: 3, Outcome: statusmodel.TryClosed,
						DispatchedAt: "2026-09-27T09:00:00Z", Tier: &strong}},
					Tier: &strong,
				},
			}},
		},
		Workers: &[]statusmodel.Worker{{
			TickID: "t4", Attempt: 1,
			Branch:         "refs/heads/ticfac/run-run_ccc/tick-t4/attempt-1",
			ElapsedSeconds: &t4Live,
		}},
		WaitsOn: &statusmodel.Wait{
			Kind:           statusmodel.WaitDeadRun,
			What:           "run run_ccc is failed: the Workflow's own record says failed, so the run has ended",
			NeedsPerson:    true,
			UnblockCommand: ptr("ticfac run --cloud hpd"),
		},
		Attention: []statusmodel.Attention{},
		Health: statusmodel.Health{
			Verdict: statusmodel.HealthVerdict{State: statusmodel.VerdictStopped,
				Summary: "the Workflow's own record says failed, so the run has ended"},
		},
		Cost: statusmodel.Cost{
			RecordedUSD: 0.06, Attempts: 1,
			Basis: "usage recorded on decision records",
			Lines: []statusmodel.CostLine{
				{Source: statusmodel.CostSourceWorkersAI, Metered: true, USD: ptr(0.06), Attempts: 1,
					Basis: "gateway usage for 1 dispatch"},
			},
		},
	}
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

// TestDashboardDuplicateRowIsDimmedAndNamed: a duplicate keeps its row (rows
// never move) but reads dim, with the tick its work belongs to in the WHAT
// column — the fact a person reads without drilling in. A plain tick is
// never dimmed for its state.
func TestDashboardDuplicateRowIsDimmedAndNamed(t *testing.T) {
	t.Parallel()
	m := epicStateFixture()
	frame := renderWatchFrame(m, plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "dup   duplicate of t2") {
		t.Errorf("the duplicate row does not name the tick the work belongs to:\n%s", joined)
	}
	coloured := renderWatchFrame(m, ansiWatchStyles(), 0, 0, "")
	var dupLine string
	for _, line := range coloured {
		if strings.Contains(line, "duplicate of t2") {
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
		if strings.Contains(line, " t1 ") && strings.Contains(line, "merged") {
			if strings.HasPrefix(line, "\x1b[2m") {
				t.Errorf("the closed tick t1's row is dimmed like a duplicate: %q", line)
			}
			break
		}
	}
}
