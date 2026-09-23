package runconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The per-world override files (tick 5uo). The property that matters most is
// ea1a62d3's: a tier the COMMON file declares — here a claude frontier tier —
// must not reach a cloud run whose runners.cloud.toml routes the role to pi,
// because the override applies last.

const layeredCommon = `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[roles.implement.tiers.frontier]
kind = "claude"
model = "opus"

[roles.review]
kind = "claude"
model = "opus"
effort = "high"

[testing.commands]
go = { command = "go test ./..." }
`

const layeredCloud = `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
effort = "medium"
`

const layeredLocal = `version = 2

[roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[tier_policy]
default = "strong"
ceiling = "frontier"
`

func writeLayered(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".tick")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "runners.toml")
}

func TestACommonFileTierCannotReachTheCloud(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{"runners.toml": layeredCommon, "runners.cloud.toml": layeredCloud})
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("LoadFor(cloud): %v", err)
	}
	w, err := cfg.ResolveOn(SubstrateCloud, "implement", TierFrontier)
	if err != nil {
		t.Fatalf("ResolveOn(cloud, implement, frontier): %v", err)
	}
	if w.Kind != "pi" || w.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3" {
		t.Errorf("a cloud run at frontier resolved %s/%s; the cloud file's pi cell must apply over the common file's claude tier", w.Kind, w.Model)
	}
	if !strings.Contains(w.Label(), ".tick/runners.cloud.toml roles.implement") {
		t.Errorf("Label() = %q, want the cloud file's cell named", w.Label())
	}
}

func TestTheOverrideFilesOwnTierStillAppliesAfterItsRoleCell(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{"runners.toml": layeredCommon, "runners.cloud.toml": layeredCloud})
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("LoadFor(cloud): %v", err)
	}
	w, err := cfg.ResolveOn(SubstrateCloud, "implement", TierEconomy)
	if err != nil {
		t.Fatal(err)
	}
	if w.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash" || w.Effort != EffortMedium {
		t.Errorf("cloud economy resolved %s/%s, want the cloud file's own economy cell (flash, medium)", w.Model, w.Effort)
	}
	// Effort the cloud file's role cell does not set is inherited from the
	// common role: the override overlays, it does not erase.
	w, err = cfg.ResolveOn(SubstrateCloud, "implement", "")
	if err != nil {
		t.Fatal(err)
	}
	if w.Effort != EffortHigh {
		t.Errorf("cloud implement resolved effort %q, want the common role's %q", w.Effort, EffortHigh)
	}
}

func TestACloudRoleTheCloudFileOmitsIsRefused(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{"runners.toml": layeredCommon, "runners.cloud.toml": layeredCloud})
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.ResolveOn(SubstrateCloud, "review", "")
	if !errors.Is(err, ErrNoCloudRouting) {
		t.Fatalf("ResolveOn(cloud, review) = %v, want ErrNoCloudRouting", err)
	}
	if !strings.Contains(err.Error(), "runners.cloud.toml") {
		t.Errorf("the refusal must name the file to edit: %v", err)
	}
}

func TestNoCloudFileMeansNoCloudRouting(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{"runners.toml": layeredCommon})
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.ResolveOn(SubstrateCloud, "implement", ""); !errors.Is(err, ErrNoCloudRouting) {
		t.Fatalf("ResolveOn(cloud) with no runners.cloud.toml = %v, want ErrNoCloudRouting", err)
	}
}

func TestTheLocalFileIsInvisibleToTheCloudAndMergedLocally(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{
		"runners.toml": layeredCommon, "runners.cloud.toml": layeredCloud, "runners.local.toml": layeredLocal,
	})
	local, err := LoadFor(path, SubstrateHerdr)
	if err != nil {
		t.Fatalf("LoadFor(herdr): %v", err)
	}
	if local.TierPolicy == nil || local.TierPolicy.Ceiling != TierFrontier {
		t.Errorf("a local run must see runners.local.toml's ladder, got %+v", local.TierPolicy)
	}
	if local.OverrideFile == "" || !strings.HasSuffix(local.OverrideFile, "runners.local.toml") {
		t.Errorf("OverrideFile = %q, want the local file recorded", local.OverrideFile)
	}
	w, err := local.ResolveOn(SubstrateHarness, "implement", TierFrontier)
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "claude" {
		t.Errorf("a local run at frontier resolved %s, want claude", w.Kind)
	}

	cloud, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatal(err)
	}
	if cloud.TierPolicy != nil {
		t.Errorf("a cloud run must not see runners.local.toml's tier policy, got %+v", cloud.TierPolicy)
	}
}

func TestNoOverrideFileReadsExactlyTheCommonFile(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{"runners.toml": layeredCommon})
	for _, sub := range []Substrate{SubstrateHerdr, SubstrateHarness, ""} {
		cfg, err := LoadFor(path, sub)
		if err != nil {
			t.Fatalf("LoadFor(%q): %v", sub, err)
		}
		if cfg.OverrideFile != "" {
			t.Errorf("LoadFor(%q) recorded an override %q with none on disk", sub, cfg.OverrideFile)
		}
		w, err := cfg.ResolveOn(sub, "implement", TierFrontier)
		if err != nil {
			t.Fatal(err)
		}
		if w.Kind != "claude" {
			t.Errorf("LoadFor(%q) frontier resolved %s, want the common file's claude tier", sub, w.Kind)
		}
	}
}

func TestArraysReplaceAndTablesMerge(t *testing.T) {
	t.Parallel()
	common := layeredCommon + `
[tier_policy]
default = "strong"

[[tier_policy.start]]
tier = "economy"
types = ["chore"]

[[tier_policy.start]]
tier = "strong"
types = ["bug"]
`
	override := `version = 2

[tier_policy]
ceiling = "strong"

[[tier_policy.start]]
tier = "economy"
types = ["docs"]
`
	path := writeLayered(t, map[string]string{"runners.toml": common, "runners.local.toml": override})
	cfg, err := LoadFor(path, SubstrateHerdr)
	if err != nil {
		t.Fatalf("LoadFor: %v", err)
	}
	p := cfg.TierPolicy
	if p.Default != TierStrong || p.Ceiling != TierStrong {
		t.Errorf("tables must merge field-wise: default %q ceiling %q, want strong/strong", p.Default, p.Ceiling)
	}
	if len(p.Start) != 1 || p.Start[0].Types[0] != "docs" {
		t.Errorf("arrays must be replaced wholesale, got %d start rules", len(p.Start))
	}
}

func TestTheInlineSubstrateFormIsRefusedWithTheFileItMovedTo(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(layeredCommon + `
[roles.implement.substrates.cloud]
kind = "pi"
`))
	if err == nil || !strings.Contains(err.Error(), "runners.cloud.toml") {
		t.Fatalf("Parse(inline substrates) = %v, want a refusal naming runners.cloud.toml", err)
	}
}

func TestAnOverrideErrorNamesTheOverride(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{"runners.toml": layeredCommon, "runners.cloud.toml": "version = 2\n[roles.implement]\nkind = \"pi\"\nmodle = \"x\"\n"})
	_, err := LoadFor(path, SubstrateCloud)
	if err == nil || !strings.Contains(err.Error(), "runners.cloud.toml") || !strings.Contains(err.Error(), "modle") {
		t.Fatalf("LoadFor with a typo in the cloud file = %v, want an error naming the file and the key", err)
	}
}

func TestACloudCellMustNameItsKind(t *testing.T) {
	t.Parallel()
	path := writeLayered(t, map[string]string{"runners.toml": layeredCommon, "runners.cloud.toml": "version = 2\n[roles.review]\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\n"})
	_, err := LoadFor(path, SubstrateCloud)
	if err == nil || !strings.Contains(err.Error(), "must name its kind") {
		t.Fatalf("LoadFor with a kindless cloud cell = %v, want a refusal", err)
	}
}
