package statusmodel

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The cost suite (epic hn6, wave 2 — tick 7uv): the spend split per river,
// metered only where something measured it and never a fabricated $0.00
// where nothing did — the first-use bug's "cost $0.00 recorded (10
// attempts)" header, with the local claude and pi/GLM spend it hid counted
// as unmetered lines. Every case builds the whole Model and holds it to the
// contract through the same bundleFixture helper the verdict suite does.

// costLineOf finds one cost line by its river, for the assertion that reads.
func costLineOf(t *testing.T, model Model, source string) CostLine {
	t.Helper()
	for _, line := range model.Cost.Lines {
		if line.Source == source {
			return line
		}
	}
	t.Fatalf("no %s cost line in %+v", source, model.Cost.Lines)
	return CostLine{}
}

// TestCostLocalSpendIsUnmeteredNeverADollarZero: local attempts on claude and
// on pi serving GLM, with no decision recorded, are two unmetered lines —
// the subscription spend and the unmeasured pi spend, each with usd null and
// a basis saying so, recorded_usd a true zero — and the marshalled JSON
// carries no line that states a number without a measurement.
func TestCostLocalSpendIsUnmeteredNeverADollarZero(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Decisions = nil
	src.Records.Attempts = []runstate.Attempt{
		attemptMarker(1, "nwj", "2026-09-27T03:19:05Z", "strong", "opus", "local-subprocess"),
		attemptMarker(2, "89m", "2026-09-27T04:00:00Z", "strong", "@cf/zai-org/glm-5.3", "local-subprocess"),
	}
	model := Build(src)

	want := []CostLine{
		{Source: CostSourceClaude, Metered: false, USD: nil, Attempts: 1, Basis: "not metered (subscription)"},
		{Source: CostSourcePiLocal, Metered: false, USD: nil, Attempts: 1, Basis: "not metered"},
	}
	if !reflect.DeepEqual(model.Cost.Lines, want) {
		t.Errorf("the cost lines are %+v, want the two unmetered ones %+v", model.Cost.Lines, want)
	}
	if model.Cost.RecordedUSD != 0 {
		t.Errorf("recorded_usd is %v, want 0: nothing measured any spend", model.Cost.RecordedUSD)
	}

	// The JSON, not the struct: no line states a number it did not measure.
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Cost struct {
			RecordedUSD float64    `json:"recorded_usd"`
			Lines       []CostLine `json:"lines"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.Cost.RecordedUSD != 0 {
		t.Errorf("the marshalled recorded_usd is %v, want 0", document.Cost.RecordedUSD)
	}
	for _, line := range document.Cost.Lines {
		if !line.Metered && line.USD != nil {
			t.Errorf("the %s line is unmetered and states usd %v: an unmetered spend wearing a number is a fabricated spend",
				line.Source, *line.USD)
		}
	}
	assertValidatesAgainstTheContract(t, model)
}

// TestCostTheCloudsWorkersAISpendIsMeteredFromTheGateway: dispatches that
// ran through the factory's gateway — a model in its namespace, or the
// executor that boots its workers in Cloudflare — are one workers-ai line,
// metered with the host's own gateway-backed number and the attempts counted.
// Locally the same models are unmetered: the calls are not joined to gateway
// logs from where the model is built, and the line says so instead of
// inventing a number.
func TestCostTheCloudsWorkersAISpendIsMeteredFromTheGateway(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Host = HostCloud
	src.RunID = "run_1a2b3c4d5e6f"
	src.WorkerCost = &WorkerCostInput{USD: 0.41, Source: "gateway"}
	src.Records.Decisions = nil
	src.Records.Attempts = []runstate.Attempt{
		attemptMarker(1, "nwj", "2026-09-27T03:19:05Z", "strong", "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "local-subprocess"),
		attemptMarker(2, "89m", "2026-09-27T04:00:00Z", "strong", "glm-5.3-flash", "cloudflare-sandbox"),
	}
	src.Standing, src.StandingRead, src.Session = nil, false, nil
	model := Build(src)

	want := []CostLine{{
		Source: CostSourceWorkersAI, Metered: true, USD: float64Ptr(0.41), Attempts: 2,
		Basis: "AI Gateway logs",
	}}
	if !reflect.DeepEqual(model.Cost.Lines, want) {
		t.Errorf("the cost lines are %+v, want the one metered workers-ai line %+v", model.Cost.Lines, want)
	}
	if model.Cost.RecordedUSD != 0.41 {
		t.Errorf("recorded_usd is %v, want the gateway number 0.41", model.Cost.RecordedUSD)
	}
	assertValidatesAgainstTheContract(t, model)

	// The same model spelled in the gateway's namespace, run LOCALLY: the
	// host states no ground truth, so the line is unmetered and says why.
	local := runningEpicSources()
	local.Records.Decisions = nil
	local.Records.Attempts = []runstate.Attempt{
		attemptMarker(1, "nwj", "2026-09-27T03:19:05Z", "strong", "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "local-subprocess"),
	}
	localModel := Build(local)
	line := costLineOf(t, localModel, CostSourceWorkersAI)
	if line.Metered || line.USD != nil || line.Attempts != 1 {
		t.Errorf("a local workers-ai line is %+v, want unmetered with no number over 1 attempt", line)
	}
	if want := "not metered: this run's Workers AI calls are not joined to gateway logs"; line.Basis != want {
		t.Errorf("a local workers-ai line's basis is %q, want %q", line.Basis, want)
	}
	if localModel.Cost.RecordedUSD != 0 {
		t.Errorf("recorded_usd is %v, want 0: nothing measured the local spend", localModel.Cost.RecordedUSD)
	}
}

// TestCostDecisionsCarryTheirOwnMeteredUsage: the run's own recorded model
// exchanges are metered — the records state the price — with their line first
// and the per-river attempt lines behind it; recorded_usd is the sum of the
// metered lines only.
func TestCostDecisionsCarryTheirOwnMeteredUsage(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())

	decisionsUSD := 0.04
	want := []CostLine{
		{Source: CostSourceDecisions, Metered: true, USD: &decisionsUSD, Attempts: 1, Basis: "usage recorded on decision records"},
		{Source: CostSourceClaude, Metered: false, USD: nil, Attempts: 1, Basis: "not metered (subscription)"},
		{Source: CostSourcePiLocal, Metered: false, USD: nil, Attempts: 2, Basis: "not metered"},
	}
	if !reflect.DeepEqual(model.Cost.Lines, want) {
		t.Errorf("the cost lines are %+v, want %+v", model.Cost.Lines, want)
	}
	metered := 0.0
	for _, line := range model.Cost.Lines {
		if line.Metered && line.USD != nil {
			metered += *line.USD
		}
	}
	if model.Cost.RecordedUSD != metered {
		t.Errorf("recorded_usd is %v, want the metered lines' sum %v", model.Cost.RecordedUSD, metered)
	}
	if model.Cost.Attempts != 3 {
		t.Errorf("the cost counts %d attempts, want the 3 dispatch markers", model.Cost.Attempts)
	}
	assertValidatesAgainstTheContract(t, model)
}

// TestCostSplitsRiversTheProvenanceNames: the river comes from each
// dispatch's own provenance — a Claude-family model is claude however the
// harness was spelled, a codex-shaped model lands on other, and a dispatch
// that states nothing lands on other too, its attempts still counted.
func TestCostSplitsRiversTheProvenanceNames(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Decisions = nil
	src.Records.Attempts = []runstate.Attempt{
		attemptMarker(1, "nwj", "2026-09-27T03:19:05Z", "strong", "sonnet", "local-subprocess"),
		attemptMarker(2, "89m", "2026-09-27T04:00:00Z", "strong", "gpt-5.6-luna", "local-subprocess"),
	}
	model := Build(src)

	if line := costLineOf(t, model, CostSourceClaude); line.Attempts != 1 || line.Metered {
		t.Errorf("the claude line is %+v, want the sonnet dispatch unmetered", line)
	}
	if line := costLineOf(t, model, CostSourceOther); line.Attempts != 1 || line.Metered || line.USD != nil {
		t.Errorf("the other line is %+v, want the codex-shaped dispatch unmetered", line)
	}
	if line := recoveredLine(model, CostSourcePiLocal); line != nil {
		t.Errorf("the model carries a pi-local line %+v, want none: no dispatch is provider-qualified", line)
	}
	assertValidatesAgainstTheContract(t, model)
}

// float64Ptr is the test-side pointer a metered line's number needs.
func float64Ptr(v float64) *float64 { return &v }

// recoveredLine is the negative lookup a split assertion needs: the line for
// a river that should carry nothing, nil when it carries nothing.
func recoveredLine(model Model, source string) *CostLine {
	for i := range model.Cost.Lines {
		if model.Cost.Lines[i].Source == source {
			return &model.Cost.Lines[i]
		}
	}
	return nil
}
