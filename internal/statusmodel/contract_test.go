package statusmodel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contracts/status-model.json — the bundle fixture for `ticfac.status.v1`.
//
// 6dh cut this fixture as a package-local file with one reader; the factory's
// phone status page (i1r, cloudflare/src/status.ts) became the second reader,
// and the fixture moved into the contract bundle when ticfac took it over
// (tick 4i8). The SHAPE half of its execution lives beside the bundle now
// (internal/contracts/parity/status_model_test.go): goldens admitted,
// negatives refused with the pinned messages, the Go Model round-tripped.
//
// What could not move is here, because it binds the BUILDER: a Model this
// package's Build produces out of Sources must validate against the same
// schema the fixture pins — the assembled answer and the pinned shape are one
// claim, not two documents that happen to agree today.

// bundleFixture reads the status model contract from the bundle, with its
// golden documents: the shape half of the fixture's execution lives in the
// parity suite, and the two bindings that live HERE — the builder and the
// dashboard golden the hn6 waves render against — both need them.
func bundleFixture(t *testing.T) (*schema.Schema, map[string]*schema.Schema, map[string]json.RawMessage) {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatalf("locate the contract bundle: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status-model.json"))
	if err != nil {
		t.Fatalf("read the status model contract: %v", err)
	}
	var fixture struct {
		Records map[string]struct {
			SchemaID string          `json:"schema_id"`
			Schema   json.RawMessage `json:"schema"`
		} `json:"records"`
		Defs   map[string]json.RawMessage `json:"$defs"`
		Golden map[string]json.RawMessage `json:"golden"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the status model contract does not parse: %v", err)
	}
	record, ok := fixture.Records["status_model"]
	if !ok {
		t.Fatal("the status model contract declares no status_model record")
	}
	if record.SchemaID != SchemaID {
		t.Fatalf("the record names schema_id %q, want %q", record.SchemaID, SchemaID)
	}
	parsed, err := schema.ParseSchema(record.Schema)
	if err != nil {
		t.Fatalf("the status model schema does not parse: %v", err)
	}
	defs, err := schema.ParseDefs(fixture.Defs)
	if err != nil {
		t.Fatalf("the status model contract's $defs do not parse: %v", err)
	}
	return parsed, defs, fixture.Golden
}

// TestTheContractBindsTheBuilder: a Model the builder produces out of Sources
// marshals and validates against the contract's own schema. The Sources are
// the build_test fixture's running epic.
func TestTheContractBindsTheBuilder(t *testing.T) {
	record, defs, _ := bundleFixture(t)
	model := Build(runningEpicSources())
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("the built model does not marshal: %v", err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if problems := schema.Validate(record, defs, document); len(problems) > 0 {
		t.Errorf("a model the builder produced is refused by the contract:\n%s\nmodel:\n%s",
			strings.Join(problems, "\n"), raw)
	}
}

// dashboardGoldenName is the name the hn6 waves pin the wave-1 golden by:
// the wave-3 renderers and the phone page reference it by name, and nothing
// else in this repository does — the parity suite iterates the goldens and
// the TS validator replays them, so a rename would leave every existing test
// green while the fixture the waves test against stops existing. The pin
// lives here, in the package whose acceptance command runs it.
const dashboardGoldenName = "dashboard"

// TestTheContractBindsTheDashboardGolden: the dashboard golden (epic hn6,
// wave 1 — tick r5i), held to what the tick says it is, in the package the
// tick's own acceptance command runs. Four claims, none of them held
// anywhere else at this seam:
//
//   - the golden exists under the name the waves pin (above);
//   - the schema admits it — a golden that "mostly" validates is a
//     fixture that certifies a shape it does not hold;
//   - the Go Model round-trips it — the field-for-field discipline that
//     catches a field the type drops to a stray omitempty, or one it grows
//     that the fixture does not know, before it breaks a renderer;
//   - it stays POPULATED at the anchors a dashboard renders: it is the
//     rendering fixture for the wave-3 ticks and the phone page, and a
//     golden that quietly decayed into valid-but-empty would leave them
//     testing nothing while every suite stayed green.
//
// And one agreement: the closed vocabularies the contract spells and the Go
// builder spells are the same sets. The contract's own description says the
// stage lists are "spelled once in the Go builder and once here, so the two
// cannot drift" — this is that sentence made executable, for every enum the
// dashboard renders by name.
func TestTheContractBindsTheDashboardGolden(t *testing.T) {
	t.Parallel()
	record, defs, goldens := bundleFixture(t)
	raw, ok := goldens[dashboardGoldenName]
	if !ok {
		t.Fatalf("the contract carries no golden named %q — the wave-3 renderers and the phone page reference it by name",
			dashboardGoldenName)
	}

	// Admitted: no violation at all.
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("the dashboard golden does not parse: %v", err)
	}
	if problems := schema.Validate(record, defs, document); len(problems) > 0 {
		t.Fatalf("the dashboard golden is refused by the schema it is the golden of:\n%s",
			strings.Join(problems, "\n"))
	}

	// Round-tripped: what the golden states and what the Go Model marshals
	// are the same document.
	var model Model
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatalf("the dashboard golden does not decode into the Go Model: %v", err)
	}
	if model.SchemaVersion != SchemaVersion {
		t.Fatalf("the dashboard golden carries schema_version %d, want %d", model.SchemaVersion, SchemaVersion)
	}
	marshaled, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("the decoded golden does not re-marshal: %v", err)
	}
	var remarshaled any
	if err := json.Unmarshal(marshaled, &remarshaled); err != nil {
		t.Fatalf("the re-marshaled golden does not parse: %v", err)
	}
	if !reflect.DeepEqual(document, remarshaled) {
		t.Errorf("the Go Model does not round-trip the dashboard golden:\n got %s", marshaled)
	}

	// Populated: the anchors the tick's golden spec names, as guards. Each
	// is a boolean over the decoded Model so a decay reads as a missing
	// anchor, not as a string comparison against a document nobody renders
	// from directly.
	var (
		doneStage, activeStage bool
		parentUnder            bool
		tryTier                bool
		tryRefusedReason       bool
		activityDrawn          bool
		handleNamed            bool
		meteredNumber          bool
		unmeteredNull          bool
	)
	for _, wave := range deref(model.Waves) {
		for _, tick := range wave.Ticks {
			for _, stage := range tick.Pipeline {
				switch stage.State {
				case StageStateDone:
					doneStage = true
				case StageStateActive:
					activeStage = true
				}
			}
			if tick.ParentTickID != nil {
				parentUnder = true
			}
			for _, try := range tick.Tries {
				if try.Tier != nil {
					tryTier = true
				}
				if (try.Outcome == TryRejected || try.Outcome == TryGateFailed) && try.Reason != nil {
					tryRefusedReason = true
				}
			}
		}
	}
	for _, worker := range derefWorkers(model.Workers) {
		if worker.Activity != nil && len(worker.Activity.Buckets) > 0 {
			activityDrawn = true
		}
		if worker.Handle != nil {
			handleNamed = true
		}
	}
	for _, line := range model.Cost.Lines {
		if line.Metered && line.USD != nil {
			meteredNumber = true
		}
		if !line.Metered && line.USD == nil {
			unmeteredNull = true
		}
	}
	if !doneStage || !activeStage {
		t.Errorf("the golden's pipeline cells carry no done/active stage (done=%v active=%v): a dashboard fills left to right, and the rendering fixture must show it filled",
			doneStage, activeStage)
	}
	if !parentUnder {
		t.Error("no tick carries parent_tick_id: the repair-child row the tick's golden spec names is gone")
	}
	if !tryTier || !tryRefusedReason {
		t.Errorf("no try carries tier, or no refused try carries its reason (tier=%v reason=%v): the rejected-try vocabulary the first-use bugs name is unexercised",
			tryTier, tryRefusedReason)
	}
	// The try anchor stops at the reason (tick 378): a next step is stated on
	// the LAST try of a refusal only, and this golden's one refusal was
	// superseded by a live try, so the derivation states no next step anywhere
	// in it — demanding one here would force the fixture back into the
	// contradiction this tick removed. WHERE a next step may exist is pinned
	// by TestEveryGoldenAgreesWithThePipelineDerivation below.
	if !activityDrawn || !handleNamed {
		t.Errorf("no worker carries activity buckets and a handle (activity=%v handle=%v): the workers panel's fixture is empty",
			activityDrawn, handleNamed)
	}
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("the golden's verdict state is %q, want %q", model.Health.Verdict.State, VerdictHealthy)
	}
	if len(model.Health.Verdict.Recovered) == 0 {
		t.Error("the golden's healthy verdict recovered nothing: the recovered list is half the claim the headline makes")
	}
	if !meteredNumber || !unmeteredNull {
		t.Errorf("the golden's cost lines carry no metered-with-a-number and unmetered-without-one pair (metered=%v unmetered=%v): the never-$0.00 rule's fixture is gone",
			meteredNumber, unmeteredNull)
	}
	if n := len(model.Recent); n != 5 {
		t.Errorf("the golden carries %d recent events, want the full 5-line tail", n)
	}

	// Agreeing: the closed vocabularies the contract spells and the Go
	// builder spells are the same sets.
	enumAgrees(t, "$defs.pipeline_stage", defs["pipeline_stage"].Enum,
		StageClaim, StageWork, StageReview, StageGate, StageCI, StageMerged, StageClosed)
	enumAgrees(t, "$defs.pipeline_state", defs["pipeline_state"].Enum,
		StageStatePending, StageStateActive, StageStateDone, StageStateFailed)
	enumAgrees(t, "$defs.health_verdict.properties.state", defs["health_verdict"].Properties["state"].Enum,
		VerdictHealthy, VerdictDegraded, VerdictStopped)
	enumAgrees(t, "$defs.cost_line.properties.source", defs["cost_line"].Properties["source"].Enum,
		CostSourceDecisions, CostSourceWorkersAI, CostSourceClaude, CostSourcePiLocal, CostSourceOther)
}

// TestEveryGoldenAgreesWithThePipelineDerivation: the goldens are rendering
// fixtures, not Build outputs, so no suite derives them — the only thing that
// keeps them saying what the wave-2 derivation (tick 3gk) actually produces
// is the rule read back over them. Tick 378 found the gap in the dashboard
// golden alone: v7z's ci cell read active beside the golden's own red CI, and
// 46x's superseded try carried a next step. Tick oro found the same class
// still live one golden over, in status_model_running_wave, which this guard
// did not read: nwj read state "closed" with a closed try beside an
// all-pending cell — the derivation makes a closed tick claim/work/gate/merged
// all done — and 6dh read state "dispatched" beside the same, where a dispatch
// marker alone makes claim done and work active. The guard is therefore EVERY
// golden's, and the state agreements below are the ones those two
// contradictions name. Each rule is faithful to the production code and reads
// only what the golden document itself states — the cell, the tick's own
// state, the try history, the feed tail the model carries:
//
//   - a cell is its role's own stage list, filled left to right — done
//     stages first, at most one live stage, every stage behind the first
//     non-done one pending;
//   - a closeout's ci stage says what the model's own CI answer makes the
//     derivation say — a red CI is that stage's FAILURE, not its activity;
//   - a try's next step exists only on the last try of a refusal, and a
//     reason only on a refusal;
//   - a claim is never a stage a run is in or fails at, and any try — one is
//     cut per dispatch marker — makes it done;
//   - a tick's own state pins its work, gate and end stages once a dispatch
//     exists: a state that answers the work (reported, integrated, closed)
//     makes work done and a closed or integrated one makes gate and end
//     done, a dispatched state leaves work done-or-active, a reported
//     current try makes its gate done where the cell's fill reaches it, and
//     nothing dispatched leaves the gate pending;
//   - a failed work stage names a refused current try nothing stands
//     behind, and a done merged or closed stage names a tick the records
//     closed (a role tick's close stays pending behind a mere integration).
//
// Tick fq0 added the four VALUE rules the cell rules never read — duration,
// elapsed, progress and gates — after finding the same drift live in values
// no rule read: durations null beside stamps that measure them, an elapsed
// that contradicted the document's own generated_at, progress that counted
// fourteen ticks beside a waves list that lists none of them, and a gates
// array that backed no closed tick's duration. Each mirrors one builder
// branch and reads only what the golden document itself states — the try
// stamps, the gates array, the workers panel, the waves list, generated_at:
//
//   - a tick's duration is its earliest parseable try stamp to its close —
//     the latest gate record naming the tick, else its closed line, else
//     generated_at while it is open — and null when no stamp states a start;
//   - a tick's elapsed is its current attempt's dispatch stamp to
//     generated_at while that attempt is live (a standing worker answers
//     for it, or the state says dispatched or reported), null otherwise —
//     and a worker's elapsed is the same span for the attempt the census
//     names;
//   - progress counts the ticks and waves the document lists, in the same
//     loop that lists them: a null or empty waves list answers null
//     counters — never numbers — and a wave is done when every tick in it
//     closed, the first wave that is not done is the active frontier, and
//     the waves behind it are upcoming;
//   - the gates array is sorted by key then started_at, and every closed
//     tick's duration traces to a stamp the document states — a gate
//     record naming the tick, or its closed line — because the array is
//     every evidence record and durationOf measures the close to the
//     latest one naming the tick.
//
// The rules read the RENDERED cell, and the fill can only ever downgrade a
// stage to pending (pipelineCell): a rendered done, active or failed stage is
// always the derivation's own answer, while a rendered pending one is the
// answer OR the fill parking it — so the rules that DEMAND a value demand it
// only where the fill provably reaches the stage (a try means the claim is
// done; a work-answered state means the work stage is), and the rules that
// FORBID a value forbid it wherever the cell shows it.
func TestEveryGoldenAgreesWithThePipelineDerivation(t *testing.T) {
	t.Parallel()
	_, _, goldens := bundleFixture(t)
	if len(goldens) == 0 {
		t.Fatal("the contract carries no golden document")
	}
	names := make([]string, 0, len(goldens))
	for name := range goldens {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var model Model
			if err := json.Unmarshal(goldens[name], &model); err != nil {
				t.Fatalf("the golden %s does not decode into the Go Model: %v", name, err)
			}
			now, err := time.Parse(time.RFC3339, model.GeneratedAt)
			if err != nil {
				t.Fatalf("the golden %s carries generated_at %q, which does not parse: every duration and elapsed the guard derives is measured against it", name, model.GeneratedAt)
			}
			goldenProgressAgreesWithTheWaves(t, name, &model)
			goldenGatesAgreeWithTheRecords(t, name, &model)
			goldenWorkersAgreeWithTheStamps(t, name, &model, now)
			for _, wave := range deref(model.Waves) {
				for i := range wave.Ticks {
					goldenTickAgreesWithTheDerivation(t, name, &model, wave.Ticks[i], now)
				}
			}
		})
	}
}

// goldenTickAgreesWithTheDerivation reads one golden tick back over the
// pipeline derivation's rules. Every rule names the branch it mirrors
// (pipeline.go), and every fact it reads is one the golden document itself
// carries, so a rule fires only on a value the derivation can never produce.
func goldenTickAgreesWithTheDerivation(t *testing.T, golden string, model *Model, tick Tick, now time.Time) {
	t.Helper()
	where := fmt.Sprintf("golden %s, tick %s", golden, tick.TickID)

	if !stagesOf(tick.Pipeline, roleStages(tick.Role)) {
		t.Errorf("%s's cell is %s, want the %v stages of its role",
			where, cellOf(tick.Pipeline), roleStages(tick.Role))
		return
	}
	filled, live := true, 0
	for _, stage := range tick.Pipeline {
		switch stage.State {
		case StageStateDone:
			if !filled {
				t.Errorf("%s's cell is %s: %s is done behind a live stage",
					where, cellOf(tick.Pipeline), stage.Stage)
			}
		case StageStateActive, StageStateFailed:
			if !filled {
				t.Errorf("%s's cell is %s: %s is live behind another live stage",
					where, cellOf(tick.Pipeline), stage.Stage)
			}
			filled = false
			live++
		case StageStatePending:
			filled = false
		default:
			t.Errorf("%s's %s stage carries the unknown state %q", where, stage.Stage, stage.State)
		}
	}
	if live > 1 {
		t.Errorf("%s's cell is %s: a tick is IN one stage, never %d",
			where, cellOf(tick.Pipeline), live)
	}
	if ci := stageOf(tick.Pipeline, StageCI); ci != nil {
		if want := goldenCIStateOf(*model, tick); ci.State != want {
			ciAnswer := "absent"
			if model.CI != nil {
				ciAnswer = model.CI.State
			}
			t.Errorf("%s's ci stage is %s, want %s: the model's own CI answer is %q, and the cell and the PR cannot say two things about one another",
				where, ci.State, want, ciAnswer)
		}
	}
	for i := range tick.Tries {
		try := &tick.Tries[i]
		last := i == len(tick.Tries)-1
		refused := try.Outcome == TryRejected || try.Outcome == TryGateFailed
		if try.Reason != nil && !refused {
			t.Errorf("%s's try %d closed as %s and still carries the reason %q: a reason is a refusal's own word",
				where, try.Try, try.Outcome, *try.Reason)
		}
		if try.NextStep != nil && (!last || !refused) {
			t.Errorf("%s's try %d (attempt %d, %s) carries the next step %q: the derivation states one on the last try of a refusal only",
				where, try.Try, try.Attempt, try.Outcome, *try.NextStep)
		}
	}

	// The state agreements (tick oro). current and currentTry are the
	// derivation's own reading of the document — stageStateOf's: the
	// checkpoint's attempt where the document names one, the try cut for it,
	// and the standing worktree the workers panel carries.
	hasTries := len(tick.Tries) > 0 // one try is cut per dispatch marker
	current := 0
	if tick.Attempt != nil {
		current = *tick.Attempt
	}
	currentTry := tryOfAttempt(tick.Tries, current)
	stands := goldenStandsFor(model, tick.TickID, current)

	// claimState: done once any record states a dispatch — a marker (a try)
	// or the claimed line; pending before that; never active or failed.
	if claim := stageOf(tick.Pipeline, StageClaim); claim != nil {
		if claim.State != StageStatePending && claim.State != StageStateDone {
			t.Errorf("%s's claim stage is %s: claimState answers pending or done and nothing else — a claim is not a stage a run is in or fails at",
				where, claim.State)
		}
		if hasTries && claim.State != StageStateDone {
			t.Errorf("%s carries %d try(ies) and its claim stage is %s: a try is cut one per dispatch marker, and a dispatch marker alone makes the claim done",
				where, len(tick.Tries), claim.State)
		}
	}
	// workState — and a role tick's review, which is that role's work.
	for _, stage := range tick.Pipeline {
		if stage.Stage != StageWork && stage.Stage != StageReview {
			continue
		}
		switch tick.State {
		case tickReported, tickIntegrated, tickClosed:
			if hasTries && stage.State != StageStateDone {
				t.Errorf("%s's %s stage is %s, want done: the tick's own state is %q — a state that answers the work — and the dispatch the tries carry lets the cell's fill reach the stage",
					where, stage.Stage, stage.State, tick.State)
			}
		case tickDispatched:
			if hasTries && stage.State != StageStateDone && stage.State != StageStateActive {
				t.Errorf("%s's %s stage is %s: the tick's own state is %q — the dispatch stands — and the derivation leaves that stage done (a collect answered) or active (the dispatch), never %s",
					where, stage.Stage, stage.State, tick.State, stage.State)
			}
		}
		if stage.State == StageStateActive && tick.State != tickDispatched && !stands {
			t.Errorf("%s's %s stage is active, but the document states neither a dispatched tick nor a standing worker for attempt %d: workState states active while a dispatch stands and nothing else",
				where, stage.Stage, current)
		}
		if stage.State == StageStateFailed {
			outcome := "none"
			if currentTry != nil {
				outcome = currentTry.Outcome
			}
			refused := currentTry != nil && currentTry.Outcome == TryRejected &&
				!laterTryStands(tick, current) && !goldenSeesLaterDispatch(model, tick.TickID, current)
			if !refused {
				t.Errorf("%s's %s stage is failed, but the document names no refused current try nothing stands behind (state %q, current try %s): workState states failed on a rejected current try without a later dispatch and nothing else",
					where, stage.Stage, tick.State, outcome)
			}
		}
	}
	// gateState, scoped to the current attempt the document names.
	if gate := stageOf(tick.Pipeline, StageGate); gate != nil {
		switch tick.State {
		case tickClosed, tickIntegrated:
			if hasTries && gate.State != StageStateDone {
				t.Errorf("%s's gate stage is %s, want done: the tick's own state is %q — gateState makes a closed or integrated tick's gate done — and the dispatch the tries carry lets the cell's fill reach the stage",
					where, gate.State, tick.State)
			}
		default:
			if current == 0 && gate.State != StageStatePending {
				t.Errorf("%s's gate stage is %s, want pending: the document names no current attempt, and gateState answers pending before any attempt",
					where, gate.State)
			}
		}
		if currentTry != nil && currentTry.Outcome == TryReported && gate.State != StageStateDone &&
			fillReaches(tick.Pipeline, StageGate) {
			t.Errorf("%s's gate stage is %s, want done: the current try (attempt %d) closed as %q — the records' all-pass evidence — and gateState makes that try's gate done where the cell's fill reaches it",
				where, gate.State, currentTry.Attempt, currentTry.Outcome)
		}
	}
	// endState: the merged stage of an implement tick, the closed stage of a
	// role tick — done on the close, and on an integration for merged only.
	if end := stageOf(tick.Pipeline, StageMerged); end != nil {
		if end.State == StageStateDone && tick.State != tickClosed && tick.State != tickIntegrated {
			t.Errorf("%s's merged stage is done, but the tick's own state is %q: endState makes the merge done on a closed or integrated tick and nothing else",
				where, tick.State)
		}
		if hasTries && (tick.State == tickClosed || tick.State == tickIntegrated) && end.State != StageStateDone {
			t.Errorf("%s's merged stage is %s, want done: the tick's own state is %q and the dispatch the tries carry lets the cell's fill reach the stage — endState makes that tick's merge done",
				where, end.State, tick.State)
		}
	}
	if end := stageOf(tick.Pipeline, StageClosed); end != nil {
		if end.State == StageStateDone && tick.State != tickClosed {
			t.Errorf("%s's closed stage is done, but the tick's own state is %q: a role tick's end stage is done on a closed tick and nothing else — an integrated role tick's close is still pending",
				where, tick.State)
		}
		if hasTries && tick.State == tickClosed && end.State != StageStateDone {
			t.Errorf("%s's closed stage is %s, want done: the tick's own state is %q and the dispatch the tries carry lets the cell's fill reach the stage — endState makes that tick's close done",
				where, end.State, tick.State)
		}
	}

	// The value agreements (tick fq0): the durations, the elapsed and the
	// worker spans the stamps the document itself carries measure.
	goldenDurationAgreesWithTheStamps(t, golden, model, tick, now)
	goldenElapsedAgreesWithTheStamps(t, golden, model, tick, now)
}

// stageOf is one named stage's own entry in a cell, or nil when the role's
// stage list does not carry it.
func stageOf(cell []PipelineStage, name string) *PipelineStage {
	for i := range cell {
		if cell[i].Stage == name {
			return &cell[i]
		}
	}
	return nil
}

// fillReaches says whether the cell's fill provably reaches the named stage:
// every stage before it is rendered done, so the stage's own value is the
// derivation's answer and not the fill parking it — pipelineCell downgrades
// every stage past the first non-done one to pending, whatever the records
// would have said.
func fillReaches(cell []PipelineStage, stage string) bool {
	for i := range cell {
		if cell[i].Stage == stage {
			return true
		}
		if cell[i].State != StageStateDone {
			return false
		}
	}
	return false
}

// laterTryStands is the marker half of hasLaterDispatch over a golden: one
// try is cut per dispatch marker, so a try the document carries at a higher
// attempt is a dispatch the records state happened later.
func laterTryStands(tick Tick, attempt int) bool {
	for _, try := range tick.Tries {
		if try.Attempt > attempt {
			return true
		}
	}
	return false
}

// goldenSeesLaterDispatch is the feed half of hasLaterDispatch, over the
// feed the document itself shows — the recent tail and liveness.last_event.
// A dispatch line beyond the tail is a line the document cannot state, and
// the rule stays silent about what it cannot see, the same trade
// goldenCIStateOf's held-line branch makes.
func goldenSeesLaterDispatch(model *Model, tickID string, attempt int) bool {
	for i := range model.Recent {
		if goldenLineDispatchesLater(&model.Recent[i], tickID, attempt) {
			return true
		}
	}
	return model.Liveness.LastEvent != nil &&
		goldenLineDispatchesLater(model.Liveness.LastEvent, tickID, attempt)
}

// goldenLineDispatchesLater is one feed line's answer to "does this line
// state a dispatch of this tick past this attempt" — the stages
// hasLaterDispatch reads, on the tick and attempt it names.
func goldenLineDispatchesLater(line *runfeed.Event, tickID string, attempt int) bool {
	if line.TickID == nil || *line.TickID != tickID || line.Attempt == nil || *line.Attempt <= attempt {
		return false
	}
	switch line.Stage {
	case reconcile.StageDispatched, reconcile.StageRedispatched, reconcile.StageRepairDispatched:
		return true
	}
	return false
}

// goldenStandsFor is the census half of workState's and isLive's standing
// question, over the workers panel the document carries: one entry per
// standing attempt, named by tick and attempt.
func goldenStandsFor(model *Model, tickID string, attempt int) bool {
	for _, worker := range derefWorkers(model.Workers) {
		if worker.TickID == tickID && worker.Attempt == attempt {
			return true
		}
	}
	return false
}

// goldenCIStateOf states what the pipeline derivation (pipeline.go's
// ciState) makes of a closeout's ci stage over the model's own CI answer —
// the same facts the phase bar and the cell render from, so the two cannot
// say two things about one PR. The held-line branch reads the model's own
// tail (recent) rather than the whole feed the derivation reads: the
// goldens carry their CI, so the branch is here for a future golden that
// does not.
func goldenCIStateOf(model Model, tick Tick) string {
	switch tick.State {
	case tickClosed, tickIntegrated:
		return StageStateDone
	}
	if model.CI != nil {
		switch model.CI.State {
		case "green":
			return StageStateDone
		case "red", "failure":
			return StageStateFailed
		case "pending":
			return StageStateActive
		}
	}
	for i := range model.Recent {
		if model.Recent[i].Stage == reconcile.StageCloseoutHeld {
			return StageStateActive
		}
	}
	return StageStatePending
}

// derefWorkers keeps the range loop readable over the nullable workers
// slice — the workers' own deref, the same shape as the waves one.
func derefWorkers(workers *[]Worker) []Worker {
	if workers == nil {
		return nil
	}
	return *workers
}

// enumAgrees holds a contract enum and the Go vocabulary that spells the
// same closed set to each other: same length, every Go word inside the
// contract's list. The contract's pipeline description says the two
// spellings "cannot drift" — this is that claim, executable.
func enumAgrees(t *testing.T, where string, got []any, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s carries %d values, want the %d Go ones", where, len(got), len(want))
	}
	have := make(map[string]bool, len(got))
	for _, value := range got {
		word, ok := value.(string)
		if !ok {
			t.Errorf("%s carries the non-string %v", where, value)
			continue
		}
		have[word] = true
	}
	for _, word := range want {
		if !have[word] {
			t.Errorf("%s does not carry the Go vocabulary's %q: the two spellings of one closed set have drifted", where, word)
		}
	}
}

// --------------------------------------------- the value rules (tick fq0) ---

// goldenDurationAgreesWithTheStamps mirrors durationOf (pipeline.go): a
// tick's duration is its earliest parseable try stamp to its close — for a
// closed or integrated tick the latest gate record naming it, else its
// closed line, else generated_at while it is open — and null when no stamp
// states a start. The tries are the dispatch markers one per marker and the
// gates array is every evidence record, so both ends are document-complete
// facts the rule can demand from; where a closed tick's end appears NOWHERE
// the rule stays silent here and the gates rule speaks (its trace demand is
// the fixture-side standard the tick sets: a golden's every value must
// re-derive from the document's own bytes).
func goldenDurationAgreesWithTheStamps(t *testing.T, golden string, model *Model, tick Tick, now time.Time) {
	t.Helper()
	where := fmt.Sprintf("golden %s, tick %s", golden, tick.TickID)
	got := tick.DurationSeconds
	start, ok := goldenEarliestDispatch(tick)
	if !ok {
		if got != nil {
			t.Errorf("%s's duration is %d, want null: no try carries a parseable dispatch stamp, and durationOf states no span it cannot start",
				where, *got)
		}
		return
	}
	var end time.Time
	endKnown := false
	closedEnd := tick.State == tickClosed || tick.State == tickIntegrated
	if closedEnd {
		var latest time.Time
		for i := range model.Gates {
			gate := &model.Gates[i]
			if gate.TickID == nil || *gate.TickID != tick.TickID || gate.FinishedAt == "" {
				continue
			}
			at, err := time.Parse(time.RFC3339, gate.FinishedAt)
			if err != nil {
				continue
			}
			if latest.IsZero() || at.After(latest) {
				latest = at
			}
		}
		if !latest.IsZero() {
			end, endKnown = latest, true
		}
	}
	if !endKnown && !closedEnd {
		end, endKnown = now, true
	}
	if !endKnown {
		if line := goldenNewestLineFor(model, tick.TickID, reconcile.StageClosed); line != nil {
			if at, err := time.Parse(time.RFC3339, line.At); err == nil {
				end, endKnown = at, true
			}
		}
	}
	if !endKnown {
		// The gates rule demands the stamp; demanding a number here too
		// would demand the degenerate no-evidence fallback, which is the
		// live-run tolerance, not a fixture's answer.
		return
	}
	want := int64(end.Sub(start).Round(time.Second).Seconds())
	if want < 0 {
		want = 0
	}
	if got == nil || *got != want {
		t.Errorf("%s's duration is %s, want %d: durationOf measures the tick's earliest dispatch (%s) to %s",
			where, nullableSeconds(got), want, start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
}

// goldenElapsedAgreesWithTheStamps mirrors the elapsed rule (build.go's
// buildTick): the tick's elapsed is its CURRENT attempt's dispatch stamp to
// generated_at while that attempt is live — a standing worker answers for
// it, or the tick's own state says dispatched or reported — and null
// otherwise.
func goldenElapsedAgreesWithTheStamps(t *testing.T, golden string, model *Model, tick Tick, now time.Time) {
	t.Helper()
	where := fmt.Sprintf("golden %s, tick %s", golden, tick.TickID)
	got := tick.ElapsedSeconds
	current := 0
	if tick.Attempt != nil {
		current = *tick.Attempt
	}
	if current == 0 {
		if got != nil {
			t.Errorf("%s's elapsed is %d, want null: the document names no current attempt, and the elapsed rule states one only for a stamped current attempt",
				where, *got)
		}
		return
	}
	var marker time.Time
	markerKnown := false
	for _, try := range tick.Tries {
		if try.Attempt != current {
			continue
		}
		if at, err := time.Parse(time.RFC3339, try.DispatchedAt); err == nil {
			marker, markerKnown = at, true
			break
		}
	}
	if !markerKnown {
		if got != nil {
			t.Errorf("%s's elapsed is %d, want null: the current attempt's dispatch stamp states nothing parseable",
				where, *got)
		}
		return
	}
	if !isLive(tick.State, goldenStandsFor(model, tick.TickID, current)) {
		if got != nil {
			t.Errorf("%s's elapsed is %d, want null: the current attempt is not live — no standing worker answers for it and the state is %q",
				where, *got, tick.State)
		}
		return
	}
	want := int64(now.Sub(marker).Round(time.Second).Seconds())
	if got == nil || *got != want {
		t.Errorf("%s's elapsed is %s, want %d: the current attempt (attempt %d) dispatched at %s, and the elapsed rule measures it to generated_at (%s)",
			where, nullableSeconds(got), want, current, marker.Format(time.RFC3339), now.Format(time.RFC3339))
	}
}

// goldenWorkersAgreeWithTheStamps mirrors buildWorkers's elapsed: one
// standing attempt's elapsed is its dispatch marker's stamp to generated_at,
// and null where no parseable marker states one. The marker map is the
// listed ticks' tries — one try per dispatch marker — so a worker whose tick
// the waves list does not carry is a census the document cannot check, and
// the rule stays silent about it.
func goldenWorkersAgreeWithTheStamps(t *testing.T, golden string, model *Model, now time.Time) {
	t.Helper()
	stamps := map[string]time.Time{}
	listed := map[string]bool{}
	for _, wave := range deref(model.Waves) {
		for _, tick := range wave.Ticks {
			listed[tick.TickID] = true
			for _, try := range tick.Tries {
				if at, err := time.Parse(time.RFC3339, try.DispatchedAt); err == nil {
					stamps[fmt.Sprintf("%s#%d", tick.TickID, try.Attempt)] = at
				}
			}
		}
	}
	for _, worker := range derefWorkers(model.Workers) {
		if !listed[worker.TickID] {
			continue
		}
		where := fmt.Sprintf("golden %s, worker %s#%d", golden, worker.TickID, worker.Attempt)
		marker, ok := stamps[fmt.Sprintf("%s#%d", worker.TickID, worker.Attempt)]
		if !ok {
			if worker.ElapsedSeconds != nil {
				t.Errorf("%s's elapsed is %d, want null: the listed tries state no parseable dispatch stamp for the attempt the census names",
					where, *worker.ElapsedSeconds)
			}
			continue
		}
		want := int64(now.Sub(marker).Round(time.Second).Seconds())
		if worker.ElapsedSeconds == nil || *worker.ElapsedSeconds != want {
			t.Errorf("%s's elapsed is %s, want %d: the attempt dispatched at %s, and buildWorkers measures it to generated_at (%s)",
				where, nullableSeconds(worker.ElapsedSeconds), want, marker.Format(time.RFC3339), now.Format(time.RFC3339))
		}
	}
}

// goldenProgressAgreesWithTheWaves mirrors buildWaves: the progress counts
// are the SAME loop that lays the waves out — the ticks it lists, the wave
// states it derives — so a progress beside a null or empty waves list, or
// beside a list it does not count, is a value the builder cannot emit. A
// wave is done when every tick in it closed, the first wave that is not done
// is the active frontier and the waves behind it are upcoming; a tracker the
// model could not read, or one with no waves, answers null waves and null
// counters — buildWaves returns nil, never an empty list.
func goldenProgressAgreesWithTheWaves(t *testing.T, golden string, model *Model) {
	t.Helper()
	where := fmt.Sprintf("golden %s", golden)
	if model.Waves == nil || len(*model.Waves) == 0 {
		if model.Waves != nil {
			t.Errorf("%s carries an empty waves list: buildWaves returns null, never an empty list — 'no waves' and 'unread' both answer null", where)
		}
		if model.Progress.Ticks != nil {
			t.Errorf("%s's progress.ticks is {total %d, closed %d, open %d}, want null: buildWaves counts the ticks it lists in the same loop that lists them, and a null or empty waves list lists none",
				where, model.Progress.Ticks.Total, model.Progress.Ticks.Closed, model.Progress.Ticks.Open)
		}
		if model.Progress.Waves != nil {
			t.Errorf("%s's progress.waves is {total %d, done %d, active %d}, want null: buildWaves counts the waves it lists in the same loop that lists them, and a null or empty waves list lists none",
				where, model.Progress.Waves.Total, model.Progress.Waves.Done, model.Progress.Waves.Active)
		}
		return
	}
	waves := *model.Waves
	total, closed, done, active := 0, 0, 0, 0
	for i := range waves {
		wave := &waves[i]
		allClosed := true
		for j := range wave.Ticks {
			total++
			if wave.Ticks[j].State == tickClosed {
				closed++
			} else {
				allClosed = false
			}
		}
		wantState := WaveUpcoming
		switch {
		case allClosed:
			wantState = WaveDone
			done++
		case active == 0:
			wantState = WaveActive
			active = wave.Wave
		}
		if wave.State != wantState {
			t.Errorf("%s's wave %d reads %q, want %q: a wave is done when every tick in it closed, the first wave that is not done is the active frontier, and the waves behind it are upcoming",
				where, wave.Wave, wave.State, wantState)
		}
	}
	if got := model.Progress.Ticks; got == nil {
		t.Errorf("%s states no progress.ticks beside %d listed tick(s): buildWaves counts the ticks it lists in the same loop that lists them", where, total)
	} else if got.Total != total || got.Closed != closed || got.Open != total-closed {
		t.Errorf("%s's progress.ticks is {total %d, closed %d, open %d}, want {total %d, closed %d, open %d}: buildWaves counts the ticks the waves list, in the same loop that lists them",
			where, got.Total, got.Closed, got.Open, total, closed, total-closed)
	}
	if got := model.Progress.Waves; got == nil {
		t.Errorf("%s states no progress.waves beside %d listed wave(s): buildWaves counts the waves it lists in the same loop that lays them out", where, len(waves))
	} else if got.Total != len(waves) || got.Done != done || got.Active != active {
		t.Errorf("%s's progress.waves is {total %d, done %d, active %d}, want {total %d, done %d, active %d}: buildWaves counts the waves it lists, and active is the frontier wave's own number",
			where, got.Total, got.Done, got.Active, len(waves), done, active)
	}
}

// goldenGatesAgreeWithTheRecords mirrors buildGates: one gate per evidence
// record, sorted by key then started_at. And it holds the fixture-side
// standard this tick sets — every value derivable from the document alone —
// for the closed ticks' durations: a closed or integrated tick whose tries
// carry a start must have its close STATED, a gate record naming it or its
// closed line in the tail, because the array is every evidence record and
// durationOf measures the close to the latest one naming the tick. A closed
// tick whose duration traces to a stamp the document does not state is the
// fixture-drift class this tick removes — the gates array must back the
// closed ticks.
func goldenGatesAgreeWithTheRecords(t *testing.T, golden string, model *Model) {
	t.Helper()
	for i := 1; i < len(model.Gates); i++ {
		next, prev := &model.Gates[i], &model.Gates[i-1]
		if (next.Key != prev.Key && next.Key < prev.Key) ||
			(next.Key == prev.Key && next.StartedAt < prev.StartedAt) {
			t.Errorf("golden %s's gates array is not sorted by key then started_at: %q follows %q — buildGates sorts the evidence records",
				golden, next.Key, prev.Key)
		}
	}
	for _, wave := range deref(model.Waves) {
		for _, tick := range wave.Ticks {
			if tick.State != tickClosed && tick.State != tickIntegrated {
				continue
			}
			start, ok := goldenEarliestDispatch(tick)
			if !ok {
				continue
			}
			if goldenClosedEndIsStated(t, golden, model, tick, start) {
				continue
			}
			span := "no stated duration"
			if tick.DurationSeconds != nil {
				span = fmt.Sprintf("duration %d", *tick.DurationSeconds)
			}
			t.Errorf("golden %s's closed tick %s carries a %s that no gate record and no closed line states the end of: the gates array is every evidence record and durationOf measures the close to the latest one naming the tick, so the gates array must back the closed ticks' durations",
				golden, tick.TickID, span)
		}
	}
}

// goldenClosedEndIsStated answers whether the document states the named
// closed tick's end at all: a gate record naming the tick with a parseable
// finished_at, or its closed line in the document's tail view of the feed.
// Where a duration is stated, the stamp must equal start + duration — the
// value the duration claims to measure.
func goldenClosedEndIsStated(t *testing.T, golden string, model *Model, tick Tick, start time.Time) bool {
	t.Helper()
	stated := false
	for i := range model.Gates {
		gate := &model.Gates[i]
		if gate.TickID == nil || *gate.TickID != tick.TickID || gate.FinishedAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, gate.FinishedAt)
		if err != nil {
			continue
		}
		stated = true
		if tick.DurationSeconds != nil {
			if want := start.Add(time.Duration(*tick.DurationSeconds) * time.Second); !at.Equal(want) {
				t.Errorf("golden %s's closed tick %s carries duration %d, but the latest gate record naming it finished at %s, which measures %d from its first dispatch (%s): durationOf measures the close to the latest gate record naming the tick",
					golden, tick.TickID, *tick.DurationSeconds, at.Format(time.RFC3339),
					int64(at.Sub(start).Round(time.Second).Seconds()), start.Format(time.RFC3339))
			}
		}
	}
	if stated {
		return true
	}
	if line := goldenNewestLineFor(model, tick.TickID, reconcile.StageClosed); line != nil {
		if at, err := time.Parse(time.RFC3339, line.At); err == nil {
			stated = true
			if tick.DurationSeconds != nil {
				if want := start.Add(time.Duration(*tick.DurationSeconds) * time.Second); !at.Equal(want) {
					t.Errorf("golden %s's closed tick %s carries duration %d, but its closed line reads %s, which measures %d from its first dispatch (%s): with no gate record naming the tick, durationOf measures the close to the closed line",
						golden, tick.TickID, *tick.DurationSeconds, at.Format(time.RFC3339),
						int64(at.Sub(start).Round(time.Second).Seconds()), start.Format(time.RFC3339))
				}
			}
		}
	}
	return stated
}

// goldenEarliestDispatch is the tick's earliest parseable try stamp — the
// document's own copy of the dispatch markers durationOf reads.
func goldenEarliestDispatch(tick Tick) (time.Time, bool) {
	var earliest time.Time
	for _, try := range tick.Tries {
		at, err := time.Parse(time.RFC3339, try.DispatchedAt)
		if err != nil {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest, !earliest.IsZero()
}

// goldenNewestLineFor is the newest line of one stage the document's tail
// view of the feed holds for one tick — liveness.last_event is the feed's
// own last line, so when it matches it is the newest; otherwise the last
// match in the recent tail is, because the tail is the feed's end and
// nothing lies beyond it.
func goldenNewestLineFor(model *Model, tickID, stage string) *runfeed.Event {
	if last := model.Liveness.LastEvent; last != nil && last.Stage == stage &&
		last.TickID != nil && *last.TickID == tickID {
		return last
	}
	var newest *runfeed.Event
	for i := range model.Recent {
		line := &model.Recent[i]
		if line.Stage == stage && line.TickID != nil && *line.TickID == tickID {
			newest = line
		}
	}
	return newest
}

// nullableSeconds prints a nullable duration the way the document states it,
// so a refusal quotes the value it refuses.
func nullableSeconds(seconds *int64) string {
	if seconds == nil {
		return "null"
	}
	return strconv.FormatInt(*seconds, 10)
}
