package profile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The plan-repair profile (ticfac tick wj6): the role a run dispatches when
// the integrated gate fails over a merge that is already on the integration
// branch, instead of stopping for a person to read a gate log and patch the
// tree by hand.
//
// It is resolved ON DEMAND — not one of [Roles], the set a run resolves at
// construction — for the same reason the resolve-conflict role is: a role
// that exists for the rare failed gate must not make every cloud run refuse
// at start over a cell the operator was never asked to declare. What these
// tests pin is the routing at the moment a run actually meets the failure,
// and it is the resolve-conflict job's routing exactly (2p6's argument, in
// full there):
//
//   - the candidates END AT THE REVIEW CELL, the frontier judgement routing
//     every config already declares: locally that is claude, and a run that
//     declares the review cell's ceiling overlay routes the job at the
//     policy's ceiling through it;
//   - in the cloud the same candidates route it to a Workers AI model, and a
//     config that routes the job to claude there is refused — the repair job
//     NEVER runs claude in the cloud, exactly as no other role may;
//   - every profile set ships the role, so a container that meets a failed
//     gate dispatches its repair through the factory, not by falling back to
//     a profile a container cannot run.

// The review cell carries the ceiling overlay the policy asks for: the
// repair job runs on the frontier's model, at the tier the policy's ceiling
// names.
func TestTheRepairJobRoutesAtTheCeilingThroughTheReviewCell(t *testing.T) {
	t.Parallel()
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.review]
kind = "claude"
model = "opus"

[roles.review.tiers.frontier]
kind = "claude"
model = "opus"
`)

	resolved, err := Resolve(RoleRepairGate, Options{RunnersConfig: config, Tier: "frontier"})
	if err != nil {
		t.Fatalf("the repair job could not be routed at the ceiling: %v", err)
	}
	if resolved.Runner != "claude" || resolved.Model != "opus" {
		t.Errorf("the repair job routed to %s/%s, want the frontier claude/opus",
			resolved.Runner, resolved.Model)
	}
	if !strings.Contains(resolved.Routed, "[roles.review.tiers.frontier]") {
		t.Errorf("the routing does not name the ceiling overlay it applied: %q", resolved.Routed)
	}
}

// A config that declares the role's own cell wins over the review fallback —
// an operator who wants the repair job elsewhere says so in one place.
func TestADedicatedRepairCellWinsOverTheReviewFallback(t *testing.T) {
	t.Parallel()
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.repair]
kind = "pi"
model = "`+glm53+`"

[roles.review]
kind = "claude"
model = "opus"
`)

	resolved, err := Resolve(RoleRepairGate, Options{RunnersConfig: config})
	if err != nil {
		t.Fatalf("a dedicated repair cell was refused: %v", err)
	}
	if resolved.Runner != "pi" || resolved.Model != glm53 {
		t.Errorf("the repair job routed to %s/%s, want the dedicated cell's pi/%s",
			resolved.Runner, resolved.Model, glm53)
	}
}

// NEVER CLAUDE IN THE CLOUD (the tick's acceptance, in so many words): a
// cloud routing that resolves the repair job to claude is refused on its
// FINAL value, and the same config on a laptop keeps it — the rule is the
// cloud's, and the repair job answers to it like every other role.
func TestTheRepairJobNeverRunsClaudeInTheCloud(t *testing.T) {
	t.Parallel()
	config := cloudRuleConfig(t, `version = 2

[roles.review]
kind = "claude"
model = "opus"
`)

	_, err := Resolve(RoleRepairGate, Options{RunnersConfig: config, Substrate: "cloud"})
	assertCloudRefusal(t, err, RoleRepairGate, "claude", "opus", "no tier")

	local, err := Resolve(RoleRepairGate, Options{RunnersConfig: config, Substrate: "herdr"})
	if err != nil {
		t.Fatalf("the cloud rule reached a herdr resolution: %v", err)
	}
	if local.Runner != "claude" || local.Model != "opus" {
		t.Errorf("a herdr repair job routed to %s/%s, want the frontier claude/opus", local.Runner, local.Model)
	}
}

// The cloud's own routing — a Workers AI model on the harness the gateway
// serves — resolves the repair job like any other role: a container that
// meets a failed gate dispatches its repair through the factory.
func TestTheCloudRoutesTheRepairJobToWorkersAI(t *testing.T) {
	t.Parallel()
	config := cloudRuleConfig(t, `version = 2

[roles.review]
kind = "pi"
model = "`+glm53+`"
`)

	resolved, err := Resolve(RoleRepairGate, Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the cloud refused a Workers AI repair routing: %v", err)
	}
	if resolved.Runner != "pi" || resolved.Model != glm53 {
		t.Errorf("the cloud repair job routed to %s/%s, want pi/%s", resolved.Runner, resolved.Model, glm53)
	}
}

// The cloud profile SET ships the plan-repair pairing with every other role:
// a container that meets a failed gate resolves the role from the set it
// selects, not by falling back to a profile a container cannot run.
func TestTheCloudProfileSetPairsTheRepairJobWithTheSandboxExecutor(t *testing.T) {
	t.Parallel()
	resolved, err := Resolve(RoleRepairGate, Options{Dir: cloudProfileDir(t)})
	if err != nil {
		t.Fatalf("the cloud profile set does not resolve the plan-repair role: %v", err)
	}
	if resolved.Executor != cloudExecutorName {
		t.Errorf("the plan-repair profile names executor %q, want %q", resolved.Executor, cloudExecutorName)
	}
	if resolved.Runner != "pi" || resolved.Model != glm53 {
		t.Errorf("the plan-repair profile pairs %s/%s, want pi/%s", resolved.Runner, resolved.Model, glm53)
	}
}

// The local compiled-in set ships the role too: a laptop's repair job is a
// claude/opus process, the ceiling-grade judgement job the profile says it
// is — the same pairing the resolve-conflict job ships.
func TestTheCompiledInSetShipsTheRepairJobProfile(t *testing.T) {
	t.Parallel()
	resolved, err := Resolve(RoleRepairGate, Options{})
	if err != nil {
		t.Fatalf("the compiled-in profile set does not resolve the plan-repair role: %v", err)
	}
	if resolved.Executor != "local-subprocess" || resolved.Runner != "claude" || resolved.Model != "opus" {
		t.Errorf("the compiled-in plan-repair profile is %s/%s on %s, want claude/opus on local-subprocess",
			resolved.Runner, resolved.Model, resolved.Executor)
	}
	if !strings.Contains(resolved.Prompt, "plan-repair") {
		t.Errorf("the plan-repair prompt does not name its own role")
	}
}

// The herdr set ships the role as well: a herdr-run repair is a Workers AI
// model on the herdr executor, not a silent fall back to the local set.
func TestTheHerdrProfileSetShipsTheRepairJob(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(RoleRepairGate, Options{Dir: filepath.Join(root, "profiles-herdr")})
	if err != nil {
		t.Fatalf("the herdr profile set does not resolve the plan-repair role: %v", err)
	}
	if resolved.Executor != "herdr" || resolved.Runner != "pi" || resolved.Model != glm53 {
		t.Errorf("the herdr plan-repair profile is %s/%s on %s, want pi/%s on herdr",
			resolved.Runner, resolved.Model, resolved.Executor, glm53)
	}
}
