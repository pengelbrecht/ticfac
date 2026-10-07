package runconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The schema and the reader cannot drift apart (tick tda): the schema is the
// authoring contract beside the authoring doc, and a reader that parses a
// table the schema does not name is a reader telling authors one thing and
// runs another — the shape of the drift that left [tier_policy] out of the
// schema for the whole of tick 5eq's life, and would have left [configs] out
// with it.
//
// The guard is structural rather than a JSON-schema library run: no such
// library is a dependency (stdlib-first), and the question that matters —
// does the schema NAME every top-level table the reader owns? — is answerable
// with the schema's own properties and the reader's own struct tags. The
// tracker's tables ([signals], [sweeps]) are the other reader's half of the
// file and are deliberately tolerated by the loader, so they are the one
// direction that may be richer on the schema's side.

// short: reads two files in this package and reflects; no harness, no git.
func TestTheSchemaNamesEveryTableTheReaderParses(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("runners-config.schema.json")
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("the schema is not JSON: %v", err)
	}

	// Every top-level key the reader owns, off the Config struct's own tags.
	parsed := map[string]bool{}
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("toml")
		if tag == "" || tag == "-" || strings.Contains(tag, ",") {
			continue
		}
		parsed[tag] = true
	}
	for key := range parsed {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("the reader parses [(%s)] and the schema names no such property — the authoring contract is a table behind the code it documents", key)
		}
	}
	// The [configs] shape the tick adds, at the depth it matters: the schema
	// must let a config carry exactly a routing — roles and a tier policy —
	// and refuse anything else, or "a config is a routing, not a second
	// repository" is a comment beside a schema that does not hold it.
	var defs struct {
		Defs struct {
			Config struct {
				Properties          map[string]any `json:"properties"`
				AdditionalProps     any            `json:"additionalProperties"`
				MinProperties       int            `json:"minProperties"`
				AdditionalPropsList []string
			} `json:"Config"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &defs); err != nil {
		t.Fatalf("re-read the schema's $defs: %v", err)
	}
	config := defs.Defs.Config
	if config.MinProperties != 1 {
		t.Errorf("the schema's Config allows minProperties %d, want 1 — an empty config is a typo the loader refuses", config.MinProperties)
	}
	if _, named := config.Properties["roles"]; !named {
		t.Error("the schema's Config does not allow roles")
	}
	if _, policy := config.Properties["tier_policy"]; !policy {
		t.Error("the schema's Config does not allow tier_policy")
	}
	if additional, isBool := config.AdditionalProps.(bool); isBool && additional {
		t.Error("the schema's Config does not close its additional properties — a config could grow a key that is no routing")
	}
}

// TestThisRepositorysOwnFilesCarryWellFormedNamedConfigs is the validate
// half for THIS repository's own declaration: the two named cloud configs the
// acceptance names — glm, the default, and claude, the subscription rung —
// parse, validate, and select, before the repository's own routing guard
// (internal/reconcile) resolves every job on them. The declaration is
// testdata/runners.cloud.configs.toml until it is appended to
// .tick/runners.cloud.toml (a worker may not write that file), so it is
// checked appended to a scratch copy of the real files — and the real files
// are checked the same way once they carry it.
//
// short: reads this repository's .tick files; no harness, no git.
func TestThisRepositorysOwnFilesCarryWellFormedNamedConfigs(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	assertWellFormedNamedConfigs(t, withTheDeclaredConfigs(t, root))
	repoFile := filepath.Join(root, ".tick", "runners.toml")
	cfg, err := LoadForConfig(repoFile, SubstrateCloud, "")
	if err != nil {
		t.Fatalf("the real merged cloud document does not load: %v", err)
	}
	if len(cfg.NamedConfigNames()) > 0 {
		assertWellFormedNamedConfigs(t, repoFile)
	}
}

// withTheDeclaredConfigs copies this repository's runners files into a
// scratch directory with testdata/runners.cloud.configs.toml appended to the
// cloud file, and returns the scratch runners.toml.
func withTheDeclaredConfigs(t *testing.T, root string) string {
	t.Helper()
	block, err := os.ReadFile(filepath.Join("testdata", "runners.cloud.configs.toml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"runners.toml", "runners.local.toml", "runners.cloud.toml"} {
		data, err := os.ReadFile(filepath.Join(root, ".tick", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "runners.cloud.toml" {
			data = append(append(data, '\n'), block...)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "runners.toml")
}

// assertWellFormedNamedConfigs holds one runners file's merged cloud
// document to the acceptance's two named configs.
func assertWellFormedNamedConfigs(t *testing.T, path string) {
	t.Helper()
	cfg, err := LoadForConfig(path, SubstrateCloud, "")
	if err != nil {
		t.Fatalf("the merged cloud document does not load: %v", err)
	}
	names := cfg.NamedConfigNames()
	if len(names) != 2 {
		t.Fatalf("the cloud document declares %d named configs, want the two the acceptance names (glm, claude): %v", len(names), names)
	}
	if cfg.DefaultConfigName() != "glm" {
		t.Errorf("the declared default is %q, want glm", cfg.DefaultConfigName())
	}
	for _, name := range []string{"glm", "claude"} {
		if _, ok := cfg.NamedConfig(name); !ok {
			t.Errorf("the named config %s is not declared", name)
		}
	}
	// Each declares a complete routing: every role the run dispatches and a
	// tier policy of its own.
	for _, name := range names {
		nc, _ := cfg.NamedConfig(name)
		for _, role := range []string{"implement", "review", "closeout"} {
			if _, ok := nc.Roles[role]; !ok {
				t.Errorf("the %s config declares no %s cell — a named config is a complete routing", name, role)
			}
		}
		if nc.TierPolicy == nil {
			t.Errorf("the %s config declares no tier policy — its ladder is the whole of its own", name)
		}
	}
	// The claude config's cells ride the subscription rung: claude on the
	// versionless aliases, never a pinned model id (the operator's note,
	// 2026-10-06).
	claude, err := LoadForConfig(path, SubstrateCloud, "claude")
	if err != nil {
		t.Fatalf("select claude: %v", err)
	}
	for _, role := range []string{"implement", "review", "closeout"} {
		w, err := claude.ResolveOn(SubstrateCloud, role, "")
		if err != nil {
			t.Errorf("the claude config's %s does not resolve in the cloud: %v", role, err)
			continue
		}
		if w.Kind != "claude" {
			t.Errorf("the claude config's %s runs on %q, want claude", role, w.Kind)
		}
		switch w.Model {
		case "sonnet", "opus":
		default:
			t.Errorf("the claude config's %s names model %q — the versionless aliases are what make the rung subscription-billed; a pinned id bills per token", role, w.Model)
		}
	}
	// The implement ladder climbs sonnet → opus (the operator's ladder,
	// 2026-10-06): economy is sonnet, strong is opus, and the step from the
	// default to the ceiling is two rungs so one failure goes straight up.
	for tier, want := range map[Tier]string{TierEconomy: "sonnet", TierStrong: "opus"} {
		w, err := claude.ResolveOn(SubstrateCloud, "implement", tier)
		if err != nil {
			t.Errorf("the claude config's implement at %s does not resolve: %v", tier, err)
			continue
		}
		if w.Model != want {
			t.Errorf("the claude config's implement at %s runs %q, want the ladder's %q", tier, w.Model, want)
		}
	}
	// The concurrency caps the operator named — claude-sub 1–2 — ride the
	// claude config's own policy: strong (opus) is capped at one, economy
	// (sonnet) at two.
	if c := claude.TierPolicy.Concurrency; c["strong"] != 1 || c["economy"] != 2 {
		t.Errorf("the claude config's concurrency caps are %v, want economy 2 strong 1 (claude-sub 1–2)", c)
	}
	// And the default config is the Workers AI routing the file's own cells
	// have always declared: nothing about an unnamed run changes.
	glm, err := LoadForConfig(path, SubstrateCloud, "glm")
	if err != nil {
		t.Fatalf("select glm: %v", err)
	}
	w, err := glm.ResolveOn(SubstrateCloud, "implement", "")
	if err != nil {
		t.Fatalf("the glm config's implement does not resolve: %v", err)
	}
	if w.Kind != "pi" || !isWorkersAIDeclared(w.Model) {
		t.Errorf("the glm config's implement runs %s/%s, want the durable harness on a Workers AI model", w.Kind, w.Model)
	}
}

// isWorkersAIDeclared is the config package's own answer for the test above,
// spelled locally so the test reads as English.
func isWorkersAIDeclared(model string) bool {
	for _, prefix := range []string{"cloudflare-workers-ai/", "workers-ai/", "@cf/"} {
		if strings.HasPrefix(model, prefix) && len(model) > len(prefix) {
			return true
		}
	}
	return false
}
