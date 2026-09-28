// Package registrytest guards the machine's real run registry from the
// test suites that exercise the code that writes it (tick 7ag).
//
// THE PROBLEM IT SETTLES. A test that claims a run — runlife.Claim, or any
// child process a test spawns that runs a run-epic — writes a machine-local
// registration into runregistry's directory, which unredirected is the
// operator's real ~/.ticfac/registry. eih redirected internal/cli's suite
// and runlife redirects its own, but the redirect only holds while the
// environment carries it: a child spawned with a hand-built environment, or
// any path that loses TICFAC_REGISTRY_DIR, still writes the real registry,
// and nothing notices until the stray surfaces in the bare `ticfac`
// overview as a phantom run pointing at a temp checkout that is gone.
//
// THE GUARD. GuardMain is the whole TestMain of a package whose tests claim
// runs. Before any test runs it gives the process its own private temp root
// — TMPDIR moves there, so every t.TempDir() fixture and every child that
// inherits the environment lands under one root this process can be named
// by — and points the run registry at a directory under it. After the last
// test it scans the operator's REAL registry for registrations whose repo
// lies under that root: those, and only those, are this run's writes, and
// one of them fails the package.
//
// WHY THE SCAN IS ATTRIBUTED AND NOT A SNAPSHOT DIFF. The factory host runs
// sibling tick suites concurrently, and a sibling whose branch predates the
// redirect writes the same shared registry during this suite's window —
// observed live during this tick's baseline run, seven entries rewritten by
// two sibling attempts. A guard that fails on ANY change to the real
// registry would fail every innocent tree on such a host: a verdict keyed
// on a directory the whole host shares is a host fixture, and the host is
// not bounded. Attributing by the private temp root makes the verdict about
// this tree alone — and on a single-writer host (CI, a quiet machine) it is
// exactly the byte-for-byte unchanged the acceptance names, because there
// the only writer there can be is this run.
//
// Use it from the registering package's TestMain:
//
//	func TestMain(m *testing.M) { registrytest.GuardMain(m) }
//
// internal/cli and internal/runlife hold the two suites that claim runs;
// any package that joins them must take this TestMain with them.
//
// AND THE HALF THAT IS NOT OPT-IN. A package that forgets this TestMain —
// or a child spawned with an environment that dropped the redirect — is not
// left to the scan: runregistry itself refuses a test binary with no
// redirect any read or write of the operator's registry, by a panic that
// fails the package at the line that lost it (runregistry's
// refuseTheOperatorsRegistryUnderTest). The scan remains for the one writer
// that refusal cannot see: a NON-test binary a test builds and runs (`go
// build ./cmd/ticfac`), whose leak still names a repo under this root.
package registrytest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// GuardMain runs a package's tests with the run registry redirected away
// from the operator's home, and fails the package when the real registry
// holds a registration this test run wrote. Call it as the whole TestMain of
// a package whose tests claim or register runs.
func GuardMain(m *testing.M) {
	root, err := setup()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()

	operator := operatorDir()
	leaks := scan(operator, root)
	if len(leaks) > 0 {
		report(operator, root, leaks)
		code = verdict(code, leaks)
	}

	_ = os.RemoveAll(root)
	os.Exit(code)
}

// setup gives this process a private temp root and points the run registry
// at a directory under it. The root is made under the temp directory the
// process STARTED with — before TMPDIR moves — so the root is never inside
// itself, and every directory a test makes afterwards, through t.TempDir()
// or a child that inherits the environment, names the root in its path:
// that is the attribution the guard's scan reads.
func setup() (string, error) {
	root, err := os.MkdirTemp("", "ticfac-registry-guard-")
	if err != nil {
		return "", fmt.Errorf("give this test process its own temp root: %w", err)
	}
	os.Setenv("TMPDIR", root)
	os.Setenv(runregistry.RegistryDirEnv, filepath.Join(root, "registry"))
	return root, nil
}

// operatorDir names the operator's real registry directory — the one
// runregistry itself falls back to with no redirect, derived from it rather
// than spelled here, so the two cannot drift.
func operatorDir() string {
	return runregistry.OperatorDir()
}

// scan reads the operator's real registry and returns the registrations this
// test run wrote: those whose repo names a directory under the private temp
// root setup gave this process. Everything else — a sibling suite's write,
// a run a person started, a file that is not a registration — is not this
// run's write and is left alone. An absent registry holds nothing; a file
// that cannot be decoded is nobody's write and is skipped rather than
// failing the scan, because a guard that cannot read past a corrupt entry
// cannot guard anything.
func scan(operatorDir, root string) []runregistry.Registration {
	rootCanon := canonical(root)
	entries, err := os.ReadDir(operatorDir)
	if err != nil {
		return nil // no registry yet: nothing this run could have written
	}
	leaks := []runregistry.Registration{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(operatorDir, entry.Name()))
		if err != nil {
			continue
		}
		var reg runregistry.Registration
		if err := json.Unmarshal(raw, &reg); err != nil || reg.Repo == "" {
			continue
		}
		if under(canonical(reg.Repo), rootCanon) {
			leaks = append(leaks, reg)
		}
	}
	return leaks
}

// canonical is a path with its symlinked prefixes resolved, for comparison,
// even when the path itself no longer stands — the shape of a stray
// registration is precisely one naming a temp dir that is gone. On macOS
// every temp path is reached through links (/var → /private/var,
// /tmp → /private/tmp), and a test that resolves its fixture's symlinks
// before claiming writes the resolved spelling into the registration, so
// two spellings of one directory must compare equal: both sides are
// canonicalized, and the longest ancestor that still stands is resolved
// when the path itself does not.
func canonical(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	// The path is gone. Walk up to the longest ancestor that stands,
	// resolve that, and carry the rest along unchanged.
	tail := ""
	for {
		parent, base := filepath.Split(path)
		parent = filepath.Clean(parent)
		tail = filepath.Join(base, tail)
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Clean(filepath.Join(resolved, tail))
		}
		if parent == path {
			// The filesystem root itself will not resolve; nothing better
			// than the path as given remains.
			return filepath.Clean(filepath.Join(path, tail))
		}
		path = parent
	}
}

// under says whether path lies inside root (or is root itself).
func under(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// verdict is the exit code the guard leaves: a leak fails a suite that would
// otherwise have passed, and never masks a failure the tests already made.
func verdict(code int, leaks []runregistry.Registration) int {
	if len(leaks) == 0 || code != 0 {
		return code
	}
	return 1
}

// report says what leaked, where, and the one rule that was broken.
func report(operatorDir, root string, leaks []runregistry.Registration) {
	var b strings.Builder
	fmt.Fprintf(&b, "this test run left %d registration(s) in the operator's real registry %s:\n",
		len(leaks), operatorDir)
	for _, reg := range leaks {
		fmt.Fprintf(&b, "  %s -> %s\n", reg.RunID, reg.Repo)
	}
	fmt.Fprintf(&b, "each names a directory under this process's own temp root %s, so it is this "+
		"run's write: a test or a child it spawned claimed or registered a run through an "+
		"environment that lost %s. Every claim a test makes must go through the redirected "+
		"registry, and every child that can claim must inherit the variable.\n",
		root, runregistry.RegistryDirEnv)
	fmt.Fprint(os.Stderr, b.String())
}
