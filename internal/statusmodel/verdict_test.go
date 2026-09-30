package statusmodel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// The VERDICT suite (epic hn6, wave 2 — tick 7uv): the health verdict's
// derivation, case by case, each built through Build — the model's own
// assembly, never the function in isolation — and each validated against
// the contract the acceptance names for every case. The base fixture is the
// running-epic shape build_test.go builds, pruned or extended per case so
// each test states exactly the lines its rule reads.

// assertAdmittedByTheContract holds one Build output to the pinned schema:
// the acceptance requires every case's model to still be a document the
// contract admits, or the derivation grew a shape no renderer can read.
func assertAdmittedByTheContract(t *testing.T, model Model) {
	t.Helper()
	record, defs, _ := bundleFixture(t)
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("the built model does not marshal: %v", err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if problems := schema.Validate(record, defs, document); len(problems) > 0 {
		t.Errorf("a model the builder produced is refused by the contract:\n%s\nmodel:\n%s",
			strings.Join(problems, "\n"), raw)
	}
}

// hostSuspendedAt is one host-suspension line in the reconciler's own format
// (suspend.go), stating how long the machine was actually down — the half
// the sleep recovery's seconds read.
func hostSuspendedAt(at time.Time, down string) runfeed.Event {
	return runfeed.NewEvent(at, "epic-2jn", "", nil, reconcile.StageHostSuspended,
		fmt.Sprintf("the host was suspended for about %s: a 5m0s wait took %s of wall clock, "+
			"and nothing of this run ran in that time — the run did not hang, the machine was not running it",
			down, down))
}

// TestVerdictHealthyWhenNothingIsWrong: an alive run whose feed carries no
// warning answers healthy, with the word itself as the summary and an empty
// recovered list (case a).
func TestVerdictHealthyWhenNothingIsWrong(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Feed = nil
	model := Build(src)
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("an alive run with nothing wrong is %q, want healthy", model.Health.Verdict.State)
	}
	if model.Health.Verdict.Summary != VerdictHealthy {
		t.Errorf("the healthy summary is %q, want the word itself", model.Health.Verdict.Summary)
	}
	if got := model.Health.Verdict.Recovered; got == nil || len(got) != 0 {
		t.Errorf("nothing was recovered, want the empty list: %+v", got)
	}
	assertAdmittedByTheContract(t, model)
}

// TestVerdictRecoveredCountsTheNetAndTheSleep: fourteen remote retries and
// two host suspensions recover as net ×14 and sleep ×2, the sleep carrying
// the wall clock its own lines state (case b) — and a suspension line that
// states no duration leaves the seconds null rather than guessing at one.
func TestVerdictRecoveredCountsTheNetAndTheSleep(t *testing.T) {
	t.Parallel()
	stated := runningEpicSources()
	feed := []runfeed.Event{}
	for i := 0; i < 14; i++ {
		feed = append(feed, runfeed.NewEvent(
			testNow.Add(-time.Duration(3*i+1)*time.Minute), "epic-2jn", "", nil,
			reconcile.StageRemoteRetried, "origin refused the fetch; waited and tried again"))
	}
	feed = append(feed,
		hostSuspendedAt(testNow.Add(-2*time.Hour), "41m0s"),
		hostSuspendedAt(testNow.Add(-1*time.Hour), "5m30s"))
	stated.Feed = feed
	model := Build(stated)
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("a run that recovered everything it hit is %q, want healthy", model.Health.Verdict.State)
	}
	seconds := int64(2790)
	want := []Recovery{
		{What: "net", Count: 14},
		{What: "sleep", Count: 2, Seconds: &seconds},
	}
	if !reflect.DeepEqual(model.Health.Verdict.Recovered, want) {
		t.Errorf("the recovered list is %+v, want %+v (41m0s + 5m30s = 2790s)", model.Health.Verdict.Recovered, want)
	}
	assertAdmittedByTheContract(t, model)

	// The else branch: suspension lines that state no duration still count,
	// and the seconds stay null — a number nobody stated is a number that
	// lies, the same rule the cost lines hold.
	unstated := runningEpicSources()
	unstated.Feed = []runfeed.Event{
		runfeed.NewEvent(testNow.Add(-2*time.Hour), "epic-2jn", "", nil,
			reconcile.StageHostSuspended, "the host was suspended: a wait took far more wall clock than it asked for"),
		runfeed.NewEvent(testNow.Add(-1*time.Hour), "epic-2jn", "", nil,
			reconcile.StageHostSuspended, "the host was suspended: a wait took far more wall clock than it asked for"),
	}
	unstatedModel := Build(unstated)
	want = []Recovery{{What: "sleep", Count: 2}}
	if !reflect.DeepEqual(unstatedModel.Health.Verdict.Recovered, want) {
		t.Errorf("the recovered list is %+v, want sleep ×2 with null seconds", unstatedModel.Health.Verdict.Recovered)
	}
}

// TestVerdictDegradedNamesTheUnreadableSource: a source that could not be
// read degrades a going run, and the summary names it (case c).
func TestVerdictDegradedNamesTheUnreadableSource(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Degraded = []string{"forge"}
	model := Build(src)
	if model.Health.Verdict.State != VerdictDegraded {
		t.Errorf("a run whose forge could not be read is %q, want degraded", model.Health.Verdict.State)
	}
	if summary := model.Health.Verdict.Summary; !strings.Contains(summary, "forge") {
		t.Errorf("the summary %q does not name the unreadable source", summary)
	}
	assertAdmittedByTheContract(t, model)
}

// TestVerdictDegradedOnAnUnansweredStuckNudge: a standing worker nudged as
// stuck whose attempt never answered degrades the run, and the summary names
// the worker and the age of its nudge (case d). A later line about the
// attempt — here the ordinary prompt a runner that ended its turn early
// gets — answers the nudge, and the run is healthy again.
func TestVerdictDegradedOnAnUnansweredStuckNudge(t *testing.T) {
	t.Parallel()
	nudged := func(at time.Time) runfeed.Event {
		return runfeed.NewEvent(at, "epic-2jn", "6dh", intPtr(3), reconcile.StageStuckNudged,
			"6dh try 2 showed no activity for 30m and was nudged in its own session")
	}

	src := runningEpicSources()
	// The fixture's own last word on this attempt is the dispatch 19s ago;
	// the nudge has to come after it — position in the feed is the clock —
	// so the case reads like a real feed: dispatched, then nudged.
	src.Feed = src.Feed[:len(src.Feed)-1]
	src.Feed = append(src.Feed, nudged(testNow.Add(-4*time.Minute)))
	model := Build(src)
	if model.Health.Verdict.State != VerdictDegraded {
		t.Errorf("a standing worker nudged as stuck reads %q, want degraded", model.Health.Verdict.State)
	}
	if summary := model.Health.Verdict.Summary; !strings.Contains(summary, "6dh") ||
		!strings.Contains(summary, "nudged as stuck 4m ago") {
		t.Errorf("the summary %q does not name the worker and the age of its nudge", summary)
	}
	assertAdmittedByTheContract(t, model)

	answered := runningEpicSources()
	answered.Feed = answered.Feed[:len(answered.Feed)-1]
	answered.Feed = append(answered.Feed,
		nudged(testNow.Add(-4*time.Minute)),
		runfeed.NewEvent(testNow.Add(-1*time.Minute), "epic-2jn", "6dh", intPtr(3), reconcile.StageWaiting,
			"the runner ended its turn without its report and was prompted again"))
	if answeredModel := Build(answered); answeredModel.Health.Verdict.State != VerdictHealthy {
		t.Errorf("a nudge the attempt answered still reads %q, want healthy", answeredModel.Health.Verdict.State)
	}

	// The watcher's own quiet statements do not answer a nudge: a wall clock
	// firing after it says the attempt is still unresolved, not that it
	// worked. The nudge stays a cause.
	stillStuck := runningEpicSources()
	stillStuck.Feed = stillStuck.Feed[:len(stillStuck.Feed)-1]
	stillStuck.Feed = append(stillStuck.Feed,
		nudged(testNow.Add(-4*time.Minute)),
		runfeed.NewEvent(testNow.Add(-2*time.Minute), "epic-2jn", "6dh", intPtr(3), reconcile.StageWallClock,
			"the wall clock of 3600s fired and attempt 3 of 6dh has not settled"))
	if stillModel := Build(stillStuck); stillModel.Health.Verdict.State != VerdictDegraded {
		t.Errorf("a nudge followed only by the watcher's own word reads %q, want degraded", stillModel.Health.Verdict.State)
	}
}

// TestVerdictStoppedWhenTheRunIsGone: a run whose process is gone while its
// lifecycle is still mid-flight is stopped, with the liveness answer's own
// reason as the summary (case e) — and when the probe said nothing, the
// dead-run wait's what.
func TestVerdictStoppedWhenTheRunIsGone(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Liveness = LivenessInput{
		Alive: false, State: "not_running",
		Reason: "no process holds this run: the last one released it, or none has claimed it here",
		Source: "run.pid",
	}
	model := Build(src)
	if model.Lifecycle.Phase != PhaseWaves {
		t.Fatalf("the fixture's run reads phase %q, want waves: the case needs a mid-flight death", model.Lifecycle.Phase)
	}
	if model.Health.Verdict.State != VerdictStopped {
		t.Errorf("a run gone mid-flight is %q, want stopped", model.Health.Verdict.State)
	}
	if summary := model.Health.Verdict.Summary; !strings.Contains(summary, "no process holds this run") {
		t.Errorf("the stopped summary %q is not the liveness answer's own reason", summary)
	}
	assertAdmittedByTheContract(t, model)

	// A probe that said nothing: the dead-run wait names the stop instead.
	quiet := runningEpicSources()
	quiet.Liveness = LivenessInput{Alive: false, State: "not_running", Reason: "", Source: "run.pid"}
	quietModel := Build(quiet)
	if quietModel.Health.Verdict.State != VerdictStopped {
		t.Fatalf("a run gone mid-flight with a silent probe is %q, want stopped", quietModel.Health.Verdict.State)
	}
	if summary := quietModel.Health.Verdict.Summary; !strings.Contains(summary, "run epic-2jn is not_running") {
		t.Errorf("the stopped summary %q is not the dead-run wait's what", summary)
	}
}

// TestVerdictDoneIsNotStopped: a run that finished its own work — done,
// nothing left — is not stopped even though its process is gone (case f):
// done is the run's own end, not a death.
func TestVerdictDoneIsNotStopped(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Checkpoint.State = "completed"
	src.Liveness = LivenessInput{
		Alive: false, State: "not_running",
		Reason: "the run finished; its process is gone", Source: "run.pid",
	}
	model := Build(src)
	if model.Lifecycle.Phase != PhaseDone {
		t.Fatalf("the fixture's completed run reads phase %q, want done", model.Lifecycle.Phase)
	}
	if model.Health.Verdict.State == VerdictStopped {
		t.Error("a completed run reads stopped: done is the run's own end, not a death")
	}
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("a completed run with nothing wrong reads %q, want healthy", model.Health.Verdict.State)
	}
	assertAdmittedByTheContract(t, model)
}

// TestVerdictDegradedWhenTheRemoteExhaustedItsRetries: a remote that
// exhausted its retries is a live cause, and the summary names it.
func TestVerdictDegradedWhenTheRemoteExhaustedItsRetries(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-2*time.Minute), "epic-2jn", "", nil,
		reconcile.StageRemoteExhausted,
		"the remote fetch failed on all 4 attempts, every one a transient remote failure; "+
			"the run stops rather than waiting past its bound"))
	model := Build(src)
	if model.Health.Verdict.State != VerdictDegraded {
		t.Errorf("a run whose remote exhausted its retries reads %q, want degraded", model.Health.Verdict.State)
	}
	if summary := model.Health.Verdict.Summary; !strings.Contains(summary, "exhausted") {
		t.Errorf("the summary %q does not name the exhausted remote", summary)
	}
	assertAdmittedByTheContract(t, model)
}

// TestVerdictDegradedOnAStallWarningInsideItsWindow: a stall warning
// degrades only inside its fifteen-minute window — a warning older than
// that is history, counted in Health's own counters, never a live cause.
func TestVerdictDegradedOnAStallWarningInsideItsWindow(t *testing.T) {
	t.Parallel()
	inside := runningEpicSources()
	inside.Feed = append(inside.Feed, runfeed.NewEvent(testNow.Add(-5*time.Minute), "epic-2jn", "6dh", intPtr(3),
		reconcile.StageStallWarned, "6dh try 2 is alive but has produced nothing durable for 15m"))
	model := Build(inside)
	if model.Health.Verdict.State != VerdictDegraded {
		t.Errorf("a run stall-warned 5m ago reads %q, want degraded", model.Health.Verdict.State)
	}
	if summary := model.Health.Verdict.Summary; !strings.Contains(summary, "6dh") ||
		!strings.Contains(summary, "warned as stalled 5m ago") {
		t.Errorf("the summary %q does not name the tick and the age of the warning", summary)
	}
	assertAdmittedByTheContract(t, model)

	outside := runningEpicSources()
	outside.Feed = append(outside.Feed, runfeed.NewEvent(testNow.Add(-16*time.Minute), "epic-2jn", "6dh", intPtr(3),
		reconcile.StageStallWarned, "6dh try 2 is alive but has produced nothing durable for 15m"))
	if outsideModel := Build(outside); outsideModel.Health.Verdict.State != VerdictHealthy {
		t.Errorf("a stall warning 16m old reads %q, want healthy: outside its window it is history",
			outsideModel.Health.Verdict.State)
	}
}

// TestVerdictNamesTheFirstCauseInOrder: the degraded summary names the FIRST
// cause in the tick's fixed order — an unreadable source before an
// unanswered nudge, an unanswered nudge before an exhausted remote — so the
// headline sends the person to the thing that most needs them.
func TestVerdictNamesTheFirstCauseInOrder(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Degraded = []string{"forge"}
	src.Feed = src.Feed[:len(src.Feed)-1]
	src.Feed = append(src.Feed,
		runfeed.NewEvent(testNow.Add(-4*time.Minute), "epic-2jn", "6dh", intPtr(3), reconcile.StageStuckNudged,
			"6dh try 2 showed no activity for 30m and was nudged in its own session"),
		runfeed.NewEvent(testNow.Add(-3*time.Minute), "epic-2jn", "", nil, reconcile.StageRemoteExhausted,
			"the remote fetch failed on all 4 attempts, every one a transient remote failure"))
	model := Build(src)
	if model.Health.Verdict.State != VerdictDegraded {
		t.Fatalf("a run with three live causes reads %q, want degraded", model.Health.Verdict.State)
	}
	if summary := model.Health.Verdict.Summary; !strings.Contains(summary, "forge") ||
		strings.Contains(summary, "nudged") || strings.Contains(summary, "exhausted") {
		t.Errorf("the summary %q names a later cause, want the unreadable source first", summary)
	}
	assertAdmittedByTheContract(t, model)
}
