//go:build !windows

package sandboximage

import (
	"os"
	"path/filepath"
	"strconv"
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
	shorttest.EndToEnd(t)
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
		// The container runs as root: without IS_SANDBOX the real CLI
		// refuses --dangerously-skip-permissions and the job dies at its
		// harness probe (epic ilz, 2026-10-07).
		`IS_SANDBOX="1"`,
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
	shorttest.EndToEnd(t)
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
	shorttest.EndToEnd(t)
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
	shorttest.EndToEnd(t)
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
	shorttest.EndToEnd(t)
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

// claudeSubProbeFails makes the fixture's claude probe fail its first `n`
// tries with `answer` and exit 1, then answer READY; the retry window is
// `tries` long and its backoff nothing. It returns the probe counter's path.
func (f *workerFixture) claudeSubProbeFails(n int, answer string, tries int) string {
	f.env["TICKS_TEST_PROBE_FAILS"] = strconv.Itoa(n)
	f.env["TICKS_TEST_PROBE_FAIL_ANSWER"] = answer
	f.env["TICKS_TEST_PROBE_COUNT"] = filepath.Join(f.root, "probe-count")
	f.env["TICKS_MODEL_PROBE_TRIES"] = strconv.Itoa(tries)
	f.env["TICKS_MODEL_PROBE_BACKOFF"] = "0"
	return f.env["TICKS_TEST_PROBE_COUNT"]
}

func probeCount(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(body)))
	return n
}

// A claude-sub boot's harness probe is the route's only proof (the model
// probe is skipped), so a hiccup between the container and Anthropic — the
// 529 an overloaded API answers — is asked again, and a probe that clears
// lets the job run. Before this, one 529 at boot was EXIT_HARNESS: a
// persistent "worker boot fault" that stopped the whole run.
func TestAClaudeSubProbeRetriesATransientAnswer(t *testing.T) {
	shorttest.EndToEnd(t)
	f := claudeSubFixture(t)
	count := f.claudeSubProbeFails(2, "API Error: 529 Overloaded", 4)

	out, code := f.run()
	if code != 0 {
		t.Fatalf("a claude-sub boot whose probe cleared on its third try exited %d:\n%s", code, out)
	}
	if got := probeCount(t, count); got != 3 {
		t.Errorf("the probe ran %d times, want 3 (two transient failures, then READY):\n%s", got, out)
	}
	if !strings.Contains(out, "harness probe try 1 of 4") {
		t.Errorf("the boot did not say it was retrying a transient probe:\n%s", out)
	}
}

// A transient answer that never clears is infrastructure, not the harness's
// wiring: the boot exits EXIT_GATEWAY_UNAVAILABLE, which the orchestrator
// redispatches at the same tier, never EXIT_HARNESS, which stops the run.
func TestAClaudeSubProbeThatNeverGetsThroughIsInfrastructure(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, answer := range []string{
		"API Error: Server is temporarily limiting requests (not your usage limit) · This request would exceed your account's rate limit.",
		"API Error: 529 Overloaded",
		"API Error: 502 Bad Gateway",
		"API Error: Connection error. (ECONNRESET)",
		"API Error: Request timed out.",
	} {
		t.Run(answer, func(t *testing.T) {
			f := claudeSubFixture(t)
			count := f.claudeSubProbeFails(99, answer, 3)

			out, code := f.run()
			if code != ExitGatewayUnavailable {
				t.Fatalf("a claude-sub probe that never got through exited %d, want %d (infrastructure):\n%s",
					code, ExitGatewayUnavailable, out)
			}
			if got := probeCount(t, count); got != 3 {
				t.Errorf("the probe ran %d times, want the window's 3:\n%s", got, out)
			}
			for _, banned := range []string{"through the gateway", "ANTHROPIC_API_KEY", "model probe above was GREEN"} {
				if strings.Contains(out, banned) {
					t.Errorf("the claude-sub stop says %q, which is the gateway route's wording:\n%s", banned, out)
				}
			}
			if !strings.Contains(out, "subscription") {
				t.Errorf("the stop does not name the subscription route:\n%s", out)
			}
		})
	}
}

// The wiring's own refusals are the same answer on every try: a token
// Anthropic refuses, the CLI's root check, a flag the pinned CLI does not
// take. They stay EXIT_HARNESS after ONE probe, and their stop speaks the
// claude-sub route's words — no gateway, no credential variable, and no
// claim that a (skipped) model probe was green.
func TestAClaudeSubProbeWiringRefusalStopsAtOnceInTheRoutesWords(t *testing.T) {
	shorttest.EndToEnd(t)
	for _, answer := range []string{
		"Failed to authenticate. API Error: 403 status code (no body)",
		`API Error: 401 {"type":"error","error":{"type":"authentication_error","message":"OAuth token has expired."}}`,
		"--dangerously-skip-permissions cannot be used with root/sudo privileges for security reasons",
		"error: unknown option '--no-such-flag'",
	} {
		t.Run(answer, func(t *testing.T) {
			f := claudeSubFixture(t)
			count := f.claudeSubProbeFails(99, answer, 4)

			out, code := f.run()
			if code != ExitHarness {
				t.Fatalf("a claude-sub probe refused by its wiring exited %d, want %d:\n%s", code, ExitHarness, out)
			}
			if got := probeCount(t, count); got != 1 {
				t.Errorf("a wiring refusal was asked %d times, want once:\n%s", got, out)
			}
			for _, banned := range []string{"through the gateway", "credential variable", "ANTHROPIC_API_KEY", "model probe above was GREEN"} {
				if strings.Contains(out, banned) {
					t.Errorf("the claude-sub stop says %q, which is the gateway route's wording:\n%s", banned, out)
				}
			}
			for _, want := range []string{"subscription", "interception", answer} {
				if !strings.Contains(out, want) {
					t.Errorf("the claude-sub stop does not say %q:\n%s", want, out)
				}
			}
		})
	}
}

// IS_SANDBOX is the claude CLI's root check, and every claude kind runs as
// root in the container — a per-token claude worker through the gateway
// (RUN_HARNESS=claude) as much as a subscription one. Without it the real
// CLI refuses --dangerously-skip-permissions at the probe.
func TestEveryClaudeBootTellsTheCLIItIsInASandbox(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.env[EnvHarness] = "claude"
	f.env["TICKS_TEST_WORKER_ENV_RECORD"] = filepath.Join(f.root, "claude-env")
	f.env["TICKS_TEST_PROBE_ENV_RECORD"] = filepath.Join(f.root, "probe-env")

	out, code := f.run()
	if code != 0 {
		t.Fatalf("a per-token claude worker exited %d:\n%s", code, out)
	}
	for _, name := range []string{"probe-env", "claude-env"} {
		body, err := os.ReadFile(filepath.Join(f.root, name))
		if err != nil {
			t.Fatalf("no %s record: %v\n%s", name, err, out)
		}
		if !strings.Contains(string(body), `IS_SANDBOX="1"`) {
			t.Errorf("the per-token claude CLI was not handed IS_SANDBOX=1 (%s):\n%s", name, body)
		}
	}
}
