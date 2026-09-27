package reconcile

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The dispatch window: how the run works on several ticks at once without
// working on the integration branch at several places at once.
//
// [orchestration].max_parallel was read, validated, and quoted in the feed's
// wave-width lines for a long time while the run loop was a plain `for` over
// the plan — one tick dispatched, waited for, collected, merged, gated and
// closed before the next was dispatched at all. Measured on the Phase 3 run's
// own feed, a tick took a median of 53 minutes, of which the gate was about 6:
// the other 47 were a worker thinking in its own worktree, which is the part
// nothing forces to happen one at a time.
//
// What the window does NOT do is introduce a second writer. Every phase still
// runs in one goroutine; what changed is that the wait is multiplexed rather
// than blocking, so `width` attempts can be dispatched and addressed while the
// collect/integrate/gate/close half stays strictly serial. That is not
// timidity: this epic produced three separate defects that were all the same
// shape — process-global git state (FETCH_HEAD, refs/remotes/<remote>/<branch>,
// a per-run peek ref) written by two parties at once — and a window that polls
// from one goroutine cannot produce a fourth.

// held is everything the window is holding at one moment: the workers still
// thinking, the ones that have settled and are waiting their turn to be
// finished, and the one attempt actually being finished.
//
// All three are the same thing for both questions the window asks: a tick this
// run has CLAIMED and not yet closed. Its wave the window may not run past, its
// dependents may not start, and it counts against the declared width.
//
// Tick 9pz made the width count only attempts with a LIVE WORKER, on the
// reasoning that max_parallel bounds a model and a pane and CPU rather than
// bookkeeping. That reasoning is fine and the number was wrong, because the
// width is not ours to define: `tk` enforces it, and tk counts CLAIMS — and a
// claim lives until its tick CLOSES. Epic dha found it. Three ticks had settled
// and were waiting to be finished: no worker, still claimed. The window
// admitted a fifth and tk refused it with the four holders named:
//
//	claim e9n: tk command "claim" refused: dispatch width exceeded: wave width
//	4 is full: 4 implementer(s) already in flight under dha (80x, aqm, b50,
//	cxk). Claiming e9n would make 5.
//
// tk is right. The width is enforced where it cannot be argued with, and "in
// flight" now means one thing on both sides of that line: claimed and not
// closed.
//
// WHAT IT COSTS, said plainly rather than discovered later: finishing is
// serial, so a queue of settled-but-unfinished ticks holds claims the window
// cannot reuse, and admission starves for as long as the queue takes to drain.
// That gives back most of what 9pz bought whenever finishes pile up. It is
// deliberate and it is temporary — correct against the tracker as it stands
// today, and no more than that. What lifts it is tk learning the distinction
// (ticks repo, e3c): a claim that is settled and being integrated is not an
// implementer in flight, and only the former should count. Until that exists,
// this side does not get to assume it. Raising max_parallel is NOT the
// workaround: it makes the two numbers disagree by a larger margin.
type held struct {
	live    []*inflightAttempt
	settled []*settledAttempt
	finish  *finishing
}

// settledAttempt is an attempt that has settled while another was being
// finished. Finishing is serial, so it waits — with its status, because that is
// the answer the collect is about and re-polling a terminal attempt to get it
// again would say "settled as succeeded" to the feed twice.
type settledAttempt struct {
	fl     *inflightAttempt
	status *subprocess.JobStatus
}

// holders is every attempt the window has not closed yet, in the order it took
// them on: what the graph boundaries are read against.
func (h *held) holders() []*inflightAttempt {
	out := make([]*inflightAttempt, 0, len(h.live)+len(h.settled)+1)
	out = append(out, h.live...)
	for _, s := range h.settled {
		out = append(out, s.fl)
	}
	if h.finish != nil {
		out = append(out, h.finish.fl)
	}
	return out
}

// claims is how many ticks this run holds a claim on: the number the declared
// width bounds, counted the way the tracker counts it (tick 3mp). A claim is
// taken at the dispatch and lives until the tick closes, so every holder counts
// — the worker still thinking, the one that settled and is waiting its turn,
// and the one being finished whose worker is already gone.
func (h *held) claims() int { return len(h.holders()) }

// runPlan works the plan and reports the ticks that were rejected.
func (r *Reconciler) runPlan(ctx context.Context, plan []planEntry) ([]string, error) {
	var failed []string
	var window held
	queue, err := r.adoptionFirst(plan)
	if err != nil {
		return nil, err
	}
	// When the window was last polled. The excuse a finish owes the attempts
	// that waited through it is measured from here rather than from the
	// finish's own start, because a finish no longer stops the polling.
	polledAt := r.now()
	// A refusal STOPS the run — it does not finish the rest of the window
	// first.
	//
	// The temptation is to drain: those attempts are paid for, why abandon
	// them? Because finishing them would mean integrating and gating on a tree
	// the refusal just said nothing stands behind. A failing gate leaves its
	// tick's merge on the integration branch (that is what makes the repair
	// "fix the tree and run again"), so every attempt drained after it would
	// gate against a tree already known to be bad and fail for a reason that
	// has nothing to do with its own work. A run that manufactures failures
	// for good work is worse than one that stops.
	//
	// Nothing is lost by stopping. An attempt in flight is durable: its marker
	// is on the remote and its commits are on its own branch, and a resumed
	// run ADOPTS it by identity rather than dispatching over it — which is
	// exactly what happened to attempt 22 of 7zs, dispatched by one
	// incarnation and adopted, collected and closed by the next.
	stopped := false

	reject := func(tick string, refusal *Refusal) error {
		failed = append(failed, tick)
		r.failure = refusal
		r.setTick(tick, refusedTickState(refusal))
		// Two answers to one question, from tick 0z0 and tick emk, kept
		// together because they are not the same claim.
		//
		// A refusal that HOLDS the run for a person gets its own vocabulary
		// (StageRunHeld, 0z0): the feed is what a non-participant watches,
		// and a hold nobody can see is a stall by definition. Every other
		// refusal still reaches the feed in its own words (emk) — without
		// that, the feed went from `dispatched` straight to a generic
		// run_finished and the reason existed only in the source.
		if holdsForAPerson(refusal.Reason) {
			r.record(tick, StageRunHeld, "%s: %s", refusal.Reason, refusal.Message)
		} else {
			r.recordRefusal(tick, refusal)
		}
		_, cErr := r.checkpoint(runstate.StateFailed, refusal.Error())
		return cErr
	}

	// stop is what a refusal raised about an attempt the window was HOLDING
	// does: record it, say which attempts the run is walking away from, and
	// end the run. The refused attempt is out of the window by the time this
	// is called — a run does not announce as abandoned the tick it is stopping
	// for.
	stop := func(tick string, err error) ([]string, error) {
		var refusal *Refusal
		if !asRefusal(err, &refusal) {
			return nil, err
		}
		stopped = true
		if cErr := reject(tick, refusal); cErr != nil {
			return nil, cErr
		}
		r.announceAbandonedWindow(&window)
		return failed, nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		for !stopped && len(queue) > 0 && r.mayAdmit(queue[0], &window, plan) {
			entry := queue[0]
			queue = queue[1:]
			fl, err := r.admit(ctx, entry)
			if err != nil {
				// A tick the tracker still holds behind an open blocker (tick
				// 3h0) is not the tick's refusal: it is the run's own
				// sequencing catching up with an edge the plan did not carry,
				// and the answer is to honour it — requeue the tick behind the
				// blocker and dispatch the blocker first — not to stop.
				var blocked *blockedTickErr
				if errors.As(err, &blocked) {
					plan, queue, err = r.requeueBlocked(ctx, plan, queue, entry, blocked, window.holders())
					if err == nil {
						continue
					}
				}
				var refusal *Refusal
				if !asRefusal(err, &refusal) {
					return nil, err
				}
				stopped = true
				if cErr := reject(entry.TickID, refusal); cErr != nil {
					return nil, cErr
				}
				break
			}
			switch {
			case fl == nil:
			case fl.integrated:
				// Already merged by somebody else: nothing to poll, so it
				// waits its turn to be finished like any settled attempt.
				window.settled = append(window.settled, &settledAttempt{fl: fl})
			default:
				window.live = append(window.live, fl)
			}
		}

		if stopped {
			r.announceAbandonedWindow(&window)
			return failed, nil
		}

		// Finishing is SERIAL, so the next finish starts only once the last one
		// is over — and it starts from the attempt that settled FIRST, which is
		// the order the queue holds them in.
		if window.finish == nil && len(window.settled) > 0 {
			next := window.settled[0]
			window.settled = window.settled[1:]
			window.finish = r.beginFinish(next.fl, next.status)
		}

		// ONE step of the finish, and then the window's own turn. This is the
		// whole of tick 9pz's second half: the step that used to be a
		// multi-minute blocking call is a step like any other, so the admission
		// above and the poll below happen through a gate instead of after it.
		if window.finish != nil {
			f := window.finish
			done, err := r.advanceFinish(ctx, f)
			if err != nil {
				window.finish = nil
				return stop(f.fl.entry.TickID, err)
			}
			if done {
				window.finish = nil
				// The gap the run really caused. It used to be the whole
				// finish, because nothing was polled through one; now the
				// window is polled every round of it, so what is forgiven is
				// what actually happened — which on a window with nothing live
				// to poll is still the whole finish.
				r.excuseWindow(window.live, r.now().Sub(polledAt))

				// An attempt has closed, which is the only moment the
				// readiness of any OTHER tick of this epic can have changed —
				// so it is the moment the plan is asked whether it still
				// describes the graph it came from.
				// reconciler-decision:D33:begin:replan-cadence — the local
				// host re-derives the plan only when an attempt closes; the
				// Workflow re-derives it on every pass
				// (decisions/reconciler-parity.json, D33).
				plan, queue, err = r.replan(ctx, plan, queue)
				// reconciler-decision:D33:end:replan-cadence
				if err != nil {
					return nil, err
				}
				continue
			}
		}

		if len(window.live) == 0 {
			holders := window.holders()
			if len(queue) == 0 && len(holders) == 0 {
				return failed, nil
			}
			// Nothing to poll, and something still to do: a finish mid-gate
			// with no worker left beside it. Resting is the honest answer —
			// spinning on it would spend the host on asking — and it rests at
			// the same beat a window of live attempts rests at, the shortest
			// cadence any attempt it is holding asked for (tick u9l). A sleep
			// of the run's own invention here would be the one interval in this
			// loop that no executor chose.
			if len(holders) > 0 {
				if err := r.restWindow(holders); err != nil {
					return nil, err
				}
			}
			continue
		}

		settled, status, err := r.pollWindow(ctx, window.live)
		polledAt = r.now()
		if settled < 0 {
			if err != nil {
				return nil, err
			}
			if err := r.restWindow(window.live); err != nil {
				return nil, err
			}
			continue
		}

		// The attempt leaves the LIVE half whichever way it ends. A refusal
		// raised while ADDRESSING it — unaddressed, wiped, past its wall
		// clock — is the tick's refusal exactly as a refusal from its gate is,
		// and reaches the feed the same way; the difference is only where the
		// run was standing when it learned.
		fl := window.live[settled]
		window.live = append(window.live[:settled], window.live[settled+1:]...)
		if err != nil {
			return stop(fl.entry.TickID, err)
		}
		window.settled = append(window.settled, &settledAttempt{fl: fl, status: status})
	}
}

// adoptionFirst is the order a pass takes the plan in: every tick this run
// already has a LIVE attempt of first, then every other tick the tracker says
// is claimed and this run has dispatched before, then the plan as planned.
//
// The plan's own order is the tracker's layering — wave, then priority — and
// says nothing about what is already in flight. The window adopted an
// in-flight attempt only when the queue happened to reach its tick, and
// counted only what it was holding, so a resumed pass could claim NEW work
// while live attempts of its own sat unadopted further down the queue: on
// epic-yoh the plan read gbs, a08, cr4, lkd, ppt; gbs was adopted, a08 was
// admitted into what the window believed were three free slots of four, and
// tk refused the claim because cr4, lkd and ppt held them. lkd and ppt were
// this run's own live workers; the pass never reached them.
//
// Adopting first makes the window's count the tracker's before anything new
// is asked for. The claimed-but-not-live ticks (rejected, or a marker whose
// start never happened) come next because they hold claims too: resolving them
// — finished from the branch, held, or redispatched into the claim they
// already have — is what frees width, and a hold is a hold wherever it sits.
// Role jobs keep their place: they run alone, after the work.
func (r *Reconciler) adoptionFirst(plan []planEntry) ([]planEntry, error) {
	attempts, err := r.store.Attempts()
	if err != nil {
		return nil, fmt.Errorf("reconcile: read the attempts this run already dispatched: %w", err)
	}
	dispatched := map[string]bool{}
	for _, attempt := range attempts {
		dispatched[attempt.TickID] = true
	}
	rank := func(entry planEntry) int {
		if !dispatched[entry.TickID] || isRoleJob(entry.Role) {
			return 2
		}
		switch r.tickState(entry.TickID) {
		case "dispatched", "reported", "integrated":
			return 0
		}
		if entry.Claimed {
			return 1
		}
		return 2
	}
	out := append([]planEntry{}, plan...)
	for i := range out {
		out[i].InFlight = rank(out[i]) == 0
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out, nil
}

// refusedTickState is the checkpoint state a refusal leaves its tick in.
//
// Every refusal REJECTS its tick — except a claim the tracker refused. That
// refusal is raised after the dispatch marker is on origin (the marker comes
// first; it is the compare-and-swap) and before any worker started, so the
// attempt it names never ran. Recording the tick "rejected" made the next
// resume read the marker as a spent attempt that "settled with nothing":
// redispatched under a new number, counted as a failed try, and escalated a
// rung on the tier ladder — epic-yoh's a08 went to frontier on its first real
// try for a claim nobody's work had anything to do with. Left "ready", the
// marker is adopted on resume: its claim is replayed, and the very attempt the
// refusal interrupted is started at the tier it was planned at.
func refusedTickState(refusal *Refusal) string {
	if refusal != nil && refusal.Reason == RefusedClaimWidth {
		return "ready"
	}
	return "rejected"
}

// replan re-derives what the run has left to do from a fresh read of the epic
// graph, and reports the plan and the remaining queue to work from.
//
// The plan a run is handed is a SNAPSHOT: `tk` assigns wave numbers by
// layering the dependency graph at the moment it is asked, so a tick whose
// blocker was still open at run start is recorded in a later wave and carries
// that number for the rest of the run. When the blocker closes — by this run,
// or by a person while it is going — the dependent does not move up. It stays
// stranded in a wave describing a graph that no longer exists, and the window
// refuses to admit it because the number says so.
//
// That is not a small inefficiency. Measured on epic ncv (tick g50): three
// free slots, two ticks whose only blocker had closed eighteen minutes
// earlier, and zero dispatches — the ticks appear ZERO times in the run's
// hundred-event feed, while a fresh read of the same graph put all three in
// one wave. The run drains, finishes, and a person restarts it to re-plan. The
// cost is one whole run incarnation per dependency edge, which is what "we
// have gone very sequential" was.
//
// Since tick 3h0 the same re-derivation admits the other way a live epic
// changes: a tick CREATED under it after the run started — a person
// absorbing a gating finding into the epic is the production shape, observed
// on epic-yoh — and a blocked_by edge added mid-run. Both were invisible to
// the run: the new tick never entered the plan, so neither its work nor its
// dependents' edges were ever worked, and the run walked straight into its
// close-out over four open blockers and opened the epic PR anyway. So the
// re-derivation now ADDS what the fresh graph carries and the plan does not,
// beside refreshing what it already had — and the dispatch itself re-reads
// the tick's blockers (settleBeforeDispatch, tick 3h0) so an edge that landed
// between two re-derivations is still honoured before the tick is dispatched.
//
// The tracker is asked again. It is the authority on what is closed —
// settleBeforeDispatch already re-reads it per tick for exactly that reason —
// and the wave numbers it layers now are about the graph as it now is. What is
// re-derived is only the two facts that go stale, the wave and the open
// blockers; everything the tier derivation is a function of is left exactly as
// PLANNING recorded it, because a derivation that changed under a tick between
// one attempt and the next would be a routing decision nothing recorded.
//
// A graph the tracker cannot answer for stops the run rather than being
// guessed at, the same way settleBeforeDispatch's read does: the alternative
// is dispatching against a readiness nobody confirmed.
func (r *Reconciler) replan(ctx context.Context, plan, queue []planEntry) ([]planEntry, []planEntry, error) {
	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return nil, nil, fmt.Errorf("reconcile: re-read the epic graph of %s: %w", r.opts.EpicID, err)
	}
	freshEntries := planFrom(graph)
	fresh := map[string]planEntry{}
	for _, entry := range freshEntries {
		fresh[entry.TickID] = entry
	}

	// Who holds a claim is re-read whether or not any wave moved: a close is
	// exactly when a claim ends, and the width is counted from these.
	plan, queue = refreshClaims(plan, fresh), refreshClaims(queue, fresh)

	rederived := resequence(plan, fresh)
	// What the fresh graph carries that the plan never did (tick 3h0): a tick
	// created under the epic after this run started — or one closed before it
	// and reopened while it was going. Both are children the close-out would
	// otherwise stand over unworked, so both are admitted to the plan and the
	// queue and dispatched by THIS run rather than by a restart nobody
	// attended.
	added := unplannedTicks(plan, freshEntries)
	if len(added) > 0 {
		rederived = append(rederived, added...)
		sortPlan(rederived)
	}
	moved := movedTicks(plan, rederived)
	if len(moved) == 0 && len(added) == 0 {
		return plan, queue, nil
	}

	r.seedTicks(added)
	r.seedTitles(added)
	for _, entry := range added {
		r.record(entry.TickID, StageReplanned,
			"the tracker carries %s, which was created under %s after this run planned its waves: the tick is admitted "+
				"by THIS run — sequenced into the waves the tracker layers now, dispatched and closed without a restart — "+
				"because a run that works only the graph as it was when it started is a run a person has to kill so a "+
				"resume can replan from scratch",
			entry.TickID, r.opts.EpicID)
	}

	// The composition check the admitted plan passed says nothing about this
	// one: two ticks that declare the same file and were sequenced into
	// different waves can now land in the SAME wave, which is precisely the
	// wave that cannot merge.
	//
	// What happens then is a refusal of the RE-DERIVATION, not of the run. The
	// run was admitted against a plan that sequences those two ticks one after
	// the other, and working that plan dispatches nothing the composition rule
	// objects to — so it keeps it, and says so. Refusing the run here would
	// kill live work over a wave the run had already decided not to dispatch
	// together, which is a worse answer than the sequencing it already has;
	// refusing SILENTLY would be the quiet deferral composition.go refuses to
	// make. The loud refusal still belongs at admission, where it costs
	// nothing and where a person can re-wave the ticks.
	//
	// With additions in the re-derivation (tick 3h0) there is one more answer
	// the old rule could not give: a composition refusal can name a tick the
	// plan never carried, and "keeping the sequencing it was admitted with"
	// does not exist for a tick that was never sequenced at all — keeping it
	// would be the quiet deferral again, pointed at the run's own plan. So the
	// old ticks keep the sequencing they were admitted with, the NEW tick joins
	// at the wave the tracker layers it into, and any overlap between them is
	// discovered where every undeclared overlap already is: at the merge,
	// loudly, refusing the attempt that crossed it.
	if refusal := r.checkWaveComposition(rederived); refusal != nil {
		if len(added) == 0 {
			r.record("", StageReplanned,
				"%s is no longer blocked and would join wave %d, but the re-derived plan cannot merge, so the run "+
					"keeps the sequencing it was admitted with and dispatches them one after the other: %s",
				moved[0].TickID, moved[0].Wave, refusal.Message)
			return plan, queue, nil
		}
		r.record("", StageReplanned,
			"the re-derived plan cannot merge — %s — so the run keeps the sequencing it was admitted with and admits "+
				"%s at the wave the tracker layers it into; any overlap between them is refused at the merge, where every "+
				"undeclared overlap already is",
			refusal.Message, tickIDs(added))
		kept := append(append([]planEntry{}, plan...), added...)
		sortPlan(kept)
		queue = append(resequence(queue, fresh), added...)
		sortPlan(queue)
		return kept, queue, nil
	}

	for _, entry := range moved {
		r.record(entry.TickID, StageReplanned,
			"the tracker now layers %s in wave %d rather than the wave %d it was planned into: what it was "+
				"sequenced behind has closed since this run started, so it is admissible now and does not wait "+
				"for a restart to be re-planned",
			entry.TickID, entry.Wave, waveOf(plan, entry.TickID))
	}
	r.recordWaveCompositionDecision(rederived)
	queue = append(resequence(queue, fresh), added...)
	sortPlan(queue)
	return rederived, queue, nil
}

// unplannedTicks is the fresh reading's ticks the plan does not carry (tick
// 3h0): created under the epic after the run started, or closed before it and
// reopened while it was going. A tick the plan carries is not an addition
// however the tracker now reads it — the plan keeps every entry it was ever
// admitted with, so an entry that is gone from the fresh graph is a closed
// tick settleBeforeDispatch settles, and an entry that is BACK in the fresh
// graph is a child this run already worked whose reopening the close-out's
// open-children gate answers for.
func unplannedTicks(plan []planEntry, fresh []planEntry) []planEntry {
	carried := map[string]bool{}
	for _, entry := range plan {
		carried[entry.TickID] = true
	}
	var added []planEntry
	for _, entry := range fresh {
		if !carried[entry.TickID] {
			added = append(added, entry)
		}
	}
	return added
}

// tickIDs is the plan's tick ids in order, for the one feed line that names a
// set rather than a tick.
func tickIDs(entries []planEntry) string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.TickID)
	}
	return strings.Join(ids, ", ")
}

// ------------------------------------------------- edges added mid-run (3h0) ---

// blockedTickErr is settleBeforeDispatch's answer for a tick whose own
// dispatch-time read found it must not be claimed yet — either an OPEN blocker
// its tracker record still names (an edge added while the run was going, or a
// blocker reopened behind the plan's back), or, for the close-out, an OPEN
// CHILD of the epic the close-out's own gate found (tick 3h0) — in which case
// the gate's refusal rides along, because when nothing this run is doing can
// close the children, that refusal — its reason, its tick, its own words — is
// the answer, not the edge vocabulary's. It is not a refusal by itself: the
// run has not yet decided what the open blockers mean, because whether one of
// them is a thing IT can still close is a fact about the plan, and the plan is
// re-derived here (requeueBlocked) rather than guessed at. The blockers are
// the OPEN ones only, in the tracker's own order, as the dispatch-time read
// found them.
type blockedTickErr struct {
	tick     string
	blockers []string
	// refusal is the gate's own answer for the close-out, carried so that the
	// refusal requeueBlocked falls back to is the one the open-children gate
	// wrote (closeout_children_open, naming the children) rather than the
	// edge vocabulary's tick_blocked_open. nil for a plain blocked_by edge.
	refusal *Refusal
}

func (b *blockedTickErr) Error() string {
	return fmt.Sprintf("%s is blocked by %s", b.tick, strings.Join(b.blockers, ", "))
}

// requeueBlocked honours a blocked_by edge the plan did not carry: the tick is
// put back behind the blocker rather than dispatched past it.
//
// The observed failure this exists for (epic-yoh, 2026-09-24): four ticks were
// absorbed into the running epic and its close-out was made blocked-by each of
// them — and the live run, whose plan carried none of it, dispatched the
// close-out anyway and opened the epic PR over four open blockers. The three
// answers the run can give, in the order it prefers them:
//
//   - the blocker is one this run has not dispatched yet: it is in the queue,
//     so the blocked tick is requeued BEHIND it and the blocker is dispatched
//     first. This is the whole incident repaired — the absorbed tick is worked
//     by the running run, and the blocked tick waits for it, with no restart.
//   - the blocker is one this run is already holding: it is a claim in the
//     window, so the blocked tick is requeued at the head with its blockers
//     refreshed in place — mayAdmit's own holder boundary then holds it until
//     the holder closes and the next re-derivation drops the edge. No settle
//     runs again until then, so the edge costs one dispatch-time read, not one
//     per poll.
//   - neither: nothing this run is doing can close the blocker, and a run that
//     waits anyway is a spin nobody asked for. The refusal names the blocker
//     and says the repair is the blocker's own, wherever it lives — a re-run
//     of the epic resumes from the graph as it stands.
//
// The plan is re-derived before any of the three answers is chosen, because
// the edge may name a tick the plan does not carry at all — a tick created
// after the last re-derivation — and the queue placement question ("is the
// blocker in the queue?") is a question about the plan as it should be, not as
// it was.
func (r *Reconciler) requeueBlocked(ctx context.Context, plan, queue []planEntry, entry planEntry,
	blocked *blockedTickErr, holders []*inflightAttempt) ([]planEntry, []planEntry, error) {
	if blocked.refusal != nil {
		// The close-out's own gate found the open children (tick 3h0), and the
		// incident's timing is why this branch exists: the children landed
		// while the REVIEW ran, a role job settles inline, and nothing replans
		// between the review's close and the close-out's settle. So the run
		// asks the fresh graph one question before the gate's refusal stands:
		// can THIS run still work any of them?
		r.record(entry.TickID, StageWaiting,
			"the close-out is still behind %s — child(ren) of the epic its own gate found open at dispatch, added or "+
				"reopened while this run was going: the run asks the fresh graph whether it can work them first, and the "+
				"close-out refuses to start over whatever remains",
			strings.Join(blocked.blockers, ", "))
	} else {
		r.record(entry.TickID, StageWaiting,
			"%s is still behind %s at dispatch — a blocked_by edge the tracker added, or a blocker reopened, while this "+
				"run was going: the edge is honoured BEFORE the tick is dispatched rather than discovered by a worker after "+
				"an hour of thinking",
			entry.TickID, strings.Join(blocked.blockers, ", "))
	}
	plan, queue, err := r.replan(ctx, plan, queue)
	if err != nil {
		return nil, nil, err
	}
	// The blockers the settle read found are the tick's current ones, and the
	// entry carries them so the window's own boundary — which reads the plan,
	// not the tracker — holds the tick while they stand.
	entry.BlockedBy = blocked.blockers
	last := -1
	for i, queued := range queue {
		if slices.Contains(blocked.blockers, queued.TickID) {
			last = i
		}
	}
	if last >= 0 {
		return plan, slices.Insert(queue, last+1, entry), nil
	}
	for _, fl := range holders {
		if slices.Contains(blocked.blockers, fl.entry.TickID) {
			return plan, slices.Insert(queue, 0, entry), nil
		}
	}
	if blocked.refusal != nil {
		// Nothing this run is doing can close the children, so the gate's own
		// refusal stands exactly as it was written: closeout_children_open,
		// filed against the close-out, naming the children in its own words.
		return nil, nil, blocked.refusal
	}
	return nil, nil, r.refuse(RefusedTickBlocked, entry.TickID,
		"%s is blocked by %s, which the tracker still reads as open, and nothing this run is doing can close it: the "+
			"edge is honoured before the tick is dispatched rather than discovered by a worker after one. The blocker is "+
			"outside this run's plan, so its repair belongs wherever the blocker lives; re-run the epic under this run id "+
			"once it closes and the resume re-derives the plan from the tracker as it stands",
		entry.TickID, strings.Join(blocked.blockers, ", "))
}

// resequence refreshes a plan's graph-derived facts from a fresh reading of
// the graph and puts it back in dispatch order.
//
// An entry the fresh graph does not carry keeps what it had. The tracker
// dropping a tick means it closed it, and a closed tick is settled by
// settleBeforeDispatch out of the tracker's own answer when the queue reaches
// it — dropping it here instead would leave its checkpoint row reading ready
// for a tick that is done, which is the disagreement settleClosedTicks exists
// to repair.
func resequence(entries []planEntry, fresh map[string]planEntry) []planEntry {
	out := append([]planEntry{}, entries...)
	for i := range out {
		if now, ok := fresh[out[i].TickID]; ok {
			out[i].Wave, out[i].BlockedBy = now.Wave, now.BlockedBy
		}
	}
	sortPlan(out)
	return out
}

// refreshClaims re-reads each entry's Claimed from a fresh reading of the
// graph. An entry the fresh graph does not carry is a tick the tracker closed,
// and a closed tick holds no claim.
func refreshClaims(entries []planEntry, fresh map[string]planEntry) []planEntry {
	out := append([]planEntry{}, entries...)
	for i := range out {
		now, ok := fresh[out[i].TickID]
		out[i].Claimed = ok && now.Claimed
	}
	return out
}

// movedTicks is the ticks the re-derivation placed in a different wave — the
// whole reason to promote a re-derived plan, and the only thing worth saying
// about one.
func movedTicks(before, after []planEntry) []planEntry {
	var moved []planEntry
	for _, entry := range after {
		if was := waveOf(before, entry.TickID); was != 0 && was != entry.Wave {
			moved = append(moved, entry)
		}
	}
	return moved
}

// waveOf is the wave a plan places one tick in, or zero for a tick it does not
// carry.
func waveOf(plan []planEntry, tick string) int {
	for _, entry := range plan {
		if entry.TickID == tick {
			return entry.Wave
		}
	}
	return 0
}

// excuseWindow forgives the polling gap that the run itself just caused.
//
// The poll IS the keepalive, and Poll concludes a job is WIPED when the
// interval between two polls exceeded the substrate's wipe threshold
// (guards.go). That inference is sound only while the interval measures the
// SUBSTRATE — and with a window it stops doing that: finishTick runs collect,
// integrate, the gate and the close for one tick, one at a time, while the
// rest of the window waits. The default wipe threshold is 20 minutes and a
// gate may run to 45, so the very next poll of a perfectly healthy attempt
// would read as wiped because the run was busy proving another tick.
//
// A refusal manufactured out of the run's own busyness is the worst kind: it
// kills work that was never in trouble and sends a person to look at a
// substrate that did nothing wrong. So the gap the run caused does not count
// against the job.
//
// This forgives only the INFERENCE, never the fact. If the substrate really
// did take the attempt away, the next Inspect says so — `lost` is settled from
// the executor's own answer, not from this clock — and the attempt is refused
// on that evidence instead. And a gap that exceeded the threshold is said out
// loud, because on a substrate that really does reclaim, a 45-minute gate is
// a thing an operator needs to know happened.
//
// # And it is where the stall warning was lost (tick dh1)
//
// The warning lives in addressOnce, so it is only ever evaluated at a POLL —
// and pollWindow does not run while finishTick does. That gap is not small
// and not rare: it is one tick's whole collect, integrate, gate and close.
//
// Measured on epic ncv, 2026-09-18. ef7 was dispatched at 16:51:29 against a
// 900s stall threshold, so the earliest its warning could fire was 17:06:29.
// The run's last poll before that was at 17:02:50, when vyg settled; from
// there it was inside finishTick(vyg) — the integrated gate alone ran from
// 17:03:24 to 17:10:08 — and at 17:10:19 vyg was refused for untriaged
// findings, which STOPS the run. ef7 was therefore never once polled while it
// was eligible to be warned about, and the feed carried no stall line for it
// in that incarnation at all. The next incarnation resumed at 17:33:35 and
// warned at its very first poll, 17:34:05: the machinery was correct the
// whole time and simply never got a turn, 28 minutes late, with 16 of the
// attempt's 60 minutes left to spend.
//
// So the moment the serial half hands the window back is a moment to look at
// the attempts that waited through it — the same moment, and the same loop,
// that already forgives the polling gap it caused.
func (r *Reconciler) excuseWindow(live []*inflightAttempt, gap time.Duration) {
	for _, fl := range live {
		r.probeProgress(fl)
		r.announceStall(fl)
		if gap > r.wipeThreshold {
			r.record(fl.entry.TickID, StageWaiting,
				"%s went unpolled for %s while another tick was being integrated and gated — "+
					"longer than the substrate's wipe threshold of %s. The gap is the run's own, so it is not "+
					"read as a wipe; if the substrate did take the attempt away, its next inspection says so",
				r.attemptName(fl.marker.TickID, fl.marker.Attempt), gap.Round(time.Second), r.wipeThreshold)
		}
		r.noteAlive(fl.marker.JobID)
	}
}

// adoptTicks takes origin's checkpoint rows as the run's tick state, keeping
// what the run already knows about the attempts it is currently holding.
//
// The rule is "durable wins, except about what I am holding right now". A
// checkpoint is written by this run, so for a settled tick origin's row is at
// worst as new as memory. For an attempt still in flight it can be older — the
// run may have learned the attempt's number since — and older must not
// overwrite newer.
func (r *Reconciler) adoptTicks(durable []runstate.TickState, live []*inflightAttempt) {
	if len(live) == 0 {
		r.ticks = durable
		return
	}
	held := make(map[string]runstate.TickState, len(live))
	for _, fl := range live {
		for _, ts := range r.ticks {
			if ts.TickID == fl.marker.TickID {
				held[ts.TickID] = ts
				break
			}
		}
	}
	next := make([]runstate.TickState, 0, len(durable)+len(held))
	seen := map[string]bool{}
	for _, ts := range durable {
		if mine, ok := held[ts.TickID]; ok {
			ts = mine
		}
		seen[ts.TickID] = true
		next = append(next, ts)
	}
	// A tick this run admitted that origin's checkpoint has not heard of yet
	// would otherwise vanish from the run's own state. Appended in the window's
	// own order, never a map's: a checkpoint whose rows reshuffle between
	// writes is one nobody can diff.
	for _, fl := range live {
		id := fl.marker.TickID
		if ts, ok := held[id]; ok && !seen[id] {
			seen[id] = true
			next = append(next, ts)
		}
	}
	r.ticks = next
}

// announceAbandoned says which attempts the run is walking away from still
// running.
//
// A width greater than one makes this a real possibility for the first time:
// the run stops on one tick's refusal while other workers are still thinking
// in their own worktrees. Those workers do not stop because the run did — their
// supervisors hold their wall clocks — so an operator reading the feed has to
// be told they are out there, and that resuming adopts them rather than
// dispatching over them. Silence here would look exactly like the run having
// finished with them.
//
// It is also the run's LAST look at them (tick dh1), which is why the probe
// and the warning are taken here too. On epic ncv the stop came at 17:10 for
// two attempts that had been eligible for a stall warning since 17:06 and had
// not been polled since 17:02; the run walked away from both without ever
// saying what it had last seen, and the person who resumed it half an hour
// later had nothing to read. A run stopping is the moment its account of a
// live attempt stops being added to, so the account should be current when it
// does.
// announceAbandonedWindow is announceAbandoned for a window that can now hold
// attempts in three states rather than one (tick 9pz).
//
// A worker still thinking gets the sentence it always got. An attempt that
// SETTLED and was not finished — one waiting its turn, or the one the run was
// mid-finish on when something else refused — gets one of its own, because
// "still running" would be false about it and the first move it asks for is
// different: nothing is out there to look at, and what a resumed run does with
// it is collect it, not wait for it.
func (r *Reconciler) announceAbandonedWindow(w *held) {
	r.announceAbandoned(w.live)
	unfinished := make([]*inflightAttempt, 0, len(w.settled)+1)
	for _, s := range w.settled {
		unfinished = append(unfinished, s.fl)
	}
	if w.finish != nil {
		unfinished = append(unfinished, w.finish.fl)
	}
	for _, fl := range unfinished {
		r.record(fl.entry.TickID, StageWaiting,
			"%s had settled and was not finished when the run stopped for another tick's refusal: "+
				"nothing about it is lost — its marker is on the remote and its commits are on its own branch, and "+
				"running the epic again under this run id adopts it by identity and finishes it rather than "+
				"dispatching over it",
			r.attemptName(fl.marker.TickID, fl.marker.Attempt))
	}
}

func (r *Reconciler) announceAbandoned(live []*inflightAttempt) {
	for _, fl := range live {
		r.probeProgress(fl)
		r.announceStall(fl)
		r.record(fl.entry.TickID, StageWaiting,
			"%s is still running and the run is stopping for another tick's refusal: "+
				"nothing about this attempt is lost — its marker is on the remote and its commits are on "+
				"its own branch, and running the epic again under this run id adopts it by identity "+
				"rather than dispatching over it",
			r.attemptName(fl.marker.TickID, fl.marker.Attempt))
	}
}

// mayAdmit answers whether the next plan entry may join the window NOW.
//
// Three boundaries the window never crosses, all of them the tracker's own
// statements about what may overlap:
//
//   - The declared width. [orchestration].max_parallel, narrowed by any
//     [tier_policy.concurrency] bound the wave's tiers carry — the same number
//     the feed's wave-width line quotes, so the run does what it said.
//   - A wave boundary. Ticks in one wave are independent; a later wave exists
//     because something in it is blocked by something in this one. Running
//     across the boundary would dispatch a tick against a base that is missing
//     the very work it was sequenced behind.
//   - A role job. Review and close-out are about a FINISHED epic, so they run
//     alone: nothing else is admitted while one is in flight, and one is not
//     admitted while anything else is.
//
// The boundary is read against the plan AS IT NOW STANDS — the one replan
// re-derives when an attempt settles — and never against the wave number a
// tick carried at run start. The rule it enforces is the semantic one, "do not
// start work that depends on unfinished work", and a tick whose blockers have
// all closed does not depend on anything unfinished whatever number it was
// planned into (tick g50).
//
// It is stated twice because the two statements fail differently. The wave is
// the tracker's own layering, and it is all there is to go on for a graph that
// reports no dependency edges at all; the blocker list is the reason behind
// the number, and it still holds when a layering is stale — which, between two
// settlements, is exactly what a layering is.
// All four boundaries are read against everything the window is HOLDING — the
// live workers, the settled attempts waiting their turn, and the one being
// finished — because all of them are ticks this run has CLAIMED and not closed.
// That includes the declared width, which is the tracker's number before it is
// ours: asking for a claim the width forbids is asking for a refusal, and a run
// must never ask the tracker for something the tracker will refuse (tick 3mp).
func (r *Reconciler) mayAdmit(next planEntry, window *held, plan []planEntry) bool {
	holders := window.holders()
	if len(holders) == 0 {
		return true
	}
	// An attempt this run already has in flight is ADOPTED, not dispatched: it
	// takes no new claim and starts no new work, so neither the width nor a
	// graph boundary is a reason to leave it unaddressed — only a role job the
	// window is holding, which runs alone (runPlan puts these first, so in
	// practice they are admitted before anything else is held).
	if next.InFlight && !isRoleJob(next.Role) {
		for _, fl := range holders {
			if isRoleJob(fl.entry.Role) {
				return false
			}
		}
		return true
	}
	// reconciler-decision:D27:begin:isrole-next — a role job is admitted only
	// when the window holds nothing; the Workflow host's admission carries no
	// such rule (decisions/reconciler-parity.json, D27).
	if isRoleJob(next.Role) {
		return false
	}
	// reconciler-decision:D27:end:isrole-next
	for _, fl := range holders {
		// reconciler-decision:D27:begin:isrole-holder — and nothing is
		// admitted while a role job is held; the Workflow host's admission
		// carries no such rule (decisions/reconciler-parity.json, D27).
		if isRoleJob(fl.entry.Role) {
			return false
		}
		// reconciler-decision:D27:end:isrole-holder
		// reconciler-decision:D26:begin:boundaries — the wave and blocker
		// boundaries this window never crosses; the Workflow host's
		// admission holds neither (decisions/reconciler-parity.json, D26).
		// The holder's wave as the CURRENT plan has it, not as its own entry
		// recorded it at dispatch: that entry is what its tier was derived
		// from and it is deliberately left alone, so the live attempt's place
		// in the graph is asked of the plan instead.
		if holder := waveOf(plan, fl.entry.TickID); holder != 0 && next.Wave > holder {
			return false
		}
		if next.blockedBy(fl.entry.TickID) {
			return false
		}
		// reconciler-decision:D26:end:boundaries
	}
	// claims(), not workers(): the finishing attempt and the settled ones
	// waiting their turn still hold claims, so they still occupy the width.
	// This is the line tick e3c lifts — once a claim can say it is merely
	// integrating, this may count workers again and the slot a settled attempt
	// frees becomes admissible in the moment it frees it (9pz's gain, and
	// TestE3cWillRestoreAdmissionWhileASettledAttemptIsBeingFinished).
	//
	// And the claims the window is NOT holding count too, because tk counts
	// them (epic-yoh): a tick claimed under this epic and not yet closed —
	// rejected and waiting for a person, adopted later in the queue, or held
	// by somebody else entirely. A tick that already holds a claim is the one
	// exception: admitting it takes no new one.
	if next.Claimed {
		return true
	}
	return r.claimsHeld(window, plan) < r.widthForWave(plan, next.Wave)
}

// claimsHeld is how many claims tk counts under this epic as the window sees
// it: every attempt the window holds, plus every tick of the plan the tracker
// reads as claimed that the window is not holding.
func (r *Reconciler) claimsHeld(window *held, plan []planEntry) int {
	count := window.claims()
	holding := map[string]bool{}
	for _, fl := range window.holders() {
		holding[fl.entry.TickID] = true
	}
	for _, entry := range plan {
		if entry.Claimed && !holding[entry.TickID] && r.tickState(entry.TickID) != "closed" {
			count++
		}
	}
	return count
}

// widthForWave is the width this wave may run at: the host's declared number,
// narrowed by whatever [tier_policy.concurrency] bound the wave's own tiers
// carry. It is computed exactly as the planning line that reports it is
// (profiles.go), so the number said and the number obeyed are one number.
func (r *Reconciler) widthForWave(plan []planEntry, wave int) int {
	width := r.hostWidth
	if r.tierPolicy != nil {
		var tiers []runconfig.Tier
		for _, entry := range plan {
			if entry.Wave != wave {
				continue
			}
			outcome, err := r.tierPolicy.Derive(runconfig.TickFacts{
				TickID: entry.TickID, Priority: entry.Priority, Type: entry.Type,
				Role: entry.Role, Labels: entry.Labels, Wave: entry.Wave, Blocks: entry.Blocks,
			}, runconfig.DeriveAttempt{Number: 1})
			if err != nil {
				continue
			}
			tiers = append(tiers, outcome.Tier)
		}
		width = r.tierPolicy.WaveWidth(r.hostWidth, tiers)
	}
	if width < 1 {
		// No declared width is ONE, not unbounded: a run that was never told
		// how wide it may be does what it has always done.
		return 1
	}
	return width
}

// admit runs the serial half of starting a tick: the tracker's own answer
// about whether it is already closed, the strike-out check, the role jobs that
// are dispatched and acted on in one piece, and finally the claim.
//
// A nil attempt with a nil error means the entry needed no wait — it was
// already closed, or it was a role job that completed here.
func (r *Reconciler) admit(ctx context.Context, entry planEntry) (*inflightAttempt, error) {
	done, err := r.settleBeforeDispatch(ctx, entry)
	if err != nil || done {
		return nil, err
	}
	return r.beginTick(ctx, entry)
}

// pollWindow takes one poll of each in-flight attempt and reports the first
// that has settled, by index. It returns -1 when every attempt is still
// running, which is the caller's cue to rest.
func (r *Reconciler) pollWindow(ctx context.Context, live []*inflightAttempt) (int, *subprocess.JobStatus, error) {
	for i, fl := range live {
		status, err := r.addressOnce(ctx, fl)
		if err != nil {
			return i, nil, err
		}
		if status != nil {
			return i, status, nil
		}
	}
	return -1, nil, nil
}

// restWindow is the pause between rounds of polling.
//
// One sleep serves the whole window — polling N attempts must not cost N
// sleeps — and the interval is the shortest any attempt in the window asked
// for, so no attempt is addressed more slowly than its executor's cadence
// (tick u9l). The step cap is still spent per attempt, because it bounds the
// leg each attempt is being addressed in.
func (r *Reconciler) restWindow(live []*inflightAttempt) error {
	if len(live) == 0 {
		return nil
	}
	d := live[0].interval
	for _, fl := range live[1:] {
		if fl.interval < d {
			d = fl.interval
		}
	}
	refreshed := false
	for _, fl := range live {
		if fl.step.Spend(d) != ExceededCap {
			continue
		}
		// This leg is over. The next one is a FRESH step that re-derives its
		// state from durable facts rather than carrying this one's memory
		// (Appendix A #3). The re-derivation is the RUN's and is done once,
		// however many attempts' legs ended together.
		//
		// The re-derivation takes origin's answer for every tick EXCEPT the
		// ones this window is holding, because for those the run's own memory
		// is the newer fact. Replacing the slice wholesale was defensible when
		// exactly one tick was ever in flight; with a window it silently
		// reverts attempts nothing has checkpointed yet.
		//
		// That is not hypothetical. An ADOPTED attempt sets its number at
		// dispatch.go:523 and returns with no checkpoint behind it — a fresh
		// dispatch is serialised at 640, an adopted one is not — so the last
		// tick admitted into a window holds its attempt number only in memory
		// until something else checkpoints. A refresh in that gap reverts it to
		// origin's row, and from then on every waiting, wall-clock and stall
		// line for that tick names the wrong attempt, or none. A resumed run
		// adopts by design, so this is the common path, not the rare one.
		if !refreshed {
			if _, err := r.store.Fetch(); err != nil {
				return err
			}
			if checkpoint, ok, err := r.store.Checkpoint(); err == nil && ok {
				r.sequence = checkpoint.Sequence
				r.adoptTicks(checkpoint.Ticks, live)
			}
			refreshed = true
		}
		fl.step = r.OpenStep(r.stepCap)
	}
	if refreshed {
		return nil
	}
	r.sleep(d)
	return nil
}
