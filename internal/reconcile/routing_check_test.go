package reconcile

import (
	"fmt"
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
// file's claude frontier included), resolves to the durable harness on a
// Workers AI model, by either of its two names (cloudDurableKinds): nothing
// in the cloud runs claude.
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
			if !cloudRunnerIsDurable(job.Profile.Runner) || !strings.HasPrefix(job.Profile.Model, "cloudflare-workers-ai/") {
				t.Errorf("cloud %s at tier %q routes to %s/%s, want the durable harness (pi or pi-durable) on a Workers AI model",
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
			if !cloudRunnerIsDurable(p.Runner) || !strings.HasPrefix(p.Model, "cloudflare-workers-ai/") {
				t.Errorf("cloud %s at tier %q resolves to %s/%s: claude (or anything off Workers AI) leaked into the cloud",
					role, tier, p.Runner, p.Model)
			}
		}
	}
}

// cloudDurableKinds are the two names of the ONE durable harness a cloud job
// may finally resolve to (epic 43y, tick twa): `pi` is the local runner
// table's name for the harness since tick hpk — the spelling this
// repository's cloud overlay (`.tick/runners.cloud.toml`) still carries —
// and `pi-durable` is the hosted kind the cloud profile set names (tick qf4)
// and the sandbox image hosts (tick jhp: a container told any other kind
// dies at boot with "unknown harness kind"). The repository guard pins the
// durable harness, not one spelling of it, because the overlay's flip to
// the hosted kind is the operator's own config change — a dispatched
// worker's .tick boundary exempts `.tick/runners.toml` and never the
// overlays — and a pin on the old spelling alone turns the gate red the
// moment that flip lands: a guard certifying the staleness it exists to
// catch. Once the overlay names pi-durable, tighten this to the hosted kind
// alone, together with the local alias in profile.CloudRule.Harnesses.
var cloudDurableKinds = []string{"pi", "pi-durable"}

// cloudRunnerIsDurable reports whether a resolved cloud profile finally
// runs the one durable harness, by either of its names (cloudDurableKinds).
func cloudRunnerIsDurable(kind string) bool {
	for _, durable := range cloudDurableKinds {
		if kind == durable {
			return true
		}
	}
	return false
}

// short: writes two small TOML files to a temp directory and resolves profiles
// in memory; no harness, no git.
//
// The operator's flip, in miniature (epic 43y, tick twa): when
// .tick/runners.cloud.toml's cells name the hosted kind `pi-durable` — the
// exact change this repository's cloud routing needs, and one a dispatched
// worker's .tick boundary cannot write — the routing check still routes
// every job, and the repository guard accepts every one of them. Before
// twa the guard pinned the old spelling `pi` alone, so this very flip would
// have turned the gate red the moment it landed: a guard certifying the
// staleness it exists to catch.
func TestTheCloudGuardAcceptsTheOverlayNamingTheHostedKind(t *testing.T) {
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

[roles.closeout]
kind = "claude"
model = "opus"
`)
	// The flipped overlay: the same cells the repository's own
	// .tick/runners.cloud.toml carries today, with every kind cell naming
	// the hosted kind instead of the local spelling.
	writeTOML(t, filepath.Join(dir, "runners.cloud.toml"), `version = 2

[roles.implement]
kind = "pi-durable"
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
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review.tiers.strong]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.closeout]
kind = "pi-durable"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
`)
	jobs, err := CheckRouting(profile.EmbeddedCloud, config, runconfig.SubstrateCloud)
	if err != nil {
		t.Fatalf("the flipped cloud routing does not route: %v", err)
	}
	seen := map[string]bool{}
	for _, job := range jobs {
		seen[job.Role] = true
		if !cloudRunnerIsDurable(job.Profile.Runner) || !strings.HasPrefix(job.Profile.Model, "cloudflare-workers-ai/") {
			t.Errorf("cloud %s at tier %q routes to %s/%s: the guard refused the overlay's own naming of the durable harness",
				job.Role, job.Tier, job.Profile.Runner, job.Profile.Model)
		}
	}
	for _, role := range profile.EveryRole() {
		if !seen[role] {
			t.Errorf("%s was never routed by the check", role)
		}
	}
	// The flip's point, stated: the base implement job finally names the
	// HOSTED kind — a runner the old pin (`Runner != "pi"`) would have failed
	// the gate on, and the one the sandbox image hosts.
	for _, job := range jobs {
		if job.Role == "implement-tick" && job.Tier == "" && job.Profile.Runner != "pi-durable" {
			t.Errorf("implement-tick resolved to %q, want the hosted kind pi-durable the flipped overlay names",
				job.Profile.Runner)
		}
	}
	// The pin still refuses every kind that is not the durable harness.
	for _, kind := range []string{"claude", "codex", "omp", "opencode"} {
		if cloudRunnerIsDurable(kind) {
			t.Errorf("the guard accepts kind %q, which is not the durable harness", kind)
		}
	}
}

// short: reads this repository's .tick/runners*.toml and resolves profiles in
// memory; no harness, no git, milliseconds.
//
// The one-harness rule, on the LOCAL half (epic 43y, tick 7ml). The common
// runners.toml's [roles.implement.tiers.balanced] named kind = "codex" — a
// Phase-2 leftover — and runners.local.toml declares no [roles.implement]
// role cell to overlay it away, so a local run (herdr or harness substrate)
// pinning --tier balanced dispatched an implement worker on the codex CLI:
// a harness that is neither pi-durable nor the claude-CLI frontier rung the
// operator's 2026-10-04 decision allows. Balanced is a rung no ladder ever
// derives — the local ladder climbs strong -> frontier, the cloud's economy
// -> strong — so the ladder check above never resolved it; only a pin
// reaches it, and this guard resolves every tier name a pin can, exactly as
// a pin does. Since hpk the `pi` kind IS the pi-durable Node harness, so
// every implement tier but frontier must resolve to pi, and frontier — the
// operator's kept claude-CLI rung — to claude. A tier no file declares must
// REFUSE naming the tier (the profile layer's fail-closed rule: a tier that
// silently falls back to the role is a tier an operator paid for and did
// not get) — never resolve onto some other harness.
func TestThisRepositorysLocalImplementTiersRouteOnTheOneHarness(t *testing.T) {
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
	} {
		for _, tier := range runconfig.TierNames {
			p, err := profile.Resolve("implement-tick", profile.Options{
				Dir: tc.dir, RunnersConfig: config, Tier: string(tier), Substrate: string(tc.substrate),
			})
			if err != nil {
				// Fail-closed or fail-loud, never fail-wrong: a tier no local
				// file declares is refused naming it. Any other refusal is a
				// routing defect in its own right.
				if !strings.Contains(err.Error(), fmt.Sprintf("declares no tier %q", tier)) {
					t.Errorf("substrate %s (profiles %q): implement at tier %q refuses for the wrong reason: %v",
						tc.substrate, tc.dir, tier, err)
				}
				continue
			}
			want := "pi"
			if tier == runconfig.TierFrontier {
				want = "claude" // the operator's kept claude-CLI frontier rung (2026-10-04)
			}
			if p.Runner != want {
				t.Errorf("substrate %s (profiles %q): implement at tier %q routes to %s/%s, want the %s harness — "+
					"pi-durable everywhere, claude only as the frontier rung: no tier a pin can reach may name another CLI",
					tc.substrate, tc.dir, tier, p.Runner, p.Model, want)
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
