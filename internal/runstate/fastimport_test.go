package runstate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/gittest"
)

// The write-path half of the streaming plumbing (tick pul did the read path:
// the held-open cat-file --batch). One `git fast-import` process writes every
// record a step holds, instead of a throwaway index per record — hash-object,
// read-tree, update-index, write-tree and commit-tree were five processes per
// record, and process creation is the majority of a reconcile end-to-end
// test's wall clock (pul measured 24x per query between a spawned git and a
// held-open one; the same arithmetic holds for writes).
//
// What must NOT change is what lands: every record is still its own commit,
// with its own message and its own guard, and the commits are byte-for-byte
// what the per-record path builds — the same tree (the parent's tree plus the
// record's one path change), the same message (commit-tree appends the
// trailing newline; so does the stream), the same author and committer. The
// byte-for-byte claim is not an aspiration: the first test below builds both
// and asserts the same sha, which is git's own answer to "are these the same
// commit".

// countingFastImport wraps the store's real fast-import so a test can assert
// over the materializations it saw: how many processes, carrying how many
// commits. The store's real fast-import is a field on git, so the seam a test
// replaces is the same call the store makes in production.
func countingFastImport(g *git, calls *[]fastImportCall) {
	real := g.fastImport
	g.fastImport = func(ref string, commits []importCommit) ([]string, error) {
		*calls = append(*calls, fastImportCall{ref: ref, commits: len(commits)})
		return real(ref, commits)
	}
}

type fastImportCall struct {
	ref     string
	commits int
}

// TestAFastImportCommitIsByteForByteWhatTheIndexDanceBuilds is the fidelity
// guard: one record written through the fast-import materializer is the SAME
// commit object the per-record path (hash-object, read-tree, update-index,
// write-tree, commit-tree) would have built — same sha, because git says so.
//
// The reference is built against origin, with the store's own identity and
// the landed commit's own date, spelled the way the materializer spells them.
// If the two ever disagree, nothing in this repository can read the other's
// records as the same history, so this test is the one that says the
// materializer did not quietly change what a record IS.
func TestAFastImportCommitIsByteForByteWhatTheIndexDanceBuilds(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("writer", testRun)

	path := EvidencePath(testRun, "probe")
	content := "{\"probe\": true}\n"
	if got, err := s.CreateIfAbsent(path, []byte(content)); err != nil || got != Created {
		t.Fatalf("the create: %v %v", got, err)
	}
	landed := strings.TrimSpace(gitRun(t, o.bare, "rev-parse", o.branch))
	base := strings.TrimSpace(gitRun(t, o.bare, "rev-parse", o.branch+"~1"))
	// The date the materializer captured at write time, read back off the
	// landed commit: %cd --date=raw is "<unix> <offset>".
	when := strings.TrimSpace(gitRun(t, o.bare, "log", "-1", "--format=%cd", "--date=raw", landed))

	// The per-record path, over the same base, at the same instant, with the
	// same identity the store carries.
	blob := gitRunStdin(t, o.bare, "", content, "hash-object", "-w", "--stdin")
	indexDir := t.TempDir()
	env := "GIT_INDEX_FILE=" + filepath.Join(indexDir, "index")
	gitRunEnv(t, o.bare, env, "read-tree", base)
	gitRunEnv(t, o.bare, env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	tree := gitRunEnv(t, o.bare, env, "write-tree")
	ident := "GIT_AUTHOR_NAME=ticfac\x00GIT_AUTHOR_EMAIL=ticfac@example.com\x00" +
		"GIT_COMMITTER_NAME=ticfac\x00GIT_COMMITTER_EMAIL=ticfac@example.com\x00"
	want := gitRunEnv(t, o.bare, ident+"GIT_AUTHOR_DATE="+when+"\x00GIT_COMMITTER_DATE="+when,
		"commit-tree", tree, "-p", base, "-m", "ticfac run "+testRun+": create "+path)

	if landed != want {
		t.Fatalf("the store landed the record as %s, but the per-record path builds %s: the materializer changed the commit object (date %s)",
			landed, want, when)
	}
}

// TestAHeldStepMaterializesOncePerRelease is the cost guard: a step of three
// records — a checkpoint, a tracker change set, another checkpoint — reaches
// git as ONE fast-import and ONE push, not as three five-process commits.
func TestAHeldStepMaterializesOncePerRelease(t *testing.T) {
	o := newOrigin(t)
	pushes := o.countPushes(t)
	s := o.actor("reconciler", testRun)
	var calls []fastImportCall
	countingFastImport(s.git, &calls)

	if _, err := s.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].commits != 1 {
		t.Fatalf("the create materialized as %v, want one fast-import of 1 commit", calls)
	}
	before, commitsBefore := pushes(), o.commits()
	calls = nil

	s.Hold()
	if _, err := s.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, s, ".tick/issues/a1.json", "{\"id\":\"a1\",\"status\":\"closed\"}\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCheckpoint(testCheckpoint(StateRunning, "a1 is closed")); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("the held step materialized %d times before its release, want 0: a held record builds no git object until it lands",
			len(calls))
	}
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].commits != 3 {
		t.Fatalf("the step of three records materialized as %v, want one fast-import of 3 commits", calls)
	}
	if got := pushes() - before; got != 1 {
		t.Errorf("the step took %d pushes, want 1", got)
	}
	if got := o.commits() - commitsBefore; got != 3 {
		t.Errorf("the step landed %d commits, want 3: every record is still its own commit", got)
	}
	want := []string{
		"ticfac run " + testRun + ": update " + CheckpointPath(testRun),
		"ticfac run " + testRun + ": close a1",
		"ticfac run " + testRun + ": update " + CheckpointPath(testRun),
	}
	subjects := o.subjects()
	tail := subjects[len(subjects)-3:]
	for i := range want {
		if tail[i] != want[i] {
			t.Errorf("commit %d of the step is %q, want %q", i+1, tail[i], want[i])
		}
	}
}

// TestAFastImportFailureFallsBackToThePerRecordPath keeps the materializer
// honest about failure the way the batch reader is (batch.go): a store whose
// fast-import dies keeps working exactly as it did before, one process per
// record, and the only cost is speed. Nothing about what lands may change.
func TestAFastImportFailureFallsBackToThePerRecordPath(t *testing.T) {
	o := newOrigin(t)
	pushes := o.countPushes(t)
	s := o.actor("reconciler", testRun)
	s.git.fastImport = func(ref string, commits []importCommit) ([]string, error) {
		return nil, errFastImportUnavailable
	}

	if got, err := s.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil || got != Created {
		t.Fatalf("the create: %v %v", got, err)
	}
	s.Hold()
	if got, err := s.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil || got != Updated {
		t.Fatalf("a held checkpoint: %v %v", got, err)
	}
	if _, err := s.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, s, ".tick/issues/a1.json", "{\"id\":\"a1\",\"status\":\"closed\"}\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if got := pushes(); got != 2 {
		t.Errorf("the store made %d pushes, want 2 (the create and the step)", got)
	}
	subjects := o.subjects()
	tail := subjects[len(subjects)-2:]
	want := []string{
		"ticfac run " + testRun + ": update " + CheckpointPath(testRun),
		"ticfac run " + testRun + ": close a1",
	}
	for i := range want {
		if tail[i] != want[i] {
			t.Errorf("commit %d of the fallback is %q, want %q", i+1, tail[i], want[i])
		}
	}
	// And the fallback's records are readable by everything that reads
	// records — a history read over the branch, the way a resumed
	// incarnation does.
	history, err := o.actor("reader", testRun).CheckpointHistory()
	if err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, c := range history {
		states = append(states, string(c.State))
	}
	if strings.Join(states, ",") != "admitted,publishing" {
		t.Errorf("the fallback's checkpoint history is %v, want admitted,publishing", states)
	}
}

// TestAStepKilledBeforeAnythingMaterializedBuildsNoCommits is the crash shape
// the deferred materializer makes strictly safer: a step that dies before its
// release used to leave built COMMITS in the local object database (unpushed,
// unreachable); deferred, it has built none. The blobs the eager hash-object
// wrote are the same strays the old path left — and like then, they are
// unreachable garbage git prunes, not records.
func TestAStepKilledBeforeAnythingMaterializedBuildsNoCommits(t *testing.T) {
	o := newOrigin(t)
	s := o.actor("reconciler", testRun)
	if _, err := s.PutCheckpoint(testCheckpoint(StateAdmitted, "admitted")); err != nil {
		t.Fatal(err)
	}
	objectsBefore := countCommits(t, s)

	s.Hold()
	if _, err := s.PutCheckpoint(testCheckpoint(StatePublishing, "closing a1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageChanges("ticfac run "+testRun+": close a1",
		trackerChange(t, s, ".tick/issues/a1.json", "{\"id\":\"a1\",\"status\":\"closed\"}\n"), true, nil); err != nil {
		t.Fatal(err)
	}
	s.Abandon()
	if got := countCommits(t, s) - objectsBefore; got != 0 {
		t.Fatalf("an abandoned step left %d new commits in the local object database, want 0: a step that dies before its release has built none",
			got)
	}
}

// countCommits is every commit object in the store's repository, packed and
// loose: what the abandoned-step guard measures, because a step's commits are
// what a crash used to leave behind and nothing else is a record.
func countCommits(t *testing.T, s *Store) int {
	t.Helper()
	out, err := s.git.run("cat-file", "--batch-all-objects", "--batch-check=%(objecttype)")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "commit" {
			n++
		}
	}
	return n
}

// gitRunEnv runs git with KEY=VALUE pairs (split on NUL) appended to this
// repository's hermetic git environment, answering stdout trimmed — the
// reference commit in the fidelity test has to spell the per-record path
// exactly, identity and dates and all.
func gitRunEnv(t *testing.T, dir, env string, args ...string) string {
	t.Helper()
	cmd := gittest.Under(append(gittest.Env(), strings.Split(env, "\x00")...), dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitRunStdin is gitRunEnv with bytes on stdin, for hash-object --stdin.
func gitRunStdin(t *testing.T, dir, env string, stdin string, args ...string) string {
	t.Helper()
	cmd := gittest.Under(append(gittest.Env(), strings.Split(env, "\x00")...), dir, args...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
