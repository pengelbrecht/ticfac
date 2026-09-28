package statusmodel

// The honest cost (epic hn6, wave 1 — tick r5i): what the run spent, split
// per source — which river, whether anything measured it, and a number ONLY
// where one exists. An unmetered line states usd null and says so in its
// basis, because an unmetered spend wearing a fabricated $0.00 is the lie
// the epic's rule 7 exists to end ("never $0.00 for unmetered spend").
//
// Wave 1 moves buildCost here unchanged (it keeps summing the decision
// records' own usage, with the basis naming that coverage) and adds the
// LINES array at its empty value. This file is where the wave-2 cost tick
// splits the lines from the decision records' usage and the host's own
// ground-truth number (Sources.WorkerCost); the per-line shape and its
// contract pin are already fixed, so that fill cannot invent a field.

// WorkerCostInput is what the run's host states about what the workers
// spent: the number and the river it was measured on (the factory names
// "gateway"). Nil from any host that states nothing.
type WorkerCostInput struct {
	USD    float64
	Source string
}

// buildCost sums what the records state about money: the decision records'
// own usage, the only cost any record carries. The basis names the coverage
// so the number cannot quietly claim more than the records do.
func buildCost(recs Records) Cost {
	cost := Cost{
		Attempts: len(recs.Attempts),
		Basis:    "usage recorded on decision records; worker jobs record no cost",
		Lines:    []CostLine{},
	}
	for _, d := range recs.Decisions {
		usage, ok := d.Response["usage"].(map[string]any)
		if !ok {
			continue
		}
		if usd, ok := usage["cost_usd"].(float64); ok {
			cost.RecordedUSD += usd
		}
	}
	return cost
}
