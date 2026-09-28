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
//     bare "NOT READY".
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
// the review still says is missing.
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
// NOT READY, when the rounds are spent (the land hold names the reasons), or
// when no blocking finding is this run's to fix.
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
		return false, nil
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
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return false, fmt.Errorf("read the epic graph of %s to place the re-review: %w", r.opts.EpicID, err)
	}
	rereview, closeout := "", ""
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
	round := rounds.rounds + 1
	if rereview == "" {
		id, err := durable.MintTickID()
		if err != nil {
			return false, err
		}
		at := r.now().UTC().Format(time.RFC3339)
		description := fmt.Sprintf(
			"Review the epic %s again, AS INTEGRATED, now that the blocking findings of its NOT READY review are "+
				"fixed. This is review round %d of at most %d.\n\nThe previous review (%s, decision %d) judged it NOT "+
				"READY: %s.\n\nJudge the tree as it stands: READY when the epic does what it said it would, NOT READY "+
				"naming what still blocks it. A NOT READY at this round is not reviewed again by the run: it holds "+
				"for a person with your reasons.",
			r.opts.EpicID, round, maxReviewRounds, reviewed, final.Decision, notReadyReasons(final))
		created, err := durable.CreateTick(ctx, tk.Tick{
			ID:          id,
			Title:       fmt.Sprintf("Re-review %s after its NOT READY review's blocking findings (round %d)", r.opts.EpicID, round),
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
			return false, fmt.Errorf("create the re-review of %s: %w", r.opts.EpicID, err)
		}
		rereview = created.ID
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
