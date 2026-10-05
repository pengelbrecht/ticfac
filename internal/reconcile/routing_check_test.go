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

// short: reads this repository's .tick/runners*.toml — and, to prove the
// sweep bites, copies of them with one extra cell in a temp directory —
// and resolves profiles in memory; no harness, no git, milliseconds.
//
// The local claude exception, blessed and then GUARDED (epic 43y, tick j6o).
// The operator's 2026-10-04 decision keeps the claude CLI as the local
// frontier rung only, and the 2026-10-05 decision on this tick adds the
// review and closeout cells to the exception: local final reviews and
// close-outs run on claude opus on purpose — the hn6 run was moved to a
// local run for exactly that — and the on-demand judgement jobs
// (resolve-conflict, plan-repair) inherit the review cell at the policy's
// ceiling. This is the 7ml guard (implement only) extended to every role a
// run can dispatch, at base values and at every tier a pin can ask for,
// held to the table below: claude EXACTLY on the blessed cells, pi
// everywhere else a resolution exists, and a refusal naming the tier
// where none does. So the exception cannot quietly widen — the old codex
// balanced cell would fail this sweep, and so would a claude cell added
// under any rung the operator did not bless — and it cannot quietly
// narrow either: a config that reroutes a local review or close-out off
// the claude CLI defies the decision the cells record.
func TestThisRepositorysLocalClaudeRoutingIsExactlyTheBlessedCells(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, ".tick", "runners.toml")
	for _, problem := range sweepLocalClaude(config) {
		t.Error(problem)
	}

	// The sweep bites. This is the 7ml shape — a leftover cell no ladder
	// derives, reachable only by a pin, naming a harness the operator did
	// not bless — and with the cell present the sweep must flag it, or it
	// certifies nothing.
	dir := t.TempDir()
	common, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	local, err := os.ReadFile(filepath.Join(root, ".tick", "runners.local.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writeTOML(t, filepath.Join(dir, "runners.toml"), string(common)+`

# the leak under test: the claude CLI on a rung the operator did not bless
[roles.implement.tiers.balanced]
kind = "claude"
model = "opus"
`)
	writeTOML(t, filepath.Join(dir, "runners.local.toml"), string(local))
	problems := sweepLocalClaude(filepath.Join(dir, "runners.toml"))
	if len(problems) == 0 {
		t.Fatal("a claude cell on an unblessed rung passed the sweep")
	}
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"implement-tick", `"balanced"`, "claude"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the sweep's report does not name %s:\n%s", want, joined)
		}
	}
}

// blessedLocalClaude is the operator's local claude exception as a table:
// every role a run can dispatch (the profile layer's [Roles] plus its
// on-demand judgement jobs) against every tier a pin can ask for, and the
// harness the resolution must produce. "" means the resolution must
// REFUSE naming the tier — fail-closed, never fail-wrong (the 7ml rule) —
// and "claude" is the exception itself: implement's kept frontier rung
// (2026-10-04) and the review/closeout cells (2026-10-05, tick j6o). The
// on-demand rows are the review cell's routing by construction — their
// candidates end at [roles.review] — so they are blessed wherever it is.
var blessedLocalClaude = map[string]map[string]string{
	// Implementation: pi-durable at base and every rung the files declare,
	// claude only as the frontier rung, a refusal for the rest (balanced,
	// since 7ml removed the codex leftover).
	"implement-tick": {"": "pi", "economy": "pi", "balanced": "", "strong": "pi", "frontier": "claude"},

	// The blessed cells: local final reviews and close-outs on the claude
	// CLI on purpose, at base values and at every tier the cells declare.
	"review-epic":   {"": "claude", "economy": "", "balanced": "", "strong": "", "frontier": "claude"},
	"closeout-epic": {"": "claude", "economy": "", "balanced": "", "strong": "", "frontier": ""},

	// The on-demand judgement jobs, at the review cell's routing.
	profile.RoleResolveConflict: {"": "claude", "economy": "", "balanced": "", "strong": "", "frontier": "claude"},
	profile.RoleRepairGate:      {"": "claude", "economy": "", "balanced": "", "strong": "", "frontier": "claude"},
}

// sweepLocalClaude resolves every local role×tier against config and
// returns every disagreement with [blessedLocalClaude], as strings rather
// than t.Errorf calls so the guard's own test can also point it at a config
// it must flag.
func sweepLocalClaude(config string) []string {
	var problems []string
	tiers := []string{""}
	for _, tier := range runconfig.TierNames {
		tiers = append(tiers, string(tier))
	}
	for _, tc := range []struct {
		substrate runconfig.Substrate
		dir       string
	}{
		{runconfig.SubstrateHerdr, ""},
		{runconfig.SubstrateHerdr, profile.EmbeddedHerdr},
		{runconfig.SubstrateHarness, ""},
	} {
		for _, role := range profile.EveryRole() {
			for _, tier := range tiers {
				want := blessedLocalClaude[role][tier]
				rung := "base values"
				if tier != "" {
					rung = fmt.Sprintf("tier %q", tier)
				}
				where := fmt.Sprintf("local %s (profiles %q): %s at %s", tc.substrate, tc.dir, role, rung)
				p, err := profile.Resolve(role, profile.Options{
					Dir: tc.dir, RunnersConfig: config, Tier: tier, Substrate: string(tc.substrate),
				})
				if want == "" {
					// Fail-closed or fail-loud, never fail-wrong: a rung no file
					// declares is refused naming it. Any other refusal is a
					// routing defect in its own right.
					if err == nil {
						problems = append(problems, fmt.Sprintf("%s routes to %s/%s, want a REFUSAL naming the rung — "+
							"no file declares it, and a pin that reaches it must be refused, never silently routed",
							where, p.Runner, p.Model))
						continue
					}
					if tier != "" && !strings.Contains(err.Error(), fmt.Sprintf("declares no tier %q", tier)) {
						problems = append(problems, fmt.Sprintf("%s refuses for the wrong reason: %v", where, err))
					}
					continue
				}
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s refuses, want the %s harness: %v", where, want, err))
					continue
				}
				if p.Runner != want {
					problems = append(problems, fmt.Sprintf("%s routes to %s/%s, want the %s harness", where, p.Runner, p.Model, want))
					continue
				}
				if want == "claude" && p.Model != "opus" {
					problems = append(problems, fmt.Sprintf("%s routes to claude/%s, want claude opus — "+
						"the operator's exception names opus, the model hn6 was moved to a local run for", where, p.Model))
				}
			}
		}
	}
	return problems
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
