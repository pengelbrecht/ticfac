package reconcile

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Can a cached pass hide a change to .tick/runners.toml? No — and this is the
// demonstration, because tick 6wh is what made the question live.
//
// The background is tick mbv. The gate stopped passing -count=1 on 2026-09-18
// so that a package whose inputs had not changed could answer from cache, and
// mbv recorded a measured exposure in that decision: a test that reads a file
// by an ABSOLUTE path OUTSIDE the module was served a stale pass after that
// file changed. This repository's drift guards — TestThisRepositorysGateIsReadable,
// TestTheGateTargetMatchesTheDeclaredGate and
// TestTheHarnessBoundOutlivesEveryDeclaredGateBound — all read
// .tick/runners.toml through contracts.RepoRoot(), which builds from
// os.Getwd() and so yields exactly that shape: an absolute path. A cached pass
// surviving an edit to that file would leave the three guards silently not
// guarding.
//
// Until 6wh the exposure was theoretical in the most embarrassing way: the gate
// ran in a fresh directory every time, so nothing was ever served from cache
// and the flag change had bought nothing. Making the cache work makes the
// exposure real, so it is answered here rather than left to mbv.
//
// The answer, measured twice — by hand against this repository's own guards on
// 2026-09-20 (edit the `go` gate's command in .tick/runners.toml and
// TestTheGateTargetMatchesTheDeclaredGate fails, uncached, instead of
// answering `ok (cached)`), and hermetically by this test on every run:
//
// go tracks what a test OPENED, and invalidates the cached result when a file
// inside the module changes, absolute path or not. mbv's unsound case is
// specifically a file outside the module — a shape none of the three guards
// has, since .tick/runners.toml sits at the module root. No guard needs
// -count=1, and `make suite` stays what it is: the paranoid answer, not a
// required one.
//
// The fixture below mirrors the guards exactly rather than approximately: the
// test lives in a package UNDER the module root, and the file it reads is at
// the module root, reached by an absolute path built from the working
// directory. Get either of those wrong and the experiment answers about a
// different shape — mbv found three shapes that disagreed.
func TestACachedPassCannotHideAnEditToAFileAGuardReads(t *testing.T) {
	t.Parallel()
	// Resolved, and this is not a detail: a module reached through a symlink
	// (t.TempDir() is /var/... and the working directory the test binary reads
	// is /private/var/...) puts every file the test opens OUTSIDE the module as
	// far as cmd/go is concerned, and go then declines to cache the package at
	// all. Measured here, and it is the same reason gatedir.go resolves the
	// slot root — an unresolved gate directory would have made this whole tick
	// a no-op while looking correct.
	module, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(module, ".tick", "runners.toml")
	writeUnder(t, module, ".tick/runners.toml", "declared = \"the gate as it stands\"\n")
	write(t, filepath.Join(module, "go.mod"), "module ticfac.example/guard\n\ngo 1.21\n")
	// Unique to this run, so the first `go test` below is a cache MISS however
	// often this suite has run before.
	writeUnder(t, module, "internal/guard/guard_test.go", fmt.Sprintf(`package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const salt = %q

// The drift guard's own shape: walk up to the module root the way
// contracts.RepoRoot() does, and read an input from it by ABSOLUTE path.
func TestTheDeclaredGateIsWhatThisTestExpects(t *testing.T) {
	_ = salt
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no module root above this package")
		}
		dir = parent
	}
	declared, err := os.ReadFile(filepath.Join(dir, ".tick", "runners.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(declared), "the gate as it stands") {
		t.Fatalf("the declared gate has drifted: %%s", declared)
	}
}
`, t.Name()))

	// Backdated, because cmd/go will not cache a result whose inputs were
	// modified during or just before the run that produced it — a file written
	// moments ago could still be being written. Measured: without this, the
	// module below never caches and this test could only ever skip. It is also
	// why a gate caches at all in practice, which is worth saying: `git
	// checkout` rewrites only the files that DIFFER between the slot's last
	// commit and this one, so the packages a tick did not touch keep their old
	// timestamps and stay cacheable, and the ones it did touch were going to
	// re-run anyway.
	backdate(t, module)

	cold, ok := goTestAllowingFailure(t, module)
	if !ok || strings.Contains(cold, "(cached)") {
		t.Fatalf("the first run of a package this test just wrote was not a fresh pass:\n%s", cold)
	}
	warm, ok := goTestAllowingFailure(t, module)
	if !ok {
		t.Fatalf("the second run failed:\n%s", warm)
	}
	if !strings.Contains(warm, "(cached)") {
		// Not an error about soundness — it is go declining to cache this shape
		// at all, which is mbv's second experiment and is safe. But then the
		// assertion below proves nothing, so say so rather than pass quietly.
		t.Skipf("go declined to cache a guard-shaped test, so nothing here can hide a stale pass:\n%s", warm)
	}

	// The edit the guards exist to catch.
	write(t, input, "declared = \"something else entirely\"\n")
	after, ok := goTestAllowingFailure(t, module)
	if ok {
		t.Fatalf("the guard PASSED after its input changed under it — a cached pass hid the edit, and the "+
			"three drift guards this repository runs over .tick/runners.toml are not guarding (ticks mbv, "+
			"6wh):\n%s", after)
	}
	if strings.Contains(after, "(cached)") {
		t.Errorf("go answered from cache after the guard's input changed:\n%s", after)
	}
}

// backdate puts every file in a tree an hour into the past, so that cmd/go
// treats it as settled input rather than as something still being written.
func backdate(t *testing.T, root string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, past, past)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// goTestAllowingFailure is goTest for a run whose failure is the answer.
func goTestAllowingFailure(t *testing.T, dir string) (output string, passed bool) {
	t.Helper()
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}
