package subprocess

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// A stopped attempt's preserved work (tick pbb; the wall-clock close-path
// half landed as tick rj0, which needed the snapshot before the pane close it
// was building and implemented the minimal version itself).
//
// The wall clock is the only mechanical bound on a worker that is alive but
// lost, so what the bound does to the work is what the bound costs. A stop at
// the wall fires on an attempt holding exactly the state the Phase 3 worker
// held: real implementation, nothing committed, minutes from done — and
// before this, every teardown path that destroyed a worktree destroyed that
// state with it, unrecorded.
//
// The snapshot is taken ONCE, wherever this run destroys a worktree that may
// hold uncommitted work:
//   - the herdr wall-clock pane close, which takes the checkout with the pane
//     (rj0's gate, kept where it is);
//   - this package's dispose, whose worktree remove is --force — the one
//     other unconditional destruction, and the path a REJECTED attempt or a
//     person's release takes.
//
// It is deliberately NOT taken anywhere else. Every other teardown refuses
// rather than destroys — herdr's worktree.remove without Force answers
// workspace-refused over dirt, and neither executor deletes a branch whose
// commits no remote has — so the work survives those paths in place, and a
// snapshot there would only litter the repository. An operator release that
// means to CARRY work forward does it with real commits (--carry-work), not
// with a snapshot.
//
// The snapshot is NOT evidence of completion and is never merged. It is
// material a LATER attempt of the same tick is pointed at in its prompt —
// the nvn shape for reports, applied to work — and it is pruned by
// PurgeState, the same explicit step that retires the record naming it:
// nothing else prunes it, because it must outlive the attempt's own teardown
// exactly as long as the archived report does.

// FileWIPSnapshot is where an executor records that an attempt's uncommitted
// work was preserved before teardown destroyed it: wip-snapshot.json beside
// the attempt record. It is EXPORTED for the same reason FileReportArchive
// is: the reconciler's dispatch walks a PREDECESSOR's state directory for it
// (tick pbb), so the name is a contract with the reconciler rather than an
// executor internal — and both executors record the same shape at the same
// name rather than two that can drift.
const FileWIPSnapshot = "wip-snapshot.json"

// WIPSnapshot is the durable record of where one attempt's uncommitted work
// was preserved: a ref of its own in the repository the worktree belonged
// to, pointing at a commit whose tree is the worktree as it stood. It is not
// evidence of completion, is never merged, and rides on no boundary-excluded
// path.
type WIPSnapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Ref           string `json:"ref"`
	Commit        string `json:"commit"`
	TakenAt       string `json:"taken_at"`
}

// WIPSnapshotSchemaVersion is the one shape this record has ever had.
const WIPSnapshotSchemaVersion = 1

// PriorSnapshot is one predecessor attempt's preserved worktree, as a
// dispatch hands it to the executor that renders the worker prompt (tick pbb,
// the nvn shape for reports). Attempt is the predecessor's own attempt
// number; Ref is the wip ref its work is preserved on and Commit the
// snapshot commit it names.
//
// The type lives in this package for the same reason PriorReport does: the
// reconciler discovers the predecessors, both executors render the same
// section from the same facts, and a herdr worker whose prompt framed a
// predecessor's work differently from a local one would be two answers to the
// same question.
type PriorSnapshot struct {
	Attempt int    `json:"attempt"`
	Ref     string `json:"ref"`
	Commit  string `json:"commit"`
}

// PriorSnapshotsSection is the prompt section that points a re-dispatched
// attempt at the work its stopped predecessors left behind. Exported because
// BOTH executors render it — the worker prompt is one job contract however
// the agent is delivered.
//
// The framing is the whole point, as it is for the reports: a snapshot is
// work as it stood when the attempt stopped — uncommitted, unreviewed,
// possibly wrong — and an attempt that merges it, pushes it or treats it as
// a verdict inherits every error it holds. The list is rendered NEWEST FIRST,
// whatever order it arrives in, exactly as the reports are.
func PriorSnapshotsSection(prior []PriorSnapshot) string {
	if len(prior) == 0 {
		return ""
	}
	ordered := append([]PriorSnapshot{}, prior...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Attempt > ordered[j].Attempt })

	var b strings.Builder
	fmt.Fprintf(&b, "## Prior attempts' preserved work — material to read, never to merge\n\n")
	fmt.Fprintf(&b, "This tick has earlier attempts whose work was stopped and preserved before their\n")
	fmt.Fprintf(&b, "worktrees were torn down. A snapshot is NOT evidence of completion: it is the work\n")
	fmt.Fprintf(&b, "as it stood when the attempt stopped — uncommitted, unreviewed, possibly wrong.\n")
	fmt.Fprintf(&b, "Never merge a snapshot, never push it, and never treat it as a verdict; read it as a\n")
	fmt.Fprintf(&b, "head start you must verify against this repository as it stands. Newest first:\n\n")
	for _, p := range ordered {
		fmt.Fprintf(&b, "- attempt %d — preserved on %s (commit %s): `git show %s:<path>` reads one\n",
			p.Attempt, p.Ref, p.Commit, p.Ref)
		fmt.Fprintf(&b, "  preserved file; `git diff %s^ %s` is everything the stop interrupted\n", p.Ref, p.Ref)
	}
	fmt.Fprintf(&b, "\n")
	return b.String()
}

// WIPSnapshotNote is the sentence a REFUSAL owes an attempt whose work was
// preserved (tick lj4). It is exported and shared by both executors for the
// same reason BoundaryRefusal is: one attempt's snapshot must read the same
// whichever collect found it, and a second copy of the wording is how the two
// come to describe one ref differently.
//
// The occasion for it is a real one. 9fc attempt 4 of epic ncv was refused as
// "no-commits: the attempt branch carries no commit beyond the base it was cut
// from", while this very mechanism had already put four files and 433 lines on
// refs/ticfac/wip/run-epic-ncv/tick-9fc/attempt-4. The operator was told the
// branch was empty and given no hint that the hour of work sat at a known ref,
// so they went and rescued it BY HAND — with the snapshot's own record on
// screen. A safety net nobody is told about is barely a safety net.
//
// So the note says three things, and refuses to imply a fourth:
//
//   - WHERE it is: the ref and the commit, as facts, not as a place to look;
//   - WHAT it is: a snapshot of an interrupted worktree — uncommitted,
//     unreviewed, possibly wrong — never evidence of completion and never
//     merged, the same framing PriorSnapshotsSection gives the worker;
//   - HOW to look at it, in one command a person can paste.
//
// And it states plainly what a re-dispatch will NOT do: the next attempt is
// cut from the integration branch, not from this snapshot. That is the
// question an operator reading a refusal actually has, and leaving it to be
// inferred is how the refusal misled in the first place.
func WIPSnapshotNote(snap WIPSnapshot) string {
	if snap.Ref == "" || snap.Commit == "" {
		return ""
	}
	return fmt.Sprintf("the work it had not committed is preserved at %s (commit %s): a work-in-progress "+
		"snapshot of an interrupted worktree — not evidence of completion, never merged — and `git diff %s^ %s` "+
		"is everything the stop interrupted. A re-dispatch of this tick is still cut from the integration "+
		"branch, not from this snapshot; what the next attempt gets is its prompt pointed at this ref",
		snap.Ref, short(snap.Commit), snap.Ref, snap.Ref)
}

// ---------------------------------------------------------------------------
// The mechanics: preserving a worktree on a ref of its own.
// ---------------------------------------------------------------------------

// WipRefFor is the ref one attempt's preserved work lives on (tick rj0, per
// pbb): refs/ticfac/wip/<run>/<tick>/<attempt>, derived from the job id —
// which already names the run, the tick and the attempt — with every path
// segment sanitised, because a job id is opaque to an executor and any
// character of it may be one a ref cannot carry. The ref is OUTSIDE
// refs/heads, so it never presents itself as a branch the run or a person
// could merge by accident: the snapshot is material a later attempt can be
// POINTED at, never evidence of completion.
func WipRefFor(jobID string) string {
	var segments []string
	for _, seg := range strings.Split(jobID, "/") {
		var b strings.Builder
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
				r == '-', r == '_', r == '.':
				b.WriteRune(r)
			default:
				b.WriteRune('-')
			}
		}
		clean := strings.Trim(b.String(), "-.")
		if clean != "" {
			segments = append(segments, clean)
		}
	}
	if len(segments) == 0 {
		segments = []string{"attempt"}
	}
	return "refs/ticfac/wip/" + strings.Join(segments, "/")
}

// SnapshotWorktree preserves the worktree as it stands — every tracked change
// and every untracked file — on the given ref, without touching the
// worktree's own index, branch or HEAD. It stages into a PRIVATE index seeded
// from HEAD, writes a tree, wraps it in a commit whose parent is HEAD, and
// points the ref at it; the worktree is left exactly as it was found.
//
// The exclusions are the boundary's, at snapshot scale: nothing under .tick/
// or .ticfac/ and nothing under the attempt's own artifact prefix — which is
// where the report lives, archived separately — may ride along as NEW work.
// Tracked content those paths already carry in HEAD stays as HEAD has it,
// because the snapshot never UNDOES a commit. Build output needs no rule of
// its own (--exclude-standard honours .gitignore), and a build product the
// repository deliberately tracks is repository content, not a snapshot
// exclusion.
func SnapshotWorktree(worktree, ref, artifactPrefix string) (commit string, err error) {
	tmpDir, err := os.MkdirTemp("", "ticfac-wip")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	index := filepath.Join(tmpDir, "index")
	indexEnv := []string{"GIT_INDEX_FILE=" + index}

	if _, err := snapshotGit(worktree, indexEnv, "read-tree", "HEAD"); err != nil {
		return "", fmt.Errorf("seed the snapshot index from HEAD: %w", err)
	}
	// The stage is NOT `git add`: add refuses a pathspec that matches an
	// ignored directory — and the attempt's own artifact prefix is in this
	// worktree's info/exclude, put there by Start — so an add-based snapshot
	// would fail on exactly the paths it exists to exclude. ls-files plus
	// one update-index is the same walk with no advice machinery: the
	// tracked-at-HEAD set from the seeded private index, the
	// untracked-and-not-ignored set from the worktree, minus the boundary
	// paths, piped into a single --add --remove.
	listed, err := snapshotGit(worktree, indexEnv, "ls-files", "-z", "-c", "-o", "--exclude-standard", "--", ".")
	if err != nil {
		return "", fmt.Errorf("list the worktree's files: %w", err)
	}
	prefix := strings.Trim(strings.TrimSpace(artifactPrefix), "/")
	var kept []byte
	for _, path := range strings.Split(listed, "\x00") {
		if path == "" || snapshotExcluded(path, prefix) {
			continue
		}
		kept = append(kept, []byte(path)...)
		kept = append(kept, 0)
	}
	if len(kept) > 0 {
		if _, err := snapshotGitStdin(worktree, indexEnv, kept, "update-index", "-z", "--add", "--remove", "--stdin"); err != nil {
			return "", fmt.Errorf("stage the worktree's changes: %w", err)
		}
	}
	tree, err := snapshotGit(worktree, indexEnv, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write the snapshot tree: %w", err)
	}
	commit, err = git(worktree, "commit-tree", tree, "-p", "HEAD",
		"-m", "ticfac: work-in-progress snapshot at the wall-clock stop — not evidence, never merged")
	if err != nil {
		return "", fmt.Errorf("commit the snapshot: %w", err)
	}
	if _, err := git(worktree, "update-ref", ref, commit); err != nil {
		return "", fmt.Errorf("point %s at the snapshot: %w", ref, err)
	}
	return commit, nil
}

// snapshotExcluded is the boundary at snapshot scale (pbb's care, rj0's
// rule): nothing under .tick/ or .ticfac/ and nothing under the attempt's
// own artifact prefix — where the report lives, archived separately — may
// ride along as work. Any path SEGMENT naming those directories is excluded,
// at any depth; tracked content those paths already carry in HEAD stays as
// HEAD has it, because the snapshot never undoes a commit — what is excluded
// is the agent's uncommitted writing under them. The prefix is trimmed HERE
// rather than at either caller, so a caller passing the spec's raw value
// (with its trailing slash) and one passing a cleaned one answer the same.
func snapshotExcluded(path, artifactPrefix string) bool {
	prefix := strings.Trim(strings.TrimSpace(artifactPrefix), "/")
	if prefix != "" && (path == prefix || strings.HasPrefix(path, prefix+"/")) {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".tick" || seg == ".ticfac" {
			return true
		}
	}
	return false
}

// UncommittedWork answers whether the worktree holds uncommitted work a
// snapshot exists to preserve: any tracked change or untracked file, counted
// AFTER the boundary exclusions — nothing under .tick/ or .ticfac/ and
// nothing under the artifact prefix is the snapshot's to carry, so it is not
// the gate's to count either. A worktree whose only dirt is its own
// unarchived report is clean, and a clean worktree is not snapshotted: one
// wip ref per ordinarily-disposed attempt would bury the stopped ones in
// noise.
func UncommittedWork(worktree, artifactPrefix string) (bool, error) {
	out, err := git(worktree, "status", "--porcelain", "-z", "-uall", "--no-renames")
	if err != nil {
		return false, err
	}
	for _, entry := range strings.Split(out, "\x00") {
		// One porcelain record is XY, a space, then the path; --no-renames
		// keeps every record one field wide, and -z splits on NUL.
		if len(entry) < 4 || snapshotExcluded(entry[3:], artifactPrefix) {
			continue
		}
		return true, nil
	}
	return false, nil
}

// snapshotGit is git with extra environment, which exists for one caller: the
// wip snapshot stages into a PRIVATE index (GIT_INDEX_FILE), so it never
// touches the worktree's own index — the agent may still be alive and running
// its own git when the snapshot is taken, and a snapshot that disturbed the
// working state it is preserving would be worse than none.
func snapshotGit(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// A git that reads the invoking user's hooks, editors or pagers is a git
	// that can block forever in a non-interactive executor.
	cmd.Env = append(append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
	), env...)
	out, err := cmd.Output()
	if err != nil {
		return "", &gitError{args: args, dir: dir, stderr: stderr.String(), err: err}
	}
	return strings.TrimSpace(string(out)), nil
}

// snapshotGitStdin is snapshotGit with stdin, for the one call that takes a
// path list on standard input: the snapshot's update-index.
func snapshotGitStdin(dir string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Env = append(append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
	), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		return "", &gitError{args: args, dir: dir, stderr: stderr.String(), err: err}
	}
	return strings.TrimSpace(string(out)), nil
}
