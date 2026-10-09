package gatescope_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/gatescope"
	"github.com/pengelbrecht/ticfac/internal/gittest"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The check against a real module in a real git repository: a real `go list`
// graph, a real `git diff`, and a real `go test` whose failure the check has
// to answer for. The selection's pure halves are tested over fixed inputs in
// gatescope_test.go; this file is the whole pipeline, including the two
// properties the gate exists for — a tick that breaks a full suite the short
// suite skips is refused, and a second gate over an unchanged tree answers
// from Go's own test cache.
//
// The fixture's module is deliberately tiny and dependency-free: its cost is
// the toolchain's own startup, not its tests'.

// alphaGo is the package a tick changes. Answer is what the tick breaks: the
// test asserts it, and only runs when -short is NOT set — dz1's shape, a
// regression the whole-repo short suite cannot see.
const alphaGo = `package alpha

// Answer is what a tick's change can break.
const Answer = 1
`

const alphaTestGo = `package alpha

import "testing"

// The end-to-end shape: this test skips under -short, so the short suite
// stays green over a change that breaks it.
func TestAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("the full suite's test, skipped by the short suite")
	}
	if Answer != 1 {
		t.Fatalf("Answer = %d, want 1: the tick broke it", Answer)
	}
}
`

// beta's TESTS import alpha and its code imports nothing of it: the reverse
// closure has to reach it over the test-import edge alone.
const betaGo = `package beta

// Beta builds nothing of alpha; its tests do.
`

const betaTestGo = `package beta

import (
	"testing"

	"example.com/probe/alpha"
)

func TestAlphasAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("the full suite's test, skipped by the short suite")
	}
	if alpha.Answer != 1 {
		t.Fatalf("alpha.Answer = %d, want 1", alpha.Answer)
	}
}
`

// newProbeRepo builds the probe: a git repository holding a module of two
// packages, one commit at base and one tick-shaped commit on top whose change
// breaks alpha's full-suite test while the short suite skips it. It answers
// the repository's directory and the two commits to diff.
func newProbeRepo(t *testing.T, tickChange func(dir string)) (dir, base, tick string) {
	t.Helper()
	shorttest.EndToEnd(t)

	root := t.TempDir()
	dir = filepath.Join(root, "probe")
	gittest.Run(t, root, "init", "--quiet", "-b", "main", dir)

	write := func(path, content string) {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/probe\n\ngo 1.21\n")
	write("alpha/alpha.go", alphaGo)
	write("alpha/alpha_test.go", alphaTestGo)
	write("beta/beta.go", betaGo)
	write("beta/beta_test.go", betaTestGo)
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "--quiet", "-m", "base")

	tickChange(dir)
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "--quiet", "-m", "the tick's change")

	return dir,
		strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD~1")),
		strings.TrimSpace(gittest.Run(t, dir, "rev-parse", "HEAD"))
}

// breakAlpha is the tick: it changes the package the way dz1's tick changed
// the pinned version — a one-line change to code a full-suite test asserts
// against — and adds a file no package owns, to show the selection is the
// packages' files and not the diff's size.
func breakAlpha(dir string) {
	if err := os.WriteFile(filepath.Join(dir, "alpha", "alpha.go"),
		[]byte(strings.Replace(alphaGo, "const Answer = 1", "const Answer = 2", 1)), 0o644); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "note.md"), []byte("a file no package owns\n"), 0o644); err != nil {
		panic(err)
	}
}

// The gate's diff pair, as the reconciler would export it.
func exported(base, head string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		switch name {
		case gatescope.EnvBase:
			return base, true
		case gatescope.EnvHead:
			return head, true
		}
		return "", false
	}
}

func TestTheCheckRefusesATickThatBreaksAFullSuiteTheShortSuiteSkips(t *testing.T) {
	shorttest.EndToEnd(t)
	dir, base, tick := newProbeRepo(t, breakAlpha)

	// The whole-repo short suite stays GREEN over the tick's change: the
	// break is invisible to the half the gate already runs, which is the
	// shape this check exists to catch.
	t.Chdir(dir)
	if out, err := gatescope.NewShell()("go", "test", "-short", "./..."); err != nil {
		t.Fatalf("the short suite refused the tick's tree, which is not this test's shape: %v\n%s", err, out)
	}
	if out, _ := gatescope.NewShell()("go", "test", "-short", "-v", "./alpha/"); !strings.Contains(out, "SKIP") {
		t.Fatalf("alpha's test did not skip under -short, so the fixture is not dz1's shape:\n%s", out)
	}

	// The check: the gate's exported pair, the real selection, the real
	// suites. alpha for the tick's change, beta because its TESTS import
	// alpha — the reverse-dep edge dz1 rode in on.
	got := &strings.Builder{}
	code := gatescope.Run(gatescope.Options{Timeout: "10m", Parallel: "4"},
		exported(base, tick), gatescope.NewShell(), got)
	if code == 0 {
		t.Fatalf("the check passed over a tick that broke alpha's full suite:\n%s", got)
	}
	for _, want := range []string{"example.com/probe/alpha", "example.com/probe/beta"} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("the selection does not name %s:\n%s", want, got)
		}
	}
	if !strings.Contains(got.String(), "FAIL") {
		t.Errorf("the check passed without go test's own failure in its output:\n%s", got)
	}
}

func TestTheCheckIsServedFromGosTestCacheTheSecondTimeOverTheSameTree(t *testing.T) {
	shorttest.EndToEnd(t)
	// A tick whose change does not break anything: the assertion is about
	// the second run, not the verdict.
	dir, base, tick := newProbeRepo(t, func(dir string) {
		change := strings.Replace(alphaGo, "what a tick's change can break", "still what a tick's change can break", 1)
		if err := os.WriteFile(filepath.Join(dir, "alpha", "alpha.go"), []byte(change), 0o644); err != nil {
			panic(err)
		}
	})
	t.Chdir(dir)

	first := &strings.Builder{}
	if code := gatescope.Run(gatescope.Options{Timeout: "10m", Parallel: "4"},
		exported(base, tick), gatescope.NewShell(), first); code != 0 {
		t.Fatalf("the check refused a passing tick:\n%s", first)
	}
	if strings.Contains(first.String(), "(cached)") {
		t.Fatalf("the first run over a package this test just changed was served from cache:\n%s", first)
	}
	second := &strings.Builder{}
	if code := gatescope.Run(gatescope.Options{Timeout: "10m", Parallel: "4"},
		exported(base, tick), gatescope.NewShell(), second); code != 0 {
		t.Fatalf("the second check refused a passing tick:\n%s", second)
	}
	// Evidence keyed as today, so an unchanged package is a cache hit: the
	// check runs without -count=1, and a re-gate over a tree the packages'
	// inputs did not change answers from Go's own cache rather than paying
	// for the suites again.
	if !strings.Contains(second.String(), "(cached)") {
		t.Errorf("a second check over an unchanged tree re-ran the suites:\n%s", second)
	}
}
