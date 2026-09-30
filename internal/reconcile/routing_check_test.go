package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// Every job a run can dispatch is routed BEFORE the run starts (run
// run_3f034e68, epic hn6, 2026-09-30). PR #146 (tick 7l1) set the cloud's
// [tier_policy] ceiling to "strong"; the resolve-conflict job routes at the
// ceiling through the REVIEW cell, and the cloud's review cell declared no
// strong tier. Nothing noticed until the run met its first merge conflict,
// hours in, and stopped: "its resolve-conflict job could not be routed at the
// ceiling: [roles.review] declares no tier "strong"". A routing that cannot
// resolve is a config defect, and a config defect is refused at start — by
// the run, by `ticfac doctor`, and by this repository's own CI.

// short: reads this repository's .tick/runners*.toml and resolves profiles in
// memory; no harness, no git, milliseconds.
//
// The repository guard: THIS repository's real routing, on every substrate a
// run of it executes on, routes every job — so a config PR like #146 fails CI
// instead of stopping a live run. And in the cloud every one of those jobs,
// and the review cell at every tier any file declares for it (the common
// file's claude frontier included), resolves to pi on a Workers AI model:
// nothing in the cloud runs claude.
func TestThisRepositorysRoutingRoutesEveryJobOnEverySubstrate(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".tick", "runners.toml")
	for _, tc := range []struct {
		substrate runconfig.Substrate
		dir       string
	}{
		{runconfig.SubstrateHerdr, ""},
		{runconfig.SubstrateHerdr, profile.EmbeddedHerdr},
		{runconfig.SubstrateHarness, ""},
		{runconfig.SubstrateCloud, profile.EmbeddedCloud},
	} {
		jobs, err := CheckRouting(tc.dir, config, tc.substrate)
		if err != nil {
			t.Errorf("substrate %s (profiles %q): %v", tc.substrate, tc.dir, err)
			continue
		}
		seen := map[string]bool{}
		for _, job := range jobs {
			seen[job.Role] = true
			if tc.substrate != runconfig.SubstrateCloud {
				continue
			}
			if job.Profile.Runner != "pi" || !strings.HasPrefix(job.Profile.Model, "cloudflare-workers-ai/") {
				t.Errorf("cloud %s at tier %q routes to %s/%s, want pi on a Workers AI model",
					job.Role, job.Tier, job.Profile.Runner, job.Profile.Model)
			}
		}
		for _, role := range profile.EveryRole() {
			if !seen[role] {
				t.Errorf("substrate %s: %s was never routed by the check", tc.substrate, role)
			}
		}
	}

	// No tier the common file declares can leak claude into the cloud: every
	// tier name the review cell resolves at under the cloud substrate — the
	// on-demand jobs route through it — is a Workers AI model.
	for _, role := range []string{"review-epic", profile.RoleResolveConflict, profile.RoleRepairGate, "implement-tick", "closeout-epic"} {
		for _, tier := range runconfig.TierNames {
			p, err := profile.Resolve(role, profile.Options{
				Dir: profile.EmbeddedCloud, RunnersConfig: config, Tier: string(tier), Substrate: string(runconfig.SubstrateCloud),
			})
			if err != nil {
				continue // a tier the cloud does not declare is refused, never run
			}
			if p.Runner != "pi" || !strings.HasPrefix(p.Model, "cloudflare-workers-ai/") {
				t.Errorf("cloud %s at tier %q resolves to %s/%s: claude (or anything off Workers AI) leaked into the cloud",
					role, tier, p.Runner, p.Model)
			}
		}
	}
}

// short: writes two small TOML files to a temp directory and resolves
// profiles in memory; no harness.
//
// The hn6 shape in miniature: the common file's review cell declares only a
// frontier tier, the cloud file caps the ladder at strong and routes review
// without a strong tier. The base roles all route — the old construction
// check passed — and the resolve-conflict job does not. The check names the
// job, the tier and the cell.
func TestTheRoutingCheckRefusesAResolveJobThatCannotRouteAtTheCeiling(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := filepath.Join(dir, "runners.toml")
	writeTOML(t, config, `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review]
kind = "claude"
model = "opus"

[roles.review.tiers.frontier]
kind = "claude"
model = "opus"

[roles.closeout]
kind = "claude"
model = "opus"
`)
	cloudCells := `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[tier_policy]
default = "economy"
ceiling = "strong"
step = 2

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
`
	writeTOML(t, filepath.Join(dir, "runners.cloud.toml"), cloudCells)

	_, err := CheckRouting(profile.EmbeddedCloud, config, runconfig.SubstrateCloud)
	if err == nil {
		t.Fatal("a cloud routing whose resolve-conflict job cannot route at the ceiling passed the check")
	}
	for _, want := range []string{profile.RoleResolveConflict, `"strong"`, "roles.review"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}

	// The fix: the cloud's review cell declares the ceiling's tier.
	writeTOML(t, filepath.Join(dir, "runners.cloud.toml"), cloudCells+`
[roles.review.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
`)
	if _, err := CheckRouting(profile.EmbeddedCloud, config, runconfig.SubstrateCloud); err != nil {
		t.Fatalf("the fixed routing is still refused: %v", err)
	}
}

// The run itself refuses at START, at construction, when the resolve-conflict
// job cannot route at the ceiling — before a tick is claimed, rather than at
// the first conflict hours in.
func TestARunRefusesAtStartWhenTheResolveJobCannotRouteAtTheCeiling(t *testing.T) {
	t.Parallel()
	const gate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.strong]
model = "opus"

[roles.review]
kind = "claude"
model = "opus"

[tier_policy]
default = "strong"
ceiling = "strong"

[testing.commands]
tree = { command = "test -f README.md", description = "the merge carries the work" }
`
	f := newFixture(t, fixtureOptions{gate: gate})
	_, err := New(f.options(f.Repo, fixtureOptions{gate: gate}))
	if err == nil {
		t.Fatal("a run whose resolve-conflict job cannot route at the ceiling was constructed")
	}
	for _, want := range []string{profile.RoleResolveConflict, `"strong"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
}

func writeTOML(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
