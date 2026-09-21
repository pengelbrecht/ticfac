package reconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The run starts no maintenance in the repository it writes its records into
// (tick mel).
//
// The bug this pins: every fetch and merge the reconciler ran ended with git
// starting `git maintenance run --auto --detach` in the run's own repository.
// On a git whose default is the geometric strategy (CI's 2.55) that is a
// background `git repack -d` as soon as objects/17 holds two loose objects,
// and its prune-packed removes the object fan-out directories it empties. A
// tracker publish writing a tree at the same moment lost the directory
// between git creating it and git creating a temporary file inside it:
//
//	git write-tree: exit status 128: error: unable to create temporary file:
//	No such file or directory
//
// That collision is inside git — between one mkdir and one mkstemp — so no
// test outside git can land a repack there on demand. What a test CAN make
// deterministic is the thing the run controls: whether it starts the repack
// at all. The repository below is configured so that any automatic
// maintenance runs in the FOREGROUND and always packs, which turns "started a
// maintenance" into a pack that exists when the command returns.
func TestTheRunStartsNoMaintenanceInTheRepositoryItWritesTo(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)

	// Something to merge, made before the repository is armed: the fixture's
	// own commit is not what is under test.
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "-b", "side")
	write(t, filepath.Join(repo.Dir, "side.txt"), "side\n")
	mustRun(t, repo.Dir, "git", "add", "side.txt")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "side")
	side := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "main")

	armMaintenance(t, repo.Dir)

	// The control: a plain git in this repository DOES start it. Without this
	// a git that ignored the arming would pass the assertion below for free.
	//
	// unpinnedGit, not mustRun: since tick qsn the harness's own runner
	// carries the same pins the reconciler's git does, so a control run
	// through it would prove nothing about the arming. This is the operator's
	// git, which is what the control was always describing.
	looseObject(t, repo.Dir, "control")
	before := packCount(t, repo.Dir)
	mustSucceed(t, unpinnedGit(repo.Dir, "fetch", "--quiet", "origin"))
	if packCount(t, repo.Dir) == before {
		t.Fatal("a plain `git fetch` in the armed repository started no maintenance; this fixture proves nothing")
	}

	looseObject(t, repo.Dir, "the run")
	before = packCount(t, repo.Dir)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	// The reconciler's fetch: the store's integration branch, and the same
	// path the tracker's originHead reads origin through.
	if err := g.fetch("main"); err != nil {
		t.Fatal(err)
	}
	if after := packCount(t, repo.Dir); after != before {
		t.Fatalf("the reconciler's fetch started git's automatic maintenance in the run's repository "+
			"(%d packs before, %d after): in the background, that repack races every tree the run writes "+
			"and fails one with \"unable to create temporary file\" (tick mel)", before, after)
	}

	// The reconciler's merge, in a throwaway worktree exactly as integrate
	// and refresh make one.
	dir, remove, err := g.tempWorktree("ticfac-mel-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if _, _, err := g.try(dir, "merge", "--no-ff", "--no-edit", "-m", "merge side", side); err != nil {
		t.Fatal(err)
	}
	if after := packCount(t, repo.Dir); after != before {
		t.Fatalf("the reconciler's merge started git's automatic maintenance in the run's repository "+
			"(%d packs before, %d after) (tick mel)", before, after)
	}
}

// armMaintenance configures a repository so that git's automatic maintenance,
// whenever a command starts it, runs before that command returns and always
// has work to do: the loose-objects task, forced on (-1 is "always"), packs
// every loose object. The same configuration means the same thing to a git
// whose default strategy is gc and to one whose default is geometric.
func armMaintenance(t *testing.T, dir string) {
	t.Helper()
	mustRun(t, dir, "git", "config", "maintenance.autoDetach", "false")
	mustRun(t, dir, "git", "config", "maintenance.loose-objects.enabled", "true")
	mustRun(t, dir, "git", "config", "maintenance.loose-objects.auto", "-1")
}

// looseObject writes one new loose object, so a maintenance that runs has
// something to pack and cannot pass unnoticed as a no-op.
func looseObject(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "object")
	write(t, path, content+"\n")
	mustRun(t, dir, "git", "hash-object", "-w", path)
}

// packCount is how many packfiles the repository's object store holds.
func packCount(t *testing.T, dir string) int {
	t.Helper()
	common := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"))
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
