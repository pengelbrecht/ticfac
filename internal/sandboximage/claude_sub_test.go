//go:build !windows

package sandboximage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The claude-sub route through the image (tick 6fv): a job the control plane
// marked TICKS_CLAUDE_SUB=1 and handed the placeholder OAuth token runs its
// claude CLI against the operator's Claude SUBSCRIPTION — the Worker
// intercepts api.anthropic.com and swaps the placeholder for the
// subscription's token, which never enters this container.
//
// What the route exists to prevent is the failure shape a claude harness
// otherwise has here: common.sh's gateway wiring exports
// ANTHROPIC_BASE_URL/ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN, and an API key
// wins over OAuth — so the very wiring that makes a per-token claude boot work
// would turn a claude-sub job into per-token spend on the vendor, silently.
// The tests pin the absences: no Anthropic credential, no gateway base URL,
// no probe against a gateway route that serves no alias — and the refusal when
// the rung's own rule is violated from inside the container.

const claudeSubPlaceholder = "ticfac-claude-sub-placeholder-not-a-token"

// claudeSubFixture is a worker fixture with the claude-sub boot's environment:
// the marker, the placeholder, the interception's CA, and the alias the
// factory's routing selected.
func claudeSubFixture(t *testing.T) *workerFixture {
	t.Helper()
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.env[EnvHarness] = "claude"
	f.env[EnvModel] = "opus"
	f.env["TICKS_CLAUDE_SUB"] = "1"
	f.env["CLAUDE_CODE_OAUTH_TOKEN"] = claudeSubPlaceholder
	f.env["NODE_EXTRA_CA_CERTS"] = "/etc/cloudflare/certs/cloudflare-containers-ca.crt"
	f.env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	f.env["TICKS_TEST_WORKER_ENV_RECORD"] = filepath.Join(f.root, "claude-env")
	return f
}

// harnessEnv reads the environment the entrypoint handed the claude CLI.
func (f *workerFixture) harnessEnv(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.root, "claude-env"))
	if err != nil {
		t.Fatalf("the claude stub never ran (no environment record): %v", err)
	}
	return string(body)
}

// A claude-sub boot runs the harness with the placeholder and NONE of the
// gateway's Anthropic wiring: no API key (an API key wins over OAuth and is
// per-token spend, the exact thing the rung exists not to do), no base URL
// (the gateway serves no subscription alias), no auth token (the gateway token
// is not a subscription credential). The job then completes like any other.
func TestAClaudeSubBootHandsTheHarnessOnlyThePlaceholder(t *testing.T) {
	f := claudeSubFixture(t)

	out, code := f.run()
	if code != 0 {
		t.Fatalf("a healthy claude-sub worker exited %d:\n%s", code, out)
	}
	env := f.harnessEnv(t)
	// export -p prints one `declare -x NAME="value"` line per variable, so the
	// assertions match on the NAME=" part and never on a value's shape.
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"} {
		if strings.Contains(env, `declare -x `+banned+`=`) || strings.Contains(env, banned+"=") {
			t.Errorf("the claude CLI was handed %s: on a claude-sub job no Anthropic variable may be set — an API key wins over the OAuth placeholder and bills per token.\n%s",
				banned, env)
		}
	}
	for _, want := range []string{
		`CLAUDE_CODE_OAUTH_TOKEN="` + claudeSubPlaceholder,
		`NODE_EXTRA_CA_CERTS="`,
		`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC="1"`,
	} {
		if !strings.Contains(env, want) {
			t.Errorf("the claude CLI was not handed %s:\n%s", want, env)
		}
	}
	// The token never in the container also means never in its environment.
	if strings.Contains(env, "sk-ant-oat") {
		t.Errorf("a real subscription token reached the container:\n%s", env)
	}
	// The model is the ALIAS, verbatim: the CLI resolves it to the latest
	// model the pinned CLI knows.
	if !strings.Contains(f.harnessRecord(), "ARG=--model\nARG=opus\n") &&
		!strings.Contains(f.harnessRecord(), "ARG=opus") {
		t.Errorf("the claude CLI was not handed the versionless alias:\n%s", f.harnessRecord())
	}
}

// The gateway's model probe is SKIPPED on a claude-sub job: there is no
// gateway route behind a subscription alias to prove, and a probe that
// pointed at the vendor would spend a request of the operator's quota for
// nothing. The harness probe is the proof, through the real intercepted path.
func TestAClaudeSubBootProbesNoGatewayRoute(t *testing.T) {
	f := claudeSubFixture(t)

	out, code := f.run()
	if code != 0 {
		t.Fatalf("a healthy claude-sub worker exited %d:\n%s", code, out)
	}
	curlRecord := f.env["TICKS_TEST_CURL_RECORD"]
	body, err := os.ReadFile(curlRecord)
	if err == nil && strings.TrimSpace(string(body)) != "" {
		t.Errorf("a claude-sub boot still made a gateway probe over curl:\n%s", string(body))
	}
	if !strings.Contains(out, "model probe skipped") {
		t.Errorf("the boot did not say why it skipped the model probe:\n%s", out)
	}
}

// The rung's own rule, enforced at the last door: the marker plus a PINNED
// model id is a configuration nobody should be able to produce, and the
// boot refuses it rather than spending per token on the vendor.
func TestAClaudeSubBootRefusesAPinnedClaudeModel(t *testing.T) {
	f := claudeSubFixture(t)
	f.env[EnvModel] = "claude-opus-5-5"

	out, code := f.run()
	if code != 7 {
		t.Fatalf("a claude-sub boot on a pinned model id exited %d, want the model refusal (7):\n%s", code, out)
	}
	if !strings.Contains(out, "sonnet, opus") {
		t.Errorf("the refusal does not name the aliases that would fix it:\n%s", out)
	}
}

// The marker belongs to the claude harness alone: a claude-sub marker on any
// other kind is a boot the factory miswired, and the container refuses it
// before a single model call.
func TestAClaudeSubMarkerOnAnotherHarnessIsRefused(t *testing.T) {
	f := claudeSubFixture(t)
	f.env[EnvHarness] = "omp"

	out, code := f.run()
	if code != 2 {
		t.Fatalf("a claude-sub marker on the omp harness exited %d, want the configuration refusal (2):\n%s", code, out)
	}
	if !strings.Contains(out, "TICKS_CLAUDE_SUB") {
		t.Errorf("the refusal does not name the marker:\n%s", out)
	}
}

// A marked boot with no placeholder is the factory's own defect: claude would
// fall back to per-token spend, so the container refuses before it starts.
func TestAClaudeSubMarkerWithoutThePlaceholderIsRefused(t *testing.T) {
	f := claudeSubFixture(t)
	delete(f.env, "CLAUDE_CODE_OAUTH_TOKEN")

	out, code := f.run()
	if code != 2 {
		t.Fatalf("a claude-sub marker with no placeholder exited %d, want the configuration refusal (2):\n%s", code, out)
	}
	if !strings.Contains(out, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("the refusal does not name the missing placeholder:\n%s", out)
	}
}
