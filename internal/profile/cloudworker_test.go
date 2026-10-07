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
		t.Fatal("the cloud substrate resolved a worker the cloud billing rule refuses")
	}
	if !errors.Is(err, ErrCloudBilling) {
		t.Errorf("the refusal is not recognisable as ErrCloudBilling: %v", err)
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A cloud cell that names claude on a PINNED model id — kind present, model
// present, the load-time shape checks all green, but a pinned claude id
// bills PER TOKEN — is refused over the FINAL resolved worker, naming the
// role, the kind and the model. The same role on a local substrate keeps its
// claude routing: the rule is the cloud's billing boundary, not a model ban.
func TestTheCloudRuleRefusesAFinalWorkerOnAPinnedClaudeModel(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"

[roles.review]
kind = "claude"
model = "claude-opus-5-5"
`)

	_, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	assertCloudRefusal(t, err, "review", "claude", "claude-opus-5-5", "no tier", "sonnet, opus")

	local, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "herdr"})
	if err != nil {
		t.Fatalf("the cloud rule reached a herdr resolution: %v", err)
	}
	// The cloud overlay is not applied on herdr, so the review keeps the
	// common file's claude/opus: the rule is the cloud's billing boundary,
	// never a model ban that reaches a laptop.
	if local.Runner != "claude" || local.Model != "opus" {
		t.Errorf("a herdr review routed to %s/%s, want the common file's claude/opus", local.Runner, local.Model)
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

// The hosted harness is one the gateway serves (epic 43y, tick qf4): the
// cloud profile set names `pi-durable` — the kind a hosted attempt runs on,
// whatever harness the dispatch names for the container its tools run in
// (tick 4uj) — so a role the cloud routing resolves to it on a Workers AI
// model is NOT refused at start: the rule is the provider, and the harness
// list is the factory gateway's, which serves the hosted kind.
func TestTheCloudRuleAdmitsTheHostedHarness(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi-durable"
model = "`+glm53+`"
`)
	p, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the hosted harness on a Workers AI model was refused by the cloud rule: %v", err)
	}
	if p.Runner != "pi-durable" || p.Model != glm53 {
		t.Errorf("implement-tick resolved to %s/%s, want pi-durable/%s", p.Runner, p.Model, glm53)
	}

	// And the raw cloud set resolves the same way: no config at all, the
	// rule keyed on the executor the set names — the shape a start check
	// sees before any repository routing applies.
	raw, err := Resolve("implement-tick", Options{Dir: cloudProfileDir(t)})
	if err != nil {
		t.Fatalf("the cloud set's own pi-durable pairing was refused: %v", err)
	}
	if raw.Runner != "pi-durable" || raw.Model != glm53 {
		t.Errorf("the cloud set's own pairing is %s/%s, want pi-durable/%s", raw.Runner, raw.Model, glm53)
	}
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
model = "claude-opus-5-5"
`)

	base, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the base cloud cell, pi on Workers AI, was refused: %v", err)
	}
	if base.Runner != "pi" || base.Model != glm53 {
		t.Errorf("the untiered review resolved to %s/%s, want pi/%s", base.Runner, base.Model, glm53)
	}

	_, err = Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud", Tier: "frontier"})
	assertCloudRefusal(t, err, "review", `tier "frontier"`, "claude", "claude-opus-5-5")
}

// The same tier overlay on the subscription rung: a tier cell that names
// claude on a VERSIONLESS ALIAS is admitted at the tier, exactly as at a
// base cell — the rung is the rule's other half, not an exception one layer
// down. The step-down target is the rung's own Workers AI fallback, decided
// at dispatch by the factory, never by a config cell.
func TestTheCloudRuleAdmitsTheSubscriptionRungAtATier(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"

[roles.implement.tiers.frontier]
kind = "claude"
model = "opus"
`)
	p, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud", Tier: "frontier"})
	if err != nil {
		t.Fatalf("claude on the versionless opus alias was refused at a tier: %v", err)
	}
	if p.Runner != "claude" || p.Model != "opus" {
		t.Errorf("the frontier tier resolved to %s/%s, want the claude/opus subscription rung", p.Runner, p.Model)
	}
	base, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the base cell, pi on Workers AI, was refused: %v", err)
	}
	if base.Runner != "pi" || base.Model != glm53 {
		t.Errorf("the untiered role resolved to %s/%s, want pi/%s", base.Runner, base.Model, glm53)
	}
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

// The subscription rung (tick 6fv): a cloud cell that names a rung harness
// on a VERSIONLESS alias resolves — claude on sonnet or opus is billed to
// the operator's Claude subscription, injected by the factory's claude-sub
// wiring, never per token. This is the rule's OTHER half, admitted at a base
// cell and at a tier, for every role that can name it.
func TestTheCloudRuleAdmitsTheClaudeSubscriptionRung(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.review]
kind = "claude"
model = "opus"

[roles.closeout]
kind = "claude"
model = "opus"
`)

	all, err := ResolveAll(Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the claude-sub ladder did not resolve on the cloud substrate: %v", err)
	}
	if all["implement-tick"].Runner != "claude" || all["implement-tick"].Model != "sonnet" {
		t.Errorf("implement-tick resolved to %s/%s, want the claude/sonnet rung",
			all["implement-tick"].Runner, all["implement-tick"].Model)
	}
	for _, role := range []string{"review-epic", "closeout-epic"} {
		p := all[role]
		if p.Runner != "claude" || p.Model != "opus" {
			t.Errorf("%s resolved to %s/%s, want the claude/opus rung", role, p.Runner, p.Model)
		}
	}
}

// The rung admits ONLY the versionless aliases: a pinned claude id —
// claude-opus-5-5, anthropic/claude-opus-4 — bills PER TOKEN wherever it
// runs, and per-token spend in the cloud is what the rule exists to refuse.
// The refusal names the aliases, because the fix is a config edit: spell the
// alias, and the subscription pays.
func TestTheCloudRuleRefusesAPinnedClaudeModelOnTheRungHarness(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "anthropic/claude-opus-4", "claude-opus-5"} {
		config := cloudRuleConfig(t, `version = 2

[roles.review]
kind = "claude"
model = "`+model+`"
`)
		_, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
		assertCloudRefusal(t, err, "review", "claude", model, "sonnet, opus")
	}
}

// A subscription alias on a harness that is NOT the rung's does not make a
// subscription worker: pi cannot speak the subscription's OAuth dialect, so
// pi on opus is refused as firmly as claude on a Workers AI id is — the rung
// is the PAIR, never either half alone.
func TestTheCloudRuleRefusesASubscriptionAliasOnAnotherHarness(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.review]
kind = "pi"
model = "opus"
`)
	_, err := Resolve("review-epic", Options{RunnersConfig: config, Substrate: "cloud"})
	assertCloudRefusal(t, err, "review", "pi", "opus")
}

// The rungs themselves are well-formed: every rung names a harness, every
// model is a versionless alias (no provider namespace in it — a pinned id
// must never read as an alias), and every fallback is a Workers AI model the
// factory can step the role down to. This is the guard that keeps a future
// edit to CloudRule from admitting per-token spend by accident.
func TestEverySubscriptionRungIsWellFormed(t *testing.T) {
	if len(CloudRule.SubscriptionRungs) == 0 {
		t.Fatal("CloudRule declares no subscription rungs — the claude-sub wiring (tick 6fv) has nothing to admit")
	}
	for _, rung := range CloudRule.SubscriptionRungs {
		if rung.Harness == "" {
			t.Errorf("a subscription rung with no harness cannot be a rung: %+v", rung)
		}
		if len(rung.Models) == 0 {
			t.Errorf("the %q rung names no aliases: no config could select it", rung.Harness)
		}
		for _, model := range rung.Models {
			if model == "" || strings.ContainsAny(model, "/") {
				t.Errorf("%q is not a versionless alias — the %q rung must name the CLI's own alias words, never a pinned id", model, rung.Harness)
			}
		}
		if !IsWorkersAIModel(rung.Fallback) {
			t.Errorf("the %q rung's fallback %q is not a Workers AI model — the step-down target must be one the gateway serves", rung.Harness, rung.Fallback)
		}
	}
}

// The three provider namespaces are three spellings of one provider route,
// and a model id beneath any of them is ONE model however it is spelled: the
// rule declares the rung fallback in pi's namespace, the factory's step-down
// answers in omp's, and the executor's acceptance compares the two — so the
// core must fold every spelling of one model to one string and keep
// different models different.
func TestWorkersAIModelCoreFoldsTheNamespacesToOneModel(t *testing.T) {
	const core = "zai-org/glm-5.3"
	for _, id := range []string{
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3", // pi's spelling
		"workers-ai/@cf/zai-org/glm-5.3",            // omp's spelling
		"@cf/zai-org/glm-5.3",                       // Workers AI's own
	} {
		if got := WorkersAIModelCore(id); got != core {
			t.Errorf("WorkersAIModelCore(%q) = %q, want %q — the namespaces are spellings of one route, not different models", id, got, core)
		}
		if !IsWorkersAIModel(id) {
			t.Errorf("%q is not read as a Workers AI model", id)
		}
	}
	// A different model stays different, namespace or none.
	if got := WorkersAIModelCore("cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"); got != "zai-org/glm-5.3-flash" {
		t.Errorf("WorkersAIModelCore folded %q onto %q's core: a different model must stay different", got, core)
	}
	// And an id in no namespace — a subscription alias, a pinned vendor id —
	// is its own string, never folded onto a namespaced model's core.
	if got := WorkersAIModelCore("sonnet"); got != "sonnet" {
		t.Errorf("WorkersAIModelCore(\"sonnet\") = %q, want it unchanged: an alias is not a namespaced model's core", got)
	}
}

// CloudBillingAllows is the rule's ONE predicate, the one the factory's
// executor and any other consumer answer to: a Workers AI worker (harness the
// gateway serves, model in a Workers AI namespace) or a subscription rung
// (rung harness, versionless alias).
func TestCloudBillingAllows(t *testing.T) {
	for _, tc := range []struct {
		harness, model string
		want           bool
	}{
		{"pi", glm53, true},
		{"pi-durable", glm53Flash, true},
		{"pi", workersAILlama, true},
		{"claude", "sonnet", true},
		{"claude", "opus", true},
		{"claude", glm53, false},
		{"claude", "claude-opus-5-5", false},
		{"claude", "anthropic/claude-opus-4", false},
		{"pi", "opus", false},
		{"pi-durable", "opus", false},
		{"omp", glm53, false},
		{"claude", "", false},
	} {
		if got := CloudBillingAllows(tc.harness, tc.model); got != tc.want {
			t.Errorf("CloudBillingAllows(%q, %q) = %v, want %v", tc.harness, tc.model, got, tc.want)
		}
	}
}

// The rule's harness allowlist is the union a dispatching executor admits:
// every harness the gateway serves, plus every subscription rung's. A rung
// whose harness no executor accepts is a rung no run can dispatch, and an
// executor list that omits one is a resolution the run refuses twice.
func TestTheRuleNamesEveryHarnessAWorkerMayRun(t *testing.T) {
	allowed := CloudAllowedHarnesses()
	for _, h := range append(append([]string{}, CloudRule.Harnesses...), subscriptionHarnesses()...) {
		found := false
		for _, a := range allowed {
			if a == h {
				found = true
			}
		}
		if !found {
			t.Errorf("%q is missing from the rule's harness allowlist %q", h, allowed)
		}
	}
	if len(allowed) != len(CloudRule.Harnesses)+len(subscriptionHarnesses()) {
		t.Errorf("the allowlist %q is not the union of the gateway harnesses and the rung harnesses", allowed)
	}
}

// The rule keys on WHAT RUNS IN CLOUDFLARE (tick 78v), not on the cloud
// substrate alone: a run whose substrate is LOCAL — herdr, harness, or none
// named at all — can still select the cloudflare-sandbox executor by pointing
// --profiles at the cloud set (profiles-cloudflare-sandbox/), and then its
// workers boot in a Cloudflare container under local routing, which is as
// much Cloudflare as the cloud substrate is. The operator's rule is about
// what runs in Cloudflare, so the refusal keys on the executor that
// dispatches into Cloudflare as well as on the substrate — declared in ONE
// place, CloudRule.Executors, the way the namespaces are.
func TestTheCloudRuleKeysOnTheExecutorThatDispatchesIntoCloudflare(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"

[roles.implement.tiers.frontier]
kind = "claude"
model = "claude-opus-5-5"
`)
	cloud := cloudProfileDir(t)

	// The base cell, pi on a Workers AI model, resolves on every substrate —
	// local ones included, because the rule is not a refusal of the executor.
	for _, sub := range []string{"herdr", "harness", ""} {
		p, err := Resolve("implement-tick", Options{Dir: cloud, RunnersConfig: config, Substrate: sub})
		if err != nil {
			t.Fatalf("the sandbox executor on %q refused pi on a Workers AI model: %v", sub, err)
		}
		// …and binds the hosted name of the durable harness: the worker boots
		// in a sandbox container whatever the substrate (tick twa).
		if p.Runner != HostedDurableHarness || p.Model != glm53 {
			t.Errorf("the %q base resolution is %s/%s, want %s/%s", sub, p.Runner, p.Model, HostedDurableHarness, glm53)
		}
	}

	// The tier that leaves the sanctioned rungs refuses on every substrate —
	// the local ones a sandbox-executor run can execute on (where the COMMON
	// file's frontier tier is the model that leaks: a pinned Anthropic id on
	// the durable harness), the substrate-blind resolution alike, and the
	// cloud substrate's own overlay (whose tier cell names a pinned id too) —
	// naming the role, the tier, the resolved kind and model, and the file
	// the routing lives in for that substrate.
	for _, sub := range []string{"herdr", "harness", ""} {
		_, err := Resolve("implement-tick", Options{Dir: cloud, RunnersConfig: config, Substrate: sub, Tier: "frontier"})
		routeFile := ".tick/runners.toml"
		if sub != "" {
			routeFile = ".tick/runners.local.toml"
		}
		assertCloudRefusal(t, err, "implement", `tier "frontier"`, HostedDurableHarness, "anthropic/claude-opus-4", routeFile)
	}
	_, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "cloud", Tier: "frontier"})
	assertCloudRefusal(t, err, "implement", `tier "frontier"`, "claude", "claude-opus-5-5", "runners.cloud.toml")
}

// The finding's exact shape, updated for the subscription rung: the claude
// tier lives in .tick/runners.local.toml — a laptop's ladder, the file the
// cloud substrate never reads — and a LOCAL run selecting the
// cloudflare-sandbox executor would boot its worker in a Cloudflare container
// on it. A tier on the versionless alias is the subscription rung and is
// admitted exactly as it is in the cloud overlay; a tier on a PINNED id bills
// per token wherever the container boots, and the refusal names the local
// override file, the one an operator reading it can actually edit.
func TestTheCloudRuleRefusesTheLocalLadderOnTheSandboxExecutor(t *testing.T) {
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"
`)
	write(t, filepath.Join(filepath.Dir(config), "runners.local.toml"), `version = 2

[roles.implement.tiers.frontier]
kind = "claude"
model = "claude-opus-5-5"
`)
	cloud := cloudProfileDir(t)
	for _, sub := range []string{"herdr", "harness"} {
		_, err := Resolve("implement-tick", Options{Dir: cloud, RunnersConfig: config, Substrate: sub, Tier: "frontier"})
		assertCloudRefusal(t, err, "implement", `tier "frontier"`, "claude", "claude-opus-5-5", "runners.local.toml")
	}
	// The same ladder's versionless alias is the subscription rung: a
	// sandbox-executor run may climb to claude on the subscription, on every
	// substrate that can dispatch into Cloudflare.
	write(t, filepath.Join(filepath.Dir(config), "runners.local.toml"), `version = 2

[roles.implement.tiers.frontier]
kind = "claude"
model = "opus"
`)
	for _, sub := range []string{"herdr", "harness"} {
		p, err := Resolve("implement-tick", Options{Dir: cloud, RunnersConfig: config, Substrate: sub, Tier: "frontier"})
		if err != nil {
			t.Fatalf("substrate %q refused the claude/opus subscription rung on the sandbox executor: %v", sub, err)
		}
		if p.Runner != "claude" || p.Model != "opus" {
			t.Errorf("substrate %q: the frontier tier resolved to %s/%s, want claude/opus", sub, p.Runner, p.Model)
		}
	}
	// The same ladder on the compiled-in LOCAL set — whose executor is the
	// local subprocess one, dispatching nothing into Cloudflare — keeps its
	// claude tier: the rule is keyed on the executor that dispatches into
	// Cloudflare, never on the claude name or on every local run.
	local, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: "herdr", Tier: "frontier"})
	if err != nil {
		t.Fatalf("the rule reached a local-executor resolution: %v", err)
	}
	if local.Runner != "claude" || local.Model != "opus" {
		t.Errorf("a herdr run's frontier tier resolved to %s/%s, want the local claude/opus", local.Runner, local.Model)
	}
}

// The rule's executor half, declared in the one place the namespaces are:
// CloudRule.Executors names the executors that dispatch their workers into
// Cloudflare, and nothing else answers for the refusal — not a substrate
// check alone, and not a model list.
func TestTheCloudRuleDeclaresTheExecutorsThatDispatchIntoCloudflare(t *testing.T) {
	if !dispatchesIntoCloudflare(cloudExecutorName) {
		t.Errorf("%q is not in CloudRule.Executors: the executor that boots one worker container per attempt in Cloudflare is the one the rule must key on", cloudExecutorName)
	}
	for _, executor := range []string{"", "local-subprocess", "herdr"} {
		if dispatchesIntoCloudflare(executor) {
			t.Errorf("%q dispatches nothing into Cloudflare but passed as if it did", executor)
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

// A dispatch INTO Cloudflare binds the hosted name of the durable harness
// (epic 43y, tick twa). A runner table names that harness `pi` — the common
// file since tick hpk, and this repository's cloud overlay too — but the
// sandbox image a cloudflare-sandbox dispatch boots hosts it as `pi-durable`
// and dies at boot on any other kind ("unknown harness kind 'pi'", tick
// jhp). The resolved runner is what the door binds as TICKS_HARNESS, so a
// resolution that left `pi` standing for a Cloudflare dispatch was a run
// that started and lost every worker container at boot. On every substrate
// that can select the executor — the cloud's, and a local one pointing
// --profiles at the cloud set (tick 78v) — the final runner is the hosted
// kind; a profile that dispatches nothing into Cloudflare keeps the runner
// table's own name, which is the local executors' kind for the harness.
func TestACloudflareDispatchBindsTheHostedNameOfTheDurableHarness(t *testing.T) {
	config := cloudRuleConfig(t, `version = 2

[roles.implement]
kind = "pi"
model = "`+glm53+`"

[roles.implement.tiers.economy]
model = "`+glm53Flash+`"

[roles.review]
kind = "pi"
model = "`+glm53+`"

[roles.closeout]
kind = "pi"
model = "`+glm53+`"
`)
	cloud := cloudProfileDir(t)
	for _, sub := range []string{"cloud", "herdr", "harness", ""} {
		for _, tc := range []struct{ role, tier string }{
			{"implement-tick", ""}, {"implement-tick", "economy"}, {"review-epic", ""}, {"closeout-epic", ""},
		} {
			if sub != "cloud" && (tc.role != "implement-tick" || tc.tier != "") {
				continue // the other cells are the cloud overlay's: a local substrate reads the common file's
			}
			p, err := Resolve(tc.role, Options{Dir: cloud, RunnersConfig: config, Substrate: sub, Tier: tc.tier})
			if err != nil {
				t.Fatalf("substrate %q: %s at tier %q did not resolve: %v", sub, tc.role, tc.tier, err)
			}
			if p.Runner != HostedDurableHarness {
				t.Errorf("substrate %q: %s at tier %q dispatches into Cloudflare on runner %q, want the hosted %q: "+
					"the sandbox image refuses any other kind at boot", sub, tc.role, tc.tier, p.Runner, HostedDurableHarness)
			}
		}
	}

	// Nothing dispatched into Cloudflare: the local set keeps the runner
	// table's name, the kind the local executors host the harness under.
	for _, sub := range []string{"herdr", "harness", ""} {
		p, err := Resolve("implement-tick", Options{RunnersConfig: config, Substrate: sub})
		if err != nil {
			t.Fatalf("substrate %q: the local implement-tick did not resolve: %v", sub, err)
		}
		if p.Runner != "pi" {
			t.Errorf("substrate %q: the local implement-tick resolved runner %q, want the runner table's own pi", sub, p.Runner)
		}
	}
}
