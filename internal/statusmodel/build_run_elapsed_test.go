package statusmodel

import (
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The run's own clock (tick e6g): the dashboard header's elapsed used to be
// a renderer derivation — earliest try stamp to generated_at — so no contract
// field carried it (the phone page could not show it) and nothing clamped it
// at the run's end (a finished run's header kept counting). These tests pin
// the model's own answer: one field, measured, and frozen the moment the
// run's own records say it ended.

// TestTheModelMeasuresTheRunsOwnElapsed: the elapsed is the span from the
// earliest dispatch any tick's try history states to the model's own now —
// the same span the header renders, now carried by the model so every
// surface renders one answer.
func TestTheModelMeasuresTheRunsOwnElapsed(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())
	if model.Progress.RunElapsedSeconds == nil {
		t.Fatal("the model states no run elapsed beside a try history that carries dispatch stamps")
	}
	// The earliest marker is nwj's 2026-09-27T03:19:05Z; the model's own
	// now is 05:30:00Z — a span of 2h10m55s.
	if *model.Progress.RunElapsedSeconds != 7855 {
		t.Errorf("the run elapsed is %d, want 7855 (03:19:05 → 05:30:00)", *model.Progress.RunElapsedSeconds)
	}
}

// TestTheRunsElapsedIsMeasuredWithoutTheTracker: the span is read off the
// dispatch markers, not the waves — a tracker that cannot be read costs the
// model the waves and the counts, not the clock the run itself keeps.
func TestTheRunsElapsedIsMeasuredWithoutTheTracker(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Graph = nil
	model := Build(src)
	if model.Waves != nil {
		t.Fatal("a nil graph still laid out waves")
	}
	if model.Progress.RunElapsedSeconds == nil || *model.Progress.RunElapsedSeconds != 7855 {
		t.Errorf("the run elapsed reads %+v beside readable dispatch markers, want 7855",
			model.Progress.RunElapsedSeconds)
	}
}

// TestTheRunsElapsedFreezesAtTheRunsOwnEnd: a run whose own records say it
// ended stops there — a completed run's header must not keep counting while
// a person reads it. The end is the run's own word: the terminal checkpoint
// it wrote, and its own terminal line in the feed.
func TestTheRunsElapsedFreezesAtTheRunsOwnEnd(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	ended := testNow.Add(-2 * time.Hour) // the run completed two hours before the model was generated
	src.Records.Checkpoint.State = "completed"
	src.Records.Checkpoint.Reason = "every tick closed behind the integrated gate"
	src.Records.Checkpoint.UpdatedAt = ended.Format(time.RFC3339)
	src.Feed = append(src.Feed,
		runfeed.NewEvent(ended, "epic-2jn", "", nil, reconcile.StageRunFinished,
			"completed: every tick closed behind the integrated gate"))
	model := Build(src)
	// 03:19:05 → 03:30:00 is 655s — NOT the 7855 a live clock would say.
	if model.Progress.RunElapsedSeconds == nil {
		t.Fatal("a finished run states no elapsed: the span was measured and must survive the freeze")
	}
	if *model.Progress.RunElapsedSeconds != 655 {
		t.Errorf("a finished run's elapsed is %d, want 655: frozen at the end the run's own records state (03:19:05 → 03:30:00), not the clock a reader reads it on (05:30:00)",
			*model.Progress.RunElapsedSeconds)
	}
}

// TestTheRunsElapsedFreezesAtADeath: run_died is a run's own terminal word
// too — a run that stopped by a signal or an operational error has ended,
// and its clock stops at the line that says so.
func TestTheRunsElapsedFreezesAtADeath(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	died := testNow.Add(-1 * time.Hour)
	src.Feed = append(src.Feed,
		runfeed.NewEvent(died, "epic-2jn", "", nil, reconcile.StageRunDied,
			"stopped by a signal (interrupt) before the run finished"))
	model := Build(src)
	if model.Progress.RunElapsedSeconds == nil {
		t.Fatal("a dead run states no elapsed")
	}
	// 03:19:05 → 04:30:00 is 4255s.
	if *model.Progress.RunElapsedSeconds != 4255 {
		t.Errorf("a dead run's elapsed is %d, want 4255: frozen at its run_died line", *model.Progress.RunElapsedSeconds)
	}
}

// TestAResumeLiftsTheFreeze: a terminal line a resume stands AFTER is
// history — the run continued, and its clock runs on. The feed is
// append-only per run id, so a resumed run still carries the previous
// incarnation's run_finished, and an elapsed that read it as an end would
// freeze a live run at a death it walked away from.
func TestAResumeLiftsTheFreeze(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Feed = append(src.Feed,
		runfeed.NewEvent(testNow.Add(-3*time.Hour), "epic-2jn", "", nil, reconcile.StageRunFinished,
			"failed: the collect wedged"),
		runfeed.NewEvent(testNow.Add(-170*time.Minute), "epic-2jn", "", nil, reconcile.StageResumed,
			"the run resumed where it stopped"))
	model := Build(src)
	if model.Progress.RunElapsedSeconds == nil {
		t.Fatal("a resumed run states no elapsed")
	}
	if *model.Progress.RunElapsedSeconds != 7855 {
		t.Errorf("a resumed run's elapsed is %d, want 7855: the failed incarnation's end is history, the clock runs on to the model's own now",
			*model.Progress.RunElapsedSeconds)
	}
}

// TestTheRunsElapsedIsHonestAboutWhatNobodyMeasured: no dispatch marker, no
// span — a run that failed before it dispatched anything states null, never
// a zero that would read as "this took no time".
func TestTheRunsElapsedIsHonestAboutWhatNobodyMeasured(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Attempts = nil
	model := Build(src)
	if model.Progress.RunElapsedSeconds != nil {
		t.Errorf("a run with no dispatch stamp states elapsed %d, want null: an elapsed nobody measured is an elapsed nobody prints",
			*model.Progress.RunElapsedSeconds)
	}
}
