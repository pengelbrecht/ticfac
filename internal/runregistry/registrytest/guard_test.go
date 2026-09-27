package registrytest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// The guard is the half of tick 7ag the redirect cannot be: the redirect
// (eih) points a package's claims at a temp registry, but a test that spawns
// a child with a hand-built environment — or a claim that reaches runregistry
// through any path that loses TICFAC_REGISTRY_DIR — still writes the
// operator's real ~/.ticfac/registry, and nothing notices until the stray
// surfaces in the bare `ticfac` overview as a phantom run. These tests hold
// the guard's two halves: it must flag a registration this test run wrote,
// and it must leave every other writer's registration alone — the second
// because the factory host runs sibling tick suites concurrently, and a
// guard that fails a tree for a sibling's write fails innocent trees.

// A registration whose repo names a directory under this process's own temp
// root is this run's write: the guard flags it.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestScanFlagsARegistrationThisRunWrote(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	operator := t.TempDir()
	t.Setenv(runregistry.RegistryDirEnv, operator)
	if err := runregistry.Register("r-leak", repo); err != nil {
		t.Fatal(err)
	}

	leaks := scan(operator, root)
	if len(leaks) != 1 {
		t.Fatalf("scan found %d leaks, want the one registration this run wrote:\n%v", len(leaks), leaks)
	}
	if leaks[0].RunID != "r-leak" {
		t.Errorf("the leak reads as run %q, want r-leak", leaks[0].RunID)
	}
}

// A registration written by ANOTHER process — a sibling tick suite on the
// same factory host — names that process's temp root, never this one, and
// the guard leaves it alone: a verdict keyed on the shared host registry
// would fail every innocent tree on a host where siblings run.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestScanLeavesForeignRegistrationsAlone(t *testing.T) {
	mine := t.TempDir()
	foreign := t.TempDir()
	repo := filepath.Join(foreign, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	operator := t.TempDir()
	t.Setenv(runregistry.RegistryDirEnv, operator)
	if err := runregistry.Register("r-sibling", repo); err != nil {
		t.Fatal(err)
	}

	if leaks := scan(operator, mine); len(leaks) != 0 {
		t.Fatalf("scan flagged a sibling's registration as this run's write:\n%v", leaks)
	}
}

// A registration may be written through a symlinked spelling of the temp
// root — on macOS /tmp and /var are links into /private — and the guard
// must still recognise it, because a test that resolves its fixture's
// symlinks before claiming (the evacuation test does) writes the resolved
// spelling into the registration.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestScanReadsARegistrationThroughItsSymlinkedSpelling(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this host cannot make a symlink: %v", err)
	}
	root := filepath.Join(link, "inner")
	repo := filepath.Join(real, "inner", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	operator := t.TempDir()
	t.Setenv(runregistry.RegistryDirEnv, operator)
	if err := runregistry.Register("r-link", repo); err != nil {
		t.Fatal(err)
	}

	if leaks := scan(operator, root); len(leaks) != 1 {
		t.Fatalf("scan found %d leaks through the symlinked root, want the one this run wrote:\n%v",
			len(leaks), leaks)
	}
}

// The shape the guard exists for: a stray registration names a temp dir that
// is already gone — every gate run leaves exactly that behind when a test
// writes the real registry — and the guard must still attribute it, because
// a path that stopped existing is not a licence to stop reading it.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestScanAttributesARegistrationWhoseRepoIsGone(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	operator := t.TempDir()
	t.Setenv(runregistry.RegistryDirEnv, operator)
	if err := runregistry.Register("r-gone", repo); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	leaks := scan(operator, root)
	if len(leaks) != 1 {
		t.Fatalf("scan found %d leaks for a registration naming a deleted repo, want 1:\n%v",
			len(leaks), leaks)
	}
	if leaks[0].RunID != "r-gone" {
		t.Errorf("the leak reads as run %q, want r-gone", leaks[0].RunID)
	}
}

// A machine with no registry yet has nothing this run could have written,
// and an entry that cannot be decoded is nobody's write: the guard reads
// past both without failing, because a guard that crashes on its own
// reading cannot guard anything.
func TestScanIgnoresAnAbsentRegistryAndUndecodableEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if leaks := scan(filepath.Join(t.TempDir(), "absent"), root); len(leaks) != 0 {
		t.Fatalf("an absent registry reported leaks: %v", leaks)
	}

	operator := t.TempDir()
	if err := os.WriteFile(filepath.Join(operator, "r-corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if leaks := scan(operator, root); len(leaks) != 0 {
		t.Fatalf("an undecodable entry reported leaks: %v", leaks)
	}
}

// setup gives this process its own temp root and points the run registry at
// a directory under it: every t.TempDir() a test creates — and every child
// that inherits the environment — lands under the one root the guard can
// then attribute, and every claim lands in the redirected registry alone.
//
// Serial: it moves TMPDIR and rewrites the registry's environment.
func TestSetupRedirectsTheRegistryAndOwnsTheTempRoot(t *testing.T) {
	beforeTemp := os.Getenv("TMPDIR")
	beforeRegistry := os.Getenv(runregistry.RegistryDirEnv)

	root, err := setup()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Setenv("TMPDIR", beforeTemp)
		os.Setenv(runregistry.RegistryDirEnv, beforeRegistry)
		os.RemoveAll(root)
	})

	if got := runregistry.Dir(); got != filepath.Join(root, "registry") {
		t.Errorf("the registry writes to %q, want the redirected %q", got, filepath.Join(root, "registry"))
	}
	probe, err := os.MkdirTemp("", "probe-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(probe)
	if !under(probe, root) {
		t.Errorf("a temp dir created after setup sits at %s, outside this process's own root %s: "+
			"attribution depends on every fixture landing under the root", probe, root)
	}
}

// The guard's verdict: a leak fails a suite that would otherwise pass, and
// never masks a test failure that already failed it.
func TestTheVerdictFailsOnlyWhenSomethingLeaked(t *testing.T) {
	t.Parallel()
	if got := verdict(0, nil); got != 0 {
		t.Errorf("a clean suite exits %d, want 0", got)
	}
	leaked := []runregistry.Registration{{RunID: "r-leak", Repo: "/gone/tmp/repo"}}
	if got := verdict(0, leaked); got != 1 {
		t.Errorf("a suite whose tests leaked a registration exits %d, want 1", got)
	}
	if got := verdict(3, leaked); got != 3 {
		t.Errorf("a suite that already failed keeps its own exit code: got %d, want 3", got)
	}
}
