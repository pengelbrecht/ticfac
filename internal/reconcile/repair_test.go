package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The repairs the final review of epic qeu asked for, each as the assertion the
// finding was missing.

// A run that stopped because a tick did not pass is RESUMABLE under the same
// run id.
//
// Every per-tick refusal ends the run with a `failed` checkpoint, and `failed`
// is terminal — so a restart used to read "the run is already failed" and stop
// before it reached the spent-attempt redispatch at all. The redispatch was
// only ever reachable in the crash window between the rejection and that
// checkpoint, which is exactly where the durable_test cuts. An operator whose
// worker answered BLOCKED had one route left: a new --run-id, which starts a
// run whose attempt numbers and evidence keys have nothing to do with the
// records of the one it is continuing.
func TestARunStoppedByARefusalResumesUnderTheSameRunID(t *testing.T) {
	blocked := fixtureOptions{mode: "blocked-first"}
	f := newFixture(t, blocked)

	// The whole first run, to its own end: no simulated kill anywhere. a1's
	// worker answers BLOCKED with nothing committed, the attempt is rejected,
	// and the run writes the terminal checkpoint a restart reads.
	first, result, err := f.run(f.Repo, blocked)
	if err != nil {
		t.Fatalf("the first run should have finished with a failed state, not an error: %v", err)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the first run ended %s: %s", result.State, result.Reason)
	}
	if got := first.Stages("a1"); !contains(got, StageRejected) {
		t.Fatalf("a1's stages %v do not record the rejection", got)
	}
	if !strings.Contains(result.Reason, "resumes it") {
		t.Errorf("the failed run's reason does not tell an operator it can be resumed: %q", result.Reason)
	}

	// The restart. Same run id, same integration branch, no flag: what the
	// operator does is run the epic again once the blocker is closed.
	f.Runner = fakeRunnerArgv(t, "report")
	second, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the resumed run did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the resumed run ended %s: %s", resumed.State, resumed.Reason)
	}
	if got := second.Stages(""); !contains(got, StageResumed) {
		t.Errorf("the run-level stages %v do not record that a stopped run was resumed", got)
	}
	if got := second.Stages("a1"); !contains(got, StageRedispatched) {
		t.Errorf("the resumed run did not redispatch the spent attempt: %v", got)
	}

	current, err := f.Tracker.Show(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "closed" {
		t.Errorf("a1 is %s after the resumed run", current.Status)
	}

	// A NEW attempt under the SAME run: two markers for a1 in one run's
	// attempt numbering, which is the whole point of resuming rather than
	// starting a run of its own.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
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
	if forA1 != 2 {
		t.Errorf("%d dispatch markers for a1 in run r-fixture; the resume should have added exactly one", forA1)
	}
}

// A COMPLETED run is still not run again. Resuming a run that stopped is not
// the same permission as replaying one that finished, and the difference is
// what keeps a replay from reclosing anything.
func TestACompletedRunIsStillNotResumed(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	if _, result, err := f.run(f.Repo, fixtureOptions{}); err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the first run ended %v: %v", result, err)
	}
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the replay ended %s", result.State)
	}
	for _, event := range r.Journal() {
		if event.Stage != StageRunFinished && event.Stage != StageResumed {
			t.Fatalf("the replay of a completed run did work: %+v", r.Journal())
		}
	}
}

// The wait for an attempt to settle is BOUNDED by the reconciler itself.
//
// The job's wall clock is the supervisor's to enforce, and a supervisor that
// died without settling enforces nothing: `running` then rests on a recorded
// pid, and a pid is a number the host reuses — the fake below is exactly that,
// an inspect that keeps answering `running` about an attempt nobody is
// running. Poll's wipe threshold does not catch it (it measures the interval
// between polls, not the age of the job), so before this bound the run
// addressed a dead attempt forever at perfect cadence.
func TestTheSettlementWaitIsBoundedByTheAttemptsOwnWallClock(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	f.wrap = func(inner Executor) Executor { return &alwaysRunningExecutor{Executor: inner} }

	opts := f.options(f.Repo, fixtureOptions{mode: "hang"})
	// A clock the wait itself advances: the deadline is minutes away in the
	// run's own terms and milliseconds away in the test's.
	clock := &testClock{at: time.Now()}
	opts.Now = clock.now
	opts.Sleep = clock.advance
	opts.PollInterval = 2 * time.Second
	opts.WipeThreshold = 20 * time.Second
	opts.StepCap = time.Hour
	opts.WallSeconds = 10

	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var result *Result
	var runErr error
	go func() {
		defer close(done)
		result, runErr = r.RunProtected(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the wait for an attempt that never settles did not end: it is not bounded")
	}
	if runErr != nil {
		t.Fatalf("the run should have refused, not errored: %v", runErr)
	}
	if result.State != runstate.StateFailed {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the refusal is %+v, want %s", result.Failure, RefusedUnaddressed)
	}
	if !strings.Contains(result.Failure.Message, "wall clock") {
		t.Errorf("the refusal does not say what bound it: %q", result.Failure.Message)
	}
	if !strings.Contains(result.Failure.Message, "ticfac settle") {
		t.Errorf("the refusal does not tell the operator who settles it: %q", result.Failure.Message)
	}
}

// alwaysRunningExecutor is the pid the host reused: inspect never settles.
type alwaysRunningExecutor struct{ Executor }

func (e *alwaysRunningExecutor) Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	status, err := e.Executor.Inspect(handle, cursor)
	if err != nil {
		return nil, err
	}
	status.State, status.Terminal = subprocess.StateRunning, false
	return status, nil
}

// testClock is the run's own clock, advanced by the wait rather than by time
// passing. The cadence under test is measured in minutes; a test that waited
// them out would not be a test anybody runs.
type testClock struct{ at time.Time }

func (c *testClock) now() time.Time          { return c.at }
func (c *testClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// An attempt nobody can address is released BY A PERSON, and the release is a
// record on origin the next run reads.
//
// Holding it is right (Appendix A #6: a live attempt is never redispatched),
// but "settled by whoever finds it next" had no next actor here — the
// reconciler cannot tell a reused pid from the job, so every restart refused
// the same tick forever and the only way on was a new run id.
func TestALostAttemptIsReleasedByAPersonAndTheNextRunDispatchesANewOne(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// The supervisor dies without settling anything: no report, no exit
	// record, no live process. That is `lost`.
	f.stopEverything()

	held, result, err := f.run(f.Repo, fixtureOptions{mode: "hang"})
	if err != nil {
		t.Fatalf("the run that found the lost attempt errored: %v", err)
	}
	if result.Failure == nil || result.Failure.Reason != RefusedUnaddressed {
		t.Fatalf("the lost attempt was not held: %+v", result.Failure)
	}
	if got := held.Stages("a1"); contains(got, StageDispatched) {
		t.Fatalf("a lost attempt was redispatched: %v", got)
	}

	// The person. It is refused without one, because a release with no author
	// is the clock release A11 exists to refuse.
	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settler.Settle(context.Background(), "a1", 1, ""); err == nil {
		t.Fatal("an unattributed release was accepted")
	}
	settled, err := settler.Settle(context.Background(), "a1", 1, "an operator")
	if err != nil {
		t.Fatalf("settle a1's lost attempt: %v", err)
	}
	if !settled.Recorded || settled.State != subprocess.StateLost {
		t.Fatalf("the settlement is %+v", settled)
	}

	// It is a DECISION on origin, readable as fields rather than as prose.
	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	decisions, err := store.Decisions()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range decisions {
		if decision.Request["op"] == settleOp && decision.Request["tick_id"] == "a1" {
			found = true
			if decision.Response["released_by"] != "an operator" {
				t.Errorf("the release does not name who made it: %+v", decision.Response)
			}
		}
	}
	if !found {
		t.Fatalf("no settlement decision on origin: %+v", decisions)
	}

	// Settling the same attempt twice writes nothing: a decision is created if
	// absent and never rewritten.
	again, err := settler.Settle(context.Background(), "a1", 1, "an operator")
	if err != nil || again.Recorded {
		t.Fatalf("a second settlement wrote a second record: %+v (%v)", again, err)
	}

	// The next run dispatches a NEW attempt rather than adopting the released
	// one, and finishes.
	f.Runner = fakeRunnerArgv(t, "report")
	next, resumed, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run after the release did not finish: %v", err)
	}
	if resumed.State != runstate.StateCompleted {
		t.Fatalf("the run after the release ended %s: %s", resumed.State, resumed.Reason)
	}
	if got := next.Stages("a1"); !contains(got, StageSettled) || !contains(got, StageDispatched) {
		t.Fatalf("a1's stages %v do not show the released attempt skipped and a new one dispatched", got)
	}
}

// A live attempt is not an operator's to release. A6 is not waivable: an
// attempt the executor can still address is cancelled, never released behind
// its back.
func TestSettleRefusesAnAttemptTheExecutorCanStillAddress(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "hang"})
	_, _, err := f.run(f.Repo, fixtureOptions{mode: "hang", stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	// Nothing is killed here: the attempt's supervisor and runner are alive.
	settler, err := New(f.options(f.Repo, fixtureOptions{mode: "hang"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = settler.Settle(context.Background(), "a1", 1, "an operator")
	if err == nil {
		t.Fatal("a live attempt was released")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("the refusal does not say what to do with a live attempt instead: %v", err)
	}
}

// A push that is not a lease race is reported as what it is.
//
// The merge's push loop rebuilt and retried eight times and then said "the
// branch moved under this reconciler", whatever the push had actually
// answered — so an unreachable remote, a credential that expired or a hook
// that declined the push all read as a conflict nobody had, and the next
// repair went looking for the wrong thing.
func TestAPushRefusedByPolicyIsNotReportedAsALeaseRace(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	// The hook goes in the moment the attempt is collected, so everything up
	// to the merge happens normally and the integration push is what meets it.
	f.wrap = func(inner Executor) Executor {
		return &afterCollect{Executor: inner, then: func() { declinePushes(t, f.Repo.Origin) }}
	}

	_, _, err := f.run(f.Repo, fixtureOptions{})
	if err == nil {
		t.Fatal("the run reported no error although its integration push was declined")
	}
	if strings.Contains(err.Error(), "moved under this reconciler") {
		t.Fatalf("a declined push is reported as a lease race: %v", err)
	}
	if !strings.Contains(err.Error(), "no epic branch writes here") {
		t.Errorf("the error does not carry what the remote actually said: %v", err)
	}
}

// afterCollect runs a function once, immediately after the collect.
type afterCollect struct {
	Executor
	then func()
	done bool
}

func (e *afterCollect) CollectDetail(handle *subprocess.JobHandle) (*subprocess.Collection, error) {
	collected, err := e.Executor.CollectDetail(handle)
	if !e.done {
		e.done = true
		e.then()
	}
	return collected, err
}

// declinePushes makes the origin refuse every write to the epic branch, the
// way a protected branch or a policy hook does. It is deliberately NOT a
// non-fast-forward: the point is an error that is not a lease race.
func declinePushes(t *testing.T, origin string) {
	t.Helper()
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"while read old new ref; do\n" +
		"  case \"$ref\" in refs/heads/epic/*) echo 'no epic branch writes here' >&2; exit 1;; esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A13: the evidence says which commit the gate ACTUALLY RAN ON.
//
// The fingerprint's source was the attempt's head and its "integration" field
// was a constant ref name, so the merge the gate ran on — the only commit the
// verdict is really about — was written to no record at all, and freshness
// could only ever notice the attempt branch moving.
func TestTheGatesEvidenceNamesTheCommitItRanOn(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %v: %v", result, err)
	}

	store := openRunStore(t, f.Repo.Dir, "epic/qeu", "r-fixture")
	keys := store.EvidenceKeys()
	if len(keys) == 0 {
		t.Fatal("the run recorded no evidence")
	}
	epicHead := strings.TrimSpace(mustRun(t, f.Repo.Dir, "git", "rev-parse", "refs/remotes/origin/epic/qeu"))
	mustRun(t, f.Repo.Dir, "git", "fetch", "--quiet", "origin", "refs/heads/epic/qeu")

	// What the gate did NOT run on: the attempt's own head. The merge is what
	// the integrated gate evaluated, and the two are different commits — which
	// is the whole of what this record could not previously say.
	attempts, err := store.Attempts()
	if err != nil {
		t.Fatal(err)
	}
	attemptHeads := map[string]bool{}
	for _, attempt := range attempts {
		marker := handleFromMap(attempt.JobHandle)
		branch := branchOf(marker.WriteRef)
		out := strings.TrimSpace(mustRun(t, f.Repo.Dir, "git", "ls-remote", "origin", refFor(branch)))
		if sha, _, ok := strings.Cut(out, "\t"); ok {
			attemptHeads[sha] = true
		}
	}
	for _, key := range keys {
		evidence, ok, err := store.Evidence(key)
		if err != nil || !ok {
			t.Fatalf("read evidence %s: %v", key, err)
		}
		gateSHA := evidence.Provenance.SourceSHA
		if len(gateSHA) < 40 {
			t.Fatalf("evidence %s does not name a commit as its source: %q", key, gateSHA)
		}
		// The commit the gate ran on is a commit of the integration branch:
		// the merge this run made, which origin's head still carries.
		if !mustRunAllowingFailure(f.Repo.Dir, "git", "merge-base", "--is-ancestor", gateSHA, epicHead) {
			t.Errorf("evidence %s names %s, which is not a commit of %s", key, short(gateSHA), "epic/qeu")
		}
		if attemptHeads[gateSHA] {
			t.Errorf("evidence %s names the attempt's own head %s, not the merge the gate ran on",
				key, short(gateSHA))
		}
		if evidence.Provenance.SourceRef != "refs/heads/epic/qeu" {
			t.Errorf("evidence %s ran on the integration branch and says it ran on %q",
				key, evidence.Provenance.SourceRef)
		}
	}
	_ = r
}

// The other half of A13: a fingerprint is held to every field it states, so an
// integration target that moved away from the gated commit refuses publication
// exactly as a moved attempt head does.
func TestFreshnessIsCheckedOnEveryFieldTheRecordStates(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	r, err := New(f.options(f.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	recorded := Fingerprint{
		"source_sha":              "aaaa",
		"attempt_head":            "bbbb",
		"integration_ref":         "refs/heads/epic/qeu",
		"context_manifest_digest": "sha256:cfg",
		"profile_digest":          "sha256:profile",
	}
	if outcome := r.RecordEvidence("gate", recorded); outcome != "recorded" {
		t.Fatalf("the fingerprint was refused: %s", outcome)
	}
	moved := Fingerprint{}
	for field, value := range recorded {
		moved[field] = value
	}
	moved["source_sha"] = "cccc"
	if outcome := r.PublishEvidence("gate", moved); outcome != "refused_stale" {
		t.Errorf("an integration target that moved published anyway: %s", outcome)
	}
	if got := recorded.Mismatch(moved); len(got) != 1 || !strings.Contains(got[0], "source_sha") {
		t.Errorf("the mismatch does not name what moved: %v", got)
	}
	moved["source_sha"] = "aaaa"
	moved["attempt_head"] = "dddd"
	if outcome := r.PublishEvidence("gate", moved); outcome != "refused_stale" {
		t.Errorf("an attempt head that moved published anyway: %s", outcome)
	}
	moved["attempt_head"] = "bbbb"
	if outcome := r.PublishEvidence("gate", moved); outcome != "published" {
		t.Errorf("an unmoved target refused publication: %s", outcome)
	}
}

// A run leaves no worktree registered behind it, and a registration a KILLED
// run left is pruned by the next one. Every worktree this package makes is
// removed by a defer, and a killed process runs no defer.
func TestARunLeavesNoWorktreeRegisteredBehindIt(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	if _, result, err := f.run(f.Repo, fixtureOptions{}); err != nil || result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %v: %v", result, err)
	}
	assertOneWorktree(t, f.Repo.Dir)

	// The killed run: cut while the tracker's worktree and the run's temp
	// worktrees exist, then the directories go the way a temp filesystem goes.
	g := newFixture(t, fixtureOptions{})
	_, _, err := g.run(g.Repo, fixtureOptions{stopAfter: stopAt("a1", StageDispatched)})
	killedAfter(t, err, "a1", StageDispatched)
	g.stopEverything()
	for _, dir := range registeredWorktrees(t, g.Repo.Dir) {
		if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
			t.Fatal(err)
		}
	}
	if len(registeredWorktrees(t, g.Repo.Dir)) == 0 {
		t.Fatal("the killed run left no registration to prune; the fixture proves nothing")
	}
	if _, _, err := g.run(g.Repo, fixtureOptions{}); err != nil {
		t.Fatalf("the run after the kill did not finish: %v", err)
	}
	assertOneWorktree(t, g.Repo.Dir)
}

// The prune itself, on its own: a registration whose directory is gone is
// removed, and one whose directory is still there is left alone — it may be
// another run's live worktree, and two epics can be reconciled in one checkout.
func TestPruningRemovesOnlyTheRegistrationsWhoseDirectoryIsGone(t *testing.T) {
	root := t.TempDir()
	repo := newRepo(t, root, "prune", passingGate)
	g := &repoGit{dir: repo.Dir, name: "ticfac", email: "ticfac@example.com", remote: "origin"}

	gone, removeGone, err := g.tempWorktree("ticfac-gate-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	kept, removeKept, err := g.tempWorktree("ticfac-merge-", repo.Base)
	if err != nil {
		t.Fatal(err)
	}
	defer removeKept()
	// The directory goes the way a temp filesystem goes, with nothing to run
	// the removal git would have been told about: a killed process runs no
	// defer.
	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatal(err)
	}
	if len(registeredWorktrees(t, repo.Dir)) != 2 {
		t.Fatalf("the fixture did not register two worktrees: %v", registeredWorktrees(t, repo.Dir))
	}

	if _, err := g.pruneWorktrees(); err != nil {
		t.Fatal(err)
	}
	left := registeredWorktrees(t, repo.Dir)
	if resolved, err := filepath.EvalSymlinks(kept); err == nil {
		kept = resolved
	}
	if len(left) != 1 || left[0] != kept {
		t.Fatalf("after the prune git holds %v; only %s should be left", left, kept)
	}
	_ = removeGone
}

func assertOneWorktree(t *testing.T, dir string) {
	t.Helper()
	if got := registeredWorktrees(t, dir); len(got) != 0 {
		t.Errorf("git worktree list carries %d worktree(s) besides the checkout itself: %v", len(got), got)
	}
}

// registeredWorktrees is every worktree git holds a registration for except the
// main checkout.
func registeredWorktrees(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(mustRun(t, dir, "git", "worktree", "list", "--porcelain"), "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if !ok {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		if path == dir {
			continue
		}
		out = append(out, path)
	}
	return out
}

func openRunStore(t *testing.T, repo, branch, runID string) *runstate.Store {
	t.Helper()
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: "origin", Branch: branch, RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fetch(); err != nil {
		t.Fatal(err)
	}
	return store
}
