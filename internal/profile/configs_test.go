package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// Named run configs through the profile reader (tick tda): a resolution that
// names a config routes on the config's cells, its provenance names the
// config, and a name nobody declared is the refusal, never a fall back.

const configProfileCommon = `version = 2

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

const configProfileCloud = `version = 2

[configs]
default = "glm"

[configs.glm.roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.review]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

[configs.glm.roles.closeout]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"

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

[roles.implement]
kind = "pi"
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
effort = "high"

[roles.implement.tiers.economy]
model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3-flash"

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
`

func writeProfileConfigRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".tick"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tick", "runners.toml"), []byte(configProfileCommon), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tick", "runners.cloud.toml"), []byte(configProfileCloud), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, ".tick", "runners.toml")
}

// TestAResolutionOnTheClaudeConfigRoutesTheClaudeSubRung: the acceptance's
// own sentence — an epic declaring config: claude runs its workers on the
// claude-sub rung (claude on the versionless sonnet/opus aliases), not on
// the Workers AI cells the file's own roles declare.
func TestAResolutionOnTheClaudeConfigRoutesTheClaudeSubRung(t *testing.T) {
	path := writeProfileConfigRepo(t)
	p, err := Resolve("implement-tick", Options{
		Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud), Config: "claude",
	})
	if err != nil {
		t.Fatalf("resolve implement on the claude config: %v", err)
	}
	if p.Runner != "claude" || p.Model != "sonnet" {
		t.Errorf("implement resolves to %s/%s, want claude/sonnet", p.Runner, p.Model)
	}
	if _, rung := SubscriptionRungFor(p.Runner, p.Model); !rung {
		t.Errorf("claude/sonnet is not on a subscription rung: the cloud billing rule would refuse it")
	}
	if p.Provenance.Config != "claude" {
		t.Errorf("the resolved profile's provenance names config %q, want claude", p.Provenance.Config)
	}
	if !strings.Contains(p.Routed, "[configs.claude.roles.implement]") {
		t.Errorf("the routed provenance does not name the config's cell: %q", p.Routed)
	}

	// The same role at the strong tier climbs to opus, the subscription's
	// dear alias — the ladder the operator named (2026-10-06).
	p, err = Resolve("implement-tick", Options{
		Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud), Config: "claude", Tier: "strong",
	})
	if err != nil {
		t.Fatalf("resolve implement at strong on the claude config: %v", err)
	}
	if p.Model != "opus" {
		t.Errorf("implement at strong resolves to %s, want opus", p.Model)
	}

	// Review and closeout run on opus, the operator's ladder.
	for _, role := range []string{"review-epic", "closeout-epic"} {
		p, err := Resolve(role, Options{
			Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud), Config: "claude",
		})
		if err != nil {
			t.Fatalf("resolve %s on the claude config: %v", role, err)
		}
		if p.Runner != "claude" || p.Model != "opus" {
			t.Errorf("%s resolves to %s/%s, want claude/opus", role, p.Runner, p.Model)
		}
	}
}

// TestTheDefaultConfigResolvesTheWorkersAICells: another epic, on the default
// GLM config, runs unchanged — pi on GLM, the file's own cloud routing.
func TestTheDefaultConfigResolvesTheWorkersAICells(t *testing.T) {
	path := writeProfileConfigRepo(t)
	p, err := Resolve("implement-tick", Options{
		Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud), Config: "glm",
	})
	if err != nil {
		t.Fatalf("resolve implement on the glm config: %v", err)
	}
	if p.Runner != "pi" || p.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3" {
		t.Errorf("implement resolves to %s/%s, want pi/GLM 5.3", p.Runner, p.Model)
	}
	// And no selection at all: the file as loaded, the historical answer.
	p, err = Resolve("implement-tick", Options{
		Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud),
	})
	if err != nil {
		t.Fatalf("resolve implement with no selection: %v", err)
	}
	if p.Provenance.Config != "" || strings.Contains(p.Routed, "[configs.") {
		t.Errorf("the no-selection resolution names a config: %q / %q", p.Provenance.Config, p.Routed)
	}
}

// TestAnUndeclaredConfigIsARefusal: an operator who asked for a config nobody
// declared gets the sentinel, never the file's own cells.
func TestAnUndeclaredConfigIsARefusal(t *testing.T) {
	path := writeProfileConfigRepo(t)
	_, err := Resolve("implement-tick", Options{
		Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud), Config: "opus-max",
	})
	if !errors.Is(err, runconfig.ErrNoSuchConfig) {
		t.Fatalf("resolving on an undeclared config is %v, want runconfig.ErrNoSuchConfig", err)
	}
}

// TestTheConfigSelectionDoesNotLeakBetweenResolutions: a run resolves every
// role — some with a config selected, some without (the on-demand jobs when
// no config was selected at all, say) — and one resolution's selection must
// not reach another's cells.
func TestTheConfigSelectionDoesNotLeakBetweenResolutions(t *testing.T) {
	path := writeProfileConfigRepo(t)
	onClaude, err := Resolve("implement-tick", Options{
		Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud), Config: "claude",
	})
	if err != nil {
		t.Fatalf("resolve implement on claude: %v", err)
	}
	plain, err := Resolve("implement-tick", Options{
		Dir: "", RunnersConfig: path, Substrate: string(runconfig.SubstrateCloud),
	})
	if err != nil {
		t.Fatalf("resolve implement with no selection: %v", err)
	}
	if plain.Model != "cloudflare-workers-ai/@cf/zai-org/glm-5.3" {
		t.Errorf("a claude resolution leaked into the plain one: %q", plain.Model)
	}
	if onClaude.Model != "sonnet" {
		t.Errorf("the claude resolution lost its cells: %q", onClaude.Model)
	}
}
