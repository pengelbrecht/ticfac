package statusmodel

import (
	"reflect"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The DASHBOARD vocabulary (epic hn6, wave 1 — tick r5i): this tick declared
// every field the dashboard renders so the wave-2 ticks fill them in
// parallel against a fixed shape. It computes NOTHING new beyond `recent`
// and the two graph copies (`epic_title`, `gloss`); every other new field
// came out at its honest empty value, and THIS test was the fence that said
// what the stubs must not quietly leave out. The wave-2 verdict and cost
// tick (7uv) has since filled the two run-level headline fields, and the
// two sections below pin them at their real values; the per-tick fields are
// still the wave-1 empties the pipeline and activity ticks (3gk, ltg) fill.

// pendingPipeline is the wave-1 pipeline cell: the role's own stage list,
// every stage pending — what a renderer lays the cell out from before
// anything has happened.
func pendingPipeline(stages []string) []PipelineStage {
	cell := make([]PipelineStage, 0, len(stages))
	for _, stage := range stages {
		cell = append(cell, PipelineStage{Stage: stage, State: StageStatePending})
	}
	return cell
}

// TestBuildEmitsTheDashboardFieldsEmpty: a Build over the running-epic
// fixture emits every dashboard field — pipeline per role all pending,
// findings empty, the report and the parent unread, no worker activity, the
// derived verdict and cost lines — and the three fields wave 1 fills for
// real: `recent` holding the last five feed lines oldest first, `epic_title`
// copied off the graph, and every tick's `gloss` copied off its graph task.
func TestBuildEmitsTheDashboardFieldsEmpty(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	model := Build(src)

	if model.Waves == nil {
		t.Fatal("the fixture's graph answered and the model states no waves")
	}

	// One tick per role, each with the role's own stage list — all pending.
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
			t.Errorf("%s's pipeline cell is %s, want the %v stages all pending",
				tickID, cellOf(got), want)
		}
		for _, stage := range got {
			if stage.State != StageStatePending {
				t.Errorf("%s's stage %s reads %q, want pending: wave 1 declares the cell, wave 2 fills it",
					tickID, stage.Stage, stage.State)
			}
		}
	}

	// The per-tick fields a dashboard drills into: nothing read yet, so
	// empty and null — never missing.
	for _, tick := range tickByID {
		if tick.Findings == nil {
			t.Errorf("%s's findings are nil, want the empty list: the field is required, and nil marshals as null", tick.TickID)
		}
		if len(tick.Findings) != 0 {
			t.Errorf("%s's findings are %+v, want empty: no wave-2 tick has filled them yet", tick.TickID, tick.Findings)
		}
		if tick.ParentTickID != nil {
			t.Errorf("%s's parent is %+v, want null: the graph layering owns indentation, not this tick", tick.TickID, *tick.ParentTickID)
		}
		if tick.DurationSeconds != nil {
			t.Errorf("%s's duration is %d, want null until the wave-2 tick measures it", tick.TickID, *tick.DurationSeconds)
		}
		if tick.Report != nil {
			t.Errorf("%s carries a report %+v, want null: no report has been read", tick.TickID, *tick.Report)
		}
		for _, try := range tick.Tries {
			if try.Tier != nil || try.Reason != nil || try.NextStep != nil {
				t.Errorf("%s's try %d carries tier/reason/next_step (%v/%v/%v), want all null: the attempt vocabulary this tick declares, the wave-2 ticks fill",
					tick.TickID, try.Try, try.Tier, try.Reason, try.NextStep)
			}
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

	// The verdict (wave 2, tick 7uv): healthy — the fixture's live run has
	// nothing wrong with it, its one stall warning ninety minutes old and
	// outside the window that reads — with what it got past on its own
	// listed as calm, never as alarms.
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("the verdict state is %q, want healthy", model.Health.Verdict.State)
	}
	if model.Health.Verdict.Summary != VerdictHealthy {
		t.Errorf("the verdict summary is %q, want %q", model.Health.Verdict.Summary, VerdictHealthy)
	}
	wantRecovered := []Recovery{
		{What: "net", Count: 1},
		{What: "interventions", Count: 1},
		{What: "wall clocks", Count: 1},
	}
	if !reflect.DeepEqual(model.Health.Verdict.Recovered, wantRecovered) {
		t.Errorf("the verdict's recovered list is %+v, want %+v", model.Health.Verdict.Recovered, wantRecovered)
	}

	// The cost lines (wave 2, tick 7uv): the spend split per river — the
	// decisions' own recorded usage metered, the fixture's one claude attempt
	// and two local pi/GLM attempts unmetered with NO number, never a
	// fabricated $0.00.
	decisionsUSD := 0.04
	wantLines := []CostLine{
		{Source: CostSourceDecisions, Metered: true, USD: &decisionsUSD, Attempts: 1, Basis: "usage recorded on decision records"},
		{Source: CostSourceClaude, Metered: false, USD: nil, Attempts: 1, Basis: "not metered (subscription)"},
		{Source: CostSourcePiLocal, Metered: false, USD: nil, Attempts: 2, Basis: "not metered"},
	}
	if !reflect.DeepEqual(model.Cost.Lines, wantLines) {
		t.Errorf("the cost lines are %+v, want %+v", model.Cost.Lines, wantLines)
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
