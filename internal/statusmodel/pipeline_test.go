package statusmodel

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/schema"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The pipeline cell (epic hn6, wave 2 — tick 3gk): every rule decorateTicks
// derives, each at the hand of one fixture epic that states exactly what the
// rule reads. The table is the tick's own nine cases (a–i) plus the arms the
// nine do not reach on their own (a close the feed lost its gate line for, a
// gate the evidence refused before any line said so, a redispatch the
// records have not read yet, a hold that names the tick and one that does
// not) — the same table, because they are the same rules. Every case also
// holds the builder to the contract: a Build output must validate against
// the same schema the bundle pins, the binding TestTheContractBindsTheBuilder
// holds for the running-epic fixture.

// pipeProvenance is one attempt's provenance over the fixture's own epic.
func pipeProvenance(tickID string, attempt int, tier string) runstate.Provenance {
	return runstate.Provenance{
		RunID:     "epic-hn6",
		TickID:    tickPtr(tickID),
		Attempt:   intPtr(attempt),
		SourceRef: "refs/heads/epic/hn6",
		SourceSHA: "5c4b2a1d3e0f8697abc30d1e2f3a4b5c6d7e8f90",
		Phase:     runstate.PhaseWorker,
		Executor:  tickPtr("local-subprocess"),
		Role:      tickPtr("implement-tick"),
		Tier:      &tier,
		Model:     tickPtr("cloudflare-workers-ai/@cf/zai-org/glm-5.3"),
	}
}

// pipeMarker is one dispatch marker.
func pipeMarker(n int, tickID, at, tier string) runstate.Attempt {
	return runstate.Attempt{
		SchemaVersion: runstate.SchemaVersion,
		Attempt:       n,
		TickID:        tickID,
		DispatchedAt:  at,
		JobHandle:     map[string]any{"executor": "local-subprocess"},
		Provenance:    pipeProvenance(tickID, n, tier),
	}
}

// pipeEvidence is the integrated gate's record for one (tick, attempt).
func pipeEvidence(key, tickID string, attempt int, result, finishedAt string) runstate.Evidence {
	return runstate.Evidence{
		SchemaVersion: runstate.SchemaVersion,
		Key:           key,
		Provenance: runstate.Provenance{
			RunID:          "epic-hn6",
			TickID:         tickPtr(tickID),
			Attempt:        intPtr(attempt),
			SourceRef:      "refs/heads/epic/hn6",
			SourceSHA:      "5c4b2a1d3e0f8697abc30d1e2f3a4b5c6d7e8f90",
			IntegrationRef: tickPtr("refs/heads/epic/hn6"),
			Phase:          runstate.PhaseIntegrated,
			Executor:       tickPtr("local-subprocess"),
			Tier:           tickPtr("strong"),
		},
		Check:      runstate.Check{ID: "go", Kind: "command"},
		StartedAt:  "2026-09-27T04:00:00Z",
		FinishedAt: finishedAt,
		Result:     result,
		Acceptance: "required",
		Output: runstate.Output{Inline: &runstate.InlineOutput{
			Mode: "inline", Stdout: "ok\n", Truncated: false, Redacted: true, MaxBytes: 1024,
		}},
		ContentDigest:  "b3f4c5d6e7",
		PersistenceURI: "file:///dev/null",
	}
}

// pipeGraph is one epic, one wave, the tasks the case states.
func pipeGraph(tasks ...tk.GraphTask) *tk.Graph {
	return &tk.Graph{
		Epic: tk.GraphEpic{
			ID:    "hn6",
			Title: "watch becomes a dashboard",
			Gloss: "watch as a dashboard",
		},
		Waves: []tk.GraphWave{{Wave: 1, Tasks: tasks}},
	}
}

// pipeTask is one graph task of the fixture's epic.
func pipeTask(id, parent, role string) tk.GraphTask {
	return tk.GraphTask{ID: id, Title: "the " + id + " tick", Status: "open", Parent: parent, Role: role}
}

// pipeCheckpoint is the run's own word about its ticks.
func pipeCheckpoint(rows ...runstate.TickState) *runstate.Checkpoint {
	provenance := pipeProvenance("", 0, "strong")
	provenance.TickID, provenance.Attempt, provenance.Role = nil, nil, nil
	return &runstate.Checkpoint{
		SchemaVersion: runstate.SchemaVersion,
		RunID:         "epic-hn6",
		EpicID:        "hn6",
		Sequence:      1,
		State:         "running",
		Reason:        "the fixture runs",
		UpdatedAt:     testNow.Add(-10 * time.Minute).Format(time.RFC3339),
		Ticks:         rows,
		Provenance:    provenance,
	}
}

// pipeLine is one feed line about one (tick, attempt).
func pipeLine(at time.Time, tickID string, attempt int, stage, detail string) runfeed.Event {
	return runfeed.NewEvent(at, "epic-hn6", tickID, intPtr(attempt), stage, detail)
}

// pipeLineAtTickScope is one run-level feed line (no tick, no attempt).
func pipeLineAtTickScope(at time.Time, stage, detail string) runfeed.Event {
	return runfeed.NewEvent(at, "epic-hn6", "", nil, stage, detail)
}

// pipeSources assembles one case's sources: the fixture epic, its records
// and feed, alive, with no census unless the case states one.
func pipeSources(graph *tk.Graph, recs *Records, feed []runfeed.Event) Sources {
	return Sources{
		Now:     testNow,
		RunID:   "epic-hn6",
		Host:    HostLocal,
		EpicID:  "hn6",
		Graph:   graph,
		Records: recs,
		Feed:    feed,
		Liveness: LivenessInput{
			Alive: true, State: "alive",
			Reason: "pid 4242 has been running since 2026-09-27T03:00:00Z",
			Source: "run.pid",
		},
	}
}

// pipeStanding is the census: the attempts whose worktrees stand.
func pipeStanding(entries ...runprogress.Attempt) ([]runprogress.Attempt, bool) {
	return entries, true
}

// pipeModel builds one case's sources and holds the answer to the contract:
// a model the builder produces must validate against the bundle's schema,
// whatever the fixture stated.
func pipeModel(t *testing.T, src Sources) Model {
	t.Helper()
	model := Build(src)
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("the built model does not marshal: %v", err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	record, defs, _ := bundleFixture(t)
	if problems := schema.Validate(record, defs, document); len(problems) > 0 {
		t.Errorf("a model the builder produced is refused by the contract:\n%s\nmodel:\n%s",
			strings.Join(problems, "\n"), raw)
	}
	return model
}

// pipeTick finds one tick of the built model by its id.
func pipeTick(t *testing.T, m Model, tickID string) Tick {
	t.Helper()
	for _, w := range deref(m.Waves) {
		for _, tick := range w.Ticks {
			if tick.TickID == tickID {
				return tick
			}
		}
	}
	t.Fatalf("the fixture's graph carries no tick %s", tickID)
	return Tick{}
}

// pipeCell is one tick's cell as a stage→state map.
func pipeCell(tick Tick) map[string]string {
	out := map[string]string{}
	for _, stage := range tick.Pipeline {
		out[stage.Stage] = stage.State
	}
	return out
}

// assertCell holds one tick's whole cell to exactly the wanted states —
// every stage named, nothing extra, the same cell a renderer draws from.
func assertCell(t *testing.T, tick Tick, want map[string]string) {
	t.Helper()
	got := pipeCell(tick)
	if len(got) != len(want) {
		t.Fatalf("%s's pipeline cell is %s, want exactly the %d stages %v",
			tick.TickID, cellOf(tick.Pipeline), len(want), want)
	}
	for stage, state := range want {
		if got[stage] != state {
			t.Errorf("%s's stage %s reads %q, want %q (cell %s)",
				tick.TickID, stage, got[stage], state, cellOf(tick.Pipeline))
		}
	}
}

// assertStageList holds the cell to the role's own stage list, in order.
func assertStageList(t *testing.T, tick Tick, want []string) {
	t.Helper()
	if !stagesOf(tick.Pipeline, want) {
		t.Errorf("%s's pipeline stages are %s, want %v", tick.TickID, cellOf(tick.Pipeline), want)
	}
}

// strOf reads a nullable string, for asserting against it.
func strOf(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}

func TestPipelineDerivesEachTicksCell(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  Sources
		want func(t *testing.T, m Model)
	}{{
		// (a) A closed implement tick: claim, work, gate and merged all done,
		// the duration from its first dispatch to its gate evidence's
		// finish, and the try closed with nothing left to answer.
		name: "closed implement tick",
		src: pipeSources(
			pipeGraph(pipeTask("ab1", "hn6", ""), pipeTask("ab2", "hn6", "")),
			&Records{
				Checkpoint: pipeCheckpoint(
					runstate.TickState{TickID: "ab1", State: "closed", Attempt: 1},
					runstate.TickState{TickID: "ab2", State: "closed", Attempt: 1},
				),
				Attempts: []runstate.Attempt{
					pipeMarker(1, "ab1", "2026-09-27T03:19:05Z", "strong"),
					pipeMarker(1, "ab2", "2026-09-27T04:00:00Z", "frontier"),
				},
				Evidence: []runstate.Evidence{
					pipeEvidence("gate-ab1-1", "ab1", 1, "pass", "2026-09-27T04:07:23Z"),
				},
			},
			[]runfeed.Event{
				pipeLine(testNow.Add(-100*time.Minute), "ab1", 1, reconcile.StageCollected, "ab1 try 1 reported"),
				pipeLine(testNow.Add(-90*time.Minute), "ab1", 1, reconcile.StageGateStarted, "the integrated gate started (go)"),
				pipeLine(testNow.Add(-82*time.Minute), "ab1", 1, reconcile.StageGatePassed, "the integrated gate passed on 5c4b2a1"),
				pipeLine(testNow.Add(-60*time.Minute), "ab2", 1, reconcile.StageClosed, "ab2 closed behind its gate"),
			}),
		want: func(t *testing.T, m Model) {
			ab1 := pipeTick(t, m, "ab1")
			assertCell(t, ab1, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageGate: StageStateDone, StageMerged: StageStateDone,
			})
			if ab1.DurationSeconds == nil || *ab1.DurationSeconds != 2898 {
				t.Errorf("ab1's duration is %v, want 2898 (03:19:05 to its gate evidence's 04:07:23)", ab1.DurationSeconds)
			}
			if len(ab1.Tries) != 1 {
				t.Fatalf("ab1 has %d tries, want 1", len(ab1.Tries))
			}
			try := ab1.Tries[0]
			if try.Tier == nil || *try.Tier != "strong" {
				t.Errorf("ab1's try tier is %v, want the marker's own strong", try.Tier)
			}
			if try.Reason != nil || try.NextStep != nil {
				t.Errorf("a closed try carries reason %q and next_step %q, want both null",
					strOf(try.Reason), strOf(try.NextStep))
			}
			if len(ab1.Findings) != 0 {
				t.Errorf("ab1's findings are %+v, want the empty list", ab1.Findings)
			}
			// ab2's feed lost its gate lines: the close itself is the
			// durable half of gate_passed, and the `closed` line the only
			// stated finish — the cell still fills whole and the duration
			// still measures to something the records state.
			ab2 := pipeTick(t, m, "ab2")
			assertCell(t, ab2, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageGate: StageStateDone, StageMerged: StageStateDone,
			})
			if ab2.DurationSeconds == nil || *ab2.DurationSeconds != 1800 {
				t.Errorf("ab2's duration is %v, want 1800 (04:00:00 to its `closed` line's 04:30:00)", ab2.DurationSeconds)
			}
		},
	}, {
		// (b) In flight with gate_started: the claim and the work behind it,
		// the gate the frontier, the merge not reached — and the duration
		// running to the model's now, because the tick is open.
		name: "in flight with gate started",
		src: func() Sources {
			src := pipeSources(
				pipeGraph(pipeTask("cd2", "hn6", "")),
				&Records{
					Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "cd2", State: "dispatched", Attempt: 2}),
					Attempts: []runstate.Attempt{
						pipeMarker(1, "cd2", "2026-09-27T04:00:00Z", "strong"),
						pipeMarker(2, "cd2", "2026-09-27T04:10:00Z", "frontier"),
					},
				},
				[]runfeed.Event{
					pipeLine(testNow.Add(-50*time.Minute), "cd2", 2, reconcile.StageCollected, "cd2 try 2 reported"),
					pipeLine(testNow.Add(-45*time.Minute), "cd2", 2, reconcile.StageGateStarted, "the integrated gate started (go)"),
				})
			src.Standing, src.StandingRead = pipeStanding(runprogress.Attempt{
				TickID: "cd2", Attempt: 2,
				Branch:   "refs/heads/ticfac/run-epic-hn6/tick-cd2/attempt-2",
				Worktree: "/worktrees/run-epic-hn6/tick-cd2/attempt-2",
			})
			return src
		}(),
		want: func(t *testing.T, m Model) {
			cd2 := pipeTick(t, m, "cd2")
			assertCell(t, cd2, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageGate: StageStateActive, StageMerged: StageStatePending,
			})
			if cd2.DurationSeconds == nil || *cd2.DurationSeconds != 5400 {
				t.Errorf("cd2's duration is %v, want 5400 (its first dispatch 04:00:00 to the model's now, while it is open)", cd2.DurationSeconds)
			}
			if len(cd2.Tries) != 2 {
				t.Fatalf("cd2 has %d tries, want 2", len(cd2.Tries))
			}
			for _, try := range cd2.Tries {
				if try.Reason != nil || try.NextStep != nil {
					t.Errorf("cd2's try %d carries reason %q and next_step %q, want both null: one try was superseded and one is still in flight",
						try.Try, strOf(try.Reason), strOf(try.NextStep))
				}
			}
			if tier := cd2.Tries[1].Tier; tier == nil || *tier != "frontier" {
				t.Errorf("cd2's current try tier is %v, want the marker's own frontier", tier)
			}
		},
	}, {
		// (c) Rejected on gate_failed with no redispatch: the gate the
		// refused stage, the reason the run's own latest line for that
		// (tick, attempt) — cut to its first 160 characters on a word
		// boundary — and the next step the run's ladder names, because
		// nothing else has happened yet. eg4 reaches the same failed gate
		// through the other arm: a rejection whose own detail begins the
		// gate's refusal word.
		name: "rejected on gate failed",
		src: pipeSources(
			pipeGraph(pipeTask("ef3", "hn6", ""), pipeTask("eg4", "hn6", "")),
			&Records{
				Checkpoint: pipeCheckpoint(
					runstate.TickState{TickID: "ef3", State: "rejected", Attempt: 1},
					runstate.TickState{TickID: "eg4", State: "rejected", Attempt: 1},
				),
				Attempts: []runstate.Attempt{
					pipeMarker(1, "ef3", "2026-09-27T04:00:00Z", "strong"),
					pipeMarker(1, "eg4", "2026-09-27T04:00:00Z", "frontier"),
				},
			},
			[]runfeed.Event{
				pipeLine(testNow.Add(-70*time.Minute), "ef3", 1, reconcile.StageCollected, "ef3 try 1 reported"),
				pipeLine(testNow.Add(-60*time.Minute), "ef3", 1, reconcile.StageGateStarted, "the integrated gate started (go)"),
				pipeLine(testNow.Add(-59*time.Minute), "ef3", 1, reconcile.StageGateFailed, "the integrated gate did not pass: go"),
				pipeLine(testNow.Add(-58*time.Minute), "ef3", 1, reconcile.StageRejected,
					"gate_failed: "+strings.Repeat("word ", 100)),
				pipeLine(testNow.Add(-70*time.Minute), "eg4", 1, reconcile.StageCollected, "eg4 try 1 reported"),
				pipeLine(testNow.Add(-58*time.Minute), "eg4", 1, reconcile.StageRejected,
					"gate_failed: the integrated gate on 9a1c3f2 did not pass for eg4: gofmt drifted in two files"),
			}),
		want: func(t *testing.T, m Model) {
			ef3 := pipeTick(t, m, "ef3")
			assertCell(t, ef3, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageGate: StageStateFailed, StageMerged: StageStatePending,
			})
			if len(ef3.Tries) != 1 {
				t.Fatalf("ef3 has %d tries, want 1", len(ef3.Tries))
			}
			try := ef3.Tries[0]
			wantReason := "gate_failed: " + strings.Repeat("word ", 28) + "word"
			if try.Reason == nil || *try.Reason != wantReason {
				t.Errorf("ef3's rejected try reason is %q, want the line's first 160 characters on a word boundary %q",
					strOf(try.Reason), wantReason)
			}
			if try.NextStep == nil || *try.NextStep != "the run will retry or escalate the tier" {
				t.Errorf("ef3's rejected try next_step is %q, want \"the run will retry or escalate the tier\"", strOf(try.NextStep))
			}
			eg4 := pipeTick(t, m, "eg4")
			assertCell(t, eg4, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageGate: StageStateFailed, StageMerged: StageStatePending,
			})
			if reason := eg4.Tries[0].Reason; reason == nil ||
				*reason != "gate_failed: the integrated gate on 9a1c3f2 did not pass for eg4: gofmt drifted in two files" {
				t.Errorf("eg4's rejected try reason is %q, want its rejection line's own detail", strOf(reason))
			}
		},
	}, {
		// (d) Rejected then redispatched: the first try's next step is null
		// (the try after it is the answer, sitting in the same history) and
		// the last try's is null too, because it is in flight. gi5 is the
		// honest silence: rejected with no line at all, the reason is null —
		// never invented — while the next step still names the ladder.
		name: "rejected then redispatched",
		src: func() Sources {
			src := pipeSources(
				pipeGraph(pipeTask("gh4", "hn6", ""), pipeTask("gi5", "hn6", "")),
				&Records{
					Checkpoint: pipeCheckpoint(
						runstate.TickState{TickID: "gh4", State: "dispatched", Attempt: 2},
						runstate.TickState{TickID: "gi5", State: "rejected", Attempt: 1},
					),
					Attempts: []runstate.Attempt{
						pipeMarker(1, "gh4", "2026-09-27T04:00:00Z", "strong"),
						pipeMarker(2, "gh4", "2026-09-27T04:30:00Z", "frontier"),
						pipeMarker(1, "gi5", "2026-09-27T04:00:00Z", "strong"),
					},
					Evidence: []runstate.Evidence{
						// The gate's own record of the first try's refusal: what
						// makes that try's durable outcome gate-failed, as a
						// gate-refused attempt always is in the records.
						pipeEvidence("gate-gh4-1", "gh4", 1, "fail", "2026-09-27T04:21:00Z"),
					},
				},
				[]runfeed.Event{
					pipeLine(testNow.Add(-70*time.Minute), "gh4", 1, reconcile.StageGateFailed, "the integrated gate did not pass: go"),
					pipeLine(testNow.Add(-69*time.Minute), "gh4", 1, reconcile.StageRejected,
						"gate_failed: the integrated gate on 9a1c3f2 did not pass for gh4: go"),
					pipeLine(testNow.Add(-60*time.Minute), "gh4", 2, reconcile.StageDispatched,
						"gh4 try 2 dispatched (run dispatch #2, branch ticfac/run-epic-hn6/tick-gh4/attempt-2)"),
				})
			src.Standing, src.StandingRead = pipeStanding(runprogress.Attempt{
				TickID: "gh4", Attempt: 2,
				Branch:   "refs/heads/ticfac/run-epic-hn6/tick-gh4/attempt-2",
				Worktree: "/worktrees/run-epic-hn6/tick-gh4/attempt-2",
			})
			return src
		}(),
		want: func(t *testing.T, m Model) {
			gh4 := pipeTick(t, m, "gh4")
			if len(gh4.Tries) != 2 {
				t.Fatalf("gh4 has %d tries, want 2", len(gh4.Tries))
			}
			if reason := gh4.Tries[0].Reason; reason == nil ||
				*reason != "gate_failed: the integrated gate on 9a1c3f2 did not pass for gh4: go" {
				t.Errorf("gh4's rejected try reason is %q, want its rejection line's own detail", strOf(reason))
			}
			if step := gh4.Tries[0].NextStep; step != nil {
				t.Errorf("gh4's first try carries next_step %q, want null: the try after it answers it", *step)
			}
			if step := gh4.Tries[1].NextStep; step != nil {
				t.Errorf("gh4's last try carries next_step %q, want null: it is in flight", *step)
			}
			assertCell(t, gh4, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateActive,
				StageGate: StageStatePending, StageMerged: StageStatePending,
			})
			gi5 := pipeTick(t, m, "gi5")
			if reason := gi5.Tries[0].Reason; reason != nil {
				t.Errorf("gi5's rejected try carries reason %q, want null: no line states why, and a reason is never invented", *reason)
			}
			if step := gi5.Tries[0].NextStep; step == nil || *step != "the run will retry or escalate the tier" {
				t.Errorf("gi5's rejected try next_step is %q, want \"the run will retry or escalate the tier\"", strOf(step))
			}
		},
	}, {
		// (e) A close-out during the ci phase: the claim and the work behind
		// it, the ci gate the frontier, the close not reached. The lifecycle
		// is built after the ticks are decorated, so the ci stage derives the
		// phase over the same inputs buildLifecycle reads it from — and the
		// two cannot disagree.
		name: "closeout during ci",
		src: pipeSources(
			pipeGraph(pipeTask("kl5", "hn6", "closeout")),
			&Records{
				Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "kl5", State: "reported", Attempt: 1}),
				Attempts:   []runstate.Attempt{pipeMarker(1, "kl5", "2026-09-27T04:00:00Z", "closeout")},
			},
			[]runfeed.Event{
				pipeLine(testNow.Add(-70*time.Minute), "kl5", 1, reconcile.StageCollected, "the closeout job reported"),
				pipeLineAtTickScope(testNow.Add(-50*time.Minute), reconcile.StageCloseoutHeld, "the close-out waits for CI green on the PR"),
			}),
		want: func(t *testing.T, m Model) {
			kl5 := pipeTick(t, m, "kl5")
			assertStageList(t, kl5, PipelineCloseout)
			assertCell(t, kl5, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageCI: StageStateActive, StageClosed: StageStatePending,
			})
		},
	}, {
		// (f) A review tick shows its own stages — claim, review, closed —
		// with the review stage reading the work's own derivation.
		name: "review tick",
		src: func() Sources {
			src := pipeSources(
				pipeGraph(pipeTask("mn6", "hn6", "review")),
				&Records{
					Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "mn6", State: "dispatched", Attempt: 1}),
					Attempts:   []runstate.Attempt{pipeMarker(1, "mn6", "2026-09-27T04:00:00Z", "strong")},
				},
				nil)
			src.Standing, src.StandingRead = pipeStanding(runprogress.Attempt{
				TickID: "mn6", Attempt: 1,
				Branch:   "refs/heads/ticfac/run-epic-hn6/tick-mn6/attempt-1",
				Worktree: "/worktrees/run-epic-hn6/tick-mn6/attempt-1",
			})
			return src
		}(),
		want: func(t *testing.T, m Model) {
			mn6 := pipeTick(t, m, "mn6")
			assertStageList(t, mn6, PipelineReview)
			assertCell(t, mn6, map[string]string{
				StageClaim: StageStateDone, StageReview: StageStateActive,
				StageClosed: StageStatePending,
			})
		},
	}, {
		// (g) An absorbed tick indents under the tick its absorption names —
		// the dispatch whose findings were collected is the tick whose
		// worker reported the finding the promotion turned into the row —
		// while a task whose own graph parent is a real tick keeps it, and a
		// direct child of the epic states null.
		name: "absorbed repair tick",
		src: pipeSources(
			pipeGraph(
				pipeTask("op7", "hn6", ""),
				pipeTask("qr8", "hn6", ""),
				pipeTask("st9", "op7", ""),
			),
			&Records{
				Absorptions: []runstate.Absorption{{
					SchemaVersion: runstate.SchemaVersion,
					Key:           "kf0f1e2d3c4b5a697869584736251400f1e2d3c4b5a697869584736251400f1e",
					TickID:        "qr8",
					Gating:        true,
					Basis:         runstate.AbsorptionPredicted,
					Reason:        "the finding breaks the epic's own done item A3",
					Placement:     runstate.AbsorptionBeforeReview,
					DecidedAt:     "2026-09-27T04:05:49Z",
					Provenance:    pipeProvenance("op7", 2, "strong"),
				}},
				Findings: []runstate.Finding{{
					SchemaVersion:  runstate.SchemaVersion,
					Key:            "kf0f1e2d3c4b5a697869584736251400f1e2d3c4b5a697869584736251400f1e",
					Source:         "worker-report",
					DiscoveredFrom: "run-epic-hn6/tick-op7/attempt-2",
					Kind:           "defect",
					Title:          "op7's own worker drafted the finding the run absorbed",
					Body:           "the body",
					Severity:       "medium",
					TickID:         "op7",
					Attempt:        2,
					Status:         runstate.FindingPromoted,
					ProposedAt:     "2026-09-27T04:05:54Z",
					TriagedAt:      "2026-09-27T04:05:49Z",
					TriagedBy:      "ticfac run epic-hn6",
					PromotedAs:     "qr8",
					Provenance:     pipeProvenance("op7", 2, "strong"),
				}},
			},
			nil),
		want: func(t *testing.T, m Model) {
			qr8 := pipeTick(t, m, "qr8")
			if qr8.ParentTickID == nil || *qr8.ParentTickID != "op7" {
				t.Errorf("the absorbed tick qr8's parent is %q, want the source tick its absorption names (op7)", strOf(qr8.ParentTickID))
			}
			if !qr8.Absorbed {
				t.Error("qr8 is not marked absorbed, want the row the run itself created")
			}
			st9 := pipeTick(t, m, "st9")
			if st9.ParentTickID == nil || *st9.ParentTickID != "op7" {
				t.Errorf("st9's parent is %q, want its graph task's own op7", strOf(st9.ParentTickID))
			}
			op7 := pipeTick(t, m, "op7")
			if op7.ParentTickID != nil {
				t.Errorf("op7's parent is %q, want null: it is a direct child of the epic", *op7.ParentTickID)
			}
			if len(op7.Findings) != 1 || op7.Findings[0].Key != "kf0f1e2d3c4b5a697869584736251400f1e2d3c4b5a697869584736251400f1e" ||
				op7.Findings[0].Gating == nil || !*op7.Findings[0].Gating {
				t.Errorf("op7's findings are %+v, want the finding its attempt 2 discovered, gating true", op7.Findings)
			}
		},
	}, {
		// (h) No dispatch: every stage pending, the duration null, nothing
		// on the row but the tick the tracker states.
		name: "no dispatch",
		src: pipeSources(
			pipeGraph(pipeTask("uv0", "hn6", "")),
			&Records{},
			nil),
		want: func(t *testing.T, m Model) {
			uv0 := pipeTick(t, m, "uv0")
			assertCell(t, uv0, map[string]string{
				StageClaim: StageStatePending, StageWork: StageStatePending,
				StageGate: StageStatePending, StageMerged: StageStatePending,
			})
			if uv0.DurationSeconds != nil {
				t.Errorf("uv0's duration is %d, want null: no dispatch marker states a start", *uv0.DurationSeconds)
			}
			if uv0.Findings == nil {
				t.Error("uv0's findings are nil, want the empty list: the field is required, and nil marshals as null")
			}
			if len(uv0.Tries) != 0 {
				t.Errorf("uv0 has %d tries, want none", len(uv0.Tries))
			}
			if uv0.ParentTickID != nil {
				t.Errorf("uv0's parent is %q, want null", *uv0.ParentTickID)
			}
		},
	}, {
		// (i) A finding discovered by attempt 2 of the tick is listed under
		// the tick — whatever try discovered it — with the absorption
		// decision's verdict when one exists, and null while nobody has
		// decided. A finding another tick discovered never rides this row.
		name: "finding discovered by attempt 2",
		src: pipeSources(
			pipeGraph(pipeTask("wx1", "hn6", "")),
			&Records{
				Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "wx1", State: "dispatched", Attempt: 2}),
				Attempts: []runstate.Attempt{
					pipeMarker(1, "wx1", "2026-09-27T04:00:00Z", "strong"),
					pipeMarker(2, "wx1", "2026-09-27T04:10:00Z", "strong"),
				},
				Absorptions: []runstate.Absorption{{
					SchemaVersion: runstate.SchemaVersion,
					Key:           "ka000000000000000000000000000000000000000000000000000000000000aa",
					TickID:        "ab1",
					Gating:        true,
					Basis:         runstate.AbsorptionPredicted,
					Reason:        "the finding gates the epic's done",
					Placement:     runstate.AbsorptionBeforeReview,
					DecidedAt:     "2026-09-27T04:20:00Z",
					Provenance:    pipeProvenance("wx1", 2, "strong"),
				}},
				Findings: []runstate.Finding{
					{
						SchemaVersion:  runstate.SchemaVersion,
						Key:            "ka000000000000000000000000000000000000000000000000000000000000aa",
						Source:         "worker-report",
						DiscoveredFrom: "run-epic-hn6/tick-wx1/attempt-2",
						Kind:           "defect",
						Title:          "the second attempt drafted this",
						Body:           "the body",
						Severity:       "low",
						TickID:         "wx1",
						Attempt:        2,
						Status:         runstate.FindingPromoted,
						ProposedAt:     "2026-09-27T04:15:00Z",
						Provenance:     pipeProvenance("wx1", 2, "strong"),
					},
					{
						SchemaVersion:  runstate.SchemaVersion,
						Key:            "kb000000000000000000000000000000000000000000000000000000000000bb",
						Source:         "worker-report",
						DiscoveredFrom: "run-epic-hn6/tick-zz9/attempt-1",
						Kind:           "proposal",
						Title:          "another tick drafted this",
						Body:           "the body",
						Severity:       "low",
						TickID:         "zz9",
						Attempt:        1,
						Status:         runstate.FindingProposed,
						ProposedAt:     "2026-09-27T04:15:00Z",
						Provenance:     pipeProvenance("zz9", 1, "strong"),
					},
					{
						SchemaVersion:  runstate.SchemaVersion,
						Key:            "kc000000000000000000000000000000000000000000000000000000000000cc",
						Source:         "worker-report",
						DiscoveredFrom: "run-epic-hn6/tick-wx1/attempt-1",
						Kind:           "proposal",
						Title:          "the first attempt drafted this one",
						Body:           "the body",
						Severity:       "low",
						TickID:         "wx1",
						Attempt:        1,
						Status:         runstate.FindingProposed,
						ProposedAt:     "2026-09-27T04:05:00Z",
						Provenance:     pipeProvenance("wx1", 1, "strong"),
					},
				},
			},
			nil),
		want: func(t *testing.T, m Model) {
			wx1 := pipeTick(t, m, "wx1")
			if len(wx1.Findings) != 2 {
				t.Fatalf("wx1's findings are %+v, want the two its attempts drafted", wx1.Findings)
			}
			second := wx1.Findings[0]
			if second.Key != "ka000000000000000000000000000000000000000000000000000000000000aa" ||
				second.Title != "the second attempt drafted this" ||
				second.Gating == nil || !*second.Gating {
				t.Errorf("the attempt-2 finding is %+v, want its own key and title and the absorption's gating true", second)
			}
			first := wx1.Findings[1]
			if first.Key != "kc000000000000000000000000000000000000000000000000000000000000cc" ||
				first.Title != "the first attempt drafted this one" || first.Gating != nil {
				t.Errorf("the undecided finding is %+v, want its own key and title and gating null: nobody has decided it", first)
			}
		},
	}, {
		// A redispatch the records have not read yet: the feed names the new
		// attempt, so the last rejected try's next step names the try the
		// run is already on.
		name: "redispatch the records have not read",
		src: func() Sources {
			src := pipeSources(
				pipeGraph(pipeTask("yz2", "hn6", "")),
				&Records{
					Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "yz2", State: "rejected", Attempt: 1}),
					Attempts:   []runstate.Attempt{pipeMarker(1, "yz2", "2026-09-27T04:00:00Z", "strong")},
				},
				[]runfeed.Event{
					pipeLine(testNow.Add(-58*time.Minute), "yz2", 1, reconcile.StageRejected,
						"collect_failed: the attempt made no commits"),
					pipeLine(testNow.Add(-40*time.Minute), "yz2", 2, reconcile.StageDispatched,
						"yz2 try 2 dispatched (run dispatch #2, branch ticfac/run-epic-hn6/tick-yz2/attempt-2)"),
				})
			src.Standing, src.StandingRead = pipeStanding(runprogress.Attempt{
				TickID: "yz2", Attempt: 2,
				Branch:   "refs/heads/ticfac/run-epic-hn6/tick-yz2/attempt-2",
				Worktree: "/worktrees/run-epic-hn6/tick-yz2/attempt-2",
			})
			return src
		}(),
		want: func(t *testing.T, m Model) {
			yz2 := pipeTick(t, m, "yz2")
			if len(yz2.Tries) != 1 {
				t.Fatalf("yz2 has %d tries, want 1: the records have not read the redispatch", len(yz2.Tries))
			}
			if step := yz2.Tries[0].NextStep; step == nil || *step != "retrying (try 2)" {
				t.Errorf("yz2's rejected try next_step is %q, want \"retrying (try 2)\": the feed names the new attempt", strOf(step))
			}
			if reason := yz2.Tries[0].Reason; reason == nil || *reason != "collect_failed: the attempt made no commits" {
				t.Errorf("yz2's rejected try reason is %q, want its rejection line's own detail", strOf(reason))
			}
		},
	}, {
		// A hold that names this tick: the next step IS the command that
		// clears it — the same unblock command the model's attention entry
		// carries, so the cell and the header answer with one sentence.
		name: "hold naming the tick",
		src: pipeSources(
			pipeGraph(pipeTask("b32", "hn6", "")),
			&Records{
				Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "b32", State: "rejected", Attempt: 1}),
				Attempts:   []runstate.Attempt{pipeMarker(1, "b32", "2026-09-27T04:00:00Z", "strong")},
			},
			[]runfeed.Event{
				pipeLine(testNow.Add(-58*time.Minute), "b32", 1, reconcile.StageRejected,
					"held: the attempt asked a question only a person answers"),
				pipeLine(testNow.Add(-58*time.Minute), "b32", 1, reconcile.StageRunHeld,
					"held: the attempt asked a question only a person answers"),
			}),
		want: func(t *testing.T, m Model) {
			b32 := pipeTick(t, m, "b32")
			if len(b32.Tries) != 1 {
				t.Fatalf("b32 has %d tries, want 1", len(b32.Tries))
			}
			step := b32.Tries[0].NextStep
			if step == nil || *step != `ticfac settle hn6 b32 1 --release "<who>"` {
				t.Errorf("b32's rejected try next_step is %q, want the settle command its hold clears by", strOf(step))
				return
			}
			for _, entry := range m.Attention {
				if entry.UnblockCommand != nil && *entry.UnblockCommand == *step {
					return
				}
			}
			t.Errorf("no attention entry carries the command %q the try's next_step names: the cell and the header must answer with one sentence", strOf(step))
		},
	}, {
		// A hold that names no tick: the run is held for a person, and the
		// try's next step points at the header that says what to do.
		name: "hold naming no tick",
		src: pipeSources(
			pipeGraph(pipeTask("c43", "hn6", "")),
			&Records{
				Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "c43", State: "rejected", Attempt: 1}),
				Attempts:   []runstate.Attempt{pipeMarker(1, "c43", "2026-09-27T04:00:00Z", "strong")},
			},
			[]runfeed.Event{
				pipeLine(testNow.Add(-58*time.Minute), "c43", 1, reconcile.StageRejected,
					"collect_failed: the attempt made no commits"),
				pipeLineAtTickScope(testNow.Add(-50*time.Minute), reconcile.StageRunHeld,
					reconcile.RefusedFindingUntriaged+": 2 untriaged finding(s) await triage"),
			}),
		want: func(t *testing.T, m Model) {
			c43 := pipeTick(t, m, "c43")
			if len(c43.Tries) != 1 {
				t.Fatalf("c43 has %d tries, want 1", len(c43.Tries))
			}
			if step := c43.Tries[0].NextStep; step == nil || *step != "held — see needs-you" {
				t.Errorf("c43's rejected try next_step is %q, want \"held — see needs-you\": the run is held for a person, but not on this tick", strOf(step))
			}
			if m.WaitsOn == nil || m.WaitsOn.Kind != WaitHeldForPerson {
				t.Errorf("the model's wait is %+v, want the held-for-person wait the fixture states", m.WaitsOn)
			}
		},
	}, {
		// The ci stage's own terminal answers: done behind a green forge,
		// failed behind a red one — the verdict, not the phase's activity.
		name: "closeout behind the forge's answer",
		src: func() Sources {
			src := pipeSources(
				pipeGraph(pipeTask("d54", "hn6", "closeout")),
				&Records{
					Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "d54", State: "reported", Attempt: 1}),
					Attempts:   []runstate.Attempt{pipeMarker(1, "d54", "2026-09-27T04:00:00Z", "closeout")},
				},
				[]runfeed.Event{
					pipeLine(testNow.Add(-70*time.Minute), "d54", 1, reconcile.StageCollected, "the closeout job reported"),
				})
			src.CI = &CIInput{State: "green", Checks: []CheckState{{Name: "go", Status: "completed", Conclusion: "success"}}}
			return src
		}(),
		want: func(t *testing.T, m Model) {
			d54 := pipeTick(t, m, "d54")
			assertCell(t, d54, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageCI: StageStateDone, StageClosed: StageStatePending,
			})
		},
	}, {
		// The gate's durable half: evidence that refused the current attempt
		// states failed before any line says so — the same record tryOutcome
		// already reads the try's outcome from.
		name: "gate failed by its evidence",
		src: pipeSources(
			pipeGraph(pipeTask("e65", "hn6", "")),
			&Records{
				Checkpoint: pipeCheckpoint(runstate.TickState{TickID: "e65", State: "reported", Attempt: 1}),
				Attempts: []runstate.Attempt{
					pipeMarker(1, "e65", "2026-09-27T04:00:00Z", "strong"),
				},
				Evidence: []runstate.Evidence{
					pipeEvidence("gate-e65-1", "e65", 1, "fail", "2026-09-27T04:31:00Z"),
				},
			},
			[]runfeed.Event{
				pipeLine(testNow.Add(-70*time.Minute), "e65", 1, reconcile.StageCollected, "e65 try 1 reported"),
			}),
		want: func(t *testing.T, m Model) {
			e65 := pipeTick(t, m, "e65")
			assertCell(t, e65, map[string]string{
				StageClaim: StageStateDone, StageWork: StageStateDone,
				StageGate: StageStateFailed, StageMerged: StageStatePending,
			})
		},
	}} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.want(t, pipeModel(t, tc.src))
		})
	}
}

// TestPipelineDerivesTheRunningEpic: the build_test fixture is the shared
// epic every model test reads, and its cells are the derivation's answers
// over real-shaped records — the closed tick whole (its gate evidenced only
// by the close itself), the in-flight tick at its work, the untouched rows
// pending, the absorbed tick whose absorption names no source stating a
// null parent, and the gate-failed try behind the in-flight one carrying its
// recorded reason and tier.
func TestPipelineDerivesTheRunningEpic(t *testing.T) {
	t.Parallel()
	m := pipeModel(t, runningEpicSources())

	nwj := pipeTick(t, m, "nwj")
	assertCell(t, nwj, map[string]string{
		StageClaim: StageStateDone, StageWork: StageStateDone,
		StageGate: StageStateDone, StageMerged: StageStateDone,
	})
	if nwj.DurationSeconds == nil || *nwj.DurationSeconds != 2898 {
		t.Errorf("nwj's duration is %v, want 2898 (its first dispatch 03:19:05 to its gate evidence's 04:07:23)", nwj.DurationSeconds)
	}
	if len(nwj.Findings) != 1 || nwj.Findings[0].Title != "README's command surface section predates the cobra+fang tree" ||
		nwj.Findings[0].Gating != nil {
		t.Errorf("nwj's findings are %+v, want the one its attempt 1 discovered with gating null (no absorption decided it)", nwj.Findings)
	}

	dh := pipeTick(t, m, "6dh")
	assertCell(t, dh, map[string]string{
		StageClaim: StageStateDone, StageWork: StageStateActive,
		StageGate: StageStatePending, StageMerged: StageStatePending,
	})
	if dh.DurationSeconds == nil || *dh.DurationSeconds != 5400 {
		t.Errorf("6dh's duration is %v, want 5400 (its first dispatch 04:00:00 to the model's now, while it is open)", dh.DurationSeconds)
	}
	if len(dh.Tries) != 2 {
		t.Fatalf("6dh has %d tries, want 2", len(dh.Tries))
	}
	if reason := dh.Tries[0].Reason; reason == nil || *reason != "the integrated gate refused attempt 2 of 6dh" {
		t.Errorf("6dh's gate-failed try reason is %q, want its gate_failed line's own detail", strOf(reason))
	}
	if tier := dh.Tries[0].Tier; tier == nil || *tier != "strong" {
		t.Errorf("6dh's gate-failed try tier is %v, want the marker's own strong", tier)
	}
	if step := dh.Tries[0].NextStep; step != nil {
		t.Errorf("6dh's first try carries next_step %q, want null: the try after it answers it", *step)
	}
	if step := dh.Tries[1].NextStep; step != nil {
		t.Errorf("6dh's last try carries next_step %q, want null: it is in flight", *step)
	}

	// The untouched rows and the review and close-out roles carry their own
	// stage lists, all pending, and no measured duration.
	for tickID, want := range map[string][]string{
		"89m": PipelineImplement,
		"152": PipelineImplement,
		"xbp": PipelineReview,
		"rrl": PipelineCloseout,
	} {
		tick := pipeTick(t, m, tickID)
		assertStageList(t, tick, want)
		states := map[string]string{}
		for _, stage := range want {
			states[stage] = StageStatePending
		}
		assertCell(t, tick, states)
		if tick.DurationSeconds != nil {
			t.Errorf("%s's duration is %d, want null: no dispatch marker states a start", tickID, *tick.DurationSeconds)
		}
	}
	if tick := pipeTick(t, m, "152"); tick.ParentTickID != nil {
		t.Errorf("the absorbed tick 152's parent is %q, want null: its absorption record names no source tick", *tick.ParentTickID)
	}
}
