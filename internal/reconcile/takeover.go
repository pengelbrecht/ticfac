package reconcile

import (
	"fmt"
	"sort"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// Taking over a dead run's claim, and its work (the hn6 cloud-run stall).
//
// Epic hn6's second cloud run died holding r5i's claim: the factory recorded
// it failed and its Workflow complete, while its checkpoint on the
// integration branch still read "dispatching r5i as attempt 3". `ticfac run
// hn6 --cloud` resumed the epic as a new submission — a new run id, a fresh
// run-state store — and the new run held on foreign_claim, resumed itself,
// held again over an unchanged tree and halted: nothing about a dead run's
// records ever changes, so a hold on it has no event left to wait for. And
// had it dispatched, it would have started r5i from scratch while the dead
// run's attempt 1 sat on origin with two commits and a green gate.
//
// So a claim whose holder is known to have ended (claim.go: its checkpoint
// reads terminal, or its host — the factory, the process table — says it
// ended) is TAKEN OVER, and the takeover is a continuation, not a restart:
//
//   - the holder's newest attempt of the tick that left committed work
//     nothing has merged is CARRIED, exactly as a carried release is (#110,
//     tick 0z0): the new attempt is cut from its head, its marker's
//     resumed_from names the holder run, attempt, ref and commit, and the
//     gate still decides what merges;
//   - the new attempt's marker records the takeover itself (taken_over: the
//     holder and the evidence it was read dead on), whether or not there was
//     work to carry, and the feed says so (claim_taken_over).
//
// Where a holder's work is: its attempt's write ref, on origin or in this
// checkout — and, for a cloudflare-sandbox attempt, the landing branch its
// container pushed, `tick/<epic>/attempt-<n>/<tick>`, or the per-run
// `…-<run id>` the container falls back to when another run's attempt already
// holds that name (image/worker.sh adopt_worker_branch, PR #129). A branch
// that does not descend from the attempt's base is another run's work and is
// never carried.

// takenOver is the takeover a dispatch was made under: the run whose claim it
// took, and the evidence that run was read as ended on. It rides the marker's
// open handle, null when the dispatch took nothing over.
type takenOver struct {
	RunID    string `json:"run_id"`
	Evidence string `json:"evidence"`
}

// StageClaimTakenOver is the line a takeover leaves: the tick, the holder
// whose claim was taken, the evidence it was read as ended on, and whether
// its work is carried.
const StageClaimTakenOver = "claim_taken_over"

// cloudflareSandboxExecutor is the executor whose attempts land on the worker
// container's own branch rather than on the attempt's write ref.
const cloudflareSandboxExecutor = "cloudflare-sandbox"

// takeOverClaim answers the takeover of entry's stale foreign claim: the
// takeover record, and the holder's committed work to carry (nil when it left
// none nothing has merged). Nil, nil when the entry is not a stale foreign
// claim.
func (r *Reconciler) takeOverClaim(entry planEntry) (*carriedWork, *takenOver) {
	holder := entry.ClaimHolder
	if !entry.StaleClaim || holder == "" || holder == r.runID {
		return nil, nil
	}
	taken := &takenOver{RunID: holder, Evidence: entry.ClaimEvidence}
	tick := entry.TickID

	attempts, err := r.store.ForeignAttempts()
	if err != nil {
		r.record(tick, StageClaimTakenOver,
			"%s's claim is taken over from run %s, which ended (%s); its attempts could not be read (%v), so the "+
				"next try starts fresh", tick, holder, entry.ClaimEvidence, err)
		return nil, taken
	}
	var theirs []runstate.Attempt
	for _, attempt := range attempts {
		if attempt.TickID == tick && attempt.Provenance.RunID == holder {
			theirs = append(theirs, attempt)
		}
	}
	// Newest first: the latest attempt that left work is the one whose work
	// is furthest along — a later attempt of the holder's exists only because
	// it rejected or lost an earlier one.
	sort.Slice(theirs, func(i, j int) bool { return theirs[i].Attempt > theirs[j].Attempt })
	for _, attempt := range theirs {
		marker := handleFromMap(attempt.JobHandle)
		if marker.TickID != tick {
			continue
		}
		ref, head := r.foreignAttemptWork(marker, holder)
		if head == "" || r.integrated(head) {
			continue
		}
		carried := marker
		carried.WriteRef = ref
		by := fmt.Sprintf("%s (took over the claim of run %s, which ended: %s)", runReleaser, holder, entry.ClaimEvidence)
		r.record(tick, StageClaimTakenOver,
			"%s's claim is taken over from run %s, which ended (%s): the run does not hold on a claim nobody is "+
				"behind. Its attempt %d left work nothing merged (%s on %s), so the next try starts from it rather "+
				"than redoing it; the gate still decides what merges",
			tick, holder, entry.ClaimEvidence, marker.Attempt, short(head), branchOf(ref))
		return &carriedWork{marker: carried, by: by, at: r.now().UTC().Format(time.RFC3339), runID: holder}, taken
	}
	r.record(tick, StageClaimTakenOver,
		"%s's claim is taken over from run %s, which ended (%s): the run does not hold on a claim nobody is "+
			"behind. None of its %d attempt(s) of %s left work nothing merged, so the next try starts fresh",
		tick, holder, entry.ClaimEvidence, len(theirs), tick)
	return nil, taken
}

// foreignAttemptWork is where another run's attempt left its committed work:
// the ref and its head, or "" when it left none beyond its base. The
// attempt's own write ref is read first — origin, then this checkout, as for
// any rejected attempt — and a cloudflare-sandbox attempt's landing branches
// after it, the per-run fallback before the shared name.
func (r *Reconciler) foreignAttemptWork(marker attemptHandle, holder string) (string, string) {
	if head := r.rejectedWorkHead(marker); head != "" {
		return marker.WriteRef, head
	}
	if marker.Executor != cloudflareSandboxExecutor || marker.BaseSHA == "" {
		return "", ""
	}
	landing := sandboximage.WorkerBranch(fmt.Sprintf("%s/attempt-%d", r.opts.EpicID, marker.Attempt), marker.TickID)
	for _, branch := range []string{landing + "-" + holder, landing} {
		head, err := r.remoteWork(branch, marker.BaseSHA)
		if err != nil || head == "" {
			continue
		}
		// Cut from this attempt's base, or it is another run's attempt that
		// happened to land on the shared name.
		if !r.git.contains(marker.BaseSHA, head) {
			continue
		}
		return refFor(branch), head
	}
	return "", ""
}

// carriedFrom reads the marker a carry names, from the store of the run it
// belongs to: the carry's own RunID when it names one (a claim taken over from
// a run that ended), else run — the run whose marker named the carry, since a
// marker's resumed_from without a run id is about an attempt of its own run.
// The owner it answers is the run the NEXT link of the chain is read against.
func (r *Reconciler) carriedFrom(run string, from *resumedFrom) (*runstate.Attempt, string, bool, error) {
	owner := run
	if from.RunID != "" {
		owner = from.RunID
	}
	if owner == "" || owner == r.runID {
		record, ok, err := r.store.Attempt(from.Attempt)
		return record, r.runID, ok, err
	}
	attempts, err := r.store.ForeignAttempts()
	if err != nil {
		return nil, owner, false, err
	}
	for i := range attempts {
		if attempts[i].Provenance.RunID == owner && attempts[i].Attempt == from.Attempt {
			return &attempts[i], owner, true, nil
		}
	}
	return nil, owner, false, nil
}

// carryKey names one link of a carry chain across runs, for cycle detection:
// two runs' attempt numbers both begin at 1.
func carryKey(run string, from *resumedFrom) string {
	if from.RunID != "" {
		run = from.RunID
	}
	return fmt.Sprintf("%s#%d", run, from.Attempt)
}
