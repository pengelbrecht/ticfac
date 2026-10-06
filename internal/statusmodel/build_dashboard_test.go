package statusmodel

import (
	"reflect"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The DASHBOARD vocabulary (epic hn6, wave 1 — tick r5i): this tick declared
// every field the dashboard renders so the wave-2 ticks could fill them in
// parallel against a fixed shape — it computed NOTHING new beyond `recent`
// and the two graph copies (`epic_title`, `gloss`), every other new field
// came out at its honest empty value, and THIS test was the fence that said
// what the stubs must not quietly leave out. Two wave-2 halves are filled
// in below: the per-tick half is 3gk's (the cells, the parents, the
// durations, the findings and the try words are the derivations) and the
// run-level half is 7uv's (the health verdict and the cost lines at their
// real values). The worker half is ltg's, and this fixture still answers it
// null — nothing measures a worker here and nobody named its handle — the
// honest not-measured, not a stub the wave quietly left behind.

// TestBuildEmitsTheDashboardFieldsEmpty: a Build over the running-epic
// fixture emits every dashboard field — the role's own pipeline stage list
// per tick with the states the records derive, the findings the ticks
// reported, the durations the markers measure, the try words the feed
// states, the derived health verdict and the cost lines — and beside them
// the fields this fixture honestly answers null: the report and the parent
// unread, no worker activity, no handle. The wave-1 half stays: `recent`
// holds the last five feed lines oldest first, `epic_title` is copied off
// the graph, and every tick's `gloss` is copied off its graph task.
func TestBuildEmitsTheDashboardFieldsEmpty(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	model := Build(src)

	if model.Waves == nil {
		t.Fatal("the fixture's graph answered and the model states no waves")
	}

	// One tick per role, each with the role's own stage list — the lists the
	// renderer lays the cell out from, whatever the records state.
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
			t.Errorf("%s's pipeline cell is %s, want the %v stages",
				tickID, cellOf(got), want)
		}
	}

	// The cells' states, from the fixture's own records (wave 2, tick 3gk):
	// the closed tick done all the way, the in-flight tick at its work, and
	// the untouched ticks pending everywhere. The left-to-right fill and the
	// at-most-one live stage are what a dashboard draws from this.
	for tickID, want := range map[string]string{
		"nwj": "[claim:done work:done gate:done merged:done]",
		"6dh": "[claim:done work:active gate:pending merged:pending]",
		"89m": "[claim:pending work:pending gate:pending merged:pending]",
		"152": "[claim:pending work:pending gate:pending merged:pending]",
		"xbp": "[claim:pending review:pending closed:pending]",
		"rrl": "[claim:pending work:pending ci:pending closed:pending]",
	} {
		if got := cellOf(tickByID[tickID].Pipeline); got != want {
			t.Errorf("%s's pipeline cell is %s, want %s", tickID, got, want)
		}
	}

	// The per-tick fields a dashboard drills into: the derivation's own
	// answers for the ticks the fixture gave records to, and null — never
	// missing — where nothing states a fact.
	for _, tick := range tickByID {
		if tick.Findings == nil {
			t.Errorf("%s's findings are nil, want the empty list: the field is required, and nil marshals as null", tick.TickID)
		}
		if tick.ParentTickID != nil {
			t.Errorf("%s's parent is %q, want null: nothing places the row — the fixture's absorption names no source tick",
				tick.TickID, *tick.ParentTickID)
		}
		if tick.Report != nil {
			t.Errorf("%s carries a report %+v, want null: no report has been read", tick.TickID, *tick.Report)
		}
	}
	// nwj reported one finding off its own attempt; the absorption the
	// fixture carries decided a DIFFERENT finding, so the draft's gating is
	// the honest no-answer.
	nwjFindings := tickByID["nwj"].Findings
	if len(nwjFindings) != 1 || nwjFindings[0].Key != "46b634a4f894acc04534dd6e9b70b677d68c39f3b66b98e820613ac7dd8c6ce2" ||
		nwjFindings[0].Title != "README's command surface section predates the cobra+fang tree" ||
		nwjFindings[0].Gating != nil {
		t.Errorf("nwj's findings are %+v, want its own draft with gating null: no absorption decided it", nwjFindings)
	}
	// The durations: the closed tick to its gate's finish, the open one to
	// the model's now, and null where no dispatch marker states a start.
	for tickID, want := range map[string]*int64{
		"nwj": durationPtr(2898), // 03:19:05 dispatch, 04:07:23 gate finish
		"6dh": durationPtr(5400), // 04:00:00 dispatch, open: to the fixture's now
		"89m": nil,
		"152": nil,
		"xbp": nil,
		"rrl": nil,
	} {
		if got := tickByID[tickID].DurationSeconds; (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%s's duration is %v, want %v", tickID, got, want)
		}
	}
	// The try vocabulary: each try's tier off its own marker, the refused
	// try's reason off the feed line that refused it, and no next step on a
	// try that is in flight or closed.
	if tries := tickByID["nwj"].Tries; len(tries) != 1 || tries[0].Tier == nil || *tries[0].Tier != "strong" ||
		tries[0].Reason != nil || tries[0].NextStep != nil {
		t.Errorf("nwj's closed try carries %+v, want the marker's tier and no reason or next step",
			tickByID["nwj"].Tries)
	}
	if tries := tickByID["6dh"].Tries; len(tries) != 2 {
		t.Errorf("6dh carries %+v tries, want its two dispatches", tries)
	} else {
		if tries[0].Tier == nil || *tries[0].Tier != "strong" {
			t.Errorf("6dh's first try carries tier %v, want its marker's own strong", tries[0].Tier)
		}
		if tries[0].Reason == nil || *tries[0].Reason != "the integrated gate refused attempt 2 of 6dh" {
			t.Errorf("6dh's first try reason is %v, want the gate_failed line's own detail",
				tries[0].Reason)
		}
		if tries[0].NextStep != nil {
			t.Errorf("6dh's first try carries next_step %q, want null: only the last try states one", *tries[0].NextStep)
		}
		if tries[1].Tier == nil || *tries[1].Tier != "strong" || tries[1].Reason != nil || tries[1].NextStep != nil {
			t.Errorf("6dh's in-flight try carries %+v, want the marker's tier and nothing else", tries[1])
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

// durationPtr is the nullable-duration helper the fence's map wants.
func durationPtr(seconds int64) *int64 { return &seconds }

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
