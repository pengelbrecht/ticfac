package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The reconciler's construction under a named run config (tick tda): the
// selection is a construction fact — resolved once in New, before anything is
// dispatched — and the reconciler a run actually builds routes every role on
// the config the epic chose (or the flag overrode, or the default named).
// This is the production entry point's own path: profiles, substrate, gate,
// rule and surface, exactly what `ticfac run-epic` assembles.

// epicConfigFakeTracker answers the epic with the labels it was given, the
// one fact the selection reads through the tracker.
type epicConfigFakeTracker struct{ labels []string }

func (f *epicConfigFakeTracker) Graph(context.Context, string) (tk.Graph, error) {
	return tk.Graph{}, nil
}
func (f *epicConfigFakeTracker) Show(_ context.Context, id string) (tk.Tick, error) {
	return tk.Tick{ID: id, Labels: f.labels}, nil
}
func (f *epicConfigFakeTracker) Claim(context.Context, string, string) (tk.Tick, error) {
	return tk.Tick{}, nil
}
func (f *epicConfigFakeTracker) Note(context.Context, string, string) (tk.Tick, error) {
	return tk.Tick{}, nil
}
func (f *epicConfigFakeTracker) Close(context.Context, string) (tk.Tick, error) {
	return tk.Tick{}, nil
}

// writeNamedConfigRepo builds the fixture repository of tick tda's
// acceptance: a common runners file and a cloud one declaring the two named
// cloud configs — glm (the default, Workers AI) and claude (the subscription
// rung: implement sonnet → opus, review and close-out on opus, the ladder the
// operator named 2026-10-06).
func writeNamedConfigRepo(t *testing.T) string {
	t.Helper()
	const common = `version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[testing.commands]
go = { command = "go test ./..." }
`
	const cloud = `version = 2

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

[configs.glm.roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review.tiers.strong]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

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

[configs.claude.roles.closeout]
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

[roles.implement.tiers.strong]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

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
	repo := initFixture(t, nil)
	tick := filepath.Join(repo, ".tick")
	if err := os.MkdirAll(tick, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tick, "runners.toml"), []byte(common), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tick, "runners.cloud.toml"), []byte(cloud), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// newOnFixture builds the reconciler the production entry point builds, on the
// fixture repository, on the cloud substrate the named configs live for.
func newOnFixture(t *testing.T, repo string, opts reconcile.Options) (*reconcile.Reconciler, error) {
	t.Helper()
	t.Setenv(runconfig.SubstrateEnvVar, "cloud")
	if opts.GateConfig == "" {
		opts.GateConfig = initRunnersPath(repo)
	}
	if opts.ProfileDir == "" {
		opts.ProfileDir = profile.EmbeddedCloud
	}
	if opts.NewExecutor == nil {
		opts.NewExecutor = executorFactory("pi", opts.GateConfig)
	}
	if opts.Executors == nil {
		opts.Executors = knownExecutors()
	}
	if opts.PullRequests == nil {
		opts.PullRequests = forge.GitHub{Token: "fixture", Repo: "example/name"}
	}
	if opts.Repo == "" {
		opts.Repo = repo
	}
	if opts.EpicID == "" {
		opts.EpicID = "e1"
	}
	if opts.Tracker == nil {
		opts.Tracker = &epicConfigFakeTracker{}
	}
	return reconcile.New(opts)
}

// TestTheEpicConfigLabelRunsTheReconcilerOnTheClaudeConfig is the acceptance's
// own sentence: an epic declaring `config: claude` runs its workers on the
// claude-sub rung — implement on sonnet, climbing to opus; review and
// close-out on opus — and the reconciler a run builds says which config it
// selected and where the choice came from.
func TestTheEpicConfigLabelRunsTheReconcilerOnTheClaudeConfig(t *testing.T) {
	repo := writeNamedConfigRepo(t)
	r, err := newOnFixture(t, repo, reconcile.Options{Tracker: &epicConfigFakeTracker{labels: []string{"config: claude"}}})
	if err != nil {
		t.Fatalf("the reconciler refuses an epic on the claude config: %v", err)
	}
	sel := r.RunConfig()
	if sel.Name != "claude" || !strings.Contains(sel.Source, "config: label") {
		t.Fatalf("the reconciler selected %+v, want claude by the epic's label", sel)
	}
	p := r.Profiles()["implement-tick"]
	if p == nil {
		t.Fatal("the reconciler resolved no implement profile")
	}
	if p.Runner != "claude" || p.Model != "sonnet" {
		t.Errorf("implement routes to %s/%s, want claude/sonnet on the subscription rung", p.Runner, p.Model)
	}
	for _, role := range []string{"review-epic", "closeout-epic"} {
		p := r.Profiles()[role]
		if p == nil {
			t.Fatalf("the reconciler resolved no %s profile", role)
		}
		if p.Runner != "claude" || p.Model != "opus" {
			t.Errorf("%s routes to %s/%s, want claude/opus", role, p.Runner, p.Model)
		}
	}
}

// TestTheConfigFlagOverridesTheEpicsChoice: --config beats the label — a run
// diagnosing a config's behaviour wants the other config without editing the
// tracker.
func TestTheConfigFlagOverridesTheEpicsChoice(t *testing.T) {
	repo := writeNamedConfigRepo(t)
	r, err := newOnFixture(t, repo, reconcile.Options{
		RunConfig: "glm",
		Tracker:   &epicConfigFakeTracker{labels: []string{"config: claude"}},
	})
	if err != nil {
		t.Fatalf("the reconciler refuses the flag's override: %v", err)
	}
	sel := r.RunConfig()
	if sel.Name != "glm" || sel.Source != "the --config flag" {
		t.Fatalf("the reconciler selected %+v, want glm by the flag over the label", sel)
	}
	p := r.Profiles()["implement-tick"]
	// The durable harness under its HOSTED name (tick twa): a runner table's
	// "pi" binds to what the sandbox image boots it by.
	if p.Runner != profile.HostedDurableHarness || p.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3" {
		t.Errorf("implement routes to %s/%s, want the glm config's hosted durable/GLM", p.Runner, p.Model)
	}
}

// TestAnotherEpicRunsOnTheDefaultConfig: with no label and no flag, the
// declared default answers — another epic on the same repository runs on
// GLM, unchanged.
func TestAnotherEpicRunsOnTheDefaultConfig(t *testing.T) {
	repo := writeNamedConfigRepo(t)
	r, err := newOnFixture(t, repo, reconcile.Options{})
	if err != nil {
		t.Fatalf("the reconciler refuses the default config: %v", err)
	}
	sel := r.RunConfig()
	if sel.Name != "glm" || sel.Source != "the [configs] default" {
		t.Fatalf("the reconciler selected %+v, want glm by the declared default", sel)
	}
	p := r.Profiles()["implement-tick"]
	if p.Runner != profile.HostedDurableHarness {
		t.Errorf("implement routes to %s, want the hosted durable harness on the default config", p.Runner)
	}
}

// TestConstructionRefusesAConfigThatCannotServeItsRung: a claude config whose
// factory holds no subscription token is refused at construction, naming the
// fix — the run would silently step every dispatch down to Workers AI.
func TestConstructionRefusesAConfigThatCannotServeItsRung(t *testing.T) {
	repo := writeNamedConfigRepo(t)
	_, err := newOnFixture(t, repo, reconcile.Options{
		Tracker:            &epicConfigFakeTracker{labels: []string{"config: claude"}},
		SubscriptionTokens: func() ([]string, error) { return nil, nil },
	})
	if err == nil {
		t.Fatal("a claude config with no subscription configured constructed; it would silently step every dispatch down to Workers AI")
	}
	if !strings.Contains(err.Error(), "no subscription is configured") || !strings.Contains(err.Error(), "CLAUDE_SUB_TOKEN") {
		t.Errorf("the refusal does not name the fix: %v", err)
	}
}

// TestConstructionRefusesAConfigTheRepositoryNeverDeclared: the flag and the
// label are both words about this repository, and a word naming a config
// nobody declared is refused naming the declared ones.
func TestConstructionRefusesAConfigTheRepositoryNeverDeclared(t *testing.T) {
	repo := writeNamedConfigRepo(t)
	_, err := newOnFixture(t, repo, reconcile.Options{
		RunConfig: "codex",
	})
	if err == nil || !strings.Contains(err.Error(), `"codex" is not one the runners files declare`) {
		t.Errorf("a flag naming an undeclared config: %v", err)
	}
	_, err = newOnFixture(t, repo, reconcile.Options{
		Tracker: &epicConfigFakeTracker{labels: []string{"config: sonnet"}},
	})
	if err == nil || !strings.Contains(err.Error(), `"sonnet" is not one the runners files declare`) {
		t.Errorf("a label naming an undeclared config: %v", err)
	}
}
