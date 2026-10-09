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
// THE WIDER AUDIT (tick mbv, 2026-10-07). 6wh's answer above covers one
// shape: a file inside the module, read in-process. The audit this tick was
// dispatched for measured every shape a gate-visible guard's input can have,
// against cmd/go's own input hashing (cmd/go/internal/test/test.go,
// computeTestInputsID, this machine's go 1.26.2) and against this repository
// itself. Three truths, each pinned by a test in this file:
//
//  1. A file inside the module, opened in-process: RECHECKED. The cache keys
//     the result on the file's size and mtime, and an edit rewrites both —
//     measured twice, by hand against this repository's own guards on
//     2026-09-20 (edit the `go` gate's command in .tick/runners.toml and
//     TestTheGateTargetMatchesTheDeclaredGate fails, uncached, instead of
//     answering `ok (cached)`) and hermetically by
//     TestACachedPassCannotHideAnEditToAFileAGuardReads on every run. All of
//     .tick/runners.toml's guards have this shape: the file sits at the module
//     root, the guard in a package under it, and the absolute path
//     contracts.RepoRoot() builds changes nothing.
//
//  2. Everything a subprocess reads: INVISIBLE. The test binary logs the
//     files it opens ITSELF; bash's opens are never logged, so no edit to a
//     script a test merely RUNS can invalidate the package. Demonstrated
//     against this repository, not a model: internal/ciwatchdog, warm,
//     answered `ok (cached)` over a .github/scripts/main-ci-watchdog.sh
//     reduced to `exit 1` — every watchdog test would have failed, had
//     anything run them. Pinned by TestACachedPassCanHideAnEditToAFileASubprocessRuns,
//     and fixed in the same tick for the three gate-visible guards that run a
//     repository script through a shell — ciwatchdog's watchdog, factory's
//     wrangler-docker.sh, release's install.sh — which now read the script
//     in-process first, so an edit invalidates the package and the guard
//     re-runs the new script.
//
//  3. A file outside the module, absolute or relative, whatever process
//     reads it: NEVER RECHECKED. Deliberate, in cmd/go's own words: "Do not
//     recheck files outside the module, GOPATH, or GOROOT root". Nothing in
//     the gate reads repository drift from outside the module. The one
//     gate-visible guard whose input is outside the module BY NATURE —
//     wirevocab diffing the pinned herd vocabulary against the installed
//     herdr binary — cannot be fixed by reading its input in-process: no
//     read of an outside-module file is ever rechecked. It is filed as a
//     finding for the -count=1 decision it forces. Pinned by
//     TestACachedPassCanHideAnEditToAFileOutsideTheModule.
//
// What this means for -count=1: the gate keeps running without it — every
// input a drift guard reads is rechecked — and `make suite` stays what it is:
// the paranoid answer, not a required one. The epic's lifecycle still contains
// uncached runs: `make suite` for whoever wants the cache refused, CI's race
// job (`-count=1`, because a cached pass from a non-race build says nothing
// about races), and CI's full `make test` on an ephemeral runner with no
// cache to consult.
//
// The fixture below mirrors the guards exactly rather than approximately: the
// test lives in a package UNDER the module root, and the file it reads is at
// the module root, reached by an absolute path built from the working
// directory. Get either of those wrong and the experiment answers about a
// different shape — mbv found three shapes that disagreed.
// short: a throwaway module of two files and two `go test` runs in it, ~0.7s — and it is the guard that says the gate's own cache cannot serve a stale pass, so every tick wants it
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

// The second shape of the audit: what a SUBPROCESS reads. The test binary
// logs the files it opens itself — cmd/go replays that log to decide whether a
// cached result still stands — and a child process's opens never enter the
// log. So a guard whose only read of its input is `bash script` is a guard go's
// cache cannot see at all: no edit to the script, however fatal, invalidates
// the package. This is not a model — it is internal/ciwatchdog's shape, and it
// was demonstrated against this repository itself at the base of this tick:
// with the package warm, a .github/scripts/main-ci-watchdog.sh reduced to
// `exit 1` was answered with `ok (cached)`, and only `-count=1` revealed that
// every watchdog test fails. That is why the three gate-visible guards that
// run a repository script through a shell (ciwatchdog's watchdog, factory's
// wrangler-docker.sh, release's install.sh) now read their script in-process
// first: this test is the pin on the mechanism that makes those reads
// load-bearing, so removing one of them has to argue with this.
//
// The assertion is deliberately inverted from the test above: it asserts the
// HOLE EXISTS, because the repository's cache rules are written against it.
// If go ever learns to see through subprocesses this fails — which is the
// point; see the message below.
// short: a throwaway module of three files and three `go test` runs in it — measured 1.1s; it is the pin on the second hole, and it says when go's cache outgrows the guards' in-process reads
func TestACachedPassCanHideAnEditToAFileASubprocessRuns(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("no sh on PATH, so the subprocess shape cannot be built here: %v", err)
	}
	module, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(module, ".github", "scripts", "watchdog.sh")
	writeUnder(t, module, ".github/scripts/watchdog.sh", "#!/bin/sh\nexit 0\n")
	write(t, filepath.Join(module, "go.mod"), "module ticfac.example/subprocess\n\ngo 1.21\n")
	// Unique to this test, so a first run of the module below is a cache miss
	// however often the suite has run before.
	writeUnder(t, module, "internal/guard/guard_test.go", fmt.Sprintf(`package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const salt = %q

// The shape under audit: the guard's only read of its input is the
// subprocess that runs it — bash never tells the test binary it opened
// anything.
func TestTheScriptHoldsItsContract(t *testing.T) {
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
	if err := exec.Command("sh", filepath.Join(dir, ".github", "scripts", "watchdog.sh")).Run(); err != nil {
		t.Fatalf("the script failed: %%v", err)
	}
}
`, t.Name()))

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
		// Not an error — it is go declining to cache this shape at all, which is
		// safe. But then the assertion below proves nothing, so say so rather
		// than pass quietly.
		t.Skipf("go declined to cache a guard-shaped test, so nothing here can hide a stale pass:\n%s", warm)
	}

	// The edit the guard exists to catch — delivered to the file only the
	// subprocess reads.
	write(t, script, "#!/bin/sh\nexit 1\n")
	after, ok := goTestAllowingFailure(t, module)
	if !ok || !strings.Contains(after, "(cached)") {
		t.Fatalf("go re-ran a guard whose subprocess input changed — go's test cache learned to see "+
			"through subprocesses. That is a CHANGE to the second truth of this file's audit (tick mbv): "+
			"the in-process reads the ciwatchdog, factory and release guards carry are no longer needed, "+
			"the wirevocab finding may be closable without -count=1, and this file's header comment "+
			"overstates what the cache cannot see. Re-audit, and update the header.\n%s", after)
	}
	// The hole stands, and this is what it costs: the guard is green from cache
	// over a script that fails the moment anything runs it. Only `-count=1` or
	// an in-process read of the script can catch the edit.
	t.Logf("demonstrated: a cached pass survived a script edit that makes every run of it fail:\n%s", after)
}

// The third shape of the audit: a file OUTSIDE the module. cmd/go's input
// hashing skips them on purpose — "Do not recheck files outside the module,
// GOPATH, or GOROOT root" is the comment in computeTestInputsID — so a test
// can read an outside-module input in-process, all day, and no cached result
// of its package ever consults that file again. A drift guard built on such a
// read is a guard that runs once.
//
// Nothing in this gate reads repository drift from outside the module, which
// is why this is a demonstration rather than a defect. The shape is here
// because the repository's one gate-visible guard with an outside-module
// input by nature — wirevocab diffing the pinned herd vocabulary against the
// installed herdr binary — inherits exactly this behaviour, and no read the
// test could perform would change it: outside-module reads are not rechecked,
// in-process or otherwise. That guard's freshness now rests on `make suite`
// and the finding this tick filed for it.
//
// The assertion is inverted from the first test's for the same reason as the
// one above: this asserts the hole exists. If go ever starts rechecking
// outside-module reads, this fails and says what to do.
// short: a throwaway module and its outside input, three `go test` runs — measured 0.9s; it is the pin on the third hole, and it says when go's cache outgrows the module boundary
func TestACachedPassCanHideAnEditToAFileOutsideTheModule(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The module and its input are SIBLINGS: the input is outside the module
	// however the walk-up inside the guard resolves it.
	module := filepath.Join(base, "module")
	input := filepath.Join(base, "input.txt")
	write(t, input, "declared = \"the input as it stands\"\n")
	writeUnder(t, module, "go.mod", "module ticfac.example/outside\n\ngo 1.21\n")
	writeUnder(t, module, "internal/guard/guard_test.go", fmt.Sprintf(`package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const salt = %q

// Reads an input OUTSIDE the module by ABSOLUTE path — the shape a walk-up
// like contracts.RepoRoot() produces when the input is not under the module.
func TestTheOutsideInputIsWhatThisTestExpects(t *testing.T) {
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
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "the input as it stands") {
		t.Fatalf("the outside input drifted: %%q", raw)
	}
}
`, t.Name()))

	backdate(t, base)

	cold, ok := goTestAllowingFailure(t, module)
	if !ok || strings.Contains(cold, "(cached)") {
		t.Fatalf("the first run of a package this test just wrote was not a fresh pass:\n%s", cold)
	}
	warm, ok := goTestAllowingFailure(t, module)
	if !ok {
		t.Fatalf("the second run failed:\n%s", warm)
	}
	if !strings.Contains(warm, "(cached)") {
		t.Skipf("go declined to cache a guard-shaped test, so nothing here can hide a stale pass:\n%s", warm)
	}

	// The edit the guard exists to catch — to the file OUTSIDE the module.
	write(t, input, "declared = \"changed\"\n")
	after, ok := goTestAllowingFailure(t, module)
	if !ok || !strings.Contains(after, "(cached)") {
		t.Fatalf("go re-ran a guard whose input lives outside the module — go's test cache started "+
			"rechecking outside-module reads. That is a CHANGE to the third truth of this file's audit "+
			"(tick mbv): the wirevocab finding may be closable without -count=1, and this file's header "+
			"comment overstates what the cache cannot see. Re-audit, and update the header.\n%s", after)
	}
	t.Logf("demonstrated: a cached pass survived an edit to a file the test reads in-process, because that file is outside the module:\n%s", after)
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
