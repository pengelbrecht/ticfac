package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The substrate a run executes on is an INPUT to role routing (tick 84z): the
// same target-repo runners.toml routes a role differently per substrate, and
// the cloud substrate REFUSES a role nobody declared cloud routing for rather
// than falling back to the base cell — the fallback is how a cloud run once
// reached a claude process nobody chose.

// resolveRunSubstrate is a pure function of the options, the environment and
// the config, so its answers are testable without a harness.
//
// short: no harness — one temp file and the pure derivation helper; the
// probes it may trigger are the production run's own and bounded to seconds.
// serial: this test states the process environment (TICKS_SUBSTRATE)
// through t.Setenv, which forbids t.Parallel.
func TestResolveRunSubstrateAnswersEveryInput(t *testing.T) {
	root := t.TempDir()
	gate := filepath.Join(root, "runners.toml")
	if err := os.WriteFile(gate, []byte("version = 2\n\n[roles.implement]\nkind = \"pi\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// An explicit pin wins, and says so — a caller that names the substrate
	// is whoever booted the run, and the file does not get to disagree.
	for _, sub := range []runconfig.Substrate{runconfig.SubstrateCloud, runconfig.SubstrateHerdr, runconfig.SubstrateHarness} {
		got, err := resolveRunSubstrate(Options{Substrate: string(sub), GateConfig: gate})
		if err != nil {
			t.Fatalf("an explicit %s pin: %v", sub, err)
		}
		if got != sub {
			t.Errorf("an explicit %s pin resolved %s", sub, got)
		}
	}

	// auto is a policy a decision procedure resolves, never a substrate a
	// caller can pin; and an unknown value is refused with the vocabulary.
	for _, pin := range []string{"auto", "edge"} {
		if _, err := resolveRunSubstrate(Options{Substrate: pin, GateConfig: gate}); err == nil {
			t.Errorf("substrate %q was accepted as a run's substrate", pin)
		}
	}

	// The environment override is how whatever BOOTS the run states the
	// substrate effective where it executes — a cloud container says so
	// without rewriting the tracked config the run's workers commit against.
	t.Setenv(runconfig.SubstrateEnvVar, "cloud")
	got, err := resolveRunSubstrate(Options{GateConfig: gate})
	if err != nil {
		t.Fatalf("the environment override did not resolve: %v", err)
	}
	if got != runconfig.SubstrateCloud {
		t.Errorf("TICKS_SUBSTRATE=cloud resolved %s", got)
	}

	// A value that is not a substrate is refused rather than read as "unset"
	// — the same fail-closed rule [runconfig.ParseOverride] enforces.
	t.Setenv(runconfig.SubstrateEnvVar, "edge")
	if _, err := resolveRunSubstrate(Options{GateConfig: gate}); err == nil {
		t.Error("an override that is not a substrate was silently ignored")
	}

	// With no override and no orchestration declaration the decision
	// procedure decides — and its answer is a substrate, never auto.
	t.Setenv(runconfig.SubstrateEnvVar, "")
	got, err = resolveRunSubstrate(Options{GateConfig: gate})
	if err != nil {
		t.Fatalf("no override, no declaration: %v", err)
	}
	if got == runconfig.SubstrateAuto {
		t.Error("the decision procedure returned auto, which a run cannot resolve roles against")
	}
	if !got.Valid() {
		t.Errorf("the decision procedure returned %q, which is not a substrate", got)
	}

	// A config that pins cloud decides cloud — the file's own declaration,
	// for runs that execute where it says.
	cloudGate := filepath.Join(root, "cloud-runners.toml")
	if err := os.WriteFile(cloudGate, []byte("version = 2\n\n[orchestration]\nsubstrate = \"cloud\"\n\n[roles.implement]\nkind = \"pi\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = resolveRunSubstrate(Options{GateConfig: cloudGate})
	if err != nil {
		t.Fatalf("a config declaring cloud: %v", err)
	}
	if got != runconfig.SubstrateCloud {
		t.Errorf("a config declaring cloud resolved %s", got)
	}
}

// The construction refusal the acceptance names: a run on the cloud substrate
// refuses AT START when any role it will dispatch has no cloud routing
// declared, naming the role — three ticks into an epic is not when a cloud
// container should discover it would have started a claude process.
func TestACloudRunRefusesAtStartOverARoleWithNoCloudRouting(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	f := newFixture(t, fixtureOptions{})
	_, _, err := f.run(f.Repo, fixtureOptions{substrate: "cloud"})
	if err == nil {
		t.Fatal("a cloud run over a config that declares no cloud routing started anyway")
	}
	if !errors.Is(err, runconfig.ErrNoCloudRouting) {
		t.Errorf("the refusal is not recognisable as ErrNoCloudRouting: %v", err)
	}
	// The role is named — the fix is a config edit, and an operator reading
	// the refusal has to know which cell to write. ResolveAll resolves the
	// roles in dispatch order, so the first unroutable one is the one named.
	for _, want := range []string{"implement", "runners.cloud.toml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// The same run, over a config that declares the cloud cells, dispatches every
// role off the claude harness — review and close-out included — while a local
// run of the same file keeps the frontier cells (asserted by the profile and
// runconfig packages against this repository's own file).
func TestACloudRunRoutesEveryRoleOffClaude(t *testing.T) {
	t.Parallel()
	shorttest.EndToEnd(t)
	const cloudGate = `version = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[roles.review]
kind = "claude"
model = "opus"

[roles.closeout]
kind = "claude"
model = "opus"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
`
	// The cloud's cells live in their own file (tick 5uo), beside the gate.
	const cloudCells = `version = 2

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
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	if err := os.WriteFile(filepath.Join(f.Repo.Dir, ".tick", "runners.cloud.toml"), []byte(cloudCells), 0o644); err != nil {
		t.Fatal(err)
	}
	_, result, err := f.run(f.Repo, fixtureOptions{gate: cloudGate, substrate: "cloud"})
	if err != nil {
		t.Fatalf("the cloud run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	for tick, want := range map[string][2]string{
		"a1": {"pi", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"},
		"a2": {"pi", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"},
		"rv": {"pi", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"},
		"co": {"pi", "cloudflare-workers-ai/@cf/zai-org/glm-5.3"},
	} {
		dispatch := f.dispatch(tick)
		if dispatch.Profile == nil {
			t.Fatalf("%s was dispatched under no profile", tick)
		}
		if dispatch.Profile.Runner != want[0] || dispatch.Profile.Model != want[1] {
			t.Errorf("%s was dispatched on %s/%s, want %s/%s — the cloud overlay did not route it",
				tick, dispatch.Profile.Runner, dispatch.Profile.Model, want[0], want[1])
		}
		if dispatch.Profile.Provenance.Substrate != "cloud" {
			t.Errorf("%s's provenance does not name the substrate: %q", tick, dispatch.Profile.Provenance.Substrate)
		}
	}
}
