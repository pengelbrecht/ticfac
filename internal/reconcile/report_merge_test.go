package reconcile

import (
	"fmt"
	"strings"
	"testing"
)

// A worker's report never reaches the integration branch, and never stops a
// merge (hn6 run_ee8e).
//
// A cloud worker COMMITS its RESULT-<tick>.md, because its branch is the only
// durable layer the collect can read (image/README.md, "It commits the
// report"). The report is a transport: collect reads it off the attempt's own
// branch and nothing reads it off the integration branch. But the integration
// merge took it in like any other file, so epic/hn6 carried eight of them,
// and 378 try 2 — whose report was a rewrite of the one 378 try 1 had already
// landed — did "not merge onto epic/hn6 (1 conflict: content in
// RESULT-378.md)". A model was dispatched to resolve a conflict in a report,
// three resolve jobs failed, and the run stopped for a person.
//
// The integration merge now drops every report: a new report does not land,
// a rewritten one does not conflict, a removed one does not come back, a
// report an older merge left on the branch leaves it with the next merge, and
// a conflict elsewhere is handed on without the report in it (obk: keeping
// each report "as the integration branch has it" undid 06t's removal of
// seven of them, because the removal arrived as the incoming side of a merge).

// reportMergeReconciler is the reconciler's merge over a mergeConflictRepo:
// `side` is the attempt, `main` the integration branch.
func reportMergeReconciler(t *testing.T, dir string, g *repoGit) (r *Reconciler, epicHead, head string) {
	t.Helper()
	r = &Reconciler{git: g, branch: "main", runID: "r-reports"}
	epicHead = strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "main"))
	head = strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "side"))
	return r, epicHead, head
}

func pathsAt(t *testing.T, dir, commit string) map[string]bool {
	t.Helper()
	paths := map[string]bool{}
	for _, p := range strings.Fields(mustRun(t, dir, "git", "ls-tree", "-r", "--name-only", commit)) {
		paths[p] = true
	}
	return paths
}

// short: one small git repository and a single merge, no harness, no runner
func TestANewWorkerReportDoesNotLandOnTheIntegrationBranch(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"README.md": "base\n"},
		map[string]string{"other.txt": "another tick's work\n"},
		map[string]string{"work.txt": "the tick's work\n", "RESULT-t1.md": "STATUS: DONE\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	merged, conflict, err := r.mergeInWorktree("t1", 1, "side", head, epicHead)
	if err != nil || conflict != nil {
		t.Fatalf("a clean merge failed: conflict %+v, err %v", conflict, err)
	}
	paths := pathsAt(t, dir, merged)
	if paths["RESULT-t1.md"] {
		t.Errorf("the worker's report landed on the integration branch")
	}
	if !paths["work.txt"] || !paths["other.txt"] {
		t.Errorf("the merge lost work: %v", paths)
	}
	parents := strings.Fields(mustRun(t, dir, "git", "rev-list", "--parents", "-n1", merged))
	if len(parents) != 3 || parents[1] != epicHead || parents[2] != head {
		t.Errorf("the merge's parents are %v, want [%s %s]", parents[1:], short(epicHead), short(head))
	}
}

// The hn6 shape: the integration branch already carries the tick's report
// (an earlier try's, from before reports were kept out), and the next try
// rewrote it.
//
// short: one small git repository and a single merge, no harness, no runner
func TestARewrittenReportIsNotAConflict(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"RESULT-378.md": "try 1\nSTATUS: DONE\n"},
		map[string]string{"RESULT-378.md": "try 1, as the resolve left it\nSTATUS: DONE\n"},
		map[string]string{"RESULT-378.md": "try 2\nSTATUS: DONE\n", "work.txt": "try 2's work\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	merged, conflict, err := r.mergeInWorktree("378", 4, "side", head, epicHead)
	if err != nil || conflict != nil {
		t.Fatalf("a conflict only in the report stopped the merge: conflict %+v, err %v", conflict, err)
	}
	if pathsAt(t, dir, merged)["RESULT-378.md"] {
		t.Errorf("the report is still on the integration branch after the merge")
	}
	if !pathsAt(t, dir, merged)["work.txt"] {
		t.Errorf("the attempt's work did not merge")
	}
}

// A report the integration branch no longer has, which the attempt (carried
// from an earlier try's branch) rewrote: modify/delete, also not a conflict.
//
// short: one small git repository and a single merge, no harness, no runner
func TestAReportTheBranchDroppedIsNotAConflict(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"RESULT-t1.md": "try 1\n", "README.md": "base\n"},
		map[string]string{"README.md": "main\n"},
		map[string]string{"RESULT-t1.md": "try 2\n", "work.txt": "work\n"},
		nil)
	mustRun(t, dir, "git", "rm", "--quiet", "RESULT-t1.md")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "main drops the report")
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	merged, conflict, err := r.mergeInWorktree("t1", 2, "side", head, epicHead)
	if err != nil || conflict != nil {
		t.Fatalf("a modify/delete of a report stopped the merge: conflict %+v, err %v", conflict, err)
	}
	if pathsAt(t, dir, merged)["RESULT-t1.md"] {
		t.Errorf("the dropped report came back")
	}
}

// A real conflict beside a report one is handed on WITHOUT the report: the
// resolve job is asked about the code, and its conflicted tree carries no
// markers in a report.
//
// short: one small git repository and a single merge, no harness, no runner
func TestAConflictBesideAReportConflictNamesOnlyTheCode(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"RESULT-t1.md": "try 1\n", "shared.txt": "base\n"},
		map[string]string{"RESULT-t1.md": "landed\n", "shared.txt": "main\n"},
		map[string]string{"RESULT-t1.md": "try 2\n", "shared.txt": "side\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	_, conflict, err := r.mergeInWorktree("t1", 2, "side", head, epicHead)
	if err != nil || conflict == nil {
		t.Fatalf("want the content conflict handed on, got conflict %+v, err %v", conflict, err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "shared.txt" {
		t.Errorf("the conflict names %v, want [shared.txt]", conflict.Files)
	}
	if strings.Contains(conflict.Detail, "RESULT-") {
		t.Errorf("the conflict's detail names the report: %q", conflict.Detail)
	}
	if !conflict.resolvable() {
		t.Errorf("the code conflict is no longer resolvable: %+v", conflict)
	}

	conflicted, err := r.conflictedTree(epicHead, head, attemptHandle{TickID: "t1", Attempt: 2,
		WriteRef: "refs/heads/ticfac/run-r/tick-t1/resolve-2"})
	if err != nil {
		t.Fatal(err)
	}
	if pathsAt(t, dir, conflicted)["RESULT-t1.md"] {
		t.Errorf("the resolve job's tree carries a report")
	}
	code, _ := showAt(dir, conflicted, "shared.txt")
	if !conflictMarkersIn(code) {
		t.Errorf("the resolve job's tree lost the code conflict: %q", code)
	}
}

// The resolve job's own container commits ITS report on its branch too; the
// merge minted from the job's tree keeps the integration branch's.
//
// short: one small git repository, one conflicted tree and one mint, no harness, no runner
func TestAResolveJobsReportIsNotMintedIntoTheMerge(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"shared.txt": "base\n"},
		map[string]string{"shared.txt": "main\n"},
		map[string]string{"shared.txt": "side\n", "RESULT-t1.md": "the attempt's report\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)
	marker := attemptHandle{TickID: "t1", Attempt: 1, WriteRef: "refs/heads/ticfac/run-r/tick-t1/resolve-1"}
	conflicted, err := r.conflictedTree(epicHead, head, marker)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "checkout", "--quiet", "--detach", conflicted)
	write(t, dir+"/shared.txt", "main and side\n")
	write(t, dir+"/RESULT-t1.md", "the resolve job's report\n")
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "resolve, and the container's report")
	resolved := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	mustRun(t, dir, "git", "checkout", "--quiet", "main")

	conflict := &mergeConflict{Files: []string{"shared.txt"}, Kind: map[string]string{"shared.txt": "content"}}
	merged, err := r.mintResolveMerge(resolved, head, epicHead, marker, conflict)
	if err != nil {
		t.Fatal(err)
	}
	if pathsAt(t, dir, merged)["RESULT-t1.md"] {
		t.Errorf("a report was minted into the integration merge")
	}
	got, _ := showAt(dir, merged, "shared.txt")
	if got != "main and side\n" {
		t.Errorf("the resolution was not minted: shared.txt %q", got)
	}
}

// The base fold's mint is the hole #169's guard did not cover (ab0cdab4):
// the resolve-conflict job of a conflicted fold starts from a tree that
// still carries the integration branch's reports (the fold brings in what
// the BASE has; it is the EPIC side that has them), and the job's container
// commits a report of its own on its branch. The merge minted from the
// job's whole tree landed a REWRITTEN RESULT-hn6.md on epic/hn6 through
// exactly this path, after the reports guard was already on the branch.
// The mint drops every report, the integration branch's included, like
// every merge into it (report_merge.go).
//
// short: one small git repository, one conflicted tree and one mint, no harness, no runner
func TestTheBaseFoldMintKeepsTheResolveJobsReportOut(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"shared.txt": "base\n"},
		map[string]string{"shared.txt": "main\n", "RESULT-t1.md": "the integration branch's report\n"},
		map[string]string{"shared.txt": "side\n", "work.txt": "the base branch's work\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)
	// `side` plays the BASE branch here: the fold of the base into the
	// integration branch, handed to the resolve-conflict job conflicted
	// (refresh_resolve.go's conflictedMerge, reports brought in as they are).
	marker := attemptHandle{JobID: "run-r/base-fold-1", Attempt: 1,
		WriteRef: "refs/heads/ticfac/run-r/base-fold-1"}
	conflicted, err := r.conflictedMerge(epicHead, head, "the conflicted base fold", nil)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "checkout", "--quiet", "--detach", conflicted)
	write(t, dir+"/shared.txt", "main and side\n")
	write(t, dir+"/RESULT-t1.md", "the resolve job rewrote the integration branch's report\n")
	write(t, dir+"/RESULT-bf.md", "the resolve job's own report\n")
	mustRun(t, dir, "git", "add", "-A")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "resolve the fold, and the container's report")
	resolved := strings.TrimSpace(mustRun(t, dir, "git", "rev-parse", "HEAD"))
	mustRun(t, dir, "git", "checkout", "--quiet", "main")

	conflict := &mergeConflict{Files: []string{"shared.txt"}, Kind: map[string]string{"shared.txt": "content"}}
	merged, err := r.mintBaseFold(resolved, head, marker, conflict,
		func(format string, args ...any) error { return fmt.Errorf(format, args...) })
	if err != nil {
		t.Fatal(err)
	}
	paths := pathsAt(t, dir, merged)
	if paths["RESULT-bf.md"] {
		t.Errorf("the resolve job's report was minted into the base-fold merge")
	}
	if paths["RESULT-t1.md"] {
		t.Errorf("the integration branch's report survived the base-fold mint")
	}
	code, _ := showAt(dir, merged, "shared.txt")
	if code != "main and side\n" {
		t.Errorf("the fold's resolution was not minted: shared.txt %q", code)
	}
	if !paths["work.txt"] {
		t.Errorf("the base branch's work did not reach the minted fold")
	}
}

// obk: 06t's commit removed the seven reports epic/hn6 carried, and the
// integration merge of that commit restored all seven, because each report
// was kept "as the integration branch has it". A removal the incoming side
// makes sticks.
//
// short: one small git repository and a single merge, no harness, no runner
func TestAReportRemovalSticksThroughTheIntegrationMerge(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"RESULT-378.md": "landed\n", "RESULT-hn6.md": "landed\n", "README.md": "base\n"},
		map[string]string{"other.txt": "another tick's work\n"},
		map[string]string{"work.txt": "06t's work\n"},
		[]string{"RESULT-378.md", "RESULT-hn6.md"})
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	merged, conflict, err := r.mergeInWorktree("06t", 1, "side", head, epicHead)
	if err != nil || conflict != nil {
		t.Fatalf("a clean merge failed: conflict %+v, err %v", conflict, err)
	}
	paths := pathsAt(t, dir, merged)
	for path := range paths {
		if isWorkerReport(path) {
			t.Errorf("the integration merge restored %s, which the merged branch removed", path)
		}
	}
	if !paths["work.txt"] || !paths["other.txt"] {
		t.Errorf("the merge lost work: %v", paths)
	}
}

// A report an older merge left on the integration branch leaves it with the
// next merge, whatever that merge is about: no report reaches main through
// the epic's PR.
//
// short: one small git repository and a single merge, no harness, no runner
func TestAReportAlreadyOnTheBranchLeavesWithTheNextMerge(t *testing.T) {
	t.Parallel()
	dir, g := mergeConflictRepo(t,
		map[string]string{"README.md": "base\n"},
		map[string]string{"RESULT-old.md": "landed before reports were kept out\n"},
		map[string]string{"work.txt": "the tick's work\n"},
		nil)
	r, epicHead, head := reportMergeReconciler(t, dir, g)

	merged, conflict, err := r.mergeInWorktree("t1", 1, "side", head, epicHead)
	if err != nil || conflict != nil {
		t.Fatalf("a clean merge failed: conflict %+v, err %v", conflict, err)
	}
	paths := pathsAt(t, dir, merged)
	if paths["RESULT-old.md"] {
		t.Errorf("the old report is still on the integration branch")
	}
	if !paths["work.txt"] || !paths["README.md"] {
		t.Errorf("the merge lost work: %v", paths)
	}
}

// short: a pure function over strings
func TestOnlyARootResultFileIsAWorkerReport(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{
		"RESULT-378.md":       true,
		"RESULT-a1b-2.md":     true,
		"RESULT.md":           false,
		"docs/RESULT-378.md":  false,
		"RESULT-378.md.orig":  false,
		"RESULT-378.txt":      false,
		"MY-RESULT-378.md":    false,
		"RESULT-../escape.md": false,
	} {
		if got := isWorkerReport(path); got != want {
			t.Errorf("isWorkerReport(%q) = %v, want %v", path, got, want)
		}
	}
}
