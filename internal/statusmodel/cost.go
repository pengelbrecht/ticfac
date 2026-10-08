package statusmodel

import (
	"fmt"
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
// spent: the number, the river it was measured on (the factory names
// "gateway"), and — since tick kf4 — what the number COVERS: a gateway
// number is a sum over CALLS, and the status line may claim only the
// attempts those calls join. Nil from any host that states nothing.
type WorkerCostInput struct {
	USD    float64
	Source string
	// Calls is how many gateway rows the number sums, whichever owner they
	// name — the unit the gateway actually measures, one row per model
	// exchange.
	Calls int
	// Attempts is how many DISTINCT dispatches those rows name, through the
	// attempt key the metering join stamps (the factory's own
	// gatewayMetadata vocabulary). Zero when no row names one.
	Attempts int
	// OwnCalls is how many rows name the run ITSELF — the classifier's
	// calls stamp a caller — measured money that belongs to no worker
	// attempt.
	OwnCalls int
	// UnnamedCalls is how many rows name neither an attempt nor a caller:
	// rows from before the join named its attempts, whose owner no
	// per-attempt claim can be made from.
	UnnamedCalls int
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
//   - decisions: the model exchanges the run itself recorded a measured
//     price for — metered because the records state the price, and ONLY
//     then (tick 1tm): a record whose usage block carries no price — no
//     usage block at all, or cost_usd as the explicit null the jev usage
//     states since tick fzt (the field is a pointer: a never-set price is
//     no price, not a zero) — is not a measurement, so the line says
//     "not metered" and states no number instead of a fabricated $0.00.
//     Omitted when the run recorded no decision at all.
//   - workers-ai: the dispatches that ran through the factory's gateway.
//     Its number is the HOST's ground truth (Sources.WorkerCost — the
//     factory's own gateway-backed cost_usd) where the host stated one;
//     otherwise the line is unmetered, because the calls are not joined
//     to the gateway logs from where the model is built. A CLOUD run
//     whose host stated no number is the unsynced record — the runs row's
//     cost_usd is NOT NULL DEFAULT 0, so before the first cost sync or
//     with telemetry unavailable the row's zero is a default, not a
//     measurement — and its basis names that, not the local unjoined
//     case. A LOCAL run's number (tick kf4) is a sum over the CALLS the
//     gateway logged, and the line claims only the attempts those calls
//     join: a resumed run whose metering began mid-run — or a host that
//     configured its gateway mid-run — has attempts the join never
//     reached, and they are called "not metered" on their own line, never
//     covered by a number that did not measure them. The run's own model
//     calls (the classifier's, tick 24u) are measured money belonging to
//     no worker attempt, and the basis names them wherever they are in
//     the number.
//   - claude: never metered — a subscription is the only way this repository
//     runs claude, and the subscription states no per-run number.
//   - pi-local and other: never metered — no reader this repository has
//     measures them.
//
// recorded_usd is the sum of the metered lines only, and no unmetered line
// ever states a number (rule 7). Since tick dm2 the sum itself is NULL when
// no line is metered: a 0 beside all-unmetered lines is the fabricated
// $0.00 again — this time on the roll-up instead of the line.
func buildCost(src Sources, recs Records) Cost {
	cost := Cost{
		Attempts: len(recs.Attempts),
		Basis: "recorded_usd is the sum of the metered lines, and null when none is: " +
			"usage recorded on decision records, and the host's Workers AI gateway number where it stated one; " +
			"worker jobs record no cost",
		Lines: []CostLine{},
		// The leased subscription's window use (tick b13) rides as its caller
		// stated it: the gathering has already reduced the factory's whole
		// pool snapshot to the one subscription THIS run leases, and the model
		// adds no opinion on which one that is.
		Subscription: src.ClaudeSub,
	}

	lines := []CostLine{}
	if len(recs.Decisions) > 0 {
		usd, carrying, measured := 0.0, 0, 0
		for _, d := range recs.Decisions {
			usage, ok := d.Response["usage"].(map[string]any)
			if !ok {
				continue
			}
			carrying++
			// A measured price is one the record STATES. Since tick fzt the
			// jev usage states no price as an explicit null (the field is a
			// pointer), so a null or absent cost_usd is the distinguishable
			// no-price and never meters. A stated zero remains ambiguous —
			// on a record that predates the pointer it is the marshalled
			// zero of a field nothing ever set, the fabrication this split
			// exists to end — so only a price above zero meters the line and
			// a stated zero reads unmeasured here, the conservative side of
			// rule 7.
			if v, ok := usage["cost_usd"].(float64); ok && v > 0 {
				measured++
				usd += v
			}
		}
		if measured > 0 {
			lines = append(lines, CostLine{
				Source: CostSourceDecisions, Metered: true, USD: &usd, Attempts: carrying,
				Basis: "usage recorded on decision records",
			})
		} else {
			lines = append(lines, CostLine{
				Source: CostSourceDecisions, Metered: false, USD: nil, Attempts: carrying,
				Basis: "not metered: the decision records carry no measured cost",
			})
		}
	}

	perSource := map[string]int{}
	for _, a := range recs.Attempts {
		perSource[costSourceOf(a)]++
	}
	if n := perSource[CostSourceWorkersAI]; n > 0 || src.WorkerCost != nil {
		line := CostLine{Source: CostSourceWorkersAI, Attempts: n}
		if src.WorkerCost == nil {
			if src.Host == HostCloud {
				// The unsynced cloud record: the row's number is a default until
				// the gateway's telemetry answers (tick 1tm), so the line names
				// the telemetry, not the local unjoined case.
				line.Basis = "not metered: the gateway's cost telemetry has not answered for this run"
			} else {
				line.Basis = "not metered: this run's Workers AI calls are not joined to gateway logs"
			}
		} else if src.Host == HostCloud {
			// The cloud run's factory proxy stamps EVERY call the containers
			// make with the run's own token, so the number covers the whole
			// river: the plain basis, the one shape the goldens carry.
			usd := src.WorkerCost.USD
			line.Metered, line.USD, line.Basis = true, &usd, "AI Gateway logs"
		} else {
			// The LOCAL join (tick kf4): the number covers the CALLS the
			// gateway logged, and the attempts those calls name — never the
			// whole river by default, because a resumed run (or a host that
			// configured its gateway mid-run) has attempts the join never
			// reached, and a number claimed over them is a lie with a
			// decimal point. The unmeasured ones are called "not metered",
			// on their own line, exactly like every other unmeasured spend.
			usd := src.WorkerCost.USD
			switch joined := src.WorkerCost; {
			case n == 0:
				// Rows answered for a run with no workers-ai dispatch at all:
				// the run's own model calls (the classifier's carry the run
				// tag, tick 24u) — measured money belonging to no worker.
				line.Metered, line.USD = true, &usd
				line.Basis = "AI Gateway logs: gateway-joined calls of no worker dispatch this run made " +
					"(the classifier's own calls carry the run tag)"
			case joined.Calls == 0 || (joined.UnnamedCalls == 0 && joined.Attempts >= n):
				// Full coverage: every row names an attempt the river counts —
				// or the host stated a bare number with no coverage at all
				// (Calls 0, the pre-kf4 input shape) — so the number and the
				// attempts it covers agree, the cloud shape. The run's own
				// calls beside them are named, because their money is in the
				// number.
				line.Metered, line.USD = true, &usd
				if joined.OwnCalls > 0 {
					line.Basis = "AI Gateway logs: every attempt's calls joined the gateway, beside the run's own model calls"
				} else {
					line.Basis = "AI Gateway logs"
				}
			case joined.UnnamedCalls == 0 && joined.Attempts > 0:
				// Every row is named — some by an attempt, some as the run's
				// own — but not every attempt joined: the number covers
				// the named attempts alone, and the rest are not metered.
				line.Attempts = joined.Attempts
				line.Metered, line.USD = true, &usd
				line.Basis = fmt.Sprintf("AI Gateway logs: the calls of %d of %d attempts joined the gateway", joined.Attempts, n)
				if joined.OwnCalls > 0 {
					line.Basis += ", beside the run's own model calls"
				}
				lines = append(lines, line)
				line = CostLine{Source: CostSourceWorkersAI, Attempts: n - joined.Attempts,
					Basis: "not metered: dispatched without the gateway metering join, their calls never reached the gateway logs"}
			case joined.UnnamedCalls == 0:
				// Every row is the run's own call, no attempt's: the river's
				// dispatches never joined, so they keep their honest line
				// while the measured own-call money keeps its number —
				// over no attempt, because it measured none.
				line.Attempts = 0
				line.Metered, line.USD = true, &usd
				line.Basis = "AI Gateway logs: the run's own model calls, no worker attempt joined"
				lines = append(lines, line)
				line = CostLine{Source: CostSourceWorkersAI, Attempts: n,
					Basis: "not metered: dispatched without the gateway metering join, their calls never reached the gateway logs"}
			default:
				// Rows that name no attempt and no caller — the window before
				// the join named anything (every dispatch between dm2 and
				// kf4): no per-attempt claim can be made from them, so the
				// one line states the calls the number sums, the attempts
				// the rows do name, and that the never-joined attempts are
				// not metered.
				line.Metered, line.USD = true, &usd
				line.Basis = fmt.Sprintf("AI Gateway logs: the sum of %d gateway-joined calls", joined.Calls)
				if joined.Attempts > 0 {
					line.Basis += fmt.Sprintf("; attempts whose calls are named by them: %d of %d", joined.Attempts, n)
				}
				if joined.OwnCalls > 0 {
					line.Basis += fmt.Sprintf("; run-own model calls: %d", joined.OwnCalls)
				}
				line.Basis += fmt.Sprintf("; rows carrying no attempt name (from before the join named its attempts): %d", joined.UnnamedCalls)
				line.Basis += "; attempts whose calls never reached the gateway are not metered"
			}
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
	sum, anyMetered := 0.0, false
	for _, l := range lines {
		if l.Metered && l.USD != nil {
			sum += *l.USD
			anyMetered = true
		}
	}
	// The number exists only where a measurement does: an all-unmetered
	// roll-up states null, never a 0 dressed up as a measured zero.
	if anyMetered {
		cost.RecordedUSD = &sum
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
