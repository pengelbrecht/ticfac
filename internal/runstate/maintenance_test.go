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

// unpinnedGitRun is gitRun WITHOUT the env-config pins: gitbin.WithoutPinnedConfig
// strips GIT_CONFIG_COUNT and its numbered pairs — git reads those above every
// config file, so they survive GIT_CONFIG_GLOBAL=/dev/null — and the two /dev/null
// config files bound the host the way gitRun's do. It exists for this file's
// CONTROL only: an assertion that a particular git starts no maintenance is
// worth nothing on a machine where no git would, and whether this machine is
// one is a property of the ENVIRONMENT, not of the tree. The store's own fetch
// (the assertion) runs as production runs it, pins and all.
func unpinnedGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitbin.Path(), args...)
	cmd.Dir = dir
	cmd.Env = append(gitbin.WithoutPinnedConfig(os.Environ()),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
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
