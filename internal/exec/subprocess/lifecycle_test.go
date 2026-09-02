package subprocess

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// contracts/lifecycle-invariants.json, against THIS executor.
//
// The fixture's gate names four subjects — the reconciler and each executor
// the SPEC plans — and says an executor that has not run this suite is not a
// supported executor. internal/contracts/parity runs the fixture's own
// sequences against the fake harness, which is what proves the fixture works.
// This file is the other half: the invariants an executor can actually
// violate, run against real processes, real worktrees and a real origin.
//
// Five of the thirteen are NOT an executor's to keep, and they are named below
// rather than quietly skipped — a suite that silently covers eight of thirteen
// is a suite nobody can audit. TestEveryInvariantIsCoveredOrNamed keeps that
// list honest against the fixture in both directions.
//
// Every covered invariant carries the fixture's own discipline: the guard is
// turned OFF and the scenario must stop agreeing. A guard nothing has been
// seen to break is not known to be a guard.

// coveredInvariants maps each invariant this executor keeps to the guards it
// implements for it. The guard NAMES are the fixture's, and the test below
// requires them to match the fixture's guards for that invariant exactly.
var coveredInvariants = map[string][]string{
	"A1":  {"stop_refuses_issue", "revoke_before_teardown"},
	"A2":  {"liveness_from_outside"},
	"A5":  {"push_on_timer"},
	"A6":  {"never_redispatch_live"},
	"A7":  {"read_back_after_write"},
	"A8":  {"settle_from_evidence"},
	"A9":  {"distinct_failure_classes"},
	"A10": {"substrate_enforces_boundary"},
}

// uncoveredInvariants are the five this executor cannot keep, each with the
// reason. They are not weaker rules: they belong to the reconciler that drives
// this executor and to the host that pays for it, and both run the same suite.
var uncoveredInvariants = map[string]string{
	"A3": "the host's step cap bounds a controller's step, and a local subprocess is not a step: " +
		"the reconciler that drives this executor is what spreads a long wait across bounded legs.",
	"A4": "polling is a keepalive because a substrate wipes what nobody addresses. Nothing wipes a local " +
		"worktree, so inspect here is a READ and not a lease renewal; the cadence belongs to the reconciler.",
	"A11": "a struck-out unit is held by the reconciler before it ever asks an executor to start anything. " +
		"This executor is not asked, so it has nothing to refuse.",
	"A12": "budgets are clamped and reported by the host that issues the credential. This executor is issued " +
		"a JobSpec whose limits are already the effective ones.",
	"A13": "evidence is fingerprinted by whatever RUNS a check. A JobResult cites evidence by key; an executor " +
		"that minted an evidence record would be fingerprinting a gate it did not evaluate.",
}

// The two lists together are the thirteen, with nothing in both and nothing in
// neither — and every covered invariant's guards are exactly the fixture's, so
// a guard added upstream cannot land here as silence.
func TestEveryInvariantIsCoveredOrNamed(t *testing.T) {
	var c lifecycleFixture
	readBundle(t, "lifecycle-invariants.json", &c)

	if len(c.Invariants) != 13 {
		t.Fatalf("the fixture carries %d invariants; Appendix A has 13", len(c.Invariants))
	}
	declared := map[string]bool{}
	for _, g := range c.Harness.Guards {
		declared[g.Guard] = true
	}

	for _, inv := range c.Invariants {
		guards, covered := coveredInvariants[inv.ID]
		reason, named := uncoveredInvariants[inv.ID]
		switch {
		case covered && named:
			t.Errorf("%s is both covered and named as uncovered", inv.ID)
		case !covered && !named:
			t.Errorf("%s (%s) is neither covered by this executor nor named as one it does not keep",
				inv.ID, inv.Title)
		case named && len(reason) < 60:
			t.Errorf("%s is named as uncovered with no real reason: %q", inv.ID, reason)
		case covered:
			if strings.Join(sorted(guards), "|") != strings.Join(sorted(inv.Guards), "|") {
				t.Errorf("%s: this executor implements guards %v and the fixture declares %v",
					inv.ID, sorted(guards), sorted(inv.Guards))
			}
			for _, guard := range guards {
				if !declared[guard] {
					t.Errorf("%s: guard %q is not declared by the fixture's harness", inv.ID, guard)
				}
			}
		}
	}

	for id := range coveredInvariants {
		if !hasInvariant(c, id) {
			t.Errorf("this executor claims to cover %s, which the fixture does not declare", id)
		}
	}
	for id := range uncoveredInvariants {
		if !hasInvariant(c, id) {
			t.Errorf("this executor names %s as uncovered, and the fixture does not declare it", id)
		}
	}
}

// A1 — a stop is a durable refusal to ISSUE, checked before every boot; and
// the credential dies before the executor does.
func TestA1StopRefusesToIssueAndRevocationPrecedesTeardown(t *testing.T) {
	t.Run("stop_refuses_issue", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{mode: "report"})
		if err := f.Executor.RequestStop("operator", "called off"); err != nil {
			t.Fatal(err)
		}
		_, err := f.Executor.Start(f.spec("run-a1/tick-s1/attempt-1", "s1"))
		if refusal, ok := AsRefusal(err); !ok || refusal.Reason != RefusedStopped {
			t.Fatalf("start under a stop returned %v", err)
		}

		// Guard off: the stop is a piece of paper nobody reads, and the next
		// boot mints a credential over the operator's revocation.
		g := newFixture(t, fixtureOptions{mode: "report", guardsOff: map[string]bool{"stop_refuses_issue": true}})
		if err := g.Executor.RequestStop("operator", "called off"); err != nil {
			t.Fatal(err)
		}
		handle, err := g.Executor.Start(g.spec("run-a1/tick-s1/attempt-1", "s1"))
		if err != nil {
			t.Fatalf("with the guard off the start should have been allowed, and it failed for another reason: %v", err)
		}
		// This supervisor outlives the call: it is not tracked by g.Start, so
		// without this the fixture's cleanup does not know it exists and it
		// keeps writing into the state dir while t.TempDir() removes it.
		g.handles = append(g.handles, handle)
	})

	t.Run("revoke_before_teardown", func(t *testing.T) {
		f := newFixture(t, fixtureOptions{mode: "report"})
		handle := f.Start(f.spec("run-a1/tick-s2/attempt-1", "s2"))
		f.waitSettled(handle)
		f.collect(handle)

		err := f.Executor.Dispose(handle, DisposeOptions{})
		if refusal, ok := AsRefusal(err); !ok || refusal.Reason != RefusedCredential {
			t.Fatalf("teardown with a live credential returned %v", err)
		}

		g := newFixture(t, fixtureOptions{mode: "report", guardsOff: map[string]bool{"revoke_before_teardown": true}})
		handle = g.Start(g.spec("run-a1/tick-s3/attempt-1", "s3"))
		g.waitSettled(handle)
		g.collect(handle)
		if err := g.Executor.Dispose(handle, DisposeOptions{}); err != nil {
			t.Fatalf("with the guard off the teardown should have proceeded over a live credential: %v", err)
		}
		if g.store(handle).credentialLive() {
			// The point of the guard, stated as what it prevents.
			t.Log("the attempt was torn down while its credential was still live — which is the guard's whole subject")
		}
	})
}

// A2 — a supervisor cannot report its own death. Liveness is observed from
// OUTSIDE; what the job wrote about itself is not evidence.
func TestA2LivenessIsObservedFromOutsideNotSelfReported(t *testing.T) {
	start := func(t *testing.T, guardsOff map[string]bool) (*fixture, *JobHandle) {
		f := newFixture(t, fixtureOptions{mode: "hang", guardsOff: guardsOff})
		handle := f.Start(f.spec("run-a2/tick-t1/attempt-1", "t1"))
		st := f.store(handle)
		// Wait until the job has written something about itself, which is the
		// record a wrong implementation would read as liveness.
		waitFor(t, "the runner to say something", 20*time.Second, func() bool {
			info, err := os.Stat(st.path(fileRunnerLog))
			return err == nil && info.Size() > 0 && st.runnerPID() > 0
		})
		// Killed from outside, with no settlement recorded: the log still says
		// everything it ever said.
		_ = signalGroup(st.runnerPID(), sigKill())
		_ = signalGroup(st.supervisorPID(), sigKill())
		waitFor(t, "the processes to die", 10*time.Second, func() bool {
			return !processAlive(st.runnerPID()) && !processAlive(st.supervisorPID())
		})
		return f, handle
	}

	f, handle := start(t, nil)
	if got := f.inspect(handle).State; got != StateLost {
		t.Fatalf("state %s after the tree was killed, want %s: nothing outside says it is alive", got, StateLost)
	}

	g, gHandle := start(t, map[string]bool{"liveness_from_outside": true})
	if got := g.inspect(gHandle).State; got != StateRunning {
		t.Fatalf("with the guard off the job's own log should have been mistaken for liveness; got %s", got)
	}
}

// A5 — in-progress work is pushed on a TIMER, so a job that dies without
// warning leaves its partial work on origin.
func TestA5InProgressWorkIsPushedOnATimer(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang", pushInterval: time.Second})
	handle := f.Start(f.spec("run-a5/tick-u1/attempt-1", "u1"))
	waitFor(t, "the timer to reach origin", 30*time.Second, func() bool {
		return originHas(f.Repo.Origin, "tick/u1")
	})
	st := f.store(handle)
	_ = signalGroup(st.runnerPID(), sigKill())
	_ = signalGroup(st.supervisorPID(), sigKill())
	if !originHas(f.Repo.Origin, "tick/u1") {
		t.Fatal("the work left origin when the job was killed")
	}

	// Guard off: nothing reaches origin, because the only push left is the one
	// at exit that a killed job never makes.
	g := newFixture(t, fixtureOptions{
		mode: "hang", pushInterval: time.Second, name: "unpushed",
		guardsOff: map[string]bool{"push_on_timer": true},
	})
	gHandle := g.Start(g.spec("run-a5/tick-u2/attempt-1", "u2"))
	gst := g.store(gHandle)
	waitFor(t, "the runner to commit", 20*time.Second, func() bool {
		return headOf(g.Repo.Dir, "tick/u2") != ""
	})
	time.Sleep(3 * time.Second)
	_ = signalGroup(gst.runnerPID(), sigKill())
	_ = signalGroup(gst.supervisorPID(), sigKill())
	if originHas(g.Repo.Origin, "tick/u2") {
		t.Fatal("with the timer off, work still reached origin; the timer is not what puts it there")
	}
}

// A6 — a live job is never redispatched. Adopt by stable identity; a fresh
// attempt is created only when the previous one is proven dead.
func TestA6ALiveAttemptIsAdoptedAndAnUnansweredOneIsHeld(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	spec := f.spec("run-a6/tick-v1/attempt-1", "v1")
	first := f.Start(spec)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return f.store(first).runnerPID() > 0 })

	adopted, err := f.Executor.Start(spec)
	if err != nil {
		t.Fatalf("a second start over a live attempt: %v", err)
	}
	firstLocal, _ := first.Local()
	adoptedLocal, _ := adopted.Local()
	if adoptedLocal.PID != firstLocal.PID {
		t.Fatalf("the live attempt was redispatched: pid %d then %d", firstLocal.PID, adoptedLocal.PID)
	}

	// Unanswerable: killed with nothing recorded. "Nobody can say" is not
	// "nothing is running", and it is held rather than redispatched.
	st := f.store(first)
	_ = signalGroup(st.runnerPID(), sigKill())
	_ = signalGroup(st.supervisorPID(), sigKill())
	waitFor(t, "the tree to die", 10*time.Second, func() bool {
		return !processAlive(st.runnerPID()) && !processAlive(st.supervisorPID())
	})
	_, err = f.Executor.Start(spec)
	if refusal, ok := AsRefusal(err); !ok || refusal.Reason != RefusedUnknown {
		t.Fatalf("start over an unanswerable attempt returned %v, want it held", err)
	}

	// Guard off: the second start spawns a second supervisor over the same
	// worktree, and the run pays twice for one tick.
	g := newFixture(t, fixtureOptions{mode: "hang", name: "twice",
		guardsOff: map[string]bool{"never_redispatch_live": true}})
	gspec := g.spec("run-a6/tick-v2/attempt-1", "v2")
	one := g.Start(gspec)
	waitFor(t, "the runner to start", 20*time.Second, func() bool { return g.store(one).runnerPID() > 0 })
	two, err := g.Executor.Start(gspec)
	if err != nil {
		t.Fatalf("with the guard off the second start should have proceeded: %v", err)
	}
	oneLocal, _ := one.Local()
	twoLocal, _ := two.Local()
	if oneLocal.PID == twoLocal.PID {
		t.Fatal("with the guard off no second process was started; the guard is not what stops the redispatch")
	}
	g.handles = append(g.handles, two)
}

// A7 — read back after write. A write that silently did not land must not look
// like a job somebody can address.
func TestA7TheAttemptRecordIsReadBackBeforeAHandleIsReturned(t *testing.T) {
	dropAttempt := func(path string, data []byte, perm fs.FileMode) error {
		if filepath.Base(path) == fileAttempt {
			return nil // the write "succeeds" and lands nowhere.
		}
		return atomicWrite(path, data, perm)
	}

	f := newFixture(t, fixtureOptions{mode: "report", writeFile: dropAttempt})
	_, err := f.Executor.Start(f.spec("run-a7/tick-w1/attempt-1", "w1"))
	if err == nil {
		t.Fatal("start returned a handle for an attempt record that never landed")
	}
	if !strings.Contains(err.Error(), "did not land") {
		t.Errorf("start failed with %v; the read-back is what should have caught it", err)
	}

	// Guard off: the handle comes back, and the job it names cannot be found.
	g := newFixture(t, fixtureOptions{mode: "report", name: "unconfirmed", writeFile: dropAttempt,
		guardsOff: map[string]bool{"read_back_after_write": true}})
	handle, err := g.Executor.Start(g.spec("run-a7/tick-w2/attempt-1", "w2"))
	if err != nil {
		t.Fatalf("with the guard off start should have returned its unconfirmed handle: %v", err)
	}
	g.handles = append(g.handles, handle)
	if got := g.inspect(handle).State; got != StateLost {
		t.Fatalf("the unconfirmed handle inspects as %s; it names a record that does not exist", got)
	}
}

// A8 — an in-flight state is settled by whoever finds it next, from durable
// evidence (does the thing exist?), never by trusting the claimer to return.
func TestA8SettlementComesFromDurableEvidence(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-a8/tick-x1/attempt-1", "x1"))
	f.waitSettled(handle)

	// A second controller, which never claimed anything and never waited for
	// the first, settles it from what is on disk.
	next, err := New(Options{Repo: f.Repo.Dir, StateDir: f.StateDir, SupervisorArgv: []string{executorBin, "supervise"}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := next.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateSucceeded || !status.Terminal {
		t.Fatalf("the next actor found %s (terminal %v)", status.State, status.Terminal)
	}

	// Guard off: nothing settles it but the claimer coming back to say so,
	// which is how an in-flight state stays in flight forever.
	g := newFixture(t, fixtureOptions{mode: "report", name: "inflight",
		guardsOff: map[string]bool{"settle_from_evidence": true}})
	gHandle := g.Start(g.spec("run-a8/tick-x2/attempt-1", "x2"))
	g.waitSettled(gHandle)
	if got := g.inspect(gHandle).State; got != StateRunning {
		t.Fatalf("with the guard off the finished attempt should have stayed in flight; got %s", got)
	}
}

// A9 — never collapse distinct failure classes into one message. An absent
// report and a branch with no commits are different problems and send the next
// repair somewhere different.
func TestA9DistinctFailuresDoNotShareAMessage(t *testing.T) {
	messages := func(t *testing.T, guardsOff map[string]bool, suffix string) (noCommits, missing string) {
		t.Helper()
		a := newFixture(t, fixtureOptions{mode: "nocommit", name: "nocommit" + suffix, guardsOff: guardsOff})
		handleA := a.Start(a.spec("run-a9/tick-y1/attempt-1", "y1"))
		a.waitSettled(handleA)

		b := newFixture(t, fixtureOptions{mode: "silent", name: "silent" + suffix, guardsOff: guardsOff})
		handleB := b.Start(b.spec("run-a9/tick-y2/attempt-1", "y2"))
		b.waitSettled(handleB)

		return a.collect(handleA).Message, b.collect(handleB).Message
	}

	noCommits, missing := messages(t, nil, "-on")
	if noCommits == "" || missing == "" {
		t.Fatalf("a failing collect said nothing: %q / %q", noCommits, missing)
	}
	if noCommits == missing {
		t.Fatalf("both failures report %q", noCommits)
	}

	offNoCommits, offMissing := messages(t, map[string]bool{"distinct_failure_classes": true}, "-off")
	if offNoCommits != offMissing {
		t.Fatalf("with the guard off the two failures should have collapsed into one message; got %q and %q",
			offNoCommits, offMissing)
	}
}

// A10 — boundaries are enforced by the substrate, not requested of the model,
// and every attempt is REPORTED.
func TestA10TheBoundaryIsEnforcedAndEveryAttemptIsReported(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "boundary"})
	handle := f.Start(f.spec("run-a10/tick-z1/attempt-1", "z1"))
	f.waitSettled(handle)

	collected := f.collect(handle)
	if collected.Verdict != VerdictBoundaryViolation || len(collected.BoundaryViolations) == 0 {
		t.Fatalf("verdict %s with violations %v", collected.Verdict, collected.BoundaryViolations)
	}
	// Reported, not merely refused: the attempt is in the durable observation
	// log, where the next reader of this attempt will find it.
	observations, _ := f.store(handle).observationsFrom("")
	if !mentions(observations, "boundary violation") {
		t.Errorf("the boundary attempt was not reported:\n%s", formatObservations(observations))
	}

	g := newFixture(t, fixtureOptions{mode: "boundary", name: "unbounded",
		guardsOff: map[string]bool{"substrate_enforces_boundary": true}})
	gHandle := g.Start(g.spec("run-a10/tick-z2/attempt-1", "z2"))
	g.waitSettled(gHandle)
	if got := g.collect(gHandle).Verdict; got != VerdictReadyToMerge {
		t.Fatalf("with the guard off the tracker write should have passed unnoticed; verdict %s", got)
	}
}

func sorted(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}

func hasInvariant(c lifecycleFixture, id string) bool {
	for _, inv := range c.Invariants {
		if inv.ID == id {
			return true
		}
	}
	return false
}
