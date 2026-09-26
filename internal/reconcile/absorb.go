package reconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/gating"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The absorption itself (tick npq): the step that today only a person can
// take, taken by the run — a finding judged GATING against the epic's own
// definition of done is promoted into the RUNNING epic as a tick, placed so
// it is fixed before the items it gates are asserted, with nobody triaging.
//
// The mechanism was already built: the two-tier decision (pzp's oracle, bse's
// predictor), the durable finding drafts (7vn), the close-out hold (aqm), and
// the plan's admission of a tick created mid-run (3h0). What this file adds
// is the DECISION DRIVER and the RECORDING that makes the decision legitimate:
//
//   - the verdict is driven by pzp's OBSERVATION where the done can be run —
//     and nothing overrides an observation — or by bse's PREDICTION where it
//     cannot yet be, with the documented absorb fallback where neither tier
//     can answer. Erring toward absorbing is deliberate and stated (bse):
//     a false positive costs the epic work it did not need, a false negative
//     closes an epic whose goal is unmet in a factory where nobody is
//     watching, and those are not the same cost.
//
//   - the decision is a RECORD on the run branch — item id, verdict,
//     observed or predicted, the confidence when a classifier answered —
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
// WHAT STAYS: a finding the done is reachable without is still a backlog tick
// with an owner, and it is still reported — unattended means nobody has to be
// there, not that nobody is ever told. And a finding the run cannot decide —
// one routed to ANOTHER repository, or an epic whose acceptance carries no
// [A<n>] items (the refusal: klq) — stays a person's at the close-out, where
// the hold already is.

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
	// Left is why the run did not decide — the finding is routed to another
	// repository, or the epic's acceptance is prose and the refusal owns the
	// decision. Empty when the run decided.
	Left string
}

// findingLeftNote is what the tick whose attempt discovered the finding says
// about where the finding went: the triage commands when the run left it for a
// person (the 7vn note, unchanged), and the tick the run promoted it to when
// the decision was the run's — because a person reading the tracker, not only
// the run state, needs to see which finding became whose work.
func (r *Reconciler) findingLeftNote(marker attemptHandle, finding subprocess.Finding, key string, decided findingDecision) string {
	if decided.TickID != "" {
		what := fmt.Sprintf("absorbed into the running epic as tick %s, with nobody triaging", decided.TickID)
		if decided.Backlog {
			what = fmt.Sprintf("promoted to a backlog tick with an owner, %s", decided.TickID)
		}
		return fmt.Sprintf("ticfac run %s: %s reported a finding the run itself decided — %s %q "+
			"(key %s, severity %s, for %s) is %s; the decision record is on the run branch at "+
			".ticfac/runs/%s/absorptions/%s.json, and the draft is triaged as promoted.",
			r.runID, r.attemptName(marker.TickID, marker.Attempt), finding.Kind, finding.Title, key,
			finding.Severity, targetName(finding.Target), what, r.runID, key)
	}
	return fmt.Sprintf("ticfac run %s: %s reported a finding drafted for triage — %s %q "+
		"(key %s, severity %s, for %s). Triage with `ticfac finding %s %s --promote-as <tick> --by "+
		"\"<who>\"`, `--discard --by \"<who>\"`, or — when it was repaired inside this epic — "+
		"`--fixed-as <commit> --by \"<who>\"`; the tick closes and the finding rides to the close-out, "+
		"which does not hand over while it is untriaged.",
		r.runID, r.attemptName(marker.TickID, marker.Attempt), finding.Kind, finding.Title, key, finding.Severity,
		targetName(finding.Target), r.opts.EpicID, key)
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

	// A finding routed to ANOTHER repository is not this run's to absorb: the
	// tick it becomes lives in the repository it targets, and writing another
	// repository's tracker from here would be the routing the finding carried,
	// silently undone. It stays a person's, at the close-out where the hold
	// already is and the finding's full text is on the PR.
	if standing.Target != "" {
		return findingDecision{Left: fmt.Sprintf(
			"the finding is routed to %s and only a person can file it there", standing.Target)}, nil
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
	evidence, commands, err := r.evidenceTable()
	if err != nil {
		return findingDecision{}, err
	}
	done, refusal, err := acceptance.Decide(epic.AcceptanceCriteria, evidence)
	if err != nil {
		return findingDecision{}, fmt.Errorf("decide the absorption of finding %s: the epic %s carries an "+
			"acceptance this run cannot read, and a person must fix the epic's own text: %w", key, r.opts.EpicID, err)
	}
	if refusal != nil {
		r.record(marker.TickID, StageAbsorptionRefused,
			"finding %s is left for a person: %s", key, refusal.Reason)
		return findingDecision{Left: refusal.Reason}, nil
	}

	// The decision already recorded: an earlier incarnation decided this
	// finding and was killed before finishing. The record IS the decision —
	// it names the tick and carries the verdict — and what remains is to
	// finish behind it: create the tick if the kill came first, place it,
	// complete the triage. A cold incarnation reaches the same epic as the
	// warm one by reading this record, which is the whole of its existence.
	if recorded, ok, err := r.store.Absorption(key); err != nil {
		return findingDecision{}, err
	} else if ok {
		return r.finishAbsorption(ctx, marker, *standing, *recorded)
	}

	// The verdict: the oracle where the done can be run, the predictor where
	// it cannot yet be, and the documented absorb fallback where neither tier
	// can answer — never a stop, because stopping is the one actor only a
	// person can play, and this tick is what takes the person out.
	finding := gating.Finding{ID: standing.Key, Title: standing.Title, Body: standing.Body}
	verdict, err := r.gatingVerdict(ctx, finding, done, evidenceRunner{r: r, commands: commands})
	if err != nil {
		return findingDecision{}, err
	}

	// THE RECURSION'S BOUND (tick qjj): a gating verdict is about to make
	// this finding the next link of a chain, and the chain is bounded. The
	// check sits AFTER the verdict, never before it, because only a GATING
	// finding extends the recursion — a non-gating one goes to the backlog
	// and stops the chain where it stands, and a run that stopped over one
	// would be holding a person for a decision the run itself was about to
	// make. A chain already at the bound refuses to absorb and stops the run
	// for a person, carrying the chain that produced the stop, so the stop is
	// rare and meaningful rather than the human gate this epic exists to
	// remove, rebuilt under a different name.
	var links []chainLink
	if verdict.Gating {
		links, err = r.absorptionChain(standing.TickID)
		if err != nil {
			return findingDecision{}, err
		}
		if absorptionDepthExceeded(links, r.opts.AbsorptionDepthBound) {
			r.record(marker.TickID, StageAbsorptionBoundExceeded,
				"the absorption of finding %s would be the %s absorption of one chain and the bound is %d: %s",
				standing.Key, ordinal(len(links)+1), r.opts.AbsorptionDepthBound, chainNarrative(links))
			return findingDecision{}, r.refuse(RefusedAbsorptionDepth, marker.TickID,
				"absorbing the finding %s (%q), reported by %s, would be the %s absorption of ONE chain that "+
					"already carries %d and the bound is %d (tick qjj): the run stops for a person rather than recurse "+
					"past the bound, because unbounded the recursion is an epic that never closes and nothing "+
					"announces it. The chain that produced the stop: %s. The finding stays a person's — triage it "+
					"with `ticfac finding %s %s --promote-as <tick> --by \"<who>\"` or `--discard --by \"<who>\"`, or "+
					"raise the bound with --absorption-depth and run the epic again. If this bound trips often, "+
					"the criterion is wrong and the bound is hiding it — the chain above is what a person judges "+
					"it by",
				standing.Key, standing.Title, r.attemptName(marker.TickID, marker.Attempt),
				ordinal(len(links)+1), len(links), r.opts.AbsorptionDepthBound, chainNarrative(links),
				r.opts.EpicID, standing.Key)
		}
	}

	// The tick the promotion will create, minted before the record that names
	// it: the record is what a killed incarnation is resumed by.
	tickID, err := r.mintTickID()
	if err != nil {
		return findingDecision{}, err
	}
	placement := runstate.AbsorptionBacklog
	review, reviewOpen := r.reviewTick(ctx)
	if verdict.Gating {
		if review != "" && reviewOpen {
			placement = runstate.AbsorptionBeforeReview
		} else {
			placement = runstate.AbsorptionAfterReview
		}
	}

	// The decision record, on the run branch, BEFORE the tick exists: the
	// reasoning that changed the epic's shape, durable, keyed by the finding.
	record := runstate.Absorption{
		Key:        standing.Key,
		TickID:     tickID,
		Gating:     verdict.Gating,
		ItemID:     verdict.ItemID,
		Basis:      string(verdict.Basis),
		Confidence: verdict.Confidence,
		Fallback:   verdict.Fallback,
		Reason:     verdict.Reason,
		Placement:  placement,
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
	stage, what := StageAbsorbed, "absorbed into the running epic"
	if !record.Gating {
		stage, what = StageBacklogged, "promoted to a backlog tick with an owner"
	}
	r.record(marker.TickID, stage,
		"finding %s is %s as tick %s: %s (basis %s%s), %s",
		record.Key, what, record.TickID, verdictLine(record), record.Basis, confidenceLine(record), placementLine(record))

	return findingDecision{TickID: record.TickID, Backlog: !record.Gating}, nil
}

// placementLine is the placement as a person reads it.
func placementLine(record runstate.Absorption) string {
	switch record.Placement {
	case runstate.AbsorptionBeforeReview:
		return fmt.Sprintf("placed before the final review, which is blocked-by %s", record.TickID)
	case runstate.AbsorptionAfterReview:
		return "placed before the close-out, which does not start while it is open; the review had already run"
	case runstate.AbsorptionBacklog:
		return "a backlog tick: the done is reachable with the finding standing"
	default:
		return "placed " + record.Placement
	}
}

// verdictLine is the verdict as the record's reader reads it: which item, and
// what decided.
func verdictLine(record runstate.Absorption) string {
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
			"against the epic's definition of done and %s. The decision record — item id, verdict, observed or "+
			"predicted, confidence — is .ticfac/runs/%s/absorptions/%s.json on the run branch.",
		runID, finding.Key, finding.DiscoveredFrom, verdictLine(record), runID, finding.Key)
	if !record.Gating {
		how = fmt.Sprintf(
			"ticfac run %s filed this backlog tick from the finding %s (reported by %s): the done is reachable "+
				"with the finding standing, so it is a backlog tick with an owner rather than the epic's to "+
				"absorb. The decision record is .ticfac/runs/%s/absorptions/%s.json on the run branch.",
			runID, finding.Key, finding.DiscoveredFrom, runID, finding.Key)
	}
	at := time.Now().UTC().Format(time.RFC3339)
	tick := tk.Tick{
		ID:             record.TickID,
		Title:          finding.Title,
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
	return tick
}

// ---------------------------------------------------------- the two tiers ---

// gatingVerdict is the two-tier decision driven over the done: the oracle's
// observation where an item is bound to a command, the predictor's guess
// where it is not yet runnable, and the combined fallback where neither tier
// can answer. Every tier's rules hold unchanged (pzp, bse): a runnable item is
// never offered to the classifier, an observation is never overridden, and an
// unreachable classifier absorbs rather than defers.
func (r *Reconciler) gatingVerdict(ctx context.Context, finding gating.Finding, done acceptance.Done,
	runner gating.Runner) (*gating.Verdict, error) {
	observed, observedReason, err := gating.NewOracle(runner).Observe(ctx, finding, done)
	if err != nil {
		return nil, fmt.Errorf("observe the done against finding %s: %w", finding.ID, err)
	}
	if observed != nil && observed.Gating {
		// Observed broken: the authoritative verdict, and no classifier
		// overrides it. Nothing else is asked.
		return &observed.Verdict, nil
	}
	predicted, predictedReason, err := gating.NewPredictor(r.opts.GatingClassifier).Predict(ctx, finding, done)
	if err != nil {
		return nil, fmt.Errorf("predict the done against finding %s: %w", finding.ID, err)
	}
	verdict, undecided := combineGating(finding.ID, done, observed, observedReason, predicted, predictedReason)
	if verdict == nil {
		return nil, fmt.Errorf("the two-tier decision over finding %s answered neither a verdict nor a reason: %s",
			finding.ID, undecided)
	}
	return verdict, nil
}

// combineGating is the meeting point of the two tiers, and the one place the
// absorption's own rule lives: where NEITHER tier can answer — the done's
// commands could not run and no classifier is configured or reachable — the
// decision errs toward absorbing, recorded as a prediction with the fallback
// saying why no model answered, because deferring would stop an unattended
// run on the one actor only a person can play.
//
// The cases, and every caller takes the same closed set:
//
//   - observed gating: the observation, unchanged.
//   - observed not gating, every item decided: the observation (all runnable
//     and demonstrated, or the prediction over the unverified half said none).
//   - observed not gating but a RUNNABLE item produced no evidence (the
//     command could not run, or answered error or skipped): the fallback —
//     an item with no evidence is never read as demonstrated, which would be
//     the false negative this epic exists to prevent, manufactured by the
//     decision itself.
//   - nothing observed: the prediction, or the fallback when neither tier
//     answered.
func combineGating(findingID string, done acceptance.Done, observed *gating.Observed, observedReason string,
	predicted *gating.Verdict, predictedReason string) (*gating.Verdict, string) {
	if observed != nil && observed.Gating {
		return &observed.Verdict, ""
	}

	// The unverified items are the predictor's alone; an unresolved entry
	// that is NOT one of them is a runnable item whose command produced no
	// evidence — a state neither tier answers, and the fallback's.
	unverified := map[string]bool{}
	for _, item := range done.Items {
		if item.State == acceptance.Unverified {
			unverified[item.ID] = true
		}
	}
	if observed != nil {
		var noEvidence []string
		for _, unresolved := range observed.Unresolved {
			if !unverified[unresolved.ItemID] {
				noEvidence = append(noEvidence, unresolved.ItemID)
			}
		}
		if predicted != nil && predicted.Gating {
			return predicted, ""
		}
		if len(noEvidence) > 0 {
			return absorbFallback(findingID, noEvidence, fmt.Sprintf(
				"the commands for %s produced no evidence about their items (observed %s on the runnable half), "+
					"and no prediction covers a runnable item", strings.Join(noEvidence, ", "),
				observedReason)), ""
		}
		if predicted != nil {
			return predicted, ""
		}
		// Nothing left to predict and nothing unresolved beyond the
		// prediction's own scope: the observation stands.
		return &observed.Verdict, ""
	}

	// Nothing was observed at all: the prediction decides where it exists,
	// and the combined fallback answers where neither tier could.
	if predicted != nil {
		return predicted, ""
	}
	var atRisk []string
	for _, item := range done.Items {
		atRisk = append(atRisk, item.ID)
	}
	return absorbFallback(findingID, atRisk, fmt.Sprintf(
		"neither tier could answer — the oracle: %s; the predictor: %s — and the decision errs toward absorbing",
		observedReason, predictedReason)), ""
}

// absorbFallback is the verdict written when NO TIER could answer: the
// documented decision to absorb anyway, recorded as a PREDICTION (a guess,
// never a measurement), with Fallback carrying why and the reason naming
// every item at risk — the same shape and the same bias bse's own fallback
// keeps, written here because this decision owns what to do with the state
// neither tier decides.
func absorbFallback(findingID string, itemIDs []string, why string) *gating.Verdict {
	return &gating.Verdict{
		FindingID: findingID,
		Gating:    true,
		Basis:     gating.BasisPredicted,
		Fallback:  why,
		Reason: fmt.Sprintf(
			"no verdict could be reached — %s — and the decision is to absorb anyway: a false negative would close "+
				"an epic whose goal is unmet in a factory where nobody is watching, which costs more than the work a "+
				"false positive wastes, and deferring would stop an unattended run on the one actor only a person can "+
				"play. The items at risk are %s",
			strings.TrimSpace(why), strings.Join(itemIDs, ", ")),
	}
}

// ------------------------------------------------------------- the runner ---

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

// evidenceRunner is the oracle's seam (pzp): it runs one command id the
// evidence table authorises, on the integration branch as origin has it, and
// reports the commit it ran on — the key of the observed verdict, so the
// record says "this tree, while the finding stands" rather than "once, at
// some point". The command runs in a throwaway worktree of the branch head,
// through the gate's own shell and bound, so the oracle reuses the one path
// the run already runs declared commands down.
type evidenceRunner struct {
	r        *Reconciler
	commands map[string]string
}

// Run executes the command the evidence table bound to an item, and reports
// what the run says about itself. A command that cannot be RESOLVED is an
// error — a violated table, and a verdict over one would be a decision
// wearing a defect's costume. A command that cannot RUN is the oracle's to
// treat as unresolved, not this seam's to smooth over.
func (e evidenceRunner) Run(ctx context.Context, command string) (gating.Run, error) {
	line, ok := e.commands[command]
	if !ok {
		return gating.Run{}, fmt.Errorf("the evidence table binds the command id %q, which neither [testing.commands] "+
			"nor [evidence.commands] declares: a binding that resolves to nothing is a stop", command)
	}
	ref := "refs/ticfac/fetched/" + e.r.git.fetch1D() + "/" + e.r.branch
	if err := e.r.git.fetch(e.r.branch); err != nil {
		return gating.Run{}, fmt.Errorf("fetch %s to run the done's command %s: %w", e.r.branch, command, err)
	}
	head, err := e.r.git.run("", "rev-parse", ref)
	if err != nil {
		return gating.Run{}, err
	}
	dir, remove, err := e.r.git.tempWorktree("ticfac-done-", head)
	if err != nil {
		return gating.Run{}, err
	}
	defer remove()

	started := time.Now().UTC()
	stdout, stderr, code, err := runShell(ctx, dir, line, e.r.opts.GateTimeout)
	finished := time.Now().UTC()
	// The gate's own classification, not a reflex to err: `sh -c` reports a
	// NORMAL non-zero exit through cmd.Wait's error with the true code beside
	// it, and a command that ANSWERED non-zero is the fail the oracle reads —
	// the evidence about the item — while a run that never reported a status
	// (negative code) is an error: no evidence, the item unresolved, never
	// assumed either way.
	if err != nil && code < 0 {
		// The command could not run: no evidence exists, and the oracle
		// reads the item as unresolved, never as either verdict.
		return gating.Run{}, fmt.Errorf("the done's command %s could not run on %s: %w", command, short(head), err)
	}
	result := gating.ResultFail
	if code == 0 {
		result = gating.ResultPass
	}
	return gating.Run{
		Commit:     head,
		Result:     result,
		ExitCode:   code,
		Stdout:     stdout,
		Stderr:     stderr,
		StartedAt:  started.Format(time.RFC3339),
		FinishedAt: finished.Format(time.RFC3339),
	}, nil
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
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if task.Role != "review" {
				continue
			}
			return task.ID, task.Status == "open"
		}
	}
	return "", false
}
