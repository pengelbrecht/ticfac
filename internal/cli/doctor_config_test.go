package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Doctor's named-config half (tick tda): a repository may declare more than
// one complete routing — one epic on GLM, another on claude through the
// operator's subscription — and doctor resolves EVERY named config so a
// config that cannot route is a missing line here, naming the config,
// rather than a run that stops on it three ticks in. The one refusal the
// acceptance names specially: a claude config with no subscription token
// configured, which a run would silently step every dispatch down to
// Workers AI.

// doctorConfiguredCloudOverlay is the cloud overlay the doctor fixture
// carries: the file's own cells (Workers AI, the durable harness) and the
// two named configs beside them — glm, the declared default, and claude, the
// subscription rung (implement sonnet → opus, review and close-out opus, the
// ladder the operator named 2026-10-06).
const doctorConfiguredCloudOverlay = `version = 2

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

// doctorConfigFixture is the cloud doctor fixture with the named configs.
func doctorConfigFixture(t *testing.T) string {
	t.Helper()
	repo := doctorFixture(t, true)
	cloudFile := filepath.Join(repo, ".tick", "runners.cloud.toml")
	if err := os.WriteFile(cloudFile, []byte(doctorConfiguredCloudOverlay), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

// TestDoctorNamesEveryNamedConfig: the routing line resolves every declared
// config — each on its own cells — and the passing detail NAMES them, so an
// operator can see from one doctor run that one epic can run on GLM and
// another on claude from this repository.
func TestDoctorNamesEveryNamedConfig(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := doctorConfigFixture(t)

	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitSuccess {
		t.Fatalf("doctor over a repository whose every config routes exits %d, want %d:\n%s", code, exitGeneric, stdout)
	}
	for _, want := range []string{
		"config claude", // the named config, in the routing detail
		"config glm",
		"every role job routes",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
}

// TestDoctorRefusesAClaudeConfigWithNoSubscription is the acceptance's own
// refusal: a cloud config riding the claude-sub rung and a factory holding
// no subscription token — the rung is off, and a run on that config would
// silently step every dispatch down to Workers AI. Doctor says so as a
// missing routing line with the `wrangler secret put` fix, and exits 1.
func TestDoctorRefusesAClaudeConfigWithNoSubscription(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := doctorConfigFixture(t)
	// The factory answers: no subscription token configured. The field is
	// present and empty — the rung is off — never absent, which is a factory
	// that predates the field.
	doctorClaudeSubLabels = func() ([]string, error) { return []string{}, nil }

	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitGeneric {
		t.Fatalf("doctor over a claude config with the rung off exits %d, want %d:\n%s", code, exitGeneric, stdout)
	}
	for _, want := range []string{
		"missing  routing",
		"config claude rides the claude-sub subscription rung",
		"implement-tick (tier economy)",
		"no subscription token (CLAUDE_SUB_TOKEN_<LABEL>)",
		"silently step every dispatch down to Workers AI",
		"fix: put the rung on: `wrangler secret put CLAUDE_SUB_TOKEN_<LABEL>`",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
}

// TestDoctorLeavesTheConfigQuestionToTheFactoryLineWhenItCannotAsk: a factory
// that cannot be asked is not a fact about the config — the routing itself
// resolved, the factory line carries the unreachable factory, and the
// config's question is asked where the live answer is (the cloud submission
// preflight). Doctor exits by its other lines, not by pretending the rung
// question was answered.
func TestDoctorLeavesTheConfigQuestionToTheFactoryLineWhenItCannotAsk(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := doctorConfigFixture(t)
	doctorClaudeSubLabels = func() ([]string, error) { return nil, errors.New("the factory did not answer") }

	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitSuccess {
		t.Fatalf("doctor over a claude config with an unanswerable factory exits %d, want %d:\n%s", code, exitSuccess, stdout)
	}
	if !strings.Contains(stdout, "every role job routes") {
		t.Errorf("the routing line does not pass:\n%s", stdout)
	}
	if strings.Contains(stdout, "no subscription token") {
		t.Errorf("the routing line reports a subscription it could not read:\n%s", stdout)
	}
}

// TestDoctorRefusesAConfigThatCannotRoute: a named config with broken cells —
// the rung's harness on a PINNED model id, which bills per token in the cloud
// exactly as firmly as claude on any other harness — is a missing routing
// line naming the CONFIG, with the same refusal a run's own start would give.
func TestDoctorRefusesAConfigThatCannotRoute(t *testing.T) {
	saveDoctorSeams(t, "", false)
	repo := doctorConfigFixture(t)
	overlay := strings.Replace(doctorConfiguredCloudOverlay,
		`[configs.claude.roles.review]
kind = "claude"
model = "opus"`,
		`[configs.claude.roles.review]
kind = "claude"
model = "claude-sonnet-5-5"`, 1)
	if overlay == doctorConfiguredCloudOverlay {
		t.Fatal("the fixture's review cell was not replaced — the test would prove nothing")
	}
	if err := os.WriteFile(filepath.Join(repo, ".tick", "runners.cloud.toml"), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := runDoctorOn(t, repo, false)
	if code != exitGeneric {
		t.Fatalf("doctor over a config that cannot route exits %d, want %d:\n%s", code, exitGeneric, stdout)
	}
	for _, want := range []string{
		"missing  routing",
		`the named run config "claude" cannot route`,
		"pinned model id bills per token",
		"fix: " + doctorFixRouting,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
	}
}
