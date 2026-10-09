package reconcile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
)

// A protected-path change gets a run channel, never a person (epic-v5t,
// 2026-10-07).
//
// Tick tda's acceptance put two named cloud configs in
// `.tick/runners.cloud.toml`. The worker boundary refuses that file
// (subprocess.OutsideBoundary), so the worker parked the block in testdata
// and filed a high-severity finding naming done item A1. The absorption
// policy absorbed it as tick yck, whose ONLY deliverable was the protected
// edit; every worker on yck answered BLOCKED with a `sed … >>` for the
// operator, the hold was always-ask, and the run ended failed waiting for a
// person — a human-only step in a factory whose one planned human touch point
// is the merge.
//
// Two rules close it:
//
//   - ABSORPTION NEVER CREATES A TICK NO WORKER CAN DO. A finding whose
//     deliverable is an edit of a protected file is decided by the run's rule
//     before the policy is asked: carrying the change itself (a v2 finding's
//     `protected_change`), it gets no tick at all — the run applies it, below;
//     carrying only prose about it, it is a backlog tick outside the epic,
//     gating nothing here (the live-run rule's shape, liverun.go). A reviewer
//     naming such a finding blocking does not pull it into the epic either.
//
//   - THE RUN APPLIES THE CHANGE ITSELF, LATE AND VISIBLY. At the close-out's
//     close — after the close-out's attempt and its integrated gate, the last
//     readers of the repository's configuration for this run (routing was
//     chosen at the run's start, from the checkout the run was started in) —
//     each proposed change is committed onto the epic branch as its own
//     labelled commit, the PR's CI runs over it like any other head, and the
//     epic PR lists every one, with its diff, under "Protected changes for
//     the merger". A run that would otherwise merge its own PR leaves one
//     carrying a protected change for a person: the merge is where it is
//     reviewed. The finding is triaged FIXED by that commit, so the
//     close-out's findings gate meets a decision, not a person's queue.
//     Per path the replacements are applied before the appends (epic ex6's
//     tgx: applied in key order, two whole-file proposals composed without
//     2pn's harness cell erased the cell the append had already laid down).
//
// The boundary itself is unchanged: a worker that writes the file in its own
// commits is refused exactly as before.

const (
	// StageProtectedChangeHeld: a finding carries a protected change, which
	// the run applies at the close-out's close rather than absorbing a tick.
	StageProtectedChangeHeld = "protected_change_held"
	// StageProtectedChangeApplied: the run committed a worker-proposed
	// protected change onto the epic branch, for the merger to review.
	StageProtectedChangeApplied = "protected_change_applied"
	// StageProtectedChangeRefused: a proposed protected change the run will
	// not apply, and why.
	StageProtectedChangeRefused = "protected_change_refused"
)

// protectedEditPhrase is the half of a finding body that says its fix is an
// edit a worker may not make.
var protectedEditPhrase = regexp.MustCompile(`(?i)\b(not a worker|no worker|worker (may|can)(not| not)|may not (write|edit)|` +
	`cannot (write|edit)|can't (write|edit)|worker boundary|boundary violation|protected (path|file|prefix)|` +
	`(by|for) the operator|operator must|the operator (has|needs) to)\b`)

// protectedDeliverable is the protected paths a finding's deliverable edits,
// read from its text: the title names one (the title is what the finding
// asks for — yck's was "Append the two named cloud configs to
// .tick/runners.cloud.toml"), or the body names one and says the edit is not
// a worker's to make. Empty for every other finding — a finding that merely
// mentions a configuration file in passing is ordinary work.
func protectedDeliverable(title, body string, cloud bool) []string {
	if paths := subprocess.ProtectedPathsIn(title, cloud); len(paths) > 0 {
		return paths
	}
	if paths := subprocess.ProtectedPathsIn(body, cloud); len(paths) > 0 && protectedEditPhrase.MatchString(body) {
		return paths
	}
	return nil
}

// onCloud says whether this run's workers run on the cloud substrate, whose
// boundary refuses every `.tick/` path (subprocess.CloudBoundaryRefuses).
func (r *Reconciler) onCloud() bool {
	return r.substrate == runconfig.SubstrateCloud
}

// notAWorkersTick says whether a finding must never become a child of the
// running epic: it carries a protected change, or its deliverable is one.
func (r *Reconciler) notAWorkersTick(f subprocess.Finding) bool {
	return f.ProtectedChange != nil || len(protectedDeliverable(f.Title, f.Body, r.onCloud())) > 0
}

// protectedEditReason is the reasoning the backlog decision record carries.
func protectedEditReason(paths []string) string {
	return fmt.Sprintf("the finding's deliverable is an edit of %s, which the worker boundary refuses to every "+
		"worker: absorbed, it would be a tick no worker can do, and a run that waits for a person. It gates none "+
		"of this epic's done here whatever it claims. It is a backlog tick outside the epic; a worker that reports "+
		"it again with the exact change as its finding's protected_change has the run apply it at the close-out, "+
		"for the merger to review", strings.Join(paths, ", "))
}

// decideProtectedEditFinding records the rule's decision for a finding whose
// deliverable is a protected edit it does not carry: a backlog tick outside
// the epic, promoted with nobody triaging.
func (r *Reconciler) decideProtectedEditFinding(ctx context.Context, marker attemptHandle, standing runstate.Finding,
	dispatch Dispatch, paths []string) (findingDecision, error) {
	tickID, err := r.mintTickID()
	if err != nil {
		return findingDecision{}, err
	}
	record := runstate.Absorption{
		Key:        standing.Key,
		TickID:     tickID,
		Gating:     false,
		Basis:      runstate.AbsorptionRule,
		Placement:  runstate.AbsorptionBacklog,
		Reason:     protectedEditReason(paths),
		DecidedAt:  r.now().UTC().Format(time.RFC3339),
		Provenance: r.attemptProvenance(dispatch),
	}
	return r.recordRoutedDecision(ctx, marker, standing, record)
}

// holdProtectedChange is the decision for a finding that carries its own
// protected change: no tick, the draft stays proposed until the close-out's
// close applies it (applyProtectedChanges), and the feed says so.
func (r *Reconciler) holdProtectedChange(marker attemptHandle, standing runstate.Finding,
	change subprocess.ProtectedChange) findingDecision {
	r.record(marker.TickID, StageProtectedChangeHeld,
		"finding %s (%q) carries %s, which no worker may write: the run applies it itself onto %s after the "+
			"close-out's reads, lists it in the epic PR for the merger, and absorbs no tick for it",
		standing.Key, standing.Title, change, r.branch)
	return findingDecision{Protected: change.String()}
}

// applyProtectedChanges commits every protected change a still-proposed
// finding of this run carries onto the epic branch, one labelled commit
// each, and triages each finding FIXED by its commit. It is called at the
// close-out's close, after everything this run reads the configuration for,
// and is idempotent: an applied finding is no longer proposed, and a change
// already on the branch commits nothing.
func (r *Reconciler) applyProtectedChanges(ctx context.Context, marker attemptHandle) error {
	findings, err := r.filedFindings()
	if err != nil {
		return fmt.Errorf("read the run's findings to apply their protected changes: %w", err)
	}
	findings = orderForApplication(findings)
	for _, finding := range findings {
		change, ok := finding.ProtectedChange()
		if !ok || finding.Status != runstate.FindingProposed {
			continue
		}
		if finding.Target != "" && !r.isThisRepository(finding.Target) {
			continue // another repository's file: the routed rule's, never this branch's
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		proposer := r.attemptName(finding.TickID, finding.Attempt)
		if err := change.Validate(); err != nil {
			r.record(marker.TickID, StageProtectedChangeRefused,
				"finding %s (%q, from %s) carries a protected change the run will not apply: %v",
				short(finding.Key), finding.Title, proposer, err)
			if _, _, err := r.store.TriageFinding(finding.Key, runstate.Triage{
				Status: runstate.FindingDiscarded,
				By:     fmt.Sprintf("ticfac run %s refusing its protected change: %v", r.runID, err),
			}); err != nil {
				return err
			}
			continue
		}
		commit, changed, err := r.commitProtectedChange(change, finding, proposer)
		if err != nil {
			return fmt.Errorf("apply the protected change finding %s carries (%s): %w", short(finding.Key), change, err)
		}
		if changed {
			r.record(marker.TickID, StageProtectedChangeApplied,
				"the run applied %s that %s proposed (finding %s) onto %s as %s, after the close-out's reads: the "+
					"epic PR lists it under protected changes for the merger", change, proposer, short(finding.Key),
				r.branch, short(commit))
		} else {
			r.record(marker.TickID, StageProtectedChangeApplied,
				"%s that %s proposed (finding %s) is already on %s at %s; nothing is written twice",
				change, proposer, short(finding.Key), r.branch, short(commit))
		}
		if _, _, err := r.store.TriageFinding(finding.Key, runstate.Triage{
			Status:  runstate.FindingFixed,
			By:      fmt.Sprintf("ticfac run %s applying the protected change the finding carries, for the merger", r.runID),
			FixedAs: commit,
		}); err != nil {
			return err
		}
	}
	return nil
}

// orderForApplication orders the findings for the application loop below:
// per path, every whole-file content replacement is applied before every
// append. An append is relative to the file that finally stands — applied
// before a replacement composed without its lines, each later replacement
// wiped it (epic ex6's tgx: 2pn's harness cell landed first in key order and
// both whole-file proposals that followed erased it from the branch the PR
// merges). Within each group the findings keep their key order, the order
// Findings() answers; and because proposals for different paths do not
// interact, one stable pass serves every path at once.
func orderForApplication(findings []runstate.Finding) []runstate.Finding {
	contents := make([]runstate.Finding, 0, len(findings))
	appends := make([]runstate.Finding, 0, len(findings))
	for _, finding := range findings {
		if change, ok := finding.ProtectedChange(); ok && change.Append != "" {
			appends = append(appends, finding)
			continue
		}
		contents = append(contents, finding)
	}
	return append(contents, appends...)
}

// protectedCommitSubject is the label every protected-change commit carries,
// so a person reading the branch's log finds them without the PR.
const protectedCommitSubject = "protected change for the merger"

// commitProtectedChange commits one change onto the epic branch as origin has
// it, under the store's compare-and-swap: a lost lease rebuilds the commit on
// the moved head rather than forcing over it. It answers the commit that
// carries the change, and whether this call wrote it.
func (r *Reconciler) commitProtectedChange(change subprocess.ProtectedChange, finding runstate.Finding,
	proposer string) (string, bool, error) {
	scratch, remove, err := tempdir.Make("ticfac-protected-")
	if err != nil {
		return "", false, err
	}
	defer remove()
	for try := 0; try < maxTrackerPushes; try++ {
		if err := r.git.fetch(r.branch); err != nil {
			return "", false, err
		}
		// The FETCHED head, not ls-remote's: the commit is built on it, so it
		// must be one this repository holds; the lease refuses it if origin
		// has moved on since.
		head, err := r.git.run("", "rev-parse", refFor("refs/ticfac/fetched/"+r.git.fetch1D()+"/"+r.branch))
		if err != nil {
			return "", false, fmt.Errorf("read %s as fetched from %s: %w", r.branch, r.opts.Remote, err)
		}
		current, mode, err := r.blobAt(head, change.Path)
		if err != nil {
			return "", false, err
		}
		next := change.Applied(current)
		if next == current && mode != "" {
			return head, false, nil
		}
		if mode == "" {
			mode = "100644"
		}
		file := filepath.Join(scratch, "content")
		if err := os.WriteFile(file, []byte(next), 0o644); err != nil {
			return "", false, err
		}
		blob, err := r.git.run("", "hash-object", "-w", file)
		if err != nil {
			return "", false, err
		}
		index := filepath.Join(scratch, "index")
		_ = os.Remove(index)
		env := []string{"GIT_INDEX_FILE=" + index}
		if _, _, err := r.git.tryEnv("", env, "read-tree", head); err != nil {
			return "", false, err
		}
		if _, _, err := r.git.tryEnv("", env, "update-index", "--add", "--cacheinfo",
			mode+","+blob+","+change.Path); err != nil {
			return "", false, err
		}
		tree, _, err := r.git.tryEnv("", env, "write-tree")
		if err != nil {
			return "", false, err
		}
		message := fmt.Sprintf("ticfac run %s: %s: %s\n\n%s proposed this change to a file the worker boundary "+
			"refuses (finding %s, %q). The run applied it after the close-out's reads, so it changed nothing this "+
			"run read; the epic PR lists it for the person who merges.",
			r.runID, protectedCommitSubject, change, proposer, finding.Key, finding.Title)
		commit, err := r.git.run("", "commit-tree", tree, "-p", head, "-m", message)
		if err != nil {
			return "", false, err
		}
		_, stderr, pushErr := r.git.try("", "push", "--force-with-lease="+refFor(r.branch)+":"+head,
			r.opts.Remote, commit+":"+refFor(r.branch))
		if pushErr == nil {
			return commit, true, nil
		}
		if !leaseRefused(stderr) {
			return "", false, pushErr
		}
	}
	return "", false, fmt.Errorf("%s moved under the protected change's writer %d times running; that is an "+
		"operational problem, not a conflict to spin on", r.branch, maxTrackerPushes)
}

// blobAt is a file's exact content and mode at a commit, "" and "" when the
// commit has no such file. Read with exec rather than repoGit, whose output
// is trimmed: an append is idempotent only against the file's exact bytes.
func (r *Reconciler) blobAt(commit, path string) (content, mode string, err error) {
	entry, err := r.git.run("", "ls-tree", commit, "--", path)
	if err != nil {
		return "", "", err
	}
	if entry == "" {
		return "", "", nil
	}
	mode = strings.Fields(entry)[0]
	cmd := exec.Command(gitbin.Path(), "cat-file", "blob", commit+":"+path)
	cmd.Dir = r.git.dir
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("read %s at %s: %w", path, short(commit), err)
	}
	return string(out), mode, nil
}

// carriesProtectedChanges says whether any finding of this run carries a
// protected change it applied or will apply: such a PR is merged by a person,
// whatever the repository opts in to.
func (r *Reconciler) carriesProtectedChanges() bool {
	if r.store == nil {
		return false
	}
	findings, err := r.store.Findings()
	if err != nil {
		return true // unreadable: keep the merge for a person, the safe side
	}
	for _, finding := range findings {
		if _, ok := finding.ProtectedChange(); ok && finding.Status != runstate.FindingDiscarded {
			return true
		}
	}
	return false
}

// landsItself is whether the run merges its own epic PR: the repository opts
// in, and the PR carries no protected change, which is the merger's to review.
func (r *Reconciler) landsItself() bool {
	return r.closeoutRule.Lands() && !r.carriesProtectedChanges()
}

// protectedChangesSection is the PR body's "Protected changes for the
// merger": every protected change a finding of this run carries, applied
// (with the commit's diff) or still to be applied at the close (with the
// proposed text). Empty when there are none. condensed omits the diffs.
func (r *Reconciler) protectedChangesSection(findings []runstate.Finding, condensed bool) string {
	var b strings.Builder
	for _, finding := range findings {
		change, ok := finding.ProtectedChange()
		if !ok || finding.Status == runstate.FindingDiscarded {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("\n## Protected changes for the merger\n\n")
			fmt.Fprintf(&b, "**Review these before you merge.** Each is a change a worker proposed to a file no worker "+
				"may write (the worker boundary), applied by the run itself as its own labelled commit (\"%s\") on "+
				"%s after the close-out's reads, so none of them changed what this run read. The run does not merge a "+
				"PR that carries one.\n", protectedCommitSubject, r.branch)
		}
		proposer := r.attemptName(finding.TickID, finding.Attempt)
		state := "proposed: the run applies it at the close-out's close"
		if finding.Status == runstate.FindingFixed {
			state = "applied as " + short(finding.FixedAs)
		}
		fmt.Fprintf(&b, "\n### `%s` — %s\n\n%s, proposed by %s (finding %s, %q).\n",
			change.Path, state, change, proposer, short(finding.Key), finding.Title)
		if condensed {
			b.WriteString("The text is omitted to keep this body under GitHub's limit")
			if finding.FixedAs != "" {
				fmt.Fprintf(&b, ": `git show %s`", short(finding.FixedAs))
			}
			b.WriteString(".\n")
			continue
		}
		if finding.Status == runstate.FindingFixed {
			if diff, err := r.git.run("", "show", "--format=", finding.FixedAs, "--", change.Path); err == nil && diff != "" {
				fmt.Fprintf(&b, "\n````diff\n%s\n````\n", diff)
				continue
			}
		}
		what := "The new file"
		if change.Append != "" {
			what = "Appended to the file"
		}
		fmt.Fprintf(&b, "\n%s:\n\n````\n%s\n````\n", what, strings.TrimRight(change.Text(), "\n"))
	}
	return b.String()
}

// protectedChangesIn is every protected change a collection's findings carry.
func protectedChangesIn(collected *subprocess.Collection) []subprocess.ProtectedChange {
	if collected == nil {
		return nil
	}
	var out []subprocess.ProtectedChange
	for _, finding := range collected.Findings {
		if finding.ProtectedChange != nil {
			out = append(out, *finding.ProtectedChange)
		}
	}
	return out
}

func protectedChangeList(changes []subprocess.ProtectedChange) string {
	names := make([]string, 0, len(changes))
	for _, change := range changes {
		names = append(names, change.String())
	}
	return strings.Join(names, "; ")
}

// acceptProtectedDelivery is the collect's half for a tick whose ONLY
// deliverable is a protected change (epic ex6's 2pn: one [testing.commands]
// cell in .tick/runners.toml, which the cloud's container refuses). The
// worker commits nothing and reports the change as a finding's
// protected_change; that is a delivery — ready to merge with no head — not an
// empty branch, on every substrate. A BLOCKED answer that committed nothing
// and carries the change is the same delivery: the wall it names is the
// boundary, and the run is what goes round it — unless the question is in the
// standing orders' always-ask class.
func (r *Reconciler) acceptProtectedDelivery(marker attemptHandle, collected *subprocess.Collection) *subprocess.Collection {
	if collected == nil || collected.Result == nil || marker.Role != "implement-tick" ||
		collected.Verdict != subprocess.VerdictNoCommits || len(collected.Report.TrackerEdits) > 0 {
		return collected
	}
	changes := protectedChangesIn(collected)
	if len(changes) == 0 {
		return collected
	}
	switch collected.Report.Status {
	case subprocess.StatusDone, subprocess.StatusDoneWithConcerns:
	case subprocess.StatusBlocked:
		if class := r.standingOrders().alwaysAskClass(collected.Report.Detail); class != "" {
			r.record(marker.TickID, StageProtectedChangeRefused, "%s answered %s carrying %s, but the question is in "+
				"the always-ask class %q of the standing orders: it holds for a person",
				r.attemptName(marker.TickID, marker.Attempt), collected.Report.Status, protectedChangeList(changes), class)
			return collected
		}
	default:
		return collected
	}
	r.record(marker.TickID, StageCollected, "%s committed no work and its findings carry %d protected change(s) (%s): "+
		"that is its delivery, not an empty branch — the run applies them itself after the close-out's reads, for the "+
		"merger to review, and gates and closes the tick now", r.attemptName(marker.TickID, marker.Attempt),
		len(changes), protectedChangeList(changes))
	return deliveredWithoutHead(collected)
}

// protectedDelivery says the run took this attempt's protected changes as its
// whole delivery (acceptProtectedDelivery): nothing to merge now.
func protectedDelivery(collected *subprocess.Collection) bool {
	return collected != nil && collected.Result != nil &&
		collected.Verdict == subprocess.VerdictReadyToMerge &&
		collected.Result.Source.HeadSHA == nil &&
		len(collected.Report.TrackerEdits) == 0 &&
		len(protectedChangesIn(collected)) > 0
}

// integrateProtectedDelivery is the integrate's half: nothing merges — the
// change is applied at the close-out — and the gate runs over the epic head.
func (r *Reconciler) integrateProtectedDelivery(marker attemptHandle, collected *subprocess.Collection) (bool, merge, error) {
	if !protectedDelivery(collected) {
		return false, merge{}, nil
	}
	head, err := r.git.remoteHead(r.branch)
	if err != nil {
		return false, merge{}, err
	}
	attemptHead, _ := r.git.remoteHead(branchOf(marker.WriteRef))
	r.setTick(marker.TickID, "integrated")
	r.record(marker.TickID, StageIntegrated, "%s's delivery is %s, which the run applies onto %s after the "+
		"close-out's reads; nothing merges now, and the gate runs over %s at %s",
		r.attemptName(marker.TickID, marker.Attempt), protectedChangeList(protectedChangesIn(collected)), r.branch,
		r.branch, short(head))
	return true, merge{AttemptHead: attemptHead, EpicHead: head, GateSHA: head, Merged: false}, nil
}
