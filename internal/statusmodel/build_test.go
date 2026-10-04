package statusmodel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The derivation suite: every field the model states is derived somewhere in
// build.go, and each derivation gets the test that pins it. The fixture is
// one running epic — the same shape the contract's golden carries — so the
// assertions here read like the questions the model exists to answer.

var testNow = time.Date(2026, 9, 27, 5, 30, 0, 0, time.UTC)

func tickPtr(id string) *string { return &id }
func intPtr(n int) *int         { return &n }

// provenanceOf is the minimal provenance a dispatch marker carries, with the
// fields the model reads: tier, model, executor.
func provenanceOf(tickID string, attempt int, tier, model, executor string) runstate.Provenance {
	return runstate.Provenance{
		RunID:     "epic-2jn",
		TickID:    tickPtr(tickID),
		Attempt:   intPtr(attempt),
		SourceRef: "refs/heads/epic/2jn",
		SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
		Phase:     runstate.PhaseWorker,
		Executor:  &executor,
		Role:      tickPtr("implement-tick"),
		Tier:      &tier,
		Model:     &model,
	}
}

func attemptMarker(n int, tickID, at string, tier, model, executor string) runstate.Attempt {
	return runstate.Attempt{
		SchemaVersion: runstate.SchemaVersion,
		Attempt:       n,
		TickID:        tickID,
		DispatchedAt:  at,
		JobHandle:     map[string]any{"executor": executor},
		Provenance:    provenanceOf(tickID, n, tier, model, executor),
	}
}

func evidence(key, check, tickID string, attempt int, result, phase, sourceSHA, startedAt, finishedAt string) runstate.Evidence {
	return runstate.Evidence{
		SchemaVersion: runstate.SchemaVersion,
		Key:           key,
		Provenance: runstate.Provenance{
			RunID:          "epic-2jn",
			TickID:         tickPtr(tickID),
			Attempt:        intPtr(attempt),
			SourceRef:      "refs/heads/epic/2jn",
			SourceSHA:      sourceSHA,
			IntegrationRef: tickPtr("refs/heads/epic/2jn"),
			Phase:          runstate.Phase(phase),
			Executor:       tickPtr("local-subprocess"),
			Tier:           tickPtr("strong"),
		},
		Check:      runstate.Check{ID: check, Kind: "command"},
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		Result:     result,
		Acceptance: "required",
		Output: runstate.Output{Inline: &runstate.InlineOutput{
			Mode: "inline", Stdout: "ok\n", Truncated: false, Redacted: true, MaxBytes: 1024,
		}},
	}
}

// testGraph is the epic's own layering: three waves, a review and a closeout
// behind them, one absorbed tick inside wave 2 — and the epic's own title
// and every task's gloss, the fields the dashboard copies straight off the
// graph (hn6 wave 1).
func testGraph() *tk.Graph {
	return &tk.Graph{
		Epic: tk.GraphEpic{
			ID:    "2jn",
			Title: "ticfac devex: one command to run, watch and triage an epic",
		},
		Waves: []tk.GraphWave{
			{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "nwj", Title: "The command surface on cobra + fang", Gloss: "cobra+fang command surface", Status: "closed"},
				{ID: "6dh", Title: "ticfac status --json: one model of every run", Gloss: "one model of every run", Status: "open"},
			}},
			{Wave: 2, Tasks: []tk.GraphTask{
				{ID: "89m", Title: "ticfac watch", Gloss: "the whole epic at a glance", Status: "open"},
				{ID: "152", Title: "go.mod's go directive is now 1.24.2", Gloss: "go directive bump", Status: "open"},
			}},
			{Wave: 3, Tasks: []tk.GraphTask{
				{ID: "xbp", Title: "Final review of the 2jn diff", Gloss: "final review", Status: "open", Role: "review"},
				{ID: "rrl", Title: "Close out 2jn", Gloss: "close out the epic", Status: "open", Role: "closeout"},
			}},
		},
	}
}

// runningEpicSources is the running-epic fixture: one closed tick behind its
// gate, one in-flight attempt mid-orientation, one gate-failed try behind
// the in-flight one, an absorbed tick, and the counts the feed typed.
func runningEpicSources() Sources {
	records := &Records{
		Checkpoint: &runstate.Checkpoint{
			SchemaVersion: runstate.SchemaVersion,
			RunID:         "epic-2jn",
			EpicID:        "2jn",
			Sequence:      8,
			State:         "running",
			Reason:        "6dh is dispatched",
			UpdatedAt:     testNow.Add(-20 * time.Minute).Format(time.RFC3339),
			Ticks: []runstate.TickState{
				{TickID: "nwj", State: "closed", Attempt: 1},
				{TickID: "6dh", State: "dispatched", Attempt: 3},
			},
		},
		Attempts: []runstate.Attempt{
			attemptMarker(1, "nwj", "2026-09-27T03:19:05Z", "strong", "claude-opus-5", "local-subprocess"),
			attemptMarker(2, "6dh", "2026-09-27T04:00:00Z", "strong", "@cf/zai-org/glm-5.3", "local-subprocess"),
			attemptMarker(3, "6dh", "2026-09-27T04:08:08Z", "strong", "@cf/zai-org/glm-5.3", "local-subprocess"),
		},
		Evidence: []runstate.Evidence{
			evidence("gate-nwj-1-go", "go", "nwj", 1, "pass", "integrated", "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a",
				"2026-09-27T04:06:38Z", "2026-09-27T04:07:23Z"),
			evidence("gate-6dh-2-go", "go", "6dh", 2, "fail", "integrated", "9f2ab6e0e8f96fc3fdc87c2f681519bb0d191a7",
				"2026-09-27T04:30:00Z", "2026-09-27T04:31:00Z"),
		},
		Decisions: []runstate.Decision{{
			SchemaVersion: runstate.SchemaVersion,
			Decision:      1,
			Role:          runstate.RoleClassifyTick,
			Request:       map[string]any{"epic_id": "2jn"},
			Response: map[string]any{
				"model": "classifier@example.com",
				"usage": map[string]any{"cost_usd": 0.04, "input_tokens": 52945, "output_tokens": 900},
			},
			Validated:   true,
			RequestedAt: "2026-09-27T03:10:00Z",
			AnsweredAt:  "2026-09-27T03:10:20Z",
		}},
		Absorptions: []runstate.Absorption{{
			SchemaVersion: runstate.SchemaVersion,
			Key:           "6721b3cacde6ff8b433d22c3d0b38054705d42296f11f5efe67e8487f5569a56",
			TickID:        "152",
			Gating:        true,
			Basis:         runstate.AbsorptionPredicted,
			Reason:        "the go directive moved with the fang requirement",
			Placement:     runstate.AbsorptionBeforeReview,
			DecidedAt:     "2026-09-27T04:05:49Z",
		}},
		Findings: []runstate.Finding{{
			SchemaVersion:  runstate.SchemaVersion,
			Key:            "46b634a4f894acc04534dd6e9b70b677d68c39f3b66b98e820613ac7dd8c6ce2",
			Source:         "worker-report",
			DiscoveredFrom: "run-epic-2jn/tick-nwj/attempt-1",
			Kind:           "proposed-tick",
			Title:          "README's command surface section predates the cobra+fang tree",
			Body:           "the README names commands the tree no longer carries",
			Severity:       "low",
			TickID:         "nwj",
			Attempt:        1,
			Status:         runstate.FindingProposed,
			ProposedAt:     "2026-09-27T04:05:54Z",
		}},
	}

	one, two, three := 1, 2, 3
	feed := []runfeed.Event{
		runfeed.NewEvent(testNow.Add(-3*time.Hour), "epic-2jn", "", nil, reconcile.StageBudgetSet, "the effective budget for this run is $1.00"),
		runfeed.NewEvent(testNow.Add(-2*time.Hour), "epic-2jn", "nwj", &one, reconcile.StageRemoteRetried, "origin refused the fetch; retry 1"),
		runfeed.NewEvent(testNow.Add(-90*time.Minute), "epic-2jn", "nwj", &one, reconcile.StageStallWarned, "nwj try 1 is alive but has produced nothing durable for 15m"),
		runfeed.NewEvent(testNow.Add(-80*time.Minute), "epic-2jn", "6dh", &two, reconcile.StageGateFailed, "the integrated gate refused attempt 2 of 6dh"),
		runfeed.NewEvent(testNow.Add(-60*time.Minute), "epic-2jn", "", nil, reconcile.StageResumedAutomatically, "the run resumed a wedged collect by itself"),
		runfeed.NewEvent(testNow.Add(-19*time.Minute), "epic-2jn", "6dh", &three, reconcile.StageWallClock, "the wall clock of 3600s fired 19m0s ago and attempt 3 of 6dh has not settled"),
		runfeed.NewEvent(testNow.Add(-19*time.Second), "epic-2jn", "6dh", &three, reconcile.StageDispatched, "6dh try 2 dispatched (run dispatch #3)"),
	}

	idle := runprogress.Duration(5 * time.Minute)
	standing := []runprogress.Attempt{{
		TickID: "6dh", Attempt: 3,
		Branch:     "refs/heads/ticfac/run-epic-2jn/tick-6dh/attempt-3",
		Worktree:   "/worktrees/run-epic-2jn/tick-6dh/attempt-3",
		BranchIdle: &idle, WorktreeIdle: &idle,
	}}

	return Sources{
		Now:          testNow,
		RunID:        "epic-2jn",
		Host:         HostLocal,
		Graph:        testGraph(),
		Records:      records,
		Feed:         feed,
		Standing:     standing,
		StandingRead: true,
		Liveness: LivenessInput{
			Alive: true, State: "alive",
			Reason: "pid 4242 has been running since 2026-09-27T03:00:00Z",
			Source: "run.pid",
		},
		Session: func(worktree string) *Turn {
			at := testNow.Add(-45 * time.Second)
			return &Turn{At: at, Summary: "assistant: read internal/cli/status.go"}
		},
	}
}

// TestTheModelDerivesTheWavesAndTicks: the waves come from the tracker's own
// layering, each tick's state from the durable records, the try history from
// the attempt markers plus the gate evidence plus the checkpoint, and the
// absorbed tick is marked as such.
func TestTheModelDerivesTheWavesAndTicks(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())

	if model.Waves == nil {
		t.Fatal("the tracker answered and the model states no waves")
	}
	waves := *model.Waves
	if len(waves) != 3 {
		t.Fatalf("the model layered %d waves, want 3", len(waves))
	}
	if waves[0].State != WaveActive {
		t.Errorf("wave 1 (a tick still open) reads %q, want the active frontier", waves[0].State)
	}
	if waves[1].State != WaveUpcoming || waves[2].State != WaveUpcoming {
		t.Errorf("waves behind the frontier read %q and %q, want upcoming, upcoming",
			waves[1].State, waves[2].State)
	}

	tickByID := map[string]Tick{}
	for _, w := range waves {
		for _, tick := range w.Ticks {
			tickByID[tick.TickID] = tick
		}
	}

	nwj := tickByID["nwj"]
	if nwj.State != tickClosed {
		t.Errorf("nwj reads %q, want closed (the checkpoint's own word)", nwj.State)
	}
	if nwj.Try == nil || *nwj.Try != 1 || nwj.Attempt == nil || *nwj.Attempt != 1 {
		t.Errorf("nwj reads try %v attempt %v, want try 1 attempt 1", nwj.Try, nwj.Attempt)
	}
	if len(nwj.Tries) != 1 || nwj.Tries[0].Outcome != TryClosed {
		t.Errorf("nwj's try history is %+v, want one closed try", nwj.Tries)
	}
	if nwj.Tier == nil || *nwj.Tier != "strong" || nwj.Model == nil || *nwj.Model != "claude-opus-5" ||
		nwj.Executor == nil || *nwj.Executor != "local-subprocess" {
		t.Errorf("nwj's provenance reads %+v, want the dispatch marker's tier/model/executor", nwj)
	}

	// The in-flight tick: two tries, the first gate-failed, the second in
	// flight — and the try numbers are the tick's own (h58), so attempt 3
	// with attempt 2 dead reads as try 2.
	dh := tickByID["6dh"]
	if dh.State != tickDispatched {
		t.Errorf("6dh reads %q, want dispatched", dh.State)
	}
	if dh.Try == nil || *dh.Try != 2 {
		t.Errorf("6dh reads try %v, want 2: attempt 2 gate-failed, attempt 3 in flight", dh.Try)
	}
	if len(dh.Tries) != 2 {
		t.Fatalf("6dh's try history has %d entries, want 2", len(dh.Tries))
	}
	if dh.Tries[0].Outcome != TryGateFailed {
		t.Errorf("6dh attempt 2's outcome is %q, want gate-failed (its gate evidence refused it)", dh.Tries[0].Outcome)
	}
	if dh.Tries[1].Outcome != TryInFlight {
		t.Errorf("6dh attempt 3's outcome is %q, want in-flight (its worktree stands)", dh.Tries[1].Outcome)
	}
	if dh.ElapsedSeconds == nil {
		t.Errorf("6dh's in-flight attempt carries no elapsed time")
	} else if *dh.ElapsedSeconds != 4912 { // 04:08:08 -> 05:30:00
		t.Errorf("6dh's elapsed is %d, want 4912", *dh.ElapsedSeconds)
	}

	if !tickByID["152"].Absorbed {
		t.Error("tick 152 — created by an absorption record — is not marked absorbed")
	}
	if tickByID["89m"].Absorbed {
		t.Error("tick 89m — never absorbed into — is marked absorbed")
	}

	if model.Progress.Ticks == nil || model.Progress.Ticks.Open != 5 || model.Progress.Ticks.Closed != 1 {
		t.Errorf("the tick progress is %+v, want 6 total, 1 closed, 5 open", model.Progress.Ticks)
	}
	if model.Progress.Waves == nil || model.Progress.Waves.Active != 1 {
		t.Errorf("the wave progress is %+v, want wave 1 active", model.Progress.Waves)
	}
}

// TestTheModelDerivesTheLifecycleAndTheWaits: a running mid-wave run is in
// the waves phase with the frontier named, waiting on its workers; the
// phases behind it are done and the phases ahead pending.
func TestTheModelDerivesTheLifecycleAndTheWaits(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())

	if model.Lifecycle.Phase != PhaseWaves {
		t.Errorf("the lifecycle phase is %q, want waves", model.Lifecycle.Phase)
	}
	if model.Lifecycle.Wave == nil || model.Lifecycle.Wave.Active != 1 || model.Lifecycle.Wave.Total != 3 {
		t.Errorf("the wave reference is %+v, want active 1 of 3", model.Lifecycle.Wave)
	}
	states := map[string]string{}
	for _, p := range model.Lifecycle.Phases {
		states[p.Phase] = p.State
	}
	if states[PhasePlan] != PhaseStateDone {
		t.Errorf("plan is %q, want done: the run dispatched long ago", states[PhasePlan])
	}
	if states[PhaseWaves] != PhaseStateActive {
		t.Errorf("waves is %q, want active", states[PhaseWaves])
	}
	for _, phase := range []string{PhaseReview, PhaseCloseout, PhaseCI, PhaseMerge} {
		if states[phase] != PhaseStatePending {
			t.Errorf("%s is %q, want pending", phase, states[phase])
		}
	}

	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitWorkers {
		t.Fatalf("the run waits on %+v, want its one live worker", model.WaitsOn)
	}
	if model.WaitsOn.NeedsPerson {
		t.Error("waiting on workers is a watcher's wait, not a person's alarm")
	}
}

// TestTheModelDerivesTheWorkers: one live worker with its gaps, the run's
// typed wall clock firing, the runner's session-log silence and last turn.
func TestTheModelDerivesTheWorkers(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())

	if model.Workers == nil || len(*model.Workers) != 1 {
		t.Fatalf("the census read one standing attempt and the model says %+v", model.Workers)
	}
	w := (*model.Workers)[0]
	if w.TickID != "6dh" || w.Attempt != 3 {
		t.Errorf("the worker reads %s#%d, want 6dh#3", w.TickID, w.Attempt)
	}
	if w.SilenceSeconds == nil || *w.SilenceSeconds != 45 {
		t.Errorf("the worker's silence is %+v, want 45s since the session log last grew", w.SilenceSeconds)
	}
	if w.LastTurn == nil || !strings.Contains(*w.LastTurn, "assistant: read internal/cli/status.go") {
		t.Errorf("the worker's last turn is %+v, want the session log's summary", w.LastTurn)
	}
	if w.WallClock == nil || !strings.Contains(w.WallClock.Detail, "wall clock of 3600s fired") {
		t.Errorf("the worker's wall clock firing is %+v, want the run's own typed line", w.WallClock)
	}
	if w.ElapsedSeconds == nil || *w.ElapsedSeconds != 4912 {
		t.Errorf("the worker's elapsed is %+v, want 4912s", w.ElapsedSeconds)
	}
}

// TestTheModelCountsHealthFromTheFeed: retries, interventions, stall
// warnings and firings are counts of the run's own typed lines, and the
// verdict beside them (wave 2, tick 7uv) is derived from the same lines and
// the model built around them: healthy here — the fixture's one stall
// warning is ninety minutes old, outside the window that still reads — with
// what the run got past listed as calm.
func TestTheModelCountsHealthFromTheFeed(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())
	want := Health{
		RemoteRetries: 1, Interventions: 1, StallWarnings: 1, WallClocksFired: 1,
		Verdict: HealthVerdict{
			State:   VerdictHealthy,
			Summary: VerdictHealthy,
			Recovered: []Recovery{
				{What: "net", Count: 1},
				{What: "interventions", Count: 1},
				{What: "wall clocks", Count: 1},
			},
		},
	}
	if !reflect.DeepEqual(model.Health, want) {
		t.Errorf("the health counts are %+v, want %+v", model.Health, want)
	}
}

// TestTheModelCarriesGatesPerCheckPerHead: the gate evidence rides field for
// field, keyed by the source sha it ran on.
func TestTheModelCarriesGatesPerCheckPerHead(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())
	if len(model.Gates) != 2 {
		t.Fatalf("the model carries %d gates, want 2", len(model.Gates))
	}
	var gate Gate
	for _, g := range model.Gates {
		if g.Key == "gate-nwj-1-go" {
			gate = g
		}
	}
	if gate.Key != "gate-nwj-1-go" {
		t.Fatalf("the nwj gate did not ride: %+v", model.Gates)
	}
	if gate.Result != "pass" {
		t.Errorf("the nwj gate reads result %q, want pass", gate.Result)
	}
	if gate.Head == nil || !strings.HasPrefix(*gate.Head, "0fc09212") {
		t.Errorf("the gate's head is %+v, want the source sha it ran on", gate.Head)
	}
	if gate.Check != "go" {
		t.Errorf("the gate's check reads %q, want go", gate.Check)
	}
}

// TestTheModelSumsTheRecordedCost: the decision records' own usage, and only
// that — with the basis saying so.
func TestTheModelSumsTheRecordedCost(t *testing.T) {
	t.Parallel()
	model := Build(runningEpicSources())
	if model.Cost.RecordedUSD != 0.04 {
		t.Errorf("the recorded cost is %v, want 0.04 from the one decision that carries usage", model.Cost.RecordedUSD)
	}
	if model.Cost.Attempts != 3 {
		t.Errorf("the model counts %d attempts, want 3 dispatch markers", model.Cost.Attempts)
	}
	if !strings.Contains(model.Cost.Basis, "worker jobs record no cost") {
		t.Errorf("the cost basis %q does not name what the number covers", model.Cost.Basis)
	}
}

// TestAHeldRunNamesTheCommandThatReleasesIt: the run's own typed run_held
// line is the wait, with the settle command spelled exactly the way watch
// spells it — which tick, which attempt, and who releases.
func TestAHeldRunNamesTheCommandThatReleasesIt(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	four := 4
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), "epic-2jn", "6dh", &four,
		reconcile.StageRunHeld, "attempt 3 of 6dh struck out: the refusal the run recorded"))
	model := Build(src)

	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
		t.Fatalf("a run holding an attempt waits on %+v, want held-for-person", model.WaitsOn)
	}
	if !model.WaitsOn.NeedsPerson {
		t.Error("a held attempt is a person's decision, and the model must say so")
	}
	if model.WaitsOn.UnblockCommand == nil ||
		*model.WaitsOn.UnblockCommand != `ticfac settle 2jn 6dh 4 --release "<who>"` {
		t.Errorf("the unblocking command is %+v, want the settle command naming tick, attempt and release",
			model.WaitsOn.UnblockCommand)
	}
	found := false
	for _, a := range model.Attention {
		if a.Kind == WaitHeldForPerson {
			found = true
		}
	}
	if !found {
		t.Errorf("a held attempt is not in the attention list: %+v", model.Attention)
	}
}

// TestAHeldRunWhoseIDIsNotTheEpicSpellingNamesIt (tick ulw): a run whose
// records live under a run id other than epic-<epic-id> — today's cloud
// runs, whose orchestrator execs run-epic --run-id <factory run id> — must
// name that id in the release command, because settle without --run-id
// defaults to the epic spelling and the store under IT carries no such
// attempt: the command the needs-you line gives would refuse. A run under
// the epic spelling keeps the bare spelling — the flag would name the run
// the command already addresses (TestAHeldRunNamesTheCommandThatReleasesIt).
func TestAHeldRunWhoseIDIsNotTheEpicSpellingNamesIt(t *testing.T) {
	t.Parallel()
	cloud := "run_1a2b3c4d5e6f"
	src := runningEpicSources()
	src.RunID = cloud
	src.Records.Checkpoint.RunID = cloud
	four := 4
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), cloud, "6dh", &four,
		reconcile.StageRunHeld, "attempt 3 of 6dh struck out: the refusal the run recorded"))
	model := Build(src)

	if model.RunID != cloud {
		t.Fatalf("the model answers for run %q, want %q", model.RunID, cloud)
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
		t.Fatalf("a run holding an attempt waits on %+v, want held-for-person", model.WaitsOn)
	}
	if model.WaitsOn.UnblockCommand == nil ||
		*model.WaitsOn.UnblockCommand != `ticfac settle 2jn 6dh 4 --run-id `+cloud+` --release "<who>"` {
		t.Errorf("the unblocking command is %+v, want the settle command addressed to the holding run",
			model.WaitsOn.UnblockCommand)
	}
}

// TestAHoldAResumeSettledIsHistory (tick 4mv): the feed is append-only per
// RUN ID, so a resumed run still carries the previous incarnation's
// run_held line — and a hold somebody already settled by resuming the run
// is history, not a standing wait. The resume is the run's own durable word
// that the lines before it belong to an incarnation that ended (the same
// rule the watch's subscription start made for lines, tick usx); a hold the
// CURRENT incarnation wrote still stands.
func TestAHoldAResumeSettledIsHistory(t *testing.T) {
	t.Parallel()

	// The previous incarnation held, and somebody resumed the run.
	src := runningEpicSources()
	three := 3
	src.Feed = append(src.Feed,
		runfeed.NewEvent(testNow.Add(-40*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageRunHeld, "attempt 3 of 6dh struck out: the refusal the run recorded"),
		runfeed.NewEvent(testNow.Add(-39*time.Minute), "epic-2jn", "", nil,
			reconcile.StageResumed, "the run stopped at failed and is resumed under the same run id"),
	)
	model := Build(src)
	for _, a := range model.Attention {
		if a.Kind == WaitHeldForPerson {
			t.Errorf("a hold a resume settled is still attention a person must answer: %+v", a)
		}
	}
	if model.WaitsOn != nil && model.WaitsOn.Kind == WaitHeldForPerson {
		t.Errorf("a hold a resume settled is still the run's wait: %+v", model.WaitsOn)
	}

	// The current incarnation's own hold still stands: a resume before it
	// makes nothing history that came after it.
	src = runningEpicSources()
	src.Feed = append(src.Feed,
		runfeed.NewEvent(testNow.Add(-40*time.Minute), "epic-2jn", "", nil,
			reconcile.StageResumed, "the run stopped at failed and is resumed under the same run id"),
		runfeed.NewEvent(testNow.Add(-30*time.Minute), "epic-2jn", "6dh", &three,
			reconcile.StageRunHeld, "attempt 3 of 6dh struck out: the refusal the run recorded"),
	)
	model = Build(src)
	found := false
	for _, a := range model.Attention {
		if a.Kind == WaitHeldForPerson {
			found = true
		}
	}
	if !found {
		t.Errorf("the current incarnation's hold is not in the attention list: %+v", model.Attention)
	}
}

// TestAHoldAboutTheWorldNamesTheRunAgainCommand (tick gf0): the holds that
// fire before the tick's first dispatch — the width, a foreign claim —
// carry a NULL attempt, so the settle command cannot address them ("-" is
// not an attempt number) and a release would not clear them anyway: they
// are facts about the world that end when the holder's tick closes or a
// slot frees. The needs-you command is the RESUME, named by the host the run
// lives on — the same command a dead run's wait carries — never a settle.
func TestAHoldAboutTheWorldNamesTheRunAgainCommand(t *testing.T) {
	t.Parallel()

	t.Run("the width", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), "epic-2jn", "w9b", nil,
			reconcile.StageRunHeld, "claim_width: the width 2jn declares is already full of claims this run does not hold"))
		model := Build(src)
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
			t.Fatalf("a width hold waits on %+v, want held-for-person", model.WaitsOn)
		}
		if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac run-epic 2jn" {
			t.Errorf("the width hold's command is %+v, want the run-again command a resume addresses",
				model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("a foreign claim", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), "epic-2jn", "w9b", nil,
			reconcile.StageRunHeld, "foreign_claim: w9b is claimed by run run-epic-xte, whose records do not read finished"))
		model := Build(src)
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
			t.Fatalf("a foreign-claim hold waits on %+v, want held-for-person", model.WaitsOn)
		}
		if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac run-epic 2jn" {
			t.Errorf("the foreign-claim hold's command is %+v, want the run-again command a resume addresses",
				model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("a foreign claim a cloud run holds", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.Host = HostCloud
		src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), "epic-2jn", "w9b", nil,
			reconcile.StageRunHeld, "foreign_claim: w9b is claimed by run run-epic-xte, whose records do not read finished"))
		model := Build(src)
		if model.WaitsOn == nil || model.WaitsOn.UnblockCommand == nil {
			t.Fatalf("the cloud foreign-claim hold carries no command: %+v", model.WaitsOn)
		}
		if *model.WaitsOn.UnblockCommand != "ticfac run 2jn --cloud" {
			t.Errorf("the cloud world hold's command is %q, want the factory resubmission a resume on the cloud host is",
				*model.WaitsOn.UnblockCommand)
		}
	})
}

// TestAnAbsorptionBoundHoldNamesTheFindingsDecision (tick gf0): the bound's
// hold asks a person to judge the chain the refusal carries, and the finding
// it refused to absorb is still theirs to decide — so the needs-you command
// is the triage, the one command that decides a finding; the refusal's own
// message names the raise with --absorption-depth as the other road. It
// carries no attempt any more than the world holds do — and even the line
// that does is not released: deciding the finding is the move, never a
// release of the attempt that reported it.
func TestAnAbsorptionBoundHoldNamesTheFindingsDecision(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	five := 5
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), "epic-2jn", "nwj", &five,
		reconcile.StageRunHeld, "absorption_depth_exceeded: absorbing the finding \"c0ffee\" would be the 4th "+
			"absorption of ONE chain that already carries 3 and the bound is 3 (tick qjj)"))
	model := Build(src)
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
		t.Fatalf("the bound's hold waits on %+v, want held-for-person", model.WaitsOn)
	}
	if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac triage 2jn" {
		t.Errorf("the bound's hold command is %+v, want the triage that decides the finding",
			model.WaitsOn.UnblockCommand)
	}
}

// TestAFinalReviewHoldNamesTheRunAgainCommand (tick quz): the final-review
// hold is the one hold that fires AFTER an attempt was dispatched — the
// close-out's — so its run_held line CARRIES an attempt, and the per-kind
// decision used to name the settle that attempt addresses. Releasing it
// clears nothing: the hold is the review's NOT READY verdict recorded on
// the PR, which the next resume re-reads and holds on again. The moves are
// the refusal's own — fix what it names and run the epic again, merge the
// PR by hand to accept it (a re-run then finds it merged), or close it —
// so the command is the run again, the same shape the world holds got: the
// reason decides, never the attempt the line happens to carry.
func TestAFinalReviewHoldNamesTheRunAgainCommand(t *testing.T) {
	t.Parallel()

	finalReviewHold := func(at time.Time) runfeed.Event {
		attempt := 2
		return runfeed.NewEvent(at, "epic-2jn", "rrl", &attempt, reconcile.StageRunHeld,
			"land_review_not_ready: the run does not merge the epic 2jn: its final review (decision 3) still judges "+
				"it NOT READY after 2 review round(s), the bound being 2. The verdict is on the epic PR, and accepting "+
				"work the run's own review rejected is a person's judgement: fix what it names and run the epic again, "+
				"merge the PR by hand to accept it (a re-run then finds it merged), or close it")
	}

	t.Run("a local run's final-review hold", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.Feed = append(src.Feed, finalReviewHold(testNow.Add(-10*time.Minute)))
		model := Build(src)
		if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
			t.Fatalf("the final-review hold waits on %+v, want held-for-person", model.WaitsOn)
		}
		if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac run-epic 2jn" {
			t.Errorf("the final-review hold's command is %+v, want the run-again command: releasing the attempt "+
				"the line carries clears nothing, the verdict on the PR is what it holds", model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("a cloud run's final-review hold", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.Host = HostCloud
		src.Feed = append(src.Feed, finalReviewHold(testNow.Add(-10*time.Minute)))
		model := Build(src)
		if model.WaitsOn == nil || model.WaitsOn.UnblockCommand == nil {
			t.Fatalf("the cloud final-review hold carries no command: %+v", model.WaitsOn)
		}
		if *model.WaitsOn.UnblockCommand != "ticfac run 2jn --cloud" {
			t.Errorf("the cloud final-review hold's command is %q, want the factory resubmission a resume on "+
				"the cloud host is", *model.WaitsOn.UnblockCommand)
		}
	})

	t.Run("a prior run's final-review hold", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.PriorRecords = []Records{{Checkpoint: priorCheckpoint("run_prior", "failed")}}
		src.PriorFeeds = map[string][]runfeed.Event{"run_prior": {finalReviewHold(testNow.Add(-2 * time.Hour))}}
		model := Build(src)
		var attention *Attention
		for i := range model.Attention {
			if model.Attention[i].Kind == WaitHeldForPerson && strings.Contains(model.Attention[i].What, "run_prior") {
				attention = &model.Attention[i]
			}
		}
		if attention == nil {
			t.Fatalf("the prior run's final-review hold is not attention: %+v", model.Attention)
		}
		if attention.UnblockCommand == nil || *attention.UnblockCommand != "ticfac run-epic 2jn" {
			t.Errorf("the prior run's final-review hold command is %+v, want the run-again command a resume "+
				"addresses", attention.UnblockCommand)
		}
	})
}

// TestAPriorRunsWorldHoldNamesTheRunAgainCommand: a hold an earlier run left
// about the world clears by the same per-kind decision the newest run's own
// hold answers with — the run again, never a settle the dash attempt would
// refuse — in the header's needs-you entry and the try's next step alike.
func TestAPriorRunsWorldHoldNamesTheRunAgainCommand(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.PriorRecords = []Records{{Checkpoint: priorCheckpoint("run_prior", "failed")}}
	src.PriorFeeds = map[string][]runfeed.Event{"run_prior": {
		runfeed.NewEvent(testNow.Add(-2*time.Hour), "run_prior", "w9b", nil, reconcile.StageRunHeld,
			"claim_width: the width 2jn declares is already full of claims this run does not hold"),
	}}
	model := Build(src)
	var attention *Attention
	for i := range model.Attention {
		if model.Attention[i].Kind == WaitHeldForPerson && strings.Contains(model.Attention[i].What, "run_prior") {
			attention = &model.Attention[i]
		}
	}
	if attention == nil {
		t.Fatalf("the prior run's width hold is not attention: %+v", model.Attention)
	}
	if attention.UnblockCommand == nil || *attention.UnblockCommand != "ticfac run-epic 2jn" {
		t.Errorf("the prior run's width hold command is %+v, want the run-again command a resume addresses",
			attention.UnblockCommand)
	}
}

// TestADeadRunIsAttentionWithTheResumeCommand: a run whose process is gone
// without its own terminal word is the first thing a person must learn, with
// the one command that resumes it.
func TestADeadRunIsAttentionWithTheResumeCommand(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Liveness.Alive = false
	src.Liveness.State = "dead"
	src.Liveness.Reason = "pid 4242 is gone and never released the run: it died"
	src.Session = nil
	src.Standing = nil
	model := Build(src)

	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitDeadRun {
		t.Fatalf("a dead run waits on %+v, want dead-run", model.WaitsOn)
	}
	if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac run-epic 2jn" {
		t.Errorf("the dead run's unblock command is %+v, want the resume", model.WaitsOn.UnblockCommand)
	}
	// The untriaged finding beside the dead run needs the person too.
	kinds := map[string]bool{}
	for _, a := range model.Attention {
		kinds[a.Kind] = true
	}
	if !kinds[WaitFinding] {
		t.Errorf("the untriaged finding beside a dead run is not attention: %+v", model.Attention)
	}
}

// TestAFinishedCloudRunIsNotADeadRun: a cloud run's own record state is
// its durable terminal word, and a checkout that cannot read that run's
// other records — another project's run, whose run state will never be in
// this checkout — must not read it as dead. Dead means gone WITHOUT a
// terminal word; the surface that aggregates every run (tick 2qz) is the
// one that showed the factory's finished runs all claiming dead-run.
func TestAFinishedCloudRunIsNotADeadRun(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"completed", "stopped", "failed"} {
		src := runningEpicSources()
		src.Host = HostCloud
		src.Records = &Records{} // nothing this checkout can read: another project's run
		src.Graph = nil
		src.Standing = nil
		src.Session = nil
		src.Liveness.Alive = false
		src.Liveness.State = state
		src.Liveness.Source = "workflow-record"
		src.Liveness.Reason = "the Workflow's own record says " + state
		model := Build(src)
		if model.WaitsOn != nil && model.WaitsOn.Kind == WaitDeadRun {
			t.Errorf("a cloud run whose record says %s waits on dead-run: %+v", state, model.WaitsOn)
		}
		for _, a := range model.Attention {
			if a.Kind == WaitDeadRun {
				t.Errorf("a cloud run whose record says %s is attention as dead-run", state)
			}
		}
	}
	// A local run's gone-without-a-word is still dead: the local probe's
	// states never name an end, and the claim is the model's own for it.
	src := runningEpicSources()
	src.Records = &Records{}
	src.Graph = nil
	src.Standing = nil
	src.Session = nil
	src.Liveness.Alive = false
	src.Liveness.State = "not_running"
	src.Liveness.Reason = "no process holds this run: the last one released it, or none has claimed it here"
	if model := Build(src); model.WaitsOn == nil || model.WaitsOn.Kind != WaitDeadRun {
		t.Errorf("a local run gone without a terminal record waits on %+v, want dead-run", model.WaitsOn)
	}
}

// TestACompletedRunWithAnOpenPRWaitsOnTheMerge: the merge is a person's,
// always — the model says it as the wait, with the PR named in the what.
func TestACompletedRunWithAnOpenPRWaitsOnTheMerge(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Records.Checkpoint.State = "completed"
	src.Liveness.Alive = false
	src.Liveness.State = "not_running"
	src.Session = nil
	src.Standing = nil
	src.CI = &CIInput{
		State: "green",
		PR: &PR{Number: 12, URL: "https://github.com/example/ticfac/pull/12",
			HeadRef: "epic/2jn", HeadSHA: "9f2ab", BaseRef: "main"},
		Checks: []CheckState{{Name: "go", Status: "completed", Conclusion: "success"}},
	}
	model := Build(src)

	if model.Lifecycle.Phase != PhaseMerge {
		t.Errorf("a completed run with its PR still open reads phase %q, want merge", model.Lifecycle.Phase)
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitMerge {
		t.Fatalf("the completed run waits on %+v, want the merge", model.WaitsOn)
	}
	if !strings.Contains(model.WaitsOn.What, "pull/12") {
		t.Errorf("the merge wait does not name the PR: %q", model.WaitsOn.What)
	}
}

// TestAFindingHoldIsClearedByTriage: the close-out's untriaged-findings
// hold is a person's decision about FINDINGS, not an attempt to release —
// the run_held line is cleared by `ticfac triage`, and naming `settle`
// there (as every other hold is) points a person at a command that refuses
// it: settle releases an attempt, and the finding hold holds no attempt.
func TestAFindingHoldIsClearedByTriage(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	four := 4
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), "epic-2jn", "rrl", &four,
		reconcile.StageRunHeld, "finding_untriaged: 1 finding(s) this run drafted are still waiting for a person"))
	model := Build(src)

	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
		t.Fatalf("a run holding for triage waits on %+v, want held-for-person", model.WaitsOn)
	}
	if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac triage 2jn" {
		t.Errorf("the finding hold's unblocking command is %+v, want ticfac triage 2jn",
			model.WaitsOn.UnblockCommand)
	}
}

// TestTheUntriagedFindingWaitIsClearedByTriage: the attention a dead run's
// untriaged findings raise names the command that SETTLES them, not the one
// that only lists them — `ticfac findings` walks away having changed
// nothing, and a person following it finds the close-out still held.
func TestTheUntriagedFindingWaitIsClearedByTriage(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Liveness.Alive = false
	src.Liveness.State = "not_running"
	src.Liveness.Reason = "no process holds this run: the last one released it, or none has claimed it here"
	src.Session = nil
	src.Standing = nil
	model := Build(src)

	for _, a := range model.Attention {
		if a.Kind != WaitFinding {
			continue
		}
		if a.UnblockCommand == nil || *a.UnblockCommand != "ticfac triage 2jn" {
			t.Errorf("the untriaged finding's unblocking command is %+v, want ticfac triage 2jn",
				a.UnblockCommand)
		}
		return
	}
	t.Errorf("the untriaged finding is not attention: %+v", model.Attention)
}

// TestACloudRunsOwnFindingHoldNamesTheRunItsStoreLivesAt (tick q8m): a
// hold the CURRENT run left is cleared by triage addressed to THAT run's
// own store. A cloud run is addressed by the factory's run_<hex> (tick
// ulw) and writes its records — its drafted findings included — under that
// id, so the bare command's default (the local spelling epic-<epic-id>)
// names a store a cloud run never wrote: a person following it finds no
// findings and the hold stands. Local runs keep the bare spelling — that
// half is pinned by TestAFindingHoldIsClearedByTriage above.
func TestACloudRunsOwnFindingHoldNamesTheRunItsStoreLivesAt(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Host = HostCloud
	src.RunID = "run_a1b2c3d4e5"
	src.Records.Checkpoint.RunID = "run_a1b2c3d4e5"
	four := 4
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-10*time.Minute), "run_a1b2c3d4e5", "rrl", &four,
		reconcile.StageRunHeld, "finding_untriaged: 1 finding(s) this run drafted are still waiting for a person"))
	model := Build(src)

	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitHeldForPerson {
		t.Fatalf("a run holding for triage waits on %+v, want held-for-person", model.WaitsOn)
	}
	if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac triage 2jn --run-id run_a1b2c3d4e5" {
		t.Errorf("the cloud finding hold's unblocking command is %+v, want the triage addressed to the run's own store",
			model.WaitsOn.UnblockCommand)
	}
}

// TestACloudRunsUntriagedFindingsWaitNamesTheRunItsStoreLivesAt (tick
// q8m): the WaitFinding attention for a dead run's own drafts names the
// triage command addressed to the store those drafts were read from — for
// a cloud run, the factory's run_<hex>, never the bare command's local
// default.
func TestACloudRunsUntriagedFindingsWaitNamesTheRunItsStoreLivesAt(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Host = HostCloud
	src.RunID = "run_a1b2c3d4e5"
	src.Records.Checkpoint.RunID = "run_a1b2c3d4e5"
	src.Liveness.Alive = false
	src.Liveness.State = "not_running"
	src.Liveness.Reason = "no process holds this run: the last one released it, or none has claimed it here"
	src.Session = nil
	src.Standing = nil
	model := Build(src)

	for _, a := range model.Attention {
		if a.Kind != WaitFinding {
			continue
		}
		if a.UnblockCommand == nil || *a.UnblockCommand != "ticfac triage 2jn --run-id run_a1b2c3d4e5" {
			t.Errorf("the cloud run's untriaged finding's unblocking command is %+v, want the triage addressed to the run's own store",
				a.UnblockCommand)
		}
		return
	}
	t.Errorf("the untriaged finding is not attention: %+v", model.Attention)
}

// priorCheckpoint is an earlier run's checkpoint as the records read it:
// one run of this epic, in the state it ended (or stands) in.
func priorCheckpoint(runID, state string) *runstate.Checkpoint {
	return &runstate.Checkpoint{
		SchemaVersion: runstate.SchemaVersion,
		RunID:         runID,
		EpicID:        "2jn",
		Sequence:      4,
		State:         runstate.State(state),
		Reason:        "the run died before its close-out",
		UpdatedAt:     testNow.Add(-2 * time.Hour).Format(time.RFC3339),
	}
}

// priorFinding is one earlier run's draft as the records read it: the
// attempt that discovered it names the run it belongs to.
func priorFinding(key, runID, tickID, at string) runstate.Finding {
	return runstate.Finding{
		SchemaVersion:  runstate.SchemaVersion,
		Key:            key,
		Source:         "ticfac-worker",
		DiscoveredFrom: "run-" + runID + "/tick-" + tickID + "/attempt-1",
		Kind:           "defect",
		Title:          "A finding only an earlier run found",
		Body:           "Discovered beside the work, reported mechanically.",
		Severity:       "medium",
		TickID:         tickID,
		Attempt:        1,
		Status:         runstate.FindingProposed,
		ProposedAt:     at,
		Provenance: runstate.Provenance{
			RunID: runID, SourceRef: "refs/heads/epic/2jn",
			SourceSHA: "0fc09212e0e8f96fc3fdc87c2f681519bb0d191a", Phase: runstate.PhaseWorker,
		},
	}
}

// TestPriorRunUntriagedFindingsReachNeedsYou (tick d23): a run that died
// before its close-out raised no run_held line — the hold the close-out's
// findings gate would have raised never happened — so its untriaged drafts
// are invisible to a WaitFinding block that reads only the newest run's
// records, although a person's decision about them is standing. Every
// run's drafts reach needs-you, each with the triage command addressed to
// the run whose own records hold them; a prior run whose records name no
// run id is skipped — there is no command to spell for a run nobody can
// address, and it answers for no other run's copy either.
func TestPriorRunUntriagedFindingsReachNeedsYou(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.PriorRecords = []Records{
		{
			Checkpoint: priorCheckpoint("run_prior", "failed"),
			Findings: []runstate.Finding{priorFinding(
				"c0ffee0000000000000000000000000000000000000000000000000000000001",
				"run_prior", "89m", "2026-09-27T02:00:00Z")},
		},
		{
			// A run the records cannot name: no triage command exists for it.
			Findings: []runstate.Finding{priorFinding(
				"c0ffee0000000000000000000000000000000000000000000000000000000002",
				"run_nameless", "152", "2026-09-26T02:00:00Z")},
		},
	}
	model := Build(src)

	finding := 0
	var attention *Attention
	for i := range model.Attention {
		if model.Attention[i].Kind != WaitFinding {
			continue
		}
		finding++
		if strings.Contains(model.Attention[i].What, "run_prior") {
			attention = &model.Attention[i]
		}
	}
	if attention == nil {
		t.Fatalf("the dead run's untriaged draft is not attention: %+v", model.Attention)
	}
	if finding != 1 {
		t.Errorf("the finding attention appears %d times, want once: a run the records cannot name must not "+
			"raise one beside it: %+v", finding, model.Attention)
	}
	if !attention.NeedsPerson {
		t.Error("a prior run's untriaged draft does not need a person")
	}
	if attention.UnblockCommand == nil ||
		*attention.UnblockCommand != "ticfac triage 2jn --run-id run_prior" {
		t.Errorf("the prior run's triage command is %+v, want the one addressed to its own store",
			attention.UnblockCommand)
	}
	if attention.Since == nil || *attention.Since != "2026-09-27T02:00:00Z" {
		t.Errorf("the prior run's findings attention reads since %+v, want the draft's own proposal",
			attention.Since)
	}
}

// TestANewerRunsFindingCopyAnswersForTheOlderRuns: a finding is one record
// across runs, keyed by content — the funnel's own identity — so the copies
// dedupe NEWEST-FIRST through needs-you. The newest run's own copy, whatever
// its status, answers for every older run's copy (adopted into the live run's
// store, or decided, its decision standing), and among prior runs the newest
// proposed copy is the one a person is asked to triage: triaging an older
// run's copy settles nothing the newer word still owns.
func TestANewerRunsFindingCopyAnswersForTheOlderRuns(t *testing.T) {
	t.Parallel()
	// The running fixture's own draft key: the newest run carries it
	// proposed in its own records.
	const newestKey = "46b634a4f894acc04534dd6e9b70b677d68c39f3b66b98e820613ac7dd8c6ce2"

	t.Run("the newest run's own copy owns the finding", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.PriorRecords = []Records{{
			Checkpoint: priorCheckpoint("run_prior", "failed"),
			Findings:   []runstate.Finding{priorFinding(newestKey, "run_prior", "89m", "2026-09-26T02:00:00Z")},
		}}
		model := Build(src)
		for _, a := range model.Attention {
			if a.Kind == WaitFinding {
				t.Errorf("an older run's copy of a finding the newest run owns raised attention: %+v", a)
			}
		}
	})

	t.Run("a newer prior run's decision stands", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.Records.Findings = nil // the newest run carries no copy of the key
		decided := priorFinding(newestKey, "run_decider", "89m", "2026-09-26T02:00:00Z")
		decided.Status = runstate.FindingDiscarded
		decided.TriagedAt = "2026-09-26T06:00:00Z"
		decided.TriagedBy = "an operator"
		src.PriorRecords = []Records{
			{ // oldest first: its proposed copy is the older word
				Checkpoint: priorCheckpoint("run_older", "failed"),
				Findings:   []runstate.Finding{priorFinding(newestKey, "run_older", "89m", "2026-09-26T01:00:00Z")},
			},
			{Checkpoint: priorCheckpoint("run_decider", "failed"), Findings: []runstate.Finding{decided}},
		}
		model := Build(src)
		for _, a := range model.Attention {
			if a.Kind == WaitFinding {
				t.Errorf("a decided finding raised attention from an older run's copy: %+v", a)
			}
		}
	})

	t.Run("the newest prior run's proposed copy is the one to triage", func(t *testing.T) {
		t.Parallel()
		src := runningEpicSources()
		src.Records.Findings = nil
		src.PriorRecords = []Records{
			{Checkpoint: priorCheckpoint("run_older", "failed"),
				Findings: []runstate.Finding{priorFinding(newestKey, "run_older", "89m", "2026-09-26T01:00:00Z")}},
			{Checkpoint: priorCheckpoint("run_newer", "failed"),
				Findings: []runstate.Finding{priorFinding(newestKey, "run_newer", "152", "2026-09-27T01:00:00Z")}},
		}
		model := Build(src)
		var attentions []Attention
		for _, a := range model.Attention {
			if a.Kind == WaitFinding {
				attentions = append(attentions, a)
			}
		}
		if len(attentions) != 1 || !strings.Contains(attentions[0].What, "run_newer") {
			t.Fatalf("the finding's attention is %+v, want one addressed to the newest prior run", attentions)
		}
		if attentions[0].UnblockCommand == nil ||
			*attentions[0].UnblockCommand != "ticfac triage 2jn --run-id run_newer" {
			t.Errorf("the command is %+v, want the newer prior run's own triage", attentions[0].UnblockCommand)
		}
	})
}

// TestADeadCloudRunResumesThroughTheFactory: the dead-run resume is named
// by the host the run lives on. A cloud run's resume is a new submission to
// its factory — `ticfac run <epic> --cloud` — because `run-epic` here would
// restart the epic LOCALLY, in the foreground, on the machine that happens
// to be reading it: the one command the overview and the watch point a
// person at must be the command that clears the stop the run is actually
// stopped in.
func TestADeadCloudRunResumesThroughTheFactory(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Host = HostCloud
	src.RunID = "run_1a2b3c4d5e6f"
	src.Liveness.Alive = false
	src.Liveness.State = "unknown"
	src.Liveness.Reason = "the factory's record carries no state for this run, so nothing claims it is alive"
	src.Liveness.Source = "workflow-record"
	src.Session = nil
	src.Standing = nil
	model := Build(src)

	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitDeadRun {
		t.Fatalf("a cloud run gone without a terminal word waits on %+v, want dead-run", model.WaitsOn)
	}
	if model.WaitsOn.UnblockCommand == nil || *model.WaitsOn.UnblockCommand != "ticfac run 2jn --cloud" {
		t.Errorf("the dead cloud run's unblock command is %+v, want ticfac run 2jn --cloud",
			model.WaitsOn.UnblockCommand)
	}
}

// TestAResumedRunWhoseFirstIncarnationFailedIsNotCompleted: a failed run is
// resumable under the same run id, so its feed carries the failed
// incarnation's run_finished beside the resumed run's own lines. The model
// must not read that first ending as the run's completion (tick bkg): the
// merge wait is for a run that finished its own work, and a run that stopped
// failed and was resumed has just begun again.
func TestAResumedRunWhoseFirstIncarnationFailedIsNotCompleted(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	// The first incarnation's ending and the resume, placed chronologically
	// inside the fixture's own feed: the failed run_finished stands before the
	// resume line, and the work that stands now comes after both.
	feed := []runfeed.Event{}
	for _, e := range src.Feed {
		if e.Stage == reconcile.StageResumedAutomatically {
			feed = append(feed,
				runfeed.NewEvent(testNow.Add(-70*time.Minute), "epic-2jn", "", nil,
					reconcile.StageRunFinished, "failed: 6dh did not pass: the run stopped rather than integrating over an unproven change"),
				runfeed.NewEvent(testNow.Add(-65*time.Minute), "epic-2jn", "", nil,
					reconcile.StageResumed, "the run stopped at failed and is resumed under the same run id: 6dh did not pass"),
			)
		}
		feed = append(feed, e)
	}
	src.Feed = feed
	// The open PR is what makes the defect bite: a run read as completed
	// with an open PR surfaces the merge wait — a person's — for work that is
	// still going.
	src.CI = &CIInput{
		State: "green",
		PR: &PR{Number: 12, URL: "https://github.com/example/ticfac/pull/12",
			HeadRef: "epic/2jn", HeadSHA: "9f2ab", BaseRef: "main"},
		Checks: []CheckState{{Name: "go", Status: "completed", Conclusion: "success"}},
	}
	model := Build(src)

	if model.Lifecycle.Phase != PhaseWaves {
		t.Errorf("a resumed run mid-wave reads phase %q, want waves: the failed incarnation's run_finished is not the run's completion", model.Lifecycle.Phase)
	}
	for _, p := range model.Lifecycle.Phases {
		if p.Phase == PhaseMerge && p.State == PhaseStateActive {
			t.Errorf("the merge phase reads %q for a run that has not finished its own work", p.State)
		}
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitWorkers {
		t.Errorf("the resumed run waits on %+v, want its live workers", model.WaitsOn)
	}
	for _, a := range model.Attention {
		if a.Kind == WaitMerge {
			t.Errorf("a run that stopped failed and was resumed raises the merge wait: %+v", a)
		}
	}
}

// TestTheFeedLastRunFinishedLineIsTheRunsOwnWord: where the records could
// not be read and the feed is all the model has, the run's own word is the
// LAST run_finished line — never the first one, and never a line whose own
// detail names a failure. A completed ending after a failed one is the
// resumed run's; a failed one alone is an ending that is not a completion.
func TestTheFeedLastRunFinishedLineIsTheRunsOwnWord(t *testing.T) {
	t.Parallel()

	mergeWait := func(feed []runfeed.Event) *Wait {
		src := runningEpicSources()
		// The records could not be read at all: the feed is the only writer
		// the model has, and a cloud record's finished vocabulary is what the
		// liveness answer carries.
		src.Records = &Records{}
		src.Feed = feed
		src.Standing, src.StandingRead, src.Session = nil, false, nil
		src.Liveness = LivenessInput{
			Alive: false, State: "completed",
			Reason: "the factory's record says completed — written by the Workflow, and a finished run is not alive",
			Source: "workflow-record",
		}
		src.CI = &CIInput{
			State: "green",
			PR: &PR{Number: 12, URL: "https://github.com/example/ticfac/pull/12",
				HeadRef: "epic/2jn", HeadSHA: "9f2ab", BaseRef: "main"},
			Checks: []CheckState{{Name: "go", Status: "completed", Conclusion: "success"}},
		}
		model := Build(src)
		return model.WaitsOn
	}

	failed := runfeed.NewEvent(testNow.Add(-2*time.Hour), "epic-2jn", "", nil,
		reconcile.StageRunFinished, "failed: 6dh did not pass: the run stopped rather than integrating over an unproven change")
	completed := runfeed.NewEvent(testNow.Add(-1*time.Hour), "epic-2jn", "", nil,
		reconcile.StageRunFinished, "completed: every tick of 2jn is closed behind the integrated gate")

	if w := mergeWait([]runfeed.Event{failed, completed}); w == nil || w.Kind != WaitMerge {
		t.Errorf("a run whose last run_finished names its completion waits on %+v, want the merge", w)
	}
	// A failed ending is not a completion (the merge stays unstated), and
	// since the run is not going it needs the one thing nothing makes for
	// itself: the resume — the dead-run wait the ended run now states
	// (tick jkb), with the run's own failure worded once.
	if w := mergeWait([]runfeed.Event{failed}); w == nil || w.Kind != WaitDeadRun ||
		!strings.Contains(w.What, "failed") || w.NeedsPerson != true ||
		w.UnblockCommand == nil || *w.UnblockCommand != "ticfac run-epic 2jn" {
		t.Errorf("a run whose only run_finished names a failure waits on %+v, want its own resume: a failed ending is not a completion, and only a person starts it again", w)
	}
}

// TestTheCloseoutCIIHoldIsAWaitNobodyAlarms: the close-out's own typed line
// is a wait the run watches itself — needs_person false, the state a
// renderer shows, not an alarm it raises.
func TestTheCloseoutCIIHoldIsAWaitNobodyAlarms(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Standing = nil
	src.Records.Checkpoint.State = "publishing"
	src.Feed = append(src.Feed, runfeed.NewEvent(testNow.Add(-5*time.Minute), "epic-2jn", "", nil,
		reconcile.StageCloseoutHeld, "the close-out waits for CI green on the PR"))
	model := Build(src)

	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitCI {
		t.Fatalf("a run held on CI waits on %+v, want ci", model.WaitsOn)
	}
	if model.WaitsOn.NeedsPerson {
		t.Error("the close-out's CI gate is a wait the run itself watches, not a person's alarm")
	}
	if len(model.Attention) != 0 {
		t.Errorf("a CI hold raised attention it must not: %+v", model.Attention)
	}
}

// TestACloudRunStatesItsWorkersAsUnreadable: a cloud run's worktrees are not
// on this machine — null, which is a different claim from "none stand".
func TestACloudRunStatesItsWorkersAsUnreadable(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	src.Host = HostCloud
	src.RunID = "run_1a2b3c4d5e6f"
	src.Standing, src.StandingRead, src.Session = nil, false, nil
	model := Build(src)

	if model.Host != HostCloud {
		t.Errorf("the model names host %q, want cloud", model.Host)
	}
	if model.Workers != nil {
		t.Errorf("a cloud run's workers read %+v, want null: the census cannot be taken here", model.Workers)
	}
	if model.EpicID != "2jn" {
		t.Errorf("the model derives epic %q, want 2jn from the records", model.EpicID)
	}
}

// TestRemainingIsStatedOnlyWhereMeasured: three closed ticks with measured
// durations support an estimate for the open ones; two do not.
func TestRemainingIsStatedOnlyWhereMeasured(t *testing.T) {
	t.Parallel()
	src := runningEpicSources()
	// Close the whole of wave 1 and give every closed tick a measured gate,
	// so three closes support the estimate and ticks stay open behind them.
	closed := []runstate.TickState{
		{TickID: "nwj", State: "closed", Attempt: 1},
		{TickID: "6dh", State: "closed", Attempt: 3},
	}
	src.Records.Checkpoint.Ticks = append(closed, runstate.TickState{TickID: "89m", State: "closed", Attempt: 4})
	src.Records.Attempts = append(src.Records.Attempts,
		attemptMarker(4, "89m", "2026-09-27T04:40:00Z", "strong", "m", "local-subprocess"))
	src.Records.Evidence = append(src.Records.Evidence,
		evidence("gate-6dh-3-go", "go", "6dh", 3, "pass", "integrated", "aa", "2026-09-27T04:50:00Z", "2026-09-27T05:00:00Z"),
		evidence("gate-89m-4-go", "go", "89m", 4, "pass", "integrated", "bb", "2026-09-27T05:10:00Z", "2026-09-27T05:20:00Z"))
	src.Standing = nil
	model := Build(src)

	if model.Remaining == nil {
		t.Fatal("three measured closes support an estimate and the model states none")
	}
	// Durations: nwj 48m18s, 6dh 51m52s, 89m 40m — median 2898s, three open.
	if model.Remaining.ApproximateSeconds != 2898*3 {
		t.Errorf("the estimate is %ds, want the median 2898s times 3 open ticks", model.Remaining.ApproximateSeconds)
	}
	if !strings.Contains(model.Remaining.Basis, "approximate") {
		t.Errorf("the estimate's basis %q does not name it approximate", model.Remaining.Basis)
	}

	// Two measured closes support nothing.
	src.Records.Checkpoint.Ticks = closed
	model = Build(src)
	if model.Remaining != nil {
		t.Errorf("two measured closes do not support an estimate, and the model states %v", model.Remaining)
	}
}

// TestRecordsFromDirReadsWhatACheckoutHolds: the directory reader is the
// store's fallback and the fixtures' reader, and it reads — strictly — every
// record kind the model consumes.
func TestRecordsFromDirReadsWhatACheckoutHolds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(rel string, raw string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	src := runningEpicSources()
	recs := src.Records
	must := func(v any, rel string) {
		t.Helper()
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", rel, err)
		}
		write(rel, string(raw))
	}
	must(recs.Checkpoint, "checkpoint.json")
	for _, a := range recs.Attempts {
		must(a, fmt.Sprintf("attempts/%d.json", a.Attempt))
	}
	must(recs.Decisions[0], "decisions/1.json")
	for _, e := range recs.Evidence {
		must(e, fmt.Sprintf("evidence/%s.json", e.Key))
	}
	must(recs.Absorptions[0], fmt.Sprintf("absorptions/%s.json", recs.Absorptions[0].Key))
	must(recs.Findings[0], fmt.Sprintf("findings/%s.json", recs.Findings[0].Key))

	read, err := RecordsFromDir(dir)
	if err != nil {
		t.Fatalf("the directory reader refused the fixture: %v", err)
	}
	if read.Checkpoint == nil || read.Checkpoint.EpicID != "2jn" {
		t.Errorf("the checkpoint did not read back: %+v", read.Checkpoint)
	}
	if len(read.Attempts) != 3 || len(read.Decisions) != 1 || len(read.Evidence) != 2 ||
		len(read.Absorptions) != 1 || len(read.Findings) != 1 {
		t.Errorf("the records read back as %d attempts, %d decisions, %d evidence, %d absorptions, %d findings",
			len(read.Attempts), len(read.Decisions), len(read.Evidence), len(read.Absorptions), len(read.Findings))
	}
	if read.Attempts[0].Attempt != 1 {
		t.Errorf("the attempts did not read back in number order: %+v", read.Attempts)
	}

	// A record the model does not know is refused, not forgiven: a reader
	// that silently skipped drifted records would be a reader whose drift
	// nobody can see.
	write("attempts/9.json", `{"schema_version": 3, "attempt": 9, "novelty": true}`)
	if _, err := RecordsFromDir(dir); err == nil {
		t.Error("an unknown field in a run record was silently forgiven")
	}
}

// TestAHealthyUnmeasurableEpicAnswersNull: a run with no records, no feed
// and no tracker — nothing exists to state — answers liveness and nulls
// everywhere else, never a guess.
func TestAHealthyUnmeasurableEpicAnswersNull(t *testing.T) {
	t.Parallel()
	model := Build(Sources{
		Now:     testNow,
		RunID:   "epic-2jn",
		Records: &Records{},
		Liveness: LivenessInput{
			Alive: false, State: "not_running",
			Reason: "no process holds this run: the last one released it, or none has claimed it here",
			Source: "run.pid",
		},
	})
	if model.Waves != nil || model.Workers != nil {
		t.Errorf("a run with nothing to read states waves %+v and workers %+v, want null and null",
			model.Waves, model.Workers)
	}
	if model.WaitsOn == nil || model.WaitsOn.Kind != WaitDeadRun {
		t.Errorf("a gone run with no terminal record waits on %+v, want dead-run", model.WaitsOn)
	}
	if model.Lifecycle.Phase != PhasePlan {
		t.Errorf("a run that never dispatched reads phase %q, want plan", model.Lifecycle.Phase)
	}
}
