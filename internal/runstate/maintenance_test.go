package runstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	looseObject(t, repo, "control")
	before := packCount(t, repo)
	gitRun(t, repo, "fetch", "--quiet", "origin")
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
