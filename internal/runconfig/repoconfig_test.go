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

// jevRoutedSubstrates are the worlds whose committed [tier_policy] routes a
// recorded Jev classification (tick ms9). A world missing from this list is
// one where the classification a run pays for changes nothing. The cloud is
// that world today (2026-09-30: every tick classified, every dispatch still
// `runs at tier ""`): its dear rung needs a Workers AI model stronger than
// GLM 5.3, which is the operator's choice to make, and it joins this list
// in the change that declares its [tier_policy].
var jevRoutedSubstrates = []Substrate{SubstrateHerdr, SubstrateHarness}

// massAt spells a distribution over the closed enum for the repo-policy tests.
func massAt(mechanical, translation, construction, diagnosis, design float64) *DeriveClassification {
	return &DeriveClassification{Probabilities: map[WorkType]float64{
		WorkMechanical: mechanical, WorkTranslation: translation, WorkConstruction: construction,
		WorkDiagnosis: diagnosis, WorkDesign: design,
	}}
}

// THE REPOSITORY'S OWN ROUTING (tick ms9): a recorded classification moves
// an implementation tick's first attempt to the dear tier on the PROBABILITY
// MASS on design + diagnosis — not the argmax — against the threshold this
// repository measured and declared, and the dear tier resolves to a different
// worker than the start tier, so the classification changes what runs. Below
// the threshold, with no classification at all (Jev unavailable, a recorded
// no-answer), and for every role-carrying job, the start is exactly what it
// was without a classifier.
func TestRepoRunnersConfigRoutesTheFirstAttemptOnJevMass(t *testing.T) {
	for _, sub := range jevRoutedSubstrates {
		t.Run(string(sub), func(t *testing.T) {
			cfg, err := LoadRepoFor(repoRootForTest(t), sub)
			if err != nil {
				t.Fatalf("LoadRepoFor(%s): %v", sub, err)
			}
			p := cfg.TierPolicy
			if p == nil {
				t.Fatalf("%s declares no [tier_policy]: a Jev classification changes nothing there, and every dispatch runs at the role's base values", sub)
			}
			if !p.MassThresholdDeclared() {
				t.Errorf("%s leans on the provisional default threshold; ms9 measured one and the file must declare it", sub)
			}
			dear := map[WorkType]bool{}
			for _, one := range p.DearWorkTypes {
				dear[one] = true
			}
			if len(dear) != 2 || !dear[WorkDesign] || !dear[WorkDiagnosis] {
				t.Fatalf("%s: dear_work_types = %v, want design and diagnosis", sub, p.DearWorkTypes)
			}
			threshold := p.MassThresholdOrDefault()
			work := TickFacts{TickID: "w", Role: "implement-tick", Type: "task", Priority: 2, Wave: 1}
			first := DeriveAttempt{Number: 1}

			// The mass rule: construction is the argmax, but design and
			// diagnosis together clear the threshold — the tick routes dear.
			half := threshold/2 + 0.01
			got, err := p.DeriveClassified(work, first, massAt(0, 0, 1-2*half, half, half))
			if err != nil {
				t.Fatal(err)
			}
			if got.Tier != p.DearTier {
				t.Errorf("%s: %.2f of mass on design+diagnosis started at %q (%s), want the dear tier %q",
					sub, 2*half, got.Tier, got.Reason, p.DearTier)
			}
			startWorker, err := cfg.ResolveOn(sub, RoleImplement, p.Default)
			if err != nil {
				t.Fatal(err)
			}
			dearWorker, err := cfg.ResolveOn(sub, RoleImplement, p.DearTier)
			if err != nil {
				t.Fatalf("%s: the dear tier %q does not resolve: %v", sub, p.DearTier, err)
			}
			if startWorker.Kind == dearWorker.Kind && startWorker.Model == dearWorker.Model {
				t.Errorf("%s: the dear tier %q resolves the same worker as the start tier %q (%s/%s): the classification would change nothing that runs",
					sub, p.DearTier, p.Default, dearWorker.Kind, dearWorker.Model)
			}

			// Under the threshold — even with design as the argmax — the
			// start is the policy's own.
			under := threshold - 0.01
			if got, err = p.DeriveClassified(work, first, massAt(0, 0, 1-under, 0, under)); err != nil {
				t.Fatal(err)
			}
			if got.Tier != p.Default {
				t.Errorf("%s: %.2f of dear mass started at %q (%s), want the start tier %q", sub, under, got.Tier, got.Reason, p.Default)
			}

			// Jev unavailable — no record, or a recorded no-answer that
			// carries no distribution: the start tier, exactly as without a
			// classifier.
			for _, absent := range []*DeriveClassification{nil, {}} {
				if got, err = p.DeriveClassified(work, first, absent); err != nil {
					t.Fatal(err)
				}
				if got.Tier != p.Default {
					t.Errorf("%s: an absent classification started at %q (%s), want the start tier %q", sub, got.Tier, got.Reason, p.Default)
				}
			}

			// Role jobs are never routed by a classification, however much
			// dear mass one would carry.
			for _, role := range []string{"review-epic", "closeout-epic", "plan-epic"} {
				plain, err := p.Derive(TickFacts{TickID: "r", Role: role}, first)
				if err != nil {
					t.Fatal(err)
				}
				routed, err := p.DeriveClassified(TickFacts{TickID: "r", Role: role}, first, massAt(0, 0, 0, 0.5, 0.5))
				if err != nil {
					t.Fatal(err)
				}
				if routed.Tier != plain.Tier {
					t.Errorf("%s: role %s moved from %q to %q on a classification (%s)", sub, role, plain.Tier, routed.Tier, routed.Reason)
				}
			}

			// A failed dear start still has somewhere to go or is at the
			// ceiling, where the next actor is a person: the ladder bounds it.
			if tierIndex(p.DearTier) > tierIndex(p.CeilingOrDefault()) {
				t.Errorf("%s: the dear tier %q sits above the ceiling %q", sub, p.DearTier, p.CeilingOrDefault())
			}
		})
	}
}
