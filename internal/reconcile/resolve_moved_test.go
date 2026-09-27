package reconcile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The resolution minted over an epic branch that MOVED while the resolve job
// ran (or while the run was down between the job's dispatch and its finish).
//
// A resolve-conflict job is cut at the conflicted merge of the attempt into
// the epic head E0. Its result is a TREE: the union of E0 and the attempt.
// When the reconciler finishes it, the epic branch may be at E1 — another
// tick's merge landed, the run-state store wrote its records, an earlier
// incarnation's job is adopted or finished from its branch after other ticks
// integrated. Minting the merge as "the job's tree, with E1 as first parent"
// states that E1 was merged, while the tree carries none of what E0..E1 added:
// every such change is silently reverted by the merge commit, and the gate
// runs over a tree that lost it.
//
// The minted merge must carry E1's work: the resolution is merged onto the
// head the branch has NOW with a real three-way merge, and a resolution that
// no longer merges there is a second conflict — the stop, never a tree copy.

// movedResolve is the world these tests build: an epic head E0 and an
// attempt head that conflict on shared.txt, the conflicted merge the job was
// cut at, the job's resolution pushed to its branch, and the epic branch
// moved on to E1 by a change the resolve never saw.
type movedResolve struct {
	r        *Reconciler
	f        *fixture
	e0, e1   string
	head     string
	resolved string
	marker   attemptHandle
	conflict *mergeConflict
}

func buildMovedResolve(t *testing.T, laterPath, laterContent string) *movedResolve {
	t.Helper()
	f := newFixture(t, fixtureOptions{mode: "report"})
	commitOnBase(t, f.Repo, "shared.txt", "the base line\n", "the shared file at base")
	forkIntegrationBranch(t, f.Repo, "epic/qeu")

	attemptBranch := "ticfac/run-r-fixture/tick-a2/attempt-1"
	side := filepath.Join(f.Root, "attempt-side")
	cloneRepo(t, f.Repo.Origin, side)
	writeUnder(t, side, "shared.txt", "the attempt's line\n")
	writeUnder(t, side, "work-a2.txt", "a2's own work\n")
	mustRun(t, side, "git", "add", "-A")
	mustRun(t, side, "git", "commit", "--quiet", "-m", "a2's attempt")
	mustRun(t, side, "git", "push", "--quiet", "origin", "HEAD:"+refFor(attemptBranch))
	head := strings.TrimSpace(mustRun(t, side, "git", "rev-parse", "HEAD"))

	commitOnIntegrationBranch(t, f, "epic/qeu", "shared.txt", "the epic's line\n", "a1 merged")
	e0 := strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor("epic/qeu")))

	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"epic/qeu", attemptBranch} {
		if err := r.git.fetch(branch); err != nil {
			t.Fatal(err)
		}
	}
	writeRef := resolveWriteRef(r.runID, "a2", 1)
	marker := attemptHandle{TickID: "a2", Attempt: 1, JobID: "run-r-fixture/tick-a2/resolve-1",
		Role: RoleResolveConflict, WriteRef: writeRef}
	conflicted, err := r.conflictedTree(e0, head, marker)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", conflicted+":"+writeRef)

	// The job: resolve shared.txt on top of the conflicted merge and push.
	job := filepath.Join(f.Root, "resolve-job")
	cloneRepo(t, f.Repo.Origin, job)
	mustRun(t, job, "git", "fetch", "--quiet", "origin", writeRef)
	mustRun(t, job, "git", "checkout", "--quiet", "-B", "resolve", "FETCH_HEAD")
	writeUnder(t, job, "shared.txt", "the epic's line\nthe attempt's line\n")
	mustRun(t, job, "git", "add", "-A")
	mustRun(t, job, "git", "commit", "--quiet", "-m", "resolve shared.txt")
	mustRun(t, job, "git", "push", "--quiet", "origin", "HEAD:"+writeRef)
	resolved := strings.TrimSpace(mustRun(t, job, "git", "rev-parse", "HEAD"))

	// The epic branch moves while the job ran: work the resolve never saw.
	commitOnIntegrationBranch(t, f, "epic/qeu", laterPath, laterContent, "b1 merged after the resolve was cut")
	if err := r.git.fetch("epic/qeu"); err != nil {
		t.Fatal(err)
	}
	e1 := strings.TrimSpace(mustRun(t, f.Repo.Origin, "git", "rev-parse", refFor("epic/qeu")))

	return &movedResolve{r: r, f: f, e0: e0, e1: e1, head: head, resolved: resolved, marker: marker,
		conflict: &mergeConflict{Files: []string{"shared.txt"}, Kind: map[string]string{"shared.txt": "content"},
			Detail: "CONFLICT (content): Merge conflict in shared.txt"}}
}

// TestAResolutionMintedOverAMovedEpicKeepsWhatLandedSince is the loss, proved
// or refuted: E1's change must survive in the minted merge.
func TestAResolutionMintedOverAMovedEpicKeepsWhatLandedSince(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	w := buildMovedResolve(t, "landed-later.txt", "b1's work\n")

	merged, err := w.r.mintResolveMerge(w.resolved, w.head, w.e1, w.marker, w.conflict)
	if err != nil {
		t.Fatalf("the mint over the moved epic failed: %v", err)
	}
	dir := w.f.Repo.Dir
	if got, ok := showAt(dir, merged, "landed-later.txt"); !ok || got != "b1's work\n" {
		t.Errorf("the minted merge %s lost landed-later.txt, which E1 (%s) added after the resolve was cut "+
			"at E0 (%s): got %q (present=%v)", short(merged), short(w.e1), short(w.e0), got, ok)
	}
	if got, _ := showAt(dir, merged, "shared.txt"); got != "the epic's line\nthe attempt's line\n" {
		t.Errorf("the minted merge does not carry the job's resolution: %q", got)
	}
	if got, _ := showAt(dir, merged, "work-a2.txt"); got != "a2's own work\n" {
		t.Errorf("the minted merge does not carry the attempt's own work: %q", got)
	}
	for _, ancestor := range []string{w.e1, w.head} {
		if !mustRunAllowingFailure(dir, "git", "merge-base", "--is-ancestor", ancestor, merged) {
			t.Errorf("the minted merge %s does not descend from %s", short(merged), short(ancestor))
		}
	}
}

// TestAResolutionThatNoLongerMergesOntoAMovedEpicIsRefused: when what landed
// since conflicts with the resolution itself, the tree copy would silently
// drop one side; the mint refuses — a second conflict on the tick is the stop.
func TestAResolutionThatNoLongerMergesOntoAMovedEpicIsRefused(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	w := buildMovedResolve(t, "shared.txt", "b1 rewrote the shared file\n")

	merged, err := w.r.mintResolveMerge(w.resolved, w.head, w.e1, w.marker, w.conflict)
	if err == nil {
		got, _ := showAt(w.f.Repo.Dir, merged, "shared.txt")
		t.Fatalf("the mint over a moved epic that conflicts with the resolution succeeded as %s with "+
			"shared.txt %q: one side was dropped", short(merged), got)
	}
	refusal, ok := AsRefusal(err)
	if !ok || refusal.Reason != RefusedMerge {
		t.Fatalf("want a merge refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "shared.txt") {
		t.Errorf("the refusal does not name the file: %v", err)
	}
}

// TestABaseFoldResolutionIsMintedAgainstTheEpicHeadItResolved is the base
// fold's half: its mint must name the epic head the job resolved against as
// the first parent, so the refresh loop folds a resolution made over an older
// head onto the head the branch has now instead of pushing the job's tree as
// if it had been made there.
func TestABaseFoldResolutionIsMintedAgainstTheEpicHeadItResolved(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "report"})
	e0, mainHead := baseFoldConflict(t, f)
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"epic/qeu", "main"} {
		if err := r.git.fetch(branch); err != nil {
			t.Fatal(err)
		}
	}
	writeRef := baseFoldWriteRef(r.runID, 1, mainHead)
	marker := attemptHandle{TickID: "qeu", Attempt: 1, JobID: "run-r-fixture/base-fold-1",
		Role: RoleResolveConflict, WriteRef: writeRef}
	conflicted, err := r.conflictedMerge(e0, mainHead, "the conflicted fold", nil)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, f.Repo.Dir, "git", "push", "--quiet", "origin", conflicted+":"+writeRef)
	job := filepath.Join(f.Root, "fold-job")
	cloneRepo(t, f.Repo.Origin, job)
	mustRun(t, job, "git", "fetch", "--quiet", "origin", writeRef)
	mustRun(t, job, "git", "checkout", "--quiet", "-B", "resolve", "FETCH_HEAD")
	writeUnder(t, job, "deps.txt", "require example.com/a v1.2.0\nrequire example.com/epic v0.3.0\n")
	mustRun(t, job, "git", "add", "-A")
	mustRun(t, job, "git", "commit", "--quiet", "-m", "resolve deps.txt")
	mustRun(t, job, "git", "push", "--quiet", "origin", "HEAD:"+writeRef)
	resolved := strings.TrimSpace(mustRun(t, job, "git", "rev-parse", "HEAD"))

	commitOnIntegrationBranch(t, f, "epic/qeu", "landed-later.txt", "b1's work\n", "b1 merged after the fold was cut")
	if err := r.git.fetch("epic/qeu"); err != nil {
		t.Fatal(err)
	}
	conflict := &mergeConflict{Files: []string{"deps.txt"}, Kind: map[string]string{"deps.txt": "content"}}
	failed := func(format string, args ...any) error {
		return r.refuse(RefusedBaseRefresh, "", format, args...)
	}
	merged, err := r.mintBaseFold(resolved, mainHead, marker, conflict, failed)
	if err != nil {
		t.Fatal(err)
	}
	parents := strings.Fields(runGitQuiet(f.Repo.Dir, "rev-list", "--parents", "-n", "1", merged))
	if len(parents) != 3 || parents[1] != e0 || parents[2] != mainHead {
		t.Errorf("the fold's mint names parents %v, want the epic head it resolved against %s and main %s: a "+
			"first parent the tree was not resolved over reverts what landed in between",
			parents[1:], short(e0), short(mainHead))
	}
}

// showAt reads one path at one commit, and whether it is there at all.
func showAt(dir, commit, path string) (string, bool) {
	out, err := harnessCommand("git", "show", commit+":"+path).output(dir)
	if err != nil {
		return "", false
	}
	return string(out), true
}
