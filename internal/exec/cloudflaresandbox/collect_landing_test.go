package cloudflaresandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The second cloud run of one epic (epic hn6, 2026-09-29). The landing branch
// a container pushes is per ATTEMPT but not per RUN, so the second run's
// attempt 1 of a tick is dispatched onto the very name the first run's
// attempt 1 pushed. That branch was merged into the epic long ago; the epic
// has since gained the first run's state files. The container sees a branch
// that does not descend from its base, leaves it alone and pushes its work to
// `<landing>-<run id>` (image/worker.sh adopt_worker_branch). The collect
// read the recorded name regardless: "no commits beyond the base", and — its
// diff being two-dot — every file the epic had gained since the first run's
// branch was charged to the worker as a boundary write, which halted the run.

// commitAt makes one commit on top of parent in dir with files written, and
// pushes it to origin as branch; it answers the commit.
func (g *gitRepo) commitAt(dir, parent, branch, message string, files map[string]string) string {
	g.t.Helper()
	mustGit(g.t, dir, "fetch", "--quiet", "origin")
	mustGit(g.t, dir, "checkout", "--quiet", "--detach", parent)
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
	mustGit(g.t, dir, "push", "--quiet", "--force", "origin", "HEAD:refs/heads/"+branch)
	return strings.TrimSpace(mustGit(g.t, dir, "rev-parse", "HEAD"))
}

// secondRunHistory builds the epic history the second run dispatches from:
// the first run's attempt pushed on the landing name, merged into the epic,
// and the epic then carrying that run's state files and tracker records. It
// answers the epic head — the base the second run's attempt is cut from.
func secondRunHistory(t *testing.T, repo *gitRepo, landing string) string {
	t.Helper()
	first := repo.workerDir("first-run-worker")
	firstHead := repo.commitAt(first, repo.Base, landing, "first run: implement keh",
		map[string]string{"internal/keh.go": "package keh // first run\n", resultFile("keh"): reportBody})
	epic := repo.workerDir("epic")
	return repo.commitAt(epic, firstHead, "epic/xte", "the first run's state lands on the epic",
		map[string]string{
			".ticfac/runs/run-first/checkpoint.json": `{"state":"failed"}`,
			".ticfac/runs/run-first/attempts/1.json": `{"attempt":1}`,
			".tick/issues/keh.json":                  `{"id":"keh","status":"in_progress"}`,
		})
}

// startAt starts attempt 1 of keh with base as the dispatched base, and tells
// the door it settled. It answers the handle and the landing branch the
// handle names.
func startAt(t *testing.T, h *harness, base string) (*subprocess.JobHandle, string) {
	t.Helper()
	spec := h.newSpec("keh")
	spec.Source.BaseSHA = base
	handle, err := h.ex.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.settled("keh", subprocess.StateSucceeded)
	payload, err := local(handle)
	if err != nil {
		t.Fatalf("decode the handle payload: %v", err)
	}
	return handle, payload.Branch
}

// TestCollectReadsTheRunsOwnBranchWhenAnotherRunHoldsTheLandingName is the
// hn6 stall: the work is on the per-run branch the container pushed, and the
// collect reads it there — ready to merge, one commit, the worker's own
// files and nothing the epic gained.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectReadsTheRunsOwnBranchWhenAnotherRunHoldsTheLandingName(t *testing.T) {
	h := newHarness(t)
	repo := newGitRepo(t)
	h.newExecutorWithRepo(t.TempDir(), repo.Clone)
	landing := "ticfac/run-" + h.door.runID + "/tick-keh/attempt-1"
	base := secondRunHistory(t, repo, landing)
	mustGit(t, repo.Clone, "fetch", "--quiet", "origin") // the orchestrator holds its epic
	handle, branch := startAt(t, h, base)
	if branch != landing {
		t.Fatalf("the handle names landing branch %q, want %q", branch, landing)
	}

	// The second run's container: the landing name is taken by a branch
	// that is not cut from its base, so it pushes beside it.
	own := branch + "-" + h.door.runID
	worker := repo.workerDir("second-run-worker")
	head := repo.commitAt(worker, base, own, "second run: implement keh",
		map[string]string{"internal/keh.go": "package keh // second run\n", resultFile("keh"): reportBody})

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Fatalf("verdict %q, want %q: %s", collected.Verdict, subprocess.VerdictReadyToMerge, collected.Message)
	}
	if got := collected.Result.Source.HeadSHA; got == nil || *got != head {
		t.Errorf("the collected head is %v, want the second run's own push %s on %s", got, head, own)
	}
	if collected.Result.Source.Commits != 1 {
		t.Errorf("counted %d commits, want 1", collected.Result.Source.Commits)
	}
	if len(collected.BoundaryViolations) != 0 {
		t.Errorf("boundary violations %v: the epic's own state files were charged to the worker",
			collected.BoundaryViolations)
	}
	if want := own + ":" + resultFile("keh"); collected.Report.Path != want {
		t.Errorf("report path %q, want %q: the report is read off the branch the work is on", collected.Report.Path, want)
	}
}

// TestCollectNeverRulesOnAnotherRunsBranch: the container of this run never
// pushed, and the landing name holds only the first run's merged branch. The
// collect must not read that branch as this attempt's — not as "no commits"
// with a DONE report, and never as boundary writes of the epic's own files.
//
// short: local throwaway git repositories; no network, no container.
func TestCollectNeverRulesOnAnotherRunsBranch(t *testing.T) {
	h := newHarness(t)
	repo := newGitRepo(t)
	h.newExecutorWithRepo(t.TempDir(), repo.Clone)
	landing := "ticfac/run-" + h.door.runID + "/tick-keh/attempt-1"
	base := secondRunHistory(t, repo, landing)
	mustGit(t, repo.Clone, "fetch", "--quiet", "origin") // the orchestrator holds its epic
	handle, _ := startAt(t, h, base)

	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("CollectDetail: %v", err)
	}
	if len(collected.BoundaryViolations) != 0 {
		t.Errorf("boundary violations %v: files the BASE gained are not writes this attempt made",
			collected.BoundaryViolations)
	}
	if collected.HasReport {
		t.Errorf("the first run's report was read as this attempt's (%s)", collected.Report.Path)
	}
	if collected.Result.Source.HeadSHA != nil {
		t.Errorf("the collected head is %s: another run's branch is not this attempt's work",
			*collected.Result.Source.HeadSHA)
	}
}

// TestBoundaryDiffIsTheAttemptsOwnCommits pins the measure itself: the paths
// an attempt changed are those its own commits change above the merge base,
// never the tree difference to a base that moved on.
//
// short: local throwaway git repositories; no network, no container.
func TestBoundaryDiffIsTheAttemptsOwnCommits(t *testing.T) {
	repo := newGitRepo(t)
	dir := repo.workerDir("history")
	behind := repo.commitAt(dir, repo.Base, "older", "an older head",
		map[string]string{"internal/old.go": "package old\n"})
	base := repo.commitAt(dir, behind, "epic", "the base moves on",
		map[string]string{".tick/issues/x.json": `{}`, ".ticfac/runs/r/checkpoint.json": `{}`})
	diverged := repo.commitAt(dir, behind, "diverged", "work cut from the older head",
		map[string]string{"internal/work.go": "package work\n"})
	mustGit(t, repo.Clone, "fetch", "--quiet", "origin")

	if got, err := changedPaths(repo.Clone, base, behind); err != nil || len(got) != 0 {
		t.Errorf("an ancestor of the base changed %v (err %v), want nothing", got, err)
	}
	got, err := changedPaths(repo.Clone, base, diverged)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"internal/work.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a diverged head changed %v, want only its own %v", got, want)
	}
}
