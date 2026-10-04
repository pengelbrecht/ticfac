package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// Tick go6: a run exits on stops that only need resuming.
//
// On 2026-09-19/20 the operator's orchestrator retyped `ticfac run-epic ...`
// about fifteen times across epics ncv and dha, almost always with identical
// arguments, and both of that day's multi-hour stalls happened inside that
// loop: the run sat dead while the party who had to retype the command was
// doing something else.
//
// Three facts are proved here, and they are the three halves of the
// distinction the tick is about:
//
//   - a stop that is resumable BY CONSTRUCTION is continued by the run itself,
//     in this process, and the continuation is RECORDED as an intervention;
//   - a stop that needs a PERSON still stops, exactly as it did before;
//   - the same refusal over an UNCHANGED TREE halts rather than spinning.
//
// Each test states what it does with supervision OFF — `autoResumeCap: -1`,
// which is this repository's behaviour before go6 — so the gain is a
// difference in this file and not a claim in a comment.

// A tracker width refusal (tick 3mp) is the first kind: tk refused a claim
// because the epic's declared width was full, which is a fact about the world
// — another run's claims, a tick a person holds — and not a verdict on this
// run's work. The held attempts are intact on origin, the next incarnation
// adopts them by identity, and nothing in the loop is a decision.
//
// With supervision off this run stops with claim_width and closes nothing,
// which is the stop the orchestrator answered by hand fifteen times.
// gate: 11.7s — the only end-to-end proof that the continuation loop keeps an autonomous run alive across a resumable stop; go6's whole subject, and a regression here is silent
func TestAResumableRefusalIsContinuedByTheRunAndCountedAsAnIntervention(t *testing.T) {
	shorttest.LoadBearing(t)
	t.Parallel()
	supervised := fixtureOptions{gate: wideGate}
	f := newFixture(t, supervised)

	// One claim is all this tracker allows, so the wave's second dispatch is
	// refused however the window counts — and then the other holder finishes,
	// which is what the refusal says will happen and the reason a person's
	// only move was to type the command again.
	f.Tracker.refuseClaimsBeyond(1)
	f.Tracker.relentAfterRefusals(1)

	first, result, err := f.supervise(f.Repo, supervised)
	if err != nil {
		t.Fatalf("the supervised run returned an operational error: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the supervised run ended %s: %s (failure %+v)", result.State, result.Reason, result.Failure)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %+v",
			result.Closed, result.State, result.Failure)
	}

	// It continued ITSELF. Nothing here starts a second process and no test
	// code runs the epic twice: the one Supervise call above is the whole run.
	if len(result.Resumes) != 1 {
		t.Fatalf("the run recorded %d automatic continuation(s), want 1: %+v", len(result.Resumes), result.Resumes)
	}
	if got := result.Resumes[0].Reason; got != RefusedClaimWidth {
		t.Errorf("the continuation was recorded over %q, want %s: a resume whose reason is not the refusal's "+
			"own value is one nobody can count by class", got, RefusedClaimWidth)
	}

	// And it is RECORDED as an intervention (tick zi2). The count must be
	// gettable from the records with no inference: the feed carries a line of
	// its own per continuation, and the durable checkpoint reason carries the
	// number, because a count that lives only in gitignored exhaust is one a
	// close-out has to reconstruct all over again — which is the defect zi2
	// exists to remove.
	events := feedStages(t, f.Repo.Dir, first.RunID())
	if n := countStage(events, StageResumedAutomatically); n != 1 {
		t.Errorf("the feed carries %d %s line(s), want 1: a resume nobody can count is a resume that did not "+
			"happen as far as the intervention gate is concerned", n, StageResumedAutomatically)
	}
	if detail := detailOfStage(events, StageResumedAutomatically); !strings.Contains(detail, "INTERVENTION") {
		t.Errorf("the continuation line does not say it is an intervention: %q", detail)
	}
	if !strings.Contains(result.Reason, "AUTOMATIC CONTINUATION") {
		t.Errorf("the run's terminal reason — the durable checkpoint a close-out reads — does not say the run "+
			"was continued automatically: %q", result.Reason)
	}

	// The work was adopted, not redone: the whole premise of continuing is
	// that the next incarnation picks up the attempt the hold preserved.
	adopted := 0
	for _, event := range events {
		if event.Stage == StageAdopted {
			adopted++
		}
	}
	if adopted == 0 {
		t.Error("the continued run dispatched over the held attempt instead of adopting it by identity: the " +
			"work the hold preserved was paid for twice")
	}

	// What this repository did before go6, through the same fixture: one
	// incarnation, a held run, and a person to type the command again.
	unsupervised := fixtureOptions{gate: wideGate, autoResumeCap: -1}
	g := newFixture(t, unsupervised)
	g.Tracker.refuseClaimsBeyond(1)
	g.Tracker.relentAfterRefusals(1)
	_, stopped, err := g.supervise(g.Repo, unsupervised)
	if err != nil {
		t.Fatalf("the unsupervised run returned an operational error: %v", err)
	}
	if stopped.State != runstate.StateFailed || len(stopped.Resumes) != 0 {
		t.Fatalf("with supervision off the run ended %s with %d resume(s); the old behaviour is one incarnation "+
			"that stops and waits for a person", stopped.State, len(stopped.Resumes))
	}
}

// The other kind, and the one that must not move: a worker that COMMITTED and
// answered BLOCKED is an escalation. Its deliverable is a question for a
// person, and dispatching it again would only produce the same question.
//
// Supervision must not touch this. The refusal, the state, the tracker and the
// branch are what they were; the only thing added is a feed line saying, in
// its own stage, that the run was NOT continued and why — which is the
// distinction go6 asks to be made louder rather than softer.
// gate: 1.9s — the other half of that proof: supervision that continued a stop needing a person would run unattended past a decision
func TestAStopThatNeedsAPersonStillStopsUnderSupervision(t *testing.T) {
	shorttest.LoadBearing(t)
	t.Parallel()
	escalating := fixtureOptions{mode: "blocked-with-work"}
	f := newFixture(t, escalating)

	first, result, err := f.supervise(f.Repo, escalating)
	if err != nil {
		t.Fatalf("the supervised run returned an operational error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedNeedsHuman {
		t.Fatalf("the run failed as %+v, want %s: supervision changed the verdict, which is the one thing it "+
			"must never do", result.Failure, RefusedNeedsHuman)
	}
	if len(result.Resumes) != 0 {
		t.Fatalf("the run continued %d time(s) across a stop that needs a person: %+v",
			len(result.Resumes), result.Resumes)
	}

	// The tick is still open, and the escalation is still the operator's to
	// answer. A supervisor that dispatched over it would have answered a
	// person's question by repeating it.
	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == "closed" {
		t.Fatalf("a1 was closed behind a worker that said it was blocked (%s)", current.ClosedReason)
	}

	events := feedStages(t, f.Repo.Dir, first.RunID())
	if n := countStage(events, StageResumedAutomatically); n != 0 {
		t.Errorf("the feed carries %d automatic continuation(s) for a stop that needs a person", n)
	}
	if n := countStage(events, StageSupervisionHalted); n != 1 {
		t.Fatalf("the feed carries %d %s line(s), want 1: a run that stopped for a DECISION has to say so in "+
			"the vocabulary a watcher matches on", n, StageSupervisionHalted)
	}
	halted := detailOfStage(events, StageSupervisionHalted)
	if !strings.Contains(halted, "needs a person") {
		t.Errorf("the halt line does not say the stop needs a person: %q", halted)
	}
	if !strings.Contains(halted, RefusedNeedsHuman) {
		t.Errorf("the halt line does not name the refusal it stopped over: %q", halted)
	}
	if result.Halt == "" || !strings.Contains(result.Halt, "needs a person") {
		t.Errorf("the result does not carry why the run was not continued: %q", result.Halt)
	}
}

// The cap's first and sharper half: a repeated refusal over an UNCHANGED TREE
// halts with the reason.
//
// The tracker here never relents, so every incarnation is refused the same
// claim, for the same tick, and merges nothing. That is a spin, and a spin is
// worse than a refusal: it burns the host and looks alive while doing it. The
// rule that catches it is mechanical — same reason, same tick, and an
// integration branch whose work is byte-for-byte what it was — and it fires on
// the FIRST repeat, long before the cap that stands behind it.
//
// The tree fingerprint deliberately ignores `.ticfac/`: the run-state store
// pushes a record onto this same branch for every state change it makes, so
// the branch HEAD moves even when nothing was produced, and a rule reading
// heads would never fire at all.
// gate: 2.6s — the anti-spin rule is what bounds the continuation loop; without it a resumable stop loops until the cap
func TestTheSameRefusalOverAnUnchangedTreeHaltsInsteadOfSpinning(t *testing.T) {
	shorttest.LoadBearing(t)
	t.Parallel()
	spinning := fixtureOptions{gate: wideGate}
	f := newFixture(t, spinning)

	// The width never frees: whoever holds the other claim never finishes.
	f.Tracker.refuseClaimsBeyond(1)

	first, result, err := f.supervise(f.Repo, spinning)
	if err != nil {
		t.Fatalf("the supervised run returned an operational error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedClaimWidth {
		t.Fatalf("the run failed as %+v, want %s", result.Failure, RefusedClaimWidth)
	}

	// It tried exactly once. The first stop is resumable by construction and
	// nothing yet says otherwise; the second is the same refusal over the same
	// tree, and that is the fact that stops it.
	if len(result.Resumes) != 1 {
		t.Fatalf("the run continued %d time(s) over a refusal that never changed; it must halt on the first "+
			"repeat over an unchanged tree: %+v", len(result.Resumes), result.Resumes)
	}
	if result.Resumes[0].Tree == treeUnreadable {
		t.Error("the stop recorded no tree fingerprint, so the anti-spin rule abstained rather than fired: " +
			"the cap alone would have let this run twelve times")
	}

	events := feedStages(t, f.Repo.Dir, first.RunID())
	halted := detailOfStage(events, StageSupervisionHalted)
	if halted == "" {
		t.Fatalf("the feed carries no %s line: a run that stopped spinning has to say what it stopped over",
			StageSupervisionHalted)
	}
	if !strings.Contains(halted, "UNCHANGED TREE") {
		t.Errorf("the halt line does not say the tree was unchanged: %q", halted)
	}
	if !strings.Contains(halted, RefusedClaimWidth) {
		t.Errorf("the halt line does not name the refusal that repeated: %q", halted)
	}
	// And the count is still honest about what it did before it gave up.
	if !strings.Contains(halted, "1 automatic continuation") {
		t.Errorf("the halt line does not carry the intervention count: %q", halted)
	}
}

// The cap is the backstop behind the rule above, and this is the unit that
// says what it does — including the ordering, which is not cosmetic: a reader
// of a halt line needs to be told about a DECISION before a spin and about a
// spin before a budget, because those three send the next move somewhere
// different.
//
// The end-to-end shape the cap alone catches — a stop that recurs while the
// tree keeps changing, so the anti-spin rule keeps abstaining — is a run that
// makes progress and is refused anyway, which no fixture in this package can
// produce without an executor that lies. The arithmetic is asserted here
// instead of not at all.
// short: the supervisor's rules over synthesised stops
func TestTheCapIsWhatStopsAResumableStopThatKeepsChangingTheTree(t *testing.T) {
	t.Parallel()

	// A resumable stop, over a tree that is different every time: the anti-spin
	// rule abstains and only the cap can end it.
	moving := func(n string) supervisedStop {
		return supervisedStop{Reason: RefusedCollect, TickID: "a1", Tree: "sha256:" + n}
	}
	previous := moving("0")
	for made := 0; made < 3; made++ {
		if halt := haltReason(moving("1"), previous, made, 3); halt != "" {
			t.Fatalf("the supervisor halted after %d continuation(s) with a cap of 3: %s", made, halt)
		}
		previous = moving("1")
		previous.Tree += "x"
	}
	halt := haltReason(moving("2"), previous, 3, 3)
	if !strings.Contains(halt, "cap of 3") {
		t.Fatalf("the cap did not halt a stop that keeps changing the tree: %q", halt)
	}

	// The ordering. A stop that needs a person is reported as that even when
	// it is also a repeat and the cap is also spent, because the person is the
	// only one of the three a reader can act on.
	human := supervisedStop{Reason: RefusedNeedsHuman, TickID: "a1", Tree: "sha256:same"}
	if halt := haltReason(human, human, 99, 3); !strings.Contains(halt, "needs a person") {
		t.Errorf("a stop that needs a person was reported as %q", halt)
	}
	// And a repeat is reported as a spin rather than as a spent cap.
	repeat := supervisedStop{Reason: RefusedCollect, TickID: "a1", Tree: "sha256:same"}
	if halt := haltReason(repeat, repeat, 99, 3); !strings.Contains(halt, "UNCHANGED TREE") {
		t.Errorf("a repeat over an unchanged tree was reported as %q", halt)
	}

	// A tree nobody could read matches nothing: the rule abstains and the cap
	// bounds the loop, which is what keeps a remote outage from halting the
	// run on its second blip.
	blind := supervisedStop{Reason: StoppedRemoteTransient, Tree: treeUnreadable}
	if halt := haltReason(blind, blind, 0, 3); halt != "" {
		t.Errorf("a stop with no readable tree was treated as a repeat: %q", halt)
	}
}

// The classification itself, asserted as the closed set it is. A reason this
// set has no evidence about needs a person: continuing across an unrecognised
// stop is continuing across a bug.
// short: the supervisor's rules over synthesised stops
func TestOnlyTheStopsThatNeedNobodyAreResumable(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{RefusedCollect, RefusedClaimWidth, RefusedForeignClaim, RefusedStale, StoppedRemoteTransient,
		RefusedCloseoutOverRedCI, RefusedCloseoutCIPending, RefusedCloseoutCIAbsent, RefusedLandCIPending, RefusedClaimHolderUnknown,
		StoppedRemoteTokenRefused, RefusedFoldReplan} {
		if !resumesWithoutAPerson(reason) {
			t.Errorf("%s is resumable by construction and the supervisor refuses to continue across it", reason)
		}
	}
	// Every hold that names a person, plus the gate — which is resumable only
	// over a tree that CHANGED, and under supervision nothing changes it.
	for _, reason := range []string{
		RefusedHeld, RefusedUnaddressed, RefusedRejectedWork, RefusedNeedsHuman, RefusedRoleAnswer,
		RefusedFindingUntriaged, RefusedFindingInvalid, RefusedGate, RefusedBoundary, RefusedMerge,
		RefusedBaseRefresh, RefusedEpicAbsent, RefusedWaveOverlap, RefusedUndeclaredTouch, RefusedTierLabel,
		RefusedWiped, RefusedIntegratedHeadMissing,
		RefusedCloseoutCI, RefusedCloseoutCIOnClose, StoppedRemoteAuthRefused, "",
		"something this build has never heard of",
	} {
		if resumesWithoutAPerson(reason) {
			t.Errorf("%s would be continued across automatically; it needs a person, and a supervisor that "+
				"retypes past a decision is a shell loop with better manners", reason)
		}
	}
}

// Tick jsz: epic-yoh halted over "a stop this run has no classification for"
// when the stop was a git auth refusal. A refusal that outlived runstate's
// small bound is a NAMED stop, it halts (a key is a person's to fix), and the
// halt line says what to check. An auth error the supervisor cannot name is
// the regression.
// short: the supervisor's rules over synthesised stops
func TestAPersistentAuthRefusalIsANamedStopThatSaysWhatToCheck(t *testing.T) {
	t.Parallel()
	retry := runstate.RemoteRetry{Sleep: func(time.Duration) {}}
	err := retry.Do("git fetch", func() error {
		return errors.New("git fetch origin epic/yoh: exit status 128: " +
			"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.")
	})
	err = fmt.Errorf("reconcile: read attempt branch of dyo: %w", err)

	reason := errorStopReason(err)
	if reason != StoppedRemoteAuthRefused {
		t.Fatalf("a persistent auth refusal is stop %q, want %q", reason, StoppedRemoteAuthRefused)
	}
	stop := supervisedStop{Reason: reason, Message: err.Error(), Tree: treeUnreadable}
	halt := haltReason(stop, supervisedStop{}, 0, 3)
	if halt == "" {
		t.Fatal("an auth refusal past its bound was continued automatically; a key is a person's to fix")
	}
	line := reasonOf(stop) + detailOf(stop)
	for _, want := range []string{StoppedRemoteAuthRefused, "ssh-agent", "deploy key", "gh auth status"} {
		if !strings.Contains(line, want) {
			t.Errorf("the halt line does not say %q: %s", want, line)
		}
	}
	if strings.Contains(line, "no classification") {
		t.Errorf("the halt line still calls a named stop unclassified: %s", line)
	}

	// And a transient failure is still the resumable one.
	if got := errorStopReason(errors.New("git fetch: exit status 128: Connection reset by github.com port 22")); got != StoppedRemoteTransient {
		t.Errorf("a reset is stop %q, want %q", got, StoppedRemoteTransient)
	}
}

// TestARefusedHTTPSTokenPushIsANamedStopNotAnUnclassifiedOne is epic hn6's
// cloud run (2026-09-29): the orchestrator's push of its own integration
// branch with the per-run GitHub App token came back 403, "Permission to …
// denied to <app>[bot]", and the boot ended as "a stop this run has no
// classification for". It is the named auth refusal, and its halt line sends
// the reader to the token — the App's permissions, workflows: write, the
// credential helper — not to ssh-agent.
// short: the supervisor's rules over synthesised stops
func TestARefusedHTTPSTokenPushIsANamedStopNotAnUnclassifiedOne(t *testing.T) {
	t.Parallel()
	retry := runstate.RemoteRetry{Sleep: func(time.Duration) {}}
	err := retry.Do("git push", func() error {
		return errors.New("git push --force-with-lease=refs/heads/epic/e1:" + strings.Repeat("a", 40) +
			" origin " + strings.Repeat("b", 40) + ":refs/heads/epic/e1: exit status 128: " +
			"remote: Permission to example/repo.git denied to example-app[bot].\n" +
			"fatal: unable to access 'https://github.com/example/repo.git/': The requested URL returned error: 403")
	})
	err = fmt.Errorf("record the gate's evidence for t1: %w", err)

	reason := errorStopReason(err)
	if reason != StoppedRemoteTokenRefused {
		t.Fatalf("a refused https token push is stop %q, want %q", reason, StoppedRemoteTokenRefused)
	}
	stop := supervisedStop{Reason: reason, Message: err.Error(), Tree: "tree-1"}
	// Resumable (epic hn6's cloud run, 2026-09-30: every refused App token
	// went through on a later attempt) — and bounded: the same refusal over
	// an unchanged tree is a spin, and it halts with the remedy in the line.
	if halt := haltReason(stop, supervisedStop{}, 0, 3); halt != "" {
		t.Errorf("a refused App token halted the run on its first stop: %s", halt)
	}
	if halt := haltReason(stop, stop, 1, 3); halt == "" {
		t.Error("the same refused token over an unchanged tree was continued again: that is a spin")
	}
	line := reasonOf(stop) + detailOf(stop)
	for _, want := range []string{StoppedRemoteTokenRefused, runstate.RemoteAuthRefusedClass, "workflows: write", "contents: write",
		"credential helper", "denied to example-app[bot]"} {
		if !strings.Contains(line, want) {
			t.Errorf("the halt line does not say %q: %s", want, line)
		}
	}
	if strings.Contains(line, "no classification") {
		t.Errorf("the halt line still calls a refused token unclassified: %s", line)
	}
}

// Waiting on CI is not a decision (epic-6in follow-up): a CI wait whose bound
// ran out resumes without a person even over an UNCHANGED tree — the world
// waited on is the forge's, not the branch's — and the continuation cap still
// bounds it.
// short: the supervisor's rules over synthesised stops
func TestACIWaitResumesOverAnUnchangedTreeUntilTheCap(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{RefusedCloseoutCIPending, RefusedCloseoutCIAbsent, RefusedLandCIPending} {
		stop := supervisedStop{Reason: reason, TickID: "co", Tree: "tree-1"}
		if halt := haltReason(stop, stop, 1, 12); halt != "" {
			t.Errorf("%s over an unchanged tree halted the run for a person: %q", reason, halt)
		}
		if halt := haltReason(stop, stop, 12, 12); halt == "" {
			t.Errorf("%s continued past the cap: the wait must stay bounded", reason)
		}
	}
	// And the anti-spin rule still holds for everything else.
	stale := supervisedStop{Reason: RefusedStale, TickID: "a1", Tree: "tree-1"}
	if halt := haltReason(stale, stale, 1, 12); halt == "" {
		t.Error("a repeated non-CI stop over an unchanged tree was continued: the anti-spin rule is gone")
	}
}

// transientDoorError is an operational error that says of itself it is a
// transient remote failure — the shape cloudflaresandbox's unreachable door
// takes — carrying text no git marker recognises.
type takenLeaseDoorError struct{}

func (takenLeaseDoorError) Error() string {
	return "the sandbox dispatch door refused (409 lease_held_by): the dispatch lease for p is held by run_b, not run_a"
}

func (takenLeaseDoorError) LeaseTaken() bool { return true }

// hn6's restarted cloud run halted over "a stop this run has no
// classification for" when the door refused its dispatch over the project's
// lease. Another run holding the project is a named stop that needs a person
// (two arbiters may never write one .tick/), and its halt says so in words.
// short: the supervisor's rules over synthesised stops
func TestALeaseAnotherRunHoldsIsANamedStopThatNeedsAPerson(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("start v16: %w", takenLeaseDoorError{})
	if got := errorStopReason(err); got != StoppedLeaseTaken {
		t.Fatalf("a taken lease is stop %q, want %q", got, StoppedLeaseTaken)
	}
	stop := supervisedStop{Reason: StoppedLeaseTaken, TickID: "v16", Tree: "tree-1"}
	halt := haltReason(stop, supervisedStop{}, 0, 3)
	if !strings.Contains(halt, "another run holds this project's dispatch lease") {
		t.Errorf("the halt does not say another run holds the lease: %q", halt)
	}
	if strings.Contains(reasonOf(stop), "no classification") {
		t.Errorf("a taken lease is still unclassified: %q", reasonOf(stop))
	}
}

type transientDoorError struct{}

func (transientDoorError) Error() string {
	return `the sandbox dispatch door could not be reached at https://factory.example.com: Get ` +
		`"https://factory.example.com/api/sandbox/attempts/a1/3": context deadline exceeded ` +
		`(Client.Timeout exceeded while awaiting headers)`
}

func (transientDoorError) TransientRemote() bool { return true }

// Epic hn6's second cloud run halted over "a stop this run has no
// classification for" when the door timed out. A typed transient failure is
// StoppedRemoteTransient however it is wrapped, and — like a CI wait — the
// anti-spin rule abstains from it over an unchanged tree: the world waited on
// is the remote's. The cap still bounds it.
// short: the supervisor's rules over synthesised stops
func TestATransientDoorFailureIsResumedOverAnUnchangedTreeUntilTheCap(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("start a1: %w", transientDoorError{})
	if got := errorStopReason(err); got != StoppedRemoteTransient {
		t.Fatalf("a door timeout is stop %q, want %q", got, StoppedRemoteTransient)
	}
	if got := errorStopReason(fmt.Errorf("start a1: %w", errors.New(transientDoorError{}.Error()))); got != "" {
		t.Errorf("the same text without the type classified as %q: the type, not the prose, is the contract", got)
	}
	stop := supervisedStop{Reason: StoppedRemoteTransient, TickID: "a1", Tree: "tree-1"}
	if halt := haltReason(stop, stop, 1, 12); halt != "" {
		t.Errorf("a repeated transient remote failure over an unchanged tree halted the run: %q", halt)
	}
	if halt := haltReason(stop, stop, 12, 12); halt == "" {
		t.Error("a transient remote failure continued past the cap")
	}
}
