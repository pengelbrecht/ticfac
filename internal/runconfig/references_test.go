package runconfig

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// runnerReferences are the runner references ticks dropped from its skill when
// it became tracker-only (ticks epic chz, released as v0.32.0) and ticfac took
// over, beside the runners.toml authoring doc they used to sit beside (tick
// dz1): the runner-neutral contract, the herdr substrate, the per-kind lookup
// table, the config reference and its schema, and the four harness adapters.
var runnerReferences = []string{
	"agent-runner.md",
	"runners-config.md",
	"runners-config.schema.json",
	"herdr-kinds.md",
	"herdr-runner.md",
	"prime-runner.md",
	"pi-runner.md",
	"claude-runner.md",
	"codex-runner.md",
}

// TestTheRunnerReferencesLiveInTicfac is dz1's A2, first half: every runner
// reference ticks' skill dropped at v0.32.0 is a file in this package, and
// every relative link one of them makes resolves inside this repository.
func TestTheRunnerReferencesLiveInTicfac(t *testing.T) {
	t.Parallel()
	for _, name := range runnerReferences {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s is not in internal/runconfig: ticks dropped it from its skill at v0.32.0, so ticfac is the only place it can live (%v)", name, err)
		}
	}
	link := regexp.MustCompile(`\]\(([^)#\s]+)(?:#[^)]*)?\)`)
	docs, err := filepath.Glob("*.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range docs {
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range link.FindAllStringSubmatch(string(body), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(doc), target)); err != nil {
				t.Errorf("%s links to %s, which does not exist in ticfac", doc, target)
			}
		}
	}
}

// TestNothingInTicfacPointsAtAReferenceTicksDeleted is dz1's A2, second half:
// no tracked source, doc or config in this repository names a file under the
// ticks skill's references/ directory that v0.32.0 no longer ships. The files
// that directory still has are tracker-authoring references (tk-commands,
// tick-patterns, …); everything execution-shaped moved here, and a pointer
// into ticks for it is a pointer to nothing.
func TestNothingInTicfacPointsAtAReferenceTicksDeleted(t *testing.T) {
	t.Parallel()
	// What skills/ticks/references/ carries at v0.32.0 (ticks 7b6c0b2f).
	stillInTicks := map[string]bool{
		"big-picture.md":   true,
		"code-smells.md":   true,
		"goal-design.md":   true,
		"tick-patterns.md": true,
		"tk-commands.md":   true,
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("the repository root is not two levels above internal/runconfig: %v", err)
	}
	pointer := regexp.MustCompile(`skills/ticks/references/([A-Za-z0-9_.-]+)`)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, ".wrangler": true, "dist": true,
		// The tracker's and the run's own records are history, not links.
		".tick": true, ".ticfac": true, "runs": true,
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range pointer.FindAllSubmatch(body, -1) {
			if name := string(m[1]); !stillInTicks[name] {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s points at skills/ticks/references/%s, which ticks deleted at v0.32.0 (chz): the reference is ticfac's now (internal/runconfig/)", rel, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
