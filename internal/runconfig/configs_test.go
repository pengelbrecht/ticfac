package runconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Named run configs (tick tda): a repository may declare more than one
// complete routing in its runners files and let each run choose between
// them. The properties this file holds:
//
//   - the shape: [configs] default, [configs.<name>] roles and tier_policy,
//     and NOTHING else — a config is a routing, not a second repository;
//   - the selection semantics: a named config's cells apply as the LAST
//     overlay, over the common file's cells, the substrate override's and
//     every tier (the shape of finding ea1a62d3: a cell that applied before
//     an overlay that applied after it), and its tier policy replaces the
//     merged policy;
//   - the fail-closed rules: a default that names nothing, a config that
//     declares nothing, a selection of a name nobody declared, a cloud role
//     a config cannot route because the merged document never did.

const configsCommon = `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[testing.commands]
go = { command = "go test ./..." }
`

const configsCloud = `version = 2

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[configs.glm.tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"

[configs.claude.roles.implement.tiers.economy]
model = "sonnet"

[configs.claude.roles.implement.tiers.strong]
model = "opus"

[configs.claude.roles.review]
kind = "claude"
model = "opus"

[configs.claude.roles.review.tiers.strong]
kind = "claude"
model = "opus"

[configs.claude.tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[configs.claude.tier_policy.concurrency]
economy = 2
strong = 1

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
effort = "medium"

[roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review.tiers.strong]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[tier_policy]
default = "economy"
ceiling = "strong"
step = 2
`

// writeConfigs writes the two files a cloud repo carries and returns the
// path of the common one, the shape [LoadFor] reads.
func writeConfigs(t *testing.T, common, cloud string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tick", "runners.toml"), []byte(common), 0o644); err != nil {
		t.Fatal(err)
	}
	if cloud != "" {
		if err := os.WriteFile(filepath.Join(dir, ".tick", "runners.cloud.toml"), []byte(cloud), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, ".tick", "runners.toml")
}

// TestNamedConfigsParse declares the names, the default and each config's
// cells — the whole [configs] table, as one load answers it.
func TestNamedConfigsParse(t *testing.T) {
	path := writeConfigs(t, configsCommon, configsCloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load the cloud-merged config: %v", err)
	}
	if names := cfg.NamedConfigNames(); strings.Join(names, ",") != "claude,glm" {
		t.Errorf("the declared configs are %v, want claude,glm", names)
	}
	if cfg.DefaultConfigName() != "glm" {
		t.Errorf("default is %q, want glm", cfg.DefaultConfigName())
	}
	nc, ok := cfg.NamedConfig("claude")
	if !ok || nc == nil {
		t.Fatalf("the claude config is not declared")
	}
	if len(nc.Roles) != 2 {
		t.Errorf("the claude config declares %d roles, want 2 (implement, review)", len(nc.Roles))
	}
	if nc.TierPolicy == nil || nc.TierPolicy.Default != TierEconomy || nc.TierPolicy.Ceiling != TierStrong {
		t.Errorf("the claude config's tier policy is %+v, want default economy ceiling strong", nc.TierPolicy)
	}
	if nc.TierPolicy.Concurrency["economy"] != 2 || nc.TierPolicy.Concurrency["strong"] != 1 {
		t.Errorf("the claude config's concurrency caps are %+v, want economy 2 strong 1", nc.TierPolicy.Concurrency)
	}
}

// TestAConfigIsARoutingAndNothingElse holds the boundary that keeps a named
// config honest: roles and a tier policy, and no other key — because the
// gate, the evidence table, the substrate and the findings routes are the
// repository's rules, the same for every run, and a config that could vary
// them would be two repositories wearing one checkout.
func TestAConfigIsARoutingAndNothingElse(t *testing.T) {
	doc := `version = 2

[roles.implement]
kind = "pi"

[testing.commands]
go = { command = "go test ./..." }

[configs]
default = "glm"

[configs.glm.roles.implement]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[configs.glm.orchestration]
max_parallel = 1
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatalf("a config declaring [orchestration] parsed; it is a routing, not a second repository")
	}
	if !strings.Contains(err.Error(), "configs.glm.orchestration") {
		t.Errorf("the refusal does not name the config's unknown key: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("the refusal does not say what the problem is: %v", err)
	}
}

// TestAConfigThatDeclaresNothingIsRefused: an empty [configs.<name>] is a
// typo'd config, and reading it as "the file's own cells, named" would
// silently run an operator's claude epic on GLM — the exact "paid for X and
// got Y" failure this package refuses everywhere else.
func TestAConfigThatDeclaresNothingIsRefused(t *testing.T) {
	doc := `version = 2

[roles.implement]
kind = "pi"

[testing.commands]
go = { command = "go test ./..." }

[configs]
default = "glm"

[configs.glm]
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatalf("an empty named config parsed; it would silently run the file's own cells")
	}
	if !strings.Contains(err.Error(), "declares no [roles] and no [tier_policy]") {
		t.Errorf("the refusal does not say what an empty config is: %v", err)
	}
}

// TestTheDefaultNamesADeclaredConfig holds the whole-document rules: with
// named configs declared, a default exists and names one — "nothing selects"
// is a choice too, and a silent fall back to the file's own cells would be a
// third routing nobody chose.
func TestTheDefaultNamesADeclaredConfig(t *testing.T) {
	cases := []struct {
		name, want string
		doc        string
	}{
		{"absent", "required when [configs] declares named configs", `version = 2

[roles.implement]
kind = "pi"

[testing.commands]
go = { command = "go test ./..." }

[configs.glm.roles.implement]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
`},
		{"unknown", "names \"claude\", which [configs] does not declare", `version = 2

[roles.implement]
kind = "pi"

[testing.commands]
go = { command = "go test ./..." }

[configs]
default = "claude"

[configs.glm.roles.implement]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
`},
		{"nothing declared", "declares none", `version = 2

[roles.implement]
kind = "pi"

[testing.commands]
go = { command = "go test ./..." }

[configs]
default = "glm"
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatalf("the config parsed; want the refusal %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "configs.default") {
				t.Errorf("the refusal does not point at configs.default: %v", err)
			}
		})
	}
}

// TestSelectAppliesTheNamedConfigOverEveryOverlay is ea1a62d3's property for
// configs: a config's cells apply LAST — over the common file's, over the
// substrate override's, over every tier — because the config is the thing a
// run chose.
func TestSelectAppliesTheNamedConfigOverEveryOverlay(t *testing.T) {
	path := writeConfigs(t, configsCommon, configsCloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load the cloud-merged config: %v", err)
	}
	selected, err := cfg.Select("claude")
	if err != nil {
		t.Fatalf("select claude: %v", err)
	}
	w, err := selected.ResolveOn(SubstrateCloud, "implement", TierEconomy)
	if err != nil {
		t.Fatalf("resolve implement at economy under the claude config: %v", err)
	}
	if w.Kind != "claude" || w.Model != "sonnet" {
		t.Errorf("implement at economy resolves to %s/%s, want claude/sonnet", w.Kind, w.Model)
	}
	if w.Config != "claude" {
		t.Errorf("the resolved worker does not name its config: %q", w.Config)
	}
	// A config tier over the substrate override's own tier cell: the cloud
	// file's economy cell names GLM Flash; the claude config's economy cell
	// names sonnet, and the config was chosen.
	w, err = selected.ResolveOn(SubstrateCloud, "implement", TierStrong)
	if err != nil {
		t.Fatalf("resolve implement at strong under the claude config: %v", err)
	}
	if w.Model != "opus" {
		t.Errorf("implement at strong resolves to %s, want opus — the config's strong tier over the overlay's cells", w.Model)
	}
	// The label points at the config's cell, the table a reader must edit.
	if !strings.Contains(w.Label(), "configs.claude.roles.implement") {
		t.Errorf("the label %q does not name the config's cell", w.Label())
	}
}

// TestSelectLeavesUndeclaredCellsTheMergedValues: a config is complete in
// what it declares and silent over what it does not — closeout here — and
// the merged document's cells stand for the rest.
func TestSelectLeavesUndeclaredCellsTheMergedValues(t *testing.T) {
	path := writeConfigs(t, configsCommon, configsCloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load the cloud-merged config: %v", err)
	}
	selected, err := cfg.Select("claude")
	if err != nil {
		t.Fatalf("select claude: %v", err)
	}
	w, err := selected.ResolveOn(SubstrateCloud, "closeout", "")
	if err != nil {
		t.Fatalf("resolve closeout under the claude config: %v", err)
	}
	if w.Kind != "pi" || w.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3" {
		t.Errorf("closeout resolves to %s/%s, want the merged document's pi/GLM — the claude config declares no closeout cell", w.Kind, w.Model)
	}
}

// TestSelectReplacesTheTierPolicy: a config's [tier_policy] replaces the
// merged policy — a ladder is a whole thing, and half of one ladder under
// half of another is a ladder nobody wrote.
func TestSelectReplacesTheTierPolicy(t *testing.T) {
	path := writeConfigs(t, configsCommon, configsCloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load the cloud-merged config: %v", err)
	}
	if cfg.TierPolicy == nil || len(cfg.TierPolicy.Concurrency) != 0 {
		t.Fatalf("the merged policy is not the base case this test needs: %+v", cfg.TierPolicy)
	}
	selected, err := cfg.Select("claude")
	if err != nil {
		t.Fatalf("select claude: %v", err)
	}
	if selected.TierPolicy == nil || selected.TierPolicy.Concurrency["strong"] != 1 {
		t.Errorf("the claude config's policy did not replace the merged one: %+v", selected.TierPolicy)
	}
	// And the receiver is untouched: doctor selects every config from one
	// merged document, and no selection may reach into another's.
	again, err := cfg.Select("glm")
	if err != nil {
		t.Fatalf("select glm: %v", err)
	}
	if again.TierPolicy == nil || len(again.TierPolicy.Concurrency) != 0 {
		t.Errorf("selecting glm after claude saw claude's policy: %+v", again.TierPolicy)
	}
	if cfg.SelectedConfig != "" {
		t.Errorf("Select reached into the merged document: its config is %q", cfg.SelectedConfig)
	}
}

// TestSelectingAnUndeclaredConfigIsRefused: an operator who asked for claude
// and silently got the file's own cells would be an operator whose epic ran
// on a routing nobody chose.
func TestSelectingAnUndeclaredConfigIsRefused(t *testing.T) {
	path := writeConfigs(t, configsCommon, configsCloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load the cloud-merged config: %v", err)
	}
	_, err = cfg.Select("codex")
	if !errors.Is(err, ErrNoSuchConfig) {
		t.Fatalf("selecting an undeclared config is %v, want ErrNoSuchConfig", err)
	}
	if !strings.Contains(err.Error(), `"codex"`) || !strings.Contains(err.Error(), `"claude", "glm"`) {
		t.Errorf("the refusal does not name the selection and the declared configs: %v", err)
	}
}

// TestNoSelectionIsTheFileAsLoaded: "" is the no-selection view, which every
// reader that does not select resolves against — the gate, the evidence
// table, the findings routes — and which today's files (no [configs] table
// at all) still are.
func TestNoSelectionIsTheFileAsLoaded(t *testing.T) {
	path := writeConfigs(t, configsCommon, configsCloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load the cloud-merged config: %v", err)
	}
	same, err := cfg.Select("")
	if err != nil {
		t.Fatalf("the no-selection view refused: %v", err)
	}
	if same != cfg {
		t.Errorf("the no-selection view is not the loaded config")
	}

	// The historical file: no [configs] at all, and no default to name.
	plain := writeConfigs(t, configsCommon, "")
	cfg2, err := LoadFor(plain, SubstrateCloud)
	if err != nil {
		t.Fatalf("load a config with no named configs: %v", err)
	}
	if names := cfg2.NamedConfigNames(); names != nil {
		t.Errorf("a file with no [configs] declares %v", names)
	}
	if cfg2.DefaultConfigName() != "" {
		t.Errorf("a file with no [configs] defaults to %q", cfg2.DefaultConfigName())
	}
	if _, err := cfg2.Select("glm"); !errors.Is(err, ErrNoSuchConfig) {
		t.Errorf("selecting from a file that declares no configs is %v, want ErrNoSuchConfig", err)
	}
}

// TestAConfigCannotRouteACloudRoleTheRepositoryNeverDid: the cloud's
// fail-closed rule ([ErrNoCloudRouting]) reads the merged document's own
// cells, and a config-created cell routes only where the base cells do —
// so [configs.<name>] is not a way to route a role the repository never
// routed for the substrate it runs on.
func TestAConfigCannotRouteACloudRoleTheRepositoryNeverDid(t *testing.T) {
	common := `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[testing.commands]
go = { command = "go test ./..." }
`
	cloud := `version = 2

[configs]
default = "claude"

[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"

[configs.claude.roles.closeout]
kind = "claude"
model = "opus"

[configs.claude.tier_policy]
default = "strong"
ceiling = "strong"

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
`
	path := writeConfigs(t, common, cloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	selected, err := cfg.Select("claude")
	if err != nil {
		t.Fatalf("select claude: %v", err)
	}
	// closeout has no cell in the merged document's cloud file — a run would
	// route it through implement's cloud cell — and the claude config
	// declares one. The config-created cell carries no substrate overlay, so
	// it cannot route in the cloud: a config selects among the routings a
	// repository declared, and a role the cloud file never declared is not
	// one of them.
	if _, err := selected.ResolveOn(SubstrateCloud, "closeout", ""); !errors.Is(err, ErrNoCloudRouting) {
		t.Errorf("a config-created closeout cell routed in the cloud: %v", err)
	}
	// And the role the merged document DOES declare routes on the config.
	w, err := selected.ResolveOn(SubstrateCloud, "implement", "")
	if err != nil {
		t.Fatalf("resolve implement under the claude config: %v", err)
	}
	if w.Kind != "claude" || w.Model != "sonnet" {
		t.Errorf("implement resolves to %s/%s, want claude/sonnet", w.Kind, w.Model)
	}
	// The same config does not exist for a LOCAL run at all: it is declared
	// only in runners.cloud.toml, and the cloud file is the cloud world's —
	// the same rule that keeps a tier declared only in runners.local.toml
	// out of a cloud run. A local run selecting claude here is a typo the
	// sentinel names, never a fall back to the file's own cells.
	local, err := LoadForConfig(path, SubstrateHerdr, "claude")
	if err == nil {
		local.Resolve("closeout", "")
		t.Fatalf("a cloud-only config selected for a local run: %+v", local)
	}
	if !errors.Is(err, ErrNoSuchConfig) {
		t.Errorf("selecting a cloud-only config locally is %v, want ErrNoSuchConfig", err)
	}
}

// TestOverrideFilesMergeConfigs: the [configs] table merges the way every
// other table does — the override's cells over the common file's, per key —
// so a cloud file can declare the configs and the common file the base
// cells, or the two can split a config between them.
func TestOverrideFilesMergeConfigs(t *testing.T) {
	common := `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs]
default = "glm"

[configs.glm.roles.implement]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[testing.commands]
go = { command = "go test ./..." }
`
	cloud := `version = 2

[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"

[configs.claude.tier_policy]
default = "strong"
ceiling = "strong"

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
`
	path := writeConfigs(t, common, cloud)
	cfg, err := LoadFor(path, SubstrateCloud)
	if err != nil {
		t.Fatalf("load the merged config: %v", err)
	}
	if cfg.DefaultConfigName() != "glm" {
		t.Errorf("the merged default is %q, want glm", cfg.DefaultConfigName())
	}
	if _, ok := cfg.NamedConfig("glm"); !ok {
		t.Errorf("the common file's glm config did not survive the merge")
	}
	selected, err := cfg.Select("claude")
	if err != nil {
		t.Fatalf("select claude: %v", err)
	}
	w, err := selected.ResolveOn(SubstrateCloud, "implement", "")
	if err != nil {
		t.Fatalf("resolve implement under claude: %v", err)
	}
	if w.Kind != "claude" || w.Model != "sonnet" {
		t.Errorf("implement resolves to %s/%s, want claude/sonnet", w.Kind, w.Model)
	}
}

// TestConfigsCellShapeRulesMatchTheTopLevelTables: a config's role cells
// and policy answer to the same shape rules the top-level tables do — the
// same effort enum, the same tier names, the same policy bounds — or the
// split between "validated here" and "hoped there" is the drift this
// package exists to close.
func TestConfigsCellShapeRulesMatchTheTopLevelTables(t *testing.T) {
	doc := `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[testing.commands]
go = { command = "go test ./..." }

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "PI"
effort = "quite"

[configs.glm.roles.implement.tiers.frugal]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[configs.glm.tier_policy]
default = "strong"
ceiling = "economy"
`
	_, err := Parse([]byte(doc))
	if err == nil {
		t.Fatalf("a config with the shape errors the top-level tables refuse parsed")
	}
	msg := err.Error()
	for _, want := range []string{
		`configs.glm.roles.implement.kind: "PI"`,
		`configs.glm.roles.implement.effort`,
		`configs.glm.roles.implement.tiers.frugal: "frugal" is not one of`,
		`configs.glm.tier_policy.ceiling`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the validation does not report %q:\n%v", want, msg)
		}
	}
}

// TestLoadForConfigSelectsAndRefuses: the loader the reconciler and the
// profile reader go through, one name, one answer — and a name nobody
// declared is the sentinel, not a fall back.
func TestLoadForConfigSelectsAndRefuses(t *testing.T) {
	path := writeConfigs(t, configsCommon, configsCloud)
	cfg, err := LoadForConfig(path, SubstrateCloud, "claude")
	if err != nil {
		t.Fatalf("load claude: %v", err)
	}
	if cfg.SelectedConfig != "claude" {
		t.Errorf("the loaded config does not name its selection: %q", cfg.SelectedConfig)
	}
	if _, err := LoadForConfig(path, SubstrateCloud, "opus-max"); !errors.Is(err, ErrNoSuchConfig) {
		t.Errorf("an undeclared selection is %v, want ErrNoSuchConfig", err)
	}
	// "" is exactly what LoadFor loads.
	plain, err := LoadForConfig(path, SubstrateCloud, "")
	if err != nil {
		t.Fatalf("the no-selection load refused: %v", err)
	}
	if plain.SelectedConfig != "" {
		t.Errorf("the no-selection load names a config: %q", plain.SelectedConfig)
	}
}
