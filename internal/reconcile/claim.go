package reconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Who holds a claim this run did not make, and are they still going (tick 823,
// folding the finding 08e5bcc0).
//
// Since dz1 the window has counted every claim under the epic against its
// width whoever holds it, and admitted a claimed tick only when the claim was
// this run's own. That was right about the width and silent about the other
// question it had stopped asking: whether the holder is still WORKING. With
// room under the width the run claimed a foreign tick again and started a
// worker over another party's live claim (08e5bcc0); with no room it held on
// claim_width until the holder's tick closed — which a holder that had already
// stopped never would, so a re-run of the epic under a new run id held forever
// on the claim its stopped predecessor left (the 823 regression, full suite
// red). Two defects, one seam: the window had no way to tell a live foreign
// claim from a stopped run's, so it could honour neither correctly.
//
// This file owns that distinction, and it is made from DURABLE EVIDENCE
// alone — the rule the parallel-claims learning states: settle in-flight
// state from the records the parties leave, never by trusting the claimer.
// The tracker's claim carries no holder a run can ask (the owner is a
// free-text field, "ticfac" for every run of this binary), but the runs of an
// epic share one integration branch, and on it every run leaves the two
// records that answer the question:
//
//   - its dispatch markers, each naming the tick it claimed and when — the
//     witness of WHO holds a claim;
//   - its checkpoint, whose state says whether the run is over and whose tick
//     rows say whether it finished the tick — the witness of whether that
//     holder is still going.
//
// A claim whose most recent witness is a run whose checkpoint reads terminal,
// and whose account of the tick does not say it closed it, is a stopped run's
// orphan: the holder is gone, its claim stands only because nothing was left
// to release it, and taking it over claims nothing the width has not already
// counted and starts no work a live worker is doing. Every other foreign
// claim — a live run's, a person's, one no record on this branch witnesses —
// is treated as LIVE, whatever the truth: a run that dispatched over it on a
// guess would be the over-claim the width exists to stop, and the cost of the
// conservative reading is one resumable hold, re-derived by the next
// incarnation.

// claimWitness is what the durable records say about one foreign claim: which
// run's dispatch marker most recently witnessed it, and whether that run is
// over with the claim still standing.
type claimWitness struct {
	// holder is the run whose dispatch of the tick is the most recent on the
	// integration branch — the party the records name as holding the claim.
	// Empty when no marker on the branch names the tick at all: a person with
	// tk, or a run from a checkout this branch does not see.
	holder string
	// stale says the holder run is OVER (its checkpoint reads terminal, or
	// the party that runs it says it ended — HolderState) and its own account
	// does not say it closed the tick: the claim stands with nobody behind
	// it, and this run may take it over.
	stale bool
	// unknown says the holder's checkpoint does not read terminal and the
	// party asked whether the holder is still running could not answer. The
	// claim is held — conservatively, as a live one is — but the hold is a
	// wait on an answer, not on a person (RefusedClaimHolderUnknown).
	unknown bool
	// evidence is what the verdict rests on, in words: the checkpoint's
	// state, or the answer the holder's host gave. It rides the takeover's
	// record and the hold's refusal, so either can be checked.
	evidence string
}

// The three answers a claim holder's host can give (tick: the hn6 cloud-run
// stall of run_6ece…, whose predecessor's claim blocked it forever).
//
// A run's checkpoint on the integration branch is the witness claim.go read
// from the start, and it is exactly the witness a run that DIED cannot leave:
// hn6's second cloud run was killed with its checkpoint at "dispatching r5i as
// attempt 3", the factory recorded it failed and its Workflow complete, and
// the next submission held on its claim as a live party's — then held again,
// and again, because nothing about a dead run's records ever changes. So the
// checkpoint is asked first, and when it does not read terminal the party
// that RUNS the holder is asked: the operator's registry and the process
// table for a local run, the factory for a cloud one.
const (
	// HolderAlive: the holder's host vouches it is running.
	HolderAlive = "alive"
	// HolderDead: the holder's host says it ended — finished, failed,
	// cancelled, its Workflow over, or its process gone without releasing.
	HolderDead = "dead"
	// HolderUnknown: nobody could say. The claim is held, never taken over
	// on a guess.
	HolderUnknown = "unknown"
)

// HolderState is one answer to "is run X still running?", with the evidence
// it rests on.
type HolderState struct {
	Verdict  string
	Evidence string
}

// foreignClaims reads the durable records' witness of every tick named: who
// holds its claim, and whether that holder is still going. The ticks asked
// about are the plan's claimed-and-not-own ones; a tick nobody claims is not
// the reader's business.
//
// The holder is the run whose dispatch marker is the MOST RECENT witness of
// the tick — dispatch time first, run id second, so the answer is
// deterministic when two records carry the same stamp. Recency is what makes
// the witness sound: a live run that took a stopped run's tick over has the
// newer marker, and reading only the newest holder never mistakes a dead
// predecessor's orphan for the claim that stands.
//
// Staleness is the holder's own two records agreeing: its checkpoint reads
// terminal, and the row that checkpoint carries for the tick does not say it
// CLOSED it. The row is the difference between an orphan and a released
// claim — a run that closed the tick spent its claim, so a claim standing on
// that tick now belongs to somebody else (a person re-opening it, another
// run) and is live by construction, however dead the run that once held it.
// A missing row does not release the claim: a run that died between its
// marker and its checkpoint left exactly this shape, and its orphan is the
// clearest case there is. A checkpoint that cannot be read at all says
// nothing, and nothing is the live answer.
//
// A checkpoint that does not read terminal is not proof of life either: a run
// that DIED never writes that it did (the hn6 cloud-run stall). So when one
// is configured, the holder's host is asked (Options.ClaimHolder): dead is a
// stale claim like a terminal checkpoint's, alive is live, and an answer
// nobody could give holds the run as a wait rather than a person's hold.
func (r *Reconciler) foreignClaims(ticks []string) (map[string]claimWitness, error) {
	// The answer is a question to another host, bounded by that host's own
	// client timeout; nothing the run is doing is cancelled by it.
	ctx := context.Background()
	out := map[string]claimWitness{}
	if len(ticks) == 0 {
		return out, nil
	}
	attempts, err := r.store.ForeignAttempts()
	if err != nil {
		return nil, fmt.Errorf("reconcile: read the other runs' dispatch markers: %w", err)
	}
	asked := map[string]bool{}
	for _, tick := range ticks {
		asked[tick] = true
	}
	latest := map[string]runstate.Attempt{}
	for _, attempt := range attempts {
		if !asked[attempt.TickID] {
			continue
		}
		prior, ok := latest[attempt.TickID]
		// Lexicographic on the stamp, then the run id: RFC3339 sorts as
		// written, and the tie-break keeps the answer deterministic for two
		// markers stamped in the same second.
		if !ok || attempt.DispatchedAt > prior.DispatchedAt ||
			(attempt.DispatchedAt == prior.DispatchedAt && attempt.Provenance.RunID > prior.Provenance.RunID) {
			latest[attempt.TickID] = attempt
		}
	}
	asked = map[string]bool{} // now: the holders whose host has been asked
	answers := map[string]HolderState{}
	for _, tick := range ticks {
		w := claimWitness{}
		if attempt, ok := latest[tick]; ok {
			w.holder = attempt.Provenance.RunID
			checkpoint, exists, err := r.store.ForeignCheckpoint(w.holder)
			if err != nil {
				// A checkpoint this binary cannot read — one written by a NEWER
				// binary, or corrupt — says nothing, and nothing is the live
				// answer: the holder is asked of its host below, exactly as a
				// run that wrote no checkpoint at all is. It is never a
				// run-ending read — one unreadable record of a run that holds
				// a claim must not stop the run that waits on the claim (tick
				// d9d).
				if !errors.Is(err, runstate.ErrUnreadable) {
					return nil, fmt.Errorf("reconcile: read run %s's checkpoint: %w", w.holder, err)
				}
				exists = false
			}
			switch {
			case exists && closedRow(checkpoint, tick):
				// A spent claim: whoever claimed the tick since is live by
				// construction, however dead this holder is.
			case exists && checkpoint.State.Terminal():
				w.stale = true
				w.evidence = fmt.Sprintf("its checkpoint on the integration branch reads %s", checkpoint.State)
			case r.opts.ClaimHolder != nil:
				// The checkpoint does not say the run is over — a run that
				// DIED never writes that it did — so the holder's host is
				// asked. Once per holder: several ticks can share one.
				if !asked[w.holder] {
					asked[w.holder] = true
					answers[w.holder] = r.opts.ClaimHolder(ctx, w.holder)
				}
				answer := answers[w.holder]
				w.evidence = answer.Evidence
				switch answer.Verdict {
				case HolderDead:
					w.stale = true
				case HolderAlive:
				default:
					w.unknown = true
				}
			}
		}
		out[tick] = w
	}
	return out, nil
}

// closedRow reports whether the checkpoint's own account of one tick says the
// run finished it: a closed row is a SPENT claim, and a claim standing on the
// tick now belongs to whoever made it — not to the run whose old marker this
// is. A row the checkpoint does not carry reads as unspent: a run that died
// between its dispatch marker and its tick row left exactly that, and its
// orphaned claim is the case the takeover exists for.
func closedRow(checkpoint *runstate.Checkpoint, tick string) bool {
	for _, row := range checkpoint.Ticks {
		if row.TickID == tick {
			return row.State == "closed"
		}
	}
	return false
}
