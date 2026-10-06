package statusmodel

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// The verdict suite (epic hn6, wave 2 — tick 7uv): the headline a dashboard
// answers "is it healthy" with — stopped when the run is not going, degraded
// and why when something is wrong with a live one, healthy otherwise, and
// what the run got past by itself listed as calm in every state. Each case
// builds the whole Model, because the verdict reads the Model built so far,
// and holds the answer to the pinned contract through the contract suite's
// own bundleFixture helper: the assembled answer and the shape are one claim.

// assertValidatesAgainstTheContract holds one case's Build output to the
// contract, through the helper contract_test.go reads the bundle with — the
// same binding TestTheContractBindsTheBuilder states once for the fixture,
// stated here per case.
func assertValidatesAgainstTheContract(t *testing.T, model Model) {
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

// TestVerdictHealthyWhenNothingHappened: a live run whose feed states no
// typed trouble answers healthy, the word itself the summary, and an empty
// recovered list — "nothing needed getting past" is a claim the empty list
// makes, not one a renderer guesses at from silence.
func TestVerdictHealthyWhenNothingHappened(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Feed = nil
	model := Build(src)

	want := HealthVerdict{State: VerdictHealthy, Summary: VerdictHealthy, Recovered: []Recovery{}}
	if !reflect.DeepEqual(model.Health.Verdict, want) {
		t.Errorf("the verdict is %+v, want %+v", model.Health.Verdict, want)
	}
	assertValidatesAgainstTheContract(t, model)
}

// TestVerdictHealthyCountsWhatTheRunGotPast: fourteen network retries waited
// through and two host suspensions slept past are recovered, listed as calm
// beside the healthy word — net with its count, sleep with its count and the
// summed span the lines' own details state. When a suspension's detail states
// no span, the seconds stay null: a duration nobody stated is not guessed.
func TestVerdictHealthyCountsWhatTheRunGotPast(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	feed := []runfeed.Event{}
	for i := 0; i < 14; i++ {
		feed = append(feed, runfeed.NewEvent(testNow.Add(-time.Duration(70-i)*time.Minute),
			"epic-2jn", "", nil, reconcile.StageRemoteRetried,
			"origin failed transiently (attempt 1 of 4); waiting 30s and trying again: connection reset"))
	}
	suspension := func(at time.Duration, about, waited, took string) runfeed.Event {
		return runfeed.NewEvent(testNow.Add(at), "epic-2jn", "", nil, reconcile.StageHostSuspended,
			"the host was suspended for about "+about+": a "+waited+" wait took "+took+
				" of wall clock, and nothing of this run ran in that time — the run did not hang, "+
				"the machine was not running it (a sleeping laptop, a suspended VM). Waits resume "+
				"against the wall clock; keep the host awake while a run lives")
	}
	feed = append(feed,
		suspension(-time.Hour, "1m30s", "30m0s", "31m30s"),
		suspension(-40*time.Minute, "39m0s", "40m0s", "79m0s"))
	src.Feed = feed
	model := Build(src)

	want := HealthVerdict{
		State:   VerdictHealthy,
		Summary: VerdictHealthy,
		Recovered: []Recovery{
			{What: "net", Count: 14},
			{What: "sleep", Count: 2, Seconds: int64Ptr(90 + 39*60)},
		},
	}
	if !reflect.DeepEqual(model.Health.Verdict, want) {
		t.Errorf("the verdict is %+v, want %+v", model.Health.Verdict, want)
	}
	assertValidatesAgainstTheContract(t, model)

	// A suspension whose detail states no span is still counted, and the
	// span sums the stated ones only: the two above still hold their 2430s
	// beside a third line that says nothing about how long it slept.
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-20*time.Minute), "epic-2jn", "", nil,
		reconcile.StageHostSuspended, "the host slept through a wait; no span was stated"))
	model = Build(src)
	sleep := recoveredWhat(model.Health.Verdict, "sleep")
	if sleep == nil || sleep.Count != 3 || sleep.Seconds == nil || *sleep.Seconds != 2430 {
		t.Errorf("the sleep recovery is %+v, want 3 counted with the two stated spans summed to 2430s", sleep)
	}

	// When no suspension's detail states a span at all, the seconds stay
	// null: a duration nobody stated is not guessed.
	none := runningEpicSources()
	none.Feed = []runfeed.Event{runfeed.NewEvent(testNow.Add(-20*time.Minute), "epic-2jn", "", nil,
		reconcile.StageHostSuspended, "the host slept through a wait; no span was stated")}
	model = Build(none)
	sleep = recoveredWhat(model.Health.Verdict, "sleep")
	if sleep == nil || sleep.Count != 1 || sleep.Seconds != nil {
		t.Errorf("the sleep recovery is %+v, want 1 counted with null seconds", sleep)
	}
}

// TestVerdictDegradedNamesTheUnreadableSource: a source that could not be
// read degrades a live run and the summary names it — the model's own words
// for what is missing, never a counter to interpret. What the run recovered
// still rides beside the word: calm is shown whatever else is true.
func TestVerdictDegradedNamesTheUnreadableSource(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Degraded = []string{"forge"}
	model := Build(src)

	want := HealthVerdict{
		State:   VerdictDegraded,
		Summary: "degraded: forge unreadable",
		Recovered: []Recovery{
			{What: "net", Count: 1},
			{What: "interventions", Count: 1},
			{What: "wall clocks", Count: 1},
		},
	}
	if !reflect.DeepEqual(model.Health.Verdict, want) {
		t.Errorf("the verdict is %+v, want %+v", model.Health.Verdict, want)
	}
	assertValidatesAgainstTheContract(t, model)
}

// TestVerdictDegradedOnAnUnresolvedStuckNudge: a live worker nudged as
// stuck, with no later line recording that attempt moving, degrades the run
// and the summary names the worker and how long ago the nudge went out. A
// later activity line for the same attempt resolves it — the gate, the
// change the run saw it produce, its settle; the run's own stop that
// CONFIRMS the silence does not, and a nudge of an attempt that no longer
// stands was resolved by whatever ended it.
func TestVerdictDegradedOnAnUnresolvedStuckNudge(t *testing.T) {
	t.Parallel()
	three := 3
	nudged := func(feed []runfeed.Event) Sources {
		src := runningEpicSources()
		src.Feed = feed
		return src
	}
	const stuck = "6dh has shown no activity for the stuck window; nudged in its own session"

	model := Build(nudged([]runfeed.Event{
		runfeed.NewEvent(testNow.Add(-30*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageDispatched, "6dh try 2 dispatched (run dispatch #3)"),
		runfeed.NewEvent(testNow.Add(-4*time.Minute), "epic-2jn", "6dh", &three, reconcile.StageStuckNudged, stuck),
	}))
	if model.Health.Verdict.State != VerdictDegraded {
		t.Fatalf("an unresolved nudge reads %q, want degraded", model.Health.Verdict.State)
	}
	if want := "degraded: 6dh nudged as stuck 4m ago"; model.Health.Verdict.Summary != want {
		t.Errorf("the degraded summary is %q, want %q", model.Health.Verdict.Summary, want)
	}
	assertValidatesAgainstTheContract(t, model)

	// The run's own stop a window later CONFIRMS the silence ("still no
	// activity"); the nudge it stopped on stays the story the headline tells.
	model = Build(nudged([]runfeed.Event{
		runfeed.NewEvent(testNow.Add(-30*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageDispatched, "6dh try 2 dispatched (run dispatch #3)"),
		runfeed.NewEvent(testNow.Add(-4*time.Minute), "epic-2jn", "6dh", &three, reconcile.StageStuckNudged, stuck),
		runfeed.NewEvent(testNow.Add(-3*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageStuckStopped, "6dh showed no activity a window after the nudge; stopped and snapshotted"),
	}))
	if model.Health.Verdict.State != VerdictDegraded {
		t.Errorf("a nudge confirmed by the stop reads %q, want degraded: the stop is the same silence, not an answer", model.Health.Verdict.State)
	}

	// The change the run saw the worker produce answers it.
	model = Build(nudged([]runfeed.Event{
		runfeed.NewEvent(testNow.Add(-30*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageDispatched, "6dh try 2 dispatched (run dispatch #3)"),
		runfeed.NewEvent(testNow.Add(-4*time.Minute), "epic-2jn", "6dh", &three, reconcile.StageStuckNudged, stuck),
		runfeed.NewEvent(testNow.Add(-3*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageWipNudged, "6dh carries a large uncommitted change; asked to commit it"),
	}))
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("a nudge answered by the worker producing a change reads %q, want healthy", model.Health.Verdict.State)
	}

	// A later line recording the attempt moving resolves it.
	model = Build(nudged([]runfeed.Event{
		runfeed.NewEvent(testNow.Add(-30*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageDispatched, "6dh try 2 dispatched (run dispatch #3)"),
		runfeed.NewEvent(testNow.Add(-4*time.Minute), "epic-2jn", "6dh", &three, reconcile.StageStuckNudged, stuck),
		runfeed.NewEvent(testNow.Add(-2*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageGateStarted, "the integrated gate started for attempt 3 of 6dh (go)"),
	}))
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("a nudge resolved by the worker reporting reads %q, want healthy", model.Health.Verdict.State)
	}

	// A nudge of an attempt that no longer stands was resolved by whatever
	// ended it; only a live worker's own current attempt reads.
	two := 2
	model = Build(nudged([]runfeed.Event{
		runfeed.NewEvent(testNow.Add(-30*time.Minute), "epic-2jn", "6dh", &two,
			reconcile.StageStuckNudged, stuck),
	}))
	if model.Health.Verdict.State != VerdictHealthy {
		t.Errorf("a nudge of an attempt that no longer stands reads %q, want healthy", model.Health.Verdict.State)
	}
}

// TestVerdictDegradedOnAFreshStallWarning: a stall warning fresh enough to
// still be news — inside the reconciler's own fifteen-minute stall threshold
// — degrades the run and names the tick; the fixture's ninety-minute-old
// warning does not, which is what keeps a stalled-and-recovered run from
// reading degraded forever.
func TestVerdictDegradedOnAFreshStallWarning(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	three := 3
	src.Feed = []runfeed.Event{
		runfeed.NewEvent(testNow.Add(-10*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageStallWarned, "6dh is alive but has produced nothing durable for 20m: a reason to look, not a verdict"),
	}
	model := Build(src)
	if model.Health.Verdict.State != VerdictDegraded {
		t.Fatalf("a fresh stall warning reads %q, want degraded", model.Health.Verdict.State)
	}
	if want := "degraded: 6dh warned as stalled 10m ago"; model.Health.Verdict.Summary != want {
		t.Errorf("the degraded summary is %q, want %q", model.Health.Verdict.Summary, want)
	}
	assertValidatesAgainstTheContract(t, model)
}

// TestVerdictDegradedOnAnExhaustedRemote: the remote exhausting its retries
// is the run's own typed word that the network gave out — a live run
// carrying it reads degraded, whatever else it recovered.
func TestVerdictDegradedOnAnExhaustedRemote(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-12*time.Minute), "epic-2jn", "", nil,
		reconcile.StageRemoteExhausted,
		"origin failed on all 4 attempts, every one a transient remote failure; the run stops rather than waiting past its bound"))
	model := Build(src)
	if model.Health.Verdict.State != VerdictDegraded {
		t.Fatalf("an exhausted remote reads %q, want degraded", model.Health.Verdict.State)
	}
	if want := "degraded: the remote exhausted its retries"; model.Health.Verdict.Summary != want {
		t.Errorf("the degraded summary is %q, want %q", model.Health.Verdict.Summary, want)
	}
	assertValidatesAgainstTheContract(t, model)
}

// TestVerdictStoppedOnARunThatIsNotGoing: not alive and the lifecycle
// neither done nor cancelled is stopped — a dead run or a failed one — and
// the summary is the liveness answer's own reason; when the probe said
// nothing, the dead-run wait's own sentence is.
func TestVerdictStoppedOnARunThatIsNotGoing(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Liveness = LivenessInput{
		Alive: false, State: "dead",
		Reason: "pid 4242 is gone and never released the run: it died",
		Source: "run.pid",
	}
	model := Build(src)
	if model.Health.Verdict.State != VerdictStopped {
		t.Fatalf("a dead run mid-waves reads %q, want stopped", model.Health.Verdict.State)
	}
	if model.Health.Verdict.Summary != src.Liveness.Reason {
		t.Errorf("the stopped summary is %q, want the liveness answer's own reason %q",
			model.Health.Verdict.Summary, src.Liveness.Reason)
	}
	assertValidatesAgainstTheContract(t, model)

	// A failed run is stopped too: the checkpoint's own terminal word.
	src.Records.Checkpoint.State = "failed"
	model = Build(src)
	if model.Health.Verdict.State != VerdictStopped {
		t.Errorf("a failed run reads %q, want stopped", model.Health.Verdict.State)
	}

	// A probe that said nothing leaves the dead-run wait's own sentence.
	src.Records.Checkpoint.State = "running"
	src.Liveness.Reason = ""
	model = Build(src)
	if model.Health.Verdict.State != VerdictStopped {
		t.Fatalf("a dead run the probe said nothing about reads %q, want stopped", model.Health.Verdict.State)
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitDeadRun {
		t.Fatalf("a dead run waits on %+v, want dead-run", model.WaitsOn)
	}
	if model.Health.Verdict.Summary != model.WaitsOn.What {
		t.Errorf("the stopped summary is %q, want the dead-run wait's own %q",
			model.Health.Verdict.Summary, model.WaitsOn.What)
	}
}

// TestVerdictACompletedOrCancelledRunIsNotStopped: done and cancelled are
// the run's own terminal answers, stated by the phase bar — a finished run is
// not "stopped", and the vocabulary's healthy is what remains for it.
func TestVerdictACompletedOrCancelledRunIsNotStopped(t *testing.T) {
	t.Parallel()
	completed := runningEpicSources()
	completed.Records.Checkpoint.State = "completed"
	completed.Liveness.Alive = false
	completed.Standing, completed.StandingRead, completed.Session = nil, false, nil
	model := Build(completed)
	if model.Health.Verdict.State != VerdictHealthy || model.Health.Verdict.Summary != VerdictHealthy {
		t.Errorf("a completed run reads %+v, want the healthy word: done is the run's own terminal answer, not a stop",
			model.Health.Verdict)
	}
	assertValidatesAgainstTheContract(t, model)

	cancelled := runningEpicSources()
	cancelled.Records.Checkpoint.State = "cancelled"
	cancelled.Liveness.Alive = false
	cancelled.Standing, cancelled.StandingRead, cancelled.Session = nil, false, nil
	if verdict := Build(cancelled).Health.Verdict; verdict.State == VerdictStopped {
		t.Errorf("a cancelled run reads stopped, want the run's own terminal answer to stand")
	}
	// A completed run waiting on its PR — the merge phase, alive no longer —
	// is the done class's own answer too (tick jkb): the verdict exemption
	// once listed only done and cancelled, and a completed local run read
	// red "stopped" while the only thing left of it was the person's merge.
	// The merge wait is the frame's needs-you answer; the verdict is calm.
	merge := runningEpicSources()
	merge.Records.Checkpoint.State = "completed"
	merge.Liveness.Alive = false
	merge.Liveness.State = "not_running"
	merge.Liveness.Reason = "no process holds this run: it finished its own work"
	merge.Standing, merge.StandingRead, merge.Session = nil, false, nil
	merge.CI = &CIInput{
		State: "green",
		PR: &PR{Number: 12, URL: "https://github.com/example/ticfac/pull/12",
			HeadRef: "epic/2jn", HeadSHA: "9f2ab", BaseRef: "main"},
		Checks: []CheckState{{Name: "go", Status: "completed", Conclusion: "success"}},
	}
	model = Build(merge)
	if model.Lifecycle.Phase != PhaseMerge {
		t.Fatalf("a completed run with its PR open reads phase %q, want merge", model.Lifecycle.Phase)
	}
	if model.Health.Verdict.State == VerdictStopped {
		t.Errorf("a completed run waiting on its merge reads %+v, want not stopped: its work is over, the merge is the person's",
			model.Health.Verdict)
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitMerge {
		t.Errorf("a completed run waiting on its PR waits on %+v, want the merge", model.WaitsOn)
	}
	assertValidatesAgainstTheContract(t, model)
}

// int64Ptr is the test-side pointer the recovered list's nullable seconds
// needs.
func int64Ptr(n int64) *int64 { return &n }

// recoveredWhat finds one recovery by its what, for the assertion that reads.
func recoveredWhat(v HealthVerdict, what string) *Recovery {
	for i := range v.Recovered {
		if v.Recovered[i].What == what {
			return &v.Recovered[i]
		}
	}
	return nil
}
