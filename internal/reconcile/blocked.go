package reconcile

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A worker that stops to ask (tick tyd).
//
// Until this tick a worker whose report ended BLOCKED or NEEDS_CONTEXT over
// work it had committed made its attempt attempt_needs_human, and the run
// stopped for a person. The operator's model is that the next human touch
// point is the ready PR, not a stopped run, so the answer is now a LADDER:
//
//  1. Below the tier ceiling the tick is dispatched again ONE TIER UP, cut from
//     the stopped attempt's commits (as a carried release is), with the
//     worker's question and its report in the new prompt: "an earlier attempt
//     stopped because: …; decide and proceed; say what you decided".
//  2. At the ceiling the repository's standing orders (.tick/config.md) decide
//     who answers. A question in a decide-and-log class is dispatched once
//     more with the explicit instruction to decide it under the orders and log
//     the decision in the report.
//  3. Only a question in an always-ask class — money, credentials, a live
//     external system, removing scope, the roadmap, force-pushes, … — holds,
//     or one asked AGAIN by the worker that was told to decide it. The hold
//     names the question; the run keeps working every tick that does not wait
//     behind the held one, and the epic PR's body lists the question.
//
// Each step is a durable decision record on the run branch — the fact a
// resumed run re-derives the same answer from, and the list the PR body is
// composed from — and a feed event naming the question.
//
// A role job (review, close-out) runs outside the ladder, so it takes steps
// 2 and 3 only, the same way: its BLOCKED answer is dispatched again to be
// decided under the standing orders unless its class is always-ask.

const (
	// StageBlockedEscalated: a worker stopped to ask below the tier ceiling,
	// and the tick is dispatched again one tier up with the question.
	StageBlockedEscalated = "blocked_escalated"
	// StageBlockedDecide: at the ceiling, the question is in no always-ask
	// class, so the tick is dispatched again to decide it under the standing
	// orders and log the decision.
	StageBlockedDecide = "blocked_decide_and_log"
	// StageBlockedHeld: the question is in an always-ask class (or came back
	// from the worker told to decide it), and the tick holds for a person.
	StageBlockedHeld = "blocked_held"
	// StageWaitsBehindHeld: a tick the run does not dispatch because it is
	// sequenced behind a held question (or is a role job over the whole
	// epic); every other tick keeps going.
	StageWaitsBehindHeld = "waits_behind_held"
	// StageTickHeld: a tick whose refusal holds only itself (holdsOnlyItsTick,
	// epic hn6 run_ee8e) — the run goes on with every tick that does not wait
	// behind it, and ends naming it once nothing else can progress.
	StageTickHeld = "tick_held"

	// RefusedBlockedRedispatch is a collect that answered a stopped worker by
	// dispatching the tick again (escalate or decide). The window requeues the
	// tick in-run rather than stopping; it is resumable without a person for
	// any path where it escapes the window.
	RefusedBlockedRedispatch = "attempt_blocked_redispatched"

	blockedKind     = "blocked_answer"
	blockedEscalate = "escalate"
	blockedDecide   = "decide"
	blockedHold     = "hold"
)

// blockedAnswer is one stopped worker's answer and the step the run took.
type blockedAnswer struct {
	Decision int
	Tick     string
	Role     string
	Attempt  int
	JobID    string
	Status   string
	Question string
	Tier     string
	Step     string
	// Class is the always-ask class a held question fell in ("" for one held
	// because it came back after the decide dispatch, and for the others).
	Class string
}

// blockedAnswers reads every blocked-answer record the run has, in decision
// order.
func (r *Reconciler) blockedAnswers() ([]blockedAnswer, error) {
	if r.store == nil {
		return nil, nil
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return nil, err
	}
	var out []blockedAnswer
	for _, d := range decisions {
		if kind, _ := d.Request["kind"].(string); kind != blockedKind {
			continue
		}
		answer := blockedAnswer{Decision: d.Decision}
		answer.Tick, _ = d.Request["tick_id"].(string)
		answer.Role, _ = d.Request["tick_role"].(string)
		answer.JobID, _ = d.Request["job_id"].(string)
		answer.Status, _ = d.Request["status"].(string)
		answer.Question, _ = d.Request["question"].(string)
		answer.Tier, _ = d.Request["tier"].(string)
		answer.Step, _ = d.Response["step"].(string)
		answer.Class, _ = d.Response["class"].(string)
		answer.Attempt = decisionAttemptOf(d)
		out = append(out, answer)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Decision < out[j].Decision })
	return out, nil
}

// blockedAnswerOf is the record for one attempt of one tick.
func (r *Reconciler) blockedAnswerOf(tick string, attempt int) (blockedAnswer, bool) {
	answers, err := r.blockedAnswers()
	if err != nil {
		return blockedAnswer{}, false
	}
	for _, a := range answers {
		if a.Tick == tick && a.Attempt == attempt {
			return a, true
		}
	}
	return blockedAnswer{}, false
}

// standingOrders reads the target repository's standing orders, fresh at the
// point of use: a person may edit them between two answers.
func (r *Reconciler) standingOrders() standingOrders {
	path := r.opts.RepoConfig
	if path == "" {
		path = RepoConfigPath(r.opts.Repo)
	}
	return readStandingOrders(path)
}

// escalatesAbove says whether dispatching this tick again would climb the
// tier ladder above the tier the stopped attempt ran at. It asks the same
// derivation the dispatch will, with one more failed attempt than the stopped
// one could have had behind it: a policy with no ladder, a pinned tier, a
// label or role route, and a tier already at the ceiling all answer no.
func (r *Reconciler) escalatesAbove(ctx context.Context, entry planEntry, marker attemptHandle) (string, bool) {
	if r.tierPolicy == nil || r.pinnedTier != "" || marker.Tier == "" || isRoleJob(entry.Role) {
		return "", false
	}
	classification, err := r.classificationFor(ctx, entry, false)
	if err != nil {
		return "", false
	}
	try := marker.Try
	if try < 1 {
		try = 1
	}
	number := marker.Attempt + 1
	if number < try+1 {
		number = try + 1
	}
	next, _, err := r.deriveTier(entry, number, try, classification)
	if err != nil {
		return "", false
	}
	if tierIndexOf(runconfig.Tier(next)) > tierIndexOf(runconfig.Tier(marker.Tier)) {
		return next, true
	}
	return "", false
}

// answerBlocked is the run's answer to a worker that stopped to ask, for an
// implement attempt or a role job. It decides the step, records it durably and
// on the feed, and returns the refusal the caller raises: RefusedBlockedRedispatch
// for the two steps that dispatch the tick again, and holdReason for a hold.
func (r *Reconciler) answerBlocked(ctx context.Context, entry planEntry, marker attemptHandle,
	answer *subprocess.RoleResult, holdReason string) *Refusal {

	tick := marker.TickID
	question := strings.TrimSpace(answer.Summary)
	if question == "" {
		question = "(the worker gave no reason)"
	}
	name := r.attemptName(tick, marker.Attempt)

	// A worker that was already told to decide under the standing orders and
	// asked again is not dispatched a third time: its question holds.
	decidedBefore := false
	if prior, err := r.blockedAnswers(); err == nil {
		for _, a := range prior {
			if a.Tick == tick && a.Attempt < marker.Attempt && a.Step == blockedDecide {
				decidedBefore = true
			}
		}
	}

	step, class, next := blockedHold, "", ""
	orders := r.standingOrders()
	if tier, up := r.escalatesAbove(ctx, entry, marker); up {
		step, next = blockedEscalate, tier
	} else if class = orders.alwaysAskClass(question); class == "" && !decidedBefore {
		step = blockedDecide
	}

	if err := r.recordBlockedAnswer(marker, answer.Status, question, step, class); err != nil {
		// The record is what a resume re-derives the step from and what the
		// PR body lists; without it the run cannot carry the work forward
		// honestly, so it holds, naming the question, as it always did.
		r.record(tick, StageRejected, "the answer of %s could not be recorded (%v): it holds", name, err)
		step = blockedHold
	}

	switch step {
	case blockedEscalate:
		r.record(tick, StageBlockedEscalated,
			"%s answered %s: %q. It is dispatched again one tier up (%s → %s), from its own commits, with the "+
				"question in the prompt: decide and proceed", name, answer.Status, question, marker.Tier, next)
		return r.refuse(RefusedBlockedRedispatch, tick,
			"%s answered %s (%s); the tick is dispatched again at tier %s with the question", name, answer.Status,
			question, next)
	case blockedDecide:
		r.record(tick, StageBlockedDecide,
			"%s answered %s at the top of the tier ladder: %q. No always-ask class of the standing orders covers it, "+
				"so the tick is dispatched again, from its commits, to decide it under the standing orders and log the "+
				"decision in its report", name, answer.Status, question)
		return r.refuse(RefusedBlockedRedispatch, tick,
			"%s answered %s (%s); the tick is dispatched again to decide it under the standing orders", name,
			answer.Status, question)
	}
	why := "it came back from the worker that was told to decide it under the standing orders"
	if class != "" {
		why = fmt.Sprintf("it is in the always-ask class %q of the standing orders", class)
	}
	r.record(tick, StageBlockedHeld, "%s answered %s: %q. It holds for a person: %s", name, answer.Status, question, why)
	return r.refuse(holdReason, tick,
		"%s answered %s: %s. The question holds for a person because %s. Its work is on %s and is NOT merged, and "+
			"the tick is NOT closed; the run keeps working every tick that does not wait behind it, and the epic PR "+
			"lists the question. Answer it on the tick, then release the attempt with `ticfac settle %s %s %d "+
			"--release \"<who>\" --carry-work` and run the epic again",
		name, answer.Status, question, why, branchOf(marker.WriteRef), r.opts.EpicID, tick, marker.Attempt)
}

// recordBlockedAnswer lands one stopped worker's answer and the run's step as
// a decision record. Create-if-absent per attempt: a resume re-reads it.
func (r *Reconciler) recordBlockedAnswer(marker attemptHandle, status, question, step, class string) error {
	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	decisions, err := r.store.Decisions()
	if err != nil {
		return err
	}
	number := len(decisions) + 1
	for _, existing := range decisions {
		if kind, _ := existing.Request["kind"].(string); kind == blockedKind &&
			existing.Request["tick_id"] == marker.TickID && existing.Request["job_id"] == marker.JobID {
			return nil
		}
		if existing.Decision >= number {
			number = existing.Decision + 1
		}
	}
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		return err
	}
	role := marker.Role
	if role == "" {
		role = "implement-tick"
	}
	stamp := r.now().UTC().Format(time.RFC3339)
	_, err = r.store.PutDecision(runstate.Decision{
		Decision: number,
		// The record's role is the implement role whatever the job was: a
		// role job's own decisions are what its resume closes behind
		// (recordedRoleDecision), and this record is not its answer.
		Role: "implement-tick",
		Request: map[string]any{
			"kind": blockedKind, "tick_id": marker.TickID, "tick_role": role, "attempt": marker.Attempt,
			"job_id": marker.JobID, "status": status, "question": question, "tier": marker.Tier,
		},
		Response:    map[string]any{"step": step, "class": class},
		Validated:   true,
		RequestedAt: stamp,
		AnsweredAt:  stamp,
		Provenance:  r.attemptProvenance(dispatch),
	})
	if err != nil {
		return fmt.Errorf("record the blocked answer of %s: %w", r.attemptName(marker.TickID, marker.Attempt), err)
	}
	return nil
}

// escalationFor is what a dispatch tells its worker about the earlier attempt
// of the tick that stopped to ask (the newest one of this run), or nil when the
// newest earlier attempt did not stop to ask. It is re-derived from the
// attempt's archived report and the run's blocked-answer record, like the
// prior reports, so a dispatch rebuilt for an adoption renders the same
// section.
func (r *Reconciler) escalationFor(tick string, number int, tier string) *subprocess.Escalation {
	var latest *subprocess.PriorReport
	for _, prior := range r.priorReports(tick, number) {
		if prior.Run != "" {
			continue
		}
		if latest == nil || prior.Attempt > latest.Attempt {
			p := prior
			latest = &p
		}
	}
	var recorded *blockedAnswer
	if answers, err := r.blockedAnswers(); err == nil {
		for i := range answers {
			if answers[i].Tick == tick && answers[i].Attempt < number &&
				(recorded == nil || answers[i].Attempt > recorded.Attempt) {
				recorded = &answers[i]
			}
		}
	}
	e := &subprocess.Escalation{Tier: tier}
	switch {
	case recorded != nil && (latest == nil || recorded.Attempt >= latest.Attempt):
		if recorded.Step == blockedHold {
			// A person released a held question; the release, not this
			// section, is what the next worker starts from.
			return nil
		}
		e.Attempt, e.Status, e.Question = recorded.Attempt, recorded.Status, recorded.Question
		if latest != nil && latest.Attempt == recorded.Attempt {
			e.Report = latest.Path
		}
		e.AtCeiling = recorded.Step == blockedDecide
	case latest != nil && needsHuman(latest.Status):
		// A worker that stopped to ask with nothing committed: its tick is
		// redispatched by the ladder already (collect_failed); the prompt
		// still carries the question and the instruction.
		e.Attempt, e.Status, e.Question, e.Report = latest.Attempt, latest.Status, latest.Detail, latest.Path
		e.AtCeiling = r.atCeiling(tier)
	default:
		return nil
	}
	if e.AtCeiling {
		e.StandingOrders = r.standingOrders().Text
	}
	return e
}

// atCeiling says a dispatch at this tier is at the top of the ladder: the run
// has no ladder, or the tier is the policy's ceiling.
func (r *Reconciler) atCeiling(tier string) bool {
	if r.tierPolicy == nil || tier == "" || r.pinnedTier != "" {
		return true
	}
	return tierIndexOf(runconfig.Tier(tier)) >= tierIndexOf(r.tierPolicy.CeilingOrDefault())
}

// blockedHoldsSection is the epic PR body's account of every worker that
// stopped to ask: the questions that hold for a person first, then the ones
// the run escalated or had decided under the standing orders. Empty when no
// worker stopped to ask.
func (r *Reconciler) blockedHoldsSection() string {
	answers, err := r.blockedAnswers()
	if err != nil || len(answers) == 0 {
		return ""
	}
	var held, handled []blockedAnswer
	for _, a := range answers {
		if a.Step == blockedHold {
			held = append(held, a)
		} else {
			handled = append(handled, a)
		}
	}
	var b strings.Builder
	if len(held) > 0 {
		b.WriteString("\n## Questions held for you\n\n")
		b.WriteString("A worker stopped to ask, and the repository's standing orders reserve the answer for a person. " +
			"These ticks are not merged; every other tick of the run went on.\n\n")
		for _, a := range held {
			class := "asked again after being told to decide it"
			if a.Class != "" {
				class = "always ask: " + a.Class
			}
			fmt.Fprintf(&b, "- **%s** (attempt %d, %s): %s — _%s_\n", a.Tick, a.Attempt, a.Status, a.Question, class)
		}
	}
	if len(handled) > 0 {
		b.WriteString("\n## Questions workers asked, and how the run answered\n\n")
		for _, a := range handled {
			how := "dispatched again one tier up"
			if a.Step == blockedDecide {
				how = "decided under the standing orders by the next attempt; its report logs the decision"
			}
			fmt.Fprintf(&b, "- **%s** (attempt %d, %s): %s — %s\n", a.Tick, a.Attempt, a.Status, a.Question, how)
		}
	}
	return b.String()
}

// blockedWorkHead is the head of the work a stopped attempt left — its own
// commits on origin or in this checkout, or the carried work it delivered —
// and "" when it left none (a review's answer has no branch to carry).
func (r *Reconciler) blockedWorkHead(marker attemptHandle) string {
	if head, err := r.remoteWork(branchOf(marker.WriteRef), marker.BaseSHA); err == nil && head != "" {
		return head
	}
	if head := r.attemptWorkHead(marker); head != "" {
		return head
	}
	return r.carriedDelivery(marker)
}

// blockedWorkIntegrated says the stopped attempt's work is already on the
// integration branch — a person merged it — so the ordinary disposition
// finishes it from there rather than the question dispatching another try.
func (r *Reconciler) blockedWorkIntegrated(marker attemptHandle) bool {
	head := r.blockedWorkHead(marker)
	return head != "" && r.integrated(head)
}

// heldQuestion is the resume's hold for an attempt whose question the run
// held (tick tyd): the question and the class that reserves it for a person,
// said again, rather than the anonymous rejected-work hold. Nil when the
// attempt is not a held question, or left no work to hold.
func (r *Reconciler) heldQuestion(entry planEntry, attempts []runstate.Attempt, existing runstate.Attempt,
	marker attemptHandle, nothingAdoptable bool) *Refusal {

	tick := existing.TickID
	blocked, ok := r.blockedAnswerOf(tick, existing.Attempt)
	if !ok || blocked.Step != blockedHold || !nothingAdoptable || r.tickState(tick) != "rejected" {
		return nil
	}
	head := r.blockedWorkHead(marker)
	if head != "" && r.integrated(head) {
		// Work somebody has since merged: the ordinary disposition finishes
		// it from the branch.
		return nil
	}
	if head == "" && isRoleJob(entry.Role) {
		// A role job's answer with nothing committed is re-asked by the
		// ordinary disposition, as a review's always was.
		return nil
	}
	if closeoutDecidesItself(entry.Role, blocked) {
		// A close-out's question in no always-ask class (epic-6in v7z): it
		// asked again after being told to decide, and the run decides it —
		// the ordinary disposition carries its work into one more try, under
		// the rejected-work bound. Only an always-ask question is a person's.
		return nil
	}
	label := attemptLabel(tick, tryOf(attempts, tick, existing.Attempt), existing.Attempt)
	r.setAttempt(tick, existing.Attempt)
	r.tearDownSettled(marker, label+" holds a question for a person", head != "")
	reason := RefusedNeedsHuman
	if isRoleJob(entry.Role) {
		reason = RefusedRoleAnswer
	}
	why := "it came back from the worker that was told to decide it under the standing orders"
	if blocked.Class != "" {
		why = fmt.Sprintf("it is in the always-ask class %q of the standing orders", blocked.Class)
	}
	if head == "" {
		// Nothing committed (tick tyd's follow-up): still the person's
		// question — a redispatch would only ask it again.
		return r.refuse(reason, tick,
			"%s answered %s: %s. The question still holds for a person because %s; it committed nothing. "+
				"Answer it on the tick, then release the attempt with `ticfac settle %s %s %d --release \"<who>\"` "+
				"and run the epic again",
			label, blocked.Status, blocked.Question, why, r.opts.EpicID, tick, existing.Attempt)
	}
	return r.refuse(reason, tick,
		"%s answered %s: %s. The question still holds for a person because %s; its work is on %s (%s) and is "+
			"NOT merged. Answer it on the tick, then release the attempt with `ticfac settle %s %s %d --release "+
			"\"<who>\" --carry-work` and run the epic again",
		label, blocked.Status, blocked.Question, why, branchOf(marker.WriteRef), short(head), r.opts.EpicID, tick,
		existing.Attempt)
}

// closeoutDecidesItself says a close-out's held question is the run's to
// decide rather than a person's: it is in no always-ask class of the standing
// orders (it held only because it came back from the try told to decide it).
// The close-out is the epic's last step, and holding it for a question the
// standing orders reserve for nobody leaves a finished epic waiting on a
// person with nothing to decide (epic-6in v7z).
func closeoutDecidesItself(role string, blocked blockedAnswer) bool {
	return role == "closeout-epic" && blocked.Class == ""
}
