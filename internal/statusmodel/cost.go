package statusmodel

import (
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The honest cost (epic hn6, wave 2 — tick 7uv): what the run spent, split
// per RIVER — which source a dispatch's spend ran through, whether anything
// measured that river, and a number ONLY where one exists.
//
// Rule 7 (epic hn6), stated where the code answers it: cost is metered
// where measured — the factory's own gateway-backed number for its
// workers, the decision records' own usage for the model exchanges — and it
// SAYS "not metered" where nothing did: the local claude on a Max
// subscription, the local pi/GLM whose calls nothing joined to a gateway.
// Never a fabricated $0.00: an unmetered line states usd null and says so
// in its basis, because an unmetered spend wearing a $0.00 is a lie with a
// decimal point — the first-use bug this wave exists to end, a header that
// said "cost $0.00 recorded (10 attempts)" while local pi/GLM and claude
// spend went uncounted.

// WorkerCostInput is what the run's host states about what the workers
// spent: the number and the river it was measured on (the factory names
// "gateway"). Nil from any host that states nothing.
type WorkerCostInput struct {
	USD    float64
	Source string
}

// buildCost splits what the run spent per river, from the records' own
// usage and the host's own ground-truth number: the decision records' usage
// is the one cost any record carries (metered, covering the decisions that
// carry usage — the line is omitted when the run recorded none), and every
// worker river is metered only where a number exists for it — the factory's
// gateway number (Sources.WorkerCost) meters the workers-ai river, and no
// host ever meters a local one. recorded_usd stays the sum of the METERED
// lines' numbers, so a river nobody measured is never quietly counted as
// $0.00 (rule 7 above).
func buildCost(src Sources, recs Records) Cost {
	cost := Cost{
		Attempts: len(recs.Attempts),
		Basis:    "usage recorded on decision records; worker jobs record no cost",
		Lines:    []CostLine{},
	}

	// The decisions' river: metered by the records' own usage.
	decisions, carrying := 0.0, 0
	for _, d := range recs.Decisions {
		usage, ok := d.Response["usage"].(map[string]any)
		if !ok {
			continue
		}
		if usd, ok := usage["cost_usd"].(float64); ok {
			decisions += usd
			carrying++
		}
	}
	if len(recs.Decisions) > 0 {
		cost.Lines = append(cost.Lines, CostLine{
			Source:   CostSourceDecisions,
			Metered:  true,
			USD:      &decisions,
			Attempts: carrying,
			Basis:    "usage recorded on decision records",
		})
	}

	// The workers' rivers: one line per river the dispatch markers name,
	// each metered only where a number exists for it. The factory's own
	// gateway number meters the workers-ai river; every river without one
	// states usd null and says why in its basis — never $0.00.
	counts := map[string]int{}
	for _, a := range recs.Attempts {
		counts[costSourceOf(a)]++
	}
	for _, source := range []string{CostSourceWorkersAI, CostSourceClaude, CostSourcePiLocal, CostSourceOther} {
		if counts[source] == 0 {
			continue
		}
		line := CostLine{Source: source, Metered: false, USD: nil, Attempts: counts[source]}
		switch source {
		case CostSourceWorkersAI:
			if src.WorkerCost != nil {
				usd := src.WorkerCost.USD
				line.Metered, line.USD, line.Basis = true, &usd, "AI Gateway logs"
			} else {
				line.Basis = "not metered: this run's Workers AI calls are not joined to gateway logs"
			}
		case CostSourceClaude:
			line.Basis = "not metered (subscription)"
		default:
			line.Basis = "not metered"
		}
		cost.Lines = append(cost.Lines, line)
	}

	// recorded_usd is the sum of what was MEASURED — never a sum that
	// quietly counts an unmetered river as $0.00.
	for _, line := range cost.Lines {
		if line.Metered && line.USD != nil {
			cost.RecordedUSD += *line.USD
		}
	}
	return cost
}

// costSourceOf decides which river one attempt's spend ran through, from
// the provenance its dispatch marker recorded — the same fields buildTick
// reads (Executor, Model):
//
//   - a workers-ai model ("cloudflare-workers-ai/…") or the cloud sandbox
//     executor ran through the factory's Workers AI gateway;
//   - an executor or model naming claude ran on the local claude harness;
//   - a local harness (local-subprocess, herdr) with any other model ran
//     the local pi;
//   - anything else names a river this model does not know, and says
//     "other" rather than guessing one.
func costSourceOf(a runstate.Attempt) string {
	model, executor := "", ""
	if a.Provenance.Model != nil {
		model = *a.Provenance.Model
	}
	if a.Provenance.Executor != nil {
		executor = *a.Provenance.Executor
	}
	switch {
	case strings.HasPrefix(model, "cloudflare-workers-ai/") || executor == "cloudflare-sandbox":
		return CostSourceWorkersAI
	case namesClaude(executor) || namesClaude(model):
		return CostSourceClaude
	case model != "" && (executor == "local-subprocess" || executor == "herdr"):
		return CostSourcePiLocal
	}
	return CostSourceOther
}

// namesClaude says whether an executor or model string names the claude
// harness: any string carrying the harness's own name, or one of the bare
// model words the local claude profiles dispatch ("sonnet", "opus").
func namesClaude(s string) bool {
	if s == "" {
		return false
	}
	for _, word := range []string{"claude", "sonnet", "opus", "haiku"} {
		if strings.Contains(s, word) {
			return true
		}
	}
	return false
}
