package reconcile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The run's merges are not resolved by the host's rerere cache (tick 6na).
//
// The bug this pins, observed live on 2026-09-19: git rerere was enabled
// GLOBALLY on the operator's host, and the reconciler merges attempt branches
// in the operator's checkout, so every merge it performs shares .git/rr-cache
// with resolutions a PERSON recorded by hand. rerere then does two things
// nobody asked it to inside a machine's merge:
//
//   - it RECORDS the run's conflicts as preimages into a person's cache; and
//   - the dangerous half: it REPLAYS a person's earlier resolution into the
//     run's merge — staged into the index, conflict markers gone — so a tree
//     nobody chose is the tree a resolve-conflict job would be handed, and
//     the evidence would say the conflict was already resolved.
//
// The fixture reproduces the incident exactly: a host-global config enabling
// rerere with autoupdate (stated by the test, so it proves the same thing on a
// host whose rerere is off), a hand merge that records a resolution the way
// every entry of the incident's cache was recorded, and then the SAME conflict
// text in a merge the run performs. The CONTROL proves the poison bites: a
// plain git in the same repository has the person's resolution replayed into
// it. The assertion is the property the whole design rests on: the run's merge
// leaves the CONFLICT — markers in the tree, nothing resolved in the index, a
// failure that names the conflict, and the person's cache untouched.
//
// serial: this test states the host's git config itself — GIT_CONFIG_GLOBAL
// via t.Setenv, so the run's merges inherit an enabled rerere the way the
// incident's did — and a process-global environment variable is not a thing
// a parallel neighbour's git invocations can be isolated from.
func TestTheRunsMergeIsNotResolvedByTheHostsRerereCache(t *testing.T) {
	// The incident's host config: rerere enabled globally, with autoupdate,
	// so a recorded resolution is staged into a matching merge without anybody
	// asking. GIT_CONFIG_GLOBAL is where the incident's enabled=true lived —
	// the run inherits the environment, config files included.
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	write(t, globalConfig, "[rerere]\n\tenabled = true\n\tautoupdate = true\n")
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	repo := newRepo(t, t.TempDir(), "repo", passingGate)

	// The conflicts the run will meet: two files, each changed differently on
	// two sides — the shape every real conflict has. work.txt is the conflict
	// a PERSON has already resolved by hand; fresh.txt is one nobody has.
	write(t, filepath.Join(repo.Dir, "work.txt"), "base\n")
	write(t, filepath.Join(repo.Dir, "fresh.txt"), "base\n")
	mustRun(t, repo.Dir, "git", "add", "work.txt", "fresh.txt")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "the files both sides touch")
	base := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))

	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "-b", "side")
	write(t, filepath.Join(repo.Dir, "work.txt"), "side\n")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-am", "side")
	side := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "side"))

	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "-b", "other", base)
	write(t, filepath.Join(repo.Dir, "fresh.txt"), "other\n")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-am", "other")
	other := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "other"))

	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "main")
	write(t, filepath.Join(repo.Dir, "work.txt"), "main\n")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-am", "main")
	write(t, filepath.Join(repo.Dir, "fresh.txt"), "main\n")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-am", "fresh")
	epicHead := strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "main"))

	// A PERSON resolves work.txt's conflict by hand in the checkout, with the
	// host's rerere enabled: the merge conflicts, the person resolves and
	// commits, and rerere records both the preimage and the resolution. That
	// is how every entry of the incident's cache came to exist.
	if err := runGit(repo.Dir, "merge", "--no-ff", "--no-edit", "-m", "a person's resolve", side); err == nil {
		t.Fatal("the hand merge did not conflict; this fixture proves nothing")
	}
	write(t, filepath.Join(repo.Dir, "work.txt"), "a person's resolution\n")
	mustRun(t, repo.Dir, "git", "add", "work.txt")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "--no-edit")
	// Back to where the run will meet the same conflict.
	mustRun(t, repo.Dir, "git", "reset", "--hard", "--quiet", epicHead)

	// The cache is poisoned: one recorded resolution, preimage and postimage
	// both, matching the conflict the run is about to merge.
	cache := filepath.Join(repo.Dir, ".git", "rr-cache")
	if ids := rrCacheIDs(t, cache); len(ids) != 1 {
		t.Fatalf("the hand resolution left %d rr-cache entries, want 1; this fixture proves nothing", len(ids))
	}
	for _, name := range []string{"preimage", "postimage"} {
		if _, err := os.Stat(filepath.Join(cache, rrCacheIDs(t, cache)[0], name)); err != nil {
			t.Fatalf("the hand resolution recorded no %s; this fixture proves nothing: %v", name, err)
		}
	}

	// The CONTROL: a plain git in this repository — an operator's own command,
	// or the reconciler before this tick — has the person's resolution
	// REPLAYED into its merge: staged into the index, no conflict markers.
	// Without this, a cache that does not bite would pass everything below
	// for free.
	control := filepath.Join(t.TempDir(), "control")
	mustRun(t, repo.Dir, "git", "worktree", "add", "--quiet", "--detach", control, epicHead)
	if err := runGit(control, "merge", "--no-ff", "--no-edit", "-m", "control", side); err == nil {
		t.Fatalf("a plain git's merge of a conflicting branch reported clean in a repository whose history " +
			"conflicts; this fixture proves nothing")
	}
	replayed, err := os.ReadFile(filepath.Join(control, "work.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(replayed)) != "a person's resolution" {
		t.Fatalf("the poisoned cache replayed %q into a plain git's merge, want %q; this fixture proves nothing",
			strings.TrimSpace(string(replayed)), "a person's resolution")
	}
	mustRun(t, repo.Dir, "git", "worktree", "remove", "--force", control)

	// The run's merge of the SAME conflicting branch: exactly the merge
	// integrate.go's mergeInWorktree performs, in a throwaway worktree of its
	// own at the epic branch's head.
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	// First the resolution nobody chose. rerere must not reach this merge:
	// the conflict must still BE the conflict when the run refuses it.
	dir, remove, err := g.tempWorktree("ticfac-rerere-", epicHead)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	stdout, _, err := g.try(dir, "merge", "--no-ff", "--no-edit", "-m", "ticfac: merge side", side)
	if err == nil {
		t.Fatalf("the run's merge of a conflicting branch reported clean; a tree nobody chose would be "+
			"gated and integrated as though the merge were evidence:\n%s", stdout)
	}
	merged, err := os.ReadFile(filepath.Join(dir, "work.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "<<<<<<<") {
		t.Fatalf("the run's worktree holds %q — a resolution replayed from the host's rerere cache, not the "+
			"conflict. The run would hand a resolve-conflict job a tree that is already resolved and is "+
			"nobody's choice: not the worker's, not the base's, not a reviewer's", strings.TrimSpace(string(merged)))
	}
	if status := mustRun(t, dir, "git", "status", "--short"); !strings.Contains(status, "UU work.txt") {
		t.Fatalf("the run's merge left work.txt resolved in the index (%q); the evidence would say the "+
			"conflict was already settled when nobody settled it", status)
	}
	if strings.Contains(stdout, "Recorded preimage") || strings.Contains(stdout, "previous resolution") {
		t.Fatalf("rerere spoke inside the run's merge; the incident handed the operator 'Recorded preimage "+
			"for ...' as the entire reason a merge failed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "CONFLICT") {
		t.Fatalf("the run's merge failure does not name the conflict it refused:\n%s", stdout)
	}

	// Then the other half: a conflict nobody has resolved must not be
	// RECORDED into a person's cache either.
	dir2, remove2, err := g.tempWorktree("ticfac-rerere-", epicHead)
	if err != nil {
		t.Fatal(err)
	}
	defer remove2()
	stdout2, _, err := g.try(dir2, "merge", "--no-ff", "--no-edit", "-m", "ticfac: merge other", other)
	if err == nil {
		t.Fatalf("the run's merge of a second conflicting branch reported clean:\n%s", stdout2)
	}
	if strings.Contains(stdout2, "Recorded preimage") {
		t.Fatalf("the run's merge recorded a preimage into a person's resolution cache:\n%s", stdout2)
	}
	if ids := rrCacheIDs(t, cache); len(ids) != 1 {
		t.Fatalf("the run's merges wrote into the host's rerere cache (%d entries recorded by hand, %d "+
			"after the run's merges); machine conflicts belong to no person's cache", 1, len(ids))
	}
}

// runGit is mustRun for a command whose FAILURE is part of the fixture: the
// hand merge conflicts, and that exit status is the setup working, not the
// test failing.
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

// rrCacheIDs is the rerere cache's conflict ids, and nil when the repository
// has no cache at all — a repository whose rerere never ran.
func rrCacheIDs(t *testing.T, cache string) []string {
	t.Helper()
	entries, err := os.ReadDir(cache)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	return ids
}
