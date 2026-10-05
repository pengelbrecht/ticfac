package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
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

// reviewRounds is the run's review decisions, read for the rounds rule.
type reviewRounds struct {
	// final is the latest review-epic decision, nil when no review answered.
	final *runstate.Decision
	// rounds is how many review ticks have a recorded decision.
	rounds int
}

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
	ticks := map[string]bool{}
	for i := range decisions {
		if decisions[i].Role != "review-epic" {
			continue
		}
		if out.final == nil || decisions[i].Decision > out.final.Decision {
			out.final = &decisions[i]
		}
		tick, _ := decisions[i].Request["tick_id"].(string)
		ticks[tick] = true
	}
	out.rounds = len(ticks)
	return out, nil
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
	if rounds.rounds >= maxReviewRounds {
		// The bound is spent. Past it nothing is absorbed — the run does not
		// argue with its review — but a tree that changed since the final
		// review judged it is a tree no review has judged (epic-hn6): it is
		// reviewed once more, and only an unchanged tree holds.
		head, changed, err := r.treeChangedSinceReview(ctx, final)
		if err != nil || !changed {
			return false, err
		}
		acted := false
		err = r.heldStep(func() error {
			var stepErr error
			acted, stepErr = r.placeChangedTreeReview(ctx, durable, rounds, final, reviewed, head)
			return stepErr
		})
		return acted, err
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
				"fixed. This is review round %d of at most %d.\n\nThe previous review (%s, decision %d) judged it NOT "+
				"READY: %s.\n\nJudge the tree as it stands: READY when the epic does what it said it would, NOT READY "+
				"naming what still blocks it. A NOT READY at this round is not reviewed again by the run unless the "+
				"tree changes after it: it holds for a person with your reasons.",
			r.opts.EpicID, round, maxReviewRounds, reviewed, final.Decision, notReadyReasons(final))
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
			"and the re-review %s (round %d of %d) runs once %s closed",
		final.Decision, r.opts.EpicID, notReadyReasons(final), strings.Join(absorbed, ", "), rereview, round,
		maxReviewRounds, plural(len(absorbed), "it is", "they are"))
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
// Everything else counts: work the run closed since, a person's commit, a
// repair, and a fold of the base — the review judges the epic AS
// INTEGRATED, and code the base brought in is code it has not seen.
//
// A history that cannot be read (the judged commit gone after a force push)
// answers CHANGED: a commit the branch can no longer show is not a tree
// anybody can say the review judged, and the review that answers it records
// the head it judged, so the next comparison is readable.
func (r *Reconciler) treeChangedSinceReview(ctx context.Context, decision runstate.Decision) (string, bool, error) {
	judged, _ := decision.Request["source_sha"].(string)
	if strings.TrimSpace(judged) == "" {
		return "", false, nil
	}
	if err := r.git.fetch(r.branch); err != nil {
		return "", false, fmt.Errorf("fetch %s to compare it with the tree the final review judged: %w", r.branch, err)
	}
	head, err := r.git.remoteHead(r.branch)
	if err != nil {
		return "", false, err
	}
	if head == "" || head == judged {
		return head, false, nil
	}
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return "", false, fmt.Errorf("read the epic graph of %s for its close-out: %w", r.opts.EpicID, err)
	}
	var closeouts []string
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Role == "closeout" {
				closeouts = append(closeouts, task.ID)
			}
		}
	}
	return head, r.changedOutsideBookkeeping(judged, head, closeouts), nil
}

// changedOutsideBookkeeping is treeChangedSinceReview's history read: one git
// log over the first-parent line from `from` to `to`, each commit's paths
// against its first parent.
func (r *Reconciler) changedOutsideBookkeeping(from, to string, closeouts []string) bool {
	const commitSep, bodySep = "\x1e", "\x1f"
	out, err := r.git.run("", "log", "--first-parent", "--diff-merges=first-parent", "--name-only",
		"--format=%x1e%B%x1f", from+".."+to)
	if err != nil {
		return true
	}
	for _, entry := range strings.Split(out, commitSep) {
		body, paths, ok := strings.Cut(entry, bodySep)
		if !ok || isCloseoutMerge(body, closeouts) {
			continue
		}
		for _, path := range strings.Split(paths, "\n") {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			under := false
			for _, prefix := range reviewBookkeepingPrefixes {
				if strings.HasPrefix(path, prefix) {
					under = true
					break
				}
			}
			if !under {
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

// placeChangedTreeReview is answerNotReadyReview's held step past the bound:
// one more review of the tree as it now stands, filed under the epic (reused
// when an earlier incarnation already made it) and placed before the
// close-out when that is still open.
func (r *Reconciler) placeChangedTreeReview(ctx context.Context, durable *durableTracker, rounds reviewRounds,
	final runstate.Decision, reviewed, head string) (bool, error) {
	judged, _ := final.Request["source_sha"].(string)
	rereview, closeout, err := r.openReReview(ctx, reviewed)
	if err != nil {
		return false, err
	}
	round := rounds.rounds + 1
	if rereview == "" {
		description := fmt.Sprintf(
			"Review the epic %s again, AS INTEGRATED: its final review (%s, decision %d) judged it NOT READY at "+
				"%s, after the %d review rounds the run makes by itself, and the epic branch has changed since — "+
				"now at %s, with work that review never saw (a commit closed or pushed since, a person's fix among "+
				"them perhaps). This is review round %d, and it is made only because the tree changed.\n\nThat "+
				"review's reasons: %s.\n\nJudge the tree as it stands: READY when the epic does what it said it "+
				"would, NOT READY naming what still blocks it. A NOT READY here holds for a person with your "+
				"reasons unless the tree changes again.",
			r.opts.EpicID, reviewed, final.Decision, short(judged), maxReviewRounds, short(head), round,
			notReadyReasons(final))
		rereview, err = r.createReReview(ctx, durable,
			fmt.Sprintf("Re-review %s: its tree changed since its NOT READY final review (round %d)", r.opts.EpicID,
				round), description)
		if err != nil {
			return false, err
		}
	}
	if closeout != "" {
		if err := durable.BlockOn(ctx, closeout, rereview); err != nil {
			return false, fmt.Errorf("place the close-out %s behind the re-review %s: %w", closeout, rereview, err)
		}
	}
	r.record(reviewed, StageReviewRound,
		"the final review (decision %d) judged %s NOT READY — %s — after %d review round(s), the bound being %d; "+
			"but the epic branch changed since the tree it judged (%s, now %s) with more than run state and "+
			"tracker records, so the review it holds on is about a tree that no longer exists. The run reviews "+
			"the tree as it stands: %s (round %d). Nothing is absorbed past the bound",
		final.Decision, r.opts.EpicID, notReadyReasons(final), rounds.rounds, maxReviewRounds, short(judged),
		short(head), rereview, round)
	return true, nil
}
