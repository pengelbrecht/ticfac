package runconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This repo dogfoods its own run config the same way ticks does: the
// execution-half reader this package is must load the `.tick/runners.toml`
// committed HERE — the file that routes the very workers building ticfac —
// through the same entry point a run will use. ticks' copy of this test also
// proves the repo carries no legacy structured sections in `.tick/config.md`
// (running the migrator finds nothing to move); that assertion is the
// migrator's own and stayed in ticks with the migrator.

// repoRootForTest walks up from this source file to the module root.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate the repo root")
	}
	// internal/runconfig -> repo root is TWO levels. It was three while this
	// package lived at internal/herd/config; the package moved out from under
	// the herdr tree so the reconciler could import it without tripping the
	// executor's seam check (it is executor-agnostic config, not herdr's).
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected a go.mod at %s: %v", root, err)
	}
	return root
}

// TestRepoRunnersConfigIsLoadable proves the committed config passes this
// package — the reader whose decision procedure and role resolution later
// Phase 2 ticks build on. A mistake in the repo's own routing file breaks
// every future run rather than failing somebody's unit test.
func TestRepoRunnersConfigIsLoadable(t *testing.T) {
	cfg, err := LoadRepo(repoRootForTest(t))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadRepo returned no config; this repo commits .tick/runners.toml")
	}
}

// TestRepoRunnersConfigCarriesTheCommandSurface proves the command tables the
// run relies on actually arrived, and that the acceptance-free shape this
// repository keeps (testing + environment, no evidence) is what the reader
// sees — the whole file validates or the run does not start.
func TestRepoRunnersConfigCarriesTheCommandSurface(t *testing.T) {
	cfg, err := LoadRepo(repoRootForTest(t))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	if cfg.Testing == nil || len(cfg.Testing.Commands) == 0 {
		t.Error("[testing.commands] is empty; the integrated gate is declared nowhere")
	}
	if cfg.Environment == nil || len(cfg.Environment.Commands) == 0 {
		t.Error("[environment.commands] is empty; the run-start pre-flight is declared nowhere")
	}
	if cfg.Evidence != nil {
		t.Error("[evidence] is present; this repository keeps close-out authorization out of the file")
	}
	// The routing this repository actually runs: the implement role carries a
	// tier overlay table, and tier overlays change the model.
	if role := cfg.Roles["implement"]; role == nil || role.Tiers["economy"] == nil || role.Tiers["strong"] == nil {
		t.Errorf("[roles.implement] does not carry the tier overlays this epic routes on: %+v", role)
	}
}

// TestRepoRunnersConfigResolvesEveryRole the way a dispatch will: through
// [Config.Resolve], including the fallback cells (a role with no entry of its
// own resolves against implement) and the tier overlays the epic actually
// names. Resolve is the seam ticks' internal/sandbox reads through, so it
// must answer for this repo's own file.
func TestRepoRunnersConfigResolvesEveryRole(t *testing.T) {
	cfg, err := LoadRepo(repoRootForTest(t))
	if err != nil {
		t.Fatalf("LoadRepo: %v", err)
	}
	for _, role := range []string{RoleImplement, "review", "closeout", "plan", "scout"} {
		for _, tier := range []Tier{"", TierEconomy, TierBalanced, TierStrong, TierFrontier} {
			w, err := cfg.Resolve(role, tier)
			if err != nil {
				t.Errorf("Resolve(%q, %q): %v", role, tier, err)
				continue
			}
			if w.Kind == "" {
				t.Errorf("Resolve(%q, %q) produced no kind", role, tier)
			}
		}
	}
	// The strong tier of the implement role is the same model as the base —
	// the routing this file documents in its own comments.
	w, err := cfg.Resolve(RoleImplement, TierStrong)
	if err != nil {
		t.Fatalf("Resolve(implement, strong): %v", err)
	}
	if !w.TierApplied || w.Model != cfg.Roles["implement"].Model {
		t.Errorf("strong tier = %+v, want the role's own model applied as an overlay", w)
	}
	if !strings.HasPrefix(w.Label(), "roles.implement.tiers.") {
		t.Errorf("Label() = %q, want the tier cell named", w.Label())
	}
}
