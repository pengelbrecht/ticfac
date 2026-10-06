package profile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The operator rule as code (tick nwn; re-flowed by tick 6fv): a cloud run
// bills NO PER-TOKEN VENDOR SPEND. Two ways to satisfy it:
//
//   - a WORKERS AI worker: a model served by Workers AI, on a harness that
//     reaches it through the factory's own gateway — billed to the
//     operator's Cloudflare account, never to a card;
//   - a SUBSCRIPTION RUNG ([CloudRule].SubscriptionRungs): a harness whose
//     CLI speaks a subscription's OAuth dialect, on one of the rung's
//     VERSIONLESS model aliases — claude on "sonnet"/"opus" against the
//     operator's Claude Max subscription, the token injected by the
//     factory's claude-sub wiring (docs/spikes/jvj-claude-sub-cloud.md)
//     and never in a container. A pinned claude id is PER-TOKEN spend and
//     is refused exactly as firmly as claude used to be.
//
// Claude is no longer refused by name: the rung's versionless aliases are
// what make it subscription-billed, and the jvj spike measured the
// subscription's own quota — not per-token billing — as the ceiling the
// per-subscription cap and the Workers AI step-down exist to respect.
//
// It is checked on the FINAL resolved worker, after every overlay [route]
// applies — the role's own cell, any tier, the `.tick/runners.cloud.toml`
// cell and that file's own tier cell — never on one input layer. Each of
// the three findings tick nwn absorbed leaked through a check on one layer:
// a cloud cell with no kind leaving the compiled-in claude (dd60e88c), a
// tier applied after the cloud cell (ea1a62d3), and a check of the declared
// cell while the final value was claude anyway (577272d7).
//
// It fires in [Resolve] wherever the work runs IN CLOUDFLARE (tick 78v),
// which is two keys and not one: the cloud substrate, and any executor that
// dispatches its workers into Cloudflare ([CloudRule].Executors) — a run
// whose substrate is local can still select the cloudflare-sandbox executor
// by pointing --profiles at the cloud set, and its workers boot in a
// Cloudflare container under local routing, which may resolve to a
// per-token model through `.tick/runners.local.toml`'s ladder. So every
// path that resolves a profile answers to it — and a run's construction
// resolves every role, and each role at every tier its policy can derive,
// so such a run refuses at START, naming the role and the tier.

// SubscriptionRung is one subscription-billed rung of [CloudRule]: the
// harness whose CLI speaks the subscription's OAuth dialect, the VERSIONLESS
// model aliases a config may name on it (never a pinned id — an id bills
// per token, and it is the alias that makes the rung subscription-billed),
// and the Workers AI model the factory steps the role down to when no
// subscription can take the job (no lease, or every one benched on its
// quota — the lease outcome names which).
type SubscriptionRung struct {
	// Harness is the CLI kind the rung runs: the one whose environment the
	// factory's claude-sub wiring owns (the OAuth placeholder, the
	// interception's CA), and the one [CloudAllowedHarnesses] admits to a
	// dispatching executor on the rung's behalf.
	Harness string
	// Models are the CLI's own versionless alias words. They resolve inside
	// the CLI binary, so they name the latest model the IMAGE'S PINNED CLI
	// knows — the automatic CLAUDE_CODE_VERSION bump (tick 6fv) is what
	// keeps them current.
	Models []string
	// Fallback is the Workers AI model the role is re-resolved at when no
	// subscription can take the job. It must satisfy [IsWorkersAIModel]:
	// TestEverySubscriptionRungIsWellFormed holds it to that.
	Fallback string
}

// CloudRule is the rule, in ONE place: change it here and nowhere else.
var CloudRule = struct {
	// ModelNamespaces are the Workers AI provider namespaces a model id is
	// spelled in — pi's `cloudflare-workers-ai/…`, omp's `workers-ai/…`, and
	// Workers AI's own bare `@cf/…`. A model is a Workers AI model when its
	// id begins with one of them and names something after it.
	ModelNamespaces []string

	// Harnesses are the runner kinds that reach Workers AI through the
	// factory gateway. A Workers AI id on a harness that cannot call it is
	// not a Workers AI worker. Both names of the ONE durable harness are
	// served (epic 43y, tick qf4): `pi-durable` is the hosted kind — the
	// cloud profile set names it, the factory's WorkerAgent runs the
	// conversation on it whatever harness the dispatch names for the
	// container its tools run in (tick 4uj) — and `pi` is a runner table's
	// name for the same durable harness (tick hpk), which this repository's
	// own cloud routing (`.tick/runners.cloud.toml`) spells too. A profile
	// that dispatches into Cloudflare never reaches the rule as `pi`: its
	// resolution binds the hosted name first ([HostedDurableHarness], tick
	// twa), because that is the kind its container is told.
	Harnesses []string

	// Executors are the executors that dispatch their workers INTO Cloudflare
	// (tick 78v): the cloudflare-sandbox executor boots one worker container
	// per attempt in Cloudflare, whatever substrate selected it — a local
	// run pointing --profiles at the cloud set dispatches through it — so the
	// rule keys on the executor as well as on the substrate. A name here is
	// the rule's, not a model list: which Workers AI model still is a choice.
	Executors []string

	// SubscriptionRungs are the subscription-billed rungs (tick 6fv): a
	// harness/alias pair that pays a flat subscription instead of per-token
	// spend. Admitted only as the PAIR — the alias on any other harness is
	// refused, and so is a pinned id on the rung's own harness.
	SubscriptionRungs []SubscriptionRung
}{
	ModelNamespaces: []string{"cloudflare-workers-ai/", "workers-ai/", "@cf/"},
	Harnesses:       []string{"pi", "pi-durable"},
	Executors:       []string{"cloudflare-sandbox"},
	// The claude-sub rung (spike jvj, taken to production by 6fv): claude on
	// the versionless sonnet/opus aliases, stepping down to GLM 5.3 on
	// Workers AI when no subscription is free. The ladder the operator named
	// (2026-10-06): implement climbs sonnet → opus, review and close-out run
	// on opus. OFF unless a config selects it — the default cloud routing
	// names Workers AI cells, and tda is the tick that makes the selection a
	// named config.
	SubscriptionRungs: []SubscriptionRung{{
		Harness:  "claude",
		Models:   []string{"sonnet", "opus"},
		Fallback: "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
	}},
}

// HostedDurableHarness is the durable harness's name in Cloudflare: the kind
// the sandbox image hosts (image/common.sh admits omp, claude and this, and
// dies at boot on any other — tick jhp) and the harness the factory's
// WorkerAgent runs a hosted attempt's conversation on (tick 4uj).
const HostedDurableHarness = "pi-durable"

// runnerTableDurableHarness is a runner table's name for the same harness:
// the local executors host it under `pi` (tick hpk), and every
// .tick/runners*.toml this repository carries spells it so.
const runnerTableDurableHarness = "pi"

// ErrCloudBilling is the refusal a cloud run gets when a role's FINAL
// resolved worker bills per-token vendor spend in Cloudflare — neither a
// Workers AI worker through the factory's gateway nor a subscription rung on
// its versionless aliases. It is a sentinel so a caller can tell it from
// every other routing failure, the way [ErrNoCloudRouting] names the missing
// cell.
var ErrCloudBilling = errors.New("what runs in Cloudflare bills no per-token vendor spend: a Workers AI model through the factory's gateway, or a subscription rung's versionless aliases")

// IsWorkersAIModel reports whether model is served by Workers AI, by its
// provider namespace ([CloudRule]) — not by a list of models.
func IsWorkersAIModel(model string) bool {
	for _, ns := range CloudRule.ModelNamespaces {
		if rest, ok := strings.CutPrefix(model, ns); ok && rest != "" {
			return true
		}
	}
	return false
}

// reachesWorkersAI reports whether kind is a harness the gateway serves.
func reachesWorkersAI(kind string) bool {
	for _, h := range CloudRule.Harnesses {
		if kind == h {
			return true
		}
	}
	return false
}

// SubscriptionRungFor reports the subscription rung a harness/model pair is
// on, and whether it is on one at all. The PAIR is the rung: the alias on any
// other harness is nothing, and so is a pinned id on the rung's own harness.
func SubscriptionRungFor(harness, model string) (SubscriptionRung, bool) {
	for _, rung := range CloudRule.SubscriptionRungs {
		if harness != rung.Harness {
			continue
		}
		for _, alias := range rung.Models {
			if model == alias {
				return rung, true
			}
		}
	}
	return SubscriptionRung{}, false
}

// subscriptionHarnesses is every rung's harness, in declaration order,
// deduplicated. Unexported: a caller wants the union, and that is
// [CloudAllowedHarnesses].
func subscriptionHarnesses() []string {
	var out []string
	for _, rung := range CloudRule.SubscriptionRungs {
		found := false
		for _, h := range out {
			if h == rung.Harness {
				found = true
			}
		}
		if !found {
			out = append(out, rung.Harness)
		}
	}
	return out
}

// CloudBillingAllows is the rule's ONE predicate: does this harness/model
// pair satisfy the cloud billing rule — a Workers AI worker (a harness the
// gateway serves, on a model in a Workers AI namespace) or a subscription
// rung (the rung's harness, on one of its versionless aliases)? The factory
// executor's door check answers to it beside [Resolve] itself, so the two
// can never drift.
func CloudBillingAllows(harness, model string) bool {
	if reachesWorkersAI(harness) && IsWorkersAIModel(model) {
		return true
	}
	_, rung := SubscriptionRungFor(harness, model)
	return rung
}

// CloudAllowedHarnesses is every harness a Cloudflare-dispatched worker may
// run on: the harnesses the gateway serves plus every subscription rung's
// harness. An executor's runner allowlist reads this union, because a
// resolution to a rung harness is legal exactly when the rung's pairing is —
// the rule refuses the model, never the harness alone.
func CloudAllowedHarnesses() []string {
	out := append([]string{}, CloudRule.Harnesses...)
	return append(out, subscriptionHarnesses()...)
}

// bindHostedHarness names the durable harness by its hosted kind on a
// profile that dispatches into Cloudflare (epic 43y, tick twa). The resolved
// runner is what the door binds as the container's TICKS_HARNESS (tick 9iz),
// and a runner table spells the harness `pi` — the local executors' name for
// it, which this repository's .tick/runners.cloud.toml carries too — so a
// Cloudflare dispatch resolved from it told every worker container a kind
// the image refuses at boot. The translation sits where the runner table's
// vocabulary meets the image's, after every overlay applied: the profile,
// its digest and the dispatch record then name what the container runs.
func bindHostedHarness(p *Profile) {
	if p.Runner == runnerTableDurableHarness {
		p.Runner = HostedDurableHarness
	}
}

// dispatchesIntoCloudflare reports whether executor boots its workers in a
// Cloudflare container (tick 78v): the cloudflare-sandbox executor does,
// whatever substrate selected it — a run whose substrate is local that points
// --profiles at the cloud set dispatches through it, and its workers run in
// Cloudflare all the same.
func dispatchesIntoCloudflare(executor string) bool {
	for _, name := range CloudRule.Executors {
		if executor == name {
			return true
		}
	}
	return false
}

// enforceCloudBilling applies [CloudRule] to one role's FINAL resolved
// profile at the tier it was resolved for, naming the role, the tier, the
// resolved kind and model, and the cells that routed it — the last of which
// is the one to edit, because the refusal is over their combination.
func enforceCloudBilling(p *Profile, tier string) error {
	if CloudBillingAllows(p.Runner, p.Model) {
		return nil
	}
	at := "no tier"
	if tier != "" {
		at = fmt.Sprintf("tier %q", tier)
	}
	var why []string
	if reachesWorkersAI(p.Runner) {
		// A gateway harness on a model the gateway does not serve — a
		// subscription alias on pi, an Anthropic id on anything. The model
		// half is the whole refusal.
		why = append(why, fmt.Sprintf("model %q is not in a Workers AI namespace (%s)",
			p.Model, strings.Join(CloudRule.ModelNamespaces, ", ")))
	} else if rungHarnessNamed(p.Runner) {
		// The rung's own harness, a model the rung does not admit: the
		// honest why names the aliases, because spelling one is the fix —
		// a pinned model id bills per token in the cloud, and the alias is
		// what makes the rung subscription-billed.
		why = append(why, fmt.Sprintf("model %q is not one of the %q subscription rung's versionless aliases (%s) — a pinned model id bills per token in the cloud",
			p.Model, p.Runner, strings.Join(rungAliases(p.Runner), ", ")))
	} else {
		why = append(why, fmt.Sprintf("kind %q cannot reach Workers AI through the factory gateway (want one of %s)",
			p.Runner, strings.Join(CloudRule.Harnesses, ", ")))
		if !IsWorkersAIModel(p.Model) {
			why = append(why, fmt.Sprintf("model %q is not in a Workers AI namespace (%s)",
				p.Model, strings.Join(CloudRule.ModelNamespaces, ", ")))
		}
	}
	routed := p.Routed
	if routed == "" {
		routed = "nothing: the compiled-in profile"
	}
	// The file the refusal points at is the one the routing resolved against
	// (tick 78v): under a local substrate that is the local override — the
	// last word there, and the file an operator reading the refusal can edit —
	// and under no substrate at all it is the common file.
	routeFile := runconfig.FileName
	if sub := runconfig.Substrate(p.Substrate); sub.Valid() && sub != runconfig.SubstrateAuto {
		if name := runconfig.OverrideFileName(sub); name != "" {
			routeFile = name
		}
	}
	return fmt.Errorf("role %q at %s fully resolves to kind %q, model %q (routed by %s): %w — %s; "+
		"route both the kind and the model in %s",
		p.Role, at, p.Runner, p.Model, routed, ErrCloudBilling, strings.Join(why, "; "), routeFile)
}

// rungHarnessNamed reports whether kind is some rung's harness — the harness
// half of a subscription rung, whatever the model half says.
func rungHarnessNamed(kind string) bool {
	for _, h := range subscriptionHarnesses() {
		if kind == h {
			return true
		}
	}
	return false
}

// rungAliases is every alias the rung named by kind admits, or nil when kind
// is no rung's harness.
func rungAliases(kind string) []string {
	for _, rung := range CloudRule.SubscriptionRungs {
		if rung.Harness == kind {
			return rung.Models
		}
	}
	return nil
}
