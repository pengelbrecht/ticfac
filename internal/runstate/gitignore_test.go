package runstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The `.gitignore` fragment, asserted with git itself.
//
// `.ticfac/runs/**` is COMMITTED — it is the run's durable record — and only
// the derived index and the logs are exhaust. A fragment that merely exists
// ignores nothing, so what is checked here is that git applies it: the
// installer writes it into a real repository and `git check-ignore` is asked.

func TestTheFragmentIsTheContracts(t *testing.T) {
	c := loadContract(t)

	if !reflect.DeepEqual(Fragment, c.Gitignore.Fragment) {
		t.Errorf("the fragment this package installs is not the contract's:\n  here:     %q\n  contract: %q",
			Fragment, c.Gitignore.Fragment)
	}
	if BeginMarker != c.Gitignore.BeginMarker || EndMarker != c.Gitignore.EndMarker {
		t.Errorf("the markers are %q/%q here and %q/%q in the contract",
			BeginMarker, EndMarker, c.Gitignore.BeginMarker, c.Gitignore.EndMarker)
	}
	if c.Gitignore.Target != ".gitignore" {
		t.Errorf("the fragment's target is %q", c.Gitignore.Target)
	}
}

func TestEnsureGitignoreIsHonouredByGitCheckIgnore(t *testing.T) {
	c := loadContract(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "--quiet", root)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("# a target repository's own ignores\n/dist\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wrote, err := EnsureGitignore(root)
	if err != nil {
		t.Fatalf("install the fragment: %v", err)
	}
	if !wrote {
		t.Error("the fragment was not written into a .gitignore that did not carry it")
	}

	for _, path := range c.Gitignore.IgnoredExamples {
		if !gitIgnores(t, root, path) {
			t.Errorf("%s must be ignored and git does not ignore it", path)
		}
	}
	for _, path := range c.Gitignore.TrackedExamples {
		if gitIgnores(t, root, path) {
			t.Errorf("%s is the run's durable record and git ignores it — a run that leaves nothing behind", path)
		}
	}

	// The repository's own ignores survive: the fragment is a block inside a
	// file somebody else owns.
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "/dist") {
		t.Error("installing the fragment ate the repository's own .gitignore")
	}

	// Idempotent: a reconciler installs this at the start of every run.
	wrote, err = EnsureGitignore(root)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("the fragment was written twice; a run should not churn the file it found in order")
	}
	after, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(body) {
		t.Errorf("a second install rewrote the file:\n%s", after)
	}
}

// A repository with no .gitignore at all: the file is created, and the fragment
// is all of it.
func TestEnsureGitignoreCreatesTheFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "--quiet", root)

	if _, err := EnsureGitignore(root); err != nil {
		t.Fatal(err)
	}
	if gitIgnores(t, root, RunDir("r-x1w")+"/checkpoint.json") {
		t.Error("a fresh install ignores the run's records")
	}
	if !gitIgnores(t, root, Root+"/.index.json") {
		t.Error("a fresh install does not ignore the derived index")
	}
}

// An out-of-date block is replaced in place, markers and all — the fragment is
// the contract's, not whatever an older ticfac wrote.
func TestEnsureGitignoreReplacesAnOldBlock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on the path")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "--quiet", root)
	stale := "/dist\n" + BeginMarker + "\n.ticfac/\n" + EndMarker + "\n# and a trailing rule\n/build\n"
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	wrote, err := EnsureGitignore(root)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("a stale block was left in place")
	}
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "\n.ticfac/\n") {
		t.Error("the stale rule survived, and it ignores the whole run state")
	}
	if !strings.Contains(string(body), "/build") {
		t.Error("replacing the block ate what came after it")
	}
	if strings.Count(string(body), BeginMarker) != 1 {
		t.Errorf("the file carries the marker %d times:\n%s", strings.Count(string(body), BeginMarker), body)
	}
	if gitIgnores(t, root, RunDir("r-x1w")+"/checkpoint.json") {
		t.Error("the run's records are still ignored after the block was replaced")
	}
}

// ticfac is a ticfac target like any other, so the fragment this package
// installs is the one this repository carries. If the two ever differ, one of
// them is wrong and nothing else would say which.
func TestThisRepositoryCarriesTheInstalledFragment(t *testing.T) {
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(body), strings.Join(Fragment, "\n")) {
		t.Errorf("this repository's .gitignore does not carry the fragment this package installs:\n%s", body)
	}
}

func gitIgnores(t *testing.T, root, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", "-q", "--no-index", path)
	cmd.Dir = root
	err := cmd.Run()
	if err == nil {
		return true
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", path, err)
	return false
}
