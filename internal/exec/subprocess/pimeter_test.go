package subprocess

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	metering := &GatewayMetering{RunID: "epic-hn6", GatewayURL: "https://gateway.ai.cloudflare.com/v1/acct/gw/"}

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
	// with run_id, spelled as the JSON object the factory's proxy stamps.
	if !strings.Contains(body, `"cf-aig-metadata": "{\"run_id\":\"epic-hn6\"}"`) {
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
	// The gateway's own credential, resolved at request time: the SAME key
	// the factory itself authenticates this exact route with, stamped as the
	// gateway's cf-aig-authorization header — the header that OPENS the
	// gateway (pi's stored wallet key does not, live: 401 code 2009). It is
	// not, on its own, the credential the call authenticates upstream with:
	// the gateway forwards the caller's Authorization to Workers AI (live,
	// tick 648 probe e), which is why the next entry exists.
	if !strings.Contains(body, `"cf-aig-authorization": "!grep '^factory_cloudflare_api_token=' `) {
		t.Errorf("the override does not name the credential the gateway route authenticates:\n%s", body)
	}
	// The upstream credential, DISPLACED: the gateway forwards the
	// caller's Authorization to Workers AI, which authenticates that header
	// and not the gateway's own (live, tick 648 probe e: a valid
	// cf-aig-authorization beside a bogus Authorization logs a failed row,
	// upstream code 10000 Authentication error) — so a host whose pi
	// stores a key Workers AI refuses turns every metered dispatch into a
	// 401, exactly the dm2 host's state. A headers.Authorization entry in
	// the override DOES displace pi's stored key (tick m4t, verified
	// against a fake gateway — the drain test pins it end to end), so the
	// SAME request-time account token rides both headers. Which credential
	// pays is the operator's decision, stated by the ~/.ticfacrc key they
	// configure: the account token the factory's own cloud runs ride.
	if !strings.Contains(body, `"Authorization": "!grep '^factory_cloudflare_api_token=' `) {
		t.Errorf("the override does not displace pi's stored key in Authorization:\n%s", body)
	}
	// The argv that loads it.
	if args := metering.ExtensionArgs(path); len(args) != 2 || args[0] != "--extension" || args[1] != path {
		t.Errorf("the extension args are %v, want --extension <path>", args)
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
