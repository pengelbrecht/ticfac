package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Supervision: continuing across the stops that need nobody (tick go6).
//
// THE OBSERVATION. On 2026-09-19/20 the operator's orchestrator restarted epic
// runs by hand about fifteen times across ncv and dha, and almost every
// restart was the same act: the run stopped, and something typed `ticfac
// run-epic ...` again with identical arguments. The next incarnation adopted
// the in-flight attempts by identity and carried on. Both of that day's
// multi-hour stalls happened inside that loop — the run sat dead while the
// party who had to retype the command was doing something else.
//
// That loop is not a decision. It is a mechanical act performed on the run's
// behalf by whoever happened to be watching, and its latency is the latency of
// a person noticing.
//
// WHY IT LIVES HERE AND NOT IN A SHELL. A wrapper around the binary can see an
// exit code and nothing else. Which refusals may be continued across is a fact
// about the reconciler's own vocabulary — the closed set of reasons below —
// and about the state of origin's integration branch, neither of which an exit
// code carries. A shell loop that re-ran on exit 1 would re-run on a worker
// that answered BLOCKED and on a finding waiting for triage, which is the one
// thing this must never do. So the knowledge goes where the knowledge is, and
// the loop goes with it.
//
// WHAT IT IS NOT. It does not soften a single hold. A stop that needs a person
// still stops, with the same refusal, the same state and the same records; the
// only thing that changed is that it now also says so in its own feed line
// (StageSupervisionHalted) instead of leaving a reader to infer it. And every
// continuation is RECORDED as an intervention — see StageResumedAutomatically
// — because a run that "completed unattended" after forty automatic resumes
// has not demonstrated what that phrase claims, and the count must make that
// visible rather than hide it (tick zi2).
//
// NO GOROUTINE APPEARS IN THIS FILE, for the package's standing reason: a run
// is reconstructible because every step of it happened in one sequence that
// its journal describes. The supervisor is a loop, not a watcher.

// Resume is one automatic continuation: what stopped the incarnation, and what
// the supervisor did about it. Each one is an INTERVENTION in the count tick
// zi2 asks for — a resume nobody typed is still a resume — and the Result
// carries them so a caller reporting an unattended run reports the number
// beside it.
type Resume struct {
	// At is when the stop was seen.
	At time.Time
	// Reason is the refusal reason that stopped the incarnation, as a value —
	// one of the members of the closed set below, never a sentence.
	Reason string
	// TickID is the tick the refusal was about, empty for a run-level stop.
	TickID string
	// Message is what the refusal said, for the person reading afterwards.
	Message string
	// Tree is the integration branch's WORK fingerprint at the stop (see
	// integrationTree): the fact the anti-spin rule is a function of.
	Tree string
	// Waited is the backoff spent before the next incarnation started.
	Waited time.Duration
}

// StoppedRemoteTransient is the one stop reason that is NOT a Refusal: the run
// returned an operational error that runstate classifies as a transient remote
// failure (tick enj). The retry inside the run already waited through its
// bound and gave up; the supervisor waits through a longer one, because a
// remote that was down for five minutes is the case the orchestrator's hand
// loop existed for, and nothing about it is a decision.
//
// It is named here, beside the refusal reasons, so the stop reasons are one
// vocabulary: a Resume's Reason is always a value a caller can switch on.
const StoppedRemoteTransient = "remote_transient"

// StoppedRemoteAuthRefused is the remote refusing this machine's credentials
// past the small bound runstate waits through (tick jsz). It is NAMED so the
// run never halts over "a stop this run has no classification for" when the
// cause is known, and its message says what to check. It is NOT resumable:
// the in-run bound already rode out a blip, so what is left is a key, an
// agent or an access grant, and those are a person's to fix.
const StoppedRemoteAuthRefused = runstate.RemoteAuthRefusedClass

// resumesWithoutAPerson is the closed set of stops the run may continue across
// by itself: the ones that are resumable BY CONSTRUCTION, where the next
// incarnation adopts by identity, re-derives, and continues, and where no
// human judgement exists anywhere in the loop.
//
// It is closed, and it FAILS CLOSED — a reason this function has no evidence
// about needs a person — for exactly the reason runstate's remote
// classification does: continuing across an unrecognised stop is continuing
// across a bug, and the whole point of the classification is that continuing
// is earned by recognising the stop, never assumed.
//
// Each member says why it is here:
//
//   - RefusedCollect: an attempt settled and was rejected with NOTHING lost —
//     no commits, nothing mergeable. The next incarnation redispatches it,
//     which is what the run's own terminal reason already says it does. There
//     is nothing for a person to decide about work that does not exist.
//   - RefusedBlockedRedispatch: a worker stopped to ask and the run answered
//     by dispatching the tick again, one tier up or to decide under the
//     standing orders (tick tyd). The window requeues it in-run; should the
//     refusal escape, the next incarnation reads the recorded answer and
//     dispatches the same way.
//   - RefusedRejectedRedispatch: an attempt was rejected with work and the
//     run disposed of the work itself by the rejection's class — carried
//     for an operational failure, released fresh on the merits
//     (rejected_work.go). The window requeues it in-run; should it escape,
//     the next incarnation reads the recorded release and dispatches the
//     same way.
//   - RefusedClaimWidth: the tracker refused a claim because the epic's
//     declared width is full (tick 3mp). It is a fact about the WORLD — another
//     run's claims, a tick a person holds — not a verdict on this run's work,
//     and it resolves when the other holder closes. The held attempts are
//     intact on origin and the next incarnation adopts them. It stays a hold
//     in holdsForAPerson and it still writes its run_held line: what changed is
//     only that the person it was waiting for was, in every observed case,
//     waiting to type the same command back.
//   - RefusedForeignClaim: a live party holds a claim on a tick this run has
//     room to dispatch but no right to (tick 823, finding 08e5bcc0). The same
//     fact about the world, with the same clock: it resolves when the
//     holder's tick closes — or when the holder's run stops, which the next
//     incarnation reads from the records it left — and re-deriving is the
//     whole of the repair. A claim a STOPPED run left never reaches this
//     stop: the next incarnation reads the holder's terminal checkpoint and
//     takes the orphaned claim over, so what remains here is live by every
//     record this checkout can read.
//   - RefusedClaimHolderUnknown: foreign_claim's conservative twin — the
//     holder's records do not read finished and its host (the factory, the
//     process table) could not say whether it still runs. The next
//     incarnation asks again; nothing on the branch has to change for the
//     answer to, so it also waits on the world (waitsOnTheWorld).
//   - RefusedStale: the integration branch moved under a gate, so its evidence
//     is no longer about what would be published (gate.go). Re-deriving is the
//     entire repair, it is keyed by commit, and a person has no part in it.
//   - RefusedCloseoutCIPending, RefusedCloseoutCIAbsent, RefusedLandCIPending:
//     the run's bound on a wait for CI ran out (waitsOnCI). Waiting is not a
//     decision, and the next incarnation's wait is for a run that exists.
//   - RefusedCloseoutOverRedCI: a close-out answered BLOCKED over code whose
//     CI the run itself reads as red (epic-6in). The next incarnation's
//     admission answers the red CI with the repair job — the tree changes —
//     and dispatches a new close-out over the green, carrying the rejected
//     one's commits (its retro); nobody has anything to decide.
//
// And one hold is continued when the stopped incarnation finds the run can
// decide it (supervisedStop.Decides): a close-out's RefusedRoleAnswer over a
// question in no always-ask class (holdDecidesItself). An always-ask question
// stays a person's.
//
// RefusedGate is deliberately ABSENT, and the omission is the argument. A gate
// that failed is resumable only when the tree has since CHANGED — and under
// supervision nothing changes the tree between incarnations, because the run
// stopped. So a supervised resume of a failing gate would re-run the longest
// thing a run does, against the identical commit, to learn a fact the evidence
// record already states. A gate that fails on an unchanged tree needs a
// person; that is the case go6 names, and it is this one.
func resumesWithoutAPerson(reason string) bool {
	switch reason {
	case RefusedCollect, RefusedClaimWidth, RefusedForeignClaim, RefusedStale, StoppedRemoteTransient, RefusedCloseoutOverRedCI,
		RefusedBlockedRedispatch, RefusedRejectedRedispatch, RefusedClaimHolderUnknown, RefusedNoCapacity:
		return true
	}
	return waitsOnCI(reason)
}

// waitsOnCI reports whether a stop is the run's own bound on a wait for CI —
// the close-out's admission or close, or the readying — running out while CI
// was still pending or had produced no run yet (epic-6in follow-up). Waiting
// on CI is not a decision: nobody has anything to judge, and a person asked
// could only type the same command back. The next incarnation re-derives CI
// from the PR, and a code commit with no run, or only cancelled ones, has its
// workflow started or restarted (closeout_ci.go), so the wait is for a run
// that exists. The tree need not change between two such stops — the world
// being waited on is the forge's, not the branch's — so the anti-spin rule
// does not apply to them; the continuation cap is their bound.
func waitsOnCI(reason string) bool {
	switch reason {
	case RefusedCloseoutCIPending, RefusedCloseoutCIAbsent, RefusedLandCIPending:
		return true
	}
	return false
}

// waitsOnTheWorld is the set of stops the anti-spin rule abstains from: the
// ones whose cause is a world that is not the integration branch, so an
// unchanged tree between two of them says nothing about progress. CI waits,
// and a transient remote — a git host or the factory's sandbox door that did
// not answer (epic hn6's second cloud run: attempt 3's start timed out
// waiting on the door, twice would have read as a spin), and a foreign claim
// whose holder's host could not say whether it still runs — the answer comes
// from that host, never from the branch. The continuation cap bounds them all.
func waitsOnTheWorld(reason string) bool {
	return waitsOnCI(reason) || reason == StoppedRemoteTransient || reason == RefusedClaimHolderUnknown ||
		reason == RefusedNoCapacity
}

// spendsTheCap says whether a continuation across this stop counts against
// the automatic-continuation cap. Every stop does except a substrate with no
// room (capacity.go): a full account is not a run misbehaving, the wait
// before it already spent CapacityWaitBound, and the factory's hourly reclaim
// is what frees it — hn6's cloud run spent its whole cap of twelve on a start
// waiting for a slot, and died of the wait rather than of anything it did.
func spendsTheCap(reason string) bool {
	return reason != RefusedNoCapacity
}

// supervisedStop is one incarnation's stop, reduced to the facts the decision
// is a function of.
type supervisedStop struct {
	Reason  string
	TickID  string
	Message string

	// Tree is the fingerprint of the WORK on origin's integration branch at
	// the moment of the stop, or treeUnreadable. See integrationTree: it is
	// deliberately not the branch head.
	Tree string

	// Decides is set on a hold whose next actor is the RUN after all: a
	// close-out's question in no always-ask class, which the next
	// incarnation disposes of by itself (holdDecidesItself).
	Decides bool
}

// treeUnreadable is the tree of a stop whose integration head could not be
// read — which is the ordinary case for a transient remote failure, since the
// thing that stopped the run is the same thing that would answer the question.
//
// It never matches itself in sameStop below: a tree nobody could read proves
// nothing in either direction, so the anti-spin rule ABSTAINS and the cap is
// what bounds the loop. Making it match instead would halt a run on the second
// blip of a remote that was merely slow to come back; making it a hard halt
// would refuse the one stop class that is pure waiting.
const treeUnreadable = ""

// sameStop is the anti-spin rule: the same refusal, about the same tick, over
// an integration branch whose work fingerprint has not changed. It is the fact
// that the last incarnation changed NOTHING — it did not merge a branch, did
// not close a tick, did not move anything but its own records — and came back
// with the identical complaint. Continuing across that is a spin, and a spin
// is worse than a refusal: it burns the host and looks alive while doing it.
func (s supervisedStop) sameStop(previous supervisedStop) bool {
	if s.Tree == treeUnreadable || previous.Tree == treeUnreadable {
		return false
	}
	return s.Reason == previous.Reason && s.TickID == previous.TickID && s.Tree == previous.Tree
}

// stopOf reads what an incarnation stopped over, and reports whether it
// stopped at all. A completed run is not a stop and there is nothing to
// continue.
//
// Anything that is neither a completed run nor a typed refusal nor a
// classified transient remote failure lands here as an UNRECOGNISED stop, with
// its reason left empty: resumesWithoutAPerson refuses it, which is the
// fail-closed default this whole file rests on.
func (r *Reconciler) stopOf(result *Result, err error) (supervisedStop, bool) {
	stop := supervisedStop{Tree: r.integrationTree()}
	switch {
	case err != nil:
		stop.Message = err.Error()
		stop.Reason = errorStopReason(err)
		return stop, true
	case result == nil:
		stop.Message = "the run returned neither a result nor an error"
		return stop, true
	case result.State == runstate.StateCompleted:
		return supervisedStop{}, false
	case result.Failure != nil:
		stop.Reason = result.Failure.Reason
		stop.TickID = result.Failure.TickID
		stop.Message = result.Failure.Message
		stop.Decides = r.holdDecidesItself(stop)
		return stop, true
	default:
		stop.Message = result.Reason
		return stop, true
	}
}

// errorStopReason names the stop an operational error is, when runstate can
// classify it, and is empty — the unclassified stop — when it cannot.
func errorStopReason(err error) string {
	// An error that says of itself it is a transient remote failure — the
	// sandbox dispatch door not answering (a transport failure or a client
	// timeout) — is one, typed, whatever its text: a door timeout reads
	// "Client.Timeout exceeded while awaiting headers", which no git marker
	// names, and it halted a cloud run as unclassified (epic hn6).
	var transient interface{ TransientRemote() bool }
	if errors.As(err, &transient) && transient.TransientRemote() {
		return StoppedRemoteTransient
	}
	switch runstate.ClassifyRemote(err) {
	case runstate.RemoteTransient:
		return StoppedRemoteTransient
	case runstate.RemoteAuthRefused:
		return StoppedRemoteAuthRefused
	}
	return ""
}

// integrationTree fingerprints the WORK on origin's integration branch: every
// top-level entry of the branch's tree EXCEPT `.ticfac/`.
//
// The branch HEAD would be the obvious thing to read and it is the wrong one.
// The run-state store commits its own records — the checkpoint, the attempt
// markers, the evidence — onto this same branch and pushes each one as it is
// written (runstate's doc.go: durable means pushed). So the head moves on
// every state change a run makes, including the state change that IS the stop:
// two incarnations that did nothing but refuse the same claim would present
// two different heads, and an anti-spin rule reading heads would never fire.
//
// `.ticfac/` is the run's account of itself; everything beside it is what the
// epic is actually producing. A tick that merged, a branch that integrated, a
// close-out that wrote a retro — each of those changes an entry here, and
// nothing a mere refusal writes does. That is exactly the distinction "an
// unchanged tree" is asking for.
//
// A failure to read is not an error: it is one of the answers, and sameStop
// knows what to do with treeUnreadable.
func (r *Reconciler) integrationTree() string {
	head, err := r.git.remoteHead(r.branch)
	if err != nil || head == "" {
		return treeUnreadable
	}
	// The commit has to be in this checkout's object store before it can be
	// listed, and origin is the authority on what the branch is.
	if err := r.git.fetch(r.branch); err != nil {
		return treeUnreadable
	}
	listing, err := r.git.run("", "ls-tree", head)
	if err != nil {
		return treeUnreadable
	}
	var work []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		if line == "" || strings.HasSuffix(line, "\t"+runStateDir) {
			continue
		}
		work = append(work, line)
	}
	return digestOf(append([]string{"integration-tree"}, work...)...)
}

// runStateDir is the one top-level entry the fingerprint above ignores: the
// run's own records, which change whenever the run records anything — a
// refusal included.
const runStateDir = ".ticfac"

// Supervise runs the epic, and keeps running it across the stops that need
// nobody — the loop the orchestrator was performing by hand.
//
// Each continuation is a NEW INCARNATION built from this reconciler's own
// options, in this process: the same run id, the same integration branch, the
// same attempt numbering, so the next run adopts the in-flight attempts by
// identity exactly as a retyped command would. It is built rather than reused
// because an incarnation is a reading of durable state, and reusing one would
// carry the previous reading's memory past the point where the run re-derives
// it — which is the property that makes a run reconstructible at all.
//
// It returns the LAST incarnation's result, carrying every Resume the run
// made. A caller that wants one incarnation and no continuation calls Run.
func (r *Reconciler) Supervise(ctx context.Context) (*Result, error) {
	incarnation := r
	backoff := r.opts.AutoResumeBackoff
	var resumes []Resume
	var previous supervisedStop
	// capped counts the continuations the cap bounds: every resume but the
	// ones spendsTheCap exempts.
	capped := 0

	for {
		result, err := incarnation.Run(ctx)
		if result != nil {
			result.Resumes = resumes
		}
		// A negative cap is supervision turned off at the surface that
		// configures it, so Supervise is exactly Run. It is here rather than in
		// a second entry point so that "is this run supervised" is one number
		// an operator can read off the run's own options.
		if r.opts.AutoResumeCap < 0 {
			return result, err
		}
		// A cancelled context is the caller withdrawing the run, which is the
		// one stop that is neither a refusal nor a fact about the world: it is
		// an instruction, and continuing across an instruction to stop is the
		// worst thing a supervisor could do.
		if ctx.Err() != nil {
			return result, err
		}

		stop, stopped := incarnation.stopOf(result, err)
		if !stopped {
			return result, err
		}

		if halt := haltReason(stop, previous, capped, r.opts.AutoResumeCap); halt != "" {
			incarnation.record("", StageSupervisionHalted,
				"the run stopped and will NOT be continued automatically: %s. What stopped it: %s%s. "+
					"%d automatic continuation(s) preceded this stop",
				halt, reasonOf(stop), detailOf(stop), len(resumes))
			if result != nil {
				result.Halt = halt
			}
			return result, err
		}

		resume := Resume{At: incarnation.now(), Reason: stop.Reason, TickID: stop.TickID,
			Message: stop.Message, Tree: stop.Tree, Waited: backoff}
		// The intervention record, written by the incarnation that STOPPED —
		// it is the one that saw the stop, and its feed is the same file the
		// successor appends to, because the feed is keyed by run id. A hand
		// resume already appended a second incarnation's lines after the
		// first's run_finished; the only thing that was ever missing from that
		// picture is this line, saying a resume happened and nobody typed it.
		count := fmt.Sprintf("number %d of at most %d", capped+1, r.opts.AutoResumeCap)
		if !spendsTheCap(stop.Reason) {
			count = fmt.Sprintf("intervention %d, NOT counted against the cap of %d: the substrate had no room, "+
				"which is a wait on the world", len(resumes)+1, r.opts.AutoResumeCap)
		}
		incarnation.record("", StageResumedAutomatically,
			"the run stopped over %s%s, which is resumable by construction: the next incarnation adopts the "+
				"in-flight attempts by identity, re-derives and continues. Waiting %s first. THIS IS AN "+
				"INTERVENTION and it is counted as one (%s): a resume nobody typed is "+
				"still a resume, and a run reported as unattended must report these beside that claim",
			reasonOf(stop), detailOf(stop), backoff, count)

		incarnation.sleep(backoff)
		if backoff *= 2; backoff > AutoResumeBackoffMax {
			backoff = AutoResumeBackoffMax
		}
		resumes = append(resumes, resume)
		if spendsTheCap(stop.Reason) {
			capped++
		}
		previous = stop

		next, newErr := New(incarnation.opts)
		if newErr != nil {
			// The options that built this reconciler no longer build one: the
			// gate config, a profile or the close-out rule changed under the
			// run. That is a refusal about configuration and never one the
			// supervisor may paper over, so it is returned as what it is,
			// with the stopped incarnation's result for the work it did.
			incarnation.record("", StageSupervisionHalted,
				"the run stopped over %s and could not be continued: the next incarnation refused to build: %v",
				reasonOf(stop), newErr)
			if result != nil {
				result.Halt = "the next incarnation could not be built"
			}
			return result, fmt.Errorf("reconcile: continue run %s after %s: %w", r.runID, reasonOf(stop), newErr)
		}
		next.priorResumes = len(resumes)
		incarnation = next
	}
}

// haltReason says why the supervisor will not continue across this stop, or
// empty if it will. The three answers are ordered by what a reader most needs
// told first: a decision beats a spin, and a spin beats a budget.
func haltReason(stop, previous supervisedStop, made, capped int) string {
	switch {
	case stop.Reason == StoppedRemoteAuthRefused:
		return "the remote refused this machine's credentials past the retry bound — a key, an ssh-agent, " +
			"an access grant or a token's permissions is a person's to fix, and the refusal below says what to check"
	case !resumesWithoutAPerson(stop.Reason) && !stop.Decides:
		return "it needs a person — this is a decision, not a retype, and the run stops for it exactly as it " +
			"always has"
	case stop.sameStop(previous) && !waitsOnTheWorld(stop.Reason):
		// The safety this tick is really about. The last incarnation changed
		// nothing — the integration branch head did not move — and came back
		// with the identical refusal. Another resume would ask the same
		// question of the same world.
		return fmt.Sprintf("the same refusal (%s) came back over an UNCHANGED TREE (%s): nothing the last "+
			"continuation did changed anything, so continuing again is a spin and not progress",
			stop.Reason, short(stop.Tree))
	case made >= capped:
		// What happens when the cap is hit: the run stops HERE, in the state
		// its last incarnation reached, with that incarnation's refusal on the
		// Result and its records already durable on origin. Nothing is
		// rolled back, nothing is re-judged, and the epic is resumable by hand
		// or by another supervised run exactly as it was before — the cap
		// bounds this process's willingness to keep retyping, not the work.
		return fmt.Sprintf("the automatic continuation cap of %d is spent: a run that resumes forever over the "+
			"same epic is a spin, and the cap is the safety that says so", capped)
	}
	return ""
}

// reasonOf is the stop's reason for a feed line: the typed value when there is
// one, and an honest admission when there is not.
func reasonOf(stop supervisedStop) string {
	if stop.Reason == "" {
		return "a stop this run has no classification for"
	}
	if stop.TickID == "" {
		return stop.Reason
	}
	return stop.Reason + " on " + stop.TickID
}

func detailOf(stop supervisedStop) string {
	if stop.Message == "" {
		return ""
	}
	return " (" + firstLine(stop.Message) + ")"
}

// autoResumeNote is the intervention count as it rides a run's own terminal
// reason — the durable checkpoint on origin, which is where a close-out reads
// what happened (tick zi2). Empty for a run nothing resumed, because a run
// that needed none must not carry a sentence saying it needed none: the
// absence is the claim.
func autoResumeNote(resumes int) string {
	if resumes <= 0 {
		return ""
	}
	return fmt.Sprintf(". This incarnation was reached after %d AUTOMATIC CONTINUATION(S) of this run, each one "+
		"an intervention nobody typed: the run did not reach here unattended", resumes)
}

// holdDecidesItself says a close-out's role-answer hold is one the next
// incarnation decides without a person (epic-6in v7z): the close-out asked
// again after being told to decide, its question is in no always-ask class,
// and the rejected-work bound is not spent — so the resume carries its work
// into one more try (heldQuestion, then disposeUndecidedRejection). A genuine
// always-ask question (tick tyd) is still a person's, and so is a close-out
// whose one further try at the bound was already spent.
func (r *Reconciler) holdDecidesItself(stop supervisedStop) bool {
	if stop.Reason != RefusedRoleAnswer || stop.TickID == "" || r.store == nil {
		return false
	}
	if _, err := r.store.Fetch(); err != nil {
		return false
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return false
	}
	var latest *runstate.Attempt
	for i := range attempts {
		if attempts[i].TickID == stop.TickID && (latest == nil || attempts[i].Attempt > latest.Attempt) {
			latest = &attempts[i]
		}
	}
	if latest == nil {
		return false
	}
	marker := handleFromMap(latest.JobHandle)
	blocked, ok := r.blockedAnswerOf(stop.TickID, latest.Attempt)
	if !ok || !closeoutDecidesItself(marker.Role, blocked) {
		return false
	}
	released, err := r.settlements()
	if err != nil {
		return false
	}
	for _, prior := range runReleasesOf(released, stop.TickID) {
		if prior.step == rejectedStepCeiling {
			return false
		}
	}
	return true
}
