package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The run's config selection (tick tda): which named run config a run routes
// under, resolved once at construction — the operator's --config flag over
// the epic's own `config:` label over the declared default — and the
// preflights that refuse a config that cannot route, including one whose
// workers ride a subscription rung no factory holds a token for.

// selectFixture is a target repository whose cloud file declares the two
// named configs of the acceptance: glm (the default, Workers AI) and claude
// (the subscription rung).
const selectCommon = `version = 2

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

const selectCloud = `version = 2

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

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

func writeSelectRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tick", "runners.toml"), []byte(selectCommon), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tick", "runners.cloud.toml"), []byte(selectCloud), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, ".tick", "runners.toml")
}

// short: pure helpers over temp config files — no harness, no git.
func TestSelectRunConfigAnswersThePrecedence(t *testing.T) {
	t.Parallel()
	gate := writeSelectRepo(t)
	opts := func(flag string) Options {
		return Options{GateConfig: gate, EpicID: "v5t", RunConfig: flag}
	}

	// The flag over the epic's label: a run diagnosing a config's behaviour
	// wants the other config without editing the tracker.
	got, err := selectRunConfig(opts("claude"), runconfig.SubstrateCloud, []string{"config: glm"}, "")
	if err != nil {
		t.Fatalf("the flag's selection refused: %v", err)
	}
	if got.Name != "claude" || got.Source != "the --config flag" {
		t.Errorf("the flag selected %+v, want claude by the flag", got)
	}

	// The epic's own label, when no flag speaks.
	got, err = selectRunConfig(opts(""), runconfig.SubstrateCloud, []string{"config: claude", "tier: strong"}, "")
	if err != nil {
		t.Fatalf("the label's selection refused: %v", err)
	}
	if got.Name != "claude" || !strings.Contains(got.Source, "config: label") {
		t.Errorf("the label selected %+v, want claude by the epic's label", got)
	}

	// The declared default, when neither the flag nor the label speaks.
	got, err = selectRunConfig(opts(""), runconfig.SubstrateCloud, nil, "")
	if err != nil {
		t.Fatalf("the default's selection refused: %v", err)
	}
	if got.Name != "glm" || got.Source != "the [configs] default" {
		t.Errorf("nothing speaking selected %+v, want glm by the default", got)
	}
	if !got.Selected() {
		t.Error("the default is not a selection: a config was chosen")
	}
}

// short: pure helpers over temp config files — no harness, no git.
func TestSelectRunConfigRefusesWhatNobodyDeclared(t *testing.T) {
	t.Parallel()
	gate := writeSelectRepo(t)

	// A flag naming a config nobody declared.
	_, err := selectRunConfig(Options{GateConfig: gate, EpicID: "v5t", RunConfig: "opus-max"},
		runconfig.SubstrateCloud, nil, "")
	if err == nil || !strings.Contains(err.Error(), `"opus-max" is not one the runners files declare`) {
		t.Errorf("a flag naming an undeclared config: %v", err)
	}

	// A label naming one.
	_, err = selectRunConfig(Options{GateConfig: gate, EpicID: "v5t"},
		runconfig.SubstrateCloud, []string{"config: sonnet"}, "")
	if err == nil || !strings.Contains(err.Error(), `config: label`) {
		t.Errorf("a label naming an undeclared config: %v", err)
	}

	// Two labels that disagree: one epic has one config, and a run that had
	// to guess which would be a run guessing at what the whole epic runs on.
	_, err = selectRunConfig(Options{GateConfig: gate, EpicID: "v5t"},
		runconfig.SubstrateCloud, []string{"config: glm", "config: claude"}, "")
	if err == nil || !strings.Contains(err.Error(), "one epic has one config") {
		t.Errorf("two disagreeing config labels: %v", err)
	}
}

// short: pure helpers over temp config files — no harness, no git.
func TestARepositoryWithoutNamedConfigsSelectsNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gate := filepath.Join(dir, "runners.toml")
	if err := os.WriteFile(gate, []byte("version = 2\n\n[roles.implement]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Nothing selecting: the historical single-routing run, exactly as before
	// tick tda — no config, no source, no error.
	got, err := selectRunConfig(Options{GateConfig: gate, EpicID: "v5t"}, runconfig.SubstrateCloud, nil, "")
	if err != nil {
		t.Fatalf("a repository without named configs refused: %v", err)
	}
	if got.Selected() {
		t.Errorf("a repository without named configs selected %+v", got)
	}
	if got.Detail() != "" {
		t.Errorf("a repository without named configs says %q", got.Detail())
	}

	// A flag against a repository that declares none: the refusal names the
	// declaration to make, not a silent run on the file's own cells.
	_, err = selectRunConfig(Options{GateConfig: gate, EpicID: "v5t", RunConfig: "claude"}, runconfig.SubstrateCloud, nil, "")
	if err == nil || !strings.Contains(err.Error(), "declare no named configs at all") {
		t.Errorf("a flag against a repository without configs: %v", err)
	}

	// And the same for the epic's label, in the cloud: the label is the only
	// word a submitted run carries, and one that asked for claude must not
	// silently get the file's own cells.
	_, err = selectRunConfig(Options{GateConfig: gate, EpicID: "v5t"}, runconfig.SubstrateCloud, []string{"config: claude"}, "")
	if err == nil || !strings.Contains(err.Error(), "declare no named configs at all") {
		t.Errorf("a label against a cloud without configs: %v", err)
	}
}

// TestALocalRunOfACloudConfiguredEpicIsNotRefused: the configs live in
// .tick/runners.cloud.toml, which a local run never reads, so an epic
// designed with `config: claude` for the cloud runs locally on the local
// files' own cells — saying, in its feed, that the label was not acted on —
// rather than being refused at construction. The flag stays a refusal: it
// is the operator's word for THIS run.
//
// short: pure helpers over temp config files — no harness, no git.
func TestALocalRunOfACloudConfiguredEpicIsNotRefused(t *testing.T) {
	t.Parallel()
	gate := writeSelectRepo(t)
	for _, sub := range []runconfig.Substrate{runconfig.SubstrateHerdr, runconfig.SubstrateHarness} {
		got, err := selectRunConfig(Options{GateConfig: gate, EpicID: "v5t"}, sub, []string{"config: claude"}, "")
		if err != nil {
			t.Fatalf("%s: a local run of a config: claude epic was refused: %v", sub, err)
		}
		if got.Selected() {
			t.Errorf("%s: a local run selected %+v from configs only the cloud declares", sub, got)
		}
		for _, want := range []string{"none — ", "config:claude", "not acted on"} {
			if !strings.Contains(got.Detail(), want) {
				t.Errorf("%s: the feed line %q does not say %q", sub, got.Detail(), want)
			}
		}
		if RunConfigFromDetail(got.Detail()) != "" {
			t.Errorf("%s: the note %q parses as a config name", sub, got.Detail())
		}
		_, err = selectRunConfig(Options{GateConfig: gate, EpicID: "v5t", RunConfig: "claude"}, sub, nil, "")
		if err == nil || !strings.Contains(err.Error(), "declare no named configs at all") {
			t.Errorf("%s: a --config flag on a substrate without configs: %v", sub, err)
		}
	}
}

// TestAResumeKeepsTheConfigItsRunStartedOn: one run is one config. A resume
// with no flag keeps the config the run's first incarnation recorded,
// whatever the label or the default now say; a resume whose flag names
// another config is refused, naming a new run id as the way to get it.
//
// short: pure helpers over temp config files and a temp feed — no harness, no git.
func TestAResumeKeepsTheConfigItsRunStartedOn(t *testing.T) {
	t.Parallel()
	gate := writeSelectRepo(t)
	opts := Options{GateConfig: gate, EpicID: "v5t", RunID: "epic-v5t"}

	got, err := selectRunConfig(opts, runconfig.SubstrateCloud, []string{"config: glm"}, "claude")
	if err != nil {
		t.Fatalf("a resume of a claude run refused: %v", err)
	}
	if got.Name != "claude" || !strings.Contains(got.Source, "first selection") {
		t.Errorf("a resume of a claude run with a glm label selected %+v, want claude kept", got)
	}

	opts.RunConfig = "claude"
	if got, err := selectRunConfig(opts, runconfig.SubstrateCloud, nil, "claude"); err != nil || got.Name != "claude" {
		t.Errorf("a resume repeating its own --config: %+v, %v", got, err)
	}

	opts.RunConfig = "glm"
	_, err = selectRunConfig(opts, runconfig.SubstrateCloud, nil, "claude")
	if err == nil || !strings.Contains(err.Error(), "one run is one config") || !strings.Contains(err.Error(), "--run-id") {
		t.Errorf("a resume whose flag switches config: %v", err)
	}

	opts.RunConfig = ""
	_, err = selectRunConfig(opts, runconfig.SubstrateCloud, nil, "opus-max")
	if err == nil || !strings.Contains(err.Error(), "no longer declare") {
		t.Errorf("a resume of a config the files no longer declare: %v", err)
	}

	// The recorded selection is read from the run's own feed: the LAST
	// run-level config_selected line, and nothing from a "none — " note.
	repo := t.TempDir()
	if got := recordedRunConfig(repo, "epic-v5t"); got != "" {
		t.Errorf("a run with no feed recorded %q", got)
	}
	feed := runfeed.Open(repo, "epic-v5t")
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	for _, detail := range []string{
		RunConfigSelection{Name: "glm", Source: "the [configs] default"}.Detail(),
		RunConfigSelection{Name: "claude", Source: "the --config flag"}.Detail(),
	} {
		if err := feed.Append(runfeed.NewEvent(at, "epic-v5t", "", nil, StageConfigSelected, detail)); err != nil {
			t.Fatal(err)
		}
	}
	if got := recordedRunConfig(repo, "epic-v5t"); got != "claude" {
		t.Errorf("the recorded config is %q, want the last line's claude", got)
	}
}

// TestALocalRunIsNeverAskedForAFactorySubscription: the rung is the
// factory's subscription lease, a cloud question. A local run's claude cells
// run on the operator's own CLI login, so the run-start check asks nothing
// of a local run — not even the network read.
//
// short: pure helpers over pre-resolved profiles — no harness, no factory.
func TestALocalRunIsNeverAskedForAFactorySubscription(t *testing.T) {
	t.Parallel()
	gate := writeSelectRepo(t)
	jobs, err := CheckRoutingConfig(profile.EmbeddedCloud, gate, runconfig.SubstrateCloud, "claude")
	if err != nil {
		t.Fatal(err)
	}
	asked := false
	opts := Options{SubscriptionTokens: func() ([]string, error) { asked = true; return nil, nil }}
	for _, sub := range []runconfig.Substrate{runconfig.SubstrateHerdr, runconfig.SubstrateHarness} {
		if err := checkSelectedConfigCanRoute(opts, sub, RunConfigSelection{Name: "claude"}, jobs); err != nil {
			t.Errorf("%s: a local run was refused for a factory subscription: %v", sub, err)
		}
	}
	if asked {
		t.Error("a local run asked the factory for its subscription labels")
	}
	if err := checkSelectedConfigCanRoute(opts, runconfig.SubstrateCloud, RunConfigSelection{Name: "claude"}, jobs); err == nil {
		t.Error("the cloud run on the same jobs was not refused")
	}
}

// TestAClaudeConfigWithoutASubscriptionIsRefusedAtRunStart is the preflight's
// own sentence from the tick: a claude config with no subscription token
// configured cannot route — the run would silently step every dispatch down
// to Workers AI, which is exactly the "paid for X and got Y" failure the
// rung's fallback (all leases busy) exists not to be confused with.
//
// short: pure helpers over pre-resolved profiles — no harness, no factory.
func TestAClaudeConfigWithoutASubscriptionIsRefusedAtRunStart(t *testing.T) {
	t.Parallel()
	gate := writeSelectRepo(t)
	jobs, err := CheckRoutingConfig(profile.EmbeddedCloud, gate, runconfig.SubstrateCloud, "claude")
	if err != nil {
		t.Fatalf("the claude config's jobs do not resolve: %v", err)
	}
	selection := RunConfigSelection{Name: "claude", Source: "the --config flag"}

	// No seam wired (a build that cannot ask the factory): the check leaves
	// the question to the surfaces that can ask, and refuses nothing.
	if err := checkSelectedConfigCanRoute(Options{}, runconfig.SubstrateCloud, selection, jobs); err != nil {
		t.Errorf("a nil subscription seam refused the run: %v", err)
	}
	// A factory that cannot be asked is not a fact about the config.
	if err := checkSelectedConfigCanRoute(Options{SubscriptionTokens: func() ([]string, error) {
		return nil, errors.New("the factory did not answer")
	}}, runconfig.SubstrateCloud, selection, jobs); err != nil {
		t.Errorf("an unanswerable factory refused the run: %v", err)
	}
	// No subscription configured: the refusal names the config, the rung and
	// the fix.
	err = checkSelectedConfigCanRoute(Options{SubscriptionTokens: func() ([]string, error) {
		return nil, nil
	}}, runconfig.SubstrateCloud, selection, jobs)
	if err == nil {
		t.Fatalf("a claude config with no subscription configured passed the preflight")
	}
	for _, want := range []string{"\"claude\"", "subscription rung", "no subscription is configured", "CLAUDE_SUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	// A subscription configured: the rung is on, and the preflight passes.
	if err := checkSelectedConfigCanRoute(Options{SubscriptionTokens: func() ([]string, error) {
		return []string{"max"}, nil
	}}, runconfig.SubstrateCloud, selection, jobs); err != nil {
		t.Errorf("a claude config with a subscription configured was refused: %v", err)
	}
	// The glm config rides no rung: no subscription needed, no question asked.
	glmJobs, err := CheckRoutingConfig(profile.EmbeddedCloud, gate, runconfig.SubstrateCloud, "glm")
	if err != nil {
		t.Fatalf("the glm config's jobs do not resolve: %v", err)
	}
	if err := checkSelectedConfigCanRoute(Options{SubscriptionTokens: func() ([]string, error) {
		return nil, nil
	}}, runconfig.SubstrateCloud, RunConfigSelection{Name: "glm"}, glmJobs); err != nil {
		t.Errorf("the glm config was refused for a subscription it does not ride: %v", err)
	}
	// And a run that selected nothing is not asked at all.
	if err := checkSelectedConfigCanRoute(Options{SubscriptionTokens: func() ([]string, error) {
		return nil, nil
	}}, runconfig.SubstrateCloud, RunConfigSelection{}, jobs); err != nil {
		t.Errorf("a no-selection run was refused by the config preflight: %v", err)
	}
}

// TestCheckEveryNamedConfigResolvesEachConfig is doctor's and the cloud
// submission preflight's question: EVERY named config routes, each on its
// own cells — the claude config's workers on the subscription rung, the glm
// config's on Workers AI — so a broken config is a missing line in the report
// rather than a run that stops on it three ticks in.
//
// short: resolves profiles in memory over temp config files; no harness, no git.
func TestCheckEveryNamedConfigResolvesEachConfig(t *testing.T) {
	t.Parallel()
	gate := writeSelectRepo(t)
	counts, jobs, err := CheckEveryNamedConfig(profile.EmbeddedCloud, gate, runconfig.SubstrateCloud)
	if err != nil {
		t.Fatalf("every named config did not resolve: %v", err)
	}
	if counts["glm"] == 0 || counts["claude"] == 0 {
		t.Errorf("the per-config counts are %v, want jobs under both", counts)
	}
	seenRung, seenWorkersAI := false, false
	for _, job := range jobs {
		if job.Config == "" {
			t.Errorf("a job from the every-config check carries no config: %s at %q", job.Role, job.Tier)
		}
		if _, rung := profile.SubscriptionRungFor(job.Profile.Runner, job.Profile.Model); rung {
			seenRung = true
			if job.Config != "claude" {
				t.Errorf("the %s config rides the subscription rung (%s/%s)", job.Config, job.Profile.Runner, job.Profile.Model)
			}
		} else if !profile.IsWorkersAIModel(job.Profile.Model) {
			t.Errorf("%s at tier %q under %s resolves to %s/%s, which is neither Workers AI nor a rung",
				job.Role, job.Tier, job.Config, job.Profile.Runner, job.Profile.Model)
		} else {
			seenWorkersAI = true
		}
	}
	if !seenRung || !seenWorkersAI {
		t.Errorf("the check saw rung jobs (%v) and Workers AI jobs (%v), want both", seenRung, seenWorkersAI)
	}

	// A config that cannot route refuses the whole call naming the config.
	dir := t.TempDir()
	broken := filepath.Join(dir, "runners.toml")
	if err := os.WriteFile(broken, []byte(`version = 2

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.claude.roles.implement]
kind = "claude"
model = "sonnet"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = CheckEveryNamedConfig(profile.EmbeddedCloud, broken, runconfig.SubstrateCloud)
	if err == nil || !strings.Contains(err.Error(), `the named run config "claude" cannot route`) {
		t.Errorf("a config that cannot route: %v", err)
	}

	// A repository that declares none answers empty: doctor's own other
	// line — the no-selection view — is unchanged.
	none := t.TempDir()
	plainGate := filepath.Join(none, "runners.toml")
	if err := os.WriteFile(plainGate, []byte("version = 2\n\n[roles.implement]\nkind = \"pi\"\nmodel = \"cloudflare-workers-ai/@cf/zai-org/glm-5.3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plainCounts, plainJobs, err := CheckEveryNamedConfig(profile.EmbeddedCloud, plainGate, runconfig.SubstrateHerdr)
	if err != nil {
		t.Fatalf("a repository that declares no configs refused: %v", err)
	}
	if len(plainCounts) != 0 || len(plainJobs) != 0 {
		t.Errorf("a repository with no configs answered %d configs, %d jobs", len(plainCounts), len(plainJobs))
	}
}
