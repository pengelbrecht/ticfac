package statusmodel

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The COST suite (epic hn6, wave 2 — tick 7uv): the honest cost lines, case
// by case, each built through Build — the model's own assembly — and each
// validated against the contract the acceptance names for every case. The
// base fixture is the running-epic shape build_test.go builds: one decision
// carrying usage, one claude attempt and two pi/GLM attempts on the local
// harness.

func float64Ptr(v float64) *float64 { return &v }

// jsonCostLines re-reads a built model's cost lines as the JSON a renderer
// parses — the shape the never-$0.00 rule is pinned against, not the Go
// struct, because the rule is about the document.
type jsonCostLine struct {
	Source  string   `json:"source"`
	Metered bool     `json:"metered"`
	USD     *float64 `json:"usd"`
}

func jsonCostLines(t *testing.T, model Model) []jsonCostLine {
	t.Helper()
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("the built model does not marshal: %v", err)
	}
	var doc struct {
		Cost struct {
			Lines []jsonCostLine `json:"lines"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the built model does not re-parse: %v", err)
	}
	return doc.Cost.Lines
}

// TestCostSplitsTheLocalRiversUnmetered: local attempts on claude and pi GLM
// with no decisions split into two unmetered lines, each stating usd null
// and saying why in its basis; recorded_usd is 0 because nothing measured
// anything — and the JSON carries no unmetered line with a number (case a).
func TestCostSplitsTheLocalRiversUnmetered(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Decisions = nil
	src.WorkerCost = nil
	model := Build(src)
	want := []CostLine{
		{Source: CostSourceClaude, Metered: false, USD: nil, Attempts: 1, Basis: "not metered (subscription)"},
		{Source: CostSourcePiLocal, Metered: false, USD: nil, Attempts: 2, Basis: "not metered"},
	}
	if !reflect.DeepEqual(model.Cost.Lines, want) {
		t.Errorf("the local rivers' lines are %+v\nwant %+v: claude on the subscription, pi/GLM unmeasured, both honest about it",
			model.Cost.Lines, want)
	}
	if model.Cost.RecordedUSD != 0 {
		t.Errorf("recorded_usd is %v with nothing metered, want 0", model.Cost.RecordedUSD)
	}
	for _, line := range jsonCostLines(t, model) {
		if !line.Metered && line.USD != nil {
			t.Errorf("the %s line is unmetered and carries the number %v: rule 7 forbids the fabricated $0.00",
				line.Source, *line.USD)
		}
	}
	assertAdmittedByTheContract(t, model)
}

// TestCostMetersTheCloudWorkersAIRun: a cloud run the factory stated a cost
// for carries one workers-ai line, metered with the factory's own
// gateway-backed number (case b) — and the same run without a stated number
// is unmetered and SAYS so, never a fabricated $0.00.
func TestCostMetersTheCloudWorkersAIRun(t *testing.T) {
	t.Parallel()
	cloudAttempts := []runstate.Attempt{
		attemptMarker(1, "nwj", "2026-09-27T03:19:05Z", "strong", "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "cloudflare-sandbox"),
		attemptMarker(2, "6dh", "2026-09-27T04:00:00Z", "strong", "@cf/zai-org/glm-5.3", "cloudflare-sandbox"),
	}

	src := runningEpicSources()
	src.Records.Decisions = nil
	src.Records.Attempts = cloudAttempts
	src.Standing = nil
	src.WorkerCost = &WorkerCostInput{USD: 0.41, Source: "gateway"}
	model := Build(src)
	want := []CostLine{
		{Source: CostSourceWorkersAI, Metered: true, USD: float64Ptr(0.41), Attempts: 2, Basis: "AI Gateway logs"},
	}
	if !reflect.DeepEqual(model.Cost.Lines, want) {
		t.Errorf("the cloud run's lines are %+v\nwant %+v: the factory's own number, metered, on the river it measured",
			model.Cost.Lines, want)
	}
	if model.Cost.RecordedUSD != 0.41 {
		t.Errorf("recorded_usd is %v, want 0.41 — the sum of the metered lines", model.Cost.RecordedUSD)
	}
	assertAdmittedByTheContract(t, model)

	// No stated number: the same river is unmetered, and its basis says
	// what is missing instead of wearing a $0.00.
	blind := runningEpicSources()
	blind.Records.Decisions = nil
	blind.Records.Attempts = cloudAttempts
	blind.Standing = nil
	blindModel := Build(blind)
	want = []CostLine{
		{Source: CostSourceWorkersAI, Metered: false, USD: nil, Attempts: 2,
			Basis: "not metered: this run's Workers AI calls are not joined to gateway logs"},
	}
	if !reflect.DeepEqual(blindModel.Cost.Lines, want) {
		t.Errorf("the unmeasured cloud run's lines are %+v\nwant %+v", blindModel.Cost.Lines, want)
	}
	if blindModel.Cost.RecordedUSD != 0 {
		t.Errorf("recorded_usd is %v with nothing metered, want 0", blindModel.Cost.RecordedUSD)
	}
	assertAdmittedByTheContract(t, blindModel)
}

// TestCostMetersTheDecisionsOwnUsage: the decision records' own usage is the
// one metered line — the existing sum, covering the decisions that carry
// usage — beside the unmetered local rivers (case c).
func TestCostMetersTheDecisionsOwnUsage(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())
	want := []CostLine{
		{Source: CostSourceDecisions, Metered: true, USD: float64Ptr(0.04), Attempts: 1, Basis: "usage recorded on decision records"},
		{Source: CostSourceClaude, Metered: false, USD: nil, Attempts: 1, Basis: "not metered (subscription)"},
		{Source: CostSourcePiLocal, Metered: false, USD: nil, Attempts: 2, Basis: "not metered"},
	}
	if !reflect.DeepEqual(model.Cost.Lines, want) {
		t.Errorf("the cost lines are %+v\nwant %+v", model.Cost.Lines, want)
	}
	if model.Cost.RecordedUSD != 0.04 {
		t.Errorf("recorded_usd is %v, want 0.04 from the one decision that carries usage", model.Cost.RecordedUSD)
	}
	if model.Cost.Attempts != 3 {
		t.Errorf("the model counts %d attempts, want 3 dispatch markers", model.Cost.Attempts)
	}
	assertAdmittedByTheContract(t, model)
}

// TestCostSortsEveryAttemptIntoItsRiver: the provenance ladder, end to end
// through Build — a workers-ai model or the cloud sandbox executor is the
// workers-ai river; an executor or model naming claude is the claude river;
// a local harness with any other model is the local pi; everything else says
// other rather than guessing a river it does not know.
func TestCostSortsEveryAttemptIntoItsRiver(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Decisions = nil
	src.WorkerCost = nil
	src.Standing = nil
	src.Records.Attempts = []runstate.Attempt{
		attemptMarker(1, "a1", "2026-09-27T03:00:00Z", "strong", "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "herdr"),
		attemptMarker(2, "a2", "2026-09-27T03:10:00Z", "strong", "@cf/zai-org/glm-5.3", "cloudflare-sandbox"),
		attemptMarker(3, "a3", "2026-09-27T03:20:00Z", "strong", "sonnet", "local-subprocess"),
		attemptMarker(4, "a4", "2026-09-27T03:30:00Z", "strong", "claude-opus-5", "local-subprocess"),
		attemptMarker(5, "a5", "2026-09-27T03:40:00Z", "strong", "@cf/zai-org/glm-5.3", "local-subprocess"),
		attemptMarker(6, "a6", "2026-09-27T03:50:00Z", "strong", "gpt-4o", "some-new-remote"),
		attemptMarker(7, "a7", "2026-09-27T04:00:00Z", "strong", "", ""),
	}
	model := Build(src)
	want := []CostLine{
		{Source: CostSourceWorkersAI, Metered: false, USD: nil, Attempts: 2,
			Basis: "not metered: this run's Workers AI calls are not joined to gateway logs"},
		{Source: CostSourceClaude, Metered: false, USD: nil, Attempts: 2, Basis: "not metered (subscription)"},
		{Source: CostSourcePiLocal, Metered: false, USD: nil, Attempts: 1, Basis: "not metered"},
		{Source: CostSourceOther, Metered: false, USD: nil, Attempts: 2, Basis: "not metered"},
	}
	if !reflect.DeepEqual(model.Cost.Lines, want) {
		t.Errorf("the rivers' lines are %+v\nwant %+v", model.Cost.Lines, want)
	}
	assertAdmittedByTheContract(t, model)
}

// TestCostOmitsTheDecisionsLineWhenTheRunRecordedNone: no decisions, no
// decisions line — an empty river is not a $0.00 river.
func TestCostOmitsTheDecisionsLineWhenTheRunRecordedNone(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Decisions = nil
	model := Build(src)
	for _, line := range model.Cost.Lines {
		if line.Source == CostSourceDecisions {
			t.Errorf("a run with no decision records carries a decisions line: %+v", line)
		}
	}
	assertAdmittedByTheContract(t, model)
}
