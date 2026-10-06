package subprocess

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
)

// The gateway metering suite (tick dm2): the generated pi override is the
// one artifact that joins a local run's Workers AI spend to the gateway
// logs, so every claim here is a claim about bytes a pane will execute —
// the provider name pi resolves, the route shape the factory already proved
// in the sandbox, the metadata key the reader filters on, and the boundary
// the public repository keeps (no operator identifier in repository
// content, none of them in a file the repository ships).

func TestGatewayMeteringAppliesOnlyToWorkersAIModels(t *testing.T) {
	t.Parallel()
	metering := &GatewayMetering{RunID: "epic-hn6", GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw"}

	for _, model := range []string{
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3",
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash",
		"workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast",
		"@cf/zai-org/glm-5.3",
	} {
		if !metering.Applies(model) {
			t.Errorf("the join does not apply to %q: a Workers AI model's spend runs through the gateway route", model)
		}
	}
	for _, model := range []string{
		"opus", "claude-opus-5", "gpt-5.6-luna", "openrouter/anthropic/claude-opus-5",
		"openai-codex/gpt-5.6-sol", "", "cloudflare-workers-ai/",
	} {
		if metering.Applies(model) {
			t.Errorf("the join applies to %q: only a Workers AI model reaches the gateway route, and the namespace alone names one", model)
		}
	}
	var none *GatewayMetering
	if none.Applies("cloudflare-workers-ai/@cf/zai-org/glm-5.3") {
		t.Error("a nil metering applies: a host with no gateway runs exactly as it did before this tick")
	}
}

func TestGatewayMeteringWritesTheOverrideTheReaderJoins(t *testing.T) {
	t.Parallel()
	metering := &GatewayMetering{
		RunID:      "epic-hn6",
		TickID:     "kf4",
		Attempt:    45,
		GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw/",
	}

	path, err := metering.WriteExtension(t.TempDir())
	if err != nil {
		t.Fatalf("write the metering extension: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the metering extension back: %v", err)
	}
	body := string(raw)

	// The provider, by pi's own name: the override must keep every model of
	// the provider it names, so a catalog entry is never lost.
	if !strings.Contains(body, `pi.registerProvider("cloudflare-workers-ai", {`) {
		t.Errorf("the override does not name pi's cloudflare-workers-ai provider:\n%s", body)
	}
	// The route, the factory's own workers-ai shape: <gateway>/workers-ai,
	// trailing slash trimmed, exactly what configure_pi_provider writes for
	// a sandbox.
	if !strings.Contains(body, `baseUrl: "https://gateway.ai.cloudflare.com/v1/acct/gw/workers-ai/v1"`) {
		t.Errorf("the override does not point the provider at the gateway's workers-ai route:\n%s", body)
	}
	// The wire, the one the sandbox proved against this route.
	if !strings.Contains(body, `api: "openai-completions"`) {
		t.Errorf("the override does not keep the OpenAI-completions wire the route serves:\n%s", body)
	}
	// The metadata, the key and value the reader filters by: cf-aig-metadata
	// with run_id, spelled as the JSON object the factory's proxy stamps —
	// and (tick kf4) with the attempt the dispatch carried, the same key the
	// factory's own gatewayMetadata stamps, so a gateway number can name
	// WHICH attempts it measured instead of claiming the whole run.
	if !strings.Contains(body, `"cf-aig-metadata": "{\"run_id\":\"epic-hn6\",\"tick_id\":\"kf4\",\"attempt\":\"45\"}"`) {
		t.Errorf("the override does not stamp the metadata the reader joins on:\n%s", body)
	}
	// The cache affinity, the run id as the instance key (D24).
	if !strings.Contains(body, `"x-session-affinity": "epic-hn6"`) {
		t.Errorf("the override does not pin the prefix cache to the run's model instance:\n%s", body)
	}
	// The boundary: the file names the credential's KEY (the command reads
	// ~/.ticfacrc at request time, prepending the Bearer scheme inside the
	// shell) but carries no token VALUE and no account identifier beyond the
	// URL the operator's own ~/.ticfacrc names — the token is resolved per
	// request, never copied into per-attempt state.
	if secret := regexp.MustCompile(`"(cfut|cf)_[A-Za-z0-9_-]{20,}"`).FindString(body); secret != "" {
		t.Errorf("the override carries a credential value %s: the token is read at request time from ~/.ticfacrc, and a copy baked into per-attempt state is one nobody asked for", secret)
	}
	// The credential the join rides, on BOTH headers: the gateway's own
	// cf-aig-authorization — the header that OPENS the gateway (pi's stored
	// wallet key does not, live: 401 code 2009) — and the plain
	// Authorization the gateway forwards to Workers AI, which authenticates
	// that header and not the gateway's own (live, tick 648 probe e: a valid
	// cf-aig-authorization beside a bogus Authorization logs a failed row,
	// upstream code 10000 Authentication error). A headers.Authorization
	// entry in the override DOES displace pi's stored key (tick m4t, verified
	// against a fake gateway — the drain test pins it end to end), so the
	// SAME request-time account token rides both headers. Which credential
	// pays is the operator's decision, stated by the ~/.ticfacrc key they
	// configure: the account token the factory's own cloud runs ride.
	//
	// The command that reads it is the credentials package's own builder —
	// the same one File.Get is pinned to (tick frr) — so the expected bytes
	// are BUILT here too, not re-spelled: an assertion that quoted the
	// command by hand would only test that two hand-writings agree.
	credentialCommand := "!" +
		credentials.ShellGetCommand(credentials.KeyCloudflareAPIToken) +
		" | sed 's/^/Bearer /'"
	for _, header := range []string{gatewayAuthHeader, upstreamAuthHeader} {
		if !strings.Contains(body, jsonWord(header)+": "+jsonWord(credentialCommand)) {
			t.Errorf("the override does not stamp %s with the credentials package's own reader of %s:\n%s",
				header, credentials.KeyCloudflareAPIToken, body)
		}
	}
	// The argv that loads it.
	if args := metering.ExtensionArgs(path); len(args) != 2 || args[0] != "--extension" || args[1] != path {
		t.Errorf("the extension args are %v, want --extension <path>", args)
	}
}

<<<<<<< HEAD
func TestTheGeneratedCredentialCommandReadsWhatGetReads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the credential command runs where pi runs: a POSIX shell")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH: the credential command runs where pi runs")
	}
	for name, rc := range map[string]string{
		// The tick's failing inputs, verbatim: a hand-edited line the Go side
		// reads fine, and a duplicated key that once produced a two-line
		// header value.
		"hand-edited spaces around the =": "factory_cloudflare_api_token = cf_spaced_token\n",
		"a duplicated key":                "factory_cloudflare_api_token=cf_first\nfactory_cloudflare_api_token=cf_second\n",
	} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, credentials.FileName), []byte(rc), 0o600); err != nil {
			t.Fatal(err)
		}

		// The real artifact, not a constant re-quoted: the command is PARSED
		// out of the extension the executor actually writes, so a
		// hand-written command in pimeter.go fails here on the very bytes a
		// pane would execute.
		metering := &GatewayMetering{RunID: "run-frr", GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw"}
		path, err := metering.WriteExtension(t.TempDir())
		if err != nil {
			t.Fatalf("%s: write the metering extension: %v", name, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read the metering extension back: %v", name, err)
		}
		quoted := regexp.MustCompile(`"` + upstreamAuthHeader + `": "((?:[^"\\]|\\.)*)"`).FindStringSubmatch(string(raw))
		if quoted == nil {
			t.Fatalf("%s: the override carries no %s command to parse:\n%s", name, upstreamAuthHeader, raw)
		}
		command, err := strconv.Unquote(`"` + quoted[1] + `"`)
		if err != nil {
			t.Fatalf("%s: unquote the credential command: %v", name, err)
		}

		// The command runs the way pi runs it: in a shell, against the
		// operator's ~/.ticfacrc — here the fixture HOME.
		var stdout bytes.Buffer
		cmd := exec.Command(sh, "-c", strings.TrimPrefix(command, "!"))
		cmd.Dir = home
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: run the credential command: %v", name, err)
		}

		// The verdict is the credential the GO side reads, from the file
		// format's own package — not a second hand-spelling of the value.
		file, err := credentials.LoadFrom(filepath.Join(home, credentials.FileName))
		if err != nil {
			t.Fatalf("%s: load the fixture ~/.ticfacrc: %v", name, err)
		}
		if got, want := strings.TrimSuffix(stdout.String(), "\n"), "Bearer "+file.Get(credentials.KeyCloudflareAPIToken); got != want {
			t.Errorf("%s: the generated command sent %q, want the ~/.ticfacrc credential %q: the two readers of the file must not drift (tick frr)", name, got, want)
		}
=======
// TestGatewayMeteringOmitsTheNamesADispatchDidNotState: the attempt and
// tick keys are omitempty — a join that names no attempt (the dm2-era
// shape, and every attempt this repository dispatched before tick kf4)
// stamps exactly the run id, so a row from before the join named anything
// stays distinguishable from one that did.
func TestGatewayMeteringOmitsTheNamesADispatchDidNotState(t *testing.T) {
	t.Parallel()
	metering := &GatewayMetering{RunID: "epic-hn6", GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw"}
	path, err := metering.WriteExtension(t.TempDir())
	if err != nil {
		t.Fatalf("write the metering extension: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the metering extension back: %v", err)
	}
	if !strings.Contains(string(raw), `"cf-aig-metadata": "{\"run_id\":\"epic-hn6\"}"`) {
		t.Errorf("a join that names no attempt stamped more than the run id:\n%s", raw)
>>>>>>> 759e8c33935a2517d60fcf99b42386bfc58fd47c
	}
}

func TestGatewayMeteringRefusesAnUnusableConfiguration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	for name, m := range map[string]*GatewayMetering{
		"no run id":          {GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw"},
		"no gateway":         {RunID: "epic-hn6"},
		"a relative gateway": {RunID: "epic-hn6", GatewayURL: "gateway.example.com/v1/acct/gw"},
	} {
		if _, err := m.WriteExtension(dir); err == nil {
			t.Errorf("%s wrote an override anyway: a join that cannot attribute a request is not a join", name)
		}
	}
	var none *GatewayMetering
	if _, err := none.WriteExtension(dir); err == nil {
		t.Error("a nil metering wrote an override: nothing is configured")
	}
	if args := none.ExtensionArgs("any"); args != nil {
		t.Errorf("a nil metering names args %v, want none", args)
	}
	if _, err := os.Stat(filepath.Join(dir, extensionFile)); err == nil {
		t.Error("a refused configuration still left a file behind: per-attempt state is written only when the join is real")
	}
}
