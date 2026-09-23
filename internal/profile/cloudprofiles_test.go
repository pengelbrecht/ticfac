package profile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The cloud dispatch profile set (tick njj, epic xte): profiles-cloudflare-sandbox/
// is the directory a cloud orchestrator container points `ticfac run-epic
// --profiles` at — the pairing neither shipped profile makes.
//
// profiles-herdr/ carries the ROUTING this set keeps — pi on
// cloudflare-workers-ai/@cf/zai-org/glm-5.3, the pairing the operator's
// 2026-09-22 decision names — beside an executor (herdr) for which a container
// has no server. profiles/ carries a local executor beside claude + sonnet,
// which is the local default and the half the operator took off the cloud.
// This set is profiles-herdr/'s routing with the executor the container's
// factory can actually dispatch through: one Cloudflare sandbox per tick,
// booted by the factory the container asks over the dispatch door.
//
// It is a directory BESIDE the compiled-in profiles/ rather than a replacement
// of it. The compiled-in set is the LOCAL default a plain `ticfac run-epic`
// resolves — a laptop has no factory URL and no run token, and a profile
// naming the sandbox executor would be refused by the honoured set anyway —
// while the container, which the factory has told to dispatch workers into
// their own sandboxes, selects this set by naming it.
//
// No run can select this set until internal/cli's honoured set registers the
// cloudflare-sandbox executor, and that registration is deliberately held
// behind the collect-and-cancel decision the executor's settle path refuses on
// (internal/exec/cloudflaresandbox/doc.go): nothing in production may select
// an executor whose settle refuses. This set is the pairing that registration
// will make selectable — the reason it ships before the registration does is
// that the profile and the wiring answer different questions and were split
// into different ticks on purpose.

// The two Workers AI models the operator's decision names, in pi's spelling:
// GLM 5.3 for complex work and GLM 5.3 Flash for simple work. Both spellings
// are a `provider/id` pair exactly as `pi --list-models` prints them — the
// workers-ai/… form is omp's spelling, and omp is not the harness this set
// dispatches.
const (
	cloudGLM53        = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
	cloudGLM53Flash   = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"
	cloudDirName      = "profiles-cloudflare-sandbox"
	cloudExecutorName = "cloudflare-sandbox"
)

// cloudProfileDir is the repository's own cloud profile set, resolved from the
// tree the way a container's checkout holds it.
//
// short: a directory read under this repository; no I/O.
func cloudProfileDir(t *testing.T) string {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, cloudDirName)
}

// A dispatch profile exists pairing runner pi, executor cloudflare-sandbox and
// model cloudflare-workers-ai/@cf/zai-org/glm-5.3 — njj's first acceptance
// item. All three roles resolve from the one set, because a run resolves every
// role up front: a cloud profile set that profiled only implement-tick is a
// set a run refuses at construction, not one it selects.
//
// short: reads of this repository's own profile files; no I/O.
func TestTheCloudProfileSetPairsPiOnGLMWithTheSandboxExecutor(t *testing.T) {
	all, err := ResolveAll(Options{Dir: cloudProfileDir(t)})
	if err != nil {
		t.Fatalf("the cloud profile set did not resolve: %v", err)
	}
	digests := map[string]string{}
	for _, role := range Roles {
		p := all[role]
		if p == nil {
			t.Fatalf("%s resolved no profile", role)
		}
		if p.Executor != cloudExecutorName {
			t.Errorf("%s names executor %q, want %q: the cloud set pairs the routing profiles-herdr/ "+
				"carries with the executor the container's factory can boot, not with herdr (no server in "+
				"a container) and not with the local subprocess one (no worker shares the orchestrator)",
				role, p.Executor, cloudExecutorName)
		}
		if p.Runner != "pi" {
			t.Errorf("%s names runner %q, want pi: workers run GLM through pi, the harness the "+
				"operator's 2026-09-22 decision names", role, p.Runner)
		}
		if p.Model != cloudGLM53 {
			t.Errorf("%s names model %q, want %q", role, p.Model, cloudGLM53)
		}
		if len(p.Prompt) < 100 || !strings.Contains(p.Prompt, role) {
			t.Errorf("%s carries a %d-byte prompt that does not name the role", role, len(p.Prompt))
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

// No profile the container can select starts a claude process — njj's third
// acceptance item, at the level this tick owns: the FILES of the set the
// container is pointed at. A claude process in the cloud is the thing the
// whole epic exists to prevent (xte: "don't use claude on the orchestrator"),
// so every role in the set — review and closeout included, which is where a
// claude profile would otherwise hide — names pi.
//
// The roles-table routing that could still route a role back onto claude is
// substrate-blind today and is tick 84z's to make substrate-aware; what this
// test pins is that the set itself names no claude runner for any role to
// fall back into.
//
// short: reads of this repository's own profile files; no I/O.
func TestNoProfileTheCloudContainerSelectsStartsAClaudeProcess(t *testing.T) {
	dir := cloudProfileDir(t)
	for _, role := range Roles {
		p, err := Resolve(role, Options{Dir: dir})
		if err != nil {
			t.Fatalf("%s did not resolve from the cloud set: %v", role, err)
		}
		if p.Runner == "claude" {
			t.Errorf("%s names runner claude: a cloud dispatch of it would start the one process "+
				"this epic exists to keep out of the container", role)
		}
		if p.Runner != "pi" {
			t.Errorf("%s names runner %q: the operator's constraint is that cloud workers run via pi, "+
				"so a profile naming any other runner is one no cloud run should select", role, p.Runner)
		}
	}
}

// Both GLM models are nameable as a worker model — njj's second acceptance
// item, and the half that is about the SHAPE rather than the values. A profile
// file is exactly {executor, runner, model, prompt} — a fifth field is refused
// (TestAProfileFileWithAFifthFieldIsRefused) — so the profile cannot itself
// carry a second model, and the place the shape already allows many models per
// role is the [roles.*.tiers.*] overlay of the target repository's
// runners.toml, read here exactly the way `tk herd` reads it.
//
// That overlay is where wne's work-type classifier will land a model: wne maps
// a kind of work onto a tier, the tier names the model, and the profile
// resolves through both. This test proves both GLM spellings have somewhere to
// land in THIS repository's own declared routing — glm-5.3 as the role's own
// base and its strong tier, glm-5.3-flash as the economy tier — so the
// classifier's table is not waiting on profile machinery.
//
// short: reads of this repository's own profile files and runners.toml; no I/O.
func TestBothGLMModelsAreNameableAsAWorkerModel(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(root, ".tick", "runners.toml")
	dir := cloudProfileDir(t)

	// The role's own base values, as this repository declares them: GLM 5.3.
	base, err := Resolve("implement-tick", Options{Dir: dir, RunnersConfig: gate})
	if err != nil {
		t.Fatalf("implement-tick did not resolve through this repository's roles table: %v", err)
	}
	if base.Runner != "pi" || base.Model != cloudGLM53 {
		t.Errorf("the base resolution is %s/%s, want pi/%s: .tick/runners.toml's [roles.implement] "+
			"states the repo's own intent — implementation runs entirely on GLM on cloudflare through pi",
			base.Runner, base.Model, cloudGLM53)
	}

	strong, err := Resolve("implement-tick", Options{Dir: dir, RunnersConfig: gate, Tier: "strong"})
	if err != nil {
		t.Fatalf("the strong tier did not resolve: %v", err)
	}
	if strong.Model != cloudGLM53 {
		t.Errorf("the strong tier names %q, want %q", strong.Model, cloudGLM53)
	}

	// The economy tier: GLM 5.3 Flash, the simple-work model. NAMEABLE is the
	// claim — the overlay resolves it, the profile digest distinguishes it,
	// and a record can therefore cite which of the two a dispatch ran on.
	economy, err := Resolve("implement-tick", Options{Dir: dir, RunnersConfig: gate, Tier: "economy"})
	if err != nil {
		t.Fatalf("the economy tier did not resolve: %v", err)
	}
	if economy.Runner != "pi" {
		t.Errorf("the economy tier runs on %q, want pi: a cheaper model is not a different harness", economy.Runner)
	}
	if economy.Model != cloudGLM53Flash {
		t.Errorf("the economy tier names %q, want %q", economy.Model, cloudGLM53Flash)
	}
	if economy.Digest == base.Digest {
		t.Error("the flash tier digests the same as the base model: two worker models a record cannot " +
			"tell apart is a tier whose routing nothing can audit")
	}
	if !strings.Contains(economy.Routed, "tiers.economy") {
		t.Errorf("the flash resolution does not name where its model came from: %q", economy.Routed)
	}
}
