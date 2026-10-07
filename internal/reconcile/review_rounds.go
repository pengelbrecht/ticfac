package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// A final review's NOT READY is the RUN's to act on (epic-6in, 2026-09-28).
//
// WHAT WAS WRONG. Epic 6in closed every tick, CI went green, and landing held
// for a person: its final review (decision 1) had judged it NOT READY. The
// review named five findings — one HIGH, a defect 6in's own 46x introduced —
// and because 6in's acceptance is prose, the prose rule (prose.go) sent all
// five to the backlog, outside the epic. Nothing in the run could ever address
// the review's reasons, so a verdict formed before later fixes could only end
// at a person's "merge it by hand, or close it". And the verdict line carried
// no detail: the hold said "NOT READY — DONE (NOT READY)".
//
// THE RULE. A review's NOT READY is its own definition-of-done judgement, so
// its BLOCKING findings are the epic's work, whatever shape the epic's
// acceptance has:
//
//   - A finding is BLOCKING when its severity is high
//     (subprocess.BlockingFinding) — one definition, shared by the review's
//     prompt, its report checker and this file. The checker refuses a NOT
//     READY without detail or without a blocking finding (lint.go), so every
//     NOT READY the run records is one it can act on.
//   - Each blocking finding's tick is made a CHILD of the epic: a finding the
//     absorption decision already placed inside the epic stays there, and one
//     it filed as backlog (the prose rule, a non-gating verdict) is ADOPTED
//     into the epic — re-parented, noted, never re-created. Lower severities
//     stay backlog.
//   - A fresh REVIEW tick (role review) is created under the epic, blocked by
//     every absorbed tick, and the close-out (when still open) is blocked by
//     it: the run works the fixes, then reviews the new tree.
//   - The rounds are BOUNDED (maxReviewRounds). A review that is still NOT
//     READY at the bound is the one stop left, and it holds for a person
//     naming the remaining reasons (land.go) — rare and meaningful, never a
//     bare "NOT READY". The bound is on asking the same question: a tree
//     that changed since the final review judged it is reviewed once more
//     (treeChangedSinceReview, epic-hn6), and only an unchanged one holds.
//
// The step is idempotent and needs no record of its own: the tracker is its
// state. A re-review tick that is already open is reused, an adoption already
// made is a no-op, an edge already drawn is left alone. So it runs both where
// the review closes (closeRoleTick) and where a run is resumed (Run) — which
// is how a run already held land_review_not_ready (epic-6in) proceeds on a new
// build with no person: the resume adopts the backlogged blocking findings,
// creates the re-review, works them and lands on the re-review's READY.

// maxReviewRounds bounds how many final reviews one run makes: the first, and
// one re-review after its NOT READY's blocking findings are fixed. A second
// NOT READY is a disagreement the run cannot settle by itself — another round
// is the same judgement asked again — so it holds for a person, naming what
// the review still says is missing. Unless the tree changed since that review
// judged it: then it is not the same judgement, and one more round is made
// (placeChangedTreeReview).
const maxReviewRounds = 2

// maxDriftReviewRounds bounds the rounds a run EXCUSES from maxReviewRounds
// because the base moved under the epic (baseDriftCaused). It is the backstop
// for a base that never stops moving: past it, a drift-caused NOT READY counts
// like any other, and the bound ends the run's rounds as it always did.
const maxDriftReviewRounds = 2

// reviewRounds is the run's review decisions, read for the rounds rule.
type reviewRounds struct {
	// final is the latest review-epic decision, nil when no review answered.
	final *runstate.Decision
	// rounds is how many review ticks have a recorded decision.
	rounds int
	// drift is how many of those rounds are excused from the bound: NOT READY
	// rounds caused by the base moving under the epic (baseDriftCaused), the
	// first maxDriftReviewRounds of them.
	drift int
	// finalDrift says the final review is one of the excused rounds, and
	// driftPaths is what the folds before it brought in that its blocking
	// findings name.
	finalDrift bool
	driftPaths []string
	// inherited is the EARLIER run of this epic whose review decision final
	// is — "" when final is this run's own. See inheritedReviews.
	inherited string
}

// counted is the rounds that count toward maxReviewRounds.
func (rr reviewRounds) counted() int { return rr.rounds - rr.drift }

// driftNote is what a record says about the rounds excused as the base
// moving: "" when none was.
func (rr reviewRounds) driftNote() string {
	switch {
	case rr.finalDrift:
		return fmt.Sprintf(". This NOT READY does not count toward the bound: the base moved under the epic since "+
			"the previous review — its folds brought in %s, which every blocking finding names — so it is about "+
			"the base, not the epic's own work (%d of at most %d such rounds excused)",
			strings.Join(rr.driftPaths, ", "), rr.drift, maxDriftReviewRounds)
	case rr.drift > 0:
		return fmt.Sprintf(" (%d earlier NOT READY %s about the base moving under the epic, excused from the "+
			"bound; at most %d are)", rr.drift, plural(rr.drift, "round was", "rounds were"), maxDriftReviewRounds)
	}
	return ""
}

// spent says the bound is spent for the final review: the run absorbs no
// more of its findings. An excused final review never spends it.
func (rr reviewRounds) spent() bool { return !rr.finalDrift && rr.counted() >= maxReviewRounds }

// readReviewRounds reads the review decisions off origin.
func (r *Reconciler) readReviewRounds() (reviewRounds, error) {
	var out reviewRounds
	if _, err := r.store.Fetch(); err != nil {
		return out, err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return out, fmt.Errorf("read the run's decisions for the final review's verdict: %w", err)
	}
	// One round per review tick, its latest decision, in decision order.
	latest := map[string]*runstate.Decision{}
	for i := range decisions {
		if decisions[i].Role != "review-epic" {
			continue
		}
		if out.final == nil || decisions[i].Decision > out.final.Decision {
			out.final = &decisions[i]
		}
		tick, _ := decisions[i].Request["tick_id"].(string)
		if prev := latest[tick]; prev == nil || decisions[i].Decision > prev.Decision {
			latest[tick] = &decisions[i]
		}
	}
	out.rounds = len(latest)
	// The newest ended earlier run of the epic's reviews (inheritedReviews):
	// its rounds count with this run's, so a resume under a new run id counts
	// them the way a resume under the same id always has, and its final
	// review is the final one while this run has none of its own. Its rounds
	// are not read for drift: decision numbers order one run's decisions, not
	// two runs'.
	sibling, theirs, err := r.inheritedReviews()
	if err != nil {
		return out, err
	}
	if sibling != "" {
		var theirFinal *runstate.Decision
		counted := map[string]bool{}
		for i := range theirs {
			if theirFinal == nil || theirs[i].Decision > theirFinal.Decision {
				theirFinal = &theirs[i]
			}
			tick, _ := theirs[i].Request["tick_id"].(string)
			if latest[tick] == nil && !counted[tick] {
				counted[tick] = true
				out.rounds++
			}
		}
		if out.final == nil {
			out.final, out.inherited = theirFinal, sibling
		}
	}
	if len(latest) < 2 {
		return out, nil
	}
	ordered := make([]*runstate.Decision, 0, len(latest))
	for _, d := range latest {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Decision < ordered[j].Decision })
	baseHead := ""
	for i := 1; i < len(ordered); i++ {
		if reviewVerdictOf(ordered[i].Response) != subprocess.ReviewVerdictNotReady {
			continue
		}
		if baseHead == "" {
			if baseHead = r.baseHeadForFolds(); baseHead == "" {
				// No base, no fold to tell apart: every round counts.
				break
			}
		}
		paths, caused := r.baseDriftCaused(*ordered[i-1], *ordered[i], baseHead)
		if !caused || out.drift >= maxDriftReviewRounds {
			continue
		}
		out.drift++
		if ordered[i] == out.final {
			out.finalDrift, out.driftPaths = true, paths
		}
	}
	return out, nil
}

// A NOT READY caused by the base moving is not a round (epic ilz, 2026-10-07).
//
// WHAT WAS WRONG. Epic ilz wrote an operator guide to cloudflare/src/claude-
// sub.ts while four PRs rewrote that file on main. Each time the run folded
// main in, the reviewer — correctly — found the guide's claims stale against
// the code at the integration head; the run fixed it, and the next round found
// the next main merge's drift. Every round counted toward maxReviewRounds, so
// after three the run held for a person over a defect the epic never made:
// "Guide's failover claims went stale when main merged under the epic".
//
// THE RULE. A NOT READY round is CAUSED BY THE BASE (baseDriftCaused) when,
// mechanically:
//
//   - between the tree the previous review judged and the tree this one
//     judged, the epic branch's first-parent line carries a FOLD of the base
//     (a merge whose second parent is on the base, as treeChangedSinceReview
//     reads it) that brought in paths outside the run's bookkeeping; and
//   - EVERY blocking finding of this review names one of those paths — by its
//     repository path, or by its file name with an extension.
//
// Such a round does not count toward maxReviewRounds: its blocking findings
// are absorbed and the epic reviewed again, as for round 1. A blocking finding
// that names nothing the base brought in is the epic's own, and the round
// counts — so a reviewer cannot excuse its epic's defects by calling them
// drift, and a base that did not move excuses nothing. The backstop is
// maxDriftReviewRounds: a base that moves under every round is excused that
// many times, and after that its rounds count again and the bound holds.

// A final review recorded by an EARLIER run of the epic (epic ilz,
// 2026-10-07).
//
// WHAT WAS WRONG. ilz's cloud run held land_review_not_ready: every tick
// closed, the close-out closed, the epic PR held on a NOT READY final review.
// The operator fixed the doc the review named and ran the epic again — the
// hold's own instruction. A cloud resume is a NEW submission, so it runs under
// a new run id, and every review rule read only the new run's own decisions:
// it found no review at all, so nothing asked whether the tree had changed
// since the NOT READY; it found no close-out of its own, so nothing was left
// to ready; and with every tick closed the run stopped over "epic ilz has no
// dispatchable tick" — an unclassified stop. The operator filed a review tick
// and reopened the close-out by hand.
//
// THE RULE. The review rules read the final review where it was recorded: a
// run with no review decision of its own takes the newest ENDED earlier run of
// its epic's final review as the final one (inheritedReviews), and counts that
// run's rounds with its own. An inherited verdict is never absorbed again — the
// run that recorded it already acted on it — so a NOT READY inherited is the
// spent-bound case whatever the count: a tree changed since it judged gets one
// more review, and its close-out after it (reopened when this run has none of
// its own to land with); an unchanged tree holds with the land hold's message.

// inheritedReviews is the earlier run of this epic whose review decisions the
// review rules read: among the runs on the integration branch whose checkpoint
// names this epic, the one whose newest review-epic decision answered last —
// taken only when that run has ENDED (its checkpoint is terminal, or its host
// says it is gone), because a live run's verdict is its own to act on. It
// answers "" when there is none.
func (r *Reconciler) inheritedReviews() (string, []runstate.Decision, error) {
	runs, err := r.inheritedRuns()
	if err != nil {
		return "", nil, err
	}
	best, bestAt := "", ""
	var bestReviews []runstate.Decision
	for _, runID := range runs {
		checkpoint, ok, err := r.store.ForeignCheckpoint(runID)
		if err != nil || !ok || checkpoint.EpicID != r.opts.EpicID {
			// An unreadable checkpoint says nothing about whose run it is
			// (the boot sweep says so once, inherit.go); it is not this
			// epic's verdict to take.
			continue
		}
		decisions, err := r.store.ForeignDecisions(runID)
		if err != nil {
			continue
		}
		var reviews []runstate.Decision
		at := ""
		for _, d := range decisions {
			if d.Role != "review-epic" {
				continue
			}
			reviews = append(reviews, d)
			if d.AnsweredAt > at {
				at = d.AnsweredAt
			}
		}
		if len(reviews) == 0 || (best != "" && at <= bestAt) {
			continue
		}
		var answer HolderState
		if !checkpoint.State.Terminal() && r.opts.ClaimHolder != nil {
			answer = r.opts.ClaimHolder(context.Background(), runID)
		}
		if _, ended := r.endedRun(checkpoint, answer); !ended {
			continue
		}
		best, bestAt, bestReviews = runID, at, reviews
	}
	return best, bestReviews, nil
}

// inheritedFrom names the earlier run a final review was recorded by, for a
// record line: "" when it is the run's own.
func inheritedFrom(rounds reviewRounds) string {
	if rounds.inherited == "" {
		return ""
	}
	return ", recorded by " + rounds.inherited + ", an earlier run of the epic"
}

// baseHeadForFolds is the base's head as origin has it now, for telling a
// fold of the base from the epic's own merges; "" when the base cannot be
// read, which leaves every merge the epic's.
func (r *Reconciler) baseHeadForFolds() string {
	base := r.prBase()
	if base == "" || base == r.branch || r.git.fetch(base) != nil {
		return ""
	}
	head, _ := r.git.remoteHead(base)
	return head
}

// baseDriftCaused says whether the review `cur`, a NOT READY, is about the
// base moving under the epic since the review `prev` (the rule above), and
// the folded paths its blocking findings name.
func (r *Reconciler) baseDriftCaused(prev, cur runstate.Decision, baseHead string) ([]string, bool) {
	from, _ := prev.Request["source_sha"].(string)
	to, _ := cur.Request["source_sha"].(string)
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" || from == to || baseHead == "" {
		return nil, false
	}
	blocking := blockingFindingsOf(cur)
	if len(blocking) == 0 {
		return nil, false
	}
	if r.git.fetch(r.branch) != nil {
		return nil, false
	}
	folded := r.foldedPaths(from, to, baseHead)
	if len(folded) == 0 {
		return nil, false
	}
	named := map[string]bool{}
	for _, f := range blocking {
		hit := false
		for _, path := range folded {
			if namesPath(f.Title+"\n"+f.Body, path) {
				named[path], hit = true, true
			}
		}
		if !hit {
			return nil, false
		}
	}
	paths := make([]string, 0, len(named))
	for path := range named {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, true
}

// foldedPaths is every path outside the bookkeeping that a fold of the base
// on the first-parent line from `from` to `to` brought in, each fold read
// against its first parent.
func (r *Reconciler) foldedPaths(from, to, baseHead string) []string {
	out, err := r.git.run("", "log", "--first-parent", "--merges", "--format=%H %P", from+".."+to)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !r.git.contains(fields[2], baseHead) {
			continue
		}
		changed, err := r.git.run("", "diff", "--name-only", fields[1], fields[0])
		if err != nil {
			continue
		}
		for _, path := range strings.Split(changed, "\n") {
			path = strings.TrimSpace(path)
			if path == "" || seen[path] || underReviewBookkeeping(path) {
				continue
			}
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

// namesPath says whether a finding's text names a path: the path itself, or
// its file name when that has an extension, standing as a word of its own
// ("claude-sub.ts:208" names cloudflare/src/claude-sub.ts; "sub.tsx" does not).
func namesPath(text, path string) bool {
	if strings.Contains(text, path) {
		return true
	}
	name := path[strings.LastIndex(path, "/")+1:]
	if !strings.Contains(name, ".") || strings.HasPrefix(name, ".") {
		return false
	}
	for at := 0; ; {
		i := strings.Index(text[at:], name)
		if i < 0 {
			return false
		}
		start, end := at+i, at+i+len(name)
		if (start == 0 || !pathWordByte(text[start-1], true)) && (end == len(text) || !pathWordByte(text[end], false)) {
			return true
		}
		at = start + 1
	}
}

// pathWordByte is a byte that continues a file name: before it, a letter,
// digit, '_', '-' or '.'; after it, the same but '.', which ends a sentence.
func pathWordByte(b byte, before bool) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '_', b == '-':
		return true
	case b == '.':
		return before
	}
	return false
}

// underReviewBookkeeping says a path is the run's or the tracker's own
// record, never the epic's work (reviewBookkeepingPrefixes).
func underReviewBookkeeping(path string) bool {
	for _, prefix := range reviewBookkeepingPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// reviewFindingsOf is the findings a recorded review decision carries, read
// back as the records they were drafted from — so a finding's key here is
// the key fileFindings drafted it under.
func reviewFindingsOf(decision runstate.Decision) []subprocess.Finding {
	raw, err := json.Marshal(decision.Response["findings"])
	if err != nil {
		return nil
	}
	var findings []subprocess.Finding
	if json.Unmarshal(raw, &findings) != nil {
		return nil
	}
	return findings
}

// blockingFindingsOf is the recorded review's blocking findings.
func blockingFindingsOf(decision runstate.Decision) []subprocess.Finding {
	var out []subprocess.Finding
	for _, f := range reviewFindingsOf(decision) {
		if subprocess.BlockingFinding(f) {
			out = append(out, f)
		}
	}
	return out
}

// reviewVerdictDetailOf is what the review said would make the epic ready:
// the verdict line's own detail, else — for a record made before the detail
// was carried — the review's summary, which is all such a record has.
func reviewVerdictDetailOf(decision runstate.Decision) string {
	if result, _ := decision.Response["result"].(map[string]any); result != nil {
		if detail, _ := result["review_verdict_detail"].(string); strings.TrimSpace(detail) != "" {
			return strings.TrimSpace(detail)
		}
	}
	summary, _ := decision.Response["summary"].(string)
	return strings.TrimSpace(summary)
}

// notReadyReasons is a NOT READY's reasons as one sentence: what the review
// said would make the epic ready, and every blocking finding by title.
func notReadyReasons(decision runstate.Decision) string {
	reasons := reviewVerdictDetailOf(decision)
	if reasons == "" {
		reasons = "the review stated no detail"
	}
	blocking := blockingFindingsOf(decision)
	if len(blocking) == 0 {
		return reasons + "; it named no blocking (high-severity) finding"
	}
	titles := make([]string, 0, len(blocking))
	for _, f := range blocking {
		titles = append(titles, fmt.Sprintf("%q", f.Title))
	}
	return fmt.Sprintf("%s; its blocking finding(s): %s", reasons, strings.Join(titles, "; "))
}

// answerNotReadyReview acts on a final review's NOT READY: the blocking
// findings become children of the epic, and a fresh review is placed behind
// them and before the close-out. It reports whether it acted, so a caller
// holding a graph re-reads it. It does nothing when the final review is not
// NOT READY, when the rounds are spent over a tree unchanged since that review
// (the land hold names the reasons), or when no blocking finding is this run's
// to fix. Past the bound, a changed tree is reviewed once more instead.
func (r *Reconciler) answerNotReadyReview(ctx context.Context) (bool, error) {
	if r.store == nil || r.opts.notReadyForAPerson {
		return false, nil
	}
	durable, ok := r.tracker.(*durableTracker)
	if !ok {
		return false, nil
	}
	rounds, err := r.readReviewRounds()
	if err != nil {
		return false, err
	}
	if rounds.final == nil || reviewVerdictOf(rounds.final.Response) != subprocess.ReviewVerdictNotReady {
		return false, nil
	}
	final := *rounds.final
	reviewed, _ := final.Request["tick_id"].(string)
	if rounds.inherited != "" {
		// An earlier run's verdict (epic ilz): that run acted on it, so
		// nothing is absorbed here — the tree decides, as past the bound.
		return r.reviewIfTreeChanged(ctx, durable, rounds)
	}
	if rounds.spent() {
		// The bound is spent. Past it nothing is absorbed — the run does not
		// argue with its review — but a tree that changed since the final
		// review judged it is a tree no review has judged (epic-hn6): it is
		// reviewed once more, and only an unchanged tree holds.
		return r.reviewIfTreeChanged(ctx, durable, rounds)
	}
	blocking := blockingFindingsOf(final)
	if len(blocking) == 0 {
		return false, nil
	}

	// Every finding still waiting for a decision is decided first, by the
	// rules it was filed under: the tick a blocking finding became is what is
	// adopted, and an undecided finding has none yet.
	if err := r.decideUndecidedFindings(ctx); err != nil {
		return false, err
	}
	if _, err := r.store.Fetch(); err != nil {
		return false, err
	}

	// The adoptions, their notes, the re-review and its placement are one
	// step (tick f61): one push. The deciding above is NOT in it — a decision
	// may wait on a classifier, and nothing may wait with a record held.
	// Nothing in the step acts outside the run, and every write in it is
	// idempotent on a resume, which re-derives the answer from the review's
	// recorded decision.
	acted := false
	err = r.heldStep(func() error {
		var stepErr error
		acted, stepErr = r.absorbNotReadyFindings(ctx, durable, rounds, final, reviewed, blocking)
		return stepErr
	})
	return acted, err
}

// absorbNotReadyFindings is answerNotReadyReview's held step: the blocking
// findings' ticks adopted into the epic, and the re-review filed and placed.
func (r *Reconciler) absorbNotReadyFindings(ctx context.Context, durable *durableTracker, rounds reviewRounds,
	final runstate.Decision, reviewed string, blocking []subprocess.Finding) (bool, error) {
	// The blocking findings' ticks, adopted into the epic where the decision
	// filed them outside it.
	var absorbed []string
	for _, f := range blocking {
		key := findingKey(f)
		record, ok, err := r.store.Absorption(key)
		if err != nil {
			return false, err
		}
		if !ok || record.TickID == "" {
			// Decided by a person (fixed, discarded) or not at all: there is
			// no tick of the run's to adopt, and the re-review judges the tree.
			r.record(reviewed, StageReviewRound, "blocking finding %s (%q) has no tick of the run's to absorb: "+
				"it was settled otherwise", key, f.Title)
			continue
		}
		if notAWorkersTick(f) {
			// A protected edit (protected_changes.go): no worker can make it,
			// so naming it blocking cannot make it the epic's work — the run
			// applies a carried change at the close-out, and the merger reads it.
			r.record(reviewed, StageReviewRound, "blocking finding %s (%q) is an edit of a file no worker may "+
				"write; it is not absorbed into the epic, and the epic PR carries it for the merger", key, f.Title)
			continue
		}
		if record.Placement == runstate.AbsorptionRouted || record.Target != "" || isLiveRun(*record) {
			// Another repository's, or a live run's: not a tree this run builds.
			r.record(reviewed, StageReviewRound, "blocking finding %s (%q) is not this run's to fix (%s); it "+
				"stays where it was filed", key, f.Title, placementLine(*record))
			continue
		}
		tick, err := r.tracker.Show(ctx, record.TickID)
		if err != nil {
			return false, fmt.Errorf("read the tick %s blocking finding %s became: %w", record.TickID, key, err)
		}
		if tick.Parent != r.opts.EpicID {
			if tick.Status == "closed" {
				// A backlog tick somebody already closed: its work is done
				// outside the run, and the re-review judges the tree.
				continue
			}
			if err := durable.Adopt(ctx, record.TickID, r.opts.EpicID); err != nil {
				return false, fmt.Errorf("absorb the blocking finding %s's tick %s into the epic %s: %w",
					key, record.TickID, r.opts.EpicID, err)
			}
			note := fmt.Sprintf("ticfac run %s: absorbed into the epic %s — the final review (decision %d, %s) "+
				"judged the epic NOT READY and named this finding as blocking (severity high). A review's NOT READY "+
				"is its own definition-of-done judgement, so its blocking findings are the epic's work whatever the "+
				"shape of the epic's acceptance; the epic is reviewed again once this tick closes.",
				r.runID, r.opts.EpicID, final.Decision, reviewed)
			if _, err := r.tracker.Note(ctx, record.TickID, note); err != nil {
				return false, fmt.Errorf("note the absorption of %s: %w", record.TickID, err)
			}
			// The decision record says so (the 2026-10-06 policy): the finding
			// is in the epic on the REVIEWER's basis now, whatever placed it
			// outside — so a cold reconstruction reads the epic the warm run
			// reached, and the PR says who brought it in.
			if err := r.recordReviewerAbsorption(*record, final, reviewed); err != nil {
				return false, err
			}
			r.record(reviewed, StageAbsorbed, "blocking finding %s (%q) of the NOT READY review is absorbed into "+
				"the running epic: its tick %s, filed as %s, is now a child of %s", key, f.Title, record.TickID,
				record.Placement, r.opts.EpicID)
		}
		absorbed = append(absorbed, record.TickID)
	}
	if len(absorbed) == 0 {
		return false, nil
	}

	// The re-review: one open review tick under the epic other than the one
	// that answered, reused when an earlier incarnation already made it.
	rereview, closeout, err := r.openReReview(ctx, reviewed)
	if err != nil {
		return false, err
	}
	round := rounds.rounds + 1
	if rereview == "" {
		description := fmt.Sprintf(
			"Review the epic %s again, AS INTEGRATED, now that the blocking findings of its NOT READY review are "+
				"fixed. This is review round %d; %d count toward the bound of %d.\n\nThe previous review (%s, decision "+
				"%d) judged it NOT READY: %s.\n\nJudge the tree as it stands: READY when the epic does what it said it "+
				"would, NOT READY naming what still blocks it. A NOT READY that spends the bound is not reviewed again "+
				"by the run unless the tree changes after it: it holds for a person with your reasons.",
			r.opts.EpicID, round, rounds.counted()+1, maxReviewRounds, reviewed, final.Decision, notReadyReasons(final))
		rereview, err = r.createReReview(ctx, durable,
			fmt.Sprintf("Re-review %s after its NOT READY review's blocking findings (round %d)", r.opts.EpicID, round),
			description)
		if err != nil {
			return false, err
		}
	}
	for _, blocker := range absorbed {
		if err := durable.BlockOn(ctx, rereview, blocker); err != nil {
			return false, fmt.Errorf("place the re-review %s behind %s: %w", rereview, blocker, err)
		}
	}
	if closeout != "" {
		if err := durable.BlockOn(ctx, closeout, rereview); err != nil {
			return false, fmt.Errorf("place the close-out %s behind the re-review %s: %w", closeout, rereview, err)
		}
	}
	r.record(reviewed, StageReviewRound,
		"the final review (decision %d) judged %s NOT READY — %s. The run acts on it: %s absorbed into the epic, "+
			"and the re-review %s (round %d; the bound is %d) runs once %s closed%s",
		final.Decision, r.opts.EpicID, notReadyReasons(final), strings.Join(absorbed, ", "), rereview, round,
		maxReviewRounds, plural(len(absorbed), "it is", "they are"), rounds.driftNote())
	return true, nil
}

// openReReview is the epic's open review tick other than the one that
// answered — a re-review an earlier incarnation already made — and its open
// close-out, each "" when there is none.
func (r *Reconciler) openReReview(ctx context.Context, reviewed string) (rereview, closeout string, err error) {
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return "", "", fmt.Errorf("read the epic graph of %s to place the re-review: %w", r.opts.EpicID, err)
	}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			switch {
			case task.Role == "review" && task.ID != reviewed && task.Status != "closed":
				rereview = task.ID
			case task.Role == "closeout" && task.Status != "closed":
				closeout = task.ID
			}
		}
	}
	return rereview, closeout, nil
}

// createReReview files a fresh review tick under the epic.
func (r *Reconciler) createReReview(ctx context.Context, durable *durableTracker, title, description string) (string, error) {
	id, err := durable.MintTickID()
	if err != nil {
		return "", err
	}
	at := r.now().UTC().Format(time.RFC3339)
	created, err := durable.CreateTick(ctx, tk.Tick{
		ID:          id,
		Title:       title,
		Description: description,
		Status:      "open",
		Priority:    1,
		Type:        "task",
		Owner:       r.opts.Owner,
		Parent:      r.opts.EpicID,
		Role:        "review",
		CreatedBy:   "ticfac run " + r.runID,
		CreatedAt:   at,
		UpdatedAt:   at,
	})
	if err != nil {
		return "", fmt.Errorf("create the re-review of %s: %w", r.opts.EpicID, err)
	}
	return created.ID, nil
}

// A spent bound over a CHANGED tree is reviewed again (epic-hn6, 2026-10-06).
//
// WHAT WAS WRONG. hn6's run held land_review_not_ready: its final review
// (decision 26) still judged it NOT READY after the two rounds the bound
// allows, and the one blocking finding was stray RESULT-*.md files at the
// epic branch's root. A person committed the fix to epic/hn6. Running the
// epic again — what the hold itself tells a person to do — held again on the
// same verdict, because the bound alone decided: the hold read "is the final
// review NOT READY, and are the rounds spent?" and never "is this still the
// tree it judged?". A verdict about a tree that no longer exists stood over
// the one that does, and the only way past it was merging by hand.
//
// THE RULE. The bound limits how often the run asks the SAME question, and a
// changed tree is a different question. So when the bound is spent and the
// final review is NOT READY:
//
//   - the epic branch as origin holds it now is compared with the tree the
//     final review judged; a change outside the run's own bookkeeping and the
//     close-out's own merge (work the run closed since, a commit anybody
//     pushed — a person's fix) is a
//     tree no review has judged, and ONE more review is placed under the epic
//     (and before the close-out, when that is still open) instead of a hold;
//   - nothing is absorbed past the bound: the extra review judges the tree as
//     it stands, it does not turn the old findings into work again;
//   - an UNCHANGED tree holds exactly as before (land.go's
//     landingReviewHold, its message naming the reasons).
//
// It terminates because each extra round is gated the same way: the extra
// review's own decision records the tree it judged, and its NOT READY over a
// tree nothing has changed since is the hold. Every further round needs a
// further change.
//
// The step hooks in where answerNotReadyReview already runs — where a review
// closes (closeRoleTick) and where a run is resumed (Run) — so the hn6 case,
// every tick including the close-out closed and the run held at the land, is
// a re-run that finds the new review tick open, works it, and lands on its
// READY through the readying as any READY final review does.

// reviewBookkeepingPrefixes are the paths a commit may touch and still leave
// the tree a review judged unchanged: the run's durable state (checkpoints,
// decisions, evidence, .ticfac/runs/**) and the tracker's records (.tick/**,
// the review's own close among them). Wider than ciIgnoredPrefixes on
// purpose: a .tick/ config file can change what CI says, but not what the
// epic's work is.
var reviewBookkeepingPrefixes = []string{runStatePrefix, ".tick/"}

// treeChangedSinceReview reports whether the epic branch holds work the
// review decision never judged, and the head it compared.
//
// WHAT WAS JUDGED. A review's decision records request.source_sha: the commit
// its attempt was cut from, which is the integration branch's head at its
// dispatch (recordDecision has recorded it since role jobs began, tick q4u),
// so no new record is needed. A decision without one — none is known, but a
// record is not trusted to be what it ought to be — answers UNCHANGED: the
// conservative side, because it keeps the hold a person already understands
// rather than buying a review on a guess.
//
// WHAT COUNTS AS A CHANGE. Every commit on the branch's first-parent line
// since that head, each read against its first parent (a merge as everything
// it brought in), is a change when it touches a path outside the bookkeeping
// (reviewBookkeepingPrefixes) — with ONE exception: the close-out's own merge.
// The close-out runs after the final review by construction (it is placed
// behind it) and lands its verification report and retro; that is not an
// answer to the review, and counting it would turn every hold into one more
// review the moment anybody re-ran the epic: the same question asked again.
// A merge is the close-out's when its message names a close-out tick of the
// epic (integrate.go writes "ticfac run <run>: tick <id> attempt <n>").
// The other exception is a FOLD OF THE BASE: a merge whose second parent is
// on the base branch (the land's fold, the start's fold, the resolve-conflict
// job's fold, a person merging main in). Counting it made a loop of the land
// while other PRs kept landing on main: a fold, a review, main moved again,
// another fold, another review — an epic that never lands. The base's code
// was reviewed where it landed, and the fold is gated by the integrated gate
// and CI before anything merges; what the review owes a judgement on is the
// epic's own work, which a fold does not add to. A resolve-conflict fold may
// carry resolution edits to the epic's files, and those are not re-reviewed
// either: they fit the epic's work to the base rather than change what it
// does, they pass the same gate and CI, and reviewing them would bring the
// loop back for every epic that touches what main touches. Everything else
// counts: work the run closed since, a person's commit, a repair.
//
// A history that cannot be read (the judged commit gone after a force push)
// answers CHANGED: a commit the branch can no longer show is not a tree
// anybody can say the review judged, and the review that answers it records
// the head it judged, so the next comparison is readable.
//
// A fold IS counted when it brought in a path the epic is ABOUT
// (foldsTouchingEpic, epic ilz): the second answer names those paths.
func (r *Reconciler) treeChangedSinceReview(ctx context.Context, decision runstate.Decision) (string, []string,
	bool, error) {
	judged, _ := decision.Request["source_sha"].(string)
	if strings.TrimSpace(judged) == "" {
		return "", nil, false, nil
	}
	if err := r.git.fetch(r.branch); err != nil {
		return "", nil, false, fmt.Errorf("fetch %s to compare it with the tree the final review judged: %w",
			r.branch, err)
	}
	head, err := r.git.remoteHead(r.branch)
	if err != nil {
		return "", nil, false, err
	}
	if head == "" || head == judged {
		return head, nil, false, nil
	}
	// The close-out's ticks, CLOSED ones included: the close-out runs after
	// the final review and closes before the land asks, and `tk graph` lists
	// open tasks alone. Reading the open graph left the closed close-out out,
	// so its own merge (its retro) read as work no review had judged and
	// bought one more review: epic ilz's round 3 (2026-10-07). The fake
	// tracker's graph lists closed tasks, which is why no test saw it.
	closeouts, err := r.closeoutTicks(ctx)
	if err != nil {
		return "", nil, false, err
	}
	// The base's head, for telling a fold from the epic's own merges. A base
	// that cannot be read leaves every merge counted: the conservative side
	// is a review, never a merge nobody judged.
	baseHead := r.baseHeadForFolds()
	if r.changedOutsideBookkeeping(judged, head, baseHead, closeouts) {
		return head, nil, true, nil
	}
	about, err := r.foldsTouchingEpic(ctx, judged, head, baseHead, closeouts)
	if err != nil {
		return "", nil, false, err
	}
	return head, about, len(about) > 0, nil
}

// closeoutTicks is the epic's close-out ticks, closed ones included.
func (r *Reconciler) closeoutTicks(ctx context.Context) ([]string, error) {
	graph, err := r.graphWithClosed(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the epic graph of %s for its close-out: %w", r.opts.EpicID, err)
	}
	var closeouts []string
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Role == "closeout" {
				closeouts = append(closeouts, task.ID)
			}
		}
	}
	return closeouts, nil
}

// A fold of the base that changes what the epic is ABOUT is reviewed (epic
// ilz, 2026-10-07).
//
// WHAT WAS WRONG. ilz wrote docs/claude-sub-operator.md, a guide to
// cloudflare/src/claude-sub.ts. Main changed claude-sub.ts after ilz's READY
// review, and the fold that brought it in was not reviewed — a fold of the
// base is not the epic's work (treeChangedSinceReview) — so the run would have
// landed a guide stale against the code it landed beside. The close-out's
// retro found it (finding xe9, high), but a close-out's findings go to the
// backlog: the land had nothing to wait on.
//
// THE RULE. A fold of the base between the tree the final review judged and
// the branch's head is a change the review is owed when it brought in a path
// the epic is ABOUT, mechanically:
//
//   - a path the epic's own diff touches: the epic branch against its merge
//     base with the base, at the judged tree or at the head; or
//   - a path the epic's acceptance criteria name, matched the way a blocking
//     finding is matched to a folded path (namesPath).
//
// A fold that touches nothing of the epic stays unreviewed, as before. A round
// such a fold buys is an ordinary round: its NOT READY, when every blocking
// finding names a path the base brought in, is excused from the bound as the
// base moving (baseDriftCaused). The backstop for a base that keeps changing
// the epic's files is maxDriftReviewRounds again: once that many of the run's
// rounds were bought by folds alone (foldReviewRounds), a fold buys no more,
// and the land goes ahead on the latest verdict.
//
// It is asked wherever the tree-changed rule is, and at the land too: the
// land's own fold comes after the last of those asks, so a readying whose fold
// brought in what the epic is about stops — resumably, needing nobody
// (RefusedLandFoldReview) — and the next incarnation reviews the folded tree
// before it lands.

// RefusedLandFoldReview is the readying's stop when its own fold brought in a
// path the epic is about after a READY final review: the next incarnation
// reviews the folded tree, and nobody has anything to decide.
const RefusedLandFoldReview = "land_fold_review"

// foldsTouchingEpic is the paths a fold of the base on the first-parent line
// from `judged` to `head` brought in that the epic is about (the rule above),
// sorted; nil when there are none, or once the backstop is reached.
func (r *Reconciler) foldsTouchingEpic(ctx context.Context, judged, head, baseHead string,
	closeouts []string) ([]string, error) {
	if baseHead == "" {
		return nil, nil
	}
	folded := r.foldedPaths(judged, head, baseHead)
	if len(folded) == 0 {
		return nil, nil
	}
	own := map[string]bool{}
	for _, at := range []string{judged, head} {
		for _, path := range r.epicDiffPaths(at, baseHead) {
			own[path] = true
		}
	}
	epic, err := r.tracker.Show(ctx, r.opts.EpicID)
	if err != nil {
		return nil, fmt.Errorf("read the epic %s for the paths its acceptance names: %w", r.opts.EpicID, err)
	}
	var about []string
	for _, path := range folded {
		if own[path] || namesPath(epic.AcceptanceCriteria, path) {
			about = append(about, path)
		}
	}
	if len(about) == 0 || r.foldReviewRounds(baseHead, closeouts) >= maxDriftReviewRounds {
		return nil, nil
	}
	sort.Strings(about)
	return about, nil
}

// epicDiffPaths is the paths outside the bookkeeping that the epic branch at
// `at` changes against its merge base with the base.
func (r *Reconciler) epicDiffPaths(at, baseHead string) []string {
	mergeBase, err := r.git.run("", "merge-base", at, baseHead)
	if err != nil || strings.TrimSpace(mergeBase) == "" {
		return nil
	}
	out, err := r.git.run("", "diff", "--name-only", strings.TrimSpace(mergeBase), at)
	if err != nil {
		return nil
	}
	var paths []string
	for _, path := range strings.Split(out, "\n") {
		if path = strings.TrimSpace(path); path != "" && !underReviewBookkeeping(path) {
			paths = append(paths, path)
		}
	}
	return paths
}

// foldReviewRounds is how many of this run's review rounds were bought by
// folds alone: rounds whose tree differs from the previous round's by
// nothing but folds of the base, the close-out's merges and bookkeeping.
func (r *Reconciler) foldReviewRounds(baseHead string, closeouts []string) int {
	if r.store == nil {
		return 0
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return 0
	}
	latest := map[string]runstate.Decision{}
	for _, d := range decisions {
		if d.Role != "review-epic" {
			continue
		}
		tick, _ := d.Request["tick_id"].(string)
		if prev, ok := latest[tick]; !ok || d.Decision > prev.Decision {
			latest[tick] = d
		}
	}
	ordered := make([]runstate.Decision, 0, len(latest))
	for _, d := range latest {
		ordered = append(ordered, d)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Decision < ordered[j].Decision })
	n := 0
	for i := 1; i < len(ordered); i++ {
		from, _ := ordered[i-1].Request["source_sha"].(string)
		to, _ := ordered[i].Request["source_sha"].(string)
		if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" || from == to {
			continue
		}
		if !r.changedOutsideBookkeeping(from, to, baseHead, closeouts) {
			n++
		}
	}
	return n
}

// landFoldOwesReview is the land's ask of the rule above, once its own fold
// made `folded`: the refusal that stops the readying, resumably, when a READY
// final review never judged what the fold brought in that the epic is about;
// nil when the land may go on.
func (r *Reconciler) landFoldOwesReview(ctx context.Context, tick, base, baseHead, folded string) (*Refusal,
	error) {
	rounds, err := r.readReviewRounds()
	if err != nil || rounds.final == nil || reviewVerdictOf(rounds.final.Response) != subprocess.ReviewVerdictReady {
		return nil, err
	}
	judged, _ := rounds.final.Request["source_sha"].(string)
	if strings.TrimSpace(judged) == "" || judged == folded {
		return nil, nil
	}
	closeouts, err := r.closeoutTicks(ctx)
	if err != nil {
		return nil, err
	}
	// The fold may carry a base newer than the head the pass read: the base
	// as origin has it now tells every fold apart.
	if now := r.baseHeadForFolds(); now != "" {
		baseHead = now
	}
	about, err := r.foldsTouchingEpic(ctx, judged, folded, baseHead, closeouts)
	if err != nil || len(about) == 0 {
		return nil, err
	}
	return r.refuse(RefusedLandFoldReview, tick,
		"the run does not merge the epic %s yet: the fold of %s (at %s) into %s brought in %s, which the epic is "+
			"about (its own diff or its acceptance names it), after its READY final review (decision %d%s) judged "+
			"%s. The next incarnation reviews the folded tree before it lands; nobody has anything to decide",
		r.opts.EpicID, base, short(baseHead), r.branch, strings.Join(about, ", "), rounds.final.Decision,
		inheritedFrom(rounds), short(judged)), nil
}

// graphAller is a tracker that can list an epic's CLOSED tasks too: tk's
// `graph --all` (tk.Client.GraphAll), which the durable tracker passes on.
type graphAller interface {
	GraphAll(ctx context.Context, epicID string) (tk.Graph, error)
}

// graphWithClosed is the epic's graph with its closed tasks where the
// tracker can list them, and its graph as it answers otherwise.
func (r *Reconciler) graphWithClosed(ctx context.Context) (tk.Graph, error) {
	if all, ok := r.tracker.(graphAller); ok {
		return all.GraphAll(ctx, r.opts.EpicID)
	}
	return r.tracker.Graph(ctx, r.opts.EpicID)
}

// changedOutsideBookkeeping is treeChangedSinceReview's history read: one git
// log over the first-parent line from `from` to `to`, each commit's paths
// against its first parent, the close-out's merges and folds of the base
// (second parent on `baseHead`) left out.
func (r *Reconciler) changedOutsideBookkeeping(from, to, baseHead string, closeouts []string) bool {
	const commitSep, bodySep = "\x1e", "\x1f"
	out, err := r.git.run("", "log", "--first-parent", "--diff-merges=first-parent", "--name-only",
		"--format=%x1e%P%n%B%x1f", from+".."+to)
	if err != nil {
		return true
	}
	for _, entry := range strings.Split(out, commitSep) {
		head, paths, ok := strings.Cut(entry, bodySep)
		if !ok {
			continue
		}
		parentLine, body, _ := strings.Cut(head, "\n")
		if isCloseoutMerge(body, closeouts) {
			continue
		}
		if parents := strings.Fields(parentLine); len(parents) > 1 && baseHead != "" &&
			r.git.contains(parents[1], baseHead) {
			continue
		}
		for _, path := range strings.Split(paths, "\n") {
			if path = strings.TrimSpace(path); path != "" && !underReviewBookkeeping(path) {
				return true
			}
		}
	}
	return false
}

// isCloseoutMerge says whether a commit message is the integration merge of
// one of the epic's close-out ticks.
func isCloseoutMerge(message string, closeouts []string) bool {
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ticfac run ") {
			continue
		}
		for _, id := range closeouts {
			if strings.Contains(line, ": tick "+id+" attempt ") {
				return true
			}
		}
	}
	return false
}

// A READY review is about the tree it judged too (after epic-hn6, 2026-10-06).
//
// WHAT WAS WRONG. hn6's branch closed three ticks (lfm, m4t, gzv) hours after
// its final review answered, and nothing reviewed them. Had that review said
// READY, the run would have merged them under it: the land asked "is the
// final review READY?", never "is this still the tree it judged?" — the
// question the NOT READY side above learned to ask.
//
// THE RULE. Before the run lands, a READY final review whose tree has changed
// since (treeChangedSinceReview: the same reading, the close-out's own merge
// excepted, so the normal path — review, close-out, land — is never a change)
// is followed by ONE more review of the tree as it stands, and the land waits
// for its verdict: READY lands, NOT READY is the final review's NOT READY and
// follows the rules above. Every round is gated the same way, so it
// terminates: a review over a tree unchanged since it lands or holds.
//
// It is asked where the run can still work a new tick: where a WORK tick
// closes (runPlan's window, before its replan) — so a review of work that
// landed after a READY runs before the close-out, and the close-out's retro
// and the epic PR are about a reviewed tree — where a run is resumed (Run,
// beside answerNotReadyReview), and, as the backstop for a change nothing in
// the run saw (a commit pushed from outside it), where a run's plan has
// drained, just before the readying (Run's work-the-plan loop).

// reviewOverChangedTree places one more review when the final review's tree
// has changed and nothing else will review it: a READY final review, or a
// NOT READY one past the bound (below the bound the absorption owns the next
// round). It reports whether a review was placed, so the caller re-reads the
// graph and works it.
func (r *Reconciler) reviewOverChangedTree(ctx context.Context) (bool, error) {
	if r.store == nil || r.opts.notReadyForAPerson {
		return false, nil
	}
	durable, ok := r.tracker.(*durableTracker)
	if !ok {
		return false, nil
	}
	rounds, err := r.readReviewRounds()
	if err != nil || rounds.final == nil {
		return false, err
	}
	switch reviewVerdictOf(rounds.final.Response) {
	case subprocess.ReviewVerdictReady:
	case subprocess.ReviewVerdictNotReady:
		if !rounds.spent() {
			return false, nil
		}
	default:
		return false, nil
	}
	return r.reviewIfTreeChanged(ctx, durable, rounds)
}

// reviewIfTreeChanged is the gate both verdicts share: one more review only
// when the tree changed since the final review judged it.
func (r *Reconciler) reviewIfTreeChanged(ctx context.Context, durable *durableTracker, rounds reviewRounds) (bool, error) {
	final := *rounds.final
	reviewed, _ := final.Request["tick_id"].(string)
	head, folded, changed, err := r.treeChangedSinceReview(ctx, final)
	if err != nil || !changed {
		return false, err
	}
	// A run with no close-out of its own has nothing to land the new review's
	// READY with (epic ilz: the close-out was an earlier run's), so the
	// epic's closed close-out is reopened behind the review. Asked before the
	// step, because the step holds the store's writes.
	co, err := r.closeoutForLanding()
	if err != nil {
		return false, err
	}
	reopen := co == nil
	acted := false
	err = r.heldStep(func() error {
		var stepErr error
		acted, stepErr = r.placeChangedTreeReview(ctx, durable, rounds, final, reviewed, head, folded, reopen)
		return stepErr
	})
	return acted, err
}

// closeoutBehindReview places the epic's close-out behind a review the run
// just placed: the open one, or — when reopen is set and the close-out is
// closed — the closed one, reopened first, so the land runs a close-out of
// this run's own after the review's verdict. It answers the close-out placed,
// "" when there is none.
func (r *Reconciler) closeoutBehindReview(ctx context.Context, durable *durableTracker, open, review string,
	reopen bool) (string, error) {
	closeout := open
	if closeout == "" && reopen {
		closed, err := r.closedCloseout(ctx)
		if err != nil {
			return "", err
		}
		if closed != "" {
			if _, err := durable.Reopen(ctx, closed); err != nil {
				return "", fmt.Errorf("reopen the close-out %s behind the review %s: %w", closed, review, err)
			}
			note := fmt.Sprintf("ticfac run %s: reopened behind the review %s — the epic %s is reviewed again over "+
				"a tree that changed since its final review, and this run has no close-out of its own to land the "+
				"new verdict with (the one that closed was an earlier run's), so the close-out runs again after it.",
				r.runID, review, r.opts.EpicID)
			if _, err := r.tracker.Note(ctx, closed, note); err != nil {
				return "", fmt.Errorf("note the reopening of %s: %w", closed, err)
			}
			closeout = closed
		}
	}
	if closeout == "" {
		return "", nil
	}
	if err := durable.BlockOn(ctx, closeout, review); err != nil {
		return "", fmt.Errorf("place the close-out %s behind the re-review %s: %w", closeout, review, err)
	}
	return closeout, nil
}

// closedCloseout is the epic's closed close-out tick, "" when it has none:
// read off the graph with its closed tasks (graphWithClosed), since `tk graph`
// alone lists the open ones.
func (r *Reconciler) closedCloseout(ctx context.Context) (string, error) {
	graph, err := r.graphWithClosed(ctx)
	if err != nil {
		return "", fmt.Errorf("read the epic graph of %s for its close-out: %w", r.opts.EpicID, err)
	}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Role == "closeout" && task.Status == "closed" {
				return task.ID, nil
			}
		}
	}
	return "", nil
}

// placeChangedTreeReview is the held step of a review over a changed tree:
// one more review of the tree as it now stands, filed under the epic (reused
// when an earlier incarnation already made it), behind every open work tick
// of the epic, and placed before the close-out when that is still open.
func (r *Reconciler) placeChangedTreeReview(ctx context.Context, durable *durableTracker, rounds reviewRounds,
	final runstate.Decision, reviewed, head string, folded []string, reopen bool) (bool, error) {
	judged, _ := final.Request["source_sha"].(string)
	saw := "with work that review never saw (a commit closed or pushed since, a person's fix among them perhaps)"
	if len(folded) > 0 {
		saw = foldedAboutEpic(folded)
	}
	rereview, closeout, err := r.openReReview(ctx, reviewed)
	if err != nil {
		return false, err
	}
	round := rounds.rounds + 1
	if reviewVerdictOf(final.Response) == subprocess.ReviewVerdictReady {
		return r.placeReviewAfterReady(ctx, durable, final, reviewed, judged, head, folded, rereview, closeout, round,
			reopen)
	}
	if rereview == "" {
		description := fmt.Sprintf(
			"Review the epic %s again, AS INTEGRATED: its final review (%s, decision %d) judged it NOT READY at "+
				"%s, after the %d review rounds the run makes by itself, and the epic branch has changed since — "+
				"now at %s, %s. This is review round %d, and it is made only because the tree changed.\n\nThat "+
				"review's reasons: %s.\n\nJudge the tree as it stands: READY when the epic does what it said it "+
				"would, NOT READY naming what still blocks it. A NOT READY here holds for a person with your "+
				"reasons unless the tree changes again.",
			r.opts.EpicID, reviewed, final.Decision, short(judged), maxReviewRounds, short(head), saw, round,
			notReadyReasons(final))
		rereview, err = r.createReReview(ctx, durable,
			fmt.Sprintf("Re-review %s: its tree changed since its NOT READY final review (round %d)", r.opts.EpicID,
				round), description)
		if err != nil {
			return false, err
		}
	}
	if err := r.placeBehindOpenWork(ctx, durable, rereview, reviewed); err != nil {
		return false, err
	}
	if _, err := r.closeoutBehindReview(ctx, durable, closeout, rereview, reopen); err != nil {
		return false, err
	}
	r.record(reviewed, StageReviewRound,
		"the final review (decision %d%s) judged %s NOT READY — %s — after %d review round(s), the bound being %d; "+
			"but the epic branch changed since the tree it judged (%s, now %s) with more than run state and "+
			"tracker records, so the review it holds on is about a tree that no longer exists. The run reviews "+
			"the tree as it stands: %s (round %d). Nothing is absorbed past the bound",
		final.Decision, inheritedFrom(rounds), r.opts.EpicID, notReadyReasons(final), rounds.rounds, maxReviewRounds,
		short(judged),
		short(head), rereview, round)
	return true, nil
}

// placeReviewAfterReady is placeChangedTreeReview for a READY final review:
// the review the land waits on.
func (r *Reconciler) placeReviewAfterReady(ctx context.Context, durable *durableTracker, final runstate.Decision,
	reviewed, judged, head string, folded []string, rereview, closeout string, round int, reopen bool) (bool, error) {
	saw := "with work that review never saw (a tick closed or a commit pushed after it)"
	changed := "with more than run state, tracker records and the close-out's merge"
	if len(folded) > 0 {
		saw, changed = foldedAboutEpic(folded), foldedAboutEpic(folded)
	}
	if rereview == "" {
		description := fmt.Sprintf(
			"Review the epic %s again, AS INTEGRATED, before the run lands it: its final review (%s, decision %d) "+
				"judged it READY at %s, and the epic branch has changed since — now at %s, %s. This is review "+
				"round %d, made only because "+
				"the tree changed; the run lands on YOUR verdict, not the earlier one.\n\nJudge the tree as it "+
				"stands: READY when the epic does what it said it would, NOT READY naming what blocks it.",
			r.opts.EpicID, reviewed, final.Decision, short(judged), short(head), saw, round)
		var err error
		rereview, err = r.createReReview(ctx, durable,
			fmt.Sprintf("Re-review %s: its tree changed since its READY final review (round %d)", r.opts.EpicID, round),
			description)
		if err != nil {
			return false, err
		}
	}
	if err := r.placeBehindOpenWork(ctx, durable, rereview, reviewed); err != nil {
		return false, err
	}
	if _, err := r.closeoutBehindReview(ctx, durable, closeout, rereview, reopen); err != nil {
		return false, err
	}
	r.record(reviewed, StageReviewRound,
		"the final review (decision %d) judged %s READY, but the epic branch changed since the tree it judged (%s, "+
			"now %s) %s: the READY is about a tree that no longer exists, so the run reviews the tree as it stands "+
			"before it lands — %s (round %d)",
		final.Decision, r.opts.EpicID, short(judged), short(head), changed, rereview, round)
	return true, nil
}

// foldedAboutEpic says what a fold the epic is about brought in, for a
// re-review's description and record.
func foldedAboutEpic(paths []string) string {
	return fmt.Sprintf("with a fold of the base that brought in %s, which the epic is about (its own diff "+
		"touches it, or its acceptance names it), so the epic is judged against the base as it now is",
		strings.Join(paths, ", "))
}

// placeBehindOpenWork blocks a review on every open work tick of the epic —
// everything but the reviews and the close-out — so it judges the tree once
// they are in it, not the tree halfway there.
func (r *Reconciler) placeBehindOpenWork(ctx context.Context, durable *durableTracker, review, reviewed string) error {
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return fmt.Errorf("read the epic graph of %s to place the re-review: %w", r.opts.EpicID, err)
	}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Status == "closed" || task.ID == review || task.ID == reviewed || task.Role == "review" ||
				task.Role == "closeout" {
				continue
			}
			if err := durable.BlockOn(ctx, review, task.ID); err != nil {
				return fmt.Errorf("place the re-review %s behind %s: %w", review, task.ID, err)
			}
		}
	}
	return nil
}

// recordReviewerAbsorption rewrites the decision record of a finding the
// NOT READY review named blocking and the run adopted into the epic: basis
// reviewer, gating, placed before the re-review, the earlier decision kept in
// the reason. A record already on the reviewer's basis is left alone.
func (r *Reconciler) recordReviewerAbsorption(record runstate.Absorption, final runstate.Decision, reviewed string) error {
	if record.Basis == runstate.AbsorptionReviewer {
		return nil
	}
	earlier := fmt.Sprintf("basis %s, placed %s: %s", record.Basis, record.Placement, record.Reason)
	record.Basis = runstate.AbsorptionReviewer
	record.Gating = true
	record.Placement = runstate.AbsorptionBeforeReview
	record.Confidence, record.Model, record.Fallback = 0, "", ""
	record.Reason = fmt.Sprintf("the final review (%s, decision %d) judged the epic NOT READY and named this "+
		"finding blocking, so it is the epic's work and is fixed before the re-review. It had been decided "+
		"before the review named it (%s)", reviewed, final.Decision, earlier)
	record.DecidedAt = r.now().UTC().Format(time.RFC3339)
	if _, err := r.store.UpdateAbsorption(record); err != nil {
		return fmt.Errorf("record the reviewer's absorption of finding %s: %w", record.Key, err)
	}
	return nil
}
