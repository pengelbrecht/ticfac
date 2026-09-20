package reconcile

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Tick 3mp: "in flight" means one thing on both sides of the tracker.
//
// 9pz made the window count only attempts with a LIVE WORKER, so a settled
// attempt awaiting its collect, integrate, gate and close no longer held a
// slot. tk counts CLAIMS, and a claim lives until its tick CLOSES. Epic dha
// found the gap: three ticks had settled and were waiting to be finished — no
// worker, still claimed — the window admitted a fifth, and tk refused it:
//
//	claim e9n: tk command "claim" refused: dispatch width exceeded: wave width
//	4 is full: 4 implementer(s) already in flight under dha (80x, aqm, b50,
//	cxk). Claiming e9n would make 5.
//
// The run did not hold on that refusal. It DIED, with three settled ticks'
// worth of finished work sitting safe on branches that nobody was told about.

// TestTheWindowNeverAsksForAClaimTheWidthForbids is the acceptance's first
// half, asserted against a tracker that counts claims exactly as tk does: a
// claim opens at the dispatch and closes at the close, never earlier.
//
// The fixture is dha's shape — a wave wider than one, ticks that settle while
// another is still being finished — so the settled-but-unfinished queue that
// 9pz introduced is what the width is measured against.
func TestTheWindowNeverAsksForAClaimTheWidthForbids(t *testing.T) {
	t.Parallel()
	gate := `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "sleep 2; test -f README.md", description = "slow enough that finishes queue" }
`
	f := newFixture(t, fixtureOptions{gate: gate})
	// Three ticks in one wave, so a width of two leaves a third waiting, and
	// the role jobs stay behind all of them.
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.BlockedBy = map[string][]string{"rv": {"a1", "a2", "b1"}, "co": {"a1", "a2", "b1", "rv"}}
	f.Tracker.write(t, state)

	// The tracker enforces the declared width, as tk does. If the window asks
	// for a claim beyond it, this refuses and the run holds — which is a
	// failure of this test, because the window should never have asked.
	f.Tracker.refuseClaimsBeyond(2)

	r, result, err := f.run(f.Repo, fixtureOptions{gate: gate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %+v",
			result.Closed, result.State, result.Failure)
	}
	if peak := f.Tracker.peakClaims(); peak > 2 {
		t.Errorf("%d claims were open at once under a declared width of 2: the window counted live workers "+
			"while the tracker counted claims, and a claim lives until its tick closes", peak)
	}

	// The INTERIM behaviour, pinned rather than left as a side effect, so that
	// the cost 3mp accepts is visible in a test and not only in a comment: a
	// width full of settled-but-unfinished ticks admits NOTHING. The third tick
	// waits for a CLOSE, not for a settle — that is what makes admission
	// starve while finishes queue, and it is what tick e3c is meant to undo.
	// Its partner is TestE3cWillRestoreAdmissionWhileASettledAttemptIsBeingFinished,
	// which is skipped until e3c exists; when this assertion starts failing,
	// that one should start passing, and neither should change alone.
	var firstClose, dispatched int
	for i, event := range r.Journal() {
		switch {
		case event.Stage == StageClosed && firstClose == 0:
			firstClose = i + 1
		case event.Tick == "b1" && event.Stage == StageDispatched && dispatched == 0:
			dispatched = i + 1
		}
	}
	if firstClose == 0 || dispatched == 0 {
		t.Fatalf("the feed is missing an event: first close %d, b1 dispatched %d", firstClose, dispatched)
	}
	if dispatched < firstClose {
		t.Errorf("b1 was dispatched at %d, before anything CLOSED at %d: the window is admitting into a slot "+
			"whose claim is still open, which is what the tracker refuses", dispatched, firstClose)
	}
}

// TestATrackerRefusingAClaimHoldsTheRunAndKeepsItsWork is the acceptance's
// second half, and it is true whatever the width arithmetic decides.
//
// The tracker is the authority and can refuse for reasons this run cannot see —
// another run holding claims under the same epic, a tick claimed by a person.
// A refusal is a fact about the world, not a crash: the run must hold with a
// reason somebody can act on, and everything it was holding must survive to be
// adopted by the next incarnation.
func TestATrackerRefusingAClaimHoldsTheRunAndKeepsItsWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: wideGate})
	// One claim is all this tracker will allow, so the second dispatch of the
	// wave is refused however the window counts.
	f.Tracker.refuseClaimsBeyond(1)

	r, result, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("the run should have HELD, not returned an operational error: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedClaimWidth {
		t.Fatalf("the run ended %s with failure %+v, want a %s refusal", result.State, result.Failure, RefusedClaimWidth)
	}
	if result.State != runstate.StateFailed {
		t.Errorf("the run ended %s; a held run is checkpointed so it can be resumed", result.State)
	}

	// It HELD: the feed says so in the vocabulary a watcher matches on, and the
	// reason names what a person does about it.
	held := false
	for _, event := range r.Journal() {
		if event.Stage == StageRunHeld {
			held = true
		}
	}
	if !held {
		t.Errorf("no %s line in the feed: a hold nobody can see is a stall by definition", StageRunHeld)
	}
	if !strings.Contains(result.Failure.Message, "adopts them by identity") {
		t.Errorf("the refusal does not tell the operator the held work is resumable: %q", result.Failure.Message)
	}

	// And the work is INTACT, which is the half that matters: the attempt the
	// run did dispatch still has its marker on origin, so a second run under
	// the same run id adopts it rather than dispatching over it.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) == 0 {
		t.Fatal("no attempt markers survived the hold: the next run has nothing to adopt")
	}

	// The tracker relents — the other holder finished, which is what the
	// refusal said would happen — and the resumed run adopts and closes.
	f.Tracker.refuseClaimsBeyond(0)
	second, resumed, err := f.run(f.Repo, fixtureOptions{gate: wideGate})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s", resumed.State, resumed.Reason)
	}
	if len(resumed.Closed) != 5 {
		t.Errorf("the resumed run closed %v, want every tick of the epic", resumed.Closed)
	}
	adopted := false
	for _, event := range second.Journal() {
		if event.Stage == StageAdopted {
			adopted = true
		}
	}
	if !adopted {
		t.Error("the resumed run dispatched over the held attempt instead of adopting it by identity: the work " +
			"the hold preserved was paid for twice")
	}
}

// admissionGate is a declared gate whose command WAITS for a third tick to be
// dispatched, and fails if it never is.
//
// The gate is the right place to put the question because the gate is where the
// time goes: a run that cannot admit through its gate cannot admit at all. The
// bound is the test's own, well under the fixture's GateTimeout, so a run that
// does not admit fails with this command's own message rather than with a
// timeout somewhere else.
func admissionGate(signal string) string {
	return `version = 2

[orchestration]
max_parallel = 2

[roles.implement]
kind = "claude"
model = "sonnet"

[testing.commands]
tree = { command = "test -f README.md && ls work-*.txt >/dev/null", description = "the merge carries the work" }
admitted = { command = "i=0; while [ $i -lt 400 ] && [ ! -f '` + signal + `' ]; do sleep 0.05; i=$((i+1)); done; test -f '` + signal + `' || { echo 'no third tick was admitted while this gate ran'; exit 9; }", description = "the window admits while this gate runs" }
`
}

// TestE3cWillRestoreAdmissionWhileASettledAttemptIsBeingFinished is tick 9pz's
// acceptance, kept intact and SKIPPED rather than deleted or re-pointed.
//
// 9pz freed a settled attempt's slot at the collect: with a width of two and
// three ready ticks, the third was dispatched into the first's slot WHILE that
// attempt was being integrated and gated, not after. On the repository's own
// epic feeds that was the difference between 7.9s and 1.8s of window time per
// finish.
//
// Tick 3mp took it back, because the width is not this side's to define: tk
// enforces it and tk counts CLAIMS, so admitting into a settled attempt's slot
// was asking the tracker for a refusal — which is how epic dha died. The
// interim behaviour is pinned by
// TestTheWindowNeverAsksForAClaimTheWidthForbids above; this is the gain that
// is currently missing, and it is left here failing-when-unskipped so that
// nobody has to remember it.
//
// It is skipped, not deleted, on purpose. A deleted test leaves no evidence
// that the throughput ever existed; a test re-pointed at the interim behaviour
// would pass forever and the throughput would never come back. This one passes
// only when the gain is really back.
func TestE3cWillRestoreAdmissionWhileASettledAttemptIsBeingFinished(t *testing.T) {
	t.Skip("tick e3c: unskip when admission counts claims until a claim can say it is merely integrating — " +
		"that is, when tk stops counting a settled-and-integrating claim as an implementer in flight and " +
		"held.claims() can stop counting the finishing attempt. Until then the window must ask for exactly " +
		"what the tracker will grant, and this gain is deliberately not available")
	t.Parallel()
	signal := filepath.Join(t.TempDir(), "third-dispatched")
	gate := admissionGate(signal)

	f := newFixture(t, fixtureOptions{gate: gate, mode: "linger-until"})
	waveOfThree(t, f)

	// a2 keeps thinking until the third tick is dispatched, so the run really
	// does have to admit from a freed slot rather than from an empty window.
	f.Runner = append([]string{f.Runner[0], "LINGER_TICK=a2", "LINGER_UNTIL=" + signal}, f.Runner[1:]...)
	f.wrap = func(inner Executor) Executor {
		return &dispatchSignal{Executor: inner, tick: "b1", path: signal}
	}

	r, result, err := f.run(f.Repo, fixtureOptions{gate: gate})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Closed) != 5 {
		t.Fatalf("closed %v, want every tick of the epic; the run ended %s: %s",
			result.Closed, result.State, result.Reason)
	}

	// The feed's own account of it: b1 was dispatched after a1 settled and
	// BEFORE a1's gate passed.
	var settled, dispatched, gated int
	for i, event := range r.Journal() {
		switch {
		case event.Tick == "a1" && event.Stage == StageWaiting && strings.Contains(event.Detail, "settled as"):
			if settled == 0 {
				settled = i + 1
			}
		case event.Tick == "b1" && event.Stage == StageDispatched:
			dispatched = i + 1
		case event.Tick == "a1" && event.Stage == StageGatePassed:
			gated = i + 1
		}
	}
	if settled == 0 || dispatched == 0 || gated == 0 {
		t.Fatalf("the feed is missing one of the three events: a1 settled %d, b1 dispatched %d, a1 gated %d",
			settled, dispatched, gated)
	}
	if dispatched > gated {
		t.Errorf("b1 was dispatched at %d, after a1's gate passed at %d: the run admitted nothing for the whole "+
			"of the finish, which is the throughput e3c is meant to give back", dispatched, gated)
	}
	if dispatched < settled {
		t.Errorf("b1 was dispatched at %d, before a1 even settled at %d: the width was exceeded", dispatched, settled)
	}
}
