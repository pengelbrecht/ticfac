package profile

import (
	"strings"
	"testing"
)

// The resolve-conflict profile (ticfac tick 2p6): the role a run dispatches
// when an attempt's merge onto the integration branch meets a content or
// add/add conflict, instead of stopping for a person.
//
// It is resolved ON DEMAND — not one of [Roles], the set a run resolves at
// construction — so what these tests pin is the routing that happens at the
// moment a run actually meets a conflict:
//
//   - the candidates END AT THE REVIEW CELL, the frontier judgement routing
//     every config already declares: locally that is claude, and a run that
//     declares the review cell's ceiling overlay routes the job at the
//     policy's ceiling through it;
//   - in the cloud the same candidates route it to a Workers AI model, and a
//     config that routes the job to claude there is refused — the resolve job
//     NEVER runs claude in the cloud, exactly as no other role may;
//   - the cloud profile SET ships the pairing (pi on GLM through the sandbox
//     executor) the same as every other role, so a container that meets a
//     conflict dispatches its resolve through the factory, not by falling
//     back to a profile a container cannot run.

// A dedicated cell always wins, and the review cell carries the ceiling
// overlay the policy asks for: the resolve-conflict job runs on the
// frontier's model, at the tier the policy's ceiling names.
func TestTheResolveJobRoutesAtTheCeilingThroughTheReviewCell(t *testing.T) {
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

	resolved, err := Resolve(RoleResolveConflict, Options{RunnersConfig: config, Tier: "frontier"})
	if err != nil {
		t.Fatalf("the resolve-conflict job could not be routed at the ceiling: %v", err)
	}
	if resolved.Runner != "claude" || resolved.Model != "opus" {
		t.Errorf("the resolve-conflict job routed to %s/%s, want the frontier claude/opus",
			resolved.Runner, resolved.Model)
	}
	if !strings.Contains(resolved.Routed, "[roles.review.tiers.frontier]") {
		t.Errorf("the routing does not name the ceiling overlay it applied: %q", resolved.Routed)
	}
}

// A config that declares the role's own cell wins over the review fallback —
// an operator who wants the resolve job elsewhere says so in one place.
func TestADedicatedResolveCellWinsOverTheReviewFallback(t *testing.T) {
	t.Parallel()
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.resolve]
kind = "pi"
model = "`+glm53+`"

[roles.review]
kind = "claude"
model = "opus"
`)

	resolved, err := Resolve(RoleResolveConflict, Options{RunnersConfig: config})
	if err != nil {
		t.Fatalf("a dedicated resolve cell was refused: %v", err)
	}
	if resolved.Runner != "pi" || resolved.Model != glm53 {
		t.Errorf("the resolve-conflict job routed to %s/%s, want the dedicated cell's pi/%s",
			resolved.Runner, resolved.Model, glm53)
	}
}

// NEVER CLAUDE IN THE CLOUD (the tick's acceptance, in so many words): a
// cloud routing that resolves the resolve-conflict job to claude is refused
// on its FINAL value, and the same config on a laptop keeps it — the rule is
// the cloud's, and the resolve job answers to it like every other role.
func TestTheResolveJobNeverRunsClaudeInTheCloud(t *testing.T) {
	t.Parallel()
	config := cloudRuleConfig(t, `version = 2

[roles.review]
kind = "claude"
model = "opus"
`)

	_, err := Resolve(RoleResolveConflict, Options{RunnersConfig: config, Substrate: "cloud"})
	assertCloudRefusal(t, err, RoleResolveConflict, "claude", "opus", "no tier")

	local, err := Resolve(RoleResolveConflict, Options{RunnersConfig: config, Substrate: "herdr"})
	if err != nil {
		t.Fatalf("the cloud rule reached a herdr resolution: %v", err)
	}
	if local.Runner != "claude" || local.Model != "opus" {
		t.Errorf("a herdr resolve-conflict routed to %s/%s, want the frontier claude/opus", local.Runner, local.Model)
	}
}

// The cloud's own routing — a Workers AI model on the harness the gateway
// serves — resolves the resolve job like any other role: a container that
// meets a conflict dispatches its resolve through the factory.
func TestTheCloudRoutesTheResolveJobToWorkersAI(t *testing.T) {
	t.Parallel()
	config := cloudRuleConfig(t, `version = 2

[roles.review]
kind = "pi"
model = "`+glm53+`"
`)

	resolved, err := Resolve(RoleResolveConflict, Options{RunnersConfig: config, Substrate: "cloud"})
	if err != nil {
		t.Fatalf("the cloud refused a Workers AI resolve-conflict routing: %v", err)
	}
	if resolved.Runner != "pi" || resolved.Model != glm53 {
		t.Errorf("the cloud resolve-conflict routed to %s/%s, want pi/%s", resolved.Runner, resolved.Model, glm53)
	}
}

// The cloud profile SET ships the resolve-conflict pairing with every other
// role (tick njj's shape, extended): a container selects the set and every
// role it can be asked to dispatch resolves from it.
func TestTheCloudProfileSetPairsTheResolveJobWithTheSandboxExecutor(t *testing.T) {
	t.Parallel()
	resolved, err := Resolve(RoleResolveConflict, Options{Dir: cloudProfileDir(t)})
	if err != nil {
		t.Fatalf("the cloud profile set does not resolve the resolve-conflict role: %v", err)
	}
	if resolved.Executor != cloudExecutorName {
		t.Errorf("the resolve-conflict profile names executor %q, want %q", resolved.Executor, cloudExecutorName)
	}
	if resolved.Runner != "pi" || resolved.Model != glm53 {
		t.Errorf("the resolve-conflict profile pairs %s/%s, want pi/%s", resolved.Runner, resolved.Model, glm53)
	}
}

// The local compiled-in set ships the role too: a laptop's resolve-conflict
// job is a claude/opus process, the ceiling-grade judgement job the profile
// says it is.
func TestTheCompiledInSetShipsTheResolveJobProfile(t *testing.T) {
	t.Parallel()
	resolved, err := Resolve(RoleResolveConflict, Options{})
	if err != nil {
		t.Fatalf("the compiled-in profile set does not resolve the resolve-conflict role: %v", err)
	}
	if resolved.Executor != "local-subprocess" || resolved.Runner != "claude" || resolved.Model != "opus" {
		t.Errorf("the compiled-in resolve-conflict profile is %s/%s on %s, want claude/opus on local-subprocess",
			resolved.Runner, resolved.Model, resolved.Executor)
	}
}
