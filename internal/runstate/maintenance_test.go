package runstate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// The store's fetch starts no maintenance in the repository the store writes
// its records into (tick mel).
//
// Every write this store makes is a blob, a tree and a commit in that
// repository's object store, and a fetch that ended by starting `git
// maintenance run --auto --detach` there started a background repack whose
// prune-packed removes the object fan-out directories under those writes. On
// CI's git (2.55, geometric by default) that failed a write with "unable to
// create temporary file: No such file or directory"; see gitbin.
// NoAutoMaintenance, and internal/reconcile's test of the same name for why
// what is asserted here is the trigger rather than the collision.
func TestTheStoresFetchStartsNoMaintenanceInTheRepositoryItWritesTo(t *testing.T) {
	t.Parallel()
	o := newOrigin(t)
	s := o.actor("a", "r-mel")
	repo := s.git.dir

	// Any automatic maintenance runs before the command that started it
	// returns, and always packs: "started one" becomes a pack on disk.
	gitRun(t, repo, "config", "maintenance.autoDetach", "false")
	gitRun(t, repo, "config", "maintenance.loose-objects.enabled", "true")
	gitRun(t, repo, "config", "maintenance.loose-objects.auto", "-1")

	// The control: a plain fetch in this repository does start it.
	//
	// unpinnedGitRun, not gitRun: the control describes the OPERATOR'S git, and
	// gitRun's environment carries whatever GIT_CONFIG_COUNT pins the test
	// process itself was started under — the read-only source grade pins
	// maintenance.auto=false and gc.auto=0 there, git reads those above every
	// config file, and GIT_CONFIG_GLOBAL=/dev/null does not neutralise them. A
	// control run under them starts no maintenance, and this fixture then
	// reports the environment's property as the tree's failure — which is the
	// failure this tick was filed over, again, on a host that pins its workers
	// this way. internal/gitbin's and internal/reconcile's same-named fixtures
	// run their controls unpinned for the same reason.
	looseObject(t, repo, "control")
	before := packCount(t, repo)
	unpinnedGitRun(t, repo, "fetch", "--quiet", "origin")
	if packCount(t, repo) == before {
		t.Fatal("a plain `git fetch` in the armed repository started no maintenance; this fixture proves nothing")
	}

	looseObject(t, repo, "the store")
	before = packCount(t, repo)
	if _, err := s.Fetch(); err != nil {
		t.Fatal(err)
	}
	if after := packCount(t, repo); after != before {
		t.Fatalf("the store's fetch started git's automatic maintenance in the repository it writes into "+
			"(%d packs before, %d after): in the background, that repack races the store's own writes (tick mel)",
			before, after)
	}
}

// unpinnedGit is a git run WITHOUT the fixture's pins: an operator's own
// command, which is a git that DOES end by starting background maintenance.
//
// It exists for the CONTROLS that need one — this file's (tick mel), and the
// trace guard in maintenance_guard_test.go (tick 35l) — and both are the
// same argument: an assertion that a particular git starts no maintenance is
// worth nothing on a machine where no git would have. Nothing else in this
// package may build a git command; TestEveryGitTheTestsStartGoesThroughThePinnedRunner
// is what says so.
//
// The GIT_CONFIG_COUNT pins are stripped rather than merely overridden by
// GIT_CONFIG_GLOBAL=/dev/null, because git reads the env-config entries
// ABOVE every config file; and the two /dev/null config files keep the
// host's own global config out of the control as well, for the same reason
// in the other direction: whether a plain git starts maintenance is a
// property of the ENVIRONMENT, and a control under a host that pins or arms
// it measures nothing and reports it as the tree's failure.
func unpinnedGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(gitbin.Path(), args...)
	cmd.Dir = dir
	cmd.Env = append(gitbin.WithoutPinnedConfig(os.Environ()),
		"GIT_AUTHOR_NAME=ticfac test", "GIT_AUTHOR_EMAIL=ticfac@example.com",
		"GIT_COMMITTER_NAME=ticfac test", "GIT_COMMITTER_EMAIL=ticfac@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0")
	return cmd
}

// unpinnedGitRun is unpinnedGit with a helper's error reporting: fatal
// rather than return, because a control git that fails says the fixture
// cannot prove what it is about to assert.
func unpinnedGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := unpinnedGit(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// looseObject writes one new loose object, so a maintenance that runs has
// something to pack.
func looseObject(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "object")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "hash-object", "-w", path)
}

// packCount is how many packfiles the repository's object store holds.
func packCount(t *testing.T, dir string) int {
	t.Helper()
	common := strings.TrimSpace(gitRun(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	entries, err := os.ReadDir(filepath.Join(common, "objects", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".pack") {
			n++
		}
	}
	return n
}
