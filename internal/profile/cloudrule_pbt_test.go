package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

// The operator's rule as a property (tick 89g): whatever the three config
// files say — common, cloud override, local override — and whatever tier is
// asked for, a role that runs IN CLOUDFLARE either refuses to resolve or
// resolves to a Workers AI model on a harness the gateway serves. Nothing
// resolves to claude (or any other provider) in a Cloudflare container.
//
// Two ways into Cloudflare, both covered: the cloud substrate, and a local
// substrate whose profiles dispatch through the cloudflare-sandbox executor
// (tick 78v).

var (
	// Weighted toward valid Workers AI cells, so most configs resolve and the
	// property is exercised; the rest try every way out.
	pbtKinds  = []string{"pi", "pi", "pi", "pi", "claude", "codex", "opencode"}
	pbtModels = []string{
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3", "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash",
		"workers-ai/@cf/openai/gpt-oss-120b", "@cf/meta/llama", "cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		"opus", "sonnet", "gpt-5.6-luna", "openrouter/anthropic/claude", "cloudflare-workers-ai/",
	}
	pbtRoles = []string{"implement", "review", "closeout"}
	pbtTiers = []string{"economy", "balanced", "strong", "frontier"}
)

// genCell draws one role or tier cell: some subset of kind and model.
func genCell(tc hegel.TestCase, header string, requireKind bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n[%s]\n", header)
	wrote := false
	if requireKind || hegel.Draw(tc, hegel.Booleans()) {
		fmt.Fprintf(&b, "kind = %q\n", hegel.Draw(tc, hegel.SampledFrom(pbtKinds)))
		wrote = true
	}
	if hegel.Draw(tc, hegel.Booleans()) || !wrote {
		fmt.Fprintf(&b, "model = %q\n", hegel.Draw(tc, hegel.SampledFrom(pbtModels)))
	}
	return b.String()
}

// genFile draws a runners file: role cells and tier cells for some roles.
func genFile(tc hegel.TestCase, common bool) string {
	var b strings.Builder
	b.WriteString("version = 2\n")
	for _, role := range pbtRoles {
		// The common file must define implement (and every role needs a kind
		// there); an override may leave any role out.
		if !common && !hegel.Draw(tc, hegel.Booleans()) {
			continue
		}
		b.WriteString(genCell(tc, "roles."+role, common))
		for _, tier := range pbtTiers {
			if hegel.Draw(tc, hegel.Integers(0, 3)) == 0 {
				b.WriteString(genCell(tc, "roles."+role+".tiers."+tier, false))
			}
		}
	}
	return b.String()
}

func TestPBTNothingInCloudflareResolvesOutsideWorkersAI(t *testing.T) {
	cloudDir := cloudProfileDir(t)
	hegel.Test(t, func(ht *hegel.T) {
		dir := t.TempDir()
		files := map[string]string{"runners.toml": genFile(ht, true)}
		if hegel.Draw(ht, hegel.Booleans()) {
			files["runners.cloud.toml"] = genFile(ht, false)
		}
		if hegel.Draw(ht, hegel.Booleans()) {
			files["runners.local.toml"] = genFile(ht, false)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				ht.Fatalf("%v", err)
			}
		}
		role := hegel.Draw(ht, hegel.SampledFrom([]string{"implement-tick", "review-epic", "closeout-epic", "resolve-conflict", "plan-repair"}))
		tier := hegel.Draw(ht, hegel.SampledFrom(append([]string{""}, pbtTiers...)))

		// Into Cloudflare by substrate, or by executor from a local substrate.
		sub := hegel.Draw(ht, hegel.SampledFrom([]string{"cloud", "herdr", "harness", ""}))
		p, err := Resolve(role, Options{Dir: cloudDir, RunnersConfig: filepath.Join(dir, "runners.toml"), Substrate: sub, Tier: tier})
		if err != nil {
			pbtOutcomes["refused"]++
			return // a refusal is always allowed
		}
		pbtOutcomes["resolved"]++
		if p.Runner != "pi" || !IsWorkersAIModel(p.Model) {
			var shown strings.Builder
			for name, body := range files {
				fmt.Fprintf(&shown, "--- %s\n%s\n", name, body)
			}
			ht.Fatalf("role %s at tier %q on substrate %q resolved to %s/%s in Cloudflare:\n%s",
				role, tier, sub, p.Runner, p.Model, shown.String())
		}
	}, hegel.WithTestCases(500))
	// Not vacuous: a property that only ever sees refusals proves nothing.
	if pbtOutcomes["resolved"] < 20 {
		t.Errorf("only %d of the generated configs resolved at all (%d refused): the generator is not exercising the rule", pbtOutcomes["resolved"], pbtOutcomes["refused"])
	}
	t.Logf("resolved %d, refused %d", pbtOutcomes["resolved"], pbtOutcomes["refused"])
}

var pbtOutcomes = map[string]int{}
