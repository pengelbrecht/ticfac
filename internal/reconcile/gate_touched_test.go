package reconcile

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gatescope"
)

// The gate's touched-diff export: what a declared command sees of the tick it
// is gating (gate_touched.go, the contract), and where the pair comes from
// for each shape of merge the gate can be over.
//
// The end-to-end answer — a tick that breaks a full suite in a package it
// touched failing its gate — is gate_touched_run_test.go.

// gateOnceIn runs one command in dir the way the integrated gate does, with
// the extra environment a gate would export, and answers its stdout.
func gateOnceIn(t *testing.T, dir, command string, extraEnv []string) string {
	t.Helper()
	shell, err := startShell(dir, command, time.Minute, time.Now(), nil, extraEnv)
	if err != nil {
		t.Fatal(err)
	}
	for !shell.settled() {
		time.Sleep(10 * time.Millisecond)
	}
	stdout, stderr, code, err := shell.wait()
	if code != 0 || err != nil {
		t.Fatalf("%s: code %d, err %v\n%s%s", command, code, err, stdout, stderr)
	}
	return stdout
}

// TestTheGateExportsTheTouchedPairToItsCommands is the contract half a
// declared command can see: both names, in the spelling gatescope owns, with
// the values the merge stated. The empty pair — "this gate has no touched
// diff" — is exported as empty strings rather than not at all, which is what
// lets a command tell the gate's nothing-to-diff from a person's bare run.
//
// short: one short-lived shell in a tempdir; no repository and no run
func TestTheGateExportsTheTouchedPairToItsCommands(t *testing.T) {
	t.Parallel()
	out := gateOnceIn(t, t.TempDir(), `printf '%s' "$TICFAC_GATE_TOUCHED_BASE|$TICFAC_GATE_TOUCHED_HEAD"`, []string{
		gatescope.EnvBase + "=base-sha", gatescope.EnvHead + "=head-sha",
	})
	if out != "base-sha|head-sha" {
		t.Errorf("a gate command saw %q, want the exported pair base-sha|head-sha", out)
	}
	empty := gateOnceIn(t, t.TempDir(), `printf '%s' "$TICFAC_GATE_TOUCHED_BASE|$TICFAC_GATE_TOUCHED_HEAD"`, []string{
		gatescope.EnvBase + "=", gatescope.EnvHead + "=",
	})
	if empty != "|" {
		t.Errorf("an empty exported pair reached the command as %q, want both names present and empty", empty)
	}
}

// TestTouchedPairReadsTheSidesOfTheMergeItGates: a merge names both its own
// sides — the branch head it merged onto and the attempt it carried in — and
// that pair is the tick's own diff, the same three-dot shape the status model
// reads a merged attempt's diff with.
func TestTouchedPairReadsTheSidesOfTheMergeItGates(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	needle := AttemptMergeNeedle("r-unit", "a1", 1)
	base, attemptHead, merge := mergeNaming(t, repo, "work-1", needle)

	gotBase, gotHead := g.touchedPair(merge, "a needle this history does not carry")
	if gotBase != base || gotHead != attemptHead {
		t.Errorf("the pair for the merge is %s...%s, want the merge's own sides %s...%s",
			short(gotBase), short(gotHead), short(base), short(attemptHead))
	}

	// A head that already CARRIES the tick — the already-contained and the
	// adopted attempt — is not a merge, and the pair is read from the tick's
	// own merge below it, found by the needle every attempt merge names.
	plainCommit(t, repo)
	carrier := gitHead(t, repo.Dir)
	gotBase, gotHead = g.touchedPair(carrier, needle)
	if gotBase != base || gotHead != attemptHead {
		t.Errorf("the pair for a head carrying the tick is %s...%s, want the tick's own merge's sides %s...%s",
			short(gotBase), short(gotHead), short(base), short(attemptHead))
	}

	// A needle no merge of this run names answers nothing: a foreign run's
	// merge is not this run's to claim (gate_touched.go).
	gotBase, gotHead = g.touchedPair(carrier, AttemptMergeNeedle("another-run", "a1", 1))
	if gotBase != "" || gotHead != "" {
		t.Errorf("a head carrying no merge of this run answered %s...%s, want no pair", short(gotBase), short(gotHead))
	}
}

// TestTouchedPairMatchesTheNeedleByLineAndNotBySubstring: attempt 1's needle
// is the opening of attempt 10's line, and a substring match would read
// attempt 10's merge as attempt 1's — answering a gate with another
// dispatch's diff.
func TestTouchedPairMatchesTheNeedleByLineAndNotBySubstring(t *testing.T) {
	t.Parallel()
	repo := newRepo(t, t.TempDir(), "repo", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	first := AttemptMergeNeedle("r-unit", "a1", 1)
	tenth := AttemptMergeNeedle("r-unit", "a1", 10)
	if !strings.HasPrefix(tenth, first) {
		t.Fatalf("the fixture is not the shape it exists for: %q is not the opening of %q", first, tenth)
	}

	firstBase, firstHead, _ := mergeNaming(t, repo, "work-1", first)
	tenthBase, tenthHead, _ := mergeNaming(t, repo, "work-10", tenth)
	plainCommit(t, repo)

	gotBase, gotHead := g.touchedPair(gitHead(t, repo.Dir), first)
	if gotBase != firstBase || gotHead != firstHead {
		t.Errorf("attempt 1's pair is %s...%s, want its own merge's sides %s...%s — not attempt 10's %s...%s",
			short(gotBase), short(gotHead), short(firstBase), short(firstHead), short(tenthBase), short(tenthHead))
	}
	gotBase, gotHead = g.touchedPair(gitHead(t, repo.Dir), tenth)
	if gotBase != tenthBase || gotHead != tenthHead {
		t.Errorf("attempt 10's pair is %s...%s, want its own merge's sides %s...%s",
			short(gotBase), short(gotHead), short(tenthBase), short(tenthHead))
	}
}

// mergeNaming merges one attempt-shaped branch into main naming the needle,
// and answers the three commits the pair is made of.
func mergeNaming(t *testing.T, repo *testRepo, branch, needle string) (base, attemptHead, merge string) {
	t.Helper()
	mustRun(t, repo.Dir, "git", "checkout", "-q", "-b", branch)
	write(t, filepath.Join(repo.Dir, branch+".txt"), "the attempt's work\n")
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "the attempt's own commit")
	attemptHead = gitHead(t, repo.Dir)
	mustRun(t, repo.Dir, "git", "checkout", "-q", "main")
	base = gitHead(t, repo.Dir)
	mustRun(t, repo.Dir, "git", "merge", "--no-ff", "--quiet", "-m", needle, branch)
	return base, attemptHead, gitHead(t, repo.Dir)
}

// plainCommit moves main by one ordinary commit, the shape a head carrying an
// already-merged tick is in: not itself a merge.
func plainCommit(t *testing.T, repo *testRepo) {
	t.Helper()
	write(t, filepath.Join(repo.Dir, "checkpoint.txt"), "a plain commit, not a merge\n")
	mustRun(t, repo.Dir, "git", "add", "-A")
	mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", "a plain commit")
}
