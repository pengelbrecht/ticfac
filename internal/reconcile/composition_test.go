package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The wave-composition check (tick 01u), driven end to end.
//
// The wave-4 shape: two ticks of one wave independently rewrote the same
// function and were discovered to overlap at the MERGE GATE, after both had
// run. The declaration moves that discovery to DISPATCH, where it costs
// nothing: a tick declares the files it expects to touch with `touch:` labels,
// and two ticks of one wave that declare the same file are refused before
// anything is claimed or started.

// Two ticks of one wave declaring the same file are refused at dispatch: no
// claim, no attempt, no executor — the refusal names both ticks and the file,
// and the run's checkpoint carries it durably.
func TestAWaveOfTwoTicksDeclaringTheSameFileIsRefusedAtDispatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	f.retick(t, "a1", func(tick *tk.Tick) {
		tick.Labels = []string{"touch:internal/exec/subprocess/collect.go"}
	})
	f.retick(t, "a2", func(tick *tk.Tick) {
		tick.Labels = []string{"touch:internal/exec/subprocess/collect.go"}
	})

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run ended with %v, want a refusal it recorded", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedWaveOverlap {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedWaveOverlap)
	}
	for _, needle := range []string{"a1", "a2", "internal/exec/subprocess/collect.go"} {
		if !strings.Contains(result.Failure.Message, needle) {
			t.Errorf("the refusal does not name %q: %s", needle, result.Failure.Message)
		}
	}

	// The refusal is at DISPATCH: nothing was claimed, nothing started, no
	// attempt marker exists — a wave that cannot merge costs nothing here.
	if f.startCount("run-r-fixture/tick-a1/attempt-1") != 0 || len(f.dispatches) != 0 {
		t.Errorf("the run started work for a wave it refused: %d dispatches built", len(f.dispatches))
	}
	if f.Tracker.count("claim:a1") != 0 || f.Tracker.count("claim:a2") != 0 {
		t.Errorf("the run claimed ticks of a wave it refused")
	}
	for _, event := range r.Journal() {
		if event.Stage == StageDispatched {
			t.Errorf("the run dispatched %s over a wave composition it refused", event.Tick)
		}
	}

	// And it is durable: the checkpoint on origin says the run failed and why.
	store := openStore(t, f, r)
	checkpoint, ok, err := store.Checkpoint()
	if err != nil || !ok {
		t.Fatalf("the run left no checkpoint: %v", err)
	}
	if checkpoint.State != runstate.StateFailed || !strings.Contains(checkpoint.Reason, "internal/exec/subprocess/collect.go") {
		t.Errorf("the checkpoint says %s: %s", checkpoint.State, checkpoint.Reason)
	}
}

// The same file declared in DIFFERENT waves is a sequence, not an overlap:
// one owner per file PER WAVE, and the second tick runs after the first
// merged.
func TestTheSameFileDeclaredInDifferentWavesIsNotRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	state.Waves = [][]string{{"a1"}, {"a2"}, {"b1"}, {"rv", "co"}}
	if err := f.Tracker.save(state); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2"} {
		f.retick(t, id, func(tick *tk.Tick) {
			// Both the shared file that makes the sequence interesting and the
			// tick's own work file, which the after-the-fact half holds it to.
			tick.Labels = []string{"touch:internal/exec/subprocess/collect.go", "touch:work-" + id + ".txt"}
		})
	}

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
}

// Disjoint declarations in one wave merge: the check refuses OVERLAP, not
// width — and a tick that touches exactly what it declared passes the
// declaration's other half, the after-the-fact diff.
func TestDisjointDeclarationsInOneWaveMerge(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"touch:work-a1.txt"} })
	f.retick(t, "a2", func(tick *tk.Tick) { tick.Labels = []string{"touch:work-a2.txt"} })

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	// The composition decision is a LOGGED line, not an unstated posture.
	var decided string
	for _, event := range r.Journal() {
		if strings.Contains(event.Detail, "wave composition") {
			decided = event.Detail
		}
	}
	if decided == "" {
		t.Errorf("the journal never states the wave-composition decision")
	}
	// And the declaration reached the DURABLE marker: the check that holds a
	// worker to its declaration reads the marker, not this incarnation's memory.
	attempts, err := openStore(t, f, r).Attempts()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range attempts {
		if record.TickID != "a1" || record.Attempt != 1 {
			continue
		}
		touch, _ := record.JobHandle["touch"].([]any)
		if len(touch) != 1 || touch[0] != "work-a1.txt" {
			t.Errorf("attempt 1 of a1 recorded its touch: declaration as %v, want [work-a1.txt]", record.JobHandle["touch"])
		}
	}
}

// A malformed touch: label is refused loudly, naming the tick and the label —
// the same rule the tier labels carry: a weakly typed field the tracker cannot
// validate surfaces here or nowhere.
func TestAMalformedTouchLabelRefusesTheRunNamingTheTick(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"touch:internal/reconcile/"} })
	f.retick(t, "a2", func(tick *tk.Tick) { tick.Labels = []string{"touch:"} })

	_, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run ended with %v, want a refusal it recorded", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedTouchLabel {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedTouchLabel)
	}
	for _, needle := range []string{"a2", `"touch:"`} {
		if !strings.Contains(result.Failure.Message, needle) {
			t.Errorf("the refusal does not name %q: %s", needle, result.Failure.Message)
		}
	}
}

// The declaration's other half: a worker that touches a file its tick did not
// declare is REFUSED at the merge, not merged silently. The boundary the
// artifact rule (tick p6b) enforces against authority is enforced here against
// the tick's own declaration — detectable after the fact, never silent.
func TestAnAttemptThatTouchesAnUndeclaredFileIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "touch-undeclared"})
	f.retick(t, "a1", func(tick *tk.Tick) { tick.Labels = []string{"touch:work-a1.txt"} })

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "touch-undeclared"})
	if err != nil {
		t.Fatalf("the run ended with %v, want a refusal it recorded", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUndeclaredTouch {
		t.Fatalf("the run failed as %+v, not as %s", result.Failure, RefusedUndeclaredTouch)
	}
	if !strings.Contains(result.Failure.Message, "sneaky-a1.txt") {
		t.Errorf("the refusal does not name the undeclared file: %s", result.Failure.Message)
	}
	// It is recorded as a REJECTION the next reader of the run can find, and
	// the tick is NOT closed behind work the declaration does not cover.
	if detail, ok := journalLine(r, "a1", StageRejected); !ok || !strings.Contains(detail, "sneaky-a1.txt") {
		t.Errorf("the journal does not record the undeclared touch: %q", detail)
	}
	state, err := f.Tracker.load()
	if err != nil {
		t.Fatal(err)
	}
	if tick := state.Ticks["a1"]; tick.Status == "closed" {
		t.Errorf("a1 was closed behind an attempt that touched undeclared files")
	}
}

// The parsing rules the two halves share, at unit size: normalisation, the
// directory declaration, and the malformed shapes the run-level check refuses.
// short: parses the touch declarations — source text in, table out
func TestTouchDeclarationsParseAndCoverMechanically(t *testing.T) {
	t.Parallel()
	files, bad := parseTouchLabels("a1", []string{"chore", "touch:./collect.go", "touch:collect.go", "touch:internal/reconcile/"})
	if len(bad) != 0 {
		t.Fatalf("valid declarations were refused: %v", bad)
	}
	if len(files) != 2 || files[0] != "collect.go" || files[1] != "internal/reconcile/" {
		t.Fatalf("declarations parse to %v, want [collect.go internal/reconcile/]", files)
	}
	for _, label := range []string{"touch:", "touch:/etc/passwd", "touch:../outside.go", "touch:internal/../outside.go"} {
		if _, bad := parseTouchLabels("a1", []string{label}); len(bad) == 0 {
			t.Errorf("label %q was accepted", label)
		}
	}
	if !touchCovers([]string{"collect.go"}, "collect.go") {
		t.Errorf("an exact declaration covers its own file")
	}
	if touchCovers([]string{"collect.go"}, "other.go") {
		t.Errorf("an exact declaration covers a file it does not name")
	}
	if !touchCovers([]string{"internal/reconcile/"}, "internal/reconcile/dispatch.go") {
		t.Errorf("a directory declaration covers the files under it")
	}
	if touchCovers([]string{"internal/reconcile/"}, "internal/reconcile2/dispatch.go") {
		t.Errorf("a directory declaration covers outside its directory")
	}
}
