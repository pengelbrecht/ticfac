package reconcile

import (
	"context"
	"fmt"
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

// runPlan works the plan and reports the ticks that were rejected.
func (r *Reconciler) runPlan(ctx context.Context, plan []planEntry) ([]string, error) {
	var failed []string
	var live []*inflightAttempt
	queue := plan
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
		r.setTick(tick, "rejected")
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

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		for !stopped && len(queue) > 0 && r.mayAdmit(queue[0], live, plan) {
			entry := queue[0]
			queue = queue[1:]
			fl, err := r.admit(ctx, entry)
			if err != nil {
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
			if fl != nil {
				live = append(live, fl)
			}
		}

		if stopped {
			r.announceAbandoned(live)
			return failed, nil
		}
		if len(live) == 0 {
			if len(queue) == 0 {
				return failed, nil
			}
			continue
		}

		settled, status, err := r.pollWindow(ctx, live)
		if settled < 0 {
			if err != nil {
				return nil, err
			}
			if err := r.restWindow(live); err != nil {
				return nil, err
			}
			continue
		}

		// The attempt leaves the window whichever way it ends. A refusal
		// raised while ADDRESSING it — unaddressed, wiped, past its wall
		// clock — is the tick's refusal exactly as a refusal from its gate is,
		// and reaches the feed the same way; the difference is only where the
		// run was standing when it learned.
		fl := live[settled]
		live = append(live[:settled], live[settled+1:]...)
		if err == nil {
			startedFinishing := r.now()
			err = r.finishTick(ctx, fl, status)
			r.excuseWindow(live, r.now().Sub(startedFinishing))
		}
		if err != nil {
			var refusal *Refusal
			if !asRefusal(err, &refusal) {
				return nil, err
			}
			stopped = true
			if cErr := reject(fl.entry.TickID, refusal); cErr != nil {
				return nil, cErr
			}
			r.announceAbandoned(live)
			return failed, nil
		}

		// An attempt has settled, which is the only moment the readiness of
		// any OTHER tick of this epic can have changed — so it is the moment
		// the plan is asked whether it still describes the graph it came from.
		plan, queue, err = r.replan(ctx, plan, queue)
		if err != nil {
			return nil, err
		}
	}
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
// So the tracker is asked again. It is the authority on what is closed —
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
	fresh := map[string]planEntry{}
	for _, entry := range planFrom(graph) {
		fresh[entry.TickID] = entry
	}

	rederived := resequence(plan, fresh)
	moved := movedTicks(plan, rederived)
	if len(moved) == 0 {
		return plan, queue, nil
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
	if refusal := r.checkWaveComposition(rederived); refusal != nil {
		r.record("", StageReplanned,
			"%s is no longer blocked and would join wave %d, but the re-derived plan cannot merge, so the run "+
				"keeps the sequencing it was admitted with and dispatches them one after the other: %s",
			moved[0].TickID, moved[0].Wave, refusal.Message)
		return plan, queue, nil
	}

	for _, entry := range moved {
		r.record(entry.TickID, StageReplanned,
			"the tracker now layers %s in wave %d rather than the wave %d it was planned into: what it was "+
				"sequenced behind has closed since this run started, so it is admissible now and does not wait "+
				"for a restart to be re-planned",
			entry.TickID, entry.Wave, waveOf(plan, entry.TickID))
	}
	r.recordWaveCompositionDecision(rederived)
	return rederived, resequence(queue, fresh), nil
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
				"attempt %d of %s went unpolled for %s while another tick was being integrated and gated — "+
					"longer than the substrate's wipe threshold of %s. The gap is the run's own, so it is not "+
					"read as a wipe; if the substrate did take the attempt away, its next inspection says so",
				fl.marker.Attempt, fl.marker.TickID, gap.Round(time.Second), r.wipeThreshold)
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
func (r *Reconciler) announceAbandoned(live []*inflightAttempt) {
	for _, fl := range live {
		r.probeProgress(fl)
		r.announceStall(fl)
		r.record(fl.entry.TickID, StageWaiting,
			"attempt %d of %s is still running and the run is stopping for another tick's refusal: "+
				"nothing about this attempt is lost — its marker is on the remote and its commits are on "+
				"its own branch, and running the epic again under this run id adopts it by identity "+
				"rather than dispatching over it",
			fl.marker.Attempt, fl.marker.TickID)
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
func (r *Reconciler) mayAdmit(next planEntry, live []*inflightAttempt, plan []planEntry) bool {
	if len(live) == 0 {
		return true
	}
	if isRoleJob(next.Role) {
		return false
	}
	for _, fl := range live {
		if isRoleJob(fl.entry.Role) {
			return false
		}
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
	}
	return len(live) < r.widthForWave(plan, next.Wave)
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
