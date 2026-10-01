package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The ladder's bound at the ceiling (hn6 run_d51a, yjq). yjq answered BLOCKED
// three times and was dispatched a fourth — the decide try the standing
// orders call for at the ceiling — which read, live, like a BLOCKED-at-ceiling
// tick redispatched without bound. It is bounded: the try told to decide gets
// ONE chance, and a question asked again by it holds for a person rather than
// being dispatched a third time. Pinned here for an implement tick, the
// shape yjq is (the close-out's own variant is closeout_carry_test.go's).
func TestAQuestionAskedAgainByTheTryToldToDecideHolds(t *testing.T) {
	t.Parallel()
	const question = "which naming convention should the new helper follow"
	f := newFixture(t, fixtureOptions{gate: ceilingGate})
	declareStandingOrders(t, f.Repo)
	f.Runner = askingRunner(t, "a1", question, "never")

	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State == runstate.StateCompleted {
		t.Fatal("the run completed over a question nobody answered")
	}
	if _, ok := journalLine(r, "a1", StageBlockedDecide); !ok {
		t.Fatalf("no %s event: the first question at the ceiling is decided, not held", StageBlockedDecide)
	}
	held, ok := journalLine(r, "a1", StageBlockedHeld)
	if !ok || !strings.Contains(held, "told to decide") {
		t.Errorf("the question asked again is not held as such: %q", held)
	}
	if got := len(tickAttempts(t, r, "a1")); got != 2 {
		t.Errorf("a1 has %d attempts, want 2 — the question and the one try told to decide it; a third is the "+
			"unbounded ladder", got)
	}
}
