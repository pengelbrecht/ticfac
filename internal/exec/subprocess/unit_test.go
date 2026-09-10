package subprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The pure decisions, tested without a process: identity, the branch the
// issuer allows, the report path, and the durability timer.

// Identity is (repo key, job id, attempt), and all three matter. The one that
// is easy to forget is the repository, which is why two checkouts running one
// tick is a test of its own — this is the same claim, one layer down.
func TestAttemptIdentityIsRepositoryJobAndAttempt(t *testing.T) {
	base := attemptKey("repo-a", "run-1/tick-x/attempt-1", 1)
	for _, other := range []struct{ key, why string }{
		{attemptKey("repo-b", "run-1/tick-x/attempt-1", 1), "a different repository"},
		{attemptKey("repo-a", "run-1/tick-y/attempt-1", 1), "a different job"},
		{attemptKey("repo-a", "run-1/tick-x/attempt-1", 2), "a different attempt"},
	} {
		if other.key == base {
			t.Errorf("%s produces the same attempt key", other.why)
		}
	}
	if attemptKey("repo-a", "run-1/tick-x/attempt-1", 1) != base {
		t.Error("the same identity produced two keys; a handle would stop addressing its own attempt")
	}
}

// The branch comes from the ONE field that says which ref the job may write,
// and a write_ref outside the namespace the grant bounds is refused by the
// issuer rather than trusted from the runner.
func TestTheBranchIsTheWriteRefAndTheGrantBoundsIt(t *testing.T) {
	spec := func(ref, prefix string) *JobSpec {
		s := &JobSpec{Source: Source{WriteRef: ref}}
		if prefix != "" {
			s.Credentials.Source = SourceCredential{Grant: &SourceGrant{Issuer: "host", Grade: "write", WriteRefPrefix: prefix}}
		}
		return s
	}

	branch, err := branchFromWriteRef(spec("refs/heads/tick/abc", ""))
	if err != nil || branch != "tick/abc" {
		t.Errorf("branch %q, err %v", branch, err)
	}
	if _, err := branchFromWriteRef(spec("refs/heads/ticfac/run-42/x", "refs/heads/ticfac/run-42/")); err != nil {
		t.Errorf("a write_ref inside the granted namespace was refused: %v", err)
	}
	err = nil
	if _, err = branchFromWriteRef(spec("refs/heads/main", "refs/heads/ticfac/run-42/")); err == nil {
		t.Error("a write_ref outside the granted namespace was allowed")
	} else if refusal, ok := AsRefusal(err); !ok || refusal.Reason != RefusedCredential {
		t.Errorf("refused with %v, want a credential refusal", err)
	}
	for _, bad := range []string{"", "refs/tags/v1", "refs/heads/", "refs/heads/-dashed", "refs/heads/a..b", "refs/heads/we ird"} {
		if _, err := branchFromWriteRef(spec(bad, "")); err == nil {
			t.Errorf("write_ref %q was accepted as a branch", bad)
		}
	}
}

// The report path is the executor's: absolute, inside the attempt worktree,
// under the spec's artifact prefix — and a prefix that escapes the worktree is
// refused rather than normalised, because the escape is the interesting part
// of such a spec.
func TestTheReportPathIsOwnedByTheExecutorAndCannotEscape(t *testing.T) {
	rel, abs, err := resultPath("/work/attempt", "runs/run-42/jobs/tick-abc/attempt-1/", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if rel != "runs/run-42/jobs/tick-abc/attempt-1/RESULT-abc.md" {
		t.Errorf("relative path %q", rel)
	}
	if abs != "/work/attempt/"+rel {
		t.Errorf("absolute path %q", abs)
	}
	for _, escape := range []string{"../", "runs/../../", "/etc/"} {
		if _, _, err := resultPath("/work/attempt", escape, "abc"); err == nil {
			t.Errorf("artifact_prefix %q put the report outside the worktree and was accepted", escape)
		}
	}
}

// Durability is a timer. The whole of Appendix A #5 is that the answer to "is
// a push due?" comes from a clock and not from the job's intentions — and that
// a revoked credential stops it, because the process that revokes and the
// process that would spend are not the same process.
func TestThePushTimerIsAClockAndACredential(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	pushes := 0
	p := &pusher{
		interval: time.Minute,
		last:     now,
		now:      func() time.Time { return now },
		enabled:  true,
		push:     func() error { pushes++; return nil },
	}

	if got := p.maybePush(true); got != "not_due" {
		t.Errorf("a push with no time passed said %q", got)
	}
	now = now.Add(59 * time.Second)
	if got := p.maybePush(true); got != "not_due" {
		t.Errorf("a push one second early said %q", got)
	}
	now = now.Add(time.Second)
	if got := p.maybePush(true); got != "pushed" {
		t.Errorf("a due push said %q", got)
	}
	now = now.Add(time.Hour)
	if got := p.maybePush(false); got != "refused_revoked" {
		t.Errorf("a due push with a revoked credential said %q", got)
	}
	if pushes != 1 {
		t.Errorf("%d pushes, want exactly the one that was due and credentialed", pushes)
	}

	// With the guard off nothing is ever due, and durability is left to the
	// job remembering to push at exit — which a killed job never does.
	p.enabled = false
	now = now.Add(time.Hour)
	if got := p.maybePush(true); got != "not_due" {
		t.Errorf("with the timer off a push said %q", got)
	}
}

// The runner table is the one place the three agents differ, and the prompt
// reaches every one of them with no placeholder left behind.
func TestEveryKnownRunnerTakesThePrompt(t *testing.T) {
	at := launch{Prompt: "PROMPT-BODY", GitCommonDir: "/repo/.git"}
	for _, name := range KnownRunners() {
		argv, err := resolveRunner(name, nil, at)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(argv) < 2 || argv[0] != name {
			t.Errorf("%s: argv %v", name, argv)
		}
		if !contains(argv, "PROMPT-BODY") {
			t.Errorf("%s: the prompt does not reach the runner: %v", name, argv)
		}
		for _, arg := range argv {
			if strings.Contains(arg, "{{") {
				t.Errorf("%s: an unsubstituted placeholder survived: %v", name, argv)
			}
		}
	}
	if _, err := resolveRunner("emacs", nil, at); err == nil {
		t.Error("an unknown runner was accepted; the set is closed")
	}
	// An override with no placeholder gets the prompt last.
	argv, err := resolveRunner("claude", []string{"/bin/sh", "-c", "true"}, at)
	if err != nil || argv[len(argv)-1] != "PROMPT-BODY" {
		t.Errorf("override argv %v, err %v", argv, err)
	}
}

// codex sandboxes its own file access, and every attempt runs in a LINKED
// worktree whose `.git` is a FILE pointing at the repository's common
// directory. So the argv carries that directory, resolved for the attempt's
// own repository — and the executor refuses to launch rather than pass an
// empty one, because `--add-dir ""` parses, sandboxes nothing, and surfaces
// two steps later as a branch with no commits.
func TestTheCodexArgvCarriesTheResolvedGitCommonDir(t *testing.T) {
	argv, err := resolveRunner("codex", nil, launch{Prompt: "P", GitCommonDir: "/checkouts/repo/.git"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "-s", "workspace-write", "--add-dir", "/checkouts/repo/.git", "P"}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Fatalf("codex argv\n  got  %v\n  want %v", argv, want)
	}
	if contains(argv, "--full-auto") {
		t.Error("`codex exec` has no --full-auto flag; it exits 2 before the runner ever starts")
	}
	if _, err := resolveRunner("codex", nil, launch{Prompt: "P"}); err == nil {
		t.Fatal("codex was launched with no git common directory to add")
	}
}

// The value the codex argv carries is the REAL common directory of a linked
// worktree's repository — never the literal `.git`, which in a worktree is a
// file, and never the worktree's own path.
func TestTheGitCommonDirIsTheRepositorysAndNotTheWorktreesDotGit(t *testing.T) {
	repo := newRepo(t, "common")
	linked := filepath.Join(t.TempDir(), "linked")
	mustRun(t, repo.Dir, "git", "worktree", "add", "--quiet", "-b", "linked-branch", linked)

	common, err := gitCommonDir(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	fromWorktree, err := gitCommonDir(linked)
	if err != nil {
		t.Fatal(err)
	}
	if common != fromWorktree {
		t.Errorf("the repository and its linked worktree resolve different common dirs:\n  %s\n  %s", common, fromWorktree)
	}
	if !filepath.IsAbs(common) {
		t.Errorf("the common dir %q is not absolute", common)
	}
	// The linked worktree's own .git is a FILE, which is exactly why the
	// runner has to be given the directory it points at.
	info, err := os.Stat(filepath.Join(linked, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Fatal("this git makes a linked worktree's .git a directory; the add-dir argument would be unnecessary")
	}
	if index := filepath.Join(common, "index"); !fileExists(index) {
		t.Errorf("%s holds no index; it is not the directory a runner must be able to write", common)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// state and terminal come from one function, so a status cannot say a job is
// still running and terminal at the same time — the disagreement the contract
// cross-checks with its anyOf.
func TestTerminalAgreesWithState(t *testing.T) {
	for state, want := range map[string]bool{
		StatePending: false, StateStarting: false, StateRunning: false, StateLost: false,
		StateSucceeded: true, StateFailed: true, StateCancelled: true,
	} {
		if got := terminalState(state); got != want {
			t.Errorf("%s: terminal %v, want %v", state, got, want)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
