package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The absorption itself (tick npq): the step that today only a person can
// take, taken by the run — a finding judged GATING against the epic's own
// definition of done is promoted into the RUNNING epic as a tick, placed so
// it is fixed before the items it gates are asserted, with nobody triaging.
//
// The mechanism was already built: the durable finding drafts (7vn), the
// close-out hold (aqm), and the plan's admission of a tick created mid-run
// (3h0). What this file adds is the DECISION DRIVER and the RECORDING that
// makes the decision legitimate:
//
//   - the verdict is the absorption POLICY's (operator decision 2026-10-06,
//     absorb_policy.go): absorbed only on an explicit basis — the final
//     reviewer names the finding blocking, or its reporter rates it high and
//     names the done item it breaks while the epic's work is under way — and
//     backlogged otherwise. No classifier is asked: the two-tier verdict
//     that drove this before (pzp's oracle, bse's Jev predictor, an
//     absorb-anyway fallback) grew epic hn6 from 13 ticks to 60+ on
//     predictions with no measured signal.
//
//   - the decision is a RECORD on the run branch — item id, verdict, and
//     its basis (reviewer, worker-asserted-high or backlog-default) —
//     because absorbing changes the epic's shape mid-run and Axiom 1 says a
//     cold reconstruction from git must reach the SAME epic: a re-derivation
//     that reaches a smaller epic than the warm run is the failure this tick
//     exists to prevent. The record is also the promotion's idempotency
//     marker: written create-if-absent BEFORE the tick is created, so an
//     incarnation killed between the two is resumed by the record.
//
//   - the placement is an EDGE, not a hope: the absorbed tick is placed so
//     the final review — where the items are asserted — runs after it. The
//     fix lands before the review when the review has not run yet; when it
//     has (the finding is the review's own discovery, or a late one), the
//     placement is before the close-out, whose gate refuses to start while a
//     child of the epic is open. A fix that lands after the review it
//     invalidates has not been absorbed, it has been appended — the review
//     itself is the one half this file does NOT re-buy (see the finding noted
//     on placement below).
//
// WHAT STAYS: a finding the policy does not absorb is still a backlog tick
// with an owner, and it is still reported in the epic PR — unattended means
// nobody has to be there, not that nobody is ever told. A finding against an
// epic whose acceptance carries no [A<n>] items (the refusal: klq) is no
// longer left for a person either (epic-6in): the policy decides it — backlog,
// unless its reporter rates it high and claims it breaks the build or CI
// (prose.go). A finding routed to
// ANOTHER repository is no longer among them: it is filed in the target's
// tracker or backlogged here naming it, and gates nothing (routed.go).

// findingDecision is what one decision did, for the caller that reports it:
// the tick the promotion created when the run decided, and why the finding
// stays a person's when it did not.
type findingDecision struct {
	// TickID is the tick the promotion created — a child of the running epic
	// for a gating finding, a backlog tick with an owner for a non-gating one.
	TickID string
	// Backlog says the verdict was NOT GATING: the tick is a backlog tick, not
	// part of the running epic.
	Backlog bool
	// Left is why the run did not decide — the epic's acceptance is prose and
	// the refusal owns the decision, or a routed finding's filing failed
	// transiently and the close-out files it. Empty when the run decided.
	Left string
	// FixedAs is the integration-branch commit the run applied the finding's
	// own tracker edit at (tracker_edits.go), and Edit names that edit: the
	// finding was fixed by the run, not absorbed. Empty otherwise.
	FixedAs string
	Edit    string
	// Protected names the protected change the finding carries, which the
	// run applies at the close-out's close (protected_changes.go): no tick
	// was created for it. Empty otherwise.
	Protected string
}

// findingLeftNote is what the tick whose attempt discovered the finding says
// about where the finding went: the triage pointer when the run left it for a
// person (the 7vn note, its command moved to `ticfac triage` by tick 8yn),
// and the tick the run promoted it to when the decision was the run's —
// because a person reading the tracker, not only the run state, needs to see
// which finding became whose work.
func (r *Reconciler) findingLeftNote(marker attemptHandle, finding subprocess.Finding, key string, decided findingDecision) string {
	if decided.FixedAs != "" {
		return fmt.Sprintf("ticfac run %s: %s reported a finding whose fix is a tracker edit it carried — %s %q "+
			"(key %s, severity %s). The run applied the edit of the %s itself, onto %s at %s, and the draft is "+
			"triaged as fixed by that commit.",
			r.runID, r.attemptName(marker.TickID, marker.Attempt), finding.Kind, finding.Title, key,
			finding.Severity, decided.Edit, r.branch, short(decided.FixedAs))
	}
	if decided.Protected != "" {
		return fmt.Sprintf("ticfac run %s: %s reported a finding whose fix is %s, a file no worker may write — "+
			"%s %q (key %s, severity %s). No tick is created for it: the run applies the change itself onto %s "+
			"after the close-out's reads, as a labelled commit, and the epic PR lists it under protected changes "+
			"for the merger.",
			r.runID, r.attemptName(marker.TickID, marker.Attempt), decided.Protected, finding.Kind, finding.Title,
			key, finding.Severity, r.branch)
	}
	if decided.TickID != "" {
		what := fmt.Sprintf("absorbed into the running epic as tick %s, with nobody triaging", decided.TickID)
		if decided.Backlog {
			what = fmt.Sprintf("promoted to a backlog tick with an owner, %s", decided.TickID)
		}
		if strings.Contains(decided.TickID, ":") {
			what = fmt.Sprintf("filed in the tracker of the repository it is routed to, as %s", decided.TickID)
		}
		return fmt.Sprintf("ticfac run %s: %s reported a finding the run itself decided — %s %q "+
			"(key %s, severity %s, for %s) is %s; the decision record is on the run branch at "+
			".ticfac/runs/%s/absorptions/%s.json, and the draft is triaged as promoted.",
			r.runID, r.attemptName(marker.TickID, marker.Attempt), finding.Kind, finding.Title, key,
			finding.Severity, targetName(finding.Target), what, r.runID, key)
	}
	return fmt.Sprintf("ticfac run %s: %s reported a finding drafted for triage — %s %q "+
		"(key %s, severity %s, for %s). %s; the tick closes and the finding rides to the close-out, "+
		"which does not hand over while it is untriaged.",
		r.runID, r.attemptName(marker.TickID, marker.Attempt), finding.Kind, finding.Title, key,
		finding.Severity, targetName(finding.Target), triagePointer(r.opts.EpicID, r.runID))
}

// decideFinding takes one drafted finding through the absorption decision. It
// is idempotent across incarnations: a finding already decided — by this run,
// by a person, or by an earlier incarnation of this run — is left alone, and
// a decision recorded but half-finished (the kill between the record and the
// tick) is finished behind the record the kill left.
func (r *Reconciler) decideFinding(ctx context.Context, marker attemptHandle, key string, dispatch Dispatch) (findingDecision, error) {
	// The standing draft, from ORIGIN: a decision is never made twice, and a
	// person's decision stands over the run's — the triage a person made
	// between two incarnations is exactly the precedence the funnel keeps.
	if _, err := r.store.Fetch(); err != nil {
		return findingDecision{}, err
	}
	standing, ok, err := r.store.Finding(key)
	if err != nil || !ok {
		return findingDecision{}, fmt.Errorf("read the drafted finding %s to decide it: %v %v", key, ok, err)
	}
	if standing.Status != runstate.FindingProposed {
		// Decided — by a person, or by an earlier incarnation of this run.
		// The decision stands, whatever this incarnation would say.
		return findingDecision{}, nil
	}

	// The decision already recorded: an earlier incarnation decided this
	// finding and was killed before finishing. The record IS the decision —
	// it names the tick and carries the verdict — and what remains is to
	// finish behind it: create the tick if the kill came first, place it,
	// complete the triage. A cold incarnation reaches the same epic as the
	// warm one by reading this record, which is the whole of its existence.
	//
	// THIS CHECK IS FIRST, before the target, evidence-table and acceptance
	// gates below (finding d6356432, tick wz0): a restart mid-absorption must
	// finish behind the recorded decision rather than re-decide it
	// differently, and the gates below are INPUTS to a fresh decision this
	// one has already made — an acceptance that became prose or malformed, a
	// runners.toml that stopped loading, between the warm and the cold
	// incarnation, is a fact about the NEXT decision, never a way to strand a
	// recorded one. A record that exists with no tick is the re-derivation
	// hole the record exists to close.
	if recorded, ok, err := r.store.Absorption(key); err != nil {
		return findingDecision{}, err
	} else if ok {
		return r.finishAbsorption(ctx, marker, *standing, *recorded)
	}

	// A finding routed to ANOTHER repository is not this run's to absorb —
	// the fix lives in a tree this run does not build — and it is not a
	// person's to wait for either (the epic-2jn close-out stall): the run
	// files it in the target's own tracker when the repository allows it, and
	// backlogs it here naming the target otherwise. It gates nothing here,
	// whatever it claims (routed.go).
	if standing.Target != "" && !r.isThisRepository(standing.Target) {
		return r.decideRoutedFinding(ctx, marker, *standing, dispatch, false)
	}

	// A finding whose remedy is a LIVE RUN — run another epic, demonstrate
	// the product on a real substrate — is not a worker's tick whatever done
	// item it claims (liverun.go): no worker inside this epic can run an
	// epic. It becomes a backlog tick outside the epic, for the next epic run
	// to satisfy, and gates nothing here.
	if needsLiveRun(*standing) {
		return r.decideLiveRunFinding(ctx, marker, *standing, dispatch)
	}

	// A finding whose deliverable is an edit of a file the worker boundary
	// refuses is never a worker's tick either (epic-v5t's yck,
	// protected_changes.go): carrying the change, it is applied by the run at
	// the close-out's close; naming it only in prose, it is a backlog tick
	// outside the epic. Neither gates this epic's done.
	if change, ok := standing.ProtectedChange(); ok {
		return r.holdProtectedChange(marker, *standing, change), nil
	}
	if paths := protectedDeliverable(standing.Title, standing.Body, r.onCloud()); len(paths) > 0 {
		return r.decideProtectedEditFinding(ctx, marker, *standing, dispatch, paths)
	}

	// The epic's own definition of done, decided once: [A<n>] items resolved
	// against the [evidence.acceptance] table of the repository's own
	// runners.toml. The three outcomes of Decide are all taken: an enumerated
	// done is absorbed against; a PROSE acceptance is the refusal's to answer
	// for, and this run refuses to absorb rather than guessing where the
	// refusal refused; a MALFORMED acceptance is a person's repair to the
	// epic's own text, and the run stops naming it rather than deciding past
	// a shape it cannot read.
	epic, err := r.tracker.Show(ctx, r.opts.EpicID)
	if err != nil {
		return findingDecision{}, fmt.Errorf("read the epic %s to decide the finding %s against its done: %w",
			r.opts.EpicID, key, err)
	}
	evidence, _, err := r.evidenceTable()
	if err != nil {
		return findingDecision{}, err
	}
	done, refusal, err := acceptance.Decide(epic.AcceptanceCriteria, evidence)
	if err != nil {
		return findingDecision{}, fmt.Errorf("decide the absorption of finding %s: the epic %s carries an "+
			"acceptance this run cannot read, and a person must fix the epic's own text: %w", key, r.opts.EpicID, err)
	}
	prose := ""
	if refusal != nil {
		// PROSE IS NOT A PERSON'S DECISION (epic-6in, 2026-09-28): 6in's
		// fifteen findings were each "left for a person" here, and the close-out
		// then held the run for them. With no [A<n>] item there is no item a
		// finding can name, so the policy decides it as any other: the
		// backlog, unless the reporter rates it high and claims it breaks the
		// build or CI, which breaks any done (prose.go).
		if r.opts.proseFindingsForAPerson {
			r.record(marker.TickID, StageAbsorptionRefused,
				"finding %s is left for a person: %s", key, refusal.Reason)
			return findingDecision{Left: refusal.Reason}, nil
		}
		prose = refusal.Reason
	}

	// THE POLICY (operator decision 2026-10-06, absorb_policy.go): a finding
	// enters the running epic only on an explicit basis — the reviewer's
	// blocking verdict (review_rounds.go), or a HIGH-severity finding whose
	// reporter names the done item it breaks, while the epic's work is still
	// under way. No classifier is asked: Jev's prediction measured no signal
	// on our own outcomes, and hn6 grew from 13 planned ticks to 60+ on it.
	verdict, err := r.policyVerdict(ctx, *standing, done, prose)
	if err != nil {
		return findingDecision{}, err
	}

	// THE RECURSION'S BOUND (tick qjj): an absorption is about to make this
	// finding the next link of a chain, and the chain is bounded. The check
	// sits AFTER the verdict, never before it, because only an absorbed
	// finding extends the recursion — a backlogged one stops the chain where
	// it stands. A chain already at the bound DEFERS the finding — a backlog
	// tick with an owner, named in the epic PR — and the run carries on: it
	// never halts for a person over the bound (absorb_bound.go).
	if verdict.Gating {
		links, bound, exceeded, err := r.boundedChain(*standing, dispatch)
		if err != nil {
			return findingDecision{}, err
		}
		if exceeded {
			// The verdict the bound overrode, in the deferral's own words: the
			// past-bound record carries WHAT the finding claimed to gate, and
			// the bound still wins (deferPastBound, absorb_bound.go).
			overridden := runstate.Absorption{Gating: true, ItemID: verdict.ItemID, Basis: verdict.Basis}
			return r.deferPastBound(ctx, marker, *standing, dispatch, links, bound,
				fmt.Sprintf("%s (basis %s)", verdictLine(overridden), verdict.Basis))
		}
	}

	// The tick the promotion will create, minted before the record that names
	// it: the record is what a killed incarnation is resumed by.
	tickID, err := r.mintTickID()
	if err != nil {
		return findingDecision{}, err
	}

	// The decision record, on the run branch, BEFORE the tick exists: the
	// reasoning that changed the epic's shape — or chose not to — durable,
	// keyed by the finding, with its basis explicit.
	record := runstate.Absorption{
		Key:        standing.Key,
		TickID:     tickID,
		Gating:     verdict.Gating,
		ItemID:     verdict.ItemID,
		Basis:      verdict.Basis,
		Reason:     verdict.Reason,
		Placement:  verdict.Placement,
		DecidedAt:  r.now().UTC().Format(time.RFC3339),
		Provenance: r.attemptProvenance(dispatch),
	}
	outcome, err := r.store.PutAbsorption(record)
	if err != nil {
		return findingDecision{}, err
	}
	if !outcome.EffectPermitted() {
		// Another incarnation of this run recorded this decision between the
		// read and the write. Theirs is the decision: finish behind the
		// record that stands, never re-decide — a decision is never made
		// twice, and the record a race leaves is the record the run reads.
		standing, ok, err := r.store.Absorption(key)
		if err != nil || !ok {
			return findingDecision{}, fmt.Errorf("read the absorption record a concurrent incarnation left for finding %s: %v %v",
				key, ok, err)
		}
		return r.finishAbsorption(ctx, marker, runstate.Finding{}, *standing)
	}
	return r.finishAbsorption(ctx, marker, *standing, record)
}

// finishAbsorption performs what a decision record says, idempotently: create
// the tick it names if nothing has yet, place it before the review for a
// gating verdict, and complete the finding's triage as the run's promotion.
// Called on the fresh path with the record just written, and on the resume
// path with the record a killed incarnation left.
func (r *Reconciler) finishAbsorption(ctx context.Context, marker attemptHandle, standing runstate.Finding, record runstate.Absorption) (findingDecision, error) {
	// The finding's own text is what the tick is built from. On the resume
	// path the draft is the durable one on origin, re-read: the kill cannot
	// take with it what the finding said.
	if standing.Title == "" || standing.Key != record.Key {
		if _, err := r.store.Fetch(); err != nil {
			return findingDecision{}, err
		}
		if draft, ok, err := r.store.Finding(record.Key); err == nil && ok {
			standing = *draft
		} else {
			return findingDecision{}, fmt.Errorf("read the finding %s to finish its absorption: %v %v",
				record.Key, ok, err)
		}
	}

	// A finding FILED in another repository's tracker (routed.go): the tick
	// is the target's, already pushed there before this record was written,
	// so what remains here is the triage and the feed line — nothing is
	// created, placed or noted in this tracker.
	if record.Placement == runstate.AbsorptionRouted {
		if _, _, err := r.store.TriageFinding(record.Key, runstate.Triage{
			Status:     runstate.FindingPromoted,
			By:         fmt.Sprintf("ticfac run %s filing %s", r.runID, record.TickID),
			PromotedAs: record.TickID,
		}); err != nil {
			return findingDecision{}, err
		}
		r.record(marker.TickID, StageFindingRouted,
			"finding %s is filed in %s's own tracker as %s and gates nothing here: %s",
			record.Key, record.Target, record.TickID, record.Reason)
		return findingDecision{TickID: record.TickID, Backlog: true}, nil
	}

	// The tick, created if absent — the promotion's mechanism. The owner is
	// the RUN for a tick inside the epic (the run works it), and a PERSON for
	// a backlog tick (a backlog tick waits for whoever owns the epic).
	owner := r.opts.Owner
	parent := ""
	if record.Gating {
		parent = r.opts.EpicID
	}
	if !record.Gating {
		if epic, err := r.tracker.Show(ctx, r.opts.EpicID); err == nil && epic.Owner != "" {
			owner = epic.Owner
		}
	}
	tick := absorbedTickRecord(r.runID, standing, record, owner, parent)
	if _, err := r.tracker.(*durableTracker).CreateTick(ctx, tick); err != nil {
		return findingDecision{}, fmt.Errorf("create the tick %s the absorption of finding %s promoted: %w",
			record.TickID, record.Key, err)
	}

	// The placement: an absorbed tick is fixed before the items it gates are
	// asserted. The review has not run yet when the finding came from the
	// work, so the edge is the review's; after the review, the close-out's
	// own gate (3h0: it does not start while a child of the epic is open) is
	// the assertion that waits, and the record says so by its placement.
	if record.Gating && record.Placement == runstate.AbsorptionBeforeReview {
		if review, open := r.reviewTick(ctx); review != "" && open {
			if err := r.tracker.(*durableTracker).BlockOn(ctx, review, record.TickID); err != nil {
				return findingDecision{}, fmt.Errorf("place the review %s behind the absorbed tick %s: %w",
					review, record.TickID, err)
			}
		}
	}

	// The triage, attributed to the run: the decision a recorded verdict
	// drove, never a person's. A person who decided between the record and
	// here wins the race — TriageFinding reads the standing draft and refuses
	// to decide what somebody already decided.
	if _, _, err := r.store.TriageFinding(record.Key, runstate.Triage{
		Status:     runstate.FindingPromoted,
		By:         fmt.Sprintf("ticfac run %s absorbing %s", r.runID, record.TickID),
		PromotedAs: record.TickID,
	}); err != nil {
		return findingDecision{}, err
	}

	// The reasoning, in the tracker beside the tick it created: a person
	// reading the epic's children sees why this one exists without walking
	// the run branch for the record.
	note := fmt.Sprintf(
		"ticfac run %s: created by absorbing finding %s — %s. The decision record is "+
			".ticfac/runs/%s/absorptions/%s.json on %s, and it says: %s",
		r.runID, record.Key, placementLine(record), r.runID, record.Key, r.branch, record.Reason)
	if _, err := r.tracker.Note(ctx, record.TickID, note); err != nil {
		return findingDecision{}, fmt.Errorf("note the absorption of %s on the tick it created: %w", record.Key, err)
	}

	// The feed says what happened, on the tick whose attempt discovered it:
	// an absorption nobody can see is indistinguishable from a finding that
	// vanished.
	if isPastBound(record) {
		r.record(marker.TickID, StageBackloggedPastBound,
			"finding %s is deferred past the absorption bound as backlog tick %s, and the run carries on: %s",
			record.Key, record.TickID, record.Reason)
		return findingDecision{TickID: record.TickID, Backlog: true}, nil
	}
	if record.Placement == runstate.AbsorptionDeferredToReview {
		if err := r.noteDeferralOnReview(ctx, marker, standing, record); err != nil {
			return findingDecision{}, err
		}
	}
	stage, what := StageAbsorbed, "absorbed into the running epic"
	if !record.Gating {
		stage, what = StageBacklogged, "promoted to a backlog tick with an owner"
	}
	if record.Placement == runstate.AbsorptionDeferredToReview {
		what = "deferred to the final reviewer"
	}
	r.record(marker.TickID, stage,
		"finding %s is %s as tick %s: %s (basis %s%s%s), %s",
		record.Key, what, record.TickID, verdictLine(record), record.Basis, confidenceLine(record),
		modelLine(record), placementLine(record))

	return findingDecision{TickID: record.TickID, Backlog: !record.Gating}, nil
}

// placementLine is the placement as a person reads it.
func placementLine(record runstate.Absorption) string {
	switch record.Placement {
	case runstate.AbsorptionBeforeReview:
		return fmt.Sprintf("placed before the final review, which is blocked-by %s", record.TickID)
	case runstate.AbsorptionAfterReview:
		return "placed before the close-out, which does not start while it is open; the review had already run"
	case runstate.AbsorptionRouted:
		return fmt.Sprintf("filed in %s's own tracker as %s", record.Target, record.TickID)
	case runstate.AbsorptionNextRun:
		return fmt.Sprintf("a backlog tick outside the epic, labelled %s: its remedy is a live run of an epic, "+
			"for the next epic run to satisfy", liveRunLabel)
	case runstate.AbsorptionPastBound:
		return "a backlog tick with an owner, outside the epic: deferred past the absorption bound, for the " +
			"close-out and the final review to judge"
	case runstate.AbsorptionDeferredToReview:
		return "a backlog tick with an owner, outside the epic: deferred to the final reviewer, who absorbs it " +
			"only by naming it blocking, and listed in the epic PR under deferred findings"
	case runstate.AbsorptionBacklog:
		if record.Target != "" {
			return fmt.Sprintf("a backlog tick here naming %s, for a person to carry there", record.Target)
		}
		if record.Basis == runstate.AbsorptionBacklogDefault {
			return "a backlog tick with an owner, outside the epic, listed in the epic PR"
		}
		return "a backlog tick: the done is reachable with the finding standing"
	default:
		return "placed " + record.Placement
	}
}

// verdictLine is the verdict as the record's reader reads it: which item, and
// what decided.
func verdictLine(record runstate.Absorption) string {
	if isLiveRun(record) {
		return "its remedy is a live run of an epic, which no worker inside this one can do, so it gates no item " +
			"of this epic's done"
	}
	if isPastBound(record) {
		return "it would have extended an absorption chain already at the absorption bound, so the bound " +
			"deferred it whatever it claims to gate"
	}
	if line, ok := policyVerdictLine(record); ok {
		return line
	}
	if record.Basis == runstate.AbsorptionRule && record.Target == "" {
		// The prose rule (prose.go): an epic whose acceptance carries no
		// [A<n>] items.
		if record.Gating {
			return "its reporter claims it breaks the build or CI, which gates any epic's done, prose or not"
		}
		return "the epic's acceptance is prose, so no item exists for it to gate: it is backlog work"
	}
	if record.Basis == runstate.AbsorptionRule {
		return fmt.Sprintf("routed to %s, which this run cannot fix, so it gates no item of this epic's done",
			record.Target)
	}
	if record.Gating {
		if record.ItemID == "" {
			return "gates the done, naming no single item (the fallback names every item at risk in its reason)"
		}
		return fmt.Sprintf("gates done item %s", record.ItemID)
	}
	return "breaks no item of the done"
}

// confidenceLine is the confidence when there was one, and nothing when an
// observation needs none.
func confidenceLine(record runstate.Absorption) string {
	if record.Basis == runstate.AbsorptionPredicted && record.Confidence > 0 {
		return fmt.Sprintf(", confidence %.2f", record.Confidence)
	}
	return ""
}

// modelLine is the answering model when one answered, and nothing when none
// did — an observation, or the documented fallback (tick ce4, finding
// b8137057). The feed's reader and the scores the close-out grades are per
// model: a decision that cannot say which model guessed is a label the later
// measurement cannot calibrate with.
func modelLine(record runstate.Absorption) string {
	if record.Basis == runstate.AbsorptionPredicted && record.Model != "" {
		return fmt.Sprintf(", answered by %s", record.Model)
	}
	return ""
}

// absorbedTickRecord is the tick a promotion creates: the finding's own title
// and body, the attempt that discovered it, and a description that says what
// decided the tick into existence — so a person reading the epic's children,
// or the backlog, sees provenance, never the 9t0 shape of a tick filed with
// no provenance and no review.
func absorbedTickRecord(runID string, finding runstate.Finding, record runstate.Absorption, owner, parent string) tk.Tick {
	description := strings.TrimSpace(finding.Body)
	if description == "" {
		description = finding.Title
	}
	how := fmt.Sprintf(
		"ticfac run %s created this tick by absorbing the finding %s (reported by %s): the finding was judged "+
			"against the epic's definition of done and %s. The decision record — item id, verdict, basis — is "+
			".ticfac/runs/%s/absorptions/%s.json on the run branch.",
		runID, finding.Key, finding.DiscoveredFrom, verdictLine(record), runID, finding.Key)
	if !record.Gating && policyBasis(record.Basis) {
		how = fmt.Sprintf(
			"ticfac run %s filed this backlog tick from the finding %s (reported by %s) rather than absorb it into "+
				"the running epic: %s. The decision record is .ticfac/runs/%s/absorptions/%s.json on the run branch.",
			runID, finding.Key, finding.DiscoveredFrom, verdictLine(record), runID, finding.Key)
	} else if !record.Gating {
		how = fmt.Sprintf(
			"ticfac run %s filed this backlog tick from the finding %s (reported by %s): the done is reachable "+
				"with the finding standing, so it is a backlog tick with an owner rather than the epic's to "+
				"absorb. The decision record is .ticfac/runs/%s/absorptions/%s.json on the run branch.",
			runID, finding.Key, finding.DiscoveredFrom, runID, finding.Key)
	}
	if isPastBound(record) {
		how = fmt.Sprintf(
			"ticfac run %s filed this backlog tick from the finding %s (reported by %s) instead of absorbing it: "+
				"it was deferred past the absorption bound — %s. Judge it with the epic's PR, and work it as "+
				"ordinary backlog. The decision record is .ticfac/runs/%s/absorptions/%s.json on the run branch.",
			runID, finding.Key, finding.DiscoveredFrom, record.Reason, runID, finding.Key)
	}
	title := finding.Title
	var labels []string
	if isLiveRun(record) {
		// A finding only the NEXT epic run can satisfy (liverun.go): outside
		// the epic, labelled so that run finds it, and saying what to record.
		title = "Next epic run: " + finding.Title
		labels = []string{liveRunLabel}
		how = fmt.Sprintf(
			"ticfac run %s filed this backlog tick from the finding %s (reported by %s) instead of absorbing it: its "+
				"remedy is a live run — running an epic, or the product on a real substrate — which no worker inside "+
				"an epic can do. Satisfy it with the next epic run made the way it names, record that run's id and "+
				"outcome here, and close this tick. The decision record is .ticfac/runs/%s/absorptions/%s.json on "+
				"the run branch.",
			runID, finding.Key, finding.DiscoveredFrom, runID, finding.Key)
	}
	if record.Target != "" {
		// The local tracking tick of a finding routed to ANOTHER repository
		// (routed.go): titled with the target and saying who it is for, so a
		// person reading this tracker sees at once that the work is not here.
		title = fmt.Sprintf("For %s: %s", record.Target, finding.Title)
		how = fmt.Sprintf(
			"This finding is for %s, not for this repository. ticfac run %s filed this backlog tick from the finding "+
				"%s (reported by %s) so it is not lost: %s. Carry it to %s's tracker and close this tick. The "+
				"decision record is .ticfac/runs/%s/absorptions/%s.json on the run branch.",
			record.Target, runID, finding.Key, finding.DiscoveredFrom, record.Reason, record.Target, runID, finding.Key)
	}
	at := time.Now().UTC().Format(time.RFC3339)
	tick := tk.Tick{
		ID:             record.TickID,
		Title:          title,
		Description:    strings.TrimSpace(description + "\n\n" + how),
		Status:         "open",
		Priority:       2,
		Type:           "task",
		Owner:          owner,
		DiscoveredFrom: finding.DiscoveredFrom,
		CreatedBy:      "ticfac run " + runID,
		// The pinned layout's required stamps (contracts/tracker-layout.json):
		// a record without them is one Go's Store refuses to read.
		CreatedAt: at,
		UpdatedAt: at,
	}
	// A child of the RUNNING epic — the plan admits it mid-run (tick 3h0) and
	// the close-out does not start while it is open; a backlog tick has no
	// parent, which is what makes it backlog rather than the epic's to absorb.
	tick.Parent = parent
	tick.Labels = labels
	return tick
}

// ------------------------------------------------------- the evidence table ---

// evidenceTable is the done's bindings and the commands they authorise, read
// from the same runners.toml the gate reads and through the same validated
// reader — [evidence.acceptance] maps an item id to the id of the one command
// that proves it, in [testing.commands] or [evidence.commands], and that
// table IS the authorisation: this package never resolves a command id to
// shell any other way.
func (r *Reconciler) evidenceTable() (map[string]string, map[string]string, error) {
	cfg, err := runconfig.LoadFor(r.opts.GateConfig, r.substrate)
	if err != nil {
		return nil, nil, fmt.Errorf("read the acceptance evidence table from %s: %w", r.opts.GateConfig, err)
	}
	bindings := map[string]string{}
	if cfg.Evidence != nil {
		for item, command := range cfg.Evidence.Acceptance {
			bindings[item] = command
		}
	}
	commands := map[string]string{}
	if cfg.Testing != nil {
		for id, command := range cfg.Testing.Commands {
			commands[id] = command.Command
		}
	}
	if cfg.Evidence != nil {
		for id, command := range cfg.Evidence.Commands {
			commands[id] = command.Command
		}
	}
	return bindings, commands, nil
}

// mintTickID mints the id a promotion will name, through the durable tracker:
// the branch is the index, and the check happens where the commit happens.
func (r *Reconciler) mintTickID() (string, error) {
	durable, ok := r.tracker.(*durableTracker)
	if !ok {
		return "", fmt.Errorf("the tracker cannot mint a tick id: an absorption needs the durable writer, and a " +
			"tracker without one leaves the promotion half-made")
	}
	return durable.MintTickID()
}

// reviewTick is the epic's final review, with whether it has run yet: the
// role job that asserts the items, read from the graph the plan itself is
// re-derived from. Absent when the epic declares no review of its own.
func (r *Reconciler) reviewTick(ctx context.Context) (string, bool) {
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return "", false
	}
	// An OPEN review first: after a NOT READY the epic carries its closed
	// review and the open re-review behind it (review_rounds.go), and a
	// finding absorbed now is fixed before the review still to come.
	first := ""
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Role != "review" {
				continue
			}
			if task.Status == "open" {
				return task.ID, true
			}
			if first == "" {
				first = task.ID
			}
		}
	}
	return first, false
}
