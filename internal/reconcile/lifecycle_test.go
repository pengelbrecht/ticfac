package reconcile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// contracts/lifecycle-invariants.json — EXECUTABLE, against the REAL
// reconciler and the REAL executor.
//
// internal/contracts/parity runs the fixture's sequences against the fake
// harness, which is what proves the FIXTURE works. internal/exec/subprocess
// runs the eight an executor can keep against real processes. This file is the
// third and last reader the gate names: the fixture's own steps, replayed
// through an ADAPTER onto the reconciler's real API, with real git, a real
// `.ticfac/` store on a real origin, and real attempts started by the shipped
// executor binary.
//
// The adapter is one op at a time onto a method the reconciler actually uses in
// Run. That is the whole discipline: an adapter that reached for a test-only
// path would be a suite that proves something nothing ships.
//
// Every covered invariant also carries the fixture's own negative control: the
// guard is turned OFF and the sequence must stop agreeing. A guard nothing has
// been seen to break is not known to be a guard.

// replayedInvariants are the ones whose fixture SEQUENCES run here, step for
// step, against the real reconciler. The guard names are the fixture's, and
// the coverage test below requires them to match it exactly.
var replayedInvariants = map[string][]string{
	"A3":  {"step_cap"},
	"A4":  {"poll_under_wipe"},
	"A11": {"release_by_person"},
	"A12": {"report_after_clamping"},
	"A13": {"evidence_fingerprinted", "publication_checks_freshness"},
}

// drivenInvariants are the ones whose subject is a whole RUN rather than a
// sequence of ops: they are kept by the reconciler driving the real executor
// over a real epic, and are asserted as scenarios below — each with the same
// discipline, the guard off and the behaviour changing.
var drivenInvariants = map[string][]string{
	"A6":  {"never_redispatch_live"},
	"A8":  {"settle_from_evidence"},
	"A9":  {"distinct_failure_classes"},
	"A10": {"substrate_enforces_boundary"},
}

// beneathInvariants are the three this reconciler does not keep, each with the
// reason. They are not weaker rules: they belong to the executor underneath it,
// which runs the same suite in internal/exec/subprocess/lifecycle_test.go
// against real processes.
var beneathInvariants = map[string]string{
	"A1": "a stop is a durable refusal to ISSUE a credential, and revocation before teardown is enforced where the " +
		"credential lives — in the executor. What the reconciler owns is the ORDER it asks for them in, which " +
		"TestTheCleanUpRevokesBeforeItTearsDown asserts against the real executor.",
	"A2": "liveness is observed from outside the thing that may be gone. The reconciler never observes a process at " +
		"all: it asks the executor, which is the component holding the pid and the settlement record.",
	"A5": "in-progress work reaches origin on a TIMER kept by the attempt's supervisor. The reconciler is not in that " +
		"loop; what it does with the result — refusing to merge work no remote has — is a different rule.",
	"A7": "the guard is a write that SILENTLY does not land, and the record it is about is the attempt record the " +
		"executor writes before it returns a handle. This reconciler reads its own dispatch marker back from origin " +
		"before it starts anything (claimDispatch), but the write it would have to sabotage to prove the guard is a " +
		"push, and a push that lands nowhere is a push that failed.",
}

// The three lists together are the thirteen, with nothing in two of them and
// nothing in none — and every claimed guard is exactly the fixture's, so a
// guard added upstream cannot land here as silence.
func TestEveryInvariantIsCoveredOrNamed(t *testing.T) {
	c := loadLifecycle(t)
	if len(c.Invariants) != 13 {
		t.Fatalf("the fixture carries %d invariants; Appendix A has 13", len(c.Invariants))
	}
	declared := map[string]bool{}
	for _, g := range c.Harness.Guards {
		declared[g.Guard] = true
	}

	for _, inv := range c.Invariants {
		replayed, isReplayed := replayedInvariants[inv.ID]
		driven, isDriven := drivenInvariants[inv.ID]
		reason, isNamed := beneathInvariants[inv.ID]

		claimed := 0
		for _, yes := range []bool{isReplayed, isDriven, isNamed} {
			if yes {
				claimed++
			}
		}
		switch {
		case claimed == 0:
			t.Errorf("%s (%s) is neither replayed, driven nor named as kept beneath this reconciler", inv.ID, inv.Title)
			continue
		case claimed > 1:
			t.Errorf("%s appears in more than one of the three lists", inv.ID)
			continue
		case isNamed:
			if len(reason) < 60 {
				t.Errorf("%s is named as kept beneath with no real reason: %q", inv.ID, reason)
			}
			continue
		}

		guards := replayed
		if isDriven {
			guards = driven
		}
		if strings.Join(sorted(guards), "|") != strings.Join(sorted(inv.Guards), "|") {
			t.Errorf("%s: this reconciler implements guards %v and the fixture declares %v",
				inv.ID, sorted(guards), sorted(inv.Guards))
		}
		for _, guard := range guards {
			if !declared[guard] {
				t.Errorf("%s: guard %q is not declared by the fixture's harness", inv.ID, guard)
			}
		}
	}

	for id := range replayedInvariants {
		mustDeclare(t, c, id)
	}
	for id := range drivenInvariants {
		mustDeclare(t, c, id)
	}
	for id := range beneathInvariants {
		mustDeclare(t, c, id)
	}
}

// ------------------------------------------------------------ the replay ---

// The fixture's own sequences, run against the real reconciler.
func TestTheFixturesSequencesRunAgainstTheRealReconciler(t *testing.T) {
	c := loadLifecycle(t)
	for _, inv := range c.Invariants {
		if _, ok := replayedInvariants[inv.ID]; !ok {
			continue
		}
		inv := inv
		t.Run(inv.ID, func(t *testing.T) {
			for _, seq := range inv.Sequences {
				seq := seq
				t.Run(seq.ID, func(t *testing.T) {
					a := newAdapter(t, nil)
					for i, step := range seq.Steps {
						if got := a.run(step); got != step.Expect {
							t.Fatalf("step %d (%s): outcome %q, contract says %q", i, step.Op, got, step.Expect)
						}
					}
					for _, mismatch := range a.finalMismatches(seq.Final) {
						t.Error(mismatch)
					}
				})
			}
		})
	}
}

// The negative control, ONE GUARD AT A TIME. Turning an invariant's guards off
// together cannot see a dead guard: A13 has two, and the first one's divergence
// would satisfy the whole control while the second could have stopped enforcing
// anything.
func TestDisablingAGuardBreaksTheInvariantItBelongsTo(t *testing.T) {
	c := loadLifecycle(t)
	for _, inv := range c.Invariants {
		guards, ok := replayedInvariants[inv.ID]
		if !ok {
			continue
		}
		inv, guards := inv, guards
		t.Run(inv.ID, func(t *testing.T) {
			for _, guard := range guards {
				guard := guard
				t.Run("without_"+guard, func(t *testing.T) {
					broke := false
					for _, seq := range inv.Sequences {
						a := newAdapter(t, map[string]bool{guard: true})
						diverged := false
						for _, step := range seq.Steps {
							if got := a.run(step); got != step.Expect {
								diverged = true
							}
						}
						if diverged || len(a.finalMismatches(seq.Final)) > 0 {
							broke = true
						}
					}
					if !broke {
						t.Errorf("%s passes with %s disabled — that guard is not what its sequences are testing",
							inv.ID, guard)
					}
					otherReplayedInvariantsStayGreen(t, c, inv.ID, guard)
				})
			}
		})
	}
}

// A guard belongs to the rule it enforces: with it off, every OTHER replayed
// invariant still passes. That makes the ownership executable rather than a
// naming convention.
func otherReplayedInvariantsStayGreen(t *testing.T, c lifecycleContract, owner, guard string) {
	t.Helper()
	for _, other := range c.Invariants {
		if other.ID == owner {
			continue
		}
		if _, ok := replayedInvariants[other.ID]; !ok {
			continue
		}
		for _, seq := range other.Sequences {
			a := newAdapter(t, map[string]bool{guard: true})
			for i, step := range seq.Steps {
				if got := a.run(step); got != step.Expect {
					t.Errorf("%s/%s step %d (%s) answers %q with %s's guard %s disabled, contract says %q — the guard is shared",
						other.ID, seq.ID, i, step.Op, got, owner, guard, step.Expect)
				}
			}
			for _, mismatch := range a.finalMismatches(seq.Final) {
				t.Errorf("%s/%s final state moved when %s's guard %s was disabled: %s",
					other.ID, seq.ID, owner, guard, mismatch)
			}
		}
	}
}

// ----------------------------------------------------------- the adapter ---

// adapter maps the fixture's op vocabulary onto the real reconciler.
//
// Everything it touches is production API. The clock is injected — a test that
// waited out thirty real minutes to see a wipe threshold would not be run — and
// so is the runner, because the agent is the one component these sequences are
// not about. Nothing else here is a stand-in.
type adapter struct {
	t *testing.T

	fixture *fixture
	r       *Reconciler
	store   *runstate.Store

	clock time.Time
	step  *Step

	guardsOff map[string]bool

	handles   map[string]*subprocess.JobHandle
	executors map[string]Executor
	confirmed map[string]bool
	written   map[string][]byte
}

func newAdapter(t *testing.T, guardsOff map[string]bool) *adapter {
	t.Helper()
	return &adapter{
		t: t, clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), guardsOff: guardsOff,
		handles: map[string]*subprocess.JobHandle{}, executors: map[string]Executor{},
		confirmed: map[string]bool{}, written: map[string][]byte{},
	}
}

// reconciler builds the real thing, lazily: a sequence that never needs a
// repository never makes one.
func (a *adapter) reconciler() *Reconciler {
	if a.r != nil {
		return a.r
	}
	a.fixture = newFixture(a.t, fixtureOptions{mode: "hang"})
	opts := a.fixture.options(a.fixture.Repo, fixtureOptions{guardsOff: a.guardsOff})
	opts.Now = func() time.Time { return a.clock }
	// The cadence is the FIXTURE's, because that is what the sequences are
	// stepping through. Nothing here invents a number.
	thresholds := loadThresholds(a.t)
	opts.WipeThreshold = time.Duration(thresholds.WipeThresholdMs) * time.Millisecond
	opts.PollInterval = time.Duration(thresholds.MaxPollMs) * time.Millisecond
	opts.StepCap = time.Duration(thresholds.StepCapMs) * time.Millisecond
	r, err := New(opts)
	if err != nil {
		a.t.Fatal(err)
	}
	// The integration branch and the store are what Run prepares first; the
	// adapter prepares the same things, because the ops that write records
	// write them to the same place a run would.
	base, err := r.git.ensureRemoteBranch(r.branch, r.opts.BaseRef)
	if err != nil {
		a.t.Fatal(err)
	}
	r.base, r.baseRef = base, refFor(r.branch)
	store, err := runstate.Open(runstate.Options{
		Repo: r.opts.Repo, Remote: r.opts.Remote, Branch: r.branch, RunID: r.runID, Now: r.now,
	})
	if err != nil {
		a.t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		a.t.Fatal(err)
	}
	r.store, a.store = store, store
	a.r = r
	return r
}

// legPath maps a fixture path onto the run-state directory. The fixture names
// paths in the abstract (`runs/r-2f9c/wave.json`); this store writes under
// `.ticfac/`, which is the only place it will write at all.
func (a *adapter) legPath(path string) string {
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(path)
	return runstate.RunDir(a.reconciler().runID) + "/legs/" + slug + ".json"
}

func (a *adapter) run(s lifecycleStep) string {
	a.t.Helper()
	switch s.Op {

	// ---- the clock. Injected, so a threshold measured in minutes is testable.

	case "advance_clock":
		a.clock = a.clock.Add(time.Duration(s.Ms) * time.Millisecond)
		return "advanced"

	// ---- A3: the host's step cap, and a leg that re-derives what it knows.

	case "open_step":
		a.step = a.reconciler().OpenStep(time.Duration(s.CapMs) * time.Millisecond)
		return "opened"

	case "spend_in_step":
		if a.step == nil {
			a.t.Fatalf("spend_in_step with no open step")
		}
		return string(a.step.Spend(time.Duration(s.Ms) * time.Millisecond))

	case "write_record":
		content, err := json.Marshal(s.Content)
		if err != nil {
			a.t.Fatal(err)
		}
		if !s.SilentlyDrops {
			outcome, err := a.reconciler().store.CreateIfAbsent(a.legPath(s.Path), append(content, '\n'))
			if err != nil {
				a.t.Fatal(err)
			}
			if outcome.EffectPermitted() {
				a.written[s.Path] = content
			}
		}
		// The writer believes it landed either way. Only the read-back knows.
		return "written"

	case "read_back":
		// Re-read from ORIGIN, not from what this writer remembers writing.
		if _, err := a.reconciler().store.Fetch(); err != nil {
			a.t.Fatal(err)
		}
		if _, ok, err := a.store.Read(a.legPath(s.Path)); err != nil {
			a.t.Fatal(err)
		} else if ok {
			a.confirmed[s.Path] = true
			return "confirmed"
		}
		a.confirmed[s.Path] = false
		return "write_did_not_land"

	case "act_on":
		if !a.confirmed[s.Path] {
			return "refused_unconfirmed"
		}
		return "acted"

	// ---- A4: polling IS the keepalive, over a REAL attempt.

	case "issue_credential":
		// The executor issues the credential inside start; what the reconciler
		// decides beforehand is the dispatch. A stop would refuse here, and
		// that refusal is the executor's — this reconciler asks for one.
		a.executors[s.Job] = a.executorFor(s.Job)
		return "issued"

	case "boot":
		executor, ok := a.executors[s.Job]
		if !ok {
			return "refused_no_credential"
		}
		handle, err := executor.Start(a.jobSpecFor(s.Job))
		if err != nil {
			a.t.Fatalf("boot %s: %v", s.Job, err)
		}
		a.handles[s.Job] = handle
		a.reconciler().noteAlive(s.Job)
		return "booted"

	case "poll":
		// The real cadence guard, over a real live attempt: inspect is the
		// address, and Poll is what decides whether the address was in time.
		if handle, ok := a.handles[s.Job]; ok {
			if _, err := a.executors[s.Job].Inspect(handle, ""); err != nil {
				a.t.Fatalf("inspect %s: %v", s.Job, err)
			}
		}
		return string(a.reconciler().Poll(s.Job))

	// ---- A11: a person releases a struck-out unit; the clock never does.

	case "strike_out":
		a.reconciler().StrikeOut(s.Unit, "struck out by the fixture")
		return "struck"

	case "clock_release":
		return a.reconciler().ClockRelease(s.Unit)

	case "person_release":
		return a.reconciler().PersonRelease(s.Unit, s.By)

	case "may_dispatch":
		return string(a.reconciler().MayDispatch(s.Unit))

	// ---- A12: report the number that will govern.

	case "set_budget":
		return a.reconciler().SetBudget(s.Requested, s.Ceiling)

	case "report_budget":
		return a.reconciler().ReportBudget()

	// ---- A13: evidence is fingerprinted, and publication checks freshness.

	case "record_evidence":
		return a.reconciler().RecordEvidence(s.Key, Fingerprint(s.Fingerprint))

	case "publish_evidence":
		return a.reconciler().PublishEvidence(s.Key, Fingerprint(s.Target))

	default:
		a.t.Fatalf("the fixture uses op %q, which this adapter does not implement against the real reconciler", s.Op)
		return ""
	}
}

func (a *adapter) executorFor(job string) Executor {
	a.t.Helper()
	r := a.reconciler()
	executor, err := r.opts.NewExecutor(Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: job, Attempt: 1,
		JobID: job, Role: "implement-tick", Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: "refs/heads/tick/" + job, BaseSHA: r.base,
		StateDir: filepath.Join(r.opts.ExecStateRoot, r.runID, job, "1"),
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return executor
}

func (a *adapter) jobSpecFor(job string) *subprocess.JobSpec {
	r := a.reconciler()
	return r.jobSpec(Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: job, Attempt: 1,
		JobID: job, Role: "implement-tick", Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: "refs/heads/tick/" + job, BaseSHA: r.base,
	})
}

// finalMismatches asserts every field the sequence declared, and only those. A
// field a sequence does not mention is not part of what its invariant is about.
func (a *adapter) finalMismatches(want lifecycleFinal) []string {
	var bad []string
	cmp := func(label string, got, expect any) {
		if canonical(got) != canonical(expect) {
			bad = append(bad, label+":\n  the reconciler holds  "+canonical(got)+"\n  the contract says     "+canonical(expect))
		}
	}
	if want.StepSpentMs != nil {
		var spent int64
		if a.step != nil {
			spent = a.step.Spent().Milliseconds()
		}
		cmp("step_spent_ms", spent, *want.StepSpentMs)
	}
	if want.Liveness != nil {
		got := map[string]string{}
		for job := range want.Liveness {
			got[job] = a.reconciler().Liveness(job)
		}
		cmp("liveness", got, want.Liveness)
	}
	if want.ReleasedBy != nil {
		cmp("released_by", a.reconciler().ReleasedBy(), want.ReleasedBy)
	}
	if want.Budget != nil {
		cmp("budget", a.reconciler().Budget(), *want.Budget)
	}
	if want.EvidenceKeys != nil {
		cmp("evidence_keys", a.reconciler().EvidenceKeys(), want.EvidenceKeys)
	}
	if want.PublishedKeys != nil {
		cmp("published_keys", a.reconciler().PublishedKeys(), want.PublishedKeys)
	}
	return bad
}

// ------------------------------------------------------------ the driven ---

// A6 — a live attempt is adopted by identity and never redispatched. The cut
// lands after the dispatch, so the marker is on origin and a job is running;
// the restart must find it rather than start a second one.
func TestA6ARestartAdoptsRatherThanRedispatching(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)

	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "adopting"))
	restarted, result, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatalf("the restart did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the restart ended %s: %s", result.State, result.Reason)
	}
	if got := restarted.Stages("a1"); !contains(got, StageAdopted) || contains(got, StageDispatched) {
		t.Fatalf("the restart's stages for a1 are %v; it should have adopted and not dispatched", got)
	}
	// The executor was asked to start a1's attempt exactly once, across both
	// incarnations. That is the number the guard is about.
	if got := f.startCount("run-r-fixture/tick-a1/attempt-1"); got != 1 {
		t.Fatalf("the executor was asked to start a1's first attempt %d times", got)
	}

	// Guard off: the restart dispatches over the attempt the previous
	// incarnation started. The executor refuses it as already settled, so the
	// run stops — one tick, two dispatch attempts, and nothing finishes.
	g := newFixture(t, fixtureOptions{})
	_, _, err = g.run(g.Repo, fixtureOptions{stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	gClone := cloneRepo(t, g.Repo.Origin, filepath.Join(g.Root, "redispatching"))
	unguarded, _, err := g.run(gClone, fixtureOptions{guardsOff: map[string]bool{"never_redispatch_live": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := unguarded.Stages("a1"); !contains(got, StageDispatched) {
		t.Fatalf("with the guard off the restart should have dispatched a1 again; stages %v", got)
	}
	// Two dispatch markers for one tick: the run paid for it twice.
	store, err := runstate.Open(runstate.Options{
		Repo: gClone.Dir, Remote: "origin", Branch: unguarded.IntegrationBranch(), RunID: unguarded.RunID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	forA1 := 0
	for _, attempt := range attempts {
		if attempt.TickID == "a1" {
			forA1++
		}
	}
	if forA1 < 2 {
		t.Fatalf("with the guard off a1 has %d dispatch markers; the redispatch is what the guard prevents", forA1)
	}
}

// A8 — an in-flight state is settled by whoever finds it next, from durable
// evidence. The claimer is killed and never comes back; the next reconciler
// reads the branch and the report and settles it.
func TestA8ARestartSettlesAnInFlightAttemptFromDurableEvidence(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	_, _, err := f.run(f.Repo, fixtureOptions{stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)

	clone := cloneRepo(t, f.Repo.Origin, filepath.Join(f.Root, "settling"))
	restarted, _, err := f.run(clone, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.Stages("a1"); !contains(got, StageCollected) {
		t.Fatalf("the attempt was never settled by the incarnation that found it: %v", got)
	}

	// Guard off: nothing settles an attempt nobody can address, and the wait
	// goes on until something outside stops it — which is how an in-flight
	// state stays in flight forever.
	g := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err = g.run(g.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	g.stopEverything()

	gClone := cloneRepo(t, g.Repo.Origin, filepath.Join(g.Root, "stuck"))
	unguarded, err := New(g.options(gClone, fixtureOptions{
		mode: "hang", guardsOff: map[string]bool{"settle_from_evidence": true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := unguarded.Run(ctx); err == nil {
		t.Fatal("with the guard off the run should never have settled the attempt nobody can address")
	}
}

// A9 — never collapse distinct failure classes into one message. A failing gate
// and a boundary violation are different problems and send the next repair
// somewhere different.
func TestA9DistinctRefusalsDoNotShareAMessage(t *testing.T) {
	messages := func(t *testing.T, guardsOff map[string]bool) (gate, boundary string) {
		t.Helper()
		a := newFixture(t, fixtureOptions{gate: failingGate})
		_, gateResult, err := a.run(a.Repo, fixtureOptions{gate: failingGate, guardsOff: guardsOff})
		if err != nil {
			t.Fatal(err)
		}
		b := newFixture(t, fixtureOptions{mode: "boundary"})
		_, boundaryResult, err := b.run(b.Repo, fixtureOptions{mode: "boundary", guardsOff: guardsOff})
		if err != nil {
			t.Fatal(err)
		}
		return failureMessage(t, gateResult), failureMessage(t, boundaryResult)
	}

	gate, boundary := messages(t, nil)
	if gate == "" || boundary == "" {
		t.Fatalf("a failing run said nothing: %q / %q", gate, boundary)
	}
	if gate == boundary {
		t.Fatalf("both failures report %q", gate)
	}

	offGate, offBoundary := messages(t, map[string]bool{"distinct_failure_classes": true})
	if offGate != offBoundary {
		t.Fatalf("with the guard off the two failures should have collapsed into one message; got %q and %q",
			offGate, offBoundary)
	}
}

func failureMessage(t *testing.T, result *Result) string {
	t.Helper()
	if result.Failure == nil {
		t.Fatalf("the run ended %s with no refusal to read: %s", result.State, result.Reason)
	}
	return result.Failure.Message
}

// A10 — the boundary is enforced by the substrate and every attempt is
// REPORTED. The executor refuses the write; the reconciler is what refuses the
// MERGE and says so where a person reading the run will find it.
func TestA10ABoundaryViolationIsReportedAndRefusesTheMerge(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "boundary"})
	r, result, err := f.run(f.Repo, fixtureOptions{mode: "boundary"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateFailed || contains(r.Stages("a1"), StageIntegrated) {
		t.Fatalf("the boundary-violating attempt reached %s with stages %v", result.State, r.Stages("a1"))
	}

	g := newFixture(t, fixtureOptions{mode: "boundary"})
	unguarded, gResult, err := g.run(g.Repo, fixtureOptions{
		mode: "boundary", guardsOff: map[string]bool{"substrate_enforces_boundary": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(unguarded.Stages("a1"), StageIntegrated) {
		t.Fatalf("with the guard off the tracker write should have passed unnoticed into %s; stages %v, run %s",
			g.Repo.Origin, unguarded.Stages("a1"), gResult.State)
	}
}

// A1's half that IS the reconciler's: the ORDER. The credential dies first, and
// only then is the attempt torn down — a container torn down before its
// credential is revoked can spend on the way out.
func TestTheCleanUpRevokesBeforeItTearsDown(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	var order []string
	f.wrap = func(inner Executor) Executor { return &orderingExecutor{Executor: inner, order: &order} }

	if _, _, err := f.run(f.Repo, fixtureOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(order) == 0 {
		t.Fatal("the run tore nothing down")
	}
	for i, call := range order {
		if call == "dispose" {
			if i == 0 || order[i-1] != "cancel" {
				t.Fatalf("dispose at %d is not preceded by cancel: %v", i, order)
			}
		}
	}
}

type orderingExecutor struct {
	Executor
	order *[]string
}

func (e *orderingExecutor) Cancel(handle *subprocess.JobHandle) (*subprocess.CancelAck, error) {
	*e.order = append(*e.order, "cancel")
	return e.Executor.Cancel(handle)
}

func (e *orderingExecutor) Dispose(handle *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	*e.order = append(*e.order, "dispose")
	return e.Executor.Dispose(handle, opts)
}

// -------------------------------------------------------- fixture reading ---

type lifecycleStep struct {
	Op     string `json:"op"`
	Expect string `json:"expect"`

	Job           string         `json:"job"`
	By            string         `json:"by"`
	As            string         `json:"as"`
	Tick          string         `json:"tick"`
	Path          string         `json:"path"`
	EvidencePath  string         `json:"evidence_path"`
	Actor         string         `json:"actor"`
	Content       map[string]any `json:"content"`
	SilentlyDrops bool           `json:"silently_drops"`
	Ms            int64          `json:"ms"`
	CapMs         int64          `json:"cap_ms"`
	Class         string         `json:"class"`
	Message       string         `json:"message"`
	Unit          string         `json:"unit"`
	Requested     float64        `json:"requested"`
	Ceiling       float64        `json:"ceiling"`
	Key           string         `json:"key"`

	Fingerprint map[string]string `json:"fingerprint"`
	Target      map[string]string `json:"target"`
}

type lifecycleFinal struct {
	Liveness      map[string]string `json:"liveness"`
	StepSpentMs   *int64            `json:"step_spent_ms"`
	ReleasedBy    map[string]string `json:"released_by"`
	Budget        *Budget           `json:"budget"`
	EvidenceKeys  []string          `json:"evidence_keys"`
	PublishedKeys []string          `json:"published_keys"`
}

type lifecycleSequence struct {
	ID    string          `json:"id"`
	Why   string          `json:"why"`
	Steps []lifecycleStep `json:"steps"`
	Final lifecycleFinal  `json:"final"`
}

type lifecycleThresholds struct {
	WipeThresholdMs int64 `json:"wipe_threshold_ms"`
	MaxPollMs       int64 `json:"max_poll_ms"`
	PushIntervalMs  int64 `json:"push_interval_ms"`
	StepCapMs       int64 `json:"step_cap_ms"`
}

type lifecycleContract struct {
	Harness struct {
		Thresholds lifecycleThresholds `json:"thresholds"`
		Guards     []struct {
			Guard string `json:"guard"`
		} `json:"guards"`
	} `json:"harness"`
	Invariants []struct {
		ID        string              `json:"id"`
		Title     string              `json:"title"`
		Guards    []string            `json:"guards"`
		Sequences []lifecycleSequence `json:"sequences"`
	} `json:"invariants"`
}

func loadLifecycle(t *testing.T) lifecycleContract {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "lifecycle-invariants.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c lifecycleContract
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse lifecycle-invariants.json: %v", err)
	}
	return c
}

func loadThresholds(t *testing.T) lifecycleThresholds {
	t.Helper()
	return loadLifecycle(t).Harness.Thresholds
}

// Appendix A #4's relationship, pinned in ONE place: this package's defaults
// are the fixture's numbers, not a second copy of them.
func TestTheDefaultCadenceIsTheFixtures(t *testing.T) {
	thresholds := loadThresholds(t)
	for _, check := range []struct {
		name string
		got  time.Duration
		want int64
	}{
		{"wipe_threshold_ms", DefaultWipeThreshold, thresholds.WipeThresholdMs},
		{"max_poll_ms", DefaultPollInterval, thresholds.MaxPollMs},
		{"step_cap_ms", DefaultStepCap, thresholds.StepCapMs},
	} {
		if check.got.Milliseconds() != check.want {
			t.Errorf("%s: this package uses %s and the fixture pins %dms", check.name, check.got, check.want)
		}
	}
	if DefaultPollInterval*2 > DefaultWipeThreshold {
		t.Errorf("a poll every %s is not WELL under a wipe threshold of %s", DefaultPollInterval, DefaultWipeThreshold)
	}
}

func mustDeclare(t *testing.T, c lifecycleContract, id string) {
	t.Helper()
	for _, inv := range c.Invariants {
		if inv.ID == id {
			return
		}
	}
	t.Errorf("this reconciler names %s, which the fixture does not declare", id)
}

func sorted(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func canonical(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(data)
}
