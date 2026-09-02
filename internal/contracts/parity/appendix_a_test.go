package parity

import "testing"

// SPEC Appendix A, encoded. Thirteen tests, one per invariant, each named for
// the rule it proves and each carrying the live failure that earned it — §9.2
// preserves the symbols when run-workflow.ts is decomposed, and this preserves
// the reasons on ticfac's side of the split.
//
// Every test does two things:
//
//  1. replays its invariant's sequences against the fake harness, and
//  2. runs the negative control ONE GUARD AT A TIME: with any single guard of
//     that invariant off, at least one sequence must stop matching the
//     contract, and every OTHER invariant must stay green while it is off.
//
// Appendix A's preamble is the standing order for all thirteen: "They are
// conformance tests, not guidance: a reconciler or executor that violates one
// is wrong regardless of what the rest of this document says." The reconciler
// and the local subprocess executor this repository is building run this same
// suite; passing it here, over the fake, is where that starts.

// A1 — A stop is a durable refusal to issue credentials, checked before every
// boot; revoke before teardown, so the money dies first.
//
// Earned by a closeout pass that enforced no budgets and therefore read no stop
// record at all: an operator killing a run mid-closeout was talking to nobody,
// and every closeout reboot minted a fresh credential over their revocation.
// The ordering half is tick gyl's: a cancelled wave torn down before its
// credential was revoked could spend on the way out.
func TestA1StopIsADurableRefusalToIssueCredentials(t *testing.T) {
	c, inv := byID(t, "A1")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A2 — A supervisor cannot report its own death. A record written by the thing
// that may be gone is not evidence of its liveness.
//
// Earned by runs whose supervisor died mid-wave: the index row stayed frozen at
// `running` with the containers orphaned and still spending, because the row
// said the run was alive and nothing outside was asked.
func TestA2ASupervisorCannotReportItsOwnDeath(t *testing.T) {
	c, inv := byID(t, "A2")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A3 — No step outlives the host's cap; long waits are spread across bounded
// steps that re-derive state from durable facts on each leg.
//
// Earned twice over: a dispatch that blocked for up to ninety-one minutes
// inside a step that may execute for ten, and every real wave killing its own
// supervisor at minute ten — run record frozen, lease unrenewed, containers
// orphaned and still spending.
func TestA3NoStepOutlivesTheHostsCap(t *testing.T) {
	c, inv := byID(t, "A3")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A4 — Polling is the keepalive, at an interval well under the substrate's
// sleep/wipe threshold, pinned by a constant or a test rather than by
// arithmetic in two files.
//
// Earned by a run whose accounting was right and whose cadence was not: a
// ceiling crossed at 11:29:52 and the token revoked at 11:31:59, two minutes
// inside a sleep.
func TestA4PollingIsTheKeepalive(t *testing.T) {
	c, inv := byID(t, "A4")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A5 — In-progress work is pushed on a timer; a process exit is never proof of
// completion, and a job that dies leaves its partial work on origin.
//
// Earned by a worker that died mid-turn holding 643 uncommitted lines and
// settled looking finished, and by a boot chain that printed 271 bytes,
// dispatched no wave, pushed no branch — and was recorded COMPLETED and
// charged for.
func TestA5InProgressWorkIsPushedOnATimer(t *testing.T) {
	c, inv := byID(t, "A5")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A6 — A live job is never redispatched. Adopt by stable identity; a fresh
// attempt is created only when the previous one is proven dead.
//
// Earned in Phase 1: a branch with no commits is exactly what a worker that
// has not committed yet looks like. Git evidence would have said redispatch
// while the process was still running. The third answer is the load-bearing
// one — a job that cannot be ASKED is `unknown`, never a redispatch.
func TestA6ALiveJobIsNeverRedispatched(t *testing.T) {
	c, inv := byID(t, "A6")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A7 — Read back after write. A recorded decision or wave is confirmed by
// re-reading it before anything acts on it.
//
// Earned by a wave request whose read-back returned a different pass's object.
// The general shape is worse: a decision write that quietly did not land
// leaves an epic that looks finished and is not.
func TestA7ReadBackAfterWrite(t *testing.T) {
	c, inv := byID(t, "A7")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A8 — An in-flight state is settled by whoever finds it next, from durable
// evidence (does the thing exist?), never by trusting the claimer to return.
//
// Earned by a row set to `committing` before an await: a death there left it
// stuck forever and reported as already decided, and a human button press has
// no source that retries it.
func TestA8AnInFlightStateIsSettledByWhoeverFindsItNext(t *testing.T) {
	c, inv := byID(t, "A8")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A9 — Never collapse distinct failure classes into one message.
//
// Earned by a run that read "the dispatch lease was lost to another run" when
// no other run existed: its lease had lapsed under a wave that renewed
// nothing, and that message sent the diagnosis looking for a competing run for
// as long as it stood.
func TestA9NeverCollapseDistinctFailureClasses(t *testing.T) {
	c, inv := byID(t, "A9")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A10 — Boundaries are enforced by the substrate, not requested of the model,
// and every attempt is reported.
//
// Earned by a worker that committed tracker state although its prompt forbade
// it in as many words. Compliance is a property of the model; a boundary the
// substrate can enforce must not rest on instruction-following.
func TestA10BoundariesAreEnforcedByTheSubstrate(t *testing.T) {
	c, inv := byID(t, "A10")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A11 — A struck-out unit is released by a person, never by the clock.
//
// Earned by a table recording "this branch was given up on" that had two write
// sites and ZERO reads: a rolling window re-opened the branch a day later, it
// silently resumed spending, and the human paged once was never told again.
func TestA11AStruckOutUnitIsReleasedByAPerson(t *testing.T) {
	c, inv := byID(t, "A11")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A12 — Effective budgets are reported after clamping: say the number that
// will govern, at submission.
//
// Earned by an operator who asked for 40 and got 8, because the deployment
// ceiling was 8 and a submission may only lower a budget. The policy was right
// and silent, and the first place the real number appeared was the
// cancellation that ended the run.
func TestA12EffectiveBudgetsAreReportedAfterClamping(t *testing.T) {
	c, inv := byID(t, "A12")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}

// A13 — Evidence is fingerprinted to what it evaluated — source SHA,
// integration SHA, config digest, profile digest — and publication checks
// freshness against the current target.
//
// Earned by green gates reported for targets they had not evaluated: parallel
// ticks each green alone, with only the INTEGRATED gate seeing the break, and
// the break landing in the innocent tick.
func TestA13EvidenceIsFingerprintedToWhatItEvaluated(t *testing.T) {
	c, inv := byID(t, "A13")
	runSequences(t, c, inv)
	disablingTheGuardBreaksIt(t, c, inv)
}
