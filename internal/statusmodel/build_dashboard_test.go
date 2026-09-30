package statusmodel

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The DASHBOARD vocabulary (epic hn6): this file pins what wave 1 declared
// (tick r5i) and what its wave-2 fills have not yet reached. Wave 1 declared
// every field the dashboard renders; the pipeline cell, the parent, the
// duration, the findings and the tries' tier/reason/next_step are now
// DERIVED (wave 2, tick 3gk — pinned case by case in pipeline_test.go, over
// this file's running-epic fixture too), so what remains here is the other
// half: the fields the remaining wave-2 ticks (7uv's verdict and cost, ltg's
// worker activity and attempt reports) still answer at their honest empty
// values, plus the three fields wave 1 filled for real (`recent`,
// `epic_title`, `gloss`) and the vocabulary the contract's enums spell.

// TestBuildEmitsTheDashboardFieldsEmpty: a Build over the running-epic
// fixture emits the dashboard fields the wave-2 fills have not reached at their
// honest empty values — the role's own pipeline stage list (the SHAPE a
// renderer lays the cell out from; the states are derived now, and
// pipeline_test.go pins them over this same fixture), findings as the
// required list (empty or carried, never nil), the report and the worker
// activity unread, the healthy stub verdict, no cost lines — and the three
// fields wave 1 filled for real: `recent` holding the last five feed lines
// oldest first, `epic_title` copied off the graph, and every tick's `gloss`
// copied off its graph task.
func TestBuildEmitsTheDashboardFieldsEmpty(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	model := Build(src)

	if model.Waves == nil {
		t.Fatal("the fixture's graph answered and the model states no waves")
	}

	// One tick per role, each with the role's own stage list — the fixed shape
	// a renderer lays the cell out from. Which stage a tick is IN is a
	// derivation pipeline_test.go pins; this holds the list itself.
	tickByID := map[string]Tick{}
	for _, w := range *model.Waves {
		for _, tick := range w.Ticks {
			tickByID[tick.TickID] = tick
		}
	}
	for tickID, want := range map[string][]string{
		"nwj": PipelineImplement,
		"6dh": PipelineImplement,
		"89m": PipelineImplement,
		"152": PipelineImplement,
		"xbp": PipelineReview,
		"rrl": PipelineCloseout,
	} {
		got := tickByID[tickID].Pipeline
		if len(got) == 0 {
			t.Fatalf("%s carries no pipeline cell at all", tickID)
		}
		if !stagesOf(got, want) {
			t.Errorf("%s's pipeline cell is %s, want the %v stages the role carries",
				tickID, cellOf(got), want)
		}
	}

	// The per-tick fields a dashboard drills into: the findings list is
	// required (never nil — the tick that drafted none and the tick that
	// drafted some are both whole rows), and the report stays unread until
	// the wave-2 report tick reads it. The derived halves — findings contents,
	// parent, duration, the tries' tier/reason/next_step — are pipeline_test's.
	for _, tick := range tickByID {
		if tick.Findings == nil {
			t.Errorf("%s's findings are nil, want the list: the field is required, and nil marshals as null", tick.TickID)
		}
		if tick.Report != nil {
			t.Errorf("%s carries a report %+v, want null: no report has been read", tick.TickID, *tick.Report)
		}
	}

	// The worker panel: no activity measured and no executor handle named —
	// both null, not zero-valued shapes.
	if model.Workers == nil || len(*model.Workers) != 1 {
		t.Fatalf("the census read one standing attempt and the model says %+v", model.Workers)
	}
	worker := (*model.Workers)[0]
	if worker.Activity != nil {
		t.Errorf("the worker's activity is %+v, want null: nothing measures it yet", *worker.Activity)
	}
	if worker.Handle != nil {
		t.Errorf("the worker's handle is %q, want null: no executor named it yet", *worker.Handle)
	}

	// The verdict: healthy with nothing recovered and nothing to say — the
	// stub's answer, which the wave-2 verdict tick grows into the real one.
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("the verdict state is %q, want healthy", model.Health.Verdict.State)
	}
	if model.Health.Verdict.Summary != "" {
		t.Errorf("the verdict summary is %q, want empty", model.Health.Verdict.Summary)
	}
	if model.Health.Verdict.Recovered == nil || len(model.Health.Verdict.Recovered) != 0 {
		t.Errorf("the verdict's recovered list is %+v, want the empty list", model.Health.Verdict.Recovered)
	}

	// The cost lines: none yet — the recorded_usd half still answers, and
	// the lines are the wave-2 cost tick's to fill.
	if model.Cost.Lines == nil {
		t.Error("the cost lines are nil, want the empty list: the field is required")
	}
	if len(model.Cost.Lines) != 0 {
		t.Errorf("the cost lines are %+v, want empty", model.Cost.Lines)
	}

	// recent: the one field this tick fills for real. The fixture's feed has
	// seven lines; the model carries the LAST five, oldest first — the tail
	// a dashboard shows, not the whole stream.
	if len(src.Feed) != 7 {
		t.Fatalf("the fixture's feed grew to %d lines; this test pins the last-5 cut against 7", len(src.Feed))
	}
	wantStages := []string{
		reconcile.StageStallWarned,
		reconcile.StageGateFailed,
		reconcile.StageResumedAutomatically,
		reconcile.StageWallClock,
		reconcile.StageDispatched,
	}
	if len(model.Recent) != 5 {
		t.Fatalf("the model carries %d recent events, want the last 5 of the feed's 7", len(model.Recent))
	}
	for i, want := range wantStages {
		if model.Recent[i].Stage != want {
			t.Errorf("recent[%d] is %s, want %s: the tail is oldest first, and it is the feed's own last five lines",
				i, model.Recent[i].Stage, want)
		}
	}

	// An empty feed answers an empty tail, not null: nothing happened is a
	// claim the dashboard renders.
	empty := src
	empty.Feed = nil
	if model := Build(empty); model.Recent == nil || len(model.Recent) != 0 {
		t.Errorf("an empty feed yields recent %+v, want the empty list", model.Recent)
	}

	// epic_title and the tick glosses: direct copies off the graph. The
	// graph read, so both answer; the unread graph is the null case the
	// fixture below pins.
	if model.EpicTitle == nil || *model.EpicTitle != src.Graph.Epic.Title {
		t.Errorf("the epic title is %v, want the graph's own %q copied", model.EpicTitle, src.Graph.Epic.Title)
	}
	for tickID, want := range map[string]string{
		"nwj": "cobra+fang command surface",
		"6dh": "one model of every run",
		"xbp": "final review",
	} {
		if got := tickByID[tickID].Gloss; got != want {
			t.Errorf("%s's gloss is %q, want the graph task's own %q", tickID, got, want)
		}
	}

	// A run whose graph could not be read states no epic title — an
	// unread tracker and a titleless epic are different claims.
	unread := src
	unread.Graph = nil
	if model := Build(unread); model.EpicTitle != nil {
		t.Errorf("a run with no graph states the epic title %q, want null", *model.EpicTitle)
	}
}

// stagesOf answers whether the cell's stages are exactly the named list, in
// order — the layout the renderer fills left to right.
func stagesOf(cell []PipelineStage, stages []string) bool {
	if len(cell) != len(stages) {
		return false
	}
	for i, stage := range stages {
		if cell[i].Stage != stage {
			return false
		}
	}
	return true
}

// cellOf renders a pipeline cell for a failure message.
func cellOf(cell []PipelineStage) string {
	out := "["
	for i, stage := range cell {
		if i > 0 {
			out += " "
		}
		out += stage.Stage + ":" + stage.State
	}
	return out + "]"
}

// TestThePipelineVocabularyIsClosed: the stage and state enums the contract
// pins, spelled once in Go so the schema and the builder cannot drift over
// a string the other side never agreed to.
func TestThePipelineVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	for _, cell := range [][]string{PipelineImplement, PipelineReview, PipelineCloseout} {
		for _, stage := range cell {
			switch stage {
			case StageClaim, StageWork, StageReview, StageGate, StageCI, StageMerged, StageClosed:
			default:
				t.Errorf("the stage %q is outside the closed vocabulary", stage)
			}
		}
	}
	if stages, want := PipelineImplement, []string{StageClaim, StageWork, StageGate, StageMerged}; !equalStages(stages, want) {
		t.Errorf("PipelineImplement is %v, want %v", stages, want)
	}
	if stages, want := PipelineReview, []string{StageClaim, StageReview, StageClosed}; !equalStages(stages, want) {
		t.Errorf("PipelineReview is %v, want %v", stages, want)
	}
	if stages, want := PipelineCloseout, []string{StageClaim, StageWork, StageCI, StageClosed}; !equalStages(stages, want) {
		t.Errorf("PipelineCloseout is %v, want %v", stages, want)
	}
}

func equalStages(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRecentCarriesTheFeedEventShape: `recent` holds the same event shape
// `liveness.last_event` carries — the feed's own line, whole, so a renderer
// prints the tail with the parser it already has.
func TestRecentCarriesTheFeedEventShape(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	model := Build(src)

	last := src.Feed[len(src.Feed)-1]
	if model.Liveness.LastEvent == nil {
		t.Fatal("the fixture's feed has a last line and liveness states none")
	}
	if model.Liveness.LastEvent.Stage != last.Stage {
		t.Fatalf("liveness's last event and the feed's last line disagree: %s vs %s",
			model.Liveness.LastEvent.Stage, last.Stage)
	}
	if len(model.Recent) == 0 {
		t.Fatal("the feed has lines and recent is empty")
	}
	// The tail entries are runfeed.Event values — the feed's own line shape,
	// not a second event vocabulary a renderer has to learn.
	var tail runfeed.Event = model.Recent[len(model.Recent)-1]
	if tail.Stage != last.Stage || tail.At != last.At || tail.Detail != last.Detail {
		t.Errorf("recent's newest line is %s at %s (%q), want the feed's own last line %s at %s (%q)",
			tail.Stage, tail.At, tail.Detail, last.Stage, last.At, last.Detail)
	}
}
