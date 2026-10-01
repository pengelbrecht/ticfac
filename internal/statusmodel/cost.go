package statusmodel

import (
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The honest cost (epic hn6, wave 2 — tick 7uv): what the run spent, split
// per river — which harness or meter the money ran through, whether anything
// measured it, a number ONLY where one exists, and a basis naming what the
// number covers so it cannot quietly claim more than it does.
//
// hn6 rule 7: cost is honest. Metered where measured — the decision records'
// own usage, the factory's gateway-backed Workers AI number — and "not
// metered" where nothing did: local claude on a Max subscription, local
// pi/GLM whose calls are not joined to any gateway log. An unmetered line
// states usd null and says so in its basis, because an unmetered spend
// wearing a fabricated $0.00 is the lie this split exists to end — the
// first-use bug read "cost $0.00 recorded (10 attempts)" while the local
// claude and pi/GLM spend went uncounted.

// WorkerCostInput is what the run's host states about what the workers
// spent: the number and the river it was measured on (the factory names
// "gateway"). Nil from any host that states nothing.
type WorkerCostInput struct {
	USD    float64
	Source string
}

// cloudflareSandbox is the executor that dispatches its workers into
// Cloudflare (tick 78v): whatever substrate selected it, its workers' model
// exchanges ran through the factory's gateway — the river the workers-ai
// line names.
const cloudflareSandbox = "cloudflare-sandbox"

// workersAINamespace is the model namespace the factory's gateway serves its
// own runs under: a model spelled in it ran through Workers AI whichever
// harness executed it. The bare "@cf/…" spelling is deliberately NOT here —
// that is how a LOCAL pi names the same model, and locally its calls are
// not joined to any gateway log, which is exactly the pi-local line's
// unmetered case below.
const workersAINamespace = "cloudflare-workers-ai/"

// claudeModelAliases are the family aliases the claude runner's --model
// takes — the same set the runner config itself recognises (runconfig's
// claudeAliases), so the cost line and the spawn-time family check agree on
// one spelling.
var claudeModelAliases = map[string]bool{"opus": true, "sonnet": true, "haiku": true, "fable": true}

// buildCost splits the run's spend per river, one line per source that has
// something on it:
//
//   - decisions: the model exchanges the run itself recorded usage for,
//     metered because the records state the price. Omitted when the run
//     recorded no decision at all.
//   - workers-ai: the dispatches that ran through the factory's gateway. Its
//     number is the HOST's ground truth (Sources.WorkerCost — the factory's
//     own gateway-backed cost_usd) where the host stated one; otherwise the
//     line is unmetered, because the calls are not joined to the gateway
//     logs from where the model is built.
//   - claude: never metered — a subscription is the only way this repository
//     runs claude, and the subscription states no per-run number.
//   - pi-local and other: never metered — no reader this repository has
//     measures them.
//
// recorded_usd is the sum of the metered lines only, and no unmetered line
// ever states a number (rule 7).
func buildCost(src Sources, recs Records) Cost {
	cost := Cost{
		Attempts: len(recs.Attempts),
		Basis: "recorded_usd is the sum of the metered lines: usage recorded on decision records, " +
			"and the host's Workers AI gateway number where it stated one; worker jobs record no cost",
		Lines: []CostLine{},
	}

	lines := []CostLine{}
	if len(recs.Decisions) > 0 {
		usd, carrying := 0.0, 0
		for _, d := range recs.Decisions {
			usage, ok := d.Response["usage"].(map[string]any)
			if !ok {
				continue
			}
			carrying++
			if v, ok := usage["cost_usd"].(float64); ok {
				usd += v
			}
		}
		lines = append(lines, CostLine{
			Source: CostSourceDecisions, Metered: true, USD: &usd, Attempts: carrying,
			Basis: "usage recorded on decision records",
		})
	}

	perSource := map[string]int{}
	for _, a := range recs.Attempts {
		perSource[costSourceOf(a)]++
	}
	if n := perSource[CostSourceWorkersAI]; n > 0 || src.WorkerCost != nil {
		line := CostLine{Source: CostSourceWorkersAI, Attempts: n}
		if src.WorkerCost != nil {
			usd := src.WorkerCost.USD
			line.Metered, line.USD, line.Basis = true, &usd, "AI Gateway logs"
		} else {
			line.Basis = "not metered: this run's Workers AI calls are not joined to gateway logs"
		}
		lines = append(lines, line)
	}
	unmetered := []struct{ source, basis string }{
		{CostSourceClaude, "not metered (subscription)"},
		{CostSourcePiLocal, "not metered"},
		{CostSourceOther, "not metered"},
	}
	for _, u := range unmetered {
		if n := perSource[u.source]; n > 0 {
			lines = append(lines, CostLine{Source: u.source, Attempts: n, Basis: u.basis})
		}
	}

	cost.Lines = lines
	for _, l := range lines {
		if l.Metered && l.USD != nil {
			cost.RecordedUSD += *l.USD
		}
	}
	return cost
}

// costSourceOf decides which river one dispatch's spend ran through, from the
// two facts its provenance carries — the executor that ran it and the model
// it ran on:
//
//   - workers-ai: a model in the factory's gateway namespace, or an executor
//     that dispatches into Cloudflare.
//   - claude: a Claude-family model — an alias (opus, sonnet) or a claude-…
//     name, the family the runner config itself recognises. A
//     provider-qualified id never reads as claude: the local pi harness
//     serving a claude model through openrouter is pay-per-token spend this
//     repository does not meter, not a subscription.
//   - pi-local: any other provider-qualified model — the local pi harness
//     serving GLM or an openrouter id. Provenance cannot name the harness
//     itself, and the model's spelling is the one evidence it carries.
//   - other: everything else — a codex model, a bare id, nothing stated.
func costSourceOf(a runstate.Attempt) string {
	executor, model := "", ""
	if a.Provenance.Executor != nil {
		executor = *a.Provenance.Executor
	}
	if a.Provenance.Model != nil {
		model = *a.Provenance.Model
	}
	switch {
	case strings.HasPrefix(model, workersAINamespace) || executor == cloudflareSandbox:
		return CostSourceWorkersAI
	case isClaudeModel(model):
		return CostSourceClaude
	case strings.Contains(model, "/"):
		return CostSourcePiLocal
	default:
		return CostSourceOther
	}
}

// isClaudeModel reports whether a model id is a Claude-family name: an alias
// for the latest model in a family, or a full claude-… name — never a
// provider-qualified id (see costSourceOf).
func isClaudeModel(model string) bool {
	if strings.Contains(model, "/") {
		return false
	}
	return claudeModelAliases[model] || strings.HasPrefix(model, "claude-")
}
