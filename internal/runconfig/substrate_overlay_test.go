package runconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// The per-substrate overlay (tick 84z): `[roles.<role>.substrates.<substrate>]`,
// the same four-field variant shape the tier overlay uses, applied between
// the role's own values and any tier overlay.
//
// The reason this exists is economics, not principle: a local claude run
// bills against a subscription and is free at the margin, a cloud run bills
// per token against credentials the factory holds — so the SAME file must
// route review and closeout to opus locally and to a container-runnable
// worker in the cloud, and the substrate is the axis they differ on.
//
// The failure this must not have is a cloud run silently falling back to a
// role's base values because nobody declared a cloud overlay: that fallback
// is how a cloud run reaches a claude process nobody chose. Absent cloud
// routing is a REFUSAL naming the role, never a default.

const overlayDocument = `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.substrates.cloud]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[roles.review]
kind = "claude"
model = "opus"
effort = "high"

[roles.review.substrates.cloud]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.closeout]
kind = "claude"
model = "opus"
effort = "high"
`

func parseOverlayConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Parse([]byte(overlayDocument))
	if err != nil {
		t.Fatalf("parse the overlay document: %v", err)
	}
	return cfg
}

// A declared cloud overlay is what a cloud run resolves against, field by
// field — the shape is the tier overlay's, so a field the overlay does not
// set inherits the role's own, exactly as a tier variant does.
func TestResolveOnAppliesTheSubstrateOverlay(t *testing.T) {
	t.Parallel()
	cfg := parseOverlayConfig(t)

	w, err := cfg.ResolveOn(SubstrateCloud, "review", "")
	if err != nil {
		t.Fatalf("ResolveOn(cloud, review): %v", err)
	}
	if w.Kind != "pi" || w.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3" {
		t.Errorf("cloud review resolved %s/%s, want pi on glm-5.3", w.Kind, w.Model)
	}
	// Effort is inherited from the role's own cell: the overlay overlays, it
	// does not erase.
	if w.Effort != EffortHigh {
		t.Errorf("cloud review resolved effort %q, want the role's own %q", w.Effort, EffortHigh)
	}
	if !w.SubstrateApplied || w.Substrate != SubstrateCloud {
		t.Errorf("the substrate overlay's application went unrecorded: %+v", w)
	}
	if w.Label() != "roles.review.substrates.cloud" {
		t.Errorf("Label() = %q, want the cloud cell named", w.Label())
	}

	// The local substrates keep the role's own values: herdr and harness are
	// where the base table was written for, and an overlay declared for cloud
	// changes nothing for them.
	for _, sub := range []Substrate{SubstrateHerdr, SubstrateHarness} {
		w, err := cfg.ResolveOn(sub, "review", "")
		if err != nil {
			t.Fatalf("ResolveOn(%s, review): %v", sub, err)
		}
		if w.Kind != "claude" || w.Model != "opus" {
			t.Errorf("%s review resolved %s/%s, want the role's own claude/opus", sub, w.Kind, w.Model)
		}
		if w.SubstrateApplied {
			t.Errorf("%s applied an overlay declared for another substrate", sub)
		}
	}
}

// The tier overlay applies on top of the substrate overlay — most specific
// last — so a per-tier model still narrows a cloud role. The label names the
// whole cell the values came from.
func TestResolveOnAppliesTheTierOverlayOverTheSubstrateOverlay(t *testing.T) {
	t.Parallel()
	cfg := parseOverlayConfig(t)

	w, err := cfg.ResolveOn(SubstrateCloud, RoleImplement, TierEconomy)
	if err != nil {
		t.Fatalf("ResolveOn(cloud, implement, economy): %v", err)
	}
	if w.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash" {
		t.Errorf("the economy tier resolved model %q over the cloud overlay", w.Model)
	}
	if !w.SubstrateApplied || !w.TierApplied {
		t.Errorf("overlay applications went unrecorded: %+v", w)
	}
	if want := "roles.implement.substrates.cloud.tiers.economy"; w.Label() != want {
		t.Errorf("Label() = %q, want %q", w.Label(), want)
	}
}

// Absent cloud routing is a refusal naming the role, never a fall back to the
// role's own values. hn0 reached a claude process in a container exactly by
// inheriting a default nobody chose; this is the refusal that closes it.
func TestResolveOnRefusesARoleWithNoCloudRouting(t *testing.T) {
	t.Parallel()
	cfg := parseOverlayConfig(t)

	_, err := cfg.ResolveOn(SubstrateCloud, "closeout", "")
	if err == nil {
		t.Fatal("a role with no cloud overlay resolved anyway, falling back to its own values")
	}
	if !errors.Is(err, ErrNoCloudRouting) {
		t.Errorf("the refusal is not recognisable as ErrNoCloudRouting: %v", err)
	}
	for _, want := range []string{"closeout", "roles.closeout", "substrates.cloud"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A role with no entry of its own resolves against implement — so the cloud
// routing that has to be declared is implement's, and the refusal names the
// cell that is missing beside the role that was asked for.
func TestResolveOnRefusesAFallbackRoleWithoutCloudRouting(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(`version = 2

[roles.implement]
kind = "pi"
model = "glm"

[roles.review]
kind = "claude"
model = "opus"

[roles.review.substrates.cloud]
kind = "pi"
model = "glm"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, err = cfg.ResolveOn(SubstrateCloud, "plan", "")
	if err == nil {
		t.Fatal("an unlisted role resolved on the cloud substrate without any cloud routing")
	}
	if !errors.Is(err, ErrNoCloudRouting) {
		t.Errorf("the refusal is not recognisable as ErrNoCloudRouting: %v", err)
	}
	// The cell the fix belongs in, and the role the caller asked for: both,
	// because neither alone tells an operator what to edit.
	if !strings.Contains(err.Error(), "roles.implement.substrates.cloud") {
		t.Errorf("the refusal does not name the missing cell: %v", err)
	}
	if !strings.Contains(err.Error(), `"plan"`) {
		t.Errorf("the refusal does not name the role that was asked for: %v", err)
	}
}

// The empty substrate is the substrate-blind view: exactly what [Resolve] has
// always answered, so a caller that knows no substrate cannot be made wrong
// by one.
func TestResolveOnWithNoSubstrateIsTheSubstrateBlindView(t *testing.T) {
	t.Parallel()
	cfg := parseOverlayConfig(t)

	for _, role := range []string{RoleImplement, "review", "closeout"} {
		for _, tier := range []Tier{"", TierEconomy} {
			blind, err := cfg.ResolveOn("", role, tier)
			if err != nil {
				t.Fatalf("ResolveOn(\"\", %q, %q): %v", role, tier, err)
			}
			direct, err := cfg.Resolve(role, tier)
			if err != nil {
				t.Fatalf("Resolve(%q, %q): %v", role, tier, err)
			}
			if !reflect.DeepEqual(blind, direct) {
				t.Errorf("ResolveOn(\"\", %q, %q) = %+v, want Resolve's %+v", role, tier, blind, direct)
			}
		}
	}
}

// auto is a policy that a decision procedure resolves, not a substrate a
// role can be resolved against; a caller that passes it has skipped the
// decision and must be told so rather than given an answer chosen by
// accident.
func TestResolveOnRefusesAutoAsASubstrate(t *testing.T) {
	t.Parallel()
	cfg := parseOverlayConfig(t)

	_, err := cfg.ResolveOn(SubstrateAuto, "review", "")
	if err == nil {
		t.Fatal("auto was accepted as a substrate to resolve roles against")
	}
	if !strings.Contains(err.Error(), "auto") {
		t.Errorf("the refusal does not name the value it refuses: %v", err)
	}
}

// An unknown substrate is refused with the vocabulary, the same refusal
// [ParseOverride] gives an environment override.
func TestResolveOnRefusesAnUnknownSubstrate(t *testing.T) {
	t.Parallel()
	cfg := parseOverlayConfig(t)

	_, err := cfg.ResolveOn(Substrate("edge"), "review", "")
	if err == nil || !strings.Contains(err.Error(), SubstrateList(true)) {
		t.Errorf("an unknown substrate was not refused with the vocabulary: %v", err)
	}
}

// A nil config resolves nothing — and on the cloud substrate it is the
// cloud refusal rather than the generic one: the only routing that exists
// without a config is whatever ships as written, and nothing here can say
// what that would be safe to run in a container.
func TestResolveOnCloudWithNoConfigIsNotARouting(t *testing.T) {
	t.Parallel()
	var cfg *Config
	_, err := cfg.ResolveOn(SubstrateCloud, "review", "")
	if !errors.Is(err, ErrNoCloudRouting) {
		t.Errorf("a nil config on the cloud substrate answered something other than the cloud refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "review") {
		t.Errorf("the refusal does not name the role: %v", err)
	}
	// Every local substrate keeps the historical answer: no config, no
	// routing, nothing to resolve against.
	if _, err := cfg.ResolveOn(SubstrateHerdr, "review", ""); !errors.Is(err, ErrNoConfig) {
		t.Errorf("a nil config on a local substrate answered something other than no-config: %v", err)
	}
}

// A substrate overlay replaces args wholesale, the same rule a tier variant
// follows: merging two argv lists whose flags may conflict is not
// well-defined, so presence replaces.
func TestASubstrateOverlayReplacesArgs(t *testing.T) {
	t.Parallel()
	cfg, err := Parse([]byte(`version = 2

[roles.implement]
kind = "pi"
model = "glm"
args = ["--approve"]

[roles.implement.substrates.cloud]
args = []
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	w, err := cfg.ResolveOn(SubstrateCloud, RoleImplement, "")
	if err != nil {
		t.Fatalf("ResolveOn(cloud, implement): %v", err)
	}
	if len(w.Args) != 0 {
		t.Errorf("an overlay declaring args = [] left %v in place", w.Args)
	}
	local, err := cfg.ResolveOn(SubstrateHarness, RoleImplement, "")
	if err != nil {
		t.Fatalf("ResolveOn(harness, implement): %v", err)
	}
	if len(local.Args) != 1 || local.Args[0] != "--approve" {
		t.Errorf("the local substrate lost the role's own args: %v", local.Args)
	}
}

// ------------------------------------------------------- load validation ---

// The overlay's keys are substrates — the same vocabulary [orchestration]
// reads — and auto is not one a role can be routed for: it is a policy that
// resolves, never the answer.
func TestALoadedConfigRefusesAnOverlayKeyThatIsNotASubstrate(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{
		"edge": `"edge" is not one of`,
		"auto": `auto`,
	} {
		_, err := Parse([]byte("version = 2\n\n[roles.implement]\nkind = \"pi\"\n\n" +
			"[roles.implement.substrates." + key + "]\nmodel = \"glm\"\n"))
		if err == nil {
			t.Errorf("substrates.%s was accepted as an overlay key", key)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal for substrates.%s does not say %q: %v", key, want, err)
		}
	}
}

// An empty substrate overlay is refused for the same reason an empty tier
// table is: a cell that changes nothing is a cell nobody can tell whether it
// was meant to.
func TestALoadedConfigRefusesAnEmptySubstrateOverlay(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("version = 2\n\n[roles.implement]\nkind = \"pi\"\n\n" +
		"[roles.implement.substrates.cloud]\n"))
	if err == nil {
		t.Fatal("an empty substrate overlay was accepted")
	}
	if !strings.Contains(err.Error(), "at least one") {
		t.Errorf("the refusal does not say what an overlay must set: %v", err)
	}
}
