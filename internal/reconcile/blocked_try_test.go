package reconcile

import (
	"strings"
	"testing"
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

// finding_blocked: the v3i shape gates on a1's OWN first try. Dispatched
// behind a2, a1's first try draws the run-wide number 2 — and must still
// answer BLOCKED with nothing committed, not quietly do the work because the
// run-wide number moved off 1.
func TestFindingBlockedTriggersOnTheTicksOwnFirstTryNotTheRunWideNumber(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "finding_blocked"})
	dispatchAnotherTickFirst(t, f)

	reconciler, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_blocked"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The ordering premise held: a2 went first, did its work, and closed.
	if got := f.Tracker.count("close:a2"); got != 1 {
		t.Fatalf("a2, dispatched first, closed %d times, want 1: without it this test proves nothing", got)
	}
	if result.Failure == nil {
		t.Fatalf("run state %s: a1's first try answered DONE — finding_blocked keyed on the run-wide "+
			"attempt number, which moved to 2 when another tick was dispatched first", result.State)
	}
	if result.Failure.TickID != "a1" {
		t.Fatalf("the refusal is for %s, want a1", result.Failure.TickID)
	}
	// The verdict itself, not merely that the run stopped: in this mode a1's
	// later answers also carry findings, and since tick aqm those findings ride
	// to the close-out rather than refusing the attempt's close — so the
	// refusal this run owes is a1's own BLOCKED answer. The fixture's
	// contract is that the FIRST TRY answers BLOCKED with nothing committed —
	// which the collect vocabulary honestly calls no-commits, exactly as it
	// does for the blocked-first shape.
	if result.Failure.Reason != RefusedCollect {
		t.Fatalf("the refusal is %s (%s), want %s: a1's first try must be refused for answering BLOCKED "+
			"with nothing committed, not for anything a DONE answer could also have caused",
			result.Failure.Reason, result.Failure.Message, RefusedCollect)
	}
	report := archivedReport(t, f, markerOfAttempt(t, f, "a1", 2))
	if !strings.Contains(report, "STATUS: BLOCKED") {
		t.Fatalf("a1's first try (run-wide attempt 2) reported:\n%s\nwant STATUS: BLOCKED — the mode "+
			"must trigger on the tick's own first try whatever the run-wide number", report)
	}
	if got := f.Tracker.count("close:a1"); got != 0 {
		t.Fatalf("a1 closed %d times on a BLOCKED answer, want 0", got)
	}
	rejected := false
	for _, event := range reconciler.Journal() {
		if event.Stage == StageRejected && event.Tick == "a1" && strings.Contains(event.Detail, "no-commits") {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("no %s line for a1 naming the no-commits verdict of its blocked first try; its stages are %v",
			StageRejected, reconciler.Stages("a1"))
	}
	// The premise, as durable fact: a1's first try IS run-wide attempt 2.
	// If a fixture change ever makes a1 draw 1 again, this test stops
	// covering the bug and must say so rather than pass vacuously.
	numbers := runWideAttemptsOf(t, f, "a1")
	if len(numbers) != 1 || numbers[0] != 2 {
		t.Fatalf("a1 was dispatched as %v, want exactly [2]: the test only proves anything while "+
			"another tick's dispatch moves the run-wide number off the tick's own try", numbers)
	}
}

// blocked-first blocks EVERY tick's own first try — including one whose first
// try draws a run-wide number far from 1, on a resume where an earlier tick's
// spent attempt has already moved the counter twice.
func TestBlockedFirstTriggersOnTheTicksOwnFirstTryNotTheRunWideNumber(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "blocked-first"})
	dispatchAnotherTickFirst(t, f)

	// Incarnation one: a2, dispatched first, draws the run-wide number 1 and
	// answers BLOCKED with nothing committed; the run rejects it and stops.
	_, first, err := f.run(f.Repo, fixtureOptions{mode: "blocked-first"})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Failure == nil || first.Failure.TickID != "a2" {
		t.Fatalf("the first run's refusal is %+v, want a2's first try refused", first.Failure)
	}

	// Incarnation two: a2's SECOND try commits and answers DONE and the tick
	// closes; a1 is then dispatched for the FIRST time — at the run-wide
	// number 3, because the run has already made two dispatches. Its own
	// first try must block exactly as a1's does in every other fixture.
	_, second, err := f.run(f.Repo, fixtureOptions{mode: "blocked-first"})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got := f.Tracker.count("close:a2"); got != 1 {
		t.Fatalf("a2 closed %d times on its second try, want 1", got)
	}
	if second.Failure == nil {
		t.Fatalf("run state %s: a1's first try answered DONE — blocked-first keyed on the run-wide "+
			"attempt number, which was 3 by the time a1 was first dispatched", second.State)
	}
	if second.Failure.TickID != "a1" {
		t.Fatalf("the second run's refusal is for %s, want a1", second.Failure.TickID)
	}
	if got := f.Tracker.count("close:a1"); got != 0 {
		t.Fatalf("a1 closed %d times on a BLOCKED answer, want 0", got)
	}
	numbers := runWideAttemptsOf(t, f, "a1")
	if len(numbers) != 1 || numbers[0] != 3 {
		t.Fatalf("a1 was dispatched as %v, want exactly [3]: the test only proves anything while "+
			"the run-wide number has moved off the tick's own try", numbers)
	}
}
