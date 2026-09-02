package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three Phase 1 role profiles, resolved from the profiles/ directory this
// repository ships, routed by the target repository's `.tick/runners.toml`, and
// recorded with the provenance an attempt record carries.

func TestTheThreeRoleProfilesResolve(t *testing.T) {
	if len(Roles) != 3 {
		t.Fatalf("Phase 1 declares %d roles: %v", len(Roles), Roles)
	}
	digests := map[string]string{}
	for _, role := range Roles {
		p, err := Resolve(role, Options{})
		if err != nil {
			t.Fatalf("%s did not resolve: %v", role, err)
		}
		if p.Role != role {
			t.Errorf("%s resolved as %s", role, p.Role)
		}
		if p.Executor != "local-subprocess" {
			t.Errorf("%s names executor %q; Phase 1 has one executor", role, p.Executor)
		}
		if p.Runner == "" || p.Model == "" {
			t.Errorf("%s resolved runner %q and model %q", role, p.Runner, p.Model)
		}
		if len(p.Prompt) < 100 || !strings.Contains(p.Prompt, role) {
			t.Errorf("%s resolved a prompt of %d bytes that does not name the role", role, len(p.Prompt))
		}
		if p.Version == "" {
			t.Errorf("%s is unversioned: a profile nobody can name a version of is not one a record can cite", role)
		}
		if p.Digest == "" || p.Source == "" || p.PromptSource == "" {
			t.Errorf("%s carries no provenance: %+v", role, p.Provenance)
		}
		if previous, clash := digests[p.Digest]; clash {
			t.Errorf("%s and %s digest the same: %s", role, previous, p.Digest)
		}
		digests[p.Digest] = role
	}
}

// SPEC §4.5's Phase 1 rule: a profile is exactly {executor, runner, model,
// prompt}. Nothing else — an option smuggled into a profile is an option
// nothing records and nothing validates.
func TestAProfileIsExactlyFourFields(t *testing.T) {
	p, err := Resolve("implement-tick", Options{})
	if err != nil {
		t.Fatal(err)
	}
	fields := p.Fields()
	want := []string{"executor", "model", "prompt", "runner"}
	if len(fields) != len(want) {
		t.Fatalf("a profile resolves %d fields: %v", len(fields), fields)
	}
	for _, name := range want {
		if fields[name] == "" {
			t.Errorf("field %q is empty or absent: %v", name, fields)
		}
	}
}

// The same rule, enforced where it can actually be broken: a profile FILE that
// declares a fifth thing is refused rather than read with the extra ignored.
func TestAProfileFileWithAFifthFieldIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "implement-tick.json"), `{
  "schema_version": 1,
  "role": "implement-tick",
  "version": "1.0.0",
  "executor": "local-subprocess",
  "runner": "claude",
  "model": "sonnet",
  "prompt": "implement-tick.md",
  "effort": "high"
}`)
	write(t, filepath.Join(dir, "implement-tick.md"), "a prompt")

	_, err := Resolve("implement-tick", Options{Dir: dir})
	if err == nil {
		t.Fatal("a profile with a fifth field was accepted")
	}
	if !strings.Contains(err.Error(), "effort") {
		t.Errorf("the refusal does not name the field it refused: %v", err)
	}
}

func TestAProfileWhosePromptIsMissingIsRefused(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "review-epic.json"), `{
  "schema_version": 1,
  "role": "review-epic",
  "version": "1.0.0",
  "executor": "local-subprocess",
  "runner": "claude",
  "model": "opus",
  "prompt": "nothing-here.md"
}`)
	if _, err := Resolve("review-epic", Options{Dir: dir}); err == nil {
		t.Fatal("a profile naming a prompt that does not exist was accepted")
	}
}

func TestAnUnknownRoleIsRefused(t *testing.T) {
	if _, err := Resolve("triage-failure", Options{}); err == nil {
		t.Fatal("a role Phase 1 ships no profile for resolved anyway")
	}
}

// The routing an operator already configures for `tk herd` is the routing a
// ticfac run honours: `[roles.*]` of the TARGET repository's runners.toml, so a
// run is configured in one place.
func TestTheTargetRepositoriesRunnersConfigRoutesRunnerAndModel(t *testing.T) {
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "codex"
model = "gpt-5.6-luna"

[roles.review]
kind = "pi"
model = "pi-large"
`)
	implement, err := Resolve("implement-tick", Options{RunnersConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	if implement.Runner != "codex" || implement.Model != "gpt-5.6-luna" {
		t.Errorf("implement-tick routed to %s/%s", implement.Runner, implement.Model)
	}
	if !strings.Contains(implement.Routed, "roles.implement") {
		t.Errorf("the provenance does not say where the routing came from: %q", implement.Routed)
	}

	review, err := Resolve("review-epic", Options{RunnersConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	if review.Runner != "pi" || review.Model != "pi-large" {
		t.Errorf("review-epic routed to %s/%s", review.Runner, review.Model)
	}

	// A role the file does not declare keeps what the profile ships with, and
	// says so: silence in the config is not a routing.
	closeout, err := Resolve("closeout-epic", Options{RunnersConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	shipped, err := Resolve("closeout-epic", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if closeout.Runner != shipped.Runner || closeout.Model != shipped.Model {
		t.Errorf("closeout-epic was routed by a file that does not declare it: %s/%s", closeout.Runner, closeout.Model)
	}
	if closeout.Routed != "" {
		t.Errorf("closeout-epic claims routing from %q", closeout.Routed)
	}
}

// A tier is the overlay `tk herd` reads it as: it overrides what it names and
// leaves the rest of the role alone.
func TestATierOverridesTheRole(t *testing.T) {
	config := writeConfig(t, `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.implement.tiers.economy]
model = "haiku"

[roles.implement.tiers.balanced]
kind = "codex"
model = "gpt-5.6-luna"
`)
	economy, err := Resolve("implement-tick", Options{RunnersConfig: config, Tier: "economy"})
	if err != nil {
		t.Fatal(err)
	}
	if economy.Runner != "claude" || economy.Model != "haiku" {
		t.Errorf("the economy tier resolved %s/%s", economy.Runner, economy.Model)
	}
	if !strings.Contains(economy.Routed, "economy") {
		t.Errorf("the provenance does not name the tier: %q", economy.Routed)
	}

	balanced, err := Resolve("implement-tick", Options{RunnersConfig: config, Tier: "balanced"})
	if err != nil {
		t.Fatal(err)
	}
	if balanced.Runner != "codex" || balanced.Model != "gpt-5.6-luna" {
		t.Errorf("the balanced tier resolved %s/%s", balanced.Runner, balanced.Model)
	}

	// A tier nobody declared is a refusal, not a silent fallback to the role:
	// an operator who asked for economy and got the default paid for the
	// default without being told.
	if _, err := Resolve("implement-tick", Options{RunnersConfig: config, Tier: "unlimited"}); err == nil {
		t.Error("a tier the config does not declare resolved anyway")
	}
}

// The digest is over what was RESOLVED, not over the file: two runs with the
// same profile file and different routing evaluated different things, and a
// digest that could not tell them apart would make one run's evidence read as
// the other's.
func TestRoutingChangesTheDigest(t *testing.T) {
	shipped, err := Resolve("implement-tick", Options{})
	if err != nil {
		t.Fatal(err)
	}
	routed, err := Resolve("implement-tick", Options{RunnersConfig: writeConfig(t, `[roles.implement]
kind = "pi"
`)})
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Digest == routed.Digest {
		t.Errorf("a routed profile digests the same as the shipped one: %s", shipped.Digest)
	}

	// And it is stable: the same inputs digest the same, or an evidence record
	// would go stale against itself between two reads.
	again, err := Resolve("implement-tick", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Digest != shipped.Digest {
		t.Errorf("two resolutions of one profile digest differently: %s and %s", shipped.Digest, again.Digest)
	}
}

func TestResolveAllResolvesEveryPhaseOneRole(t *testing.T) {
	all, err := ResolveAll(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(Roles) {
		t.Fatalf("ResolveAll returned %d of %d profiles", len(all), len(Roles))
	}
	for _, role := range Roles {
		if all[role] == nil {
			t.Errorf("no profile for %s", role)
		}
	}
}

func writeConfig(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runners.toml")
	write(t, path, document)
	return path
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
