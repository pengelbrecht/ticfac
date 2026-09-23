package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// OPERATOR RULE, 2026-09-23: "the thing you must avoid is to invoke claude
// models from cf cloud executors" - while claude is encouraged LOCALLY when a
// tick is stuck (.tick/runners.toml puts claude opus at the top of the local
// implement ladder).
//
// The hard guard is the factory gateway, not routing config: it forwards
// Workers AI alone unless GATEWAY_ALLOWED_PROVIDERS names another provider
// (cloudflare/src/gateway.ts, allowedProviders), and a container holds no
// provider key of its own - only its run's gateway token. So even a container
// that somehow started the claude CLI cannot reach a Claude model. This test
// keeps that true: the deployed configuration may not opt the gateway into any
// provider but Workers AI. Widening it is a decision to write down here, in the
// same change, not a variable to flip.
//
// short: parses one tracked file, no repository and no process
func TestTheFactoryGatewayRoutesWorkersAIOnly(t *testing.T) {
	path := filepath.Join("..", "..", "cloudflare", "wrangler.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg struct {
		Vars map[string]any            `toml:"vars"`
		Env  map[string]map[string]any `toml:"env"`
	}
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	check := func(where string, vars map[string]any) {
		value, set := vars["GATEWAY_ALLOWED_PROVIDERS"]
		if !set {
			return
		}
		for _, provider := range strings.FieldsFunc(strings.ToLower(strings.TrimSpace(toString(value))), func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			if provider != "workers-ai" {
				t.Errorf("%s sets GATEWAY_ALLOWED_PROVIDERS to include %q: the factory gateway must route Workers AI "+
					"only, so no cloud executor can reach a Claude (or any other billed) model", where, provider)
			}
		}
	}
	check("[vars]", cfg.Vars)
	for name, env := range cfg.Env {
		if vars, ok := env["vars"].(map[string]any); ok {
			check("[env."+name+".vars]", vars)
		}
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
