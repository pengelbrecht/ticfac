package cloudflaresandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Collect from the durable layer: the landing branch and the report the
// container's own entrypoint committed to it, read from the ORCHESTRATOR'S
// checkout the way worker-collect.ts reads them through GitHub — commits,
// the changed-file list, the report — because where collect lives was this
// tick's decision and this is the decided answer exercised.
//
// Nothing here mocks git: the property this package owns is that the same
// facts a cloud Workflow's collect reads off the remote, the Go side reads
// off the same remote through the clone it already holds. The git
// repositories are local and throwaway.

// gitRepo is one throwaway repository: a bare origin and a clone of it, the
// shape a run's checkout and its remote make.
type gitRepo struct {
	t      *testing.T
	Origin string
	Clone  string
	Base   string
}

// newGitRepo makes an origin with one commit and a clone of it, the clone
// standing in for the orchestrator's own checkout.
func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "project-origin.git")
	clone := filepath.Join(root, "project")
	mustGit(t, root, "init", "--quiet", "--bare", "-b", "main", origin)
	mustGit(t, root, "init", "--quiet", "-b", "main", clone)
	mustGit(t, clone, "config", "user.email", "orchestrator@example.com")
	mustGit(t, clone, "config", "user.name", "ticfac test")
	mustGit(t, clone, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("# project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, clone, "add", "-A")
	mustGit(t, clone, "commit", "--quiet", "-m", "base")
	mustGit(t, clone, "remote", "add", "origin", origin)
	mustGit(t, clone, "push", "--quiet", "origin", "main")
	base := strings.TrimSpace(mustGit(t, clone, "rev-parse", "HEAD"))
	return &gitRepo{t: t, Origin: origin, Clone: clone, Base: base}
}

// commitOn writes one commit on branch from dir and pushes it to origin, the
// shape a worker container's exit leaves behind.
func (g *gitRepo) commitOn(dir, branch, message string, files map[string]string) string {
	g.t.Helper()
	mustGit(g.t, dir, "checkout", "--quiet", "-B", branch, g.Base)
	for path, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			g.t.Fatal(err)
		}
		mustGit(g.t, dir, "add", "--", path)
	}
	mustGit(g.t, dir, "commit", "--quiet", "-m", message)
	mustGit(g.t, dir, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
	head := strings.TrimSpace(mustGit(g.t, dir, "rev-parse", "HEAD"))
	mustGit(g.t, dir, "checkout", "--quiet", "main")
	return head
}

// workerDir is a second clone of the origin, standing in for the worker
// container's own checkout, so the landing branch is pushed the way the
// container pushes it — from a different repository, never from the
// orchestrator's own.
func (g *gitRepo) workerDir(name string) string {
	g.t.Helper()
	dir := filepath.Join(filepath.Dir(g.Clone), name)
	mustGit(g.t, filepath.Dir(g.Clone), "clone", "--quiet", g.Origin, dir)
	mustGit(g.t, dir, "config", "user.email", "worker@example.com")
	mustGit(g.t, dir, "config", "user.name", "ticfac test worker")
	mustGit(g.t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// newCollectHarness starts one attempt against the door with the repository
// configured, then tells the door the attempt settled — the sequence the
// reconciler runs before it collects. The spec's base is the repository's
// real base commit, because the collect measures its diff from the base the
// dispatch named.
func newCollectHarness(t *testing.T) (*harness, *gitRepo, *subprocess.JobHandle, string) {
	t.Helper()
	return newCollectHarnessRole(t, "implement-tick")
}

// newCollectHarnessRole is newCollectHarness for a dispatch of any role: the
// collect's no-commits rule is the ROLE's recorded one (NoCommitsIsFailure,
// tick 19l), so a review's branch is held to a different rule than an
// implementation's and the tests state which they mean.
func newCollectHarnessRole(t *testing.T, role string) (*harness, *gitRepo, *subprocess.JobHandle, string) {
	t.Helper()
	h := newHarness(t)
	repo := newGitRepo(t)
	h.newExecutorWithRepo(t.TempDir(), repo.Clone)

	spec := h.newSpec("keh")
	spec.Source.BaseSHA = repo.Base
	spec.Role = role
	handle, err := h.ex.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.settled("keh", subprocess.StateSucceeded)

	// The landing branch is the name the door's handle carries: on the real
	// side the container derives it from its boot's two slots; here the
	// fake door derives it from the write ref the way the handle payload
	// does.
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	return h, repo, handle, payload.Branch
}

// newExecutorWithRepo points a fresh executor at the door with the repository
// configured, the way the cli factory builds one per dispatch.
func (h *harness) newExecutorWithRepo(state, repo string) *Executor {
	h.Helper()
	ex, err := New(Options{
		FactoryURL: h.door.URL(),
		Token:      "run-r1-token",
		EpicID:     "xte",
		BaseRef:    "epic/xte",
		Title:      "A cloudflare-sandbox executor that returns a handle, not a result",
		Model:      testModel,
		Harness:    testHarness,
		Prompt:     testPrompt,
		Attempt:    1,
		StateDir:   state,
		Repo:       repo,
		Remote:     "origin",
		Now:        func() time.Time { return time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		h.Fatalf("New: %v", err)
	}
	h.ex = ex
	return ex
}

// reportBody is a report with a status line the parser reads.
const reportBody = "# keh\n\nWhat changed, what ran.\n\nSTATUS: DONE\n"

// TestCollectReadsTheDurableLayer is the decided placement exercised: the
// collect reads the landing branch the container pushed and the report its
// entrypoint committed to it, from the orchestrator's own checkout, and
// answers the same verdict the other two executors would for the same facts.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectReadsTheDurableLayer(t *testing.T) {
	h, repo, handle, branch := newCollectHarness(t)

	// The worker container: a second clone, one work commit, and the report
	// its own entrypoint commits at the branch root.
	worker := repo.workerDir("worker")
	head := repo.commitOn(worker, branch, "implement keh",
		map[string]string{"internal/keh.go": "package keh\n", resultFile("keh"): reportBody})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict %q, want %q: %s", collected.Verdict, subprocess.VerdictReadyToMerge, collected.Message)
	}
	if collected.Result == nil {
		t.Fatal("no protocol record in the collection")
	}
	if collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("outcome %q, want %q", collected.Result.Outcome, subprocess.OutcomeSucceeded)
	}
	if collected.Result.Source.BaseSHA != repo.Base {
		t.Errorf("the collected base is %q, want the dispatched %q", collected.Result.Source.BaseSHA, repo.Base)
	}
	if collected.Result.Source.Commits != 1 {
		t.Errorf("counted %d commits, want 1", collected.Result.Source.Commits)
	}
	if collected.Result.Source.HeadSHA == nil || *collected.Result.Source.HeadSHA != head {
		t.Errorf("the collected head is %v, want the landing branch head %s", collected.Result.Source.HeadSHA, head)
	}
	if collected.Result.Source.WriteRef != handleWriteRef(t, handle) {
		t.Errorf("the collected write ref is %q, want the dispatched %q",
			collected.Result.Source.WriteRef, handleWriteRef(t, handle))
	}
	if !collected.HasReport {
		t.Fatal("the report the container committed to the branch was not read")
	}
	if collected.Report.Status != subprocess.StatusDone {
		t.Errorf("report status %q, want %q", collected.Report.Status, subprocess.StatusDone)
	}
	if want := branch + ":" + resultFile("keh"); collected.Report.Path != want {
		t.Errorf("report path %q, want %q: the branch is where the report lives on this substrate",
			collected.Report.Path, want)
	}
	if collected.Result.RoleResult == nil || collected.Result.RoleResult.Status != subprocess.StatusDone {
		t.Errorf("no role result carrying the worker's DONE answer: %+v", collected.Result.RoleResult)
	}
	if len(collected.BoundaryViolations) != 0 || len(collected.ArtifactViolations) != 0 {
		t.Errorf("violations on a clean branch: %v / %v", collected.BoundaryViolations, collected.ArtifactViolations)
	}
}

// TestCollectArchivesTheReportForTheNextAttempt: the report is archived
// beside the attempt record, so a later collect of the SAME attempt — a
// resumed run, whose container is long gone — reads the archive even after
// the landing branch is deleted from the remote, and a redispatch's prompt
// finds the analysis (tick nvn's discovery reads this same file).
//
// short: local throwaway git repositories; no network, no container.
func TestCollectArchivesTheReportForTheNextAttempt(t *testing.T) {
	h, repo, handle, branch := newCollectHarness(t)
	worker := repo.workerDir("worker")
	repo.commitOn(worker, branch, "implement keh",
		map[string]string{"internal/keh.go": "package keh\n", resultFile("keh"): reportBody})
	if _, err := h.ex.CollectDetail(handle); err != nil {
		t.Fatalf("first CollectDetail: %v", err)
	}
	state := h.ex.stateDirFor(handle.JobID, 1)
	if _, err := os.Stat(filepath.Join(state, subprocess.FileReportArchive)); err != nil {
		t.Fatalf("the report was not archived beside the attempt record: %v", err)
	}

	// The branch is gone from the remote — nothing on this substrate owns
	// keeping it — and a FRESH executor, the process a resumed run is, still
	// collects the attempt from the archive.
	mustGit(t, repo.Clone, "push", "--quiet", "origin", "--delete", "refs/heads/"+branch)
	fresh := h.newExecutorWithRepo(t.TempDir(), repo.Clone)
	collected, err := fresh.CollectDetail(handle)
	if err != nil {
		t.Fatalf("second CollectDetail: %v", err)
	}
	if !collected.HasReport || collected.Report.Status != subprocess.StatusDone {
		t.Errorf("the archived report was not read back: has=%v status=%q", collected.HasReport, collected.Report.Status)
	}
	if collected.Result.Source.Commits != 0 {
		t.Errorf("a deleted branch counted %d commits, want 0", collected.Result.Source.Commits)
	}
	// The branch is gone and the archived report claims DONE, which is the
	// closed vocabulary's no-commits shape — the same verdict herdr answers
	// for a readable report over an empty branch — and never a merge: the
	// only fact left is a claim with no work behind it.
	if collected.Verdict != subprocess.VerdictNoCommits {
		t.Errorf("verdict %q, want %q: a report claiming an outcome over a branch that is not there",
			collected.Verdict, subprocess.VerdictNoCommits)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome %q, want %q", collected.Result.Outcome, subprocess.OutcomeFailed)
	}
}

// TestCollectRefusesAReportOnlyAttempt: the finding this tick absorbed
// (73ba193d) as its acceptance criterion — a worker that did nothing still
// leaves one commit beyond the base, because the container's own entrypoint
// commits the report, so commits alone read it as ready-to-merge. The
// collect must refuse it with the no-commits verdict and its OWN sentence,
// the way the subprocess executor refuses a worker that committed nothing.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectRefusesAReportOnlyAttempt(t *testing.T) {
	h, repo, handle, branch := newCollectHarness(t)
	worker := repo.workerDir("worker")
	// The branch a worker that did nothing leaves: no source, no tests —
	// only the report its own entrypoint commits at the branch root, saying
	// DONE over work that does not exist.
	head := repo.commitOn(worker, branch, "commit the report", map[string]string{resultFile("keh"): reportBody})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictNoCommits {
		t.Errorf("verdict %q, want %q: a branch whose only commit is the worker's own report is "+
			"the no-work shape in this substrate's terms, never a merge", collected.Verdict, subprocess.VerdictNoCommits)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome %q, want %q: a worker that did nothing did not succeed", collected.Result.Outcome,
			subprocess.OutcomeFailed)
	}
	if !strings.Contains(collected.Message, "the container's own report") {
		t.Errorf("the refusal does not say the only commit is the report — the sentence an empty branch reads is a "+
			"lie about this one: %q", collected.Message)
	}
	// The report is still read and still archived: the refusal says what the
	// worker CLAIMED, and a redispatch's prompt reads its analysis.
	if !collected.HasReport || collected.Report.Status != subprocess.StatusDone {
		t.Errorf("the report was not read: has=%v status=%q", collected.HasReport, collected.Report.Status)
	}
	// The role result carries the verdict the run acts on, which is the
	// refusal — never the worker's own DONE over nothing.
	if collected.Result.RoleResult == nil || collected.Result.RoleResult.Result["verdict"] != subprocess.VerdictNoCommits {
		t.Errorf("the role result does not carry the refusal verdict: %+v", collected.Result.RoleResult)
	}
	// The collected head is still the branch's, a stated fact: the refusal is
	// about the WORK, not a denial that the commit exists.
	if collected.Result.Source.HeadSHA == nil || *collected.Result.Source.HeadSHA != head {
		t.Errorf("the collected head is %v, want %s", collected.Result.Source.HeadSHA, head)
	}
}

// TestCollectKeepsAReportOnlyReviewReadyToMerge: the role's recorded rule
// (NoCommitsIsFailure, tick 19l) is part of the report-only verdict exactly
// as it is of the empty-branch one — a review dispatched read-only whose
// only commit is its report delivered its whole deliverable, and the new
// check must not refuse every review this substrate dispatches.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectKeepsAReportOnlyReviewReadyToMerge(t *testing.T) {
	h, repo, handle, branch := newCollectHarnessRole(t, "review-epic")
	worker := repo.workerDir("worker")
	repo.commitOn(worker, branch, "commit the report", map[string]string{resultFile("keh"): reportBody})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict %q, want %q: a review whose only commit is its report is the answer the role exists "+
			"to deliver (%s)", collected.Verdict, subprocess.VerdictReadyToMerge, collected.Message)
	}
	if collected.Result.Outcome != subprocess.OutcomeSucceeded {
		t.Errorf("outcome %q, want %q", collected.Result.Outcome, subprocess.OutcomeSucceeded)
	}
}

// TestCollectRefusesABoundaryWrite: the tracker-record boundary is measured
// from the same shared helpers the other two collects measure it with, so
// three executors cannot disagree about one diff.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectRefusesABoundaryWrite(t *testing.T) {
	h, repo, handle, branch := newCollectHarness(t)
	worker := repo.workerDir("worker")
	repo.commitOn(worker, branch, "implement keh, and close the tick behind its back",
		map[string]string{
			"internal/keh.go":       "package keh\n",
			".tick/issues/keh.json": `{"id":"keh","status":"closed"}`,
			resultFile("keh"):       reportBody,
		})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictBoundaryViolation {
		t.Errorf("verdict %q, want %q", collected.Verdict, subprocess.VerdictBoundaryViolation)
	}
	if len(collected.BoundaryViolations) == 0 || collected.BoundaryViolations[0] != ".tick/issues/keh.json" {
		t.Errorf("boundary violations %v, want the tracker record the worker wrote", collected.BoundaryViolations)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome %q, want %q: a boundary write never merges", collected.Result.Outcome, subprocess.OutcomeFailed)
	}
	if !strings.Contains(collected.Message, "an authority that is not its own") {
		t.Errorf("the message does not carry the shared refusal sentence: %s", collected.Message)
	}
}

// TestCollectSurfacesAPreventedBoundaryAttempt: a caught violation is a
// prevented one — the container's guard swept the write, the branch reads
// clean — and the marker its guard prepends to the report is the only trace
// the diff can see nothing of. The collect carries it on the feed line
// rather than swallowing a clean branch's history.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectSurfacesAPreventedBoundaryAttempt(t *testing.T) {
	h, repo, handle, branch := newCollectHarness(t)
	worker := repo.workerDir("worker")
	repo.commitOn(worker, branch, "implement keh",
		map[string]string{"internal/keh.go": "package keh\n",
			resultFile("keh"): "# keh\n\nBOUNDARY VIOLATION ATTEMPTED: the agent tried to write .tick/issues/keh.json; the container refused\n\nSTATUS: DONE\n"})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict %q, want %q: a prevented violation is not a committed one", collected.Verdict,
			subprocess.VerdictReadyToMerge)
	}
	if !strings.Contains(collected.Message, "attempting a tracker-record write") {
		t.Errorf("the prevented attempt is not surfaced on the feed line: %q", collected.Message)
	}
}

// TestCollectWithoutAReportIsMissingResult: a container that pushed work
// commits but no report — the entrypoint's fallback never fired and the
// harness wrote nothing — collects as a failure nobody can read, not as a
// merge.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectWithoutAReportIsMissingResult(t *testing.T) {
	h, repo, handle, branch := newCollectHarness(t)
	worker := repo.workerDir("worker")
	repo.commitOn(worker, branch, "implement keh", map[string]string{"internal/keh.go": "package keh\n"})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictMissingResult {
		t.Errorf("verdict %q, want %q", collected.Verdict, subprocess.VerdictMissingResult)
	}
	if collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("outcome %q, want %q", collected.Result.Outcome, subprocess.OutcomeFailed)
	}
	if collected.Result.RoleResult != nil {
		t.Errorf("a reportless collect minted a role result: %+v", collected.Result.RoleResult)
	}
	if !strings.Contains(collected.Message, "no report") {
		t.Errorf("the message does not say the report is missing: %s", collected.Message)
	}
}

// TestCollectWithoutARepositoryRefuses: collect is the Go side's, from git —
// so an executor configured with no repository to read from refuses, rather
// than guessing a verdict about a durable layer it cannot see.
//
// short: the fake door and one state directory; no git, no network.
func TestCollectWithoutARepositoryRefuses(t *testing.T) {
	h := newHarness(t)
	h.running("keh")
	handle, err := h.start("keh")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := h.ex.CollectDetail(handle); err == nil {
		t.Fatal("a collect with no repository configured answered, rather than refusing")
	} else if !strings.Contains(err.Error(), "no repository is configured") {
		t.Errorf("the refusal does not say the repository is missing: %v", err)
	}
}

// handleWriteRef reads the write ref the handle's record carries.
func handleWriteRef(t *testing.T, handle *subprocess.JobHandle) string {
	t.Helper()
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	return payload.WriteRef
}
