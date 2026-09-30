package statusmodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
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
	// by TestTheDashboardGoldenAgreesWithThePipelineDerivation below.
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

// TestTheDashboardGoldenAgreesWithThePipelineDerivation: the golden is a
// rendering fixture, not a Build output, so no suite derives it — the only
// thing that keeps it saying what the wave-2 derivation (tick 3gk) actually
// produces is the rule read back over it. Tick 378 found the gap: v7z's ci
// cell read active beside the golden's own red CI, and 46x's superseded try
// carried a next step, both values decorateTicks can never state — and the
// wave-3 renderers and the phone page take this golden as THE shape fixture,
// so the illustration and the model it illustrates disagreed, the exact
// failure A5's one-model rule exists to prevent. Three agreements, each a
// rule 3gk spelled:
//
//   - a cell is its role's own stage list, filled left to right — done
//     stages first, at most one live stage, every stage behind the first
//     non-done one pending;
//   - a closeout's ci stage says what the model's own CI answer makes the
//     derivation say — a red CI is that stage's FAILURE, not its activity;
//   - a try's next step exists only on the last try of a refusal, and a
//     reason only on a refusal.
func TestTheDashboardGoldenAgreesWithThePipelineDerivation(t *testing.T) {
	t.Parallel()
	_, _, goldens := bundleFixture(t)
	raw, ok := goldens[dashboardGoldenName]
	if !ok {
		t.Fatalf("the contract carries no golden named %q", dashboardGoldenName)
	}
	var model Model
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatalf("the dashboard golden does not decode into the Go Model: %v", err)
	}

	for _, wave := range deref(model.Waves) {
		for _, tick := range wave.Ticks {
			if !stagesOf(tick.Pipeline, roleStages(tick.Role)) {
				t.Errorf("%s's cell is %s, want the %v stages of its role",
					tick.TickID, cellOf(tick.Pipeline), roleStages(tick.Role))
				continue
			}
			filled, live := true, 0
			for _, stage := range tick.Pipeline {
				switch stage.State {
				case StageStateDone:
					if !filled {
						t.Errorf("%s's cell is %s: %s is done behind a live stage",
							tick.TickID, cellOf(tick.Pipeline), stage.Stage)
					}
				case StageStateActive, StageStateFailed:
					if !filled {
						t.Errorf("%s's cell is %s: %s is live behind another live stage",
							tick.TickID, cellOf(tick.Pipeline), stage.Stage)
					}
					filled = false
					live++
				case StageStatePending:
					filled = false
				default:
					t.Errorf("%s's %s stage carries the unknown state %q", tick.TickID, stage.Stage, stage.State)
				}
			}
			if live > 1 {
				t.Errorf("%s's cell is %s: a tick is IN one stage, never %d",
					tick.TickID, cellOf(tick.Pipeline), live)
			}
			if ci := stageOf(tick.Pipeline, StageCI); ci != nil {
				if want := goldenCIStateOf(model, tick); ci.State != want {
					ciAnswer := "absent"
					if model.CI != nil {
						ciAnswer = model.CI.State
					}
					t.Errorf("%s's ci stage is %s, want %s: the model's own CI answer is %q, and the cell and the PR cannot say two things about one another",
						tick.TickID, ci.State, want, ciAnswer)
				}
			}
			for i := range tick.Tries {
				try := &tick.Tries[i]
				last := i == len(tick.Tries)-1
				refused := try.Outcome == TryRejected || try.Outcome == TryGateFailed
				if try.Reason != nil && !refused {
					t.Errorf("%s's try %d closed as %s and still carries the reason %q: a reason is a refusal's own word",
						tick.TickID, try.Try, try.Outcome, *try.Reason)
				}
				if try.NextStep != nil && (!last || !refused) {
					t.Errorf("%s's try %d (attempt %d, %s) carries the next step %q: the derivation states one on the last try of a refusal only",
						tick.TickID, try.Try, try.Attempt, try.Outcome, *try.NextStep)
				}
			}
		}
	}
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
