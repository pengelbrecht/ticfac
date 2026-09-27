package reconcile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The other side of a resolve-conflict job's conflict, as the job spec, the
// dispatch title and the decision record name it.
//
// epic-2jn on 2026-09-27: "4mv try 1 (run dispatch #33) does not merge onto
// epic/2jn (2 conflicts: content in README.md; content in internal/cli/exit.go)
// ... the two intents in conflict are 4mv and 0z0 and 2qz and 35l ... and
// asked and bot and closed and cuts ... and it and k4s and mn7 and must and
// named and never ... and wants". Two faults made that list: the parse read
// the WHOLE history of the conflicting files (every tick that ever touched
// README.md, most of them long merged before 4mv forked), and it took any
// word after "tick" in a commit body as a tick id ("tick closed", "tick it",
// "tick must").
func TestTheOtherSideOfAConflictIsOnlyTheTicksMergedSinceTheFork(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := newRepo(t, root, "sides", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}
	r := &Reconciler{git: g, opts: Options{Remote: "origin"}}

	commit := func(path, content, subject, body string) string {
		t.Helper()
		write(t, filepath.Join(repo.Dir, path), content)
		mustRun(t, repo.Dir, "git", "add", "-A")
		mustRun(t, repo.Dir, "git", "commit", "--quiet", "-m", subject, "-m", body)
		return strings.TrimSpace(mustRun(t, repo.Dir, "git", "rev-parse", "HEAD"))
	}
	if err := os.MkdirAll(filepath.Join(repo.Dir, ".tick", "issues"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Every tick named below is a real record in the tracker, so only the
	// RANGE can keep the old one out.
	for _, id := range []string{"old", "self", "new"} {
		write(t, filepath.Join(repo.Dir, ".tick", "issues", id+".json"), `{"id":"`+id+`"}`+"\n")
	}
	// A tick merged long before the attempt forked, touching the same file.
	commit("README.md", "# sides\nold\n",
		"Merge ticfac/run-x/tick-old/attempt-1 into epic/sides",
		"tick old attempt 1 merged; the tick closed")

	// The attempt forks here, and edits the file its own way.
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "-b", "attempt")
	attemptHead := commit("README.md", "# sides\nself\n", "self's work", "tick self")

	// The epic moves on: one tick merges onto the same file after the fork,
	// its body carrying the prose that is not a tick.
	mustRun(t, repo.Dir, "git", "checkout", "--quiet", "main")
	epicHead := commit("README.md", "# sides\nnew\n",
		"Merge ticfac/run-x/tick-new/attempt-1 into epic/sides",
		"tick new attempt 1 merged. The tick closed its gap, the tick it named, and the tick must be "+
			"followed by tick never")

	got := r.conflictingTickIDs(epicHead, attemptHead, "self", []string{"README.md"})
	if want := []string{"new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the other side of the conflict is %q, want %q: only a tick the tracker has, merged since the fork",
			got, want)
	}
}
