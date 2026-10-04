package runconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This repo dogfoods its own run config the same way ticks does: the
// execution-half reader this package is must load the `.tick/runners.toml`
// committed HERE — the file that routes the very workers building ticfac —
// through the same entry point a run will use. ticks' copy of this test also
// proves the repo carries no legacy structured sections in `.tick/config.md`
// (running the migrator finds nothing to move); that assertion is the
// migrator's own and stayed in ticks with the migrator.

// repoRootForTest walks up from this source file to the module root.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate the repo root")
	}
	// internal/runconfig -> repo root is TWO levels. It was three while this
	// package lived at internal/herd/config; the package moved out from under
	// the herdr tree so the reconciler could import it without tripping the
	// executor's seam check (it is executor-agnostic config, not herdr's).
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected a go.mod at %s: %v", root, err)
	}
	return root
}

// TestRepoRunnersConfigIsLoadable proves the committed config passes this
// package — the reader whose decision procedure and role resolution later
// Phase 2 ticks build on. A mistake in the repo's own routing file breaks
// every future run rather than failing somebody's unit test.
func TestRepoRunnersConfigIsLoadable(t *testing.T) {
	cfg, err := LoadRepo(repoRootForTest(t))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadRepo returned no config; this repo commits .tick/runners.toml")
	}
}

// TestRepoRunnersConfigCarriesTheCommandSurface proves the command tables the
// run relies on actually arrived, and that the acceptance-free shape this
// repository keeps (testing + environment, no evidence) is what the reader
// sees — the whole file validates or the run does not start.
func TestRepoRunnersConfigCarriesTheCommandSurface(t *testing.T) {
	cfg, err := LoadRepo(repoRootForTest(t))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	if cfg.Testing == nil || len(cfg.Testing.Commands) == 0 {
		t.Error("[testing.commands] is empty; the integrated gate is declared nowhere")
	}
	if cfg.Environment == nil || len(cfg.Environment.Commands) == 0 {
		t.Error("[environment.commands] is empty; the run-start pre-flight is declared nowhere")
	}
	if cfg.Evidence != nil {
		t.Error("[evidence] is present; this repository keeps close-out authorization out of the file")
	}
	// The routing this repository actually runs: the implement role carries a
	// tier overlay table, and tier overlays change the model.
	if role := cfg.Roles["implement"]; role == nil || role.Tiers["economy"] == nil || role.Tiers["strong"] == nil {
		t.Errorf("[roles.implement] does not carry the tier overlays this epic routes on: %+v", role)
	}
}

// TestRepoRunnersConfigResolvesEveryRole the way a dispatch will: through
// [Config.Resolve], including the fallback cells (a role with no entry of its
// own resolves against implement) and the tier overlays the epic actually
// names. Resolve is the seam ticks' internal/sandbox reads through, so it
// must answer for this repo's own file.
func TestRepoRunnersConfigResolvesEveryRole(t *testing.T) {
	cfg, err := LoadRepo(repoRootForTest(t))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	for _, role := range []string{RoleImplement, "review", "closeout", "plan", "scout"} {
		for _, tier := range []Tier{"", TierEconomy, TierBalanced, TierStrong, TierFrontier} {
			w, err := cfg.Resolve(role, tier)
			if err != nil {
				t.Errorf("Resolve(%q, %q): %v", role, tier, err)
				continue
			}
			if w.Kind == "" {
				t.Errorf("Resolve(%q, %q) produced no kind", role, tier)
			}
		}
	}
	// The strong tier of the implement role is the same model as the base —
	// the routing this file documents in its own comments.
	w, err := cfg.Resolve(RoleImplement, TierStrong)
	if err != nil {
		t.Fatalf("Resolve(implement, strong): %v", err)
	}
	if !w.TierApplied || w.Model != cfg.Roles["implement"].Model {
		t.Errorf("strong tier = %+v, want the role's own model applied as an overlay", w)
	}
	if !strings.HasPrefix(w.Label(), "roles.implement.tiers.") {
		t.Errorf("Label() = %q, want the tier cell named", w.Label())
	}
}

// The operator constraint this repository's cloud routing exists to honour
// (tick 84z): everything OFF claude in the cloud, for every role — while a
// LOCAL run keeps the frontier review and close-out on opus, which a Max
// subscription pays for at the margin and a container can neither run nor
// afford. Both halves are asserted against the file that routes the very
// workers building ticfac.
func TestRepoRunnersConfigKeepsClaudeOffTheCloud(t *testing.T) {
	cfg, err := LoadRepoFor(repoRootForTest(t), SubstrateCloud)
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	for _, role := range []string{RoleImplement, "review", "closeout"} {
		for _, tier := range []Tier{"", TierEconomy, TierBalanced, TierStrong, TierFrontier} {
			w, err := cfg.ResolveOn(SubstrateCloud, role, tier)
			if err != nil {
				t.Errorf("ResolveOn(cloud, %q, %q): %v — a cloud run would refuse at start over this cell", role, tier, err)
				continue
			}
			if w.Kind == "claude" {
				t.Errorf("ResolveOn(cloud, %q, %q) routes the claude harness into a container", role, tier)
			}
		}
	}
}

// The local half of the same constraint: herdr and harness are where this
// file's base cells were written for, and the frontier review and close-out
// stay on opus there — taking opus out of the file altogether would be the
// one-line fix that loses exactly this.
func TestRepoRunnersConfigKeepsTheLocalFrontierReviewOnOpus(t *testing.T) {
	for _, sub := range []Substrate{SubstrateHerdr, SubstrateHarness} {
		cfg, err := LoadRepoFor(repoRootForTest(t), sub)
		if err != nil {
			t.Fatalf("LoadRepoFor(%s): %v", sub, err)
		}
		for _, role := range []string{"review", "closeout"} {
			w, err := cfg.ResolveOn(sub, role, "")
			if err != nil {
				t.Errorf("ResolveOn(%s, %q): %v", sub, role, err)
				continue
			}
			if w.Kind != "claude" || w.Model != "opus" {
				t.Errorf("ResolveOn(%s, %q) = %s/%s, want the frontier claude/opus", sub, role, w.Kind, w.Model)
			}
		}
	}
}

// The local claude ladder (tick 5uo), from the operator: "if a tick is
// complex you can consider using a claude executor locally" — and "the thing
// you must avoid is to invoke claude models from cf cloud executors". A local
// run's implement ladder climbs from GLM to claude; the cloud's never can,
// because the cloud never reads runners.local.toml.
func TestRepoRunnersConfigLadderClimbsToClaudeOnlyLocally(t *testing.T) {
	for _, sub := range []Substrate{SubstrateHerdr, SubstrateHarness} {
		cfg, err := LoadRepoFor(repoRootForTest(t), sub)
		if err != nil {
			t.Fatalf("LoadRepoFor(%s): %v", sub, err)
		}
		if cfg.TierPolicy == nil || cfg.TierPolicy.Default != TierStrong || cfg.TierPolicy.Ceiling != TierFrontier {
			t.Fatalf("%s: tier policy = %+v, want strong climbing to frontier", sub, cfg.TierPolicy)
		}
		first, err := cfg.ResolveOn(sub, RoleImplement, TierStrong)
		if err != nil {
			t.Fatal(err)
		}
		if first.Kind != "pi" {
			t.Errorf("%s: a first attempt resolves %s, want pi on GLM", sub, first.Kind)
		}
		top, err := cfg.ResolveOn(sub, RoleImplement, TierFrontier)
		if err != nil {
			t.Fatal(err)
		}
		if top.Kind != "claude" {
			t.Errorf("%s: the top of the ladder resolves %s, want claude", sub, top.Kind)
		}
	}
	cloud, err := LoadRepoFor(repoRootForTest(t), SubstrateCloud)
	if err != nil {
		t.Fatal(err)
	}
	if cloud.TierPolicy != nil && cloud.TierPolicy.Ceiling == TierFrontier {
		t.Errorf("a cloud run sees a ladder to frontier: %+v", cloud.TierPolicy)
	}
}

// classifiedSubstrates are the worlds whose committed [tier_policy] a
// recorded Jev classification could reach. Every one of them now declares
// the dear-mass rule OFF (operator decision 2026-10-04, tick r3y's
// evaluation, docs/classifier-eval-2026-10-04-jev-clef.md): classifications
// are recorded, but none moves a start.
var classifiedSubstrates = []Substrate{SubstrateHerdr, SubstrateHarness, SubstrateCloud}

// THE CLOUD'S LADDER (tick 7l1, the operator's option B): work starts on
// GLM 5.3 Flash, a failed attempt climbs straight to GLM 5.3 and stops
// there. Every rung is a
// Workers AI model through pi: the cloud rule holds on the ladder as well as
// on the base cells.
func TestRepoRunnersConfigCloudLadderIsFlashToGLM(t *testing.T) {
	cfg, err := LoadRepoFor(repoRootForTest(t), SubstrateCloud)
	if err != nil {
		t.Fatalf("LoadRepoFor(cloud): %v", err)
	}
	p := cfg.TierPolicy
	if p == nil || p.Default != TierEconomy || p.CeilingOrDefault() != TierStrong {
		t.Fatalf("cloud tier policy = %+v, want economy start and strong ceiling", p)
	}
	const workersAI = "cloudflare-workers-ai/@cf/"
	want := map[Tier]string{
		TierEconomy: workersAI + "zai-org/glm-5.3-flash",
		TierStrong:  workersAI + "zai-org/glm-5.3",
	}
	for tier, model := range want {
		w, err := cfg.ResolveOn(SubstrateCloud, RoleImplement, tier)
		if err != nil {
			t.Fatalf("ResolveOn(cloud, implement, %q): %v", tier, err)
		}
		if w.Kind != "pi" || w.Model != model {
			t.Errorf("cloud %q rung = %s/%s, want pi/%s", tier, w.Kind, w.Model, model)
		}
	}
	// One failed attempt on Flash climbs to GLM 5.3, never to an
	// intermediate rung the ladder does not declare, and never past it.
	work := TickFacts{TickID: "w", Role: "implement-tick", Type: "task", Priority: 2, Wave: 1}
	for _, tc := range []struct {
		attempt DeriveAttempt
		want    Tier
	}{
		{DeriveAttempt{Number: 1}, TierEconomy},
		{DeriveAttempt{Number: 2, Failed: 1}, TierStrong},
		{DeriveAttempt{Number: 3, Failed: 2}, TierStrong},
	} {
		got, err := p.Derive(work, tc.attempt)
		if err != nil {
			t.Fatal(err)
		}
		if got.Tier != tc.want {
			t.Errorf("cloud attempt %d (%d failed) derives %q (%s), want %q", tc.attempt.Number, tc.attempt.Failed, got.Tier, got.Reason, tc.want)
		}
	}
}

// massAt spells a distribution over the closed enum for the repo-policy tests.
func massAt(mechanical, translation, construction, diagnosis, design float64) *DeriveClassification {
	return &DeriveClassification{Probabilities: map[WorkType]float64{
		WorkMechanical: mechanical, WorkTranslation: translation, WorkConstruction: construction,
		WorkDiagnosis: diagnosis, WorkDesign: design,
	}}
}

// THE REPOSITORY'S OWN ROUTING: the dear-mass rule tick ms9 declared is
// switched off (operator decision 2026-10-04) — r3y measured dear mass
// against outcomes at AUC 0.37-0.42 for every classifier tried, so no
// recorded classification may move a start. However much mass a
// classification puts on design + diagnosis, an implementation tick's first
// attempt starts at the policy default and only a failure climbs.
func TestRepoRunnersConfigStartsNoTickOnTheClassifier(t *testing.T) {
	for _, sub := range classifiedSubstrates {
		t.Run(string(sub), func(t *testing.T) {
			cfg, err := LoadRepoFor(repoRootForTest(t), sub)
			if err != nil {
				t.Fatalf("LoadRepoFor(%s): %v", sub, err)
			}
			p := cfg.TierPolicy
			if p == nil {
				t.Fatalf("%s declares no [tier_policy]", sub)
			}
			if len(p.DearWorkTypes) != 0 || p.DearTier != "" || p.MassThresholdDeclared() {
				t.Fatalf("%s re-declares the dear-mass rule (types %v, tier %q, threshold %v): it was switched off on 2026-10-04 for having no predictive signal",
					sub, p.DearWorkTypes, p.DearTier, p.MassThreshold)
			}
			work := TickFacts{TickID: "w", Role: "implement-tick", Type: "task", Priority: 2, Wave: 1}
			first := DeriveAttempt{Number: 1}
			plain, err := p.Derive(work, first)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range []*DeriveClassification{massAt(0, 0, 0, 0.5, 0.5), massAt(0, 0, 1, 0, 0), nil} {
				got, err := p.DeriveClassified(work, first, c)
				if err != nil {
					t.Fatal(err)
				}
				if got.Tier != p.Default || got.Tier != plain.Tier {
					t.Errorf("%s: a classification started the tick at %q (%s), want the default %q", sub, got.Tier, got.Reason, p.Default)
				}
			}
		})
	}
}
