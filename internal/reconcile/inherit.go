package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The untriaged drafts every ENDED earlier run of this epic left behind
// (tick d23).
//
// A run that dies before its close-out leaves its findings PROPOSED under a
// run id nothing will ever decide them. The close-out's findings gate reads
// only the run's own store (gateCloseoutOnFindings), so a run resumed under
// a NEW id never gated on the dead run's drafts: they could silently never
// gate anything, while a person's decision about them stood — and because a
// run that dies before its close-out raises no run_held line, needs-you
// never said so either (statusmodel's WaitFinding read the newest run's
// records alone).
//
// The claim takeover adopts the drafts of a holder whose claim this run
// takes (takeover.go) — but only that holder, and only when a claim is taken
// at all. A run that died between dispatches, or after its ticks closed but
// before its close-out, holds nothing this run takes over, and its drafts
// had no path into any living decision.
//
// The decision, made here once for the run: ADOPT, not gate on the foreign
// drafts. Every ended sibling run's untriaged proposals become this run's
// own drafts at its start, before anything is planned, so the run's own
// rules decide them (decideUndecidedFindings: absorbed, backlogged, routed)
// and its close-out's gate holds for a person what the rules cannot — the
// same funnel every finding of its own rides, one decision point, at the
// end. Gating on the foreign drafts instead would hold a person over what
// the run's own rules already answer, the step backward the absorption
// channel (npq, aqm) exists not to take; and the model can then read the
// drafts from this run's records alone, keyed by content, one truth.
//
// ADOPTION IS CONSERVATIVE ABOUT WHOSE DRAFTS IT TAKES: a sibling is ended
// only by its own checkpoint's terminal word, or by the answer of the same
// host the claim staleness asks (Options.ClaimHolder). A sibling that may
// still be running — alive, or nobody can say — keeps its drafts: taking a
// live run's drafts would take a decision out of a run that is still making
// it, and its drafts stay visible to needs-you, addressed to its own store.

// StageInheritUnreadable is the line the boot sweep leaves for a sibling run
// whose checkpoint this binary cannot read (tick d9d): the sibling is skipped
// — its untriaged findings stay where they stand — and the line says so,
// because a skip nobody can see is indistinguishable from a sweep that never
// ran. An unreadable checkpoint is a fact about the record, never a reason to
// stop the run: one newer binary's checkpoint, or one corrupt record, must
// not abort every boot of the epic over a record no run of this epic can fix.
const StageInheritUnreadable = "inherit_unreadable"

// adoptInheritedFindings is the boot sweep: every ended sibling run of this
// epic's untriaged drafts, taken into this run's own store before anything
// is planned. Idempotent across incarnations — a draft already in this
// run's store is skipped by the shared core's own dedup, and a sibling whose
// drafts were adopted stays adopted for the incarnation (adoptedFindingsOf).
func (r *Reconciler) adoptInheritedFindings() error {
	if r.store == nil {
		return nil
	}
	runs, err := r.inheritedRuns()
	if err != nil {
		return err
	}
	// The checkpoints first, in run-id order: this epic's siblings kept, every
	// other epic's left alone, and the ones this binary cannot read skipped
	// with a line said about each.
	type sibling struct {
		id         string
		checkpoint *runstate.Checkpoint
	}
	ours := []sibling{}
	for _, runID := range runs {
		checkpoint, ok, err := r.store.ForeignCheckpoint(runID)
		if err != nil {
			// A checkpoint this binary cannot read — one written by a NEWER
			// binary, or corrupt — says nothing: not which epic the run is,
			// not that it ended. The sweep's own rule answers: a sibling whose
			// records cannot say is a sibling not ended, so its drafts stay
			// where they stand. What this must never be is a boot failure: the
			// sweep reads every run on the branch, other epics' included, over
			// a record no run of this epic can fix — one unreadable checkpoint
			// used to abort EVERY boot of the epic, forever (tick d9d).
			if !errors.Is(err, runstate.ErrUnreadable) {
				return fmt.Errorf("reconcile: read run %s's checkpoint to take over its untriaged findings: %w",
					runID, err)
			}
			r.record("", StageInheritUnreadable,
				"run %s's checkpoint cannot be read by this binary (%v); the run boots anyway, and the "+
					"run's untriaged findings are left where they stand",
				runID, err)
			continue
		}
		// A sibling with no checkpoint never drafted a finding: findings are
		// drafted at collect, and collect happens after the admitted
		// checkpoint a first dispatch already cost.
		if !ok {
			continue
		}
		// Only this epic's runs: the base branch folds other epics' runs in,
		// and their drafts are their own epics' close-outs' business.
		if checkpoint.EpicID != r.opts.EpicID {
			continue
		}
		ours = append(ours, sibling{id: runID, checkpoint: checkpoint})
	}
	// The host question — is this non-terminal sibling still going? — is
	// asked CONCURRENTLY, once per sibling: each ask is a round trip to
	// another host (the factory, the process table) bounded by that host's
	// own client timeout, and a serial sweep would pay every sibling's
	// latency before the run plans anything (tick d9d). The answers are
	// folded back in the sweep's own run-id order, so what the run does with
	// them is as deterministic as it ever was.
	answers := map[string]HolderState{}
	if holder := r.opts.ClaimHolder; holder != nil {
		asking := make([]string, 0, len(ours))
		for _, s := range ours {
			if !s.checkpoint.State.Terminal() {
				asking = append(asking, s.id)
			}
		}
		states := make([]HolderState, len(asking))
		var wg sync.WaitGroup
		for i, runID := range asking {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Bounded by that host's own client timeout and never by
				// this run's context — the same rule claim.go keeps, so a
				// cancellation of the run is not a cancellation of the
				// question.
				states[i] = holder(context.Background(), runID)
			}()
		}
		wg.Wait()
		for i, runID := range asking {
			answers[runID] = states[i]
		}
	}
	for _, s := range ours {
		evidence, ended := r.endedRun(s.checkpoint, answers[s.id])
		if !ended {
			continue
		}
		if err := r.adoptRunFindings(s.id, evidence,
			"at this run's start, before anything is planned"); err != nil {
			return err
		}
	}
	return nil
}

// inheritedRuns is every OTHER run with a checkpoint on this integration
// branch, ordered by run id: the runs whose records a boot can read.
func (r *Reconciler) inheritedRuns() ([]string, error) {
	runs := []string{}
	for _, path := range r.store.List(runstate.Root + "/runs") {
		rest, ok := strings.CutPrefix(path, runstate.Root+"/runs/")
		if !ok {
			continue
		}
		runID, ok := strings.CutSuffix(rest, "/checkpoint.json")
		if !ok || runID == "" || runID == r.runID || strings.Contains(runID, "/") {
			continue
		}
		runs = append(runs, runID)
	}
	return runs, nil
}

// endedRun says whether a sibling run of this epic is over, and on what
// evidence. The sibling's own checkpoint is asked first — terminal is the
// one record a run that ended by its own account writes — and a checkpoint
// that cannot say (a run that DIED never writes it: the hn6 cloud-run stall)
// is answered by the host the claim staleness asks (Options.ClaimHolder),
// the answer the sweep has already asked for alongside every other
// non-terminal sibling's. Alive and unknown are both NOT ended, for the
// reason the file header keeps: a live sibling's drafts are its own to
// decide, and a question nobody can answer holds nothing here.
func (r *Reconciler) endedRun(checkpoint *runstate.Checkpoint, answer HolderState) (string, bool) {
	if checkpoint.State.Terminal() {
		return fmt.Sprintf("its checkpoint on the integration branch reads %s", checkpoint.State), true
	}
	if answer.Verdict != HolderDead {
		return "", false
	}
	if answer.Evidence == "" {
		answer.Evidence = "its host says it ended"
	}
	return answer.Evidence, true
}
