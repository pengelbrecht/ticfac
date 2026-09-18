package reconcile

import (
	"context"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/forge"
)

// The close-out's CI read, and why it cannot simply ask about the PR's head.
//
// The run's durable state lives in .ticfac/ ON the integration branch, and the
// close-out gates on CI of the epic PR whose head IS that branch. So the act of
// gating rewrites the thing being gated: the run writes a checkpoint, the push
// moves the PR's head, and the close-out then asks the forge about a commit
// created seconds ago that no check has run on yet. It sees nothing, and either
// holds until its bound or refuses as "no CI has run at all".
//
// That is not a race that can be won by waiting. Every resume writes another
// checkpoint and moves the head again, so the next attempt asks about an even
// newer commit. Epic 9pd deadlocked exactly this way — heads cde6d962 ->
// 73fdbb59 -> ae0089e7, each refused, the last four commits on the branch all
// "update checkpoint.json", while CI sat green the whole time on whichever head
// had stood still long enough. An operator had to merge it by hand.
//
// The fix rests on what a run-state commit IS: a write under .ticfac/ and
// nothing else. It cannot change what the build compiles or what the tests run,
// so CI's verdict on an earlier commit is still a true statement about this
// tree's CODE — which is what the gate is actually about. So:
//
//   - the PR is RE-READ, because the head this run remembers is stale the
//     moment it checkpoints,
//   - CI is asked about the head first, the ordinary case,
//   - and when the head has no CI, the newest ancestor that DOES is found and
//     used, but only after proving that everything between it and the head
//     lives under .ticfac/.
//
// The proof is the point. If anything outside .ticfac/ changed since the last
// commit CI ran on, the verdict does not describe this tree and the answer is
// pending — a real wait for a real check, not a borrowed pass.
const (
	// ciWalkLimit bounds how far back a CI verdict is looked for. A run-state
	// commit per phase is a handful; a hundred would mean something else is
	// wrong and the honest answer is that CI has not run.
	ciWalkLimit = 25

	// runStatePrefix is the only path a commit may touch and still be one the
	// gate can see past. NOT .tick/ as well: tracker files include
	// runners.toml, which the gate's own drift guards read, so a .tick/ change
	// can legitimately change what CI says.
	runStatePrefix = ".ticfac/"
)

// ciForTree answers what CI says about the code this PR would merge.
//
// It returns the report, the sha the report is about, and whether that sha is
// the PR's own head — callers say so in the feed, because "green on the head"
// and "green on the last commit that changed code" are different sentences and
// an operator is owed the true one.
func (r *Reconciler) ciForTree(ctx context.Context, pr *forge.PullRequest) (forge.CIReport, string, bool, error) {
	// Re-read the PR. The head this run holds was read before its own
	// checkpoint push, so it is stale by construction — which is the first
	// half of the defect this function exists for.
	if fresh, err := r.opts.PullRequests.Find(ctx, pr.HeadRef, pr.BaseRef); err == nil && fresh != nil && fresh.HeadSHA != "" {
		*pr = *fresh
	}

	head := pr.HeadSHA
	report, err := r.opts.PullRequests.CI(ctx, *pr)
	if err != nil {
		return forge.CIReport{}, head, true, err
	}
	if report.State != forge.CINone {
		return report, head, true, nil
	}

	// Nothing on the head. Look back for a commit CI did run on, and take its
	// verdict only if this tree's code is that commit's code.
	older, olderReport, ok, err := r.ciFromAnAncestor(ctx, pr)
	if err != nil || !ok {
		// No verdict anywhere in reach: the honest answer is the one the
		// forge gave about the head.
		return report, head, true, err
	}
	return olderReport, older, false, nil
}

// ciFromAnAncestor walks back from the PR's head for a commit that has a CI
// verdict, and proves the walk crossed nothing but run state.
func (r *Reconciler) ciFromAnAncestor(ctx context.Context, pr *forge.PullRequest) (string, forge.CIReport, bool, error) {
	// The branch has to be in this checkout before its history can be read.
	// A fetch failure is not fatal here: the walk simply finds nothing and
	// the caller falls back to the head's own answer.
	if err := r.git.fetch(pr.HeadRef); err != nil {
		return "", forge.CIReport{}, false, nil
	}
	out, err := r.git.run("", "rev-list", "--max-count="+fmt.Sprint(ciWalkLimit), pr.HeadSHA)
	if err != nil {
		return "", forge.CIReport{}, false, nil
	}
	for i, line := range strings.Split(out, "\n") {
		sha := strings.TrimSpace(line)
		if sha == "" || i == 0 {
			continue // i == 0 is the head, already asked
		}
		candidate := *pr
		candidate.HeadSHA = sha
		report, err := r.opts.PullRequests.CI(ctx, candidate)
		if err != nil {
			return "", forge.CIReport{}, false, err
		}
		if report.State == forge.CINone {
			continue
		}
		// A verdict. It describes this tree only if nothing outside run state
		// changed since — and a diff this cannot read counts as "changed",
		// because a gate that cannot prove the tree is unchanged does not get
		// to assume it.
		if confined := r.onlyRunState(sha, pr.HeadSHA); !confined {
			return "", forge.CIReport{}, false, nil
		}
		return sha, report, true, nil
	}
	return "", forge.CIReport{}, false, nil
}

// onlyRunState reports whether every path that changed between two commits
// lives under .ticfac/.
//
// A diff that cannot be read answers false, deliberately: this is the proof
// that lets an older verdict stand for this tree, and an unreadable proof is
// not a proof.
func (r *Reconciler) onlyRunState(from, to string) bool {
	out, err := r.git.run("", "diff", "--name-only", from, to)
	if err != nil {
		return false
	}
	for _, path := range strings.Split(out, "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, runStatePrefix) {
			return false
		}
	}
	return true
}

// ciSubject is how the feed and a refusal name what a CI verdict is about.
func ciSubject(sha string, isHead bool, pr *forge.PullRequest) string {
	if isHead {
		return fmt.Sprintf("the epic PR #%d's head %s", pr.Number, short(sha))
	}
	return fmt.Sprintf("%s, the newest commit of the epic PR #%d that CI ran on (every commit since changes only %s, so the verdict is about this tree's code)",
		short(sha), pr.Number, runStatePrefix)
}
