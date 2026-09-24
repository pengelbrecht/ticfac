package profile

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The operator rule as code (tick nwn): NOTHING IN A CLOUD RUN LEAVES WORKERS
// AI. The refusal is over a role's FINAL resolved worker — the value after
// every overlay (the role's own cell, any tier, the `.tick/runners.cloud.toml`
// cell and that file's own tier cell) — never over one input layer, because
// the three findings this tick absorbed each leaked through a layer the checks
// were written against:
//
//   - a cloud cell with no kind left the runner at the compiled-in claude
//     (dd60e88c);
//   - a tier overlay applied after the cloud cell routed a worker the cloud
//     cell had not sanctioned (ea1a62d3);
//   - a check on the DECLARED cell passed while the FINAL value was claude
//     anyway (577272d7).
//
// The rule is the Workers AI PROVIDER, not a model list: GLM is today's
// choice within it, and a different Workers AI model needs no code change.

const glm53 = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
const glm53Flash = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

// A Workers AI model that is not GLM — the proof the rule is the provider.
const workersAILlama = "cloudflare-workers-ai/@cf/meta/llama-4-scout-17b-16e-instruct"

// cloudRuleCommon is the common runners.toml the cloud file overlays: local
// semantics — implementation on pi, the frontier review and the close-out on
// claude — written for a laptop and exactly what a container must refuse to
// fall back into.
const cloudRuleCommon = `version = 2

[roles.implement]
kind = "pi"
model = "` + glm53 + `"

[roles.implement.tiers.frontier]
model = "anthropic/claude-opus-4"

[roles.review]
kind = "claude"
model = "opus"

[roles.closeout]
kind = "claude"
model = "opus"
`

// cloudRuleConfig writes the common document with the cloud cells beside it,
// and returns the runners.toml path.
func cloudRuleConfig(t *testing.T, cloudCells string) string {
	t.Helper()
	config := writeConfig(t, cloudRuleCommon)
	write(t, filepath.Join(filepath.Dir(config), "runners.cloud.toml"), cloudCells)
	return config
}

// assertCloudRefusal checks a refusal is the rule's and names what it must.
func assertCloudRefusal(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("the cloud substrate resolved a worker that is not a Workers AI model")
	}
	if !errors.Is(err, ErrNotWorkersAI) {
		t.Errorf("the refusal is not recognisable as ErrNotWorkersAI: %v", err)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A cloud cell that names claude outright — kind present, model present, the
// load-time shape checks all green — is refused over the FINAL resolved
// worker, naming the role, the kind and the model. The same role on a local
// substrate keeps its claude routing: the rule is the cloud substrate's.
func TestTheCloudRuleRefusesAFinalWorkerThatIsClaude(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"

[roles.review]
kind = "claude"
model = "opus"
`)

	_, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	assertCloudRefusal(t, err, "review", "claude", "opus", "no tier")

	local, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "herdr"})
	if err != nil {
		t.Fatalf("the cloud rule reached a herdr resolution: %v", err)
	}
	if local.Runner != "claude" || local.Model != "opus" {
		t.Errorf("a herdr review routed to %s/%s, want the frontier claude/opus", local.Runner, local.Model)
	}

	implement, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("pi on a Workers AI model was refused on the cloud substrate: %v", err)
	}
	if implement.Runner != "pi" || implement.Model != glm53 {
		t.Errorf("implement-tick resolved to %s/%s, want pi/%s", implement.Runner, implement.Model, glm53)
	}
}

// A harness that cannot reach Workers AI through the factory gateway is
// refused even when the MODEL is a Workers AI one: claude cannot run it, and a
// Workers AI id on a harness that cannot call it is not a Workers AI worker.
func TestTheCloudRuleRefusesAHarnessTheGatewayDoesNotServe(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "claude"
model = "`+glm53+`"
`)
	_, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud"})
	assertCloudRefusal(t, err, "implement", "claude", glm53)
}

// The tier overlay applied AFTER the cloud cell (finding ea1a62d3): the cloud
// file's own tier cell applies last of all, so a cell that is pi on Workers AI
// at its base can still finally resolve to claude at a tier — and the refusal
// is over that FINAL value, naming the role AND the tier.
func TestTheCloudRuleRefusesTheCloudTierOverlayAfterTheCloudCell(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.review]
kind = "pi"
model = "`+glm53+`"

[roles.review.tiers.frontier]
kind = "claude"
model = "opus"
`)

	base, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the base cloud cell, pi on Workers AI, was refused: %v", err)
	}
	if base.Runner != "pi" || base.Model != glm53 {
		t.Errorf("the untiered review resolved to %s/%s, want pi/%s", base.Runner, base.Model, glm53)
	}

	_, err = Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud", Tier: "frontier"})
	assertCloudRefusal(t, err, "review", `tier "frontier"`, "claude", "opus")
}

// The COMMON file's tier overlay leaking under a cloud cell that routes only
// the kind: the cloud cell sets pi, the common tier's model survives, and the
// final worker is pi on an Anthropic model. Only the final value shows it.
func TestTheCloudRuleRefusesACommonTierModelThatSurvivesTheCloudCell(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi"
`)
	// The untiered role keeps the common file's Workers AI model and is fine.
	if _, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud"}); err != nil {
		t.Fatalf("pi on the common file's Workers AI model was refused: %v", err)
	}
	_, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud", Tier: "frontier"})
	assertCloudRefusal(t, err, "implement", `tier "frontier"`, "pi", "anthropic/claude-opus-4")
}

// The compiled-in default (finding dd60e88c): when no file routes a role's
// MODEL — the common cell names only a kind, the cloud cell only pi — the
// model is the shipped profile's, and the shipped profiles are claude models.
// The final check refuses pi-on-opus exactly as it refuses claude-on-opus.
func TestTheCloudRuleRefusesTheCompiledInDefault(t *testing.T) {
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"

[roles.closeout]
kind = "claude"
`)
	write(t, filepath.Join(filepath.Dir(config), "runners.cloud.toml"), `version = 2

[roles.implement]
kind = "pi"

[roles.closeout]
kind = "pi"
`)
	shipped, err := Resolve("closeout-epic", Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resolve("closeout-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	assertCloudRefusal(t, err, "closeout", "pi", shipped.Model)
}

// A cloud cell with no kind (finding dd60e88c): the load-time check refuses
// it, naming the cell to fix. The final check stands behind it, not instead.
func TestTheCloudRuleRefusesACellWithNoKind(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.review]
model = "`+glm53+`"
`)
	_, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err == nil {
		t.Fatal("a cloud role cell with no kind resolved")
	}
	for _, want := range []string{"review", "kind"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// What the rule accepts: pi on ANY Workers AI model — GLM today, and a
// non-GLM Workers AI model with no code change — at the base cell and at a
// tier, for every role.
func TestTheCloudRuleAcceptsAnyWorkersAIModel(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"

[roles.implement.tiers.economy]
model = "`+glm53Flash+`"

[roles.implement.tiers.frontier]
model = "`+workersAILlama+`"

[roles.review]
kind = "pi"
model = "`+workersAILlama+`"

[roles.closeout]
kind = "pi"
model = "`+glm53+`"
`)

	all, err := ResolveAll(Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the sanctioned cloud routing did not resolve: %v", err)
	}
	if all["review-epic"].Model != workersAILlama {
		t.Errorf("review-epic resolved to %s, want the non-GLM Workers AI %s", all["review-epic"].Model, workersAILlama)
	}
	for tier, want := range map[string]string{"economy": glm53Flash, "frontier": workersAILlama} {
		p, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud", Tier: tier})
		if err != nil {
			t.Fatalf("tier %s, pi on %s, was refused: %v", tier, want, err)
		}
		if p.Runner != "pi" || p.Model != want {
			t.Errorf("tier %s resolved to %s/%s, want pi/%s", tier, p.Runner, p.Model, want)
		}
	}
}

// The rule itself: the provider namespace of the model, not the model family.
func TestIsWorkersAIModel(t *testing.T) {
	for _, model := range []string{
		glm53,
		glm53Flash,
		workersAILlama,
		"workers-ai/@cf/zai-org/glm-5.3",
		"@cf/openai/gpt-oss-120b",
		"cloudflare-workers-ai/@cf/openai/gpt-oss-120b",
	} {
		if !IsWorkersAIModel(model) {
			t.Errorf("%q is a Workers AI model and was not recognised as one", model)
		}
	}
	for _, model := range []string{
		"",
		"opus",
		"sonnet",
		"claude-opus-4",
		"glm-5.3",
		"zai/glm-5.3",
		"anthropic/claude-opus-4",
		"openai/gpt-5.6-luna",
		"openrouter/cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		"cloudflare-workers-ai/",
		"@cf/",
	} {
		if IsWorkersAIModel(model) {
			t.Errorf("%q passed as a Workers AI model", model)
		}
	}
}
