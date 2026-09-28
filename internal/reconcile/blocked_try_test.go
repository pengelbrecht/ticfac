package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The fixture's blocked-first modes key on the tick's OWN first try, not on
// the run-wide attempt number (tick vw0).
//
// Attempt numbers count the RUN's dispatches, because the number is the
// attempt's identity — its branch, its marker, the argument to `ticfac
// settle`. A fixture mode that gated "first try" on TICFAC_ATTEMPT = 1 was
// gating on "this try drew the run's first dispatch", which is a fact about
// dispatch ORDER, not about the tick. Every fixture dispatched a1 first, so
// the two readings never came apart and no test could tell them apart.
//
// The two tests here dispatch another tick first, on purpose, so a1's own
// first try draws a run-wide number nobody would mistake for "first": the
// ordering premise that let the bug sit latent. They fail on the old fixture
// exactly there — a1 answers DONE where the mode means BLOCKED.

// dispatchAnotherTickFirst reorders the fixture's first wave so a2 is
// dispatched before a1. Attempt numbers count the run's dispatches, so a1's
// first try then draws a number past 1 — and any fixture behaviour keyed on
// "the run-wide number is 1" stops describing the tick's first try.
func dispatchAnotherTickFirst(t *testing.T, f *fixture) {
	t.Helper()
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.Waves = [][]string{{"a2", "a1"}, {"b1"}, {"rv", "co"}}
	f.Tracker.write(t, state)
}

// markerOfAttempt reads the dispatch marker the run state carries for one
// tick's attempt, the way a restart reads it: off origin, through the same
// handleFromMap every resume goes through.
func markerOfAttempt(t *testing.T, f *fixture, tick string, attempt int) attemptHandle {
	t.Helper()
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, existing := range attempts {
		if existing.TickID == tick && existing.Attempt == attempt {
			return handleFromMap(existing.JobHandle)
		}
	}
	t.Fatalf("no dispatch marker for %s attempt %d on origin", tick, attempt)
	return attemptHandle{}
}

// runWideAttemptsOf is the run-wide attempt numbers the run state carries for
// one tick, read from origin the way a restart reads it.
func runWideAttemptsOf(t *testing.T, f *fixture, tick string) []int {
	t.Helper()
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	var numbers []int
	for _, attempt := range attempts {
		if attempt.TickID == tick {
			numbers = append(numbers, attempt.Attempt)
		}
	}
	return numbers
}

// blockedAnsweredInRun asserts one tick's FIRST TRY (run-wide number first)
// reported STATUS: BLOCKED and that the run answered it in-run (tick tyd) —
// decided under the standing orders by a later try — rather than closing on it.
func blockedAnsweredInRun(t *testing.T, f *fixture, r *Reconciler, tick string, first int) {
	t.Helper()
	report := archivedReport(t, f, markerOfAttempt(t, f, tick, first))
	if !strings.Contains(report, "STATUS: BLOCKED") {
		t.Fatalf("%s's first try (run-wide attempt %d) reported:\n%s\nwant STATUS: BLOCKED — the mode must "+
			"trigger on the tick's own first try whatever the run-wide number", tick, first, report)
	}
	if _, ok := journalLine(r, tick, StageBlockedDecide); !ok {
		t.Fatalf("%s's BLOCKED first try was not answered in-run: its stages are %v", tick, r.Stages(tick))
	}
	if got := f.Tracker.count("close:" + tick); got != 1 {
		t.Fatalf("%s closed %d times, want once — on the later try, never on the BLOCKED answer", tick, got)
	}
}

// finding_blocked: the v3i shape gates on a1's OWN first try. Dispatched
// behind a2, a1's first try draws the run-wide number 2 — and must still
// answer BLOCKED with nothing committed, not quietly do the work because the
// run-wide number moved off 1. Since tick tyd the BLOCKED answer is answered
// in-run (decided under the standing orders by the next try), so the proof is
// the first try's own report and the in-run answer, not a stopped run.
func TestFindingBlockedTriggersOnTheTicksOwnFirstTryNotTheRunWideNumber(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "finding_blocked"})
	dispatchAnotherTickFirst(t, f)

	reconciler, _, err := f.run(f.Repo, fixtureOptions{mode: "finding_blocked"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The ordering premise held: a2 went first, did its work, and closed.
	if got := f.Tracker.count("close:a2"); got != 1 {
		t.Fatalf("a2, dispatched first, closed %d times, want 1: without it this test proves nothing", got)
	}
	// The premise, as durable fact: a1's first try IS run-wide attempt 2.
	// If a fixture change ever makes a1 draw 1 again, this test stops
	// covering the bug and must say so rather than pass vacuously.
	numbers := runWideAttemptsOf(t, f, "a1")
	if len(numbers) < 1 || numbers[0] != 2 {
		t.Fatalf("a1 was dispatched as %v, want its first try at 2: the test only proves anything while "+
			"another tick's dispatch moves the run-wide number off the tick's own try", numbers)
	}
	blockedAnsweredInRun(t, f, reconciler, "a1", 2)
}

// blocked-first blocks EVERY tick's own first try — including one whose first
// try draws a run-wide number other than 1 because another tick went first.
func TestBlockedFirstTriggersOnTheTicksOwnFirstTryNotTheRunWideNumber(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "blocked-first"})
	dispatchAnotherTickFirst(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "blocked-first"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): every BLOCKED first try is answered in-run", result.State, result.Failure)
	}
	a2 := runWideAttemptsOf(t, f, "a2")
	a1 := runWideAttemptsOf(t, f, "a1")
	if len(a2) < 1 || a2[0] != 1 {
		t.Fatalf("a2 was dispatched as %v, want its first try at 1", a2)
	}
	if len(a1) < 1 || a1[0] == 1 {
		t.Fatalf("a1 was dispatched as %v, want its first try off the run-wide number 1", a1)
	}
	blockedAnsweredInRun(t, f, r, "a2", a2[0])
	blockedAnsweredInRun(t, f, r, "a1", a1[0])
}
