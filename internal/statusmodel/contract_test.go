package statusmodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
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
		tryWhyAndNext          bool
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
				if try.Reason != nil && try.NextStep != nil {
					tryWhyAndNext = true
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
	if !tryTier || !tryWhyAndNext {
		t.Errorf("no try carries tier/reason/next_step (tier=%v reason+next=%v): the rejected-try vocabulary the first-use bugs name is unexercised",
			tryTier, tryWhyAndNext)
	}
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
