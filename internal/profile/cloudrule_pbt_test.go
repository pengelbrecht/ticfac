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
//
// THE GENERATOR: A RESOLVABLE CORE, THEN MUTATIONS.
// Every case starts from a core that satisfies the rule by construction —
// all three files, every role, every cell a durable-harness kind (pi or
// pi-durable) on a Workers AI model — and about half the cases then mutate it: each cell and each
// structural choice (a file present, a role declared, a key written) is
// independently swapped, one time in three, for an arbitrary one drawn from
// every way out (other harnesses, other providers' models, a bare
// namespace). A core resolves; a mutant often refuses and sometimes
// resolves, and the property is the one assertion that matters for both:
// whatever resolves, resolves to Workers AI.
//
// The coverage check used to be "at least 20 resolved" over a generator with
// no core, where a resolution was a ~6% event: 500 draws landed on 17 often
// enough to fail a gate now and then, and the counter was a package global
// that -count added up across runs, hiding exactly the thin run it was there
// to catch. Now the counts are this test's own, the core is checked to
// resolve every time, and the overall share is a proportion of the draws
// actually made.

var (
	// The rule-satisfying pools a core draws from. Both names of the one
	// durable harness are good (epic 43y, tick qf4): `pi` is the local
	// runner table's name for it (tick hpk), `pi-durable` the hosted kind
	// the cloud profile set names — both reach Workers AI through the
	// factory gateway.
	pbtGoodKinds  = []string{"pi", "pi-durable"}
	pbtGoodModels = []string{
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3", "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash",
		"workers-ai/@cf/openai/gpt-oss-120b", "@cf/meta/llama",
	}
	// The pools a mutation draws from: every way out, and the good ones too.
	pbtKinds  = []string{"pi", "pi-durable", "claude", "codex", "opencode"}
	pbtModels = append([]string{
		"opus", "sonnet", "gpt-5.6-luna", "openrouter/anthropic/claude", "cloudflare-workers-ai/",
	}, pbtGoodModels...)
	pbtRoles = []string{"implement", "review", "closeout"}
	pbtTiers = []string{"economy", "balanced", "strong", "frontier"}
)

// pbtGen draws one case. mutate is false for a pure core.
type pbtGen struct {
	tc     hegel.TestCase
	mutate bool
}

// mutated answers whether this choice departs from the core: never for a
// core, one time in three for a mutant.
func (g pbtGen) mutated() bool {
	return g.mutate && hegel.Draw(g.tc, hegel.Integers(0, 2)) == 0
}

// cell draws one role or tier cell. A core cell writes both keys from the
// good pools; a mutant may drop one key (never both) and may draw either
// value from the full pools.
func (g pbtGen) cell(header string, requireKind bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n[%s]\n", header)
	writeKind := requireKind || !g.mutated()
	writeModel := !writeKind || !g.mutated()
	if writeKind {
		kinds := pbtGoodKinds
		if g.mutated() {
			kinds = pbtKinds
		}
		fmt.Fprintf(&b, "kind = %q\n", hegel.Draw(g.tc, hegel.SampledFrom(kinds)))
	}
	if writeModel {
		models := pbtGoodModels
		if g.mutated() {
			models = pbtModels
		}
		fmt.Fprintf(&b, "model = %q\n", hegel.Draw(g.tc, hegel.SampledFrom(models)))
	}
	return b.String()
}

// file draws a runners file. The common file declares every role, with a
// kind, and in the core every tier of it too: a tier asked for that the
// role does not declare is a refusal, never a fall back to the role's cell,
// so a core without its tiers would not resolve. A mutant drops a common
// tier one time in three. An override may leave a role out (a mutant does,
// one time in three) and declares each tier one time in four, core or not.
func (g pbtGen) file(common bool) string {
	var b strings.Builder
	b.WriteString("version = 2\n")
	for _, role := range pbtRoles {
		if !common && g.mutated() {
			continue
		}
		b.WriteString(g.cell("roles."+role, common))
		for _, tier := range pbtTiers {
			declare := hegel.Draw(g.tc, hegel.Integers(0, 3)) == 0
			if common {
				declare = !g.mutated()
			}
			if declare {
				b.WriteString(g.cell("roles."+role+".tiers."+tier, false))
			}
		}
	}
	return b.String()
}

func TestPBTNothingInCloudflareResolvesOutsideWorkersAI(t *testing.T) {
	cloudDir := cloudProfileDir(t)
	// This test's own counts: a fresh set per run, so -count cannot add a
	// thin run to a fat one.
	var draws, resolved, coreDraws, coreResolved int
	hegel.Test(t, func(ht *hegel.T) {
		g := pbtGen{tc: ht, mutate: hegel.Draw(ht, hegel.Booleans())}
		dir := t.TempDir()
		files := map[string]string{"runners.toml": g.file(true)}
		if !g.mutated() {
			files["runners.cloud.toml"] = g.file(false)
		}
		if !g.mutated() {
			files["runners.local.toml"] = g.file(false)
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
		draws++
		if !g.mutate {
			coreDraws++
		}
		if err != nil {
			return // a refusal is always allowed
		}
		resolved++
		if !g.mutate {
			coreResolved++
		}
		if !reachesWorkersAI(p.Runner) || !IsWorkersAIModel(p.Model) {
			var shown strings.Builder
			for name, body := range files {
				fmt.Fprintf(&shown, "--- %s\n%s\n", name, body)
			}
			ht.Fatalf("role %s at tier %q on substrate %q resolved to %s/%s in Cloudflare, not a Workers AI model on a harness the gateway serves:\n%s",
				role, tier, sub, p.Runner, p.Model, shown.String())
		}
	}, hegel.WithTestCases(500))
	t.Logf("resolved %d of %d draws; cores resolved %d of %d", resolved, draws, coreResolved, coreDraws)
	// Not vacuous: a property that only ever sees refusals proves nothing.
	// The core resolves by construction, so a core that refuses is a broken
	// generator (or a resolver refusing an all-Workers-AI config — worth
	// knowing either way), and the share resolved is measured against the
	// draws this run actually made.
	if coreResolved != coreDraws {
		t.Errorf("%d of %d rule-satisfying cores refused to resolve: the generator's core no longer satisfies the resolver",
			coreDraws-coreResolved, coreDraws)
	}
	if draws == 0 || resolved*4 < draws {
		t.Errorf("only %d of %d generated configs resolved (under a quarter): the generator is not exercising the rule", resolved, draws)
	}
}
