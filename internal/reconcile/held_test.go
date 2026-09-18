package reconcile

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A held attempt must not become a stall (tick 0z0). A held attempt is CORRECT
// and must stay; what turns it into a stall is that nobody is told, and that
// clearing it throws the work away. These tests are the tick's acceptance
// criteria in its smallest form:
//
//   - a run that ends holding an attempt says so on the feed, naming the tick
//     and the attempt, with the refusal's own reason — a line a watcher can
//     surface to a human without matching on a sentence;
//   - a release can carry the released attempt's commits forward, so the next
//     attempt starts from them, the gate still deciding what merges, and the
//     provenance recording where the work came from;
//   - a release that does not carry starts the next attempt where every
//     dispatch starts: the integration branch as origin has it now.

// A run that stops holding an attempt for a person writes the one line a
// watcher needs: a distinct STAGE — never a sentence to match — carrying the
// tick and the attempt it is about, with the refusal's own reason leading the
// detail.
func TestARunThatEndsHoldingAnAttemptSaysSoOnTheFeed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// The supervisor dies without settling anything: that is `lost`, and the
	// next run holds the attempt rather than redispatching it.
	f.stopEverything()

	_, held, err := f.run(f.Repo, fixtureOptions{mode: "hang"})
	if err != nil {
		t.Fatalf("the run that found the lost attempt errored: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the lost attempt was not held: %+v", held.Failure)
	}

	events, err := runfeed.Read(runfeed.Path(f.Repo.Dir, "r-fixture"))
	if err != nil {
		t.Fatalf("read the run's feed: %v", err)
	}
	var line *runfeed.Event
	finished := -1
	for i, event := range events {
		if event.Stage == StageRunHeld && line == nil {
			copy := event
			line = &copy
		}
		if event.Stage == StageRunFinished {
			finished = i
		}
	}
	if line == nil {
		t.Fatalf("no %s line on the feed: the run ended holding a1 and said so only to the durable records", StageRunHeld)
	}
	if line.TickID == nil || *line.TickID != "a1" {
		t.Errorf("the held line names tick %v, want a1", line.TickID)
	}
	if line.Attempt == nil || *line.Attempt != 1 {
		t.Errorf("the held line names attempt %v, want 1", line.Attempt)
	}
	if !strings.Contains(line.Detail, RefusedUnaddressed) {
		t.Errorf("the held line's detail %q does not lead with the refusal's own reason", line.Detail)
	}
	// The hold is stated before the run's terminal line, so a watcher that
	// stops at run_finished has already seen it.
	for i, event := range events {
		if event.Stage == StageRunHeld && finished >= 0 && i > finished {
			t.Errorf("the held line landed after run_finished: the run was over before it said what it was holding")
		}
	}
}

// waitHeldWork waits for the hung attempt of a tick to COMMIT its work — the
// observable every carry test depends on, polled cheaply where it lives (the
// branch in this checkout), never a guessed sleep. The fixture kills the run
// the moment the dispatch is recorded, which is before the runner has
// necessarily written anything; a test that killed first and asked about the
// work after would be testing a race, not the carry.
func waitHeldWork(t *testing.T, f *fixture, tick string) (branch, base, head string) {
	t.Helper()
	branch = fmt.Sprintf("refs/heads/ticfac/run-r-fixture/tick-%s/attempt-1", tick)
	base = f.dispatch(tick).BaseSHA
	deadline := time.Now().Add(10 * time.Second)
	for {
		head = branchHead(f.Repo.Dir, branch)
		if head != "" && head != base {
			return branch, base, head
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hung attempt of %s never committed: %s is %q, base %q", tick, branch, head, base)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A release can CARRY the released attempt's work: the next attempt is cut
// from the released branch, so the worker starts from the work rather than
// redoing it. The gate still decides — nothing merges unproven — and the
// records say where the work came from: the provenance's source IS the
// released attempt's ref and commit, and the marker's open handle carries the
// explicit resume.
func TestAReleasedAttemptCanCarryItsWorkToTheNextAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// The work the release will be asked to carry: wait for the runner to
	// COMMIT it before anything is killed — the commit is the observable.
	releasedRef, _, releasedHead := waitHeldWork(t, f, "a1")
	f.stopEverything()

	_, held, err := f.run(f.Repo, fixtureOptions{mode: "hang"})
	if err != nil {
		t.Fatalf("the run that found the lost attempt errored: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the lost attempt was not held: %+v", held.Failure)
	}

	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := settler.SettleCarry(context.Background(), "a1", 1, "an operator")
	if err != nil {
		t.Fatalf("settle a1's lost attempt carrying its work: %v", err)
	}
	if !settled.Carried || settled.CarryRef != releasedRef || settled.CarrySHA != releasedHead {
		t.Fatalf("the settlement is %+v, want the work carried from %s at %s", settled, releasedRef, releasedHead)
	}

	// It is a DECISION on origin, and the carry is a FIELD of it — never a
	// sentence to match.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range decisions {
		if decision.Request["op"] != settleOp || decision.Request["tick_id"] != "a1" {
			continue
		}
		found = true
		if decision.Response["disposition"] != "carry-work" {
			t.Errorf("the release's disposition is %v, want carry-work", decision.Response["disposition"])
		}
		if decision.Response["carry_ref"] != releasedRef || decision.Response["carry_sha"] != releasedHead {
			t.Errorf("the release does not name the work it carries: %v", decision.Response)
		}
	}
	if !found {
		t.Fatalf("no settlement decision on origin: %+v", decisions)
	}

	// The next run dispatches a new attempt FROM THE RELEASED WORK, and the
	// gate still decides what merges.
	f.Runner = fakeRunnerArgv(t, "report")
	next, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after a carrying release did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the run after a carrying release ended %s: %s", resumed.State, resumed.Reason)
	}
	if !contains(resumed.Closed, "a1") {
		t.Fatalf("a1 was not closed: %+v", resumed.Ticks)
	}
	spec := f.spec("a1")
	if spec == nil {
		t.Fatal("a1 was never dispatched")
	}
	if spec.Source.BaseSHA != releasedHead {
		t.Fatalf("the next attempt was cut from %s, want the released work at %s",
			spec.Source.BaseSHA, releasedHead)
	}
	stages := next.Stages("a1")
	if !contains(stages, StageSettled) {
		t.Errorf("a1's stages %v do not show the released attempt skipped", stages)
	}
	if !contains(stages, StageCarried) {
		t.Errorf("a1's stages %v do not record that the new attempt starts from the released work", stages)
	}
	// The carried work reached the integration branch — behind the gate, which
	// is the whole claim of "carry": the next worker starts from the work, and
	// the gate still decides what merges.
	if !containsCommit(t, f, releasedHead, "origin/epic/qeu") {
		t.Fatalf("the released work never reached the integration branch behind the gate")
	}

	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	attempt2, ok, err := store.Attempt(2)
	if err != nil || !ok {
		t.Fatalf("attempt 2 was never recorded: %v", err)
	}
	// The design question this tick had to answer, answered in the closed
	// object: a resumed-from-work attempt's SOURCE is the released attempt's
	// ref and commit, so "this work came from a released attempt" is a claim
	// provenance makes in fields it already has.
	if attempt2.Provenance.SourceRef != releasedRef {
		t.Errorf("attempt 2's provenance names source_ref %q, want the released attempt's %q",
			attempt2.Provenance.SourceRef, releasedRef)
	}
	if attempt2.Provenance.SourceSHA != releasedHead {
		t.Errorf("attempt 2's provenance names source_sha %s, want the released work at %s",
			attempt2.Provenance.SourceSHA, releasedHead)
	}
	resumedFrom, _ := attempt2.JobHandle["resumed_from"].(map[string]any)
	if resumedFrom == nil {
		t.Fatalf("attempt 2's marker carries no resumed_from: %v", attempt2.JobHandle)
	}
	if resumedFrom["tick_id"] != "a1" || resumedFrom["attempt"] != float64(1) {
		t.Errorf("the marker's resumed_from does not name the released attempt: %v", resumedFrom)
	}
	if resumedFrom["released_by"] != releaseHandle("an operator") {
		t.Errorf("the marker's resumed_from does not name who released it as a stable handle: %v", resumedFrom)
	}

	// Settling the same attempt twice writes nothing, carry or no carry: a
	// decision is created if absent and never rewritten.
	again, err := settler.SettleCarry(context.Background(), "a1", 1, "an operator")
	if err != nil || again.Recorded {
		t.Fatalf("a second settlement wrote a second record: %+v (%v)", again, err)
	}
}

// A release that does NOT carry works exactly as it did: the next attempt is
// cut from the integration branch, and the released attempt's commits stay on
// their own write ref.
func TestAReleaseWithoutCarryStartsTheNextAttemptFromTheIntegrationBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// The released attempt must actually hold work, or the "not carried"
	// assertion below would pass vacuously.
	releasedRef, _, releasedHead := waitHeldWork(t, f, "a1")
	f.stopEverything()

	_, held, err := f.run(f.Repo, fixtureOptions{mode: "hang"})
	if err != nil {
		t.Fatalf("the run that found the lost attempt errored: %v", err)
	}
	if held.Failure == nil || held.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the lost attempt was not held: %+v", held.Failure)
	}

	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := settler.Settle(context.Background(), "a1", 1, "an operator")
	if err != nil {
		t.Fatalf("settle a1's lost attempt: %v", err)
	}
	if settled.Carried {
		t.Fatalf("a plain release carried the work: %+v", settled)
	}

	// Carry may not be written over a release that made no such decision: the
	// release stands as the person made it, and the refusal says so rather
	// than quietly rewriting a decision.
	if _, err := settler.SettleCarry(context.Background(), "a1", 1, "an operator"); err == nil {
		t.Fatal("a carry was recorded over a release that decided nothing of the kind")
	} else if !strings.Contains(err.Error(), "never rewritten") {
		t.Errorf("the refusal does not say the decision stands: %v", err)
	}

	// The next run dispatches a new attempt from the INTEGRATION branch: the
	// released work is not carried, is not adopted, and stays on its ref.
	f.Runner = fakeRunnerArgv(t, "report")
	_, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after a plain release did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the run after a plain release ended %s: %s", resumed.State, resumed.Reason)
	}
	spec := f.spec("a1")
	if spec == nil {
		t.Fatal("a1 was never dispatched")
	}
	if spec.Source.BaseSHA == releasedHead {
		t.Fatal("a plain release based the next attempt on the released work")
	}
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	attempt2, ok, err := store.Attempt(2)
	if err != nil || !ok {
		t.Fatalf("attempt 2 was never recorded: %v", err)
	}
	if attempt2.Provenance.SourceRef == releasedRef {
		t.Errorf("attempt 2's provenance names the released attempt's ref although the release carried nothing")
	}
	if attempt2.JobHandle["resumed_from"] != nil {
		t.Errorf("attempt 2's marker claims a resume: %v", attempt2.JobHandle["resumed_from"])
	}
}

// Carrying is refused when the released attempt left nothing to carry: the
// person asked for the work to go forward, and there is none, so the release
// is told to say what it is instead of recording a carry with nothing in it.
func TestCarryingAReleaseRefusesAnAttemptThatLeftNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{mode: "nocommit"})
	_, failed, err := f.run(f.Repo, fixtureOptions{mode: "nocommit"})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if failed.State != runstate.StateFailed {
		t.Fatalf("the run ended %s, want failed: %s", failed.State, failed.Reason)
	}
	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "nocommit"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = settler.SettleCarry(context.Background(), "a1", 1, "an operator")
	if err == nil {
		t.Fatal("a carry was accepted for an attempt that committed nothing")
	}
	if !strings.Contains(err.Error(), "no commit beyond") {
		t.Errorf("the refusal does not say there is no work to carry: %v", err)
	}
	// The plain release still works: that is what the attempt actually calls
	// for — a fresh attempt from the integration branch.
	if _, err := settler.Settle(context.Background(), "a1", 1, "an operator"); err != nil {
		t.Fatalf("the plain release was refused: %v", err)
	}
}
