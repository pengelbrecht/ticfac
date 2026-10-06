package cli

// The tracker the dashboard reads is the INTEGRATION BRANCH's (hn6, tick
// gmo): an operator watching a cloud run from a main checkout must see the
// ticks the integration branch closed, and the epic's whole table — closed
// tasks included, with the record fields the graph endpoint omits enriched
// from the same branch's `tk list --all`. A checkout with no origin, or a
// branch origin does not have yet, falls back to the checkout's own tree.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/tk"
)

// integrationTrackerFixture is a checkout whose origin carries the
// integration branch's tracker: an epic, a closed child, and a closed
// duplicate naming the tick it duplicates — the fields the graph endpoint
// omits. The CHECKOUT's own tree carries an OLDER tracker (the same records
// before the closes), which is the whole shape of the defect: a tree the
// operator's checkout has is not where the epic's truth is.
func integrationTrackerFixture(t *testing.T) (repo string) {
	t.Helper()
	if _, err := exec.LookPath("tk"); err != nil {
		t.Skip("tk is not on the path")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	repo = filepath.Join(root, "checkout")

	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
		return string(out)
	}
	write := func(dir, rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run(root, "init", "--quiet", "--bare", "-b", "main", bare)
	run(root, "init", "--quiet", "-b", "main", seed)
	run(seed, "config", "user.email", "t@example.com")
	run(seed, "config", "user.name", "t")

	// The epic on the default branch, as an operator's checkout holds it:
	// everything open.
	write(seed, ".tick/config.json", `{"version":1}`)
	write(seed, ".tick/issues/qeu.json", `{
  "id": "qeu", "title": "the epic", "status": "in_progress", "priority": 1,
  "type": "epic", "owner": "t@example.com", "created_by": "t@example.com",
  "created_at": "2026-09-01T00:00:00Z", "updated_at": "2026-09-01T00:00:00Z",
  "acceptance_criteria": "[A1] done"
}`)
	write(seed, ".tick/issues/c1.json", `{
  "id": "c1", "title": "a tick", "status": "open", "priority": 2,
  "type": "task", "owner": "t@example.com", "parent": "qeu",
  "created_by": "t@example.com", "created_at": "2026-09-01T00:00:00Z",
  "updated_at": "2026-09-01T00:00:00Z"
}`)
	run(seed, "add", "-A")
	run(seed, "commit", "--quiet", "-m", "the tracker, open")
	run(seed, "push", "--quiet", bare, "main")

	// The integration branch, where the run's own tracker writes land: the
	// same epic with the tick closed and a duplicate closed beside it.
	run(seed, "checkout", "--quiet", "-b", "epic/qeu")
	write(seed, ".tick/issues/c1.json", `{
  "id": "c1", "title": "a tick", "status": "closed", "priority": 2,
  "type": "task", "owner": "ticfac", "parent": "qeu",
  "created_by": "t@example.com", "created_at": "2026-09-01T00:00:00Z",
  "updated_at": "2026-09-02T00:00:00Z", "closed_at": "2026-09-02T00:00:00Z"
}`)
	write(seed, ".tick/issues/c2.json", `{
  "id": "c2", "title": "the duplicate", "status": "closed", "priority": 2,
  "type": "task", "owner": "ticfac", "parent": "qeu",
  "created_by": "t@example.com", "created_at": "2026-09-01T00:00:00Z",
  "updated_at": "2026-09-02T00:00:00Z", "closed_at": "2026-09-02T00:00:00Z",
  "notes": "2026-09-02 - ticfac run run_x: closed as a duplicate of c1. Both were promoted from finding f1, and the earlier one stands"
}`)
	run(seed, "add", "-A")
	run(seed, "commit", "--quiet", "-m", "the tracker, closed")
	run(seed, "push", "--quiet", bare, "epic/qeu")

	run(root, "clone", "--quiet", bare, repo)
	run(repo, "config", "user.name", "t")
	run(repo, "config", "user.email", "t@example.com")
	// A file:// spelling of the same remote: a bare PATH is a remote format
	// tk refuses to parse as a project, and this fixture stands for the
	// https/ssh origins every real checkout names.
	run(repo, "remote", "set-url", "origin", "file://"+bare)
	// The checkout stays on main: the older tracker is all its tree has.
	return repo
}

// TestTheIntegrationBranchTrackerAnswers: the graph read from the
// integration branch carries the CLOSED tasks and the duplicate's record —
// what a checkout-on-main tree could never show — and the graph's tasks are
// enriched with the fields the endpoint omits.
func TestTheIntegrationBranchTrackerAnswers(t *testing.T) {
	repo := integrationTrackerFixture(t)
	graph := graphAtIntegrationBranch(context.Background(), repo, "qeu")
	if graph == nil {
		t.Fatal("the integration branch's tracker answered nothing")
	}
	if graph.Epic.ID != "qeu" {
		t.Fatalf("the graph names epic %+v, want qeu", graph.Epic)
	}
	byID := map[string]tk.GraphTask{}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			byID[task.ID] = task
		}
	}
	if len(byID) != 2 {
		t.Fatalf("the graph carried %d tasks (%v), want the epic's two children with the closed ones included", len(byID), byID)
	}
	c1 := byID["c1"]
	if c1.Status != "closed" || c1.ClosedAt == "" {
		t.Errorf("c1 reads %+v, want the closed task with the tracker's closed_at enriched in", c1)
	}
	c2 := byID["c2"]
	if !strings.Contains(c2.Notes, "closed as a duplicate of c1") {
		t.Errorf("c2's notes did not ride from the list read: %+v", c2)
	}
}

// TestEpicGraphPrefersTheIntegrationBranch: the graph the status model
// gathers reads the integration branch when origin has it — the closed
// ticks ride from a branch the checkout is not on — and falls back to the
// checkout's own tree when the branch is not on origin.
func TestEpicGraphPrefersTheIntegrationBranch(t *testing.T) {
	repo := integrationTrackerFixture(t)

	graph := epicGraph(context.Background(), repo, "qeu")
	if graph == nil {
		t.Fatal("the tracker answered nothing")
	}
	closed := 0
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Status == "closed" {
				closed++
			}
		}
	}
	if closed != 2 {
		t.Fatalf("the graph from the integration branch carries %d closed tasks, want both closes", closed)
	}

	// The checkout's own tree, behind the fallback: a clone whose origin
	// has no epic branch answers from the tree, open statuses included.
	withoutBranch := repo
	if out, err := exec.Command("git", "-C", withoutBranch, "push", "origin", "--delete", "epic/qeu").CombinedOutput(); err != nil {
		t.Fatalf("delete the epic branch from origin: %v\n%s", err, out)
	}
	graph = epicGraph(context.Background(), withoutBranch, "qeu")
	if graph == nil {
		t.Fatal("the checkout's own tracker answered nothing behind the fallback")
	}
	fellBack := false
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.ID == "c1" && task.Status == "open" {
				fellBack = true
			}
		}
	}
	if !fellBack {
		t.Fatalf("the fallback did not read the checkout's own (older) tree: %+v", graph.Waves)
	}
}

// TestGraphAtIntegrationBranchWithoutOriginIsNil: a checkout with no origin
// is nothing to read a branch from — nil, the caller's fallback, never an
// error.
func TestGraphAtIntegrationBranchWithoutOriginIsNil(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "--quiet", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if graph := graphAtIntegrationBranch(context.Background(), repo, "qeu"); graph != nil {
		t.Fatalf("a repo with no origin answered %+v, want nil", graph)
	}
}
