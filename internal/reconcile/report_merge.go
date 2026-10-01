package reconcile

import (
	"fmt"
	"regexp"
	"strings"
)

// A worker's report stays out of the integration branch (hn6 run_ee8e).
//
// A cloud worker COMMITS its RESULT-<tick>.md on its branch, because the
// branch is the only durable layer its collect can read (image/README.md, "It
// commits the report"); a local worker leaves it uncommitted. Either way the
// report is a TRANSPORT: the collect reads it off the attempt's own branch
// (exec/cloudflaresandbox/collect.go, exec/subprocess), a takeover reads the
// report-only commits BEYOND the integration branch (takeover.go), and
// nothing — the integrated gate, the final review, the close-out, the
// contracts — reads one off the integration branch. Merged in like any other
// file, they piled up on epic/hn6 (eight of them), and 378 try 2's rewrite of
// the report 378 try 1 had already landed was a content conflict: a model was
// dispatched to resolve a report, three resolve jobs failed, and the run
// stopped for a person over a file no reader needed.
//
// So every merge INTO the integration branch keeps each report path exactly
// as the integration branch already has it: a new report does not land, a
// rewritten one does not conflict, and a dropped one does not come back. It
// is mechanical — no model is asked about a report — and it leaves the
// merge's parentage alone, so an attempt's head is still contained in the
// branch it merged into. The reports already on a branch stay until somebody
// removes them; this only stops new ones landing.

// workerReport is the spelling sandboximage.WorkerResultFile writes: a root
// RESULT-<tick>.md.
var workerReport = regexp.MustCompile(`^RESULT-[A-Za-z0-9][A-Za-z0-9_-]*\.md$`)

// isWorkerReport says whether a repository path is a worker's report.
func isWorkerReport(path string) bool { return workerReport.MatchString(path) }

// keepReportsOut sets every worker report in the worktree `dir` — mid-merge,
// conflicted or not — to what `keep` has: its content where `keep` carries
// the path, absent where it does not. Reports live at the root, so the
// candidates are the root entries of the index and of `keep`.
func (r *Reconciler) keepReportsOut(dir, keep string) error {
	candidates := map[string]bool{}
	indexed, err := r.git.run(dir, "ls-files")
	if err != nil {
		return fmt.Errorf("list the merge's paths: %w", err)
	}
	for _, path := range strings.Split(indexed, "\n") {
		if isWorkerReport(path) {
			candidates[path] = true
		}
	}
	kept := map[string]bool{}
	rooted, err := r.git.run(dir, "ls-tree", "--name-only", keep)
	if err != nil {
		return fmt.Errorf("list the root of %s: %w", short(keep), err)
	}
	for _, path := range strings.Split(rooted, "\n") {
		if isWorkerReport(path) {
			candidates[path] = true
			kept[path] = true
		}
	}
	for path := range candidates {
		if kept[path] {
			if _, _, err := r.git.try(dir, "checkout", keep, "--", path); err != nil {
				return fmt.Errorf("keep %s as %s has it: %w", path, short(keep), err)
			}
			continue
		}
		if _, _, err := r.git.try(dir, "rm", "--quiet", "-f", "--ignore-unmatch", "--", path); err != nil {
			return fmt.Errorf("keep %s out of the merge: %w", path, err)
		}
	}
	return nil
}

// mergeKeepingReportsOut is `git merge --no-ff -m message head` in `dir`
// (checked out at `epicHead`) with the worker reports kept out: the merge is
// made without committing, the reports are set to the epic head's, and the
// merge is committed when nothing else is left unmerged. When something is,
// the merge is aborted and git's output comes back with the reports' own
// conflict lines removed, for describeMergeFailure and classifyMergeFailure
// to read as they always have; `unmerged` then names what is left.
func (r *Reconciler) mergeKeepingReportsOut(dir, epicHead, message, head string) (stdout, stderr, unmerged string, err error) {
	// Changelogs merge as a union (union_merge.go).
	union, removeUnion, uerr := unionMergeConfig()
	if uerr != nil {
		return "", "", "", uerr
	}
	defer removeUnion()
	stdout, stderr, err = r.git.try(dir, append(union, "merge", "--no-ff", "--no-commit", "-m", message, head)...)
	if err != nil {
		unmerged, _ = r.git.run(dir, "diff", "--name-only", "--diff-filter=U")
		if strings.TrimSpace(unmerged) == "" {
			// No conflict at all: an operational failure, as it always was.
			_, _, _ = r.git.try(dir, "merge", "--abort")
			return stdout, stderr, unmerged, err
		}
	}
	if kerr := r.keepReportsOut(dir, epicHead); kerr != nil {
		_, _, _ = r.git.try(dir, "merge", "--abort")
		return stdout, stderr, "", kerr
	}
	unmerged, _ = r.git.run(dir, "diff", "--name-only", "--diff-filter=U")
	if strings.TrimSpace(unmerged) != "" {
		_, _, _ = r.git.try(dir, "merge", "--abort")
		if err == nil {
			err = fmt.Errorf("git merge left %s unmerged", strings.Join(strings.Fields(unmerged), ", "))
		}
		return withoutReportConflicts(stdout), withoutReportConflicts(stderr), unmerged, err
	}
	if _, cstderr, cerr := r.git.try(dir, "commit", "--quiet", "-m", message); cerr != nil {
		_, _, _ = r.git.try(dir, "merge", "--abort")
		return stdout, cstderr, "", fmt.Errorf("commit the merge of %s: %w", short(head), cerr)
	}
	return stdout, stderr, "", nil
}

// withoutReportConflicts drops git's CONFLICT lines about a worker report:
// those conflicts were resolved mechanically, and a refusal or a resolve job
// that named them would be about a file nobody needs.
func withoutReportConflicts(output string) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CONFLICT (") && namesWorkerReport(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func namesWorkerReport(line string) bool {
	for _, field := range strings.Fields(line) {
		if isWorkerReport(strings.TrimRight(field, ".,;:")) {
			return true
		}
	}
	return false
}

// treeWithoutReports is the tree of `commit` with every worker report set to
// what `keep` has — the resolve job's tree, minted into the integration
// merge without the report its container committed.
func (r *Reconciler) treeWithoutReports(commit, keep string) (string, error) {
	dir, remove, err := r.git.tempWorktree("ticfac-mint-", commit)
	if err != nil {
		return "", fmt.Errorf("prepare a worktree at %s: %w", short(commit), err)
	}
	defer remove()
	if err := r.keepReportsOut(dir, keep); err != nil {
		return "", err
	}
	return r.git.run(dir, "write-tree")
}
