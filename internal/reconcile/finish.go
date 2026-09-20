package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// Finishing a tick, one step at a time (tick 9pz).
//
// # What was wrong
//
// The dispatch window admits an attempt the moment a slot frees — it does not
// wait for a wave. But the FINISH ran inline in the run loop: collect,
// integrate, the integrated gate, the close, and only then the teardown. The
// loop admits at the top, so for the whole of that leg the run admitted
// nothing, however many ticks were ready and however many slots were free.
//
// Measured on this repository's own three epic feeds — every settlement whose
// next admission followed its gate directly, so the number is the block and not
// some other wait — a finish cost the window a median of 7m19s, of which the
// integrated gate was 89% (median 6m32s); collect was 13-42s and the close
// about 30s. On an 18-tick epic that is over two hours of a window admitting
// nothing. The worst single case in the sample was a 42-minute gate.
//
// # The two halves of the fix
//
// FIRST, the worker is FINISHED once its evidence is collected. Its commits are
// on the remote (preserveAttemptWork pushes them inside the collect) and its
// report is archived beside its attempt record rather than left in the worktree
// (tick 35h, subprocess/collect.go's artifacts), so integrate, the gate and the
// close read git refs and the tracker and never the worktree. The worker's
// credential and worktree therefore go HERE rather than after the close, and
// from that moment it holds no slot: max_parallel bounds LIVE WORKERS — a
// model, a pane, CPU — not the orchestrator's bookkeeping. What does NOT go is
// the branch: a refusal is what a person reads next and the branch is where
// they read it from, so the branch is kept exactly as every refusal path keeps
// it, and the close retires it afterwards precisely where it always did.
//
// SECOND, the finish stops blocking the loop. It is a state machine the run
// loop advances ONE STEP per round, the way it already polls live attempts —
// and that is why there is not a goroutine anywhere near it. This package has
// zero by design: its three worst defects were all process-global git state
// written by two parties at once, and a run that advances a state machine from
// one goroutine cannot produce a fourth. It is also what makes a run
// reconstructible — every state it passes through is a value some later step
// reads, not a stack frame nobody can see.
//
// # What stays exactly as it was
//
// Finishing is still SERIAL. There is one integration branch and one gate at a
// time, in settle order; a second settled attempt waits its turn rather than
// starting a finish of its own. A refusal still STOPS the run before anything
// else merges. And an attempt being finished still holds its place in the
// GRAPH — its wave, and the ticks it blocks — so a newly admitted attempt is
// still one the tick being finished does not stand in front of, and still
// branches from the integration branch as it stands.

// finishStage is how far through its finish an attempt has got.
type finishStage int

const (
	finishCollecting finishStage = iota
	finishIntegrating
	finishGating
	finishClosing
	finishDone
)

// finishing is one settled attempt being taken from settled to closed.
type finishing struct {
	fl     *inflightAttempt
	status *subprocess.JobStatus
	stage  finishStage

	// startedAt is when this finish began, which is what the window's other
	// attempts are excused for when it ends (excuseWindow).
	startedAt time.Time

	collected *subprocess.Collection
	merged    merge
	gate      *gateProgress

	// released says the worker behind this finish is gone — credential revoked,
	// worktree removed — so it no longer counts against the declared width, and
	// nothing may tear it down a second time.
	released bool
}

// entry is the plan entry of the tick being finished. It is what the admission
// check reads: a tick mid-finish is still holding its place in the graph.
func (f *finishing) entry() planEntry { return f.fl.entry }

// beginFinish starts the finish of one settled attempt.
func (r *Reconciler) beginFinish(fl *inflightAttempt, status *subprocess.JobStatus) *finishing {
	return &finishing{fl: fl, status: status, stage: finishCollecting, startedAt: r.now()}
}

// advanceFinish takes one finish one step and reports whether it is over.
//
// The steps are the ones finishTick always ran, in the order it always ran them
// — collect, integrate, gate, close, clean up. The only thing that changed is
// that the caller gets the turn back between them, and between every poll of a
// running gate command.
func (r *Reconciler) advanceFinish(ctx context.Context, f *finishing) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	switch f.stage {
	case finishCollecting:
		return false, r.finishCollect(ctx, f)
	case finishIntegrating:
		return false, r.finishIntegrate(f)
	case finishGating:
		return false, r.finishGate(ctx, f)
	case finishClosing:
		return true, r.finishClose(ctx, f)
	default:
		return true, nil
	}
}

// finishCollect is the first step: take the attempt's evidence, and then let
// the worker go.
func (r *Reconciler) finishCollect(ctx context.Context, f *finishing) error {
	fl := f.fl
	marker := fl.marker
	tick := marker.TickID

	// A resumed run does not collect an attempt it has already MERGED. The
	// refusal that stopped the previous incarnation — the gate, or the
	// freshness check after it — tore the attempt down, so its worktree is
	// gone, and a second collect would report a missing report this run
	// removed itself instead of the verdict the attempt really had. Nothing is
	// merged on the strength of this: integrate reports "already contained"
	// and merges nothing, and what decides the close is the gate, which does
	// run again.
	integrated, err := r.integratedHead(marker)
	if err != nil {
		return err
	}
	if integrated != "" {
		r.record(tick, StageCollected,
			"attempt %d is already merged into %s at %s; it is not collected a second time",
			marker.Attempt, r.branch, short(integrated))
		// Nothing was collected, so nothing is durable that was not already:
		// this attempt's worker is held to the end, exactly as it was before,
		// and the close retires it. There is no evidence to stand the early
		// release on and an early release is not a thing to do on faith.
		f.stage = finishIntegrating
		return nil
	}

	collected, err := r.collect(ctx, fl.handle, fl.executor, marker, f.status)
	if err != nil {
		// The collect's own refusals tear their attempt down where they are
		// raised, which is why nothing is disposed here.
		return err
	}
	f.collected = collected
	r.releaseWorker(f)
	f.stage = finishIntegrating
	return nil
}

// finishIntegrate merges the attempt into the integration branch.
func (r *Reconciler) finishIntegrate(f *finishing) error {
	merged, err := r.integrate(f.fl.marker, f.collected)
	if err != nil {
		r.disposeFinished(f, err)
		return err
	}
	f.merged = merged
	f.stage = finishGating
	return nil
}

// finishGate runs the integrated gate, a step at a time.
func (r *Reconciler) finishGate(ctx context.Context, f *finishing) error {
	fl := f.fl
	if f.gate == nil {
		g, err := r.beginGate(fl.marker, f.merged)
		if err != nil {
			r.disposeFinished(f, err)
			return err
		}
		f.gate = g
		return nil
	}
	done, err := r.stepGate(ctx, fl.marker, f.merged, f.gate)
	if err != nil {
		r.disposeFinished(f, err)
		return err
	}
	if done {
		f.stage = finishClosing
	}
	return nil
}

// finishClose is the verdict on the gate, the close behind it, and the last of
// the attempt.
func (r *Reconciler) finishClose(ctx context.Context, f *finishing) error {
	fl := f.fl
	if err := r.closeAfterGate(ctx, fl.entry, fl.marker, f.collected, f.merged, f.gate); err != nil {
		r.disposeFinished(f, err)
		return err
	}
	// Cleanup is LAST, and only after the close. A cleanup before the close
	// throws away the only copy of what was closed — which is still true of the
	// BRANCH, and is what this call is now for: the worktree and the credential
	// went at the collect, and the branch goes here, where it always went.
	r.cleanUp(fl.handle, fl.executor, fl.marker)
	f.stage = finishDone
	return nil
}

// releaseWorker lets the worker go the moment its evidence is durable.
//
// Everything the rest of the finish reads is a git ref or the tracker: integrate
// merges a commit that is already on the remote (the collect pushed it), the
// gate runs in a throwaway worktree of its own checked out from the integration
// branch, the freshness check reads origin, and the close talks to the tracker.
// The attempt's OWN worktree is read by nobody after the collect, and its report
// is archived beside its attempt record where the teardown cannot reach it
// (tick 35h). So holding the worker through the gate held a pane, a credential
// and a checkout for a party that had already gone home.
//
// The BRANCH stays. That is the whole difference between this and the cleanUp
// that follows the close: a refusal from the integrate, the gate or the close is
// what a person reads next, and the branch is where they read the commits it is
// about — so this teardown keeps it, and the close retires it afterwards exactly
// where it always did.
func (r *Reconciler) releaseWorker(f *finishing) {
	fl := f.fl
	if f.released || fl.handle == nil || fl.executor == nil {
		return
	}
	f.released = true
	r.tearDown(fl.handle, fl.executor, fl.marker, fmt.Sprintf(
		"attempt %d of %s is collected: its commits are on %s and its report is archived beside its attempt "+
			"record, so the worker is finished and its credential and worktree go now rather than after an "+
			"integrate, a gate and a close it is no part of. The branch is kept; the close retires it",
		fl.marker.Attempt, fl.marker.TickID, r.opts.Remote), true)
}

// disposeFinished is disposeRefused for a finish whose worker may already be
// gone.
//
// A refusal raised after the collect used to tear the attempt down here, keeping
// the branch. The release at the collect has already done exactly that, so
// there is nothing left to dispose of and a second teardown would only say so
// twice. What is still owed is the sentence: a person reading the feed at a
// refusal should not have to infer from an earlier line that the worktree is
// already gone.
func (r *Reconciler) disposeFinished(f *finishing, err error) {
	if !f.released {
		r.disposeRefused(f.fl.handle, f.fl.executor, f.fl.marker, err)
		return
	}
	var refusal *Refusal
	if !asRefusal(err, &refusal) {
		return
	}
	r.record(f.fl.marker.TickID, StageCleanedUp,
		"attempt %d of %s was refused (%s): %s. Its worktree and credential went when it was collected; its "+
			"branch is still there, with the commits the refusal is about",
		f.fl.marker.Attempt, f.fl.marker.TickID, refusal.Reason, firstLine(refusal.Message))
}

// The blocking finish driver that used to live here — finishTick, and
// processTick above it in dispatch.go — is gone (tick bhc). Tick 9pz turned
// the finish into a state machine the WINDOW drives one step per round
// (window.go), so that it can admit and poll between the steps, and from that
// day the two of them had no caller: what they documented was the sequential
// run loop, which no longer exists.
//
// What they said that is still true has moved to the state machine itself:
// this half is deliberately NOT concurrent, whatever the window's width, since
// there is one integration branch and a gate that ran on a tree other than the
// one being closed proves nothing about it — so attempts queue and go through
// one at a time, in the order they settled (advanceFinish above,
// pollWindow in window.go).
