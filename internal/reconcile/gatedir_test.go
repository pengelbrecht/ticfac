package reconcile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The gate's directory is REUSED, and Go's test cache is the whole reason
// (tick 6wh).
//
// These three tests are the acceptance criteria of that tick, one each: a
// second gate over an unchanged tree answers from cache, a reused directory
// carries nothing from the gate before it, and two gates in one checkout do
// not share a directory.

// TestASecondGateOverAnUnchangedTreeIsServedFromGosTestCache is the tick's own
// measurement, run as a test.
//
// It asserts on what `go test` REPORTS about its own cache — "(cached)" is go's
// word, not this package's — rather than on anything the reconciler logged.
// The negative control matters as much as the assertion: the same commit in a
// throwaway worktree, which is what every gate got before this tick, must NOT
// be served from cache. Without that line the test would pass just as happily
// against a Go whose cache had been disabled, and prove nothing.
func TestASecondGateOverAnUnchangedTreeIsServedFromGosTestCache(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	// A package whose source is unique to this test run, so that the first run
	// below is a cache MISS no matter how often this suite has run before: go
	// keys the result on the package's sources as well as on the paths its
	// test opened.
	commitGoModule(t, repo.Dir, t.Name())
	head := gitHead(t, repo.Dir)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	first, _, release, err := g.gateWorktree("ticfac-gate-", head)
	if err != nil {
		t.Fatal(err)
	}
	cold := goTest(t, first)
	release()
	if strings.Contains(cold, "(cached)") {
		t.Fatalf("the first gate over a package this test just wrote was served from cache; the measurement "+
			"below would mean nothing:\n%s", cold)
	}

	second, release2 := reacquire(t, g, head, first)
	defer release2()
	if second != first {
		t.Fatalf("the second gate over the same repository ran in %s and the first in %s: go's test cache keys "+
			"on the ABSOLUTE paths a test opened, so a gate that moves can never hit it", second, first)
	}
	warm := goTest(t, second)
	if !strings.Contains(warm, "(cached)") {
		t.Errorf("a second gate over an unchanged tree re-ran the suite instead of answering from go's test "+
			"cache (tick 6wh):\n%s", warm)
	}

	// The control: the throwaway worktree the gate used to get, at the same
	// commit, with the same sources.
	throwaway, removeThrowaway, err := g.tempWorktree("ticfac-gate-control-", head)
	if err != nil {
		t.Fatal(err)
	}
	defer removeThrowaway()
	if moved := goTest(t, throwaway); strings.Contains(moved, "(cached)") {
		t.Errorf("the same commit in a FRESH directory was served from cache, so this test cannot tell a stable "+
			"gate directory from a throwaway one and proves nothing:\n%s", moved)
	}
}

// TestAReusedGateDirectoryCarriesNothingFromThePreviousGate is the property the
// throwaway directory used to buy by accident.
//
// Everything a gate command can leave behind is left behind here — a modified
// tracked file, a staged change, an untracked file, an ignored build artefact,
// a directory, and a nested git repository — and the next gate must find none
// of it. The reset happens on ACQUIRE rather than on release precisely so that
// this holds for a gate that was killed and never got to clean up, which is
// why the release below is the only thing that happens between the two.
func TestAReusedGateDirectoryCarriesNothingFromThePreviousGate(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	write(t, filepath.Join(repo.Dir, "tracked.txt"), "committed\n")
	mustRun(t, repo.Dir, "git", "add", "tracked.txt")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "tracked")
	first := gitHead(t, repo.Dir)
	// A second commit, so the reuse is also asked to MOVE the tree rather than
	// only to clean it.
	write(t, filepath.Join(repo.Dir, "tracked.txt"), "committed twice\n")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-am", "tracked again")
	second := gitHead(t, repo.Dir)

	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	dir, _, release, err := g.gateWorktree("ticfac-gate-", first)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "tracked.txt"), "a gate command rewrote this\n")
	write(t, filepath.Join(dir, "untracked.txt"), "left behind\n")
	writeUnder(t, dir, ".ticfac/logs/run.jsonl", "ignored by the run-state fragment\n")
	writeUnder(t, dir, "artefacts/binary", "a build artefact\n")
	mustRun(t, dir, "git", "add", "untracked.txt")
	mustRun(t, filepath.Dir(dir), "git", "init", "--quiet", filepath.Join(dir, "vendored"))
	release()

	reused, release2 := reacquire(t, g, second, dir)
	defer release2()
	if reused != dir {
		t.Fatalf("the second gate took a different directory (%s, not %s); this test is not about the one it "+
			"was meant to be about", reused, dir)
	}
	if got := gitHead(t, reused); got != second {
		t.Errorf("the reused directory is at %s and the gate asked for %s", short(got), short(second))
	}
	if status := strings.TrimSpace(mustRun(t, reused, "git", "status", "--porcelain")); status != "" {
		t.Errorf("the reused gate directory is not a clean tree; the previous gate's work is still in it:\n%s", status)
	}
	if staged := strings.TrimSpace(mustRun(t, reused, "git", "diff", "--cached", "--name-only")); staged != "" {
		t.Errorf("the reused gate directory carries the previous gate's INDEX, which `git status` alone would "+
			"not have shown: %s", staged)
	}
	for _, left := range []string{"untracked.txt", ".ticfac/logs/run.jsonl", "artefacts/binary", "vendored/.git"} {
		if _, err := os.Stat(filepath.Join(reused, filepath.FromSlash(left))); !os.IsNotExist(err) {
			t.Errorf("%s survived into the next gate (%v): a verdict about this tree would be partly about the "+
				"previous gate's leftovers", left, err)
		}
	}
	if content := readFile(t, filepath.Join(reused, "tracked.txt")); content != "committed twice\n" {
		t.Errorf("the tracked file the previous gate rewrote reads %q, not the committed %q",
			content, "committed twice\n")
	}

	// Again, at the commit the slot is ALREADY at: a restarted run re-gates the
	// same merge, and "the tree is already where you asked" is the case where a
	// reset that only moves HEAD would do nothing at all.
	write(t, filepath.Join(reused, "tracked.txt"), "a second gate rewrote this\n")
	write(t, filepath.Join(reused, "again.txt"), "left behind again\n")
	mustRun(t, reused, "git", "add", "again.txt")
	// A merge left half-done, which is what a gate command killed mid-`git
	// merge` leaves: no file in the tree says so, and every git command in the
	// next gate would be answering about a merge in progress.
	mergeHead := filepath.Join(strings.TrimSpace(mustRun(t, reused, "git", "rev-parse", "--absolute-git-dir")),
		"MERGE_HEAD")
	write(t, mergeHead, first+"\n")
	release2()

	same, release3 := reacquire(t, g, second, dir)
	defer release3()
	if status := strings.TrimSpace(mustRun(t, same, "git", "status", "--porcelain")); status != "" {
		t.Errorf("a gate re-run at the commit the slot was already at found the previous gate's work still "+
			"in the tree:\n%s", status)
	}
	if _, err := os.Stat(mergeHead); !os.IsNotExist(err) {
		t.Errorf("the reused directory is still mid-merge (%v): the next gate's verdict would be about a tree "+
			"git considers a conflict resolution in progress", err)
	}
}

// TestTwoGatesInOneCheckoutDoNotShareTheirDirectory.
//
// Two epics can be reconciled in one checkout, and two gates writing into one
// directory would be two verdicts about a tree neither of them can describe.
// The slot is held under an flock for the life of the gate, so the second gate
// takes the next slot; the test also asks for the slot BACK after the first
// one lets go, because a lock that is never released would "pass" this
// assertion while quietly costing every later gate its cache.
func TestTwoGatesInOneCheckoutDoNotShareTheirDirectory(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	held, _, release, err := g.gateWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	beside, _, releaseBeside, err := g.gateWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBeside()
	if beside == held {
		t.Fatalf("a second gate was handed the directory the first one is still running in (%s)", held)
	}
	// Both are usable trees, not just different strings.
	for _, dir := range []string{held, beside} {
		if got := gitHead(t, dir); got != repo.Base {
			t.Errorf("%s is at %s, not the commit the gate asked for", dir, short(got))
		}
	}

	release()
	again, releaseAgain := reacquire(t, g, repo.Base, held)
	defer releaseAgain()
	if again != held {
		t.Errorf("the released slot was not handed back (%s, not %s): a slot that is never reused is a gate "+
			"that never hits go's test cache", again, held)
	}
}

// reacquire takes a gate slot, asking again if it is handed one other than
// `want`.
//
// Asked more than once because a released slot can read as held for a few
// microseconds, and these tests would otherwise be flaky about it: a process
// forked between fork and exec carries a copy of every descriptor its parent
// had, the lock's included, and a probe landing in that window sees a lock
// nobody is really holding (gatedir_unix.go says what it costs a real gate —
// one cold run, never a shared directory). Asking again costs a test nothing.
// A slot that NEVER comes back is a lock that is not being released, and the
// caller's own assertion is what says so.
func reacquire(t *testing.T, g *repoGit, commit, want string) (string, func()) {
	t.Helper()
	var dir string
	var release func()
	for attempt := 0; attempt < 10; attempt++ {
		got, _, releaseGot, err := g.gateWorktree("ticfac-gate-", commit)
		if err != nil {
			t.Fatal(err)
		}
		dir, release = got, releaseGot
		if got == want {
			break
		}
		releaseGot()
	}
	return dir, release
}

// commitGoModule writes a one-package Go module whose test is unique to `salt`
// and commits it.
func commitGoModule(t *testing.T, dir, salt string) {
	t.Helper()
	write(t, filepath.Join(dir, "go.mod"), "module ticfac.example/gatecache\n\ngo 1.21\n")
	write(t, filepath.Join(dir, "cached_test.go"), fmt.Sprintf(
		"package gatecache\n\nimport \"testing\"\n\nconst salt = %q\n\nfunc TestNothing(t *testing.T) { _ = salt }\n",
		salt))
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "a package to cache")
}

// goTest runs the toolchain's own test command in `dir` and answers with
// everything it said. The gate's declared command is `go test`; this is the
// same question asked directly, so that what the assertion reads is go's
// report and not a line this package wrote about it.
func goTest(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test in %s: %v\n%s", dir, err, out)
	}
	return string(out)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
}
