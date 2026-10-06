package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// A cloud run's hold prints `ticfac settle <epic> <tick> <n> --run-id run_…`,
// and the person who reads it types it on their own machine — a host that
// never ran the worker, holds none of its state and cannot build its executor
// (no run credential). Before tick bd5 the settle refused there with "this
// host holds no state … nothing to release": the command the header printed
// could not clear its own hold. The factory that ran the worker is now asked
// (Options.FactoryAttempt), and its answer is ruled on by the same settlement
// rule the executor's own Inspect is — so the printed command works where the
// person typing it is.

// factoryAnswers stands in for the operator's factory: what it says about one
// of its runs' attempts, and how many times it was asked.
type factoryAnswers struct {
	mu     chan struct{}
	answer FactoryAttemptAnswer
	asks   []struct {
		runID, tickID string
		attempt       int
	}
}

func newFactoryAnswers(answer FactoryAttemptAnswer) *factoryAnswers {
	return &factoryAnswers{mu: make(chan struct{}, 1), answer: answer}
}

func (a *factoryAnswers) asked() int {
	a.mu <- struct{}{}
	defer func() { <-a.mu }()
	return len(a.asks)
}

func (a *factoryAnswers) callback(ctx context.Context, runID, tickID string, attempt int) FactoryAttemptAnswer {
	a.mu <- struct{}{}
	a.asks = append(a.asks, struct {
		runID   string
		tickID  string
		attempt int
	}{runID, tickID, attempt})
	<-a.mu
	return a.answer
}

// aFactoryAttempt is a cloud run's attempt in the store — dispatched through
// the sandbox door, killed the moment its dispatch is recorded — and the
// store and marker a release from elsewhere rules on. The container stays
// running at the door: an orchestrator died mid-attempt, exactly the shape a
// cloud run's hold is.
func aFactoryAttempt(t *testing.T, f *fixture, door *fakeSandboxDoor) (*runstate.Store, int) {
	t.Helper()
	_, _, err := runDoorIncarnation(t, f, f.Repo, door, f.StateRoot, stopAt("a1", StageDispatched))
	killedAfter(t, err, "a1", StageDispatched)
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	markers, executor := attemptMarkersOf(t, store, "a1")
	if markers != 1 {
		t.Fatalf("%d dispatch markers for a1, want the one the killed incarnation recorded", markers)
	}
	if executor != doorExecutorName {
		t.Fatalf("the marker names executor %q, want %q: the release must rule on a factory attempt", executor, doorExecutorName)
	}
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	number := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" && attempt.Attempt > number {
			number = attempt.Attempt
		}
	}
	if number == 0 {
		t.Fatal("no attempt of a1 on origin")
	}
	return store, number
}

// aSettlerOnAnotherHost builds the reconciler the person's machine gets: none
// of the attempt's state (a fresh executor state root), no credential to build
// its executor (any build attempt fails, loudly), and the factory's answer
// through the seam.
func aSettlerOnAnotherHost(t *testing.T, f *fixture, door *fakeSandboxDoor, factory *factoryAnswers) (*Reconciler, string) {
	t.Helper()
	opts := f.doorOptions(t, f.Repo, door, filepath.Join(f.Root, "laptop-state"), nil)
	opts.NewExecutor = func(d Dispatch) (Executor, Substrate, error) {
		return nil, Substrate{}, fmt.Errorf(
			"this host holds no credential for %s's executor %s: the run's own token is the container's",
			d.TickID, profileExecutorOf(d))
	}
	opts.FactoryAttempt = factory.callback
	settler, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return settler, filepath.Join(f.Root, "laptop-state")
}

// The release the hold's own command asks for is answered by the factory, on
// the machine that never ran the attempt: a factory that cannot say what
// became of the worker answers `lost`, and the release is recorded as a
// decision on origin — attributed, stateless as to teardown, and never
// reaching for an executor this host cannot build.
func TestASettleReleasesAFactorysLostAttemptFromAHostThatNeverRanIt(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")
	store, attempt := aFactoryAttempt(t, f, door)

	factory := newFactoryAnswers(FactoryAttemptAnswer{
		Asked: true, Lost: true,
		Evidence: "the factory answers for no worker of the attempt: run r-fixture is over (its record says failed)",
	})
	settler, _ := aSettlerOnAnotherHost(t, f, door, factory)
	ctx := context.Background()

	// Nothing to carry — the worker never pushed — so a carrying release is
	// refused as it always is, and refuses WITHOUT recording anything: the
	// plain release is the person's next move.
	if _, err := settler.SettleCarry(ctx, "a1", attempt, "an operator"); err == nil {
		t.Fatal("a carrying release of an attempt that left no commit was accepted")
	} else if !strings.Contains(err.Error(), "no work to carry") {
		t.Errorf("the carrying release refuses for the wrong reason: %v", err)
	}

	settled, err := settler.Settle(ctx, "a1", attempt, "an operator")
	if err != nil {
		t.Fatalf("release the factory's lost attempt from a host that never ran it: %v", err)
	}
	if !settled.Recorded || settled.State != subprocess.StateLost || settled.Disposed {
		t.Fatalf("the settlement is %+v, want recorded, lost, and nothing torn down here", settled)
	}
	if settled.RunID != "r-fixture" || settled.TickID != "a1" || settled.Attempt != attempt {
		t.Fatalf("the settlement names %+v, want run r-fixture's a1 attempt %d", settled, attempt)
	}
	// The factory is asked once per release attempt, never polled: the
	// carrying settle that was refused asked, the release that ruled asked,
	// and the second settle below — reading the record, not the answer —
	// asks nothing.
	if got := factory.asked(); got != 2 {
		t.Fatalf("the factory was asked %d times, want 2 (the refused carry and the release): the release rules on "+
			"the factory's answer, not on a poll of it", got)
	}

	// It is a DECISION on origin, readable as fields rather than as prose.
	// The store is opened FRESH here: it is read after the release, so what it
	// answers is what the next run will fetch.
	store = openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range decisions {
		if op, _ := decision.Request["op"].(string); op != SettleOp {
			continue
		}
		if tick, _ := decision.Request["tick_id"].(string); tick != "a1" {
			continue
		}
		found = true
		if by, _ := decision.Response["released_by"].(string); by != releaseHandle("an operator") {
			t.Errorf("the release does not name who made it as a stable handle: %+v", decision.Response)
		}
		if state, _ := decision.Request["state"].(string); state != subprocess.StateLost {
			t.Errorf("the release does not record the factory's last word as its state: %v", decision.Request)
		}
	}
	if !found {
		t.Fatalf("no settlement decision on origin: %+v", decisions)
	}

	// Settling the same attempt twice writes nothing: a decision is created if
	// absent and never rewritten — and the second settle reads the record on
	// origin, never the factory's answer again.
	if again, err := settler.Settle(ctx, "a1", attempt, "an operator"); err != nil || again.Recorded {
		t.Fatalf("a second settlement wrote a second record: %+v (%v)", again, err)
	}
	if got := factory.asked(); got != 2 {
		t.Errorf("the factory was asked %d times after the release, still want 2: a settled attempt is read from "+
			"the record on origin, never re-asked", got)
	}
}

// A factory attempt the run can still address is not a person's to release:
// the factory saying its run is live keeps A6 exactly where the executor's
// own "running" answer keeps it.
func TestAFactoryAttemptItsRunCanStillAddressIsNotReleasedFromElsewhere(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")
	store, attempt := aFactoryAttempt(t, f, door)

	factory := newFactoryAnswers(FactoryAttemptAnswer{
		Asked: true, Live: true,
		Evidence: "the factory says run r-fixture is live: its Workflow instance is running",
	})
	settler, _ := aSettlerOnAnotherHost(t, f, door, factory)
	_, err := settler.Settle(context.Background(), "a1", attempt, "an operator")
	if err == nil {
		t.Fatal("an attempt its run can still address was released from elsewhere")
	}
	for _, want := range []string{"Appendix A #6", "cancelled", factory.answer.Evidence} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range decisions {
		if op, _ := decision.Request["op"].(string); op == SettleOp {
			t.Fatalf("a refused release was recorded: %+v", decision)
		}
	}
}

// The factory's own settlement record of the worker is ruled on by the SAME
// rule the executor's Inspect is: a terminal answer this run has not rejected
// is refused — the next run collects it — so a factory answer is an address
// for the rule, never a way around it.
func TestAFactorysTerminalAnswerIsNotAReleaseAroundTheRule(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")
	store, attempt := aFactoryAttempt(t, f, door)

	factory := newFactoryAnswers(FactoryAttemptAnswer{
		Asked: true, Terminal: true, State: subprocess.StateFailed,
		Evidence: "the factory recorded its worker container failed with exit 3 at 2026-10-04T13:06:00Z",
	})
	settler, _ := aSettlerOnAnotherHost(t, f, door, factory)
	_, err := settler.Settle(context.Background(), "a1", attempt, "an operator")
	if err == nil {
		t.Fatal("a settled attempt the run has not rejected was released")
	}
	for _, want := range []string{"has not rejected", "collects it", subprocess.StateFailed} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range decisions {
		if op, _ := decision.Request["op"].(string); op == SettleOp {
			t.Fatalf("a refused release was recorded: %+v", decision)
		}
	}
}

// No factory configured, and the refusal that was stands: an attempt nothing
// on this host can address is not this release's to rule on — the local
// attempt's guard, unchanged.
func TestAHostWithNoFactoryToAskKeepsItsRefusal(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{gate: cloudGate})
	door := newFakeSandboxDoor("r-fixture")
	_, attempt := aFactoryAttempt(t, f, door)

	opts := f.doorOptions(t, f.Repo, door, filepath.Join(f.Root, "laptop-state"), nil)
	opts.NewExecutor = func(d Dispatch) (Executor, Substrate, error) {
		return nil, Substrate{}, fmt.Errorf("this host holds no credential for %s's executor", d.TickID)
	}
	settler, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = settler.Settle(context.Background(), "a1", attempt, "an operator")
	if err == nil {
		t.Fatal("an attempt nothing could address was released")
	}
	if !strings.Contains(err.Error(), "this host holds no state") {
		t.Errorf("the refusal does not say what the host is missing: %v", err)
	}
}

// A terminal factory answer for an attempt THIS RUN REJECTED is the other
// thing a person may release — the printed command's rejected-work hold, the
// one whose only copy of the work the refusal strand — now answerable from a
// host that never ran the attempt.
//
// The fixture's attempts here are the LOCAL executor's, on purpose: what is
// under test is the RULE the factory's answer is admitted to, and the
// rejection it rules on is the durable record's — a factory answers for its
// own runs' attempts (a local run's id is not one of its), which is why the
// answer cannot reach a release the record does not permit.
func TestAFactorysTerminalAnswerReleasesTheAttemptTheRunRejected(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "silent", gate: tierGate})
	// A worker that never reports climbs the ladder to its ceiling, gets the
	// one further try there, and the resume holds on the rejected attempt —
	// the RefusedRejectedWork hold whose prose names `ticfac settle`.
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "silent"})
	if err != nil {
		t.Fatalf("the ladder run did not finish: %v", err)
	}
	_, held, err := f.run(f.Repo, fixtureOptions{mode: "silent"})
	if err != nil {
		t.Fatalf("the resume did not finish: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedRejectedWork {
		t.Fatalf("the resume ended %+v, want the %s hold", held.Failure, RefusedRejectedWork)
	}
	if !strings.Contains(held.Failure.Message, "ticfac settle qeu a1") {
		t.Fatalf("the hold names no release command: %s", held.Failure.Message)
	}
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	latest := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" && attempt.Attempt > latest {
			latest = attempt.Attempt
		}
	}
	if latest == 0 {
		t.Fatal("no attempt of a1 on origin")
	}

	// The person: another host (no state), the factory's terminal answer for
	// the worker, and the durable rejection the rule reads.
	factory := newFactoryAnswers(FactoryAttemptAnswer{
		Asked: true, Terminal: true, State: subprocess.StateFailed,
		Evidence: "the factory recorded its worker container failed with exit 1 at 2026-10-04T13:06:00Z",
	})
	opts := f.options(f.Repo, fixtureOptions{mode: "silent"})
	opts.ExecStateRoot = filepath.Join(f.Root, "laptop-state")
	opts.FactoryAttempt = factory.callback
	settler, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	settled, err := settler.Settle(context.Background(), "a1", latest, "an operator")
	if err != nil {
		t.Fatalf("release the rejected attempt the factory answers for: %v", err)
	}
	if !settled.Recorded || settled.State != subprocess.StateFailed || settled.Disposed {
		t.Fatalf("the settlement is %+v, want recorded, the factory's state, and nothing torn down here", settled)
	}
	// Where the rejected work lives, read from the remote the release names:
	// the release must say, never guess.
	if settled.WorkRef == "" || settled.WorkSHA == "" {
		t.Errorf("the settlement names no work (%+v): the rejected attempt's commits are what the person is "+
			"releasing, and the release must say where they are", settled)
	}

	// The decision on origin, readable as fields. The store is opened FRESH
	// here: it is read after the release, so what it answers is what the
	// next run will fetch.
	store = openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range decisions {
		if op, _ := decision.Request["op"].(string); op != SettleOp {
			continue
		}
		if tick, _ := decision.Request["tick_id"].(string); tick != "a1" {
			continue
		}
		if number := attemptOfDecision(decision); number != latest {
			continue
		}
		found = true
		if state, _ := decision.Request["state"].(string); state != subprocess.StateFailed {
			t.Errorf("the release records the state %v, want the factory's own word", decision.Request)
		}
	}
	if !found {
		t.Fatalf("no settlement decision for a1 attempt %d on origin: %+v", latest, decisions)
	}
}

// attemptOfDecision reads the attempt number a settlement decision was
// addressed by, in the JSON shape PutDecision wrote (float64) and the one a
// hand-written record would carry (int).
func attemptOfDecision(decision runstate.Decision) int {
	switch value := decision.Request["attempt"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	}
	return 0
}
