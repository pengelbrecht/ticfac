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

	"github.com/pengelbrecht/ticfac/internal/reconcile"
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

// priorFeedsAcrossRuns is the two earlier runs' own feeds, keyed by run id:
// the facts about a hold only the feed states. Run bbb held at3 for a person
// and nobody has answered since — the records say the tick was rejected, but
// only the line says the run held it FOR A PERSON. Run aaa's own hold was
// answered by a resume (the person released the attempt, the run continued
// and closed the tick), so its feed leaves nothing standing.
func priorFeedsAcrossRuns() map[string][]runfeed.Event {
	return map[string][]runfeed.Event{
		"run_aaa": {
			{SchemaVersion: 1, At: "2026-09-26T11:00:00Z", RunID: "run_aaa",
				TickID: tickPtr("at2"), Attempt: intPtr(2), Stage: reconcile.StageRunHeld,
				Detail: reconcile.RefusedRejectedWork + ": attempt 2 of at2 was rejected with commits nothing merged"},
			{SchemaVersion: 1, At: "2026-09-26T11:20:00Z", RunID: "run_aaa", TickID: nil, Attempt: nil,
				Stage: reconcile.StageResumed, Detail: "resumed after the person released the attempt"},
		},
		"run_bbb": {
			{SchemaVersion: 1, At: "2026-09-27T08:10:00Z", RunID: "run_bbb",
				TickID: tickPtr("at3"), Attempt: intPtr(12), Stage: reconcile.StageRunHeld,
				Detail: reconcile.RefusedRejectedWork + ": attempt 12 of at3 was rejected with commits nothing merged"},
		},
	}
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
		PriorFeeds:   priorFeedsAcrossRuns(),
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

	// But a hold an earlier run left for a person is not the run section's
	// to swallow: run bbb held at3 and nobody answered, and the newest run
	// failed at boot before writing a hold of its own — so the wait IS the
	// hold, outranking the standing worker, with the command that releases
	// THAT run's attempt (attempt numbers are per run).
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
		t.Fatalf("the run waits on %+v, want the hold run bbb left on at3", model.WaitsOn)
	}
	if !strings.Contains(model.WaitsOn.What, "run_bbb") || !strings.Contains(model.WaitsOn.What, "at3") {
		t.Errorf("the hold reads %q, want the holding run and the tick named", model.WaitsOn.What)
	}
	if model.WaitsOn.Since == nil || *model.WaitsOn.Since != "2026-09-27T08:10:00Z" {
		t.Errorf("the hold reads since %+v, want the run_held line's own stamp", model.WaitsOn.Since)
	}
	if model.WaitsOn.UnblockCommand == nil ||
		*model.WaitsOn.UnblockCommand != `ticfac settle hpd at3 12 --run-id run_bbb --release "<who>"` {
		t.Errorf("the hold's command is %+v, want the release addressed to the holding run", model.WaitsOn.UnblockCommand)
	}
	if len(model.Attention) != 2 {
		t.Fatalf("the attention reads %+v, want the prior hold and the newest run's own failed resume (tick jkb)", model.Attention)
	}
	for _, a := range model.Attention {
		switch a.Kind {
		case WaitHeldForPerson:
			if !strings.Contains(a.What, "run_bbb") || !strings.Contains(a.What, "at3") {
				t.Errorf("the held-for-person entry reads %q, want the holding run and the tick named", a.What)
			}
		case WaitDeadRun:
			// The newest run failed by its own word and is not going: the
			// resume is a person's, beside the hold — the hold is the harder
			// stop, so it is the one the run waits ON.
			if !strings.Contains(a.What, "failed") || a.UnblockCommand == nil ||
				*a.UnblockCommand != "ticfac run hpd --cloud" {
				t.Errorf("the failed run's resume entry reads %+v, want the cloud resume", a)
			}
		default:
			t.Errorf("the attention carries an entry nobody states: %+v", a)
		}
	}

	// Run aaa's hold was answered — the resume stands after it in its own
	// feed — and no older run's word may resurface it beside the live one.
	for _, a := range model.Attention {
		if strings.Contains(a.What, "run_aaa") {
			t.Errorf("the attention carries a hold run aaa's resume already answered: %+v", a)
		}
	}
}

// TestBuildETAAcrossRuns: the estimate is built from every closed tick the
// epic has measured, earlier runs included — with the duplicate excluded.
// A run that ended by its own word states NONE (tick jkb): the estimate is
// the going run's answer to "how long is left", and a failed or stopped run
// will finish nothing in the time it would state — so the same fixture is
// turned going (the failed ending answered by a resume, the run alive
// again) to pin the across-runs estimate where it belongs.
func TestBuildETAAcrossRuns(t *testing.T) {
	t.Parallel()
	ended := Build(failedNewestSources())
	if ended.Remaining != nil {
		t.Errorf("a run whose own word says it failed states an ETA of %+v, want none: nothing is going to finish in that time",
			ended.Remaining)
	}

	// The same epic, going again: the failed incarnation's ending is
	// history — the resume stands after it — and the estimate returns,
	// measured across runs as before.
	src := failedNewestSources()
	src.Records.Checkpoint.State = "running"
	src.Records.Checkpoint.Reason = "at4 is dispatched again"
	two := 2
	src.Feed = append(src.Feed,
		runfeed.Event{SchemaVersion: 1, At: src.Now.Add(-20 * time.Minute).UTC().Format(time.RFC3339),
			RunID: "run_ccc", Stage: reconcile.StageResumed,
			Detail: "the run stopped at failed and is resumed under the same run id: the orchestrator could not claim the epic's width"},
		runfeed.Event{SchemaVersion: 1, At: src.Now.Add(-15 * time.Minute).UTC().Format(time.RFC3339),
			RunID: "run_ccc", TickID: tickPtr("at4"), Attempt: &two, Stage: reconcile.StageDispatched,
			Detail: "at4 try 2 dispatched"},
	)
	src.Liveness = LivenessInput{
		Alive: true, State: "running",
		Reason: "the Workflow's supervisor says the orchestrator container is running",
		Source: "workflow-supervisor",
	}
	model := Build(src)
	if model.Remaining == nil {
		t.Fatal("three measured closes support an estimate and the going fixture has them: the model must state one")
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

// TestAParkedTrysNextStepAgreesWithTheNeedsYouHeader: the try's next step and
// the needs-you header are two wordings of one answer, and they cannot
// disagree (tick eli). A tick a prior run held for a person is parked work —
// and while that hold stands, the row's word is the same release command the
// header carries, never the generic resume: a run-epic resume alone does not
// clear the hold (the new run holds again on the unreleased attempt), so the
// resume sends the person to a first move that answers nothing. A parked tick
// with NO standing hold is still the resume's to name — that branch stands.
func TestAParkedTrysNextStepAgreesWithTheNeedsYouHeader(t *testing.T) {
	t.Parallel()

	t.Run("a standing prior hold names the header's release command", func(t *testing.T) {
		t.Parallel()
		model := Build(failedNewestSources())
		held := epicTick(t, model, "at3")
		if len(held.Tries) != 1 || held.Tries[0].NextStep == nil {
			t.Fatalf("the held tick's try reads %+v, want a next step", held.Tries)
		}
		if model.WaitsOn == nil || model.WaitsOn.UnblockCommand == nil {
			t.Fatalf("the header carries no release command: %+v", model.WaitsOn)
		}
		if *held.Tries[0].NextStep != *model.WaitsOn.UnblockCommand {
			t.Errorf("the held tick's next step reads %q, want the header's own command %q: two wordings of one answer cannot disagree",
				*held.Tries[0].NextStep, *model.WaitsOn.UnblockCommand)
		}
	})

	// l1t: the same agreement for a hold the CURRENT run left — the row's
	// next step comes from attentionCommand's mirror of buildWaits, and the
	// two spellings of the settle sentence it could hold drifted apart the
	// first wording change touched only one of them.
	t.Run("a standing current-run hold names the header's release command", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.Records.Checkpoint.Ticks = []runstate.TickState{{TickID: "at1", State: "rejected", Attempt: 1}}
			src.Records.Attempts = []runstate.Attempt{attemptMarker(1, "at1",
				src.Now.Add(-15*time.Minute).UTC().Format(time.RFC3339), "strong", "claude-opus-5", "local-subprocess")}
			src.Feed = append(src.Feed,
				runfeed.Event{
					SchemaVersion: 1, At: src.Now.Add(-12 * time.Minute).UTC().Format(time.RFC3339), RunID: src.RunID,
					TickID: tickPtr("at1"), Attempt: intPtr(1), Stage: reconcile.StageRejected,
					Detail: reconcile.RefusedRejectedWork + ": attempt 1 of at1 was rejected with commits nothing merged",
				},
				runfeed.Event{
					SchemaVersion: 1, At: src.Now.Add(-10 * time.Minute).UTC().Format(time.RFC3339), RunID: src.RunID,
					TickID: tickPtr("at1"), Attempt: intPtr(1), Stage: reconcile.StageRunHeld,
					Detail: "attempt 1 of at1 struck out: the refusal the run recorded",
				})
		}))
		held := epicTick(t, model, "at1")
		if len(held.Tries) != 1 || held.Tries[0].NextStep == nil {
			t.Fatalf("the held tick's try reads %+v, want a next step", held.Tries)
		}
		if model.WaitsOn == nil || model.WaitsOn.UnblockCommand == nil {
			t.Fatalf("the header carries no release command: %+v", model.WaitsOn)
		}
		if *held.Tries[0].NextStep != *model.WaitsOn.UnblockCommand {
			t.Errorf("the held tick's next step reads %q, want the header's own command %q: two wordings of one answer cannot disagree",
				*held.Tries[0].NextStep, *model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("a parked tick with no standing hold still names the resume", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			prior := src.PriorRecords[0]
			prior.Checkpoint.Ticks = []runstate.TickState{{TickID: "at1", State: "rejected", Attempt: 7}}
			prior.Attempts = []runstate.Attempt{attemptMarker(7, "at1",
				testNow.Add(-47*time.Hour).UTC().Format(time.RFC3339), "strong", "claude-opus-5", "local-subprocess")}
			src.PriorRecords[0] = prior
			src.PriorFeeds["run_old"] = append(src.PriorFeeds["run_old"], runfeed.Event{
				SchemaVersion: 1, At: testNow.Add(-40 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
				Stage: reconcile.StageResumed, Detail: "resumed after the person released the attempt",
			})
		}))
		parked := epicTick(t, model, "at1")
		if len(parked.Tries) != 1 || parked.Tries[0].NextStep == nil {
			t.Fatalf("the parked tick's try reads %+v, want a next step", parked.Tries)
		}
		if !strings.Contains(*parked.Tries[0].NextStep, "nothing of that run is working") {
			t.Errorf("the parked tick's next step reads %q, want the resume the run's own machinery cannot give",
				*parked.Tries[0].NextStep)
		}
	})

	t.Run("a standing prior hold's triage wording names the triage command", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			prior := src.PriorRecords[0]
			prior.Checkpoint.Ticks = []runstate.TickState{{TickID: "at1", State: "rejected", Attempt: 7}}
			prior.Attempts = []runstate.Attempt{attemptMarker(7, "at1",
				testNow.Add(-47*time.Hour).UTC().Format(time.RFC3339), "strong", "claude-opus-5", "local-subprocess")}
			src.PriorRecords[0] = prior
			src.PriorFeeds["run_old"] = []runfeed.Event{{
				SchemaVersion: 1, At: testNow.Add(-47 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
				TickID: tickPtr("at1"), Stage: reconcile.StageRunHeld,
				Detail: reconcile.RefusedFindingUntriaged + ": 2 findings are untriaged",
			}}
		}))
		held := epicTick(t, model, "at1")
		if len(held.Tries) != 1 || held.Tries[0].NextStep == nil {
			t.Fatalf("the held tick's try reads %+v, want a next step", held.Tries)
		}
		want := "ticfac triage hpd --run-id run_old"
		if *held.Tries[0].NextStep != want {
			t.Errorf("the held tick's next step reads %q, want %q: the triage addressed to the holding run",
				*held.Tries[0].NextStep, want)
		}
	})
}

// epicTick finds one tick's row in a built model — the same lookup the
// across-runs assertions make.
func epicTick(t *testing.T, model Model, tickID string) Tick {
	t.Helper()
	for _, w := range deref(model.Waves) {
		for _, tick := range w.Ticks {
			if tick.TickID == tickID {
				return tick
			}
		}
	}
	t.Fatalf("the model's waves carry no tick %s", tickID)
	return Tick{}
}

// priorHoldSources is the smallest epic a prior run's hold can stand in: one
// tick, one earlier run that held it for a person, one newest run that
// failed at boot — its own feed carries no run_held line, so without the
// prior feed the model would answer "nothing needs you" while a person's
// decision is standing. The mutate hook bends the fixture into each case.
func priorHoldSources(mutate func(*Sources)) Sources {
	now := testNow.Add(24 * time.Hour)
	seven := 7
	prior := Records{
		Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion,
			RunID:         "run_old",
			EpicID:        "hpd",
			Sequence:      3,
			State:         "failed",
			Reason:        "the container was evicted mid-run",
			UpdatedAt:     testNow.Add(-48 * time.Hour).Format(time.RFC3339),
		},
	}
	priorFeed := []runfeed.Event{
		{SchemaVersion: 1, At: testNow.Add(-48 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
			Stage: "run_started", Detail: "the run started"},
		{SchemaVersion: 1, At: testNow.Add(-47 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
			TickID: tickPtr("at1"), Attempt: &seven, Stage: reconcile.StageRunHeld,
			Detail: reconcile.RefusedRejectedWork + ": attempt 7 of at1 was rejected with commits nothing merged"},
	}
	feed := []runfeed.Event{
		{SchemaVersion: 1, At: now.Add(-30 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_new",
			Stage: "run_started", Detail: "the run started"},
		{SchemaVersion: 1, At: now.Add(-5 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_new",
			Stage: "run_finished", Detail: "failed: the orchestrator could not claim the epic's width"},
	}
	src := Sources{
		Now:    now,
		RunID:  "run_new",
		EpicID: "hpd",
		Graph: &tk.Graph{Waves: []tk.GraphWave{{
			Wave:  1,
			Tasks: []tk.GraphTask{{ID: "at1", Title: "the one tick", Status: "open"}},
		}}},
		Records: &Records{Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion, RunID: "run_new", EpicID: "hpd",
			Sequence: 1, State: "failed",
			Reason:    "failed: the orchestrator could not claim the epic's width",
			UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		}},
		PriorRecords: []Records{prior},
		PriorFeeds:   map[string][]runfeed.Event{"run_old": priorFeed},
		Feed:         feed,
		Liveness: LivenessInput{
			Alive: false, State: "failed",
			Reason: "the Workflow's own record says failed, so the run has ended",
			Source: "workflow-record",
		},
	}
	if mutate != nil {
		mutate(&src)
	}
	return src
}

// TestAPriorHoldStandsUntilAnswered: the cases a prior run's hold is read
// through. It stands until somebody answers it — the attempt is released (a
// settlement decision), the run is resumed past it, or the epic moves past
// the tick (closed, or taken up by a newer run) — and while it stands the
// model's attention names it with the command that clears THAT run's hold.
func TestAPriorHoldStandsUntilAnswered(t *testing.T) {
	t.Parallel()
	// The prior-hold tests below answer one hold at a time; the fixture's
	// newest run has ALSO failed by its own word, so the model states its
	// resume beside whatever the sub-case leaves standing (tick jkb) — the
	// assertions that the hold is gone are assertions about the HOLD, read
	// through this helper: the only wait left is the failed run's own
	// resume, and no held-for-person entry remains.
	answeredHold := func(t *testing.T, model Model) {
		t.Helper()
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitDeadRun ||
			!strings.Contains(model.WaitsOn.What, "failed") ||
			model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac run hpd --cloud" {
			t.Fatalf("the run waits on %+v, want only the failed newest run's own resume", model.WaitsOn)
		}
		for _, a := range model.Attention {
			if a.Kind == WaitHeldForPerson {
				t.Errorf("an answered hold still reads as a held-for-person wait: %+v", a)
			}
		}
	}

	t.Run("the hold stands and names the clearing command", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(nil))
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
			t.Fatalf("the run waits on %+v, want the hold run_old left", model.WaitsOn)
		}
		if !strings.Contains(model.WaitsOn.What, "run_old") || !strings.Contains(model.WaitsOn.What, "at1") {
			t.Errorf("the hold reads %q, want the holding run and the tick named", model.WaitsOn.What)
		}
		if model.WaitsOn.UnblockCommand == nil ||
			*model.WaitsOn.UnblockCommand != `ticfac settle hpd at1 7 --run-id run_old --release "<who>"` {
			t.Errorf("the hold's command is %+v, want the release addressed to the holding run", model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("a resume within the prior run answers it", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.PriorFeeds["run_old"] = append(src.PriorFeeds["run_old"], runfeed.Event{
				SchemaVersion: 1, At: testNow.Add(-40 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
				Stage: reconcile.StageResumed, Detail: "resumed after the person released the attempt",
			})
		}))
		if model.WaitsOn != nil || len(model.Attention) != 0 {
			t.Errorf("an answered hold still reads as a wait: %+v / %+v", model.WaitsOn, model.Attention)
		}
	})

	t.Run("a person's release answers it", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			prior := src.PriorRecords[0]
			prior.Decisions = []runstate.Decision{{
				SchemaVersion: runstate.SchemaVersion, Decision: 1,
				Role: "triage-failure", Validated: true,
				Request: map[string]any{
					"op": reconcile.SettleOp, "run_id": "run_old", "epic_id": "hpd",
					"tick_id": "at1", "attempt": 7,
				},
				Response: map[string]any{"settled": true, "released_by": "person-abc"},
			}}
			src.PriorRecords[0] = prior
		}))
		if model.WaitsOn != nil || len(model.Attention) != 0 {
			t.Errorf("a released hold still reads as a wait: %+v / %+v", model.WaitsOn, model.Attention)
		}
	})

	t.Run("the newest run's own hold on the tick is the live word", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.Feed = append(src.Feed, runfeed.Event{
				SchemaVersion: 1, At: src.Now.Add(-10 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_new",
				TickID: tickPtr("at1"), Attempt: intPtr(1), Stage: reconcile.StageRunHeld,
				Detail: "attempt 1 of at1 struck out: the refusal the run recorded",
			})
		}))
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson ||
			!strings.Contains(model.WaitsOn.What, "struck out") {
			t.Fatalf("the run waits on %+v, want the newest run's own hold", model.WaitsOn)
		}
		for _, a := range model.Attention {
			if strings.Contains(a.What, "run_old") {
				t.Errorf("the attention doubles the same decision with the older run's hold: %+v", a)
			}
		}
	})

	t.Run("a newer run taking the tick up supersedes it", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.Feed = append(src.Feed, runfeed.Event{
				SchemaVersion: 1, At: src.Now.Add(-10 * time.Minute).UTC().Format(time.RFC3339), RunID: "run_new",
				TickID: tickPtr("at1"), Attempt: intPtr(1), Stage: reconcile.StageDispatched,
				Detail: "at1 try 1 dispatched",
			})
		}))
		if model.WaitsOn != nil || len(model.Attention) != 0 {
			t.Errorf("a superseded hold still reads as a wait: %+v / %+v", model.WaitsOn, model.Attention)
		}
	})

	t.Run("a closed tick's hold is history", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.PriorRecords = append(src.PriorRecords, Records{
				Checkpoint: &runstate.Checkpoint{
					SchemaVersion: runstate.SchemaVersion, RunID: "run_mid", EpicID: "hpd",
					Sequence: 2, State: "failed",
					UpdatedAt: testNow.Add(-24 * time.Hour).Format(time.RFC3339),
					Ticks:     []runstate.TickState{{TickID: "at1", State: "closed", Attempt: 1}},
				},
			})
		}))
		if model.WaitsOn != nil || len(model.Attention) != 0 {
			t.Errorf("a hold on a closed tick still reads as a wait: %+v / %+v", model.WaitsOn, model.Attention)
		}
	})

	t.Run("the untriaged-findings hold is cleared by triage, addressed to the holding run", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.PriorFeeds["run_old"] = []runfeed.Event{{
				SchemaVersion: 1, At: testNow.Add(-47 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
				TickID: tickPtr("at1"), Stage: reconcile.StageRunHeld,
				Detail: reconcile.RefusedFindingUntriaged + ": 2 findings are untriaged",
			}}
		}))
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
			t.Fatalf("the run waits on %+v, want the findings hold", model.WaitsOn)
		}
		if model.WaitsOn.UnblockCommand == nil ||
			*model.WaitsOn.UnblockCommand != "ticfac triage hpd --run-id run_old" {
			t.Errorf("the hold's command is %+v, want the triage addressed to the holding run", model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("the absorption bound's hold is cleared by the triage that decides the finding", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.PriorFeeds["run_old"] = []runfeed.Event{{
				SchemaVersion: 1, At: testNow.Add(-47 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
				Stage:  reconcile.StageRunHeld,
				Detail: reconcile.RefusedAbsorptionDepth + ": the absorption recursion reached its bound",
			}}
		}))
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
			t.Fatalf("the run waits on %+v, want the run-level hold", model.WaitsOn)
		}
		if model.WaitsOn.UnblockCommand == nil ||
			*model.WaitsOn.UnblockCommand != "ticfac triage hpd --run-id run_old" {
			t.Errorf("the hold's command is %+v, want the triage addressed to the holding run: the "+
				"finding the bound refused to absorb is still that run's to decide (tick gf0)", model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("a hold the closed set does not know carries no command", func(t *testing.T) {
		t.Parallel()
		model := Build(priorHoldSources(func(src *Sources) {
			src.PriorFeeds["run_old"] = []runfeed.Event{{
				SchemaVersion: 1, At: testNow.Add(-47 * time.Hour).UTC().Format(time.RFC3339), RunID: "run_old",
				Stage:  reconcile.StageRunHeld,
				Detail: "some_future_hold: a hold kind the decision does not know",
			}}
		}))
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
			t.Fatalf("the run waits on %+v, want the run-level hold", model.WaitsOn)
		}
		if model.WaitsOn.UnblockCommand != nil {
			t.Errorf("the hold's command is %+v, want none: no command is better than a wrong one a "+
				"person copies", model.WaitsOn.UnblockCommand)
		}
	})
}
