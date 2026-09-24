package profile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The operator rule as code (tick nwn): a cloud run runs WORKERS AI MODELS
// ONLY, through the factory's Workers AI gateway — so nothing in the cloud
// runs claude. Claude is refused because it is not a Workers AI model and its
// harness cannot reach the gateway, not by name. Which Workers AI model is a
// choice (GLM 5.3 / Flash today), never the rule: the rule is the provider.
//
// It is checked on the FINAL resolved worker, after every overlay [route]
// applies — the role's own cell, any tier, the `.tick/runners.cloud.toml`
// cell and that file's own tier cell — never on one input layer. Each of the
// three findings this tick absorbed leaked through a check on one layer: a
// cloud cell with no kind leaving the compiled-in claude (dd60e88c), a tier
// applied after the cloud cell (ea1a62d3), and a check of the declared cell
// while the final value was claude anyway (577272d7).
//
// It fires in [Resolve] wherever the work runs IN CLOUDFLARE (tick 78v),
// which is two keys and not one: the cloud substrate, and any executor that
// dispatches its workers into Cloudflare ([CloudRule].Executors) — a run
// whose substrate is local can still select the cloudflare-sandbox executor
// by pointing --profiles at the cloud set, and its workers boot in a
// Cloudflare container under local routing, which may resolve to claude
// through `.tick/runners.local.toml`'s ladder. So every path that resolves a
// profile answers to it — and a run's construction resolves every role, and
// each role at every tier its policy can derive, so such a run refuses at
// START, naming the role and the tier.

// CloudRule is the rule, in ONE place: change it here and nowhere else.
var CloudRule = struct {
	// ModelNamespaces are the Workers AI provider namespaces a model id is
	// spelled in — pi's `cloudflare-workers-ai/…`, omp's `workers-ai/…`, and
	// Workers AI's own bare `@cf/…`. A model is a Workers AI model when its
	// id begins with one of them and names something after it.
	ModelNamespaces []string

	// Harnesses are the runner kinds that reach Workers AI through the
	// factory gateway. A Workers AI id on a harness that cannot call it is
	// not a Workers AI worker.
	Harnesses []string

	// Executors are the executors that dispatch their workers INTO Cloudflare
	// (tick 78v): the cloudflare-sandbox executor boots one worker container
	// per attempt in Cloudflare, whatever substrate selected it — a local
	// run pointing --profiles at the cloud set dispatches through it — so the
	// rule keys on the executor as well as on the substrate. A name here is
	// the rule's, not a model list: which Workers AI model still is a choice.
	Executors []string
}{
	ModelNamespaces: []string{"cloudflare-workers-ai/", "workers-ai/", "@cf/"},
	Harnesses:       []string{"pi"},
	Executors:       []string{"cloudflare-sandbox"},
}

// ErrNotWorkersAI is the refusal a cloud run gets when a role's FINAL resolved
// worker is not a Workers AI model on a harness the gateway serves. It is a
// sentinel so a caller can tell it from every other routing failure, the way
// [ErrNoCloudRouting] names the missing cell.
var ErrNotWorkersAI = errors.New("what runs in Cloudflare runs Workers AI models only, through the factory's Workers AI gateway")

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

// enforceWorkersAI applies [CloudRule] to one role's FINAL resolved profile at
// the tier it was resolved for, naming the role, the tier, the resolved kind
// and model, and the cells that routed it — the last of which is the one to
// edit, because the refusal is over their combination.
func enforceWorkersAI(p *Profile, tier string) error {
	harnessOK, modelOK := reachesWorkersAI(p.Runner), IsWorkersAIModel(p.Model)
	if harnessOK && modelOK {
		return nil
	}
	at := "no tier"
	if tier != "" {
		at = fmt.Sprintf("tier %q", tier)
	}
	var why []string
	if !harnessOK {
		why = append(why, fmt.Sprintf("kind %q cannot reach Workers AI through the factory gateway (want one of %s)",
			p.Runner, strings.Join(CloudRule.Harnesses, ", ")))
	}
	if !modelOK {
		why = append(why, fmt.Sprintf("model %q is not in a Workers AI namespace (%s)",
			p.Model, strings.Join(CloudRule.ModelNamespaces, ", ")))
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
		p.Role, at, p.Runner, p.Model, routed, ErrNotWorkersAI, strings.Join(why, "; "), routeFile)
}
