package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// A missing directory is reported as not installed, not an error: a repo
// that has never installed the skill is an ordinary outcome, not a failure
// to compare.
func TestDiffMissingDirectory(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ticfac")
	result, err := Diff("ticfac", dir, "v1.2.3")
	if err != nil {
		t.Fatalf("diff a missing directory: %v", err)
	}
	if result.Installed {
		t.Errorf("a missing directory reports Installed = true")
	}
	if !result.Drift() {
		t.Errorf("a missing directory must report drift (install is the fix)")
	}
}

// A fresh install at the current version, untouched, carries no drift.
func TestDiffFreshInstallMatchesExactly(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ticfac")
	if _, err := Install("ticfac", dir, "v1.2.3", false); err != nil {
		t.Fatalf("install: %v", err)
	}
	result, err := Diff("ticfac", dir, "v1.2.3")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !result.Installed {
		t.Errorf("a freshly installed directory reports Installed = false")
	}
	if result.Drift() {
		t.Errorf("a fresh install at the compared version reports drift: %+v", result)
	}
	if result.Stamp != "v1.2.3" {
		t.Errorf("Stamp = %q, want v1.2.3", result.Stamp)
	}
}

// A stamp that names a different version than the one being compared
// against is drift, whatever the files say — the gap diff exists to catch.
func TestDiffVersionMismatchIsDrift(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ticfac")
	if _, err := Install("ticfac", dir, "v1.0.0", false); err != nil {
		t.Fatalf("install: %v", err)
	}
	result, err := Diff("ticfac", dir, "v2.0.0")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !result.Drift() {
		t.Errorf("a version mismatch (installed v1.0.0, binary v2.0.0) was not reported as drift")
	}
	if result.Stamp != "v1.0.0" {
		t.Errorf("Stamp = %q, want v1.0.0", result.Stamp)
	}
	if len(result.Added) != 0 || len(result.Removed) != 0 || len(result.Changed) != 0 {
		t.Errorf("a version-only mismatch reported file differences: %+v", result)
	}
}

// An unstamped directory (no .ticfac-skills-version) reports an empty
// Stamp, and is drift because an empty stamp never equals a real version.
func TestDiffUnstampedDirectoryIsDrift(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ticfac")
	if _, err := Install("ticfac", dir, "v1.0.0", false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, StampFile)); err != nil {
		t.Fatal(err)
	}
	result, err := Diff("ticfac", dir, "v1.0.0")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if result.Stamp != "" {
		t.Errorf("Stamp = %q, want empty for an unstamped directory", result.Stamp)
	}
	if !result.Drift() {
		t.Errorf("an unstamped directory was not reported as drift")
	}
}

// Files added, removed and changed on disk are all reported, by relative
// path, sorted.
func TestDiffFileChanges(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "ticfac")
	if _, err := Install("ticfac", dir, "v1.0.0", false); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Change an existing file.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("hand-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove a file the bundle carries.
	paths, err := Paths("ticfac")
	if err != nil {
		t.Fatal(err)
	}
	var removedPath string
	for _, p := range paths {
		if p != "SKILL.md" {
			removedPath = p
			break
		}
	}
	if removedPath == "" {
		t.Fatal("the ticfac bundle has only one file; cannot exercise a removal")
	}
	if err := os.Remove(filepath.Join(dir, removedPath)); err != nil {
		t.Fatal(err)
	}
	// Add a file the bundle does not carry.
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Diff("ticfac", dir, "v1.0.0")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !result.Drift() {
		t.Fatalf("file changes were not reported as drift")
	}
	if len(result.Added) != 1 || result.Added[0] != "extra.txt" {
		t.Errorf("Added = %v, want [extra.txt]", result.Added)
	}
	if len(result.Removed) != 1 || result.Removed[0] != removedPath {
		t.Errorf("Removed = %v, want [%s]", result.Removed, removedPath)
	}
	if len(result.Changed) != 1 || result.Changed[0] != "SKILL.md" {
		t.Errorf("Changed = %v, want [SKILL.md]", result.Changed)
	}
}
