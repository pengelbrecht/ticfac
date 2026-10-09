package statusmodel

// The ended-run truths (epic ymf, tick jym, folding the open watch defects):
// what a model states about a run that is no longer going, read back off the
// records the run left. Two defect classes live here:
//
//   - tick 4dn: a run that ended with a tick still marked dispatched left
//     that tick its LAST word, not live work — the per-tick elapsed must not
//     grow over a run whose own clock has already stopped;
//   - tick t0y: a run that completed and had its PR merged must read its CI
//     chapter done beside its merge done, never ci in progress next to a
//     done merge — the close-out's held line is still the run's last word
//     in the feed, and it must not keep the phase it was the hint about.
//
// Both are stated in the model, so every renderer that reads it inherits the
// answer; the frame-level reads are the watch view tests'.

import (
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// endedRunSources is tick 4dn's own shape: one run, ended by its own terminal
// checkpoint, whose last word about a tick the tracker has NOT closed is the
// dispatched row the run left when it stopped. The mutate hook bends the
// fixture into each case — the same records while the run was going, or the
// reported variant.
func endedRunSources(mutate func(src *Sources, recs *Records)) Sources {
	now := testNow.Add(72 * time.Hour)
	records := &Records{
		Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion,
			RunID:         "epic-4dn",
			EpicID:        "4dn",
			Sequence:      3,
			State:         "failed",
			Reason:        "the worker died mid-attempt",
			UpdatedAt:     now.Add(-2 * time.Hour).Format(time.RFC3339),
			Ticks:         []runstate.TickState{{TickID: "d1", State: "dispatched", Attempt: 2}},
		},
		Attempts: []runstate.Attempt{
			attemptMarker(2, "d1", now.Add(-1*time.Hour).Format(time.RFC3339), "strong",
				"@cf/zai-org/glm-5.3", "local-subprocess"),
		},
	}
	src := Sources{
		Now:   now,
		RunID: "epic-4dn",
		Host:  HostLocal,
		Graph: &tk.Graph{
			Epic: tk.GraphEpic{ID: "4dn", Title: "the epic the run worked"},
			Waves: []tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "d1", Title: "the dispatched tick", Gloss: "dispatched", Status: "open"},
			}}},
		},
		Records: records,
		Liveness: LivenessInput{
			Alive: false, State: "dead",
			Reason: "no process holds this run", Source: "run.pid",
		},
	}
	if mutate != nil {
		mutate(&src, records)
	}
	return src
}

// TestAnEndedRunLeavesItsDispatchedRowUnmeasured (tick 4dn): the per-tick
// elapsed freezes where the run-level clock already does. A dead run's stale
// dispatched row for a tick the tracker has not closed still stands — it is
// the run's last word and the history the holds and the resume read — but
// its elapsed is null: measuring its dispatch stamp to now grows a countdown
// over a run that is over. The same records while the run was going measure
// the attempt, and a reported row on an ended run is the same frozen history.
func TestAnEndedRunLeavesItsDispatchedRowUnmeasured(t *testing.T) {
	t.Parallel()

	t.Run("a dead run's dispatched row measures nothing", func(t *testing.T) {
		t.Parallel()
		model := Build(endedRunSources(nil))
		tick := epicTick(t, model, "d1")
		if tick.State != tickDispatched {
			t.Fatalf("the tick reads state %q, want dispatched: the tracker has not closed it", tick.State)
		}
		if tick.ElapsedSeconds != nil {
			t.Errorf("the ended run's dispatched row reads a live elapsed of %ds, want null: "+
				"the countdown would grow over a run the run-level clock has already stopped",
				*tick.ElapsedSeconds)
		}
	})

	t.Run("a dead run's reported row measures nothing either", func(t *testing.T) {
		t.Parallel()
		model := Build(endedRunSources(func(src *Sources, recs *Records) {
			recs.Checkpoint.Ticks = []runstate.TickState{{TickID: "d1", State: "reported", Attempt: 2}}
		}))
		tick := epicTick(t, model, "d1")
		if tick.ElapsedSeconds != nil {
			t.Errorf("the ended run's reported row reads a live elapsed of %ds, want null", *tick.ElapsedSeconds)
		}
	})

	t.Run("the same records while the run is going still measure the attempt", func(t *testing.T) {
		t.Parallel()
		model := Build(endedRunSources(func(src *Sources, recs *Records) {
			recs.Checkpoint.State = "running"
			recs.Checkpoint.UpdatedAt = src.Now.Add(-time.Minute).Format(time.RFC3339)
			src.Liveness = LivenessInput{Alive: true, State: "alive", Reason: "pid 4242", Source: "run.pid"}
		}))
		tick := epicTick(t, model, "d1")
		if tick.ElapsedSeconds == nil || *tick.ElapsedSeconds != 3600 {
			t.Errorf("the live run's dispatched row reads elapsed %v, want 3600 (dispatched 1h ago): "+
				"the fix is the ended check, never the dispatched state", tick.ElapsedSeconds)
		}
	})

	t.Run("a standing attempt is measured even on an ended run's records", func(t *testing.T) {
		// The census is the live-work authority: a row it answers for is
		// measured whatever the checkpoint says, the same precedence the
		// liveness hint carries beside it.
		t.Parallel()
		model := Build(endedRunSources(func(src *Sources, recs *Records) {
			src.StandingRead = true
			src.Standing = []runprogress.Attempt{{TickID: "d1", Attempt: 2}}
		}))
		tick := epicTick(t, model, "d1")
		if tick.ElapsedSeconds == nil || *tick.ElapsedSeconds != 3600 {
			t.Errorf("a standing attempt's elapsed reads %v, want 3600: the census answers for it",
				tick.ElapsedSeconds)
		}
	})
}

// TestAnEndedMergedRunReadsItsCIAndMergeDone (tick t0y): the run completed,
// the PR it opened was merged (so the forge answers no open PR), and the
// close-out's held line is still the run's last word in the feed. The phase
// record reads ci done beside merge done — a chapter whose subject is gone
// is done, the same fact the merge phase reads — and the track's "PR & CI"
// step and "Merged" step carry it, so no surface can show ci in progress
// next to a merged epic. The old dashboard showed exactly that pair.
func TestAnEndedMergedRunReadsItsCIAndMergeDone(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Checkpoint.State = "completed"
	src.Liveness.Alive = false
	src.Liveness.State = "not_running"
	src.Session = nil
	src.Standing = nil
	// The run's last words: the close-out held on CI, then the run completed.
	src.Feed = append(src.Feed,
		runfeed.NewEvent(testNow.Add(-5*time.Minute), "epic-2jn", "", nil,
			reconcile.StageCloseoutHeld, "the close-out waits for CI green on the PR"),
		runfeed.NewEvent(testNow.Add(-time.Minute), "epic-2jn", "", nil,
			reconcile.StageRunFinished, "completed: every tick of 2jn is closed behind the integrated gate"))
	// The PR is merged: the forge's open-PR read answers nothing (Find reads
	// open PRs), so the model's CI is nil — the merged epic has no PR left.
	src.CI = nil
	model := Build(src)

	states := map[string]string{}
	for _, p := range model.Lifecycle.Phases {
		states[p.Phase] = p.State
	}
	if states[PhaseCI] != PhaseStateDone {
		t.Errorf("the merged run's ci phase reads %q, want done: the PR it waited on is merged", states[PhaseCI])
	}
	if states[PhaseMerge] != PhaseStateDone {
		t.Errorf("the merged run's merge phase reads %q, want done: no PR stands open", states[PhaseMerge])
	}
	track := map[string]string{}
	for _, step := range model.Lifecycle.Track {
		track[step.Label] = step.State
	}
	if track["PR & CI"] != PhaseStateDone || track["merged"] != PhaseStateDone {
		t.Errorf("the track reads PR & CI=%q, merged=%q, want done and done: "+
			"the old dashboard showed ci in progress beside a done merge", track["PR & CI"], track["merged"])
	}
	if model.Lifecycle.Phase != PhaseDone {
		t.Errorf("the merged run reads phase %q, want done", model.Lifecycle.Phase)
	}
	if model.WaitsOn != nil && model.WaitsOn.NeedsPerson {
		t.Errorf("a merged epic needs nobody: %+v", model.WaitsOn)
	}
}
