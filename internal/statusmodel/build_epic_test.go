package statusmodel

// The epic's state across runs (hn6, tick gmo): the dashboard answers for the
// EPIC, not for the newest run's own checkpoint. Two earlier runs closed
// ticks; the newest run failed at boot before touching anything; the model
// must still show the closed ticks with the last run's tier, pipeline, time
// and attempts, count them in the progress bar, keep the held tick held, and
// say in the run header that the NEWEST run failed — the run section is the
// newest run's alone, and every closed tick's row is the epic's.
//
// The fixture is exactly the shape the operator screenshot caught: the newest
// run's checkpoint carries "ready" rows for ticks earlier runs closed (a
// fresh run seeds its plan before it settles the tracker's answer), which is
// why a naive read of the newest records answered 0/11 with empty rows.

import (
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// epicGraphAcrossRuns is the epic's own layering with closed tasks included:
// the integration branch's tracker says which ticks are closed, and the two
// duplicate promotions the runs closed as duplicates stand in wave 2 with
// their dedup note — the record the model reads to exclude them from the
// progress count.
func epicGraphAcrossRuns() *tk.Graph {
	dupNote := "2026-10-01 04:52 - ticfac run run_aaa: created by absorbing finding f519\n" +
		"2026-10-01 06:47 - ticfac run run_bbb: closed as a duplicate of at2. Both were promoted from " +
		"finding f519 — at2 by run run_aaa, this one by run run_bbb — and a finding is one tick, so the " +
		"later promotion is closed; the work is t2's"
	return &tk.Graph{
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
}

// priorRecordsAcrossRuns is the two earlier runs' records, oldest first: run
// aaa closed t1 and t2 (t2 at a strong tier), run bbb re-closed t2 at a
// frontier tier — the last run that touched it, whose provenance a closed
// row shows — and held t3: its last attempt was rejected for a person, and
// nobody released it.
func priorRecordsAcrossRuns() []Records {
	runA := Records{
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
			attemptMarker(1, "at1", "2026-09-26T10:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
			attemptMarker(2, "at2", "2026-09-26T10:30:00Z", "strong", "claude-opus-5", "local-subprocess"),
		},
		Evidence: []runstate.Evidence{
			evidence("gate-t1-1-go", "go", "at1", 1, "pass", "integrated", "0fc09212",
				"2026-09-26T11:30:00Z", "2026-09-26T11:55:00Z"),
		},
	}
	runB := Records{
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
			attemptMarker(1, "at2", "2026-09-27T06:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
			attemptMarker(2, "at2", "2026-09-27T07:00:00Z", "frontier", "claude-opus-5", "local-subprocess"),
			attemptMarker(12, "at3", "2026-09-27T08:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
			attemptMarker(3, "at5", "2026-09-27T09:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
		},
		Evidence: []runstate.Evidence{
			evidence("gate-t2-2-go", "go", "at2", 2, "pass", "integrated", "0fc09212",
				"2026-09-27T08:30:00Z", "2026-09-27T08:55:00Z"),
			evidence("gate-t5-3-go", "go", "at5", 3, "pass", "integrated", "0fc09212",
				"2026-09-27T09:10:00Z", "2026-09-27T09:35:00Z"),
		},
	}
	return []Records{runA, runB}
}

// failedNewestSources is the newest run: it failed at boot before touching
// anything, so its checkpoint seeds every planned tick "ready" — including
// the two earlier runs closed — and carries one "ready" row for the held
// tick too. Its own dispatch (t4's first attempt) is the only thing it did
// before failing, so the run section still has a worker.
func failedNewestSources() Sources {
	now := testNow.Add(48 * time.Hour)
	records := &Records{
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
			attemptMarker(1, "at4", now.Add(-60*time.Minute).Format(time.RFC3339),
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
	one := 1
	t4 := "at4"
	feed := []runfeed.Event{
		{SchemaVersion: 1, At: now.Add(-90 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_ccc",
			Stage: "run_started", Detail: "the run started"},
		{SchemaVersion: 1, At: now.Add(-60 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_ccc",
			TickID: &t4, Attempt: &one, Stage: "dispatched", Detail: "t4 try 1 dispatched"},
		{SchemaVersion: 1, At: now.Add(-30 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_ccc",
			Stage: "run_finished", Detail: "failed: the orchestrator could not claim the epic's width"},
	}
	return Sources{
		Now:          now,
		RunID:        "run_ccc",
		Host:         HostCloud,
		Graph:        epicGraphAcrossRuns(),
		Records:      records,
		PriorRecords: priorRecordsAcrossRuns(),
		Feed:         feed,
		Standing:     standing,
		StandingRead: true,
		Liveness: LivenessInput{
			Alive: false, State: "failed",
			Reason: "the Workflow's own record says failed, so the run has ended",
			Source: "workflow-record",
		},
	}
}

// TestBuildDerivesTheEpicAcrossRuns: the acceptance, as model tests over the
// fixture. The progress bar and rows reflect the ticks the two earlier runs
// closed; the held tick stays held; the run header says the newest run
// failed; duplicates are excluded from the count; and the run section —
// workers, waits, cost, feed, liveness — stays the newest run's alone.
func TestBuildDerivesTheEpicAcrossRuns(t *testing.T) {
	t.Parallel()
	src := failedNewestSources()
	model := Build(src)

	if model.Waves == nil {
		t.Fatal("the fixture's graph answered and the model states no waves")
	}
	tickByID := map[string]Tick{}
	for _, w := range *model.Waves {
		for _, tick := range w.Ticks {
			tickByID[tick.TickID] = tick
		}
	}

	// The progress bar counts real ticks: t1, t2 and t5 are closed by
	// earlier runs, dup is a duplicate promotion and is not a real tick, t3
	// and t4 are open.
	if model.Progress.Ticks == nil {
		t.Fatal("the graph answered and the progress carries no tick counts")
	}
	if model.Progress.Ticks.Total != 5 {
		t.Errorf("the progress counts %d ticks, want 5 real ones (the duplicate excluded): %+v",
			model.Progress.Ticks.Total, *model.Progress.Ticks)
	}
	if model.Progress.Ticks.Closed != 3 {
		t.Errorf("the progress counts %d closed, want the three ticks the earlier runs closed: %+v",
			model.Progress.Ticks.Closed, *model.Progress.Ticks)
	}
	if model.Progress.Ticks.Open != 2 {
		t.Errorf("the progress counts %d open, want t3 and t4: %+v",
			model.Progress.Ticks.Open, *model.Progress.Ticks)
	}

	// Closed ticks show done, with the LAST run that touched them as their
	// provenance: t2 was re-closed by run bbb at frontier, so its row reads
	// frontier and one try, not run aaa's strong two.
	t2 := tickByID["at2"]
	if t2.State != "closed" {
		t.Errorf("t2 reads state %q, want closed from the records of the run that closed it", t2.State)
	}
	if t2.Tier == nil || *t2.Tier != "frontier" {
		t.Errorf("t2 reads tier %v, want the last run's frontier", t2.Tier)
	}
	if len(t2.Tries) != 2 || t2.Tries[1].Outcome != TryClosed {
		t.Errorf("t2 reads tries %+v, want run bbb's two with the second closed", t2.Tries)
	}
	if t2.DurationSeconds == nil || *t2.DurationSeconds != 10500 {
		t.Errorf("t2 reads duration %v, want the last run's own span: its first dispatch (06:00) to its gate's finish (08:55)", t2.DurationSeconds)
	}
	t1 := tickByID["at1"]
	if t1.State != "closed" {
		t.Errorf("t1 reads state %q, want closed though the newest run's row says ready", t1.State)
	}
	if t1.Tier == nil || *t1.Tier != "strong" {
		t.Errorf("t1 reads tier %v, want run aaa's strong", t1.Tier)
	}
	if t1.DurationSeconds == nil || *t1.DurationSeconds != 6900 {
		t.Errorf("t1 reads duration %v, want 1h55m from run aaa's dispatch to its gate's finish", t1.DurationSeconds)
	}
	for _, stage := range t2.Pipeline {
		if stage.State != StageStateDone {
			t.Errorf("t2's %s stage reads %s, want done: a closed tick's cell fills to the end", stage.Stage, stage.State)
		}
	}

	// The held tick stays held: the newest run's "ready" row does not reset
	// the rejection the last run recorded for the attempt nobody released.
	t3 := tickByID["at3"]
	if t3.State != "rejected" {
		t.Errorf("t3 reads state %q, want the held attempt's rejection, not the newest run's ready row", t3.State)
	}
	if len(t3.Tries) != 1 || t3.Tries[0].Attempt != 12 || t3.Tries[0].Outcome != TryRejected {
		t.Errorf("t3 reads tries %+v, want run bbb's rejected attempt 12", t3.Tries)
	}

	// The duplicate carries the tick it duplicates, and its row still exists.
	dup := tickByID["dup"]
	if dup.DuplicateOf == nil || *dup.DuplicateOf != "at2" {
		t.Errorf("dup reads duplicate_of %v, want at2", dup.DuplicateOf)
	}

	// The run header says the newest run failed — the epic's closed work
	// does not soften the verdict.
	if model.Lifecycle.Phase != PhaseFailed {
		t.Errorf("the lifecycle reads phase %q, want the newest run's failed", model.Lifecycle.Phase)
	}
	if model.Health.Verdict.State != VerdictStopped {
		t.Errorf("the verdict reads %q, want stopped for a failed run", model.Health.Verdict.State)
	}
	if !strings.Contains(model.Health.Verdict.Summary, "failed") {
		t.Errorf("the stopped summary reads %q, want the newest run's own failure", model.Health.Verdict.Summary)
	}

	// The run section is the newest run's alone: its worker stands, and the
	// cost counts the newest run's dispatches, not the epic's whole history.
	if model.Workers == nil || len(*model.Workers) != 1 || (*model.Workers)[0].TickID != "at4" {
		t.Errorf("the workers read %+v, want the newest run's own standing attempt", model.Workers)
	}
	if model.Cost.Attempts != 1 {
		t.Errorf("the cost reads %d attempts, want the newest run's own 1: prior runs' spend is not this run's", model.Cost.Attempts)
	}
	if len(model.Gates) != 0 {
		t.Errorf("the gates read %+v, want none: the newest run recorded no gate evidence", model.Gates)
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitWorkers {
		t.Errorf("the run waits on %+v, want its own standing worker", model.WaitsOn)
	}
}

// TestBuildETAAcrossRuns: the estimate is built from every closed tick the
// epic has measured, earlier runs included — with the duplicate excluded.
func TestBuildETAAcrossRuns(t *testing.T) {
	t.Parallel()
	src := failedNewestSources()
	model := Build(src)
	if model.Remaining == nil {
		t.Fatal("three measured closes support an estimate and the fixture has them: the model must state one")
	}
	// The measured durations are t1 1h55m (run aaa), t2 2h55m (run bbb),
	// t5 35m (run bbb) — median 1h55m, times the two ticks still open
	// (t3, t4).
	if model.Remaining.ApproximateSeconds != 6900*2 {
		t.Errorf("the estimate reads %d seconds, want the median duration times the two open ticks",
			model.Remaining.ApproximateSeconds)
	}
}

// TestDuplicateOfReadsTheTrackerRecord: the two spellings a duplicate
// closure takes in the tracker's own records — the dedup writer's note and
// a hand-closed reason — and the shapes that are not duplicates at all.
func TestDuplicateOfReadsTheTrackerRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		task tk.GraphTask
		want *string
	}{
		{
			name: "the dedup writer's note",
			task: tk.GraphTask{Status: "closed", Notes: "2026-10-01 06:47 - ticfac run run_bbb: closed as a duplicate of at2. Both were promoted from finding f519 — at2 by run run_aaa, this one by run run_bbb — and a finding is one tick"},
			want: strPtr("at2"),
		},
		{
			name: "a hand-closed reason naming the tick",
			task: tk.GraphTask{Status: "closed", ClosedReason: "duplicate of ky5, fixed in #46"},
			want: strPtr("ky5"),
		},
		{
			name: "a closed reason that names only the fact",
			task: tk.GraphTask{Status: "closed", ClosedReason: "duplicate: tracked in pengelbrecht/ticks:jlm"},
			want: strPtr(""),
		},
		{
			name: "a reason with no pointer at all",
			task: tk.GraphTask{Status: "closed", ClosedReason: "duplicate"},
			want: strPtr(""),
		},
		{
			name: "an open tick is never a duplicate",
			task: tk.GraphTask{Status: "open", Notes: "closed as a duplicate of at2"},
			want: nil,
		},
		{
			name: "a closed tick with no duplicate words",
			task: tk.GraphTask{Status: "closed", ClosedReason: "Merged; wave gate green."},
			want: nil,
		},
		{
			name: "prose that merely contains the words in another shape",
			task: tk.GraphTask{Status: "closed", Notes: "a note about duplicates in general"},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := duplicateOf(tc.task)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("duplicateOf is %v, want %v", got, tc.want)
			}
			if got != nil && *got != *tc.want {
				t.Fatalf("duplicateOf is %q, want %q", *got, *tc.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestNextStepOfAParkedTickNamesTheResume: a refusal the newest run did not
// leave belongs to a run that is over, and the run will retry nothing — the
// try's next step is the resume command the run header names, not the
// ladder's own retry-or-escalate word.
func TestNextStepOfAParkedTickNamesTheResume(t *testing.T) {
	t.Parallel()
	src := failedNewestSources()
	model := Build(src)
	var held Tick
	for _, w := range *model.Waves {
		for _, tick := range w.Ticks {
			if tick.TickID == "at3" {
				held = tick
			}
		}
	}
	if len(held.Tries) != 1 || held.Tries[0].NextStep == nil {
		t.Fatalf("the parked tick's try reads %+v, want a next step", held.Tries)
	}
	if !strings.Contains(*held.Tries[0].NextStep, "nothing of that run is working") {
		t.Errorf("the parked tick's next step reads %q, want the resume the run's own machinery cannot give",
			*held.Tries[0].NextStep)
	}
}
