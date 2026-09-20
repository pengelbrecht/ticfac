package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/schema"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The run event feed (tick u9l, epic av8; the contract is
// contracts/run-event-feed.json): the append-only JSONL stream a
// NON-PARTICIPANT subscribes to instead of guessing an interval or polling
// the durable records. Three things are proved here:
//
//   - a real run writes one, in order, with run/tick/attempt identity on
//     every line, and the bytes validate against the vendored contract;
//   - a lost signal changes no verdict — a run whose feed cannot be written
//     at all settles exactly as a watched one does, because the feed is a
//     hint and the evidence decides;
//   - the poll interval comes from the EXECUTOR, not from one global
//     constant — a local substrate's seconds are what the wait sleeps, and a
//     cadence that is not a keepalive is refused at construction wherever it
//     is declared.

func feedLineSchema(t *testing.T) *schema.Schema {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, contracts.DirName, "run-event-feed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Records map[string]struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	record, ok := contract.Records["feed_event"]
	if !ok {
		t.Fatal("run-event-feed.json declares no feed_event schema")
	}
	parsed, err := schema.ParseSchema(record.Schema)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestTheRunWritesAFeedANonParticipantCanFollow(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	r, result, err := f.run(f.Repo, fixtureOptions{})
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s: %s", result.State, result.Reason)
	}

	path := runfeed.Path(f.Repo.Dir, "r-fixture")
	events, err := runfeed.Read(path)
	if err != nil {
		t.Fatalf("the run left no feed a non-participant can read: %v", err)
	}
	if r.FeedError() != nil {
		t.Fatalf("a healthy run's feed reported: %v", r.FeedError())
	}

	// Identity on every line: the run's id, and an attempt wherever the event
	// belongs to one. The bytes validate against the contract's schema, which
	// is what makes the fixture the second reader of the writer.
	lineSchema := feedLineSchema(t)
	stages := map[string]int{}
	for _, event := range events {
		if event.RunID != "r-fixture" {
			t.Errorf("a line carries run_id %q", event.RunID)
		}
		if event.TickID != nil {
			stages[*event.TickID]++
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		if errs := schema.Validate(lineSchema, nil, document); len(errs) > 0 {
			t.Errorf("a feed line does not validate against the contract: %v", errs)
		}
	}

	// The dispatch identity: a settled tick's dispatched line names its tick
	// and its first attempt.
	var dispatched []runfeed.Event
	for _, event := range events {
		if event.Stage == StageDispatched && event.TickID != nil && *event.TickID == "a1" {
			dispatched = append(dispatched, event)
		}
	}
	if len(dispatched) != 1 {
		t.Fatalf("tick a1 was dispatched %d times in the feed", len(dispatched))
	}
	if dispatched[0].Attempt == nil || *dispatched[0].Attempt != 1 {
		t.Errorf("the dispatched line for a1 carries attempt %v, want 1", dispatched[0].Attempt)
	}

	// The line that ends the run: run-level identity, null tick, null
	// attempt, and a stage a subscriber can wait for.
	finished := false
	for _, event := range events {
		if event.Stage == StageRunFinished {
			finished = true
			if event.TickID != nil || event.Attempt != nil {
				t.Errorf("the run-level line carries %v/%v, want null/null", event.TickID, event.Attempt)
			}
		}
	}
	if !finished {
		t.Error("the feed carries no run_finished line: a non-participant cannot learn the run ended")
	}
	// And exactly ONE, as the LAST line. The budget report used to be written
	// as run_finished too, so every budgeted run told its subscribers it had
	// ended within seconds of starting — and this test passed anyway, because
	// it only asked whether SOME run_finished existed. A terminal event that
	// is not terminal defeats the whole feed.
	var finishedAt []int
	for i, event := range events {
		if event.Stage == StageRunFinished {
			finishedAt = append(finishedAt, i)
		}
	}
	if len(finishedAt) != 1 {
		t.Errorf("the feed carries %d run_finished lines, want exactly 1: a terminal event said twice is not terminal", len(finishedAt))
	} else if finishedAt[0] != len(events)-1 {
		t.Errorf("run_finished is at index %d of %d: the terminal event must be the LAST line",
			finishedAt[0], len(events))
	}

	// The journal and the feed agree on the order a tick passed through,
	// which is what makes the feed a projection of the journal rather than a
	// second opinion about it.
	var fromFeed []string
	for _, event := range events {
		if event.TickID != nil && *event.TickID == "a1" {
			fromFeed = append(fromFeed, event.Stage)
		}
	}
	assertOrder(t, fromFeed,
		StageClaimed, StageDispatched, StageCollected, StageIntegrated, StageGatePassed, StageClosed, StageCleanedUp)
}

// A lost or late signal changes no verdict: the feed is a hint about when to
// look, and the verdict stays with the durable evidence. So a run whose feed
// cannot be written AT ALL — every append refused — settles exactly as a
// watched run does: same end state, same stages, same closes. The signal is
// absent, the evidence is not, and nothing in the verdict path reads a line.
func TestALostFeedSignalChangesNoVerdict(t *testing.T) {
	t.Parallel()

	watched := func(t *testing.T) (*Reconciler, *Result) {
		f := newFixture(t, fixtureOptions{})
		r, result, err := f.run(f.Repo, fixtureOptions{})
		if err != nil {
			t.Fatalf("the run did not finish: %v", err)
		}
		return r, result
	}
	// The blind variant: the feed's path is a directory, so every append
	// fails from the first event to the last — the signal is lost completely,
	// not merely late.
	fBlind := newFixture(t, fixtureOptions{})
	if err := os.MkdirAll(runfeed.Path(fBlind.Repo.Dir, "r-fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	rBlind, err := New(fBlind.options(fBlind.Repo, fixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	resultBlind, err := rBlind.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the unwritable-feed run did not finish: %v", err)
	}
	if rBlind.FeedError() == nil {
		t.Fatal("a feed that could never be written reported no error: a lost signal nobody can see is a lost watcher")
	}
	// And the run's own RESULT carries it too (tick d6s): a feed failure is
	// not a verdict about the work — the run above proves the verdict is
	// unchanged — but silence is the one thing it must not be, because the
	// operator who runs `ticfac events --follow` against this run waits
	// forever on a file that will never appear. The result is the surface
	// the operator reads; the error has to be on it.
	if resultBlind.FeedError == nil {
		t.Fatal("the run's result hid the feed failure: a run nobody can watch said nothing about it")
	}

	rWatched, resultWatched := watched(t)
	if resultBlind.State != resultWatched.State {
		t.Errorf("the verdict changed: a watched run ended %s, a blind one %s",
			resultWatched.State, resultBlind.State)
	}
	if resultBlind.State != runstate.StateCompleted {
		t.Errorf("the blind run ended %s: %s", resultBlind.State, resultBlind.Reason)
	}
	for _, tick := range []string{"a1", "a2", "b1", "rv", "co"} {
		want, got := rWatched.Stages(tick), rBlind.Stages(tick)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("tick %s settled differently without its feed:\n  watched %v\n  blind   %v", tick, want, got)
		}
		current, err := fBlind.Tracker.Show(context.Background(), tick)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != "closed" {
			t.Errorf("tick %s is %s, not closed — the feed was load-bearing after all", tick, current.Status)
		}
	}
}

// The interval belongs to the executor, not to one global constant: the wait
// addresses a live job at the cadence the job's EXECUTOR declares — seconds
// for a local substrate — and falls back to the run's interval only where the
// executor states none. What is refused everywhere is a cadence that is not a
// keepalive under the substrate's wipe threshold, because the poll IS the
// keepalive wherever a substrate wipes.
// KNOWN LOAD-SENSITIVE (tick cy2, sixth occurrence, 2026-09-19). This test
// refused tick 9fc of epic ncv — a tick whose change was TypeScript and one
// closeout test, nothing to do with poll cadence — with:
//
//	feed_test.go: the wait never slept; the executor's cadence never applied
//
// What was established before filing it, so nobody redoes the work: it fails
// only inside the full package at -parallel 12, where ~80 tests each fork git
// and worker processes; internal/reconcile took 845s in the failing gate
// against 410s for a clean run of the SAME tree. It passes 3/3 alone and 6/6
// under synthetic CPU load at load average 13-29, so the trigger is process
// and fd contention rather than a starved CPU.
//
// The failure is stronger than a missed deadline and that is the clue worth
// keeping: holdingInspector forces two holds, so Inspect should report
// `running` twice and the wait should sleep twice DETERMINISTICALLY. An empty
// `slept` means the wait never polled a held job at all. Suspects, in order:
// the attempt settling before the first poll despite the hold, the wrap not
// reaching the attempt that ran, or the run returning before dispatch under
// contention.
//
// It is not a regression from the liveness probe (tick dh1): the same tree
// passes this test repeatedly in isolation.
func TestTheWaitUsesTheExecutorsCadenceNotTheRuns(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	hold := func(t *testing.T, count int, opts Options) []time.Duration {
		t.Helper()
		f := newFixture(t, fixtureOptions{})
		f.wrap = func(inner Executor) Executor {
			return &holdingInspector{t: t, inner: inner, holds: count}
		}
		chosen := f.options(f.Repo, fixtureOptions{})
		chosen.Executors = opts.Executors
		chosen.PollInterval = opts.PollInterval
		chosen.StepCap = 5 * time.Second // the wait SLEEPS at its cadence; no re-deriving legs
		var mu sync.Mutex
		var slept []time.Duration
		chosen.Sleep = func(d time.Duration) {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
		}
		r, err := New(chosen)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.RunProtected(context.Background()); err != nil {
			t.Fatalf("the run did not finish: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		return slept
	}

	executorInterval := 30 * time.Millisecond
	runInterval := 77 * time.Millisecond
	local := KnownExecutor{
		Name:         subprocess.ExecutorName,
		Runners:      subprocess.KnownRunners(),
		AcceptsModel: subprocess.RunnerAcceptsModel,
		PollInterval: executorInterval,
	}

	slept := hold(t, 2, Options{Executors: []KnownExecutor{local}, PollInterval: runInterval})
	if len(slept) == 0 {
		t.Fatal("the wait never slept; the executor's cadence never applied")
	}
	for _, d := range slept {
		if d != executorInterval {
			t.Errorf("the wait slept %s, want the executor's %s — the interval belongs to the executor", d, executorInterval)
		}
	}

	// And the fallback: an executor that states no cadence of its own is
	// addressed at the run's interval — the cloud keepalive's home.
	slept = hold(t, 2, Options{Executors: []KnownExecutor{{Name: local.Name, Runners: local.Runners, AcceptsModel: local.AcceptsModel}}, PollInterval: runInterval})
	for _, d := range slept {
		if d != runInterval {
			t.Errorf("the wait slept %s, want the run's %s where the executor states no cadence", d, runInterval)
		}
	}
}

func TestAnExecutorCadenceThatIsNotAKeepaliveIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, fixtureOptions{})
	opts := f.options(f.Repo, fixtureOptions{})
	opts.WipeThreshold = 20 * time.Minute
	opts.Executors = []KnownExecutor{{
		Name:         subprocess.ExecutorName,
		Runners:      subprocess.KnownRunners(),
		AcceptsModel: subprocess.RunnerAcceptsModel,
		PollInterval: 11 * time.Minute,
	}}
	_, err := New(opts)
	if err == nil {
		t.Fatal("an executor cadence at over half the wipe threshold was accepted")
	}
	if !strings.Contains(err.Error(), "polling IS the keepalive") {
		t.Errorf("the refusal does not carry the keepalive reasoning: %v", err)
	}
	if !strings.Contains(err.Error(), subprocess.ExecutorName) {
		t.Errorf("the refusal does not name the executor: %v", err)
	}
}

// holdingInspector holds a live job non-terminal for a bounded number of
// inspects, so a wait has legs to sleep through without a slow fixture.
type holdingInspector struct {
	t     *testing.T
	inner Executor
	holds int
}

func (h *holdingInspector) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	return h.inner.Start(spec)
}

func (h *holdingInspector) Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	status, err := h.inner.Inspect(handle, cursor)
	if err != nil {
		return status, err
	}
	// The hold converts whatever the real Inspect said — including an already
	// settled job — into a live one, because the point is legs for the wait
	// to sleep through, not a genuinely slow fixture: a fake runner settles
	// faster than the first poll.
	if h.holds > 0 {
		h.holds--
		held := *status
		held.Terminal = false
		held.State = subprocess.StateRunning
		held.Cursor = nil
		return &held, nil
	}
	return status, nil
}

func (h *holdingInspector) CollectDetail(handle *subprocess.JobHandle) (*subprocess.Collection, error) {
	return h.inner.CollectDetail(handle)
}

func (h *holdingInspector) Cancel(handle *subprocess.JobHandle) (*subprocess.CancelAck, error) {
	return h.inner.Cancel(handle)
}

func (h *holdingInspector) Dispose(handle *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	return h.inner.Dispose(handle, opts)
}

var _ Executor = (*holdingInspector)(nil)

// The attempt identity on the claim and the start failure (tick d6s). The feed
// contract puts run/tick/attempt identity on every line, and the dispatch used
// to set its attempt number only AFTER Start succeeded — so the claimed line
// and the line a failed start leaves both carried the PREVIOUS attempt (or
// null on a fresh run), exactly on the lines a reader needs when an attempt
// failed. The number belongs to the dispatch the moment its marker is on
// origin: every line from there — the claim, the start failure — is about it.
func TestClaimAndStartFailureLinesCarryTheAttemptTheyAreAbout(t *testing.T) {
	shorttest.EndToEnd(t)
	t.Parallel()

	// claimAndFailure returns the attempt numbers on a1's LAST claimed line
	// and its start-failure line, from the feed as a non-participant reads it.
	// A tick-scoped line that carries a NULL attempt is reported as nil —
	// that is the defect, and it must not read as "the line is missing".
	claimAndFailure := func(t *testing.T, f *fixture) (claimed, failed *int) {
		t.Helper()
		events, err := runfeed.Read(runfeed.Path(f.Repo.Dir, "r-fixture"))
		if err != nil {
			t.Fatal(err)
		}
		var sawClaim, sawFailure bool
		for _, event := range events {
			if event.TickID == nil || *event.TickID != "a1" {
				continue
			}
			switch event.Stage {
			case StageClaimed:
				sawClaim = true
				claimed = event.Attempt
			case StageStartFailed:
				sawFailure = true
				failed = event.Attempt
			}
		}
		if !sawClaim {
			t.Fatal("the feed carries no claimed line for a1")
		}
		if !sawFailure {
			t.Fatal("the feed carries no line for the start failure: a start that fails must not be silent — " +
				"the feed exists precisely so a reader learns when to look, and an attempt that never started is the moment")
		}
		return claimed, failed
	}
	wantAttempt := func(t *testing.T, name string, got *int, want int) {
		t.Helper()
		if got == nil {
			t.Errorf("%s carries attempt null, want %d", name, want)
			return
		}
		if *got != want {
			t.Errorf("%s carries attempt %d, want %d — the line is about attempt %d and must name it", name, *got, want, want)
		}
	}

	t.Run("a first attempt's claim and start failure say attempt 1", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, fixtureOptions{})
		f.wrap = func(inner Executor) Executor { return &failingStartExecutor{Executor: inner} }
		if _, _, err := f.run(f.Repo, fixtureOptions{}); err == nil {
			t.Fatal("a run whose every start fails reported success")
		}
		claimed, failed := claimAndFailure(t, f)
		wantAttempt(t, "the claimed line", claimed, 1)
		wantAttempt(t, "the start-failure line", failed, 1)
	})

	// The defect pass's shape: attempt 1 was rejected, attempt 2's claim and
	// start failure used to carry attempt 1 — the number from the checkpoint
	// the restart read, not the number of the dispatch those lines are about.
	t.Run("a second attempt's claim and start failure say attempt 2", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, fixtureOptions{mode: "blocked-first"})
		killed := fixtureOptions{mode: "blocked-first", stopAfter: stopAt("a1", StageRejected)}
		if _, _, err := f.run(f.Repo, killed); err == nil {
			t.Fatal("the fixture's cut did not happen: attempt 1 was not spent")
		}
		f.wrap = func(inner Executor) Executor { return &failingStartExecutor{Executor: inner} }
		if _, _, err := f.run(f.Repo, fixtureOptions{mode: "blocked-first"}); err == nil {
			t.Fatal("a run whose every start fails reported success")
		}
		claimed, failed := claimAndFailure(t, f)
		wantAttempt(t, "the claimed line", claimed, 2)
		wantAttempt(t, "the start-failure line", failed, 2)
	})
}

// failingStartExecutor refuses every Start, standing in for a host that cannot
// start the job at all — the one moment a reader of the feed needs the lines
// the dispatch leaves before anything runs.
type failingStartExecutor struct{ Executor }

func (e *failingStartExecutor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	return nil, errors.New("start " + spec.JobID + ": the fixture refuses every start")
}

var _ Executor = (*failingStartExecutor)(nil)
