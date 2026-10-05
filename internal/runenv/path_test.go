package runenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// executable writes an executable file named name in dir.
func executable(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// lookIn resolves name on path, as exec.LookPath does on PATH.
func lookIn(path, name string) (string, bool) {
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, true
		}
	}
	return "", false
}

func TestTicfacsOwnBinariesAreTheOnesHidden(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess", "ticfac-dash"} {
		if !IsTicfacBinary(name) {
			t.Errorf("%s is one of ticfac's binaries, but IsTicfacBinary says it is not", name)
		}
	}
	for _, name := range []string{"tk", "herdr", "claude", "go", "ticfacx", "my-ticfac"} {
		if IsTicfacBinary(name) {
			t.Errorf("%s is not ticfac's, but IsTicfacBinary takes it", name)
		}
	}
}

// A PATH directory holding an installed ticfac — the operator's
// ~/.local/bin — keeps every other tool it holds, in its place in PATH, and
// loses only ticfac's own binaries. A directory without one is untouched.
func TestHidingTicfacKeepsEveryOtherToolInItsPlace(t *testing.T) {
	t.Parallel()
	installed, plain, mirrors := t.TempDir(), t.TempDir(), t.TempDir()
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess", "ticfac-dash", "tk", "herdr", "with space"} {
		executable(t, installed, name)
	}
	executable(t, plain, "go")
	executable(t, plain, "herdr") // shadowed by installed's, before and after

	path := strings.Join([]string{plain, installed}, string(os.PathListSeparator))
	got, hidden, err := HideTicfacBinaries(path, mirrors)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 1 || hidden[0] != installed {
		t.Errorf("hid %v, want exactly the directory holding ticfac (%s)", hidden, installed)
	}
	dirs := filepath.SplitList(got)
	if len(dirs) != 2 || dirs[0] != plain {
		t.Fatalf("PATH became %q: a directory without ticfac must keep its place", got)
	}
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess", "ticfac-dash"} {
		if found, ok := lookIn(got, name); ok {
			t.Errorf("%s still resolves, to %s: the host's installed ticfac answers for the tree", name, found)
		}
	}
	for name, want := range map[string]string{"tk": installed, "with space": installed, "go": plain, "herdr": plain} {
		found, ok := lookIn(got, name)
		if !ok {
			t.Errorf("%s no longer resolves: hiding ticfac took the directory's other tools with it", name)
			continue
		}
		if real, _ := filepath.EvalSymlinks(found); filepath.Dir(real) != mustEval(t, want) {
			t.Errorf("%s resolves to %s (%s), want the copy in %s", name, found, real, want)
		}
	}

	// Asked again over the same directory, the mirror is reused, not remade.
	again, _, err := HideTicfacBinaries(path, mirrors)
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Errorf("a second hide made %q, want the first mirror %q reused", again, got)
	}
}

func mustEval(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// TestATestBinaryDoesNotSeeTheHostsInstalledTicfac starts this test binary
// with a PATH whose directory holds an installed ticfac — the operator's Mac,
// where hn6's gate passed two tests GitHub CI failed — and asks it what its
// tests can resolve: none of ticfac's binaries, every other tool.
func TestATestBinaryDoesNotSeeTheHostsInstalledTicfac(t *testing.T) {
	t.Parallel()
	installed := t.TempDir()
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess", "tk"} {
		executable(t, installed, name)
	}
	scrubbed, _ := Scrub(os.Environ())
	var env []string
	for _, entry := range scrubbed {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "TMPDIR=") {
			env = append(env, entry)
		}
	}
	env = append(env, pathChildEnv+"=1", "TMPDIR="+t.TempDir(),
		"PATH="+installed+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command(os.Args[0], "-test.run", "^TestPathIsolationChild$", "-test.count=1", "-test.v")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, out)
	}
	seen := ""
	for _, line := range strings.Split(string(out), "\n") {
		if _, s, ok := strings.Cut(line, "seen: "); ok {
			seen = strings.TrimSpace(s)
		}
	}
	if want := "ticfac=absent ticfac-exec-subprocess=absent tk=found"; seen != want {
		t.Errorf("a test binary started with an installed ticfac on PATH saw %q, want %q\n%s", seen, want, out)
	}
}

// pathChildEnv marks this binary re-exec'd by the PATH isolation test.
const pathChildEnv = "RUNENV_TEST_PATH_CHILD"

// TestPathIsolationChild is the child's half; it does nothing in a normal run.
func TestPathIsolationChild(t *testing.T) {
	t.Parallel()
	if os.Getenv(pathChildEnv) == "" {
		t.Skip("run only as the PATH isolation test's child")
	}
	var parts []string
	for _, name := range []string{"ticfac", "ticfac-exec-subprocess", "tk"} {
		state := "absent"
		if _, err := exec.LookPath(name); err == nil {
			state = "found"
		}
		parts = append(parts, name+"="+state)
	}
	t.Logf("seen: %s", strings.Join(parts, " "))
}
