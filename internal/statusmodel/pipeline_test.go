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

// The pipeline derivation suite (epic hn6, wave 2 — tick 3gk): every stage
// state, parent row, duration, finding and try word the pipeline cell and its
// drill-in carry is derived here, from the durable records and the feed's own
// typed lines — and every case's whole model still validates against the
// contract, because a field the derivation fills that the contract refuses is
// a field no renderer can show. The nine cases the tick names are the table's
// spine; the rows around them pin the branches of the same rules (the work
// refusal that never reached a gate, the redispatch the marker lost, the hold
// a person releases) so the derivation is covered edge to edge, not only on
// its happy path.

// pipelineCase is one scenario's durable facts: the graph, the checkpoint's
// tick rows, the dispatch markers, the gate evidence, the absorptions, the
// findings, the feed and the census. Everything else the Sources carry is the
// same honest running fixture.
type pipelineCase struct {
	tasks       []tk.GraphTask
	rows        []runstate.TickState
	markers     []runstate.Attempt
	evidence    []runstate.Evidence
	absorptions []runstate.Absorption
	findings    []runstate.Finding
	feed        []runfeed.Event
	standing    []runprogress.Attempt
	ci          *CIInput
}

// sources assembles the case's Sources: a live local run at the fixture's own
// clock, with the graph and records the case states.
func (c pipelineCase) sources() Sources {
	return Sources{
		Now:    testNow,
		RunID:  "run-pip",
		Host:   HostLocal,
		EpicID: "pip",
		Graph: &tk.Graph{
			Epic:  tk.GraphEpic{ID: "pip", Title: "the pipeline fixture epic"},
			Waves: []tk.GraphWave{{Wave: 1, Tasks: c.tasks}},
		},
		Records: &Records{
			Checkpoint: &runstate.Checkpoint{
				SchemaVersion: runstate.SchemaVersion,
				RunID:         "run-pip",
				EpicID:        "pip",
				Sequence:      3,
				State:         "running",
				Reason:        "the fixture is running",
				UpdatedAt:     testNow.Format(time.RFC3339),
				Ticks:         c.rows,
			},
			Attempts:    c.markers,
			Evidence:    c.evidence,
			Absorptions: c.absorptions,
			Findings:    c.findings,
		},
		Feed:         c.feed,
		Standing:     c.standing,
		StandingRead: true,
		Liveness: LivenessInput{
			Alive: true, State: "alive",
			Reason: "the fixture runs", Source: "run.pid",
		},
		CI: c.ci,
	}
}

// line is the feed's own constructor at the case's clock: one typed stage
// line, for a tick and an attempt or run-level when both are empty.
func line(at time.Time, tickID string, attempt int, stage, detail string) runfeed.Event {
	var who *int
	if attempt > 0 {
		who = intPtr(attempt)
	}
	return runfeed.NewEvent(at, "run-pip", tickID, who, stage, detail)
}

// wantCell spells one expected pipeline cell the way a failure reads it:
// "claim:done" pairs, in the role's own order.
func wantCell(pairs ...string) []PipelineStage {
	cell := make([]PipelineStage, 0, len(pairs))
	for _, pair := range pairs {
		stage, state, _ := strings.Cut(pair, ":")
		cell = append(cell, PipelineStage{Stage: stage, State: state})
	}
	return cell
}

// cellEquals answers whether the derived cell is exactly the wanted one —
// the stages in the role's order, each with its own state.
func cellEquals(got, want []PipelineStage) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i].Stage != want[i].Stage || got[i].State != want[i].State {
			return false
		}
	}
	return true
}

// pipelineTick finds one tick's row in the built model.
func pipelineTick(t *testing.T, model Model, tickID string) Tick {
	t.Helper()
	for _, wave := range deref(model.Waves) {
		for _, tick := range wave.Ticks {
			if tick.TickID == tickID {
				return tick
			}
		}
	}
	t.Fatalf("the fixture's graph carries no tick %s", tickID)
	return Tick{}
}

// pipelineTry finds one tick's try by its attempt number.
func pipelineTry(t *testing.T, tick Tick, attempt int) Try {
	t.Helper()
	for _, try := range tick.Tries {
		if try.Attempt == attempt {
			return try
		}
	}
	t.Fatalf("%s's try history carries no attempt %d: %+v", tick.TickID, attempt, tick.Tries)
	return Try{}
}

// validatesAgainstContract holds a whole built model to the pinned schema:
// every field the pipeline derivation fills is a field the contract admits,
// and a case that drifts the shape fails here rather than at a renderer.
func validatesAgainstContract(t *testing.T, model Model) {
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
		t.Errorf("a model the pipeline derivation filled is refused by the contract:\n%s\nmodel:\n%s",
			strings.Join(problems, "\n"), raw)
	}
}

// The reason-detail fixture for the truncation case: 247 characters whose
// first-160 cut lands inside the word "released", so the word-boundary cut
// backs up to "nobody" — the exact sentence a dashboard shows.
const pipelineLongDetail = "gate_failed: the integrated gate refused attempt 4 of ccc: gofmt drifted in two files, " +
	"the race detector found a data race in dispatch.go beside a mutex nobody released, and the " +
	"evidence records the refusal with the exit code the check left behind"

// pipelineCutDetail is pipelineLongDetail cut to its first 160 characters on
// a word boundary: the boundary inside the limit is "nobody", at 159.
const pipelineCutDetail = "gate_failed: the integrated gate refused attempt 4 of ccc: gofmt drifted in two files, " +
	"the race detector found a data race in dispatch.go beside a mutex nobody"

// TestPipelineCells is the derivation's table: one row per scenario, each
// naming the tick under test, its whole expected cell, and the drill-in facts
// beside it. The first nine rows are the tick's own named cases; the rows
// after them pin the branches of the same rules.
func TestPipelineCells(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		fact  pipelineCase
		tick  string
		want  []PipelineStage
		check func(t *testing.T, model Model)
	}{
		{
			// (a) a closed implement tick: claim, work, gate and merged all done.
			name: "closed implement tick is done all the way",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "closed"}},
				rows:  []runstate.TickState{{TickID: "aaa", State: "closed", Attempt: 1}},
				markers: []runstate.Attempt{
					attemptMarker(1, "aaa", "2026-09-27T03:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
				evidence: []runstate.Evidence{
					evidence("gate-aaa-1-go", "go", "aaa", 1, "pass", "integrated",
						"0fc09212e0e8f96fc3fdc87c2f681519bb0d191a", "2026-09-27T03:10:00Z", "2026-09-27T03:20:00Z"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-40*time.Minute), "aaa", 0, reconcile.StageClaimed, "claimed for the fixture"),
					line(testNow.Add(-30*time.Minute), "aaa", 1, reconcile.StageCollected, "attempt 1 of aaa collected"),
					line(testNow.Add(-25*time.Minute), "aaa", 1, reconcile.StageGateStarted, "the integrated gate started on aaa"),
					line(testNow.Add(-20*time.Minute), "aaa", 1, reconcile.StageGatePassed, "the integrated gate passed"),
					line(testNow.Add(-19*time.Minute), "aaa", 1, reconcile.StageClosed, "closed behind the integrated gate"),
				},
			},
			tick: "aaa",
			want: wantCell("claim:done", "work:done", "gate:done", "merged:done"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "aaa")
				if tick.DurationSeconds == nil || *tick.DurationSeconds != 1200 {
					t.Errorf("aaa's duration is %v, want 1200s: the earliest dispatch to the gate evidence's finish",
						tick.DurationSeconds)
				}
				try := pipelineTry(t, tick, 1)
				if try.Tier == nil || *try.Tier != "strong" {
					t.Errorf("aaa's try carries tier %v, want the marker's own strong", try.Tier)
				}
				if try.Reason != nil || try.NextStep != nil {
					t.Errorf("aaa's closed try carries reason/next_step (%v/%v), want none: it closed",
						try.Reason, try.NextStep)
				}
			},
		},
		{
			// (b) in flight with gate_started: claim and work done, gate
			// active, merged pending.
			name: "in flight at the gate",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "bbb", Title: "B", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "bbb", State: "reported", Attempt: 5}},
				markers: []runstate.Attempt{
					attemptMarker(5, "bbb", "2026-09-27T05:00:00Z", "frontier", "@cf/zai-org/glm-5.3", "herdr"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-28*time.Minute), "bbb", 5, reconcile.StageCollected, "attempt 5 of bbb collected"),
					line(testNow.Add(-27*time.Minute), "bbb", 5, reconcile.StageGateStarted, "the integrated gate started on bbb"),
				},
			},
			tick: "bbb",
			want: wantCell("claim:done", "work:done", "gate:active", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "bbb")
				// Open, so the span runs to the model's own now.
				if tick.DurationSeconds == nil || *tick.DurationSeconds != 1800 {
					t.Errorf("bbb's duration is %v, want 1800s: the dispatch to the model's now",
						tick.DurationSeconds)
				}
				try := pipelineTry(t, tick, 5)
				if try.Tier == nil || *try.Tier != "frontier" {
					t.Errorf("bbb's try carries tier %v, want the marker's own frontier", try.Tier)
				}
				if try.Reason != nil || try.NextStep != nil {
					t.Errorf("bbb's reported try carries reason/next_step (%v/%v), want none: nothing refused it",
						try.Reason, try.NextStep)
				}
			},
		},
		{
			// (c) rejected on gate_failed with no redispatch: gate failed, the
			// try's reason the line's own detail cut to a word boundary, and
			// the run's answer the ladder's.
			name: "rejected on a failed gate stops at the gate",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "ccc", Title: "C", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "ccc", State: "rejected", Attempt: 4}},
				markers: []runstate.Attempt{
					attemptMarker(4, "ccc", "2026-09-27T03:00:00Z", "strong", "@cf/zai-org/glm-5.3", "local-subprocess"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-90*time.Minute), "ccc", 4, reconcile.StageCollected, "attempt 4 of ccc collected"),
					line(testNow.Add(-80*time.Minute), "ccc", 4, reconcile.StageGateStarted, "the integrated gate started on ccc"),
					line(testNow.Add(-75*time.Minute), "ccc", 4, reconcile.StageGateFailed, pipelineLongDetail),
					line(testNow.Add(-74*time.Minute), "ccc", 4, reconcile.StageRejected, pipelineLongDetail),
				},
			},
			tick: "ccc",
			want: wantCell("claim:done", "work:done", "gate:failed", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "ccc")
				if tick.Findings == nil || len(tick.Findings) != 0 {
					t.Errorf("ccc's findings are %+v, want the empty list: the tick reported none", tick.Findings)
				}
				try := pipelineTry(t, tick, 4)
				if try.Outcome != TryRejected {
					t.Errorf("ccc's try outcome is %q, want %q", try.Outcome, TryRejected)
				}
				if try.Reason == nil || *try.Reason != pipelineCutDetail {
					t.Errorf("ccc's reason is %q, want the line's own detail cut at the word boundary:\n%q",
						derefString(try.Reason), pipelineCutDetail)
				}
				if len(pipelineCutDetail) > reasonDetailLimit {
					t.Errorf("the fixture's expected cut is %d characters, want at most %d",
						len(pipelineCutDetail), reasonDetailLimit)
				}
				if try.NextStep == nil || *try.NextStep != "the run will retry or escalate the tier" {
					t.Errorf("ccc's next step is %v, want the ladder's own answer",
						derefString(try.NextStep))
				}
			},
		},
		{
			// (d) rejected then redispatched: the first try's next_step is
			// null and the last try's is null, because the last try is in
			// flight.
			name: "rejected then redispatched: both next steps null, the last try in flight",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "ddd", Title: "D", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "ddd", State: "dispatched", Attempt: 9}},
				markers: []runstate.Attempt{
					attemptMarker(4, "ddd", "2026-09-27T03:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
					attemptMarker(9, "ddd", "2026-09-27T05:10:00Z", "frontier", "@cf/zai-org/glm-5.3", "local-subprocess"),
				},
				evidence: []runstate.Evidence{
					evidence("gate-ddd-4-go", "go", "ddd", 4, "fail", "integrated",
						"9f2ab6e0e8f96fc3fdc87c2f681519bb0d191a7", "2026-09-27T04:30:00Z", "2026-09-27T04:31:00Z"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-80*time.Minute), "ddd", 4, reconcile.StageGateFailed,
						"the integrated gate refused attempt 4 of ddd (go)"),
					line(testNow.Add(-79*time.Minute), "ddd", 4, reconcile.StageRejected,
						"gate_failed: the integrated gate did not pass: gofmt"),
					line(testNow.Add(-20*time.Minute), "ddd", 9, reconcile.StageDispatched,
						"ddd try 2 dispatched (run dispatch #9)"),
				},
				standing: []runprogress.Attempt{{TickID: "ddd", Attempt: 9}},
			},
			tick: "ddd",
			want: wantCell("claim:done", "work:active", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "ddd")
				if len(tick.Tries) != 2 {
					t.Fatalf("ddd carries %d tries, want the two dispatches", len(tick.Tries))
				}
				first := pipelineTry(t, tick, 4)
				if first.Outcome != TryGateFailed {
					t.Errorf("ddd's first try outcome is %q, want %q", first.Outcome, TryGateFailed)
				}
				if first.Reason == nil || *first.Reason != "gate_failed: the integrated gate did not pass: gofmt" {
					t.Errorf("ddd's first try reason is %v, want the LATEST line for its attempt, the rejection",
						derefString(first.Reason))
				}
				if first.NextStep != nil {
					t.Errorf("ddd's first try carries next_step %q, want null: only the last try states one",
						*first.NextStep)
				}
				last := pipelineTry(t, tick, 9)
				if last.Outcome != TryInFlight {
					t.Errorf("ddd's last try outcome is %q, want %q", last.Outcome, TryInFlight)
				}
				if last.NextStep != nil {
					t.Errorf("ddd's last try carries next_step %q, want null: the last try is in flight",
						*last.NextStep)
				}
				if tick.DurationSeconds == nil || *tick.DurationSeconds != 9000 {
					t.Errorf("ddd's duration is %v, want 9000s: the earliest dispatch to the model's now",
						tick.DurationSeconds)
				}
			},
		},
		{
			// (e) closeout during phase ci: claim done, work done, ci active,
			// closed pending.
			name: "closeout waiting on ci shows the phase live in its cell",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "eee", Title: "E", Status: "open", Role: "closeout"}},
				rows:  []runstate.TickState{{TickID: "eee", State: "reported", Attempt: 3}},
				markers: []runstate.Attempt{
					attemptMarker(3, "eee", "2026-09-27T04:40:00Z", "closeout", "@cf/zai-org/glm-5.3", "local-subprocess"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-10*time.Minute), "eee", 3, reconcile.StageCollected, "the closeout job collected"),
					line(testNow.Add(-5*time.Minute), "", 0, reconcile.StageCloseoutHeld,
						"the close-out waits for CI green on the PR"),
				},
			},
			tick: "eee",
			want: wantCell("claim:done", "work:done", "ci:active", "closed:pending"),
		},
		{
			// (f) a review tick: [claim, review, closed].
			name: "a review tick carries its own stages",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "fff", Title: "F", Status: "closed", Role: "review"}},
				rows:  []runstate.TickState{{TickID: "fff", State: "closed", Attempt: 1}},
				markers: []runstate.Attempt{
					attemptMarker(1, "fff", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "herdr"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-55*time.Minute), "fff", 1, reconcile.StageCollected, "the review job collected"),
					line(testNow.Add(-50*time.Minute), "fff", 1, reconcile.StageClosed,
						"closed behind a validated review answer"),
				},
			},
			tick: "fff",
			want: wantCell("claim:done", "review:done", "closed:done"),
		},
		{
			// (g) an absorbed repair tick: parent_tick_id set — and the
			// graph's own parent and the epic itself each read their own way.
			name: "an absorbed repair tick indents under its source",
			fact: pipelineCase{
				tasks: []tk.GraphTask{
					{ID: "src", Title: "the source tick", Status: "open"},
					{ID: "rep", Title: "the absorbed repair", Status: "open"},
					{ID: "kid", Title: "a graph child of another tick", Status: "open", Parent: "src"},
					{ID: "epi", Title: "a direct child of the epic", Status: "open", Parent: "pip"},
				},
				absorptions: []runstate.Absorption{{
					SchemaVersion: runstate.SchemaVersion,
					Key:           "fkey-rep",
					TickID:        "rep",
					Gating:        true,
					Reason:        "the fixture absorbed the finding",
					DecidedAt:     testNow.Add(-30 * time.Minute).Format(time.RFC3339),
					Provenance:    provenanceOf("src", 2, "strong", "claude-opus-5", "local-subprocess"),
				}},
			},
			tick: "rep",
			want: wantCell("claim:pending", "work:pending", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				rep := pipelineTick(t, model, "rep")
				if rep.ParentTickID == nil || *rep.ParentTickID != "src" {
					t.Errorf("the absorbed repair's parent is %v, want src: the absorption names the source in its provenance",
						derefString(rep.ParentTickID))
				}
				if !rep.Absorbed {
					t.Error("the absorbed repair is not marked absorbed: the promotion is the row's own shape change")
				}
				kid := pipelineTick(t, model, "kid")
				if kid.ParentTickID == nil || *kid.ParentTickID != "src" {
					t.Errorf("the graph child's parent is %v, want src: the task's own Parent",
						derefString(kid.ParentTickID))
				}
				epi := pipelineTick(t, model, "epi")
				if epi.ParentTickID != nil {
					t.Errorf("the epic's direct child states parent %q, want null: the epic is the row it indents under anyway",
						*epi.ParentTickID)
				}
				src := pipelineTick(t, model, "src")
				if src.ParentTickID != nil {
					t.Errorf("the plain tick states parent %q, want null: nothing places it", *src.ParentTickID)
				}
			},
		},
		{
			// (h) no dispatch: every stage pending, duration null.
			name: "a tick never dispatched is pending everywhere",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "hhh", Title: "H", Status: "open"}},
			},
			tick: "hhh",
			want: wantCell("claim:pending", "work:pending", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "hhh")
				if tick.DurationSeconds != nil {
					t.Errorf("hhh's duration is %d, want null: no dispatch marker states a start", *tick.DurationSeconds)
				}
				if len(tick.Tries) != 0 {
					t.Errorf("hhh carries %+v tries, want none: nothing was dispatched", tick.Tries)
				}
				if tick.Findings == nil || len(tick.Findings) != 0 {
					t.Errorf("hhh's findings are %+v, want the empty list", tick.Findings)
				}
			},
		},
		{
			// (i) a finding discovered by attempt 2 of the tick: listed under
			// the tick, with the absorption's verdict where one exists.
			name: "a finding discovered by the tick's second attempt is listed under the tick",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "ggg", Title: "G", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "ggg", State: "reported", Attempt: 2}},
				markers: []runstate.Attempt{
					attemptMarker(1, "ggg", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
					attemptMarker(2, "ggg", "2026-09-27T04:10:00Z", "frontier", "@cf/zai-org/glm-5.3", "local-subprocess"),
				},
				findings: []runstate.Finding{{
					SchemaVersion:  runstate.SchemaVersion,
					Key:            "fkey-ggg",
					Source:         "worker-report",
					DiscoveredFrom: "run-pip/tick-ggg/attempt-2",
					Kind:           "defect",
					Title:          "the gate's own evidence page never renders",
					Body:           "the drill-in renders an empty page",
					Severity:       "medium",
					TickID:         "ggg",
					Attempt:        2,
					Status:         runstate.FindingProposed,
					ProposedAt:     testNow.Add(-20 * time.Minute).Format(time.RFC3339),
				}},
				absorptions: []runstate.Absorption{{
					SchemaVersion: runstate.SchemaVersion,
					Key:           "fkey-ggg",
					TickID:        "rep2",
					Gating:        true,
					Reason:        "the fixture decided the finding gates the done",
					DecidedAt:     testNow.Add(-15 * time.Minute).Format(time.RFC3339),
					Provenance:    provenanceOf("ggg", 2, "strong", "claude-opus-5", "local-subprocess"),
				}},
			},
			tick: "ggg",
			want: wantCell("claim:done", "work:done", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "ggg")
				if len(tick.Findings) != 1 {
					t.Fatalf("ggg carries %+v findings, want the one its attempt 2 drafted", tick.Findings)
				}
				finding := tick.Findings[0]
				if finding.Key != "fkey-ggg" || finding.Title != "the gate's own evidence page never renders" {
					t.Errorf("the finding is %+v, want the draft's own key and title", finding)
				}
				if finding.Gating == nil || !*finding.Gating {
					t.Errorf("the finding's gating is %v, want true: the absorption decided it", finding.Gating)
				}
			},
		},

		// The branches of the same rules the nine cases ride on.

		{
			// A refused current try with no later dispatch fails the WORK
			// stage: the cell says where the tick stopped.
			name: "a work refusal with no redispatch fails the work stage",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "jjj", Title: "J", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "jjj", State: "rejected", Attempt: 2}},
				markers: []runstate.Attempt{
					attemptMarker(2, "jjj", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-70*time.Minute), "jjj", 2, reconcile.StageRejected,
						"collect_failed: the report never came"),
				},
			},
			tick: "jjj",
			want: wantCell("claim:done", "work:failed", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "jjj")
				try := pipelineTry(t, tick, 2)
				if try.Reason == nil || *try.Reason != "collect_failed: the report never came" {
					t.Errorf("jjj's reason is %v, want the rejection line's own detail", derefString(try.Reason))
				}
			},
		},
		{
			// A rejection NO line explains: no reason is invented.
			name: "a rejection no line explains invents no reason",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "kkk", Title: "K", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "kkk", State: "rejected", Attempt: 2}},
				markers: []runstate.Attempt{
					attemptMarker(2, "kkk", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
			},
			tick: "kkk",
			want: wantCell("claim:done", "work:failed", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "kkk")
				try := pipelineTry(t, tick, 2)
				if try.Reason != nil {
					t.Errorf("kkk's reason is %q, want null: no line states why, and no reason is invented", *try.Reason)
				}
				if try.NextStep == nil || *try.NextStep != "the run will retry or escalate the tier" {
					t.Errorf("kkk's next step is %v, want the ladder's own answer", derefString(try.NextStep))
				}
			},
		},
		{
			// The last try refused, a dispatch past it that no marker states:
			// the run's own next step says it is retrying.
			name: "a refused last try with a later dispatch says it is retrying",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "lll", Title: "L", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "lll", State: "rejected", Attempt: 4}},
				markers: []runstate.Attempt{
					attemptMarker(4, "lll", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-70*time.Minute), "lll", 4, reconcile.StageRejected,
						"collect_failed: the report never came"),
					line(testNow.Add(-10*time.Minute), "lll", 7, reconcile.StageDispatched,
						"lll try 2 dispatched (run dispatch #7)"),
				},
			},
			tick: "lll",
			// The work stage is pending rather than failed: a later dispatch
			// exists, so the run has already moved this tick on — the failure is
			// behind it, and where the new try is the records have not said.
			want: wantCell("claim:done", "work:pending", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "lll")
				try := pipelineTry(t, tick, 4)
				if try.NextStep == nil || *try.NextStep != "retrying (try 2)" {
					t.Errorf("lll's next step is %v, want retrying (try 2): the feed states the redispatch",
						derefString(try.NextStep))
				}
			},
		},
		{
			// The run held on this tick's refused attempt: the next step is
			// the command that clears the hold — the same one the header's
			// attention entry carries.
			name: "a held run's refused try names the command that clears it",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "mmm", Title: "M", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "mmm", State: "rejected", Attempt: 4}},
				markers: []runstate.Attempt{
					attemptMarker(4, "mmm", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-60*time.Minute), "mmm", 4, reconcile.StageRunHeld,
						"attempt_unaddressed: attempt 4 of mmm cannot be addressed and has not settled"),
				},
			},
			tick: "mmm",
			want: wantCell("claim:done", "work:failed", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "mmm")
				try := pipelineTry(t, tick, 4)
				want := "ticfac settle pip mmm 4 --release \"<who>\""
				if try.NextStep == nil || *try.NextStep != want {
					t.Errorf("mmm's next step is %v, want %q: the attention entry's own unblock command",
						derefString(try.NextStep), want)
				}
			},
		},
		{
			// A struck-out tick nobody redispatched: the next step points the
			// person at the needs-you line.
			name: "a struck-out tick's refused try points at needs-you",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "nnn", Title: "N", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "nnn", State: "rejected", Attempt: 2}},
				markers: []runstate.Attempt{
					attemptMarker(2, "nnn", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-60*time.Minute), "nnn", 2, reconcile.StageHeld,
						"pip/nnn is struck out and only a person releases it"),
				},
			},
			tick: "nnn",
			want: wantCell("claim:done", "work:failed", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "nnn")
				try := pipelineTry(t, tick, 2)
				if try.NextStep == nil || *try.NextStep != "held — see needs-you" {
					t.Errorf("nnn's next step is %v, want held — see needs-you: only a person releases it",
						derefString(try.NextStep))
				}
			},
		},
		{
			// A hold a settle already released is history, not a next step.
			name: "a released hold is history",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "ooo", Title: "O", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "ooo", State: "rejected", Attempt: 2}},
				markers: []runstate.Attempt{
					attemptMarker(2, "ooo", "2026-09-27T04:00:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
				feed: []runfeed.Event{
					line(testNow.Add(-60*time.Minute), "ooo", 2, reconcile.StageHeld,
						"pip/ooo is struck out and only a person releases it"),
					line(testNow.Add(-30*time.Minute), "ooo", 2, reconcile.StageSettled,
						"a person released attempt 2 of ooo"),
				},
			},
			tick: "ooo",
			want: wantCell("claim:done", "work:failed", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "ooo")
				try := pipelineTry(t, tick, 2)
				if try.NextStep == nil || *try.NextStep != "the run will retry or escalate the tier" {
					t.Errorf("ooo's next step is %v, want the ladder's own answer: the hold was released",
						derefString(try.NextStep))
				}
			},
		},
		{
			// A claimed line without a marker: the claim reads done, and the
			// duration stays null — a claim is not a dispatch.
			name: "a claimed line with no marker reads claimed and states no duration",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "ppp", Title: "P", Status: "open"}},
				feed: []runfeed.Event{
					line(testNow.Add(-20*time.Minute), "ppp", 0, reconcile.StageClaimed, "claimed for the fixture"),
				},
			},
			tick: "ppp",
			want: wantCell("claim:done", "work:pending", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "ppp")
				if tick.DurationSeconds != nil {
					t.Errorf("ppp's duration is %d, want null: a claim states no dispatch time", *tick.DurationSeconds)
				}
			},
		},
		{
			// A marker whose stamp does not parse: the claim reads done, the
			// duration stays null.
			name: "a marker whose stamp does not parse states no duration",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "qqq", Title: "Q", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "qqq", State: "dispatched", Attempt: 1}},
				markers: []runstate.Attempt{{
					SchemaVersion: runstate.SchemaVersion,
					Attempt:       1,
					TickID:        "qqq",
					DispatchedAt:  "yesterday, roughly",
					JobHandle:     map[string]any{"executor": "local-subprocess"},
					Provenance:    provenanceOf("qqq", 1, "strong", "claude-opus-5", "local-subprocess"),
				}},
			},
			tick: "qqq",
			want: wantCell("claim:done", "work:active", "gate:pending", "merged:pending"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "qqq")
				if tick.DurationSeconds != nil {
					t.Errorf("qqq's duration is %d, want null: no dispatch stamp parses", *tick.DurationSeconds)
				}
			},
		},
		{
			// CI red on the close-out's PR: the forge names the failure, the
			// cell states it.
			name: "a red ci on the epic pr fails the closeout's ci stage",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "rrr", Title: "R", Status: "open", Role: "closeout"}},
				rows:  []runstate.TickState{{TickID: "rrr", State: "reported", Attempt: 3}},
				markers: []runstate.Attempt{
					attemptMarker(3, "rrr", "2026-09-27T04:40:00Z", "closeout", "@cf/zai-org/glm-5.3", "local-subprocess"),
				},
				ci: &CIInput{State: "red"},
			},
			tick: "rrr",
			want: wantCell("claim:done", "work:done", "ci:failed", "closed:pending"),
		},
		{
			// An integrated tick: merged reads done — the merge is the
			// substance, the close the formality behind it.
			name: "an integrated tick reads merged done",
			fact: pipelineCase{
				tasks: []tk.GraphTask{{ID: "sss", Title: "S", Status: "open"}},
				rows:  []runstate.TickState{{TickID: "sss", State: "integrated", Attempt: 2}},
				markers: []runstate.Attempt{
					attemptMarker(2, "sss", "2026-09-27T04:50:00Z", "strong", "claude-opus-5", "local-subprocess"),
				},
				evidence: []runstate.Evidence{
					evidence("gate-sss-2-go", "go", "sss", 2, "pass", "integrated",
						"0fc09212e0e8f96fc3fdc87c2f681519bb0d191a", "2026-09-27T05:10:00Z", "2026-09-27T05:15:00Z"),
				},
			},
			tick: "sss",
			want: wantCell("claim:done", "work:done", "gate:done", "merged:done"),
			check: func(t *testing.T, model Model) {
				tick := pipelineTick(t, model, "sss")
				// Integrated: the span measures to the gate evidence's finish.
				if tick.DurationSeconds == nil || *tick.DurationSeconds != 1500 {
					t.Errorf("sss's duration is %v, want 1500s: the dispatch to the gate evidence's finish",
						tick.DurationSeconds)
				}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := Build(tc.fact.sources())
			tick := pipelineTick(t, model, tc.tick)
			if !cellEquals(tick.Pipeline, tc.want) {
				t.Errorf("%s's pipeline cell is %s, want %s", tc.tick, cellOf(tick.Pipeline), cellOf(tc.want))
			}
			if tc.check != nil {
				tc.check(t, model)
			}
			validatesAgainstContract(t, model)
		})
	}
}

// TestPipelineRowsKeepTheRoleStageLists: the stage list is the role's — an
// implement tick's, a review's, a close-out's — whatever the records state,
// because the renderer lays the cell out from the list alone.
func TestPipelineRowsKeepTheRoleStageLists(t *testing.T) {
	t.Parallel()
	model := Build(pipelineCase{
		tasks: []tk.GraphTask{
			{ID: "imp", Title: "an implement tick", Status: "open"},
			{ID: "rev", Title: "a review tick", Status: "open", Role: "review"},
			{ID: "clo", Title: "a closeout tick", Status: "open", Role: "closeout"},
		},
	}.sources())
	for tickID, want := range map[string][]string{
		"imp": PipelineImplement,
		"rev": PipelineReview,
		"clo": PipelineCloseout,
	} {
		if !stagesOf(pipelineTick(t, model, tickID).Pipeline, want) {
			t.Errorf("%s's pipeline stages are %s, want the role's own %v",
				tickID, cellOf(pipelineTick(t, model, tickID).Pipeline), want)
		}
	}
}

// TestPipelineReasonCut: the word-boundary cut itself — short details whole,
// a cut that lands on a space, a detail with no boundary inside the limit,
// and a multibyte word never split by a byte count.
func TestPipelineReasonCut(t *testing.T) {
	t.Parallel()
	short := "a detail well inside the limit comes back whole"
	if got := cutToWordBoundary(short, reasonDetailLimit); got != short {
		t.Errorf("cut(%q) is %q, want the detail whole", short, got)
	}

	// A cut that lands exactly before a space keeps the space's left side.
	detail := strings.Repeat("word ", 33) + "the tail nobody sees"
	want := strings.Repeat("word ", 31) + "word"
	if got := cutToWordBoundary(detail, reasonDetailLimit); got != want || strings.HasSuffix(got, " ") {
		t.Errorf("the cut is %q, want %q: the boundary inside the limit is the cut", got, want)
	}

	// No boundary inside the limit: a hard cut, never a stretched one.
	if got := cutToWordBoundary(strings.Repeat("x", 200), reasonDetailLimit); len([]rune(got)) != reasonDetailLimit {
		t.Errorf("the hard cut is %d runes, want %d", len([]rune(got)), reasonDetailLimit)
	}

	// A multibyte word at the cut: the limit is characters, not bytes.
	detail = strings.Repeat("æ", 200)
	if got := cutToWordBoundary(detail, reasonDetailLimit); len([]rune(got)) != reasonDetailLimit {
		t.Errorf("the multibyte cut is %d runes, want %d", len([]rune(got)), reasonDetailLimit)
	}
	if got := cutToWordBoundary(strings.Repeat("æ", 100), reasonDetailLimit); got != strings.Repeat("æ", 100) {
		t.Errorf("a short multibyte detail did not come back whole: %d runes", len([]rune(got)))
	}
}

// derefString renders a nullable string for a failure message.
func derefString(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
