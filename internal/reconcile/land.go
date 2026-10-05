package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The READY epic PR, and — where the repository opts in — its merge
// (operator decisions, 2026-09-28).
//
// WHAT WAS WRONG. A run ended at the close-out: the epic PR opened, CI green
// on it at that moment, and everything after was a person's. Epic 2jn showed
// what that costs. After its run completed, main moved (other PRs merged), the
// epic PR's CI went red on a semantic conflict, and a person folded main into
// epic/2jn by hand with tk's merge drivers, resolved the conflicts, re-ran the
// gate, pushed and waited for CI — then merged epic/2jn into main by hand,
// meeting more conflicts from a PR that landed meanwhile. Every one of those
// steps is one the run already knows how to take: the base fold and its
// resolve-conflict job (refresh.go, refresh_resolve.go), the integrated gate
// and its repair job (gate.go, gate_repair.go), the CI wait on the PR
// (closeout.go). A step whose only actor is a person is a design defect.
//
// THE TOUCH POINT. The common way an epic reaches ticfac is handed over from
// interactive work with a frontier agent, and the next touch point is the PR:
// the person reviews it, with their agent, and merges it. So the run's job
// ends at a PR that is READY — and it keeps it ready without a person:
//
//  1. FOLD the branch the PR merges into (its base) into the epic branch —
//     the fold a run makes at start, so a conflict is the resolve-conflict
//     job's before it is anybody's.
//  2. GATE the folded head with the integrated gate when the fold made a new
//     commit; a failing check is the repair job's, as for any merge.
//  3. WAIT FOR CI green on the PR's head (ciForTree, which sees past the run's
//     own .ticfac/ commits). A red CI is re-run once, as at the close-out; a
//     red that stays red is a GATE FAILURE the repair job is dispatched over
//     — its evidence the failing jobs — never a hold.
//  4. If the base moved meanwhile, go round again (bounded, maxLandPasses);
//     otherwise the PR is ready, and its body — what the epic did, the done
//     items and their evidence, the review's verdict, the findings and where
//     each went, what to look at first, and this readiness — is rewritten
//     for the reviewer.
//
// This runs at the end of every run whose repository declares the PR + CI
// gate, and AGAIN whenever a completed run is re-entered (`ticfac run <epic>`
// on an epic whose run completed): a base that moved since is folded, gated
// and CI'd again, so a stale or conflicted PR is one command's work, not a
// person's afternoon. A PR that has been merged, or closed, is left alone.
//
// THE OPT-IN: THE RUN MERGES. A repository whose Rules say the run merges its
// own PR (CloseoutRule.RunMerges — this repository's, for development
// velocity) has the ready PR merged by the run:
//
//  5. MERGE the epic into the base: a merge commit (never a squash: the
//     tracker's records need tk's merge drivers, and a squash would sever the
//     epic's history from the base), made locally with the drivers and pushed
//     with a lease on the base head the fold carried. The epic contains that
//     head, so the merge's tree IS the tree CI just passed, and GitHub marks
//     the PR merged because the base now contains its head. A lost lease is
//     the base moving: the next pass folds again.
//  6. VERIFY CI on the base branch's merge commit.
//
// WHY A LOCAL MERGE AND NOT THE FORGE'S MERGE BUTTON. GitHub's merge API cannot
// run tk's merge drivers, and it cannot be told which base head the merge is
// allowed on: between this run's check and the call the base can move, and
// GitHub would then three-way merge `.tick/` records as plain text — the merge
// the drivers exist to replace. A local merge pushed with --force-with-lease on
// the folded base head is a compare-and-swap on exactly the tree CI passed.
//
// A final review that judged the epic NOT READY is never merged by the run:
// the verdict is carried on the PR, and accepting work the run's own review
// rejected is a person's judgement (RefusedLandReviewNotReady).

// maxLandPasses bounds the readying loop. A pass that does not finish is the
// base moving under the run, or a repair the pass just merged; five in a row
// is a base that moves faster than a gate runs — an operational problem to
// name, not a race to spin on.
const maxLandPasses = 5

// landingCloseout is the close-out tick the readying acts for: the repair
// job's allowance, the gate's evidence and the feed lines are all the
// close-out's, because a ready PR is the close-out's deliverable, kept.
type landingCloseout struct {
	entry  planEntry
	marker attemptHandle
}

// readiness is what one readying established, for the PR body and the
// run's terminal reason.
type readiness struct {
	// PR is the epic PR's number, zero when none is open.
	PR int
	// Base is the branch the PR merges into, and BaseHead the commit of it
	// the epic branch carries.
	Base, BaseHead string
	// Folded is the fold this readying made, "" when the branch already
	// carried the base.
	Folded string
	// CI is the commit CI is green on.
	CI string
	// Landed is the base's commit that carries the epic, when the run merged
	// it (the opt-in) or somebody already had.
	Landed string
	// Note is the one-line account of how it ended when it did not end
	// ready: the PR merged, closed, or absent.
	Note string
}

// line is the readiness as the terminal reason's sentence.
func (rd readiness) line() string {
	switch {
	case rd.Landed != "":
		return fmt.Sprintf("; the epic is merged into %s as %s", rd.Base, short(rd.Landed))
	case rd.Note != "":
		return "; " + rd.Note
	case rd.PR != 0:
		return fmt.Sprintf("; the epic PR #%d is ready: %s at %s folded in, CI green on %s", rd.PR, rd.Base,
			short(rd.BaseHead), short(rd.CI))
	}
	return ""
}

// closeoutForLanding is the latest close-out attempt this run dispatched, or
// nil when the epic ran no close-out — and then there is no epic PR.
func (r *Reconciler) closeoutForLanding() (*landingCloseout, error) {
	if _, err := r.store.Fetch(); err != nil {
		return nil, err
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return nil, err
	}
	var found *attemptHandle
	for _, attempt := range attempts {
		marker := handleFromMap(attempt.JobHandle)
		if marker.TickID == "" {
			marker.TickID = attempt.TickID
		}
		if marker.Attempt == 0 {
			marker.Attempt = attempt.Attempt
		}
		if marker.Role != "closeout-epic" {
			continue
		}
		if found == nil || marker.Attempt > found.Attempt {
			latest := marker
			found = &latest
		}
	}
	if found == nil {
		return nil, nil
	}
	found.Repo = r.opts.Repo
	found.StateRoot = r.execStateDir(found.TickID, found.Attempt)
	return &landingCloseout{
		entry:  planEntry{TickID: found.TickID, Role: "closeout-epic", Title: r.titles[found.TickID]},
		marker: *found,
	}, nil
}

// finishReadying is how a run whose every tick is closed ends: the readying,
// then the terminal checkpoint. `done` is the sentence the completed state
// opens with. A refusal from the readying is the run's stop, recorded the way
// runPlan records one — a hold for a person where the reason is one — and a
// re-run resumes at the readying (every tick is closed, so it is all that is
// left).
func (r *Reconciler) finishReadying(ctx context.Context, done string) (*Result, error) {
	// A run-start fold the run deferred is folded now, before anything is
	// readied from the branch (refresh_defer.go).
	if err := r.retryDeferredFold(ctx); err != nil {
		var refusal *Refusal
		if !asRefusal(err, &refusal) {
			return nil, err
		}
		r.failure = refusal
		r.recordRefusal(refusal.TickID, refusal)
		reason := fmt.Sprintf("%s, and the epic branch is not ready to be readied: %s", done, refusal.Error()) +
			autoResumeNote(r.priorResumes)
		if _, err := r.checkpoint(runstate.StateFailed, reason); err != nil {
			return nil, err
		}
		r.record("", StageRunFinished, "%s: %s", runstate.StateFailed, reason)
		return r.result(runstate.StateFailed, reason), nil
	}
	rd, err := r.readyEpic(ctx)
	if err != nil {
		var refusal *Refusal
		if !asRefusal(err, &refusal) {
			return nil, err
		}
		r.failure = refusal
		if holdsForAPerson(refusal.Reason) {
			r.record(refusal.TickID, StageRunHeld, "%s: %s", refusal.Reason, refusal.Message)
		} else {
			r.recordRefusal(refusal.TickID, refusal)
		}
		reason := fmt.Sprintf("%s, and the epic PR is not ready: %s. Running the epic again resumes at keeping "+
			"the PR ready", done, refusal.Error()) + autoResumeNote(r.priorResumes)
		if _, err := r.checkpoint(runstate.StateFailed, reason); err != nil {
			return nil, err
		}
		r.record("", StageRunFinished, "%s: %s", runstate.StateFailed, reason)
		return r.result(runstate.StateFailed, reason), nil
	}
	// The completed run's end: every job's leftovers, the ones that name no
	// tick included (a base fold's). This is the sweep runPlan's caller meant
	// to make on completion — it returns here first, so without this line the
	// "everything" sweep only ever ran for a run that was already terminal.
	r.sweepClosed(ctx, true)
	reason := done + rd.line() + autoResumeNote(r.priorResumes)
	if _, err := r.checkpoint(runstate.StateCompleted, reason); err != nil {
		return nil, err
	}
	r.record("", StageRunFinished, "%s: %s", runstate.StateCompleted, reason)
	return r.result(runstate.StateCompleted, reason), nil
}

// readyEpic brings the epic PR to READY and keeps it there — and, where the
// repository opts in, merges it. It is a no-op for a repository that declares
// no PR + CI gate, and for an epic that ran no close-out (there is no PR).
func (r *Reconciler) readyEpic(ctx context.Context) (readiness, error) {
	if !r.closeoutRule.Declared || r.opts.PullRequests == nil || r.store == nil {
		return readiness{}, nil
	}
	co, err := r.closeoutForLanding()
	if err != nil {
		return readiness{}, err
	}
	if co == nil {
		r.record("", StageLandSkipped, "the epic ran no close-out, so no epic PR exists to keep ready")
		return readiness{}, nil
	}
	lands := r.closeoutRule.Lands()
	if lands {
		if refusal, err := r.landingReviewHold(co.marker.TickID); err != nil {
			return readiness{}, err
		} else if refusal != nil {
			return readiness{}, refusal
		}
	}
	for pass := 1; pass <= maxLandPasses; pass++ {
		if err := ctx.Err(); err != nil {
			return readiness{}, err
		}
		rd, done, err := r.readyPass(ctx, co, pass, lands)
		if err != nil {
			return readiness{}, err
		}
		if done {
			return rd, nil
		}
	}
	return readiness{}, r.refuse(RefusedLandBaseMoving, co.marker.TickID,
		"the epic PR of %s is not ready: %d passes each found the base branch or the epic branch moved between the "+
			"fold, the gate and CI. That is a base moving faster than a gate runs — an operational problem, not a race "+
			"to spin on. Nothing half-landed: every fold and merge is pushed under a lease. Run the epic again and "+
			"the readying resumes from the branches as they stand", r.opts.EpicID, maxLandPasses)
}

// readyPass is one pass of the readying loop. `done` says the pass finished —
// the PR ready (or merged, with the opt-in), or nothing left to keep ready —
// and false says something moved, or a repair just merged, and the next pass
// has to look again.
func (r *Reconciler) readyPass(ctx context.Context, co *landingCloseout, pass int, lands bool) (readiness, bool, error) {
	tick := co.marker.TickID
	base := r.prBase()
	pr, err := r.opts.PullRequests.Find(ctx, r.branch, base)
	if err != nil {
		return readiness{}, false, r.refuse(RefusedLandPR, tick,
			"the epic PR of %s cannot be kept ready: the code-hosting surface could not say whether it exists for "+
				"%s (→ %s): %v", r.opts.EpicID, r.branch, base, err)
	}
	if pr != nil && pr.BaseRef != "" {
		base = pr.BaseRef
	}
	rd := readiness{Base: base}
	baseHead, err := r.git.remoteHead(base)
	if err != nil {
		return readiness{}, false, err
	}
	if baseHead == "" {
		return readiness{}, false, r.refuse(RefusedLandPR, tick,
			"the epic PR of %s cannot be kept ready: %s has no branch %s for it to merge into",
			r.opts.EpicID, r.opts.Remote, base)
	}
	if err := r.git.fetch(base); err != nil {
		return readiness{}, false, fmt.Errorf("fetch %s from %s to ready %s: %w", base, r.opts.Remote, r.branch, err)
	}
	if err := r.git.fetch(r.branch); err != nil {
		return readiness{}, false, err
	}
	epicHead, err := r.git.remoteHead(r.branch)
	if err != nil {
		return readiness{}, false, err
	}

	// Already merged — by a person, or by an earlier incarnation killed between
	// its push and its checkpoint. Nothing is folded into, or merged, twice.
	if at, ok := r.landedAt(baseHead, epicHead); ok {
		rd.Landed = at
		r.record(tick, StageLanded, "%s already carries the epic branch %s (at %s): nothing is kept ready or "+
			"merged twice", base, r.branch, short(at))
		if lands {
			return rd, true, r.verifyBaseCI(ctx, tick, pr, base, at)
		}
		return rd, true, nil
	}
	if pr == nil {
		if lands {
			return readiness{}, false, r.refuse(RefusedLandPR, tick,
				"the epic %s cannot be merged: no open PR exists for %s (→ %s) and %s does not carry the epic's "+
					"work. The close-out opened one; it was closed without merging since. Re-open it and run the epic "+
					"again", r.opts.EpicID, r.branch, base, base)
		}
		rd.Note = fmt.Sprintf("no open epic PR exists for %s (→ %s): it was closed without merging, so there is "+
			"nothing to keep ready", r.branch, base)
		r.record(tick, StageLandSkipped, "%s", rd.Note)
		return rd, true, nil
	}
	rd.PR = pr.Number
	r.record(tick, StageLanding, "readying pass %d of at most %d: the epic PR #%d merges %s (at %s) into %s (at %s)",
		pass, maxLandPasses, pr.Number, r.branch, short(epicHead), base, short(baseHead))

	// 1. The fold, and 2. the gate over what it made.
	if !r.git.contains(baseHead, epicHead) {
		if err := r.refreshFrom(ctx, base); err != nil {
			return readiness{}, false, err
		}
		if _, err := r.store.Fetch(); err != nil {
			return readiness{}, false, err
		}
		if folded := r.base; folded != "" && folded != epicHead {
			rd.Folded = folded
			repaired, err := r.gateLanding(ctx, co, base, folded)
			if err != nil || repaired {
				return readiness{}, false, err
			}
		}
	}

	// 3. CI on the PR's head.
	ciSHA, repaired, err := r.landingCI(ctx, co, pr)
	if err != nil || repaired {
		return readiness{}, false, err
	}
	rd.CI = ciSHA

	// 4. Did anything move while the pass ran? The base, or the epic branch
	// out from under the fold.
	baseNow, err := r.git.remoteHead(base)
	if err != nil {
		return readiness{}, false, err
	}
	if err := r.git.fetch(r.branch); err != nil {
		return readiness{}, false, err
	}
	epicNow, err := r.git.remoteHead(r.branch)
	if err != nil {
		return readiness{}, false, err
	}
	if baseNow != baseHead || !r.git.contains(baseHead, epicNow) {
		r.record(tick, StageLandBaseMoved, "%s moved from %s to %s while the pass folded, gated and waited for CI: "+
			"the next pass folds it in", base, short(baseHead), short(baseNow))
		return readiness{}, false, nil
	}
	rd.BaseHead = baseHead

	if !lands {
		if err := r.carryReadiness(ctx, tick, pr, rd); err != nil {
			return readiness{}, false, err
		}
		r.record(tick, StagePRReady, "the epic PR #%d is ready for its reviewer: %s at %s is folded in, the "+
			"integrated gate passed, CI is green on %s — the merge is a person's", pr.Number, base, short(baseHead),
			short(ciSHA))
		return rd, true, nil
	}

	// 5. The merge (the opt-in). The merge must be of the tree CI passed: the
	// branch may since carry the run's own state and tracker records - the
	// writes CI starts no run for (ciIgnoredPrefixes) - and nothing else.
	if ciSHA != epicNow && !r.onlyCIIgnored(ciSHA, epicNow) {
		r.record(tick, StageLandBaseMoved, "%s moved from %s, the commit CI passed, to %s with more than run state and tracker records: "+
			"the next pass waits for CI on what the branch holds now", r.branch, short(ciSHA), short(epicNow))
		return readiness{}, false, nil
	}
	if err := r.carryReadiness(ctx, tick, pr, rd); err != nil {
		return readiness{}, false, err
	}
	// The body write above may have moved nothing on the branch — it is the
	// forge's — but the merge re-reads the head anyway: what is merged is
	// what the branch holds at the push, never a remembered sha.
	epicNow, err = r.git.remoteHead(r.branch)
	if err != nil {
		return readiness{}, false, err
	}
	if ciSHA != epicNow && !r.onlyCIIgnored(ciSHA, epicNow) {
		return readiness{}, false, nil
	}
	merged, moved, err := r.mergeEpicInto(tick, base, baseHead, epicNow, pr)
	if err != nil {
		return readiness{}, false, err
	}
	if moved {
		r.record(tick, StageLandBaseMoved, "%s moved under the merge's push (its lease on %s was lost): the next "+
			"pass folds it in", base, short(baseHead))
		return readiness{}, false, nil
	}
	rd.Landed = merged
	r.record(tick, StageLanded, "the epic %s is merged by the run, as the repository opts in (%s): %s (at %s) is "+
		"merged into %s as %s, and the epic PR #%d with it", r.opts.EpicID, r.closeoutRule.MergeStated, r.branch,
		short(epicNow), base, short(merged), pr.Number)

	// 6. CI on the base.
	return rd, true, r.verifyBaseCI(ctx, tick, pr, base, merged)
}

// carryReadiness rewrites the PR body for its reviewer, with this readying's
// account at the end: the body is the record's view, recomposed, and the
// readiness is the one fact the record does not hold — what the run checked
// the PR against just now.
func (r *Reconciler) carryReadiness(ctx context.Context, tick string, pr *forge.PullRequest, rd readiness) error {
	body, findings, err := r.composePRBody(rd.section(r))
	if err != nil {
		return r.refuse(RefusedCloseoutPRBody, tick,
			"the epic PR #%d cannot carry the run's record for its reviewer: the record could not be read to "+
				"compose it: %v", pr.Number, err)
	}
	return r.carryOntoThePR(ctx, tick, *pr, body, findings)
}

// section is the readiness as the PR body states it.
func (rd readiness) section(r *Reconciler) string {
	var b strings.Builder
	b.WriteString("\n## Readiness\n\n")
	fmt.Fprintf(&b, "Checked by ticfac run %s at %s:\n\n", r.runID, r.now().UTC().Format(time.RFC3339))
	if rd.Folded != "" {
		fmt.Fprintf(&b, "- `%s` at %s is folded into `%s` (as %s), with tk's merge drivers; the integrated gate "+
			"(%s) passed on the fold.\n", rd.Base, short(rd.BaseHead), r.branch, short(rd.Folded), r.gate)
	} else {
		fmt.Fprintf(&b, "- `%s` already carries `%s` at %s: nothing needed folding in.\n", r.branch, rd.Base,
			short(rd.BaseHead))
	}
	fmt.Fprintf(&b, "- CI (%s) is green on %s.\n", r.closeoutRule.CIWorkflow, short(rd.CI))
	if r.closeoutRule.Lands() {
		fmt.Fprintf(&b, "- The repository opts in to the run merging its own PR (%s), so the run merges this PR "+
			"now, with tk's merge drivers, as a merge commit.\n", r.closeoutRule.MergeStated)
	} else {
		fmt.Fprintf(&b, "- The merge is yours. If `%s` moves before you merge, `ticfac run %s` folds it in, gates "+
			"and waits for CI again — no hand merging.\n", rd.Base, r.opts.EpicID)
	}
	return b.String()
}

// landedAt reports whether the base already carries the epic's work, and the
// commit on the base that first carried it. "Carries" is ciForTree's proof
// again: everything the epic branch holds beyond its merge base with the base
// is run state under .ticfac/ — the checkpoints a run writes after its own
// merge, which the base never needs.
func (r *Reconciler) landedAt(baseHead, epicHead string) (string, bool) {
	mergeBase, err := r.git.run("", "merge-base", baseHead, epicHead)
	if err != nil || mergeBase == "" || mergeBase == baseHead {
		// The base is an ancestor of the epic: nothing of the epic is on it.
		return "", false
	}
	if mergeBase != epicHead && !r.onlyRunState(mergeBase, epicHead) {
		return "", false
	}
	after, _ := r.git.run("", "rev-list", "--ancestry-path", "--reverse", mergeBase+".."+baseHead)
	if first := strings.TrimSpace(strings.SplitN(after, "\n", 2)[0]); first != "" {
		return first, true
	}
	return mergeBase, true
}

// landingReviewHold refuses to merge an epic whose final review judged it NOT
// READY: that verdict is carried on the PR (reviewverdict.go), and accepting
// work the run's own review rejected is not a step the run takes for anybody.
//
// It is reached only once the run has acted on the NOT READY itself
// (review_rounds.go): a review whose blocking findings could be absorbed has
// had them absorbed, fixed and the epic reviewed again, so the hold is the
// review that is STILL NOT READY after maxReviewRounds rounds — or one that
// named nothing the run could fix — and it names the remaining reasons.
func (r *Reconciler) landingReviewHold(tick string) (*Refusal, error) {
	rounds, err := r.readReviewRounds()
	if err != nil {
		return nil, err
	}
	if rounds.final == nil || reviewVerdictOf(rounds.final.Response) != subprocess.ReviewVerdictNotReady {
		return nil, nil
	}
	final := *rounds.final
	why := fmt.Sprintf("after %d review round(s), the bound being %d", rounds.rounds, maxReviewRounds)
	if rounds.rounds < maxReviewRounds {
		why = "and it named no blocking finding the run could absorb and fix"
	}
	return r.refuse(RefusedLandReviewNotReady, tick,
		"the run does not merge the epic %s: its final review (decision %d) still judges it NOT READY %s. What "+
			"the review says would make it ready: %s. The verdict is on the epic PR, and accepting work the run's own "+
			"review rejected is a person's judgement: fix what it names and run the epic again, merge the PR by hand "+
			"to accept it (a re-run then finds it merged), or close it",
		r.opts.EpicID, final.Decision, why, notReadyReasons(final)), nil
}

// landingAttemptHead is what the readying's gate fingerprint names as the
// attempt head: the close-out attempt's branch where origin still has it,
// else the head being gated — the close retired the attempt's branch, and
// the freshness check reads an absent branch as the recorded head the
// integration branch carries (currentTarget).
func (r *Reconciler) landingAttemptHead(co *landingCloseout, fallback string) string {
	if head, err := r.git.remoteHead(branchOf(co.marker.WriteRef)); err == nil && head != "" {
		return head
	}
	return fallback
}

// gateLanding runs the integrated gate over the fold the readying made, under
// the close-out's identity. A failing check is the repair job's, exactly as
// for any merge (gate_repair.go); `repaired` says the repair merged and its
// own gate — and the close-out's CI gate behind it — passed, so the pass
// starts again from the branches as they now stand.
func (r *Reconciler) gateLanding(ctx context.Context, co *landingCloseout, base, folded string) (bool, error) {
	tick := co.marker.TickID
	merged := merge{AttemptHead: r.landingAttemptHead(co, folded), EpicHead: folded, GateSHA: folded, Merged: true}
	g, err := r.beginGate(co.marker, merged)
	if err != nil {
		return false, err
	}
	for {
		done, err := r.stepGate(ctx, co.marker, merged, g)
		if err != nil {
			return false, err
		}
		if done {
			break
		}
		time.Sleep(gatePollInterval)
	}
	if !g.passed {
		r.record(tick, StageGateFailed, "the integrated gate did not pass on %s, the fold of %s into %s: %s",
			short(folded), base, r.branch, strings.Join(g.failures, ", "))
		if len(g.failed) > 0 {
			return true, r.repairFailedGate(ctx, co.entry, co.marker, merged, g)
		}
		return false, r.refuse(RefusedGate, tick,
			"the integrated gate on %s, the fold of %s into %s, did not pass: %s. The epic PR is not ready; fix the "+
				"check or the tree, push it to %s, and run the epic again",
			short(folded), base, r.branch, strings.Join(g.failures, ", "), r.branch)
	}
	r.record(tick, StageGatePassed, "the integrated gate passed on %s, the fold of %s into %s (%s)",
		short(folded), base, r.branch, r.gate)
	return false, nil
}

// landingCI waits for CI on the epic PR's head, and answers the commit its
// green verdict is about. A red CI that its one re-run does not cure is a
// gate failure: the repair job is dispatched over it (`repaired` then says
// the repair merged and passed, and the pass starts again). Pending — and CI
// that has not appeared yet — is a bounded wait, as at the close-out.
func (r *Reconciler) landingCI(ctx context.Context, co *landingCloseout, pr *forge.PullRequest) (string, bool, error) {
	tick := co.marker.TickID
	deadline := r.now().Add(r.opts.GateTimeout)
	for {
		report, ciSHA, ciIsHead, err := r.ciForTree(ctx, pr)
		if err != nil {
			return "", false, r.refuse(RefusedLandPR, tick,
				"the epic PR of %s cannot be kept ready: CI on the epic PR #%d could not be read: %v",
				r.opts.EpicID, pr.Number, err)
		}
		if report.State == forge.CIRed && r.rerunRedCIOnce(ctx, tick, pr, report) {
			report.State = forge.CIPending
		}
		switch report.State {
		case forge.CIGreen:
			r.record(tick, StageLandCIGreen, "CI is green on %s", ciSubject(ciSHA, ciIsHead, pr))
			return ciSHA, false, nil
		case forge.CIRed:
			r.record(tick, StageGateFailed, "CI on the epic PR #%d is red on %s: %s failed; the repair job is "+
				"dispatched over it", pr.Number, short(ciSHA), strings.Join(report.Failing, ", "))
			if err := r.repairLandingCI(ctx, co, pr, ciSHA, report); err != nil {
				return "", false, err
			}
			return "", true, nil
		default:
			if r.now().After(deadline) {
				return "", false, r.refuse(RefusedLandCIPending, tick,
					"the epic PR #%d (%s) of %s is not ready: CI on it was still %s %s after the readying began "+
						"waiting on it. Run the epic again once CI concludes: the readying re-derives CI from the PR",
					pr.Number, pr.URL, r.opts.EpicID, report.State, r.opts.GateTimeout)
			}
			r.record(tick, StageLandHeld, "CI on the epic PR #%d is %s; the readying waits for it", pr.Number,
				report.State)
			r.sleep(r.pollInterval)
		}
	}
}

// repairLandingCI answers a red CI on the epic PR with the repair job, the way
// a failed integrated gate is answered: the failing jobs become an evidence
// record on the run branch — the repair's input, as a failing check's output
// is — and the repair's merge is gated as usual.
func (r *Reconciler) repairLandingCI(ctx context.Context, co *landingCloseout, pr *forge.PullRequest, sha string,
	report forge.CIReport) error {
	return r.repairRedCI(ctx, co.entry, co.marker, pr, sha, report, "land-ci")
}

// repairRedCI is the one answer to a red CI verdict on the epic's code, at
// every place the run meets one — the close-out's admission, the close-out's
// close, and the readying (epic-6in): the failing jobs are recorded as gate
// evidence under `prefix`, and the repair job is dispatched over them under
// the attempt `marker` names, its merge gated as usual behind `entry`.
func (r *Reconciler) repairRedCI(ctx context.Context, entry planEntry, marker attemptHandle, pr *forge.PullRequest,
	sha string, report forge.CIReport, prefix string) error {

	tick := marker.TickID
	failing := strings.Join(report.Failing, ", ")
	if failing == "" {
		failing = "a job the forge did not name"
	}
	key := fmt.Sprintf("%s-%s-%d-%s", prefix, tick, marker.Attempt, short(sha))
	if err := r.recordCIEvidence(marker, key, sha, pr, report); err != nil {
		return err
	}
	g := &gateProgress{
		passed:   false,
		failures: []string{fmt.Sprintf("CI on the epic PR #%d (%s)", pr.Number, failing)},
		failed:   []gateCheck{{Name: "ci", Key: key}},
	}
	attemptHead := sha
	if marker.WriteRef != "" {
		if head, err := r.git.remoteHead(branchOf(marker.WriteRef)); err == nil && head != "" {
			attemptHead = head
		}
	}
	merged := merge{AttemptHead: attemptHead, EpicHead: sha, GateSHA: sha, Merged: true}
	return r.repairFailedGate(ctx, entry, marker, merged, g)
}

// recordCIEvidence writes a red CI verdict as a gate evidence record: the
// repair job's inputs name evidence, and a failing CI job is the gate's answer
// run by the forge rather than by this host.
func (r *Reconciler) recordCIEvidence(marker attemptHandle, key, sha string, pr *forge.PullRequest,
	report forge.CIReport) error {

	dispatchProfile, err := r.profileOfMarker(marker)
	if err != nil {
		return fmt.Errorf("the CI evidence for %s cannot say which profile dispatched it: %w", marker.TickID, err)
	}
	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	if _, ok, err := r.store.Evidence(key); err != nil {
		return err
	} else if ok {
		return nil
	}
	tickID, attempt := marker.TickID, marker.Attempt
	integration := refFor(r.branch)
	code := 1
	at := r.now().UTC().Format(time.RFC3339)
	stdout := fmt.Sprintf("CI (%s) is red on %s, the head of the epic PR #%d (%s), after one re-run of its failed "+
		"jobs.\nFailing jobs: %s\nWorkflow runs: %v\nThe failing jobs' logs are on the forge; reproduce them locally "+
		"with the repository's own gate (.tick/runners.toml [testing.commands]) on this tree.\n",
		r.closeoutRule.CIWorkflow, sha, pr.Number, pr.URL, strings.Join(report.Failing, ", "), report.FailingRuns)
	record := runstate.Evidence{
		Key: key,
		Provenance: runstate.Provenance{
			RunID:                  r.runID,
			TickID:                 &tickID,
			Attempt:                &attempt,
			SourceRef:              integration,
			SourceSHA:              sha,
			IntegrationRef:         runstate.Ptr(integration),
			Phase:                  runstate.PhaseIntegrated,
			Executor:               gateExecutorPtr(marker),
			Role:                   runstate.Ptr(marker.Role),
			ProfileDigest:          runstate.Ptr(dispatchProfile.Digest),
			Tier:                   gateTierPtr(marker),
			SubstrateProtocol:      gateSubstrateProtocolPtr(marker),
			SubstrateServerVersion: gateSubstrateServerVersionPtr(marker),
			Model:                  runstate.Ptr(r.gateModel(marker)),
			ContextManifestDigest:  runstate.Ptr(r.gateDigest),
		},
		Check:      runstate.Check{ID: "ci", Kind: "command", Command: []string{"forge-ci", r.closeoutRule.CIWorkflow}},
		StartedAt:  at,
		FinishedAt: at,
		ExitCode:   &code,
		Output: runstate.Output{Inline: &runstate.InlineOutput{
			Mode: "inline", Stdout: bound(stdout), MaxBytes: maxInlineOutput,
		}},
		Result:         "fail",
		Acceptance:     "required",
		ContentDigest:  contentDigest(stdout, "", fmt.Sprint(code)),
		PersistenceURI: "git:" + integration + ":" + runstate.EvidencePath(r.runID, key),
	}
	if _, err := r.store.PutEvidence(record); err != nil {
		return fmt.Errorf("record the CI evidence for %s: %w", marker.TickID, err)
	}
	return nil
}

// mergeEpicInto makes the opt-in's merge commit — the epic head merged into
// the base head with tk's merge drivers, never fast-forwarded and never
// squashed — and pushes it with a lease on the base head. `moved` says the
// lease was lost (or the epic no longer merges cleanly onto that head): the
// base moved, and the caller's next pass folds it in.
func (r *Reconciler) mergeEpicInto(tick, base, baseHead, epicHead string, pr *forge.PullRequest) (string, bool, error) {
	drivers, err := r.mergeDrivers()
	if err != nil {
		return "", false, err
	}
	dir, remove, err := r.git.tempWorktree("ticfac-land-", baseHead)
	if err != nil {
		return "", false, fmt.Errorf("prepare the merge worktree at %s: %w", short(baseHead), err)
	}
	defer remove()

	message := fmt.Sprintf("Merge branch '%s' into %s (#%d)\n\nticfac run %s merges epic %s, as the repository's "+
		"rules opt in to: every tick closed behind the integrated gate, the final review carried on the epic PR, "+
		"CI green on the head merged here.", r.branch, base, pr.Number, r.runID, r.opts.EpicID)
	args := append(driverConfig(drivers), "merge", "--no-ff", "--no-edit", "-m", message, epicHead)
	if _, stderr, err := r.git.try(dir, args...); err != nil {
		unmerged, _ := r.git.run(dir, "diff", "--name-only", "--diff-filter=U")
		_, _, _ = r.git.try(dir, "merge", "--abort")
		if strings.TrimSpace(unmerged) != "" {
			// The epic was folded onto this very head, so a conflict is a head
			// the fold did not carry: the base moved.
			return "", true, nil
		}
		return "", false, fmt.Errorf("merge %s into %s: %w: %s", r.branch, base, err, firstLine(stderr))
	}
	merged, err := r.git.run(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", false, err
	}
	_, stderr, pushErr := r.git.try("", "push",
		"--force-with-lease="+refFor(base)+":"+baseHead,
		r.opts.Remote, merged+":"+refFor(base))
	if pushErr == nil {
		return merged, false, nil
	}
	if leaseRefused(stderr) {
		return "", true, nil
	}
	return "", false, r.refuse(RefusedLandPush, tick,
		"the epic %s is not merged: %s refused the push of its merge into %s (%v: %s). That is not the base moving "+
			"— a moved base loses the lease and the next pass folds again — it is the remote declining the write: a "+
			"protected branch, a permission, a hook. Allow this run's credential to push %s, or merge the epic PR "+
			"#%d by hand; a re-run then finds it merged",
		r.opts.EpicID, r.opts.Remote, base, pushErr, firstLine(stderr), base, pr.Number)
}

// verifyBaseCI waits for CI on the base branch's commit that carries the
// epic. Green is the merge verified; red — after the one re-run — is a typed
// failure naming the jobs, since the base the next epic is cut from is red. A
// CI that has not concluded within the run's bound is recorded as UNVERIFIED
// rather than failed: the merge's tree is the tree CI already passed on the
// epic PR, so the base's run is confirmation, not the gate.
func (r *Reconciler) verifyBaseCI(ctx context.Context, tick string, pr *forge.PullRequest, base, sha string) error {
	target := forge.PullRequest{HeadRef: base, BaseRef: base, HeadSHA: sha}
	if pr != nil {
		target.Number, target.URL = pr.Number, pr.URL
	}
	deadline := r.now().Add(r.opts.GateTimeout)
	for {
		report, err := r.opts.PullRequests.CI(ctx, target)
		if err != nil {
			report = forge.CIReport{State: forge.CIPending}
			r.record(tick, StageLandHeld, "CI on %s at %s could not be read: %v", base, short(sha), err)
		}
		if report.State == forge.CIRed && r.rerunRedCIOnce(ctx, tick, &target, report) {
			report.State = forge.CIPending
		}
		switch report.State {
		case forge.CIGreen:
			r.record(tick, StageLandVerified, "CI is green on %s at %s, the merge that carries the epic", base, short(sha))
			return nil
		case forge.CIRed:
			return r.refuse(RefusedLandBaseCI, tick,
				"the epic %s is merged into %s as %s, and CI is RED there: %s failed. The epic PR's CI was green on "+
					"the same tree, so the difference is the base's own workflow or a flaky job; the base the next "+
					"epic is cut from is red, and that is what to fix first",
				r.opts.EpicID, base, short(sha), strings.Join(report.Failing, ", "))
		default:
			if r.now().After(deadline) {
				r.record(tick, StageLandVerified, "CI on %s at %s was still %s %s after the merge: the base's CI is "+
					"UNVERIFIED by this run, and the merge's tree is the tree CI passed on the epic PR", base, short(sha),
					report.State, r.opts.GateTimeout)
				return nil
			}
			r.record(tick, StageLandHeld, "CI on %s at %s is %s; the run waits to verify it", base, short(sha),
				report.State)
			r.sleep(r.pollInterval)
		}
	}
}
