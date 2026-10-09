package cli

// The dashboard half of `ticfac watch` (epic ymf, tick ugm): the frame this
// file pins is the redesigned dashboard — the identity line with its rule,
// the needs-you answer (a box per hold when one stands, the quiet line
// beside the health summary when none does), the phase track with its
// you-are-here marker, the ticks grouped by state with their status words,
// and the latest sentences under their rule with the key hints.
//
// The renderer is a pure function of the model plus the pane's width and
// height: nothing here reads a run's own records, spawns a process or
// measures a host — so the five scenarios the design names (a fresh run, a
// busy wave, a held tick, an ended landed run, an ended failed run) are
// pinned byte for byte against golden files at 80x24 and 120x40, and every
// rule the design states has its own test. The wiring (the redraw loop, the
// keep lines, the exit codes) stays in watch_block_test.go.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// updateGoldens regenerates the dashboard golden files from the current
// renderer: `go test -run TestDashboardScenario ./internal/cli/ -update`.
var updateGoldens = flag.Bool("update", false, "rewrite the watch dashboard golden files from the current renderer")

// plainStyles is the identity style set: every line comes back exactly as it
// was built, so a content assertion reads the words and not the escape
// codes. The colour tests use ansiWatchStyles() instead.
func plainStyles() watchStyles {
	return identityWatchStyles()
}

// ptr is the one-line pointer helper the model fixtures lean on.
func ptr[T any](v T) *T { return &v }

// dashboardContractGolden decodes the contract bundle's `dashboard` golden —
// the rendering fixture the contract carries: every dashboard field
// populated, admitted by the schema and held to its anchors by the
// statusmodel suite, so the tests below render a model that is the shape
// the whole epic agreed on.
func dashboardContractGolden(t *testing.T) statusmodel.Model {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatalf("locate the contract bundle: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status-model.json"))
	if err != nil {
		t.Fatalf("read the status model contract: %v", err)
	}
	var fixture struct {
		Golden map[string]json.RawMessage `json:"golden"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the status model contract does not parse: %v", err)
	}
	golden, ok := fixture.Golden["dashboard"]
	if !ok {
		t.Fatal("the status model contract carries no dashboard golden — the renderers and the phone page reference it by name")
	}
	var m statusmodel.Model
	if err := json.Unmarshal(golden, &m); err != nil {
		t.Fatalf("the dashboard golden does not decode into the status model: %v", err)
	}
	return m
}

// statusModelGoldens decodes every golden the status model contract carries,
// by name — the same map the phone page's suite imports straight from the
// bundle, so a golden added for a cross-renderer test is one fixture both
// suites render.
func statusModelGoldens(t *testing.T) map[string]statusmodel.Model {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatalf("locate the contract bundle: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status-model.json"))
	if err != nil {
		t.Fatalf("read the status model contract: %v", err)
	}
	var fixture struct {
		Golden map[string]json.RawMessage `json:"golden"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the status model contract does not parse: %v", err)
	}
	out := make(map[string]statusmodel.Model, len(fixture.Golden))
	for name, raw := range fixture.Golden {
		var m statusmodel.Model
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("golden %s does not decode into the status model: %v", name, err)
		}
		out[name] = m
	}
	return out
}

// ---------------------------------------------------------------------------
// The running fixture: one mid-run epic, every field the dashboard renders
// populated, with its one-wave-later successor.
// ---------------------------------------------------------------------------

// dashboardFixture is one mid-run epic as the model states it: wave 1 done
// (t1 closed, its absorbed repair child t1c closed under it), wave 2 active
// (t2 at its gate with a live worker and an escalated second try, t3
// ready), wave 3 upcoming (t4, a review tick at its review stage). Healthy,
// a metered cost line, a CI with one check running and one green, and a
// recent tail.
func dashboardFixture() statusmodel.Model {
	strong, frontier := "strong", "frontier"
	attempt := 2
	duration := int64(2940)
	childDuration := int64(2070)
	live := int64(3600)
	t1, t2 := 1, 2
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "epic-rmod",
		EpicID:        "rmod",
		Host:          statusmodel.HostLocal,
		GeneratedAt:   "2026-09-28T19:20:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: true, State: "alive", Reason: "pid 4242", Source: "run.pid",
			LastEventAgeSeconds: ptr(int64(300)),
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseWaves,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateActive},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStatePending},
			},
			Wave: &statusmodel.WaveRef{Active: 2, Total: 3},
			Track: []statusmodel.TrackStep{
				{Label: statusmodel.TrackLabels[0], State: statusmodel.PhaseStateActive},
				{Label: statusmodel.TrackLabels[1], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[2], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[3], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[4], State: statusmodel.PhaseStatePending},
			},
			Here: 0,
		},
		Progress: statusmodel.Progress{
			Ticks: &statusmodel.TickProgress{Total: 5, Closed: 2, Open: 3},
			Waves: &statusmodel.WaveProgress{Total: 3, Done: 1, Active: 2},
		},
		EpicTitle: ptr("a takeover of what ticks drops"),
		Recent: []runfeed.Event{
			{SchemaVersion: 1, At: "2026-09-28T18:49:00Z", RunID: "epic-rmod",
				TickID: ptr("t2"), Attempt: ptr(t2), Stage: "gate_failed",
				Detail: "the integrated gate refused attempt 2 of t2 (go)"},
			{SchemaVersion: 1, At: "2026-09-28T18:58:02Z", RunID: "epic-rmod",
				TickID: ptr("t2"), Attempt: ptr(t2), Stage: "dispatched",
				Detail: "t2 try 2 dispatched"},
			{SchemaVersion: 1, At: "2026-09-28T19:04:12Z", RunID: "epic-rmod",
				Stage: "closeout_held", Detail: "the close-out waits for CI green on the PR"},
		},
		Waves: &[]statusmodel.Wave{
			{Wave: 1, State: statusmodel.WaveDone, Ticks: []statusmodel.Tick{
				{
					TickID: "t1", Title: "the first tick", Gloss: "the first tick's gloss",
					State: "closed", Status: statusmodel.WordMerged,
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStateDone},
					},
					DurationSeconds: &duration,
					Try:             &t1, Attempt: &t1,
					Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
						DispatchedAt: "2026-09-28T17:11:00Z", Tier: &strong}},
					Tier: &strong,
				},
				{
					TickID: "t1c", Title: "the repair the run absorbed", Gloss: "a repair child",
					State: "closed", Status: statusmodel.WordMerged, ParentTickID: ptr("t1"), Absorbed: true,
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStateDone},
					},
					DurationSeconds: &childDuration,
					Tries: []statusmodel.Try{
						{Try: 1, Attempt: 1, Outcome: statusmodel.TryGateFailed,
							DispatchedAt: "2026-09-28T18:00:30Z", Tier: &frontier},
						{Try: 2, Attempt: 2, Outcome: statusmodel.TryClosed,
							DispatchedAt: "2026-09-28T18:18:00Z", Tier: &frontier},
					},
					Tier: &frontier,
				},
			}},
			{Wave: 2, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{
				{
					TickID: "t2", Title: "the second tick", Gloss: "the second tick's gloss",
					State: "dispatched", Status: statusmodel.WordTesting,
					Exception: ptr("attempt 2, model escalated"),
					Attempt:   &attempt, Try: &attempt,
					Model: ptr("cloudflare-workers-ai/@cf/zai-org/glm-5.3"), Executor: ptr("herdr"),
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateActive},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
					},
					DurationSeconds: &live,
					Tries: []statusmodel.Try{
						{Try: 1, Attempt: 1, Outcome: statusmodel.TryRejected,
							DispatchedAt: "2026-09-28T18:20:00Z", Tier: &strong,
							Reason: ptr("gofmt drifted in two files")},
						{Try: 2, Attempt: 2, Outcome: statusmodel.TryInFlight,
							DispatchedAt: "2026-09-28T18:58:02Z", Tier: &frontier},
					},
					Tier: &frontier,
				},
				{
					TickID: "t3", Title: "the third tick", Gloss: "the third tick's gloss",
					State: "ready", Status: statusmodel.WordUpNext,
				},
			}},
			{Wave: 3, State: statusmodel.WaveUpcoming, Ticks: []statusmodel.Tick{
				{
					TickID: "t4", Title: "the review tick", Gloss: "the review tick's gloss",
					Role: "review", State: "dispatched", Status: statusmodel.WordReviewing,
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageReview, State: statusmodel.StageStateActive},
						{Stage: statusmodel.StageClosed, State: statusmodel.StageStatePending},
					},
					Tries: []statusmodel.Try{{Try: 1, Attempt: 3, Outcome: statusmodel.TryInFlight,
						DispatchedAt: "2026-09-28T19:00:00Z", Tier: &frontier}},
					Tier: &frontier,
				},
			}},
		},
		Groups: &statusmodel.TickGroups{
			Now:    []string{"t2", "t4"},
			Done:   []string{"t1", "t1c"},
			UpNext: []string{"t3"},
			Held:   []string{},
		},
		Workers: &[]statusmodel.Worker{{
			TickID: "t2", Attempt: 2, Branch: "refs/heads/ticfac/run-epic-rmod/tick-t2/attempt-2",
			Handle: ptr("herdr pane tick-t2-a2"),
			Activity: &statusmodel.WorkerActivity{
				WindowSeconds: 600,
				Buckets:       []int{1, 3, 5, 8, 7, 5, 3, 1, 2, 5},
				LastAction:    ptr("ran go test ./internal/reconcile"),
				LastActionAt:  ptr("2026-09-28T19:18:31Z"),
				Nudges:        1,
			},
			ElapsedSeconds: ptr(int64(1318)),
		}},
		WaitsOn: &statusmodel.Wait{
			Kind: statusmodel.WaitWorkers, What: "1 in-flight attempt(s)",
		},
		Attention: []statusmodel.Attention{},
		Health: statusmodel.Health{
			Verdict: statusmodel.HealthVerdict{
				State: statusmodel.VerdictHealthy,
				Recovered: []statusmodel.Recovery{
					{What: "net", Count: 14},
					{What: "sleep", Count: 2, Seconds: ptr(int64(2460))},
				},
			},
		},
		CI: &statusmodel.CI{
			State: "red",
			PR: &statusmodel.PR{Number: 98, URL: "https://github.com/example/ticfac/pull/98",
				HeadRef: "epic/rmod", HeadSHA: "9f2ab6e0e8f96fc3fdc87c2f681519bb0d191a7", BaseRef: "main"},
			Checks: []statusmodel.CheckState{
				{Name: "go", Status: "in_progress", Conclusion: "", StartedAt: "2026-09-28T19:14:00Z"},
				{Name: "ts", Status: "completed", Conclusion: "success", StartedAt: "2026-09-28T18:44:00Z"},
			},
		},
		Cost: statusmodel.Cost{
			RecordedUSD: ptr(0.41), Attempts: 6,
			Basis: "usage recorded on decision records",
			Lines: []statusmodel.CostLine{
				{Source: statusmodel.CostSourceWorkersAI, Metered: true, USD: ptr(0.41), Attempts: 4,
					Basis: "gateway usage for 4 dispatches"},
				{Source: statusmodel.CostSourceClaude, Metered: false, USD: nil, Attempts: 2,
					Basis: "local claude on a Max subscription — not metered"},
			},
		},
	}
}

// dashboardSuccessor is the fixture one wave later: t2 closed behind its
// gate, t3 dispatched, t4's review still going. Same ticks, advanced states
// — and advanced groups, because the rows move between them.
func dashboardSuccessor() statusmodel.Model {
	raw, err := json.Marshal(dashboardFixture())
	if err != nil {
		panic(err)
	}
	var m statusmodel.Model
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	frontier := "frontier"
	for wi := range *m.Waves {
		for ti := range (*m.Waves)[wi].Ticks {
			tick := &(*m.Waves)[wi].Ticks[ti]
			switch tick.TickID {
			case "t2":
				tick.State = "closed"
				tick.Status = statusmodel.WordMerged
				tick.Exception = nil
				for si := range tick.Pipeline {
					tick.Pipeline[si].State = statusmodel.StageStateDone
				}
				tick.Tries = append(tick.Tries, statusmodel.Try{Try: 3, Attempt: 3,
					Outcome: statusmodel.TryClosed, DispatchedAt: "2026-09-28T19:40:00Z", Tier: &frontier})
			case "t3":
				tick.State = "dispatched"
				tick.Status = statusmodel.WordWritingCode
				tick.Pipeline = []statusmodel.PipelineStage{
					{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
					{Stage: statusmodel.StageWork, State: statusmodel.StageStateActive},
					{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
					{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
				}
				tick.Tries = []statusmodel.Try{{Try: 1, Attempt: 4, Outcome: statusmodel.TryInFlight,
					DispatchedAt: "2026-09-28T19:30:00Z", Tier: &frontier}}
				tick.Tier = &frontier
				tick.DurationSeconds = ptr(int64(1800))
			}
		}
	}
	m.Progress.Ticks.Closed = 3
	m.Progress.Ticks.Open = 2
	m.Groups = &statusmodel.TickGroups{
		Now:    []string{"t3", "t4"},
		Done:   []string{"t1", "t1c", "t2"},
		UpNext: []string{},
		Held:   []string{},
	}
	m.Workers = &[]statusmodel.Worker{{
		TickID: "t3", Attempt: 4, Handle: ptr("herdr pane tick-t3-a4"),
	}}
	return m
}

// ---------------------------------------------------------------------------
// The five scenarios the design names, each rendered at 80x24 and 120x40.
// ---------------------------------------------------------------------------

// dashboardScenarios is the five scenarios the design's verification section
// names, each a hand-built model the renderer is a pure function of —
// a fresh run, a busy wave, a held tick (needs-you box), an ended landed
// run, an ended failed run.
func dashboardScenarios() map[string]statusmodel.Model {
	return map[string]statusmodel.Model{
		"fresh":  scenarioFresh(),
		"busy":   scenarioBusy(),
		"held":   scenarioHeld(),
		"landed": scenarioLanded(),
		"failed": scenarioFailed(),
	}
}

// scenarioFresh is a run that just started: the plan is in, nothing has been
// dispatched, no worker stands, no clock has started — the whole epic is
// still ahead of it.
func scenarioFresh() statusmodel.Model {
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "epic-pln",
		EpicID:        "pln",
		Host:          statusmodel.HostLocal,
		GeneratedAt:   "2026-09-28T19:20:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: true, State: "alive", Reason: "pid 4242", Source: "run.pid",
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseWaves,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateActive},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStatePending},
			},
			Wave: &statusmodel.WaveRef{Active: 1, Total: 1},
			Track: []statusmodel.TrackStep{
				{Label: statusmodel.TrackLabels[0], State: statusmodel.PhaseStateActive},
				{Label: statusmodel.TrackLabels[1], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[2], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[3], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[4], State: statusmodel.PhaseStatePending},
			},
			Here: 0,
		},
		Progress: statusmodel.Progress{
			Ticks: &statusmodel.TickProgress{Total: 3, Closed: 0, Open: 3},
			Waves: &statusmodel.WaveProgress{Total: 1, Done: 0, Active: 1},
		},
		EpicTitle: ptr("the fresh epic's plan"),
		Recent: []runfeed.Event{
			{SchemaVersion: 1, At: "2026-09-28T19:18:00Z", RunID: "epic-pln",
				Stage: reconcile.StageRefreshed, Detail: "pulled in the latest base"},
		},
		Waves: &[]statusmodel.Wave{{
			Wave: 1, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{
				{TickID: "p1", Title: "the first tick of the plan", Gloss: "the first tick of the plan",
					State: "ready", Status: statusmodel.WordUpNext},
				{TickID: "p2", Title: "the second tick of the plan", Gloss: "the second tick of the plan",
					State: "ready", Status: statusmodel.WordUpNext},
				{TickID: "p3", Title: "the third tick of the plan", Gloss: "the third tick of the plan",
					State: "ready", Status: statusmodel.WordUpNext},
			},
		}},
		Groups:    &statusmodel.TickGroups{UpNext: []string{"p1", "p2", "p3"}},
		Workers:   &[]statusmodel.Worker{},
		Attention: []statusmodel.Attention{},
		Health: statusmodel.Health{
			Verdict: statusmodel.HealthVerdict{State: statusmodel.VerdictHealthy},
		},
	}
}

// scenarioBusy is the design's own picture: a busy wave on a cloud run —
// three workers going (two writing, one at its gate, one of them on its
// second, escalated try), three ticks done, twelve up next, the run 55
// minutes in with eight hours left by its own estimate, and a leased
// subscription's window use where a wallet number would lie.
func scenarioBusy() statusmodel.Model {
	strong, frontier := "strong", "frontier"
	one, two := 1, 2
	upNext := []string{"s3r", "wmd", "k4f", "m8q", "p2n", "q7d", "r3t", "u8v", "x1z", "y5b", "z9c", "a2f"}
	upNextGloss := map[string]string{
		"s3r": "kanpla-etl /sync/intraday", "wmd": "paired report capture",
		"k4f": "intraday reconciliation report", "m8q": "eod settlement totals",
		"p2n": "holiday calendar alignment", "q7d": "rate-limit backoff tuning",
		"r3t": "audit log stream", "u8v": "retry queue metrics",
		"x1z": "idempotency keys", "y5b": "schema drift checks",
		"z9c": "backfill runner", "a2f": "alerting hooks",
	}
	ticks := []statusmodel.Tick{
		{TickID: "gzt", Title: "sum same-key kanpla lines", Gloss: "sum same-key kanpla lines",
			State: "closed", Status: statusmodel.WordMerged, DurationSeconds: ptr(int64(1320)),
			Try: &one, Attempt: &one, Tier: &strong,
			Pipeline: donePipeline(statusmodel.PipelineImplement),
			Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
				DispatchedAt: "2026-10-08T12:20:00Z", Tier: &strong}}},
		{TickID: "rtk", Title: "order-feed contract v1", Gloss: "order-feed contract v1",
			State: "closed", Status: statusmodel.WordMerged, DurationSeconds: ptr(int64(2040)),
			Try: &one, Attempt: &one, Tier: &strong,
			Pipeline: donePipeline(statusmodel.PipelineImplement),
			Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
				DispatchedAt: "2026-10-08T12:10:00Z", Tier: &strong}}},
		{TickID: "ww2", Title: "kitchen_order_feed dbt view", Gloss: "kitchen_order_feed dbt view",
			State: "closed", Status: statusmodel.WordMerged, DurationSeconds: ptr(int64(2700)),
			Try: &one, Attempt: &one, Tier: &strong,
			Pipeline: donePipeline(statusmodel.PipelineImplement),
			Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
				DispatchedAt: "2026-10-08T12:00:00Z", Tier: &strong}}},
		{TickID: "tgi", Title: "order-feed service skeleton", Gloss: "order-feed service skeleton",
			State: "dispatched", Status: statusmodel.WordWritingCode, DurationSeconds: ptr(int64(1140)),
			Try: &one, Attempt: &one, Tier: &strong, Executor: ptr("herdr"),
			Pipeline: []statusmodel.PipelineStage{
				{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
				{Stage: statusmodel.StageWork, State: statusmodel.StageStateActive},
				{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
				{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
			},
			Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryInFlight,
				DispatchedAt: "2026-10-08T12:49:00Z", Tier: &strong}}},
		{TickID: "5az", Title: "smoke validation vs kofoed dumps", Gloss: "smoke validation vs kofoed dumps",
			State: "dispatched", Status: statusmodel.WordWritingCode, DurationSeconds: ptr(int64(480)),
			Exception: ptr("attempt 2, model escalated"),
			Try:       &two, Attempt: &two, Tier: &frontier, Executor: ptr("herdr"),
			Pipeline: []statusmodel.PipelineStage{
				{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
				{Stage: statusmodel.StageWork, State: statusmodel.StageStateActive},
				{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
				{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
			},
			Tries: []statusmodel.Try{
				{Try: 1, Attempt: 1, Outcome: statusmodel.TryRejected,
					DispatchedAt: "2026-10-08T12:55:00Z", Tier: &strong,
					Reason: ptr("the smoke fixtures did not match")},
				{Try: 2, Attempt: 2, Outcome: statusmodel.TryInFlight,
					DispatchedAt: "2026-10-08T13:00:00Z", Tier: &frontier}}},
		{TickID: "v1g", Title: "safe intraday kanpla pulls", Gloss: "safe intraday kanpla pulls",
			State: "dispatched", Status: statusmodel.WordTesting, DurationSeconds: ptr(int64(1920)),
			Try: &one, Attempt: &one, Tier: &strong,
			Pipeline: []statusmodel.PipelineStage{
				{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
				{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
				{Stage: statusmodel.StageGate, State: statusmodel.StageStateActive},
				{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
			},
			Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryInFlight,
				DispatchedAt: "2026-10-08T12:36:00Z", Tier: &strong}}},
	}
	// The twelve up-next ticks wait in the second wave; the rest of the
	// epic's waves are still empty placeholders the shape carries.
	queued := make([]statusmodel.Tick, 0, len(upNext))
	for _, id := range upNext {
		queued = append(queued, statusmodel.Tick{
			TickID: id, Title: upNextGloss[id], Gloss: upNextGloss[id],
			State: "ready", Status: statusmodel.WordUpNext,
		})
	}
	waves := []statusmodel.Wave{
		{Wave: 1, State: statusmodel.WaveActive, Ticks: ticks},
		{Wave: 2, State: statusmodel.WaveUpcoming, Ticks: queued},
	}
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "run_9g5feedv1c0ffee",
		EpicID:        "9g5",
		Host:          statusmodel.HostCloud,
		GeneratedAt:   "2026-10-08T13:08:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: true, State: "running", Reason: "the Workflow hosts this run",
			Source: "workflow-record",
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseWaves,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateActive},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStatePending},
			},
			Wave: &statusmodel.WaveRef{Active: 1, Total: 2},
			Track: []statusmodel.TrackStep{
				{Label: statusmodel.TrackLabels[0], State: statusmodel.PhaseStateActive},
				{Label: statusmodel.TrackLabels[1], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[2], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[3], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[4], State: statusmodel.PhaseStatePending},
			},
			Here: 0,
		},
		Progress: statusmodel.Progress{
			Ticks:             &statusmodel.TickProgress{Total: 18, Closed: 3, Open: 15},
			Waves:             &statusmodel.WaveProgress{Total: 2, Done: 0, Active: 1},
			RunElapsedSeconds: ptr(int64(3300)),
		},
		EpicTitle: ptr("order feed v1"),
		RunConfig: ptr("claude"),
		Recent: []runfeed.Event{
			{SchemaVersion: 1, At: "2026-10-08T13:02:00Z", RunID: "run_9g5feedv1c0ffee",
				TickID: ptr("ww2"), Attempt: &one, Stage: reconcile.StageClosed,
				Detail: "ww2 merged"},
			{SchemaVersion: 1, At: "2026-10-08T13:08:00Z", RunID: "run_9g5feedv1c0ffee",
				TickID: ptr("v1g"), Attempt: &one, Stage: reconcile.StageIntegrated,
				Detail: "v1g merged into the epic"},
		},
		Waves:  &waves,
		Groups: &statusmodel.TickGroups{Now: []string{"tgi", "5az", "v1g"}, Done: []string{"gzt", "rtk", "ww2"}, UpNext: upNext, Held: []string{}},
		Workers: &[]statusmodel.Worker{
			{TickID: "tgi", Attempt: 1, Handle: ptr("tgi-worker"),
				LastTurn: ptr("assistant: adding the /feed handler"),
				Activity: &statusmodel.WorkerActivity{WindowSeconds: 600,
					Buckets: []int{2, 4, 6, 8, 7, 5, 3, 2, 1, 1}}},
			{TickID: "5az", Attempt: 2, Handle: ptr("5az-worker"),
				Activity: &statusmodel.WorkerActivity{WindowSeconds: 600,
					Buckets:    []int{1, 2, 3, 5, 8, 8, 6, 4, 2, 1},
					LastAction: ptr("running pytest"), LastActionAt: ptr("2026-10-08T13:06:00Z")}},
		},
		WaitsOn:   &statusmodel.Wait{Kind: statusmodel.WaitWorkers, What: "3 in-flight attempt(s)"},
		Attention: []statusmodel.Attention{},
		Health: statusmodel.Health{
			Verdict: statusmodel.HealthVerdict{State: statusmodel.VerdictHealthy},
		},
		Cost: statusmodel.Cost{
			Subscription: &statusmodel.CostSubscription{Label: "MAX1", FiveHour: ptr(0.34)},
		},
		Remaining: &statusmodel.Remaining{
			ApproximateSeconds: 28800,
			Basis:              "15 open ticks x the median of the closed ones",
		},
	}
}

// scenarioHeld is a busy wave with a hold standing: the run stopped for a
// person, the attempt struck out, and the box naming the one command that
// releases it.
func scenarioHeld() statusmodel.Model {
	strong := "strong"
	one, two := 1, 2
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "epic-hld",
		EpicID:        "hld",
		Host:          statusmodel.HostLocal,
		GeneratedAt:   "2026-09-28T19:20:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: true, State: "alive", Reason: "pid 4242", Source: "run.pid",
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseWaves,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateActive},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStatePending},
			},
			Wave: &statusmodel.WaveRef{Active: 1, Total: 2},
			Track: []statusmodel.TrackStep{
				{Label: statusmodel.TrackLabels[0], State: statusmodel.PhaseStateActive},
				{Label: statusmodel.TrackLabels[1], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[2], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[3], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[4], State: statusmodel.PhaseStatePending},
			},
			Here: 0,
		},
		Progress: statusmodel.Progress{
			Ticks:             &statusmodel.TickProgress{Total: 5, Closed: 1, Open: 4},
			Waves:             &statusmodel.WaveProgress{Total: 2, Done: 0, Active: 1},
			RunElapsedSeconds: ptr(int64(2700)),
		},
		EpicTitle: ptr("the held epic's work"),
		Recent: []runfeed.Event{
			{SchemaVersion: 1, At: "2026-09-28T19:10:00Z", RunID: "epic-hld",
				TickID: ptr("h3"), Attempt: &one, Stage: reconcile.StageDispatched,
				Detail: "h3 try 1 dispatched"},
			{SchemaVersion: 1, At: "2026-09-28T19:16:00Z", RunID: "epic-hld",
				TickID: ptr("h2"), Attempt: &two, Stage: reconcile.StageRunHeld,
				Detail: "attempt_unaddressed: nobody can say whether the attempt is running"},
		},
		Waves: &[]statusmodel.Wave{{
			Wave: 1, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{
				{TickID: "h1", Title: "the first tick", Gloss: "the first tick",
					State: "closed", Status: statusmodel.WordMerged, DurationSeconds: ptr(int64(600)),
					Try: &one, Attempt: &one, Tier: &strong,
					Pipeline: donePipeline(statusmodel.PipelineImplement),
					Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
						DispatchedAt: "2026-09-28T19:00:00Z", Tier: &strong}}},
				{TickID: "h2", Title: "the held tick", Gloss: "the held tick",
					State: "dispatched", Status: statusmodel.WordHeldPrefix + "the attempt was struck out for release",
					DurationSeconds: ptr(int64(1200)),
					Try:             &two, Attempt: &two, Tier: &strong,
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateActive},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
					},
					Tries: []statusmodel.Try{
						{Try: 1, Attempt: 1, Outcome: statusmodel.TryRejected,
							DispatchedAt: "2026-09-28T19:04:00Z", Tier: &strong,
							Reason: ptr("the fixture drift was real")},
						{Try: 2, Attempt: 2, Outcome: statusmodel.TryDispatched,
							DispatchedAt: "2026-09-28T19:10:00Z", Tier: &strong}}},
				{TickID: "h3", Title: "the running tick", Gloss: "the running tick",
					State: "dispatched", Status: statusmodel.WordWritingCode, DurationSeconds: ptr(int64(600)),
					Try: &one, Attempt: &one, Tier: &strong, Executor: ptr("herdr"),
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateActive},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
					},
					Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryInFlight,
						DispatchedAt: "2026-09-28T19:10:00Z", Tier: &strong}}},
				{TickID: "h4", Title: "the queued tick", Gloss: "the queued tick",
					State: "ready", Status: statusmodel.WordUpNext},
				{TickID: "h5", Title: "the last queued tick", Gloss: "the last queued tick",
					State: "ready", Status: statusmodel.WordUpNext},
			},
		}},
		Groups: &statusmodel.TickGroups{Now: []string{"h3"}, Done: []string{"h1"}, UpNext: []string{"h4", "h5"}, Held: []string{"h2"}},
		Workers: &[]statusmodel.Worker{{TickID: "h3", Attempt: 1, Handle: ptr("h3-worker"),
			Activity: &statusmodel.WorkerActivity{WindowSeconds: 600,
				Buckets:    []int{0, 1, 2, 3, 5, 8, 6, 4, 2, 1},
				LastAction: ptr("editing internal/x.go"), LastActionAt: ptr("2026-09-28T19:19:00Z")}}},
		WaitsOn: &statusmodel.Wait{
			Kind:  statusmodel.WaitHeldForPerson,
			What:  "attempt 2 of h2 was struck out for release",
			Since: ptr("2026-09-28T19:16:00Z"), NeedsPerson: true,
			UnblockCommand: ptr(`ticfac settle hld h2 2 --release "<who>"`),
		},
		Attention: []statusmodel.Attention{{
			Kind:  statusmodel.WaitHeldForPerson,
			What:  "attempt 2 of h2 was struck out for release",
			Since: ptr("2026-09-28T19:16:00Z"), NeedsPerson: true,
			UnblockCommand: ptr(`ticfac settle hld h2 2 --release "<who>"`),
		}},
		Health: statusmodel.Health{
			Verdict: statusmodel.HealthVerdict{State: statusmodel.VerdictHealthy},
		},
		Remaining: &statusmodel.Remaining{
			ApproximateSeconds: 3600,
			Basis:              "4 open ticks x the median of the closed ones",
		},
	}
}

// scenarioLanded is an ended run whose epic landed: every tick closed and
// merged, the PR merged, the run's own clock stopped at its end.
func scenarioLanded() statusmodel.Model {
	strong := "strong"
	one := 1
	ticks := []statusmodel.Tick{}
	durations := map[string]int64{"l1": 720, "l2": 1200, "l3": 540, "l4": 1860}
	glosses := map[string]string{
		"l1": "the first landed tick", "l2": "the second landed tick",
		"l3": "the third landed tick", "l4": "the fourth landed tick",
	}
	for _, id := range []string{"l1", "l2", "l3", "l4"} {
		ticks = append(ticks, statusmodel.Tick{
			TickID: id, Title: glosses[id], Gloss: glosses[id],
			State: "closed", Status: statusmodel.WordMerged, DurationSeconds: ptr(durations[id]),
			Try: &one, Attempt: &one, Tier: &strong,
			Pipeline: donePipeline(statusmodel.PipelineImplement),
			Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
				DispatchedAt: "2026-09-28T17:00:00Z", Tier: &strong}}},
		)
	}
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "epic-lnd",
		EpicID:        "lnd",
		Host:          statusmodel.HostLocal,
		GeneratedAt:   "2026-09-28T19:20:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: false, State: "completed", Reason: "the run finished", Source: "run.pid",
			LastEvent: &runfeed.Event{SchemaVersion: 1, At: "2026-09-28T19:12:00Z", RunID: "epic-lnd",
				Stage: reconcile.StageRunFinished, Detail: "completed: every tick closed behind the gate"},
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseDone,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStateDone},
			},
			Track: []statusmodel.TrackStep{
				{Label: statusmodel.TrackLabels[0], State: statusmodel.PhaseStateDone},
				{Label: statusmodel.TrackLabels[1], State: statusmodel.PhaseStateDone},
				{Label: statusmodel.TrackLabels[2], State: statusmodel.PhaseStateDone},
				{Label: statusmodel.TrackLabels[3], State: statusmodel.PhaseStateDone},
				{Label: statusmodel.TrackLabels[4], State: statusmodel.PhaseStateDone},
			},
			Here: 4,
		},
		Progress: statusmodel.Progress{
			Ticks:             &statusmodel.TickProgress{Total: 4, Closed: 4, Open: 0},
			Waves:             &statusmodel.WaveProgress{Total: 1, Done: 1, Active: 0},
			RunElapsedSeconds: ptr(int64(7740)),
		},
		EpicTitle: ptr("the landed epic"),
		RunConfig: ptr("claude"),
		Recent: []runfeed.Event{
			{SchemaVersion: 1, At: "2026-09-28T19:10:00Z", RunID: "epic-lnd",
				TickID: ptr("l4"), Attempt: &one, Stage: reconcile.StageClosed,
				Detail: "l4 merged"},
			{SchemaVersion: 1, At: "2026-09-28T19:11:00Z", RunID: "epic-lnd",
				Stage: reconcile.StageLanded, Detail: "the epic is merged"},
		},
		Waves:     &[]statusmodel.Wave{{Wave: 1, State: statusmodel.WaveDone, Ticks: ticks}},
		Groups:    &statusmodel.TickGroups{Done: []string{"l1", "l2", "l3", "l4"}},
		Workers:   &[]statusmodel.Worker{},
		Attention: []statusmodel.Attention{},
		Health: statusmodel.Health{
			Verdict: statusmodel.HealthVerdict{State: statusmodel.VerdictHealthy},
		},
		Cost: statusmodel.Cost{
			RecordedUSD: ptr(0.41), Attempts: 4, Basis: "usage recorded on decision records",
			Lines: []statusmodel.CostLine{{Source: statusmodel.CostSourceWorkersAI,
				Metered: true, USD: ptr(0.41), Attempts: 4, Basis: "gateway usage for 4 dispatches"}},
		},
	}
}

// scenarioFailed is an ended run that failed: one tick closed, one refused
// at its gate, the rest never started, the run's own terminal word in the
// header and the resume a person can type in the box.
func scenarioFailed() statusmodel.Model {
	strong := "strong"
	one, two := 1, 2
	return statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "epic-fld",
		EpicID:        "fld",
		Host:          statusmodel.HostLocal,
		GeneratedAt:   "2026-09-28T19:20:00Z",
		Degraded:      []string{},
		Liveness: statusmodel.Liveness{
			Alive: false, State: "failed", Reason: "the run finished failed", Source: "run.pid",
			LastEvent: &runfeed.Event{SchemaVersion: 1, At: "2026-09-28T19:12:00Z", RunID: "epic-fld",
				Stage: reconcile.StageRunFinished, Detail: "failed: f2 did not pass"},
		},
		Lifecycle: statusmodel.Lifecycle{
			Phase: statusmodel.PhaseFailed,
			Phases: []statusmodel.PhaseState{
				{Phase: statusmodel.PhasePlan, State: statusmodel.PhaseStateDone},
				{Phase: statusmodel.PhaseWaves, State: statusmodel.PhaseStateActive},
				{Phase: statusmodel.PhaseReview, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCloseout, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseCI, State: statusmodel.PhaseStatePending},
				{Phase: statusmodel.PhaseMerge, State: statusmodel.PhaseStatePending},
			},
			Wave: &statusmodel.WaveRef{Active: 1, Total: 1},
			Track: []statusmodel.TrackStep{
				{Label: statusmodel.TrackLabels[0], State: statusmodel.PhaseStateActive},
				{Label: statusmodel.TrackLabels[1], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[2], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[3], State: statusmodel.PhaseStatePending},
				{Label: statusmodel.TrackLabels[4], State: statusmodel.PhaseStatePending},
			},
			Here: 0,
		},
		Progress: statusmodel.Progress{
			Ticks:             &statusmodel.TickProgress{Total: 4, Closed: 1, Open: 3},
			Waves:             &statusmodel.WaveProgress{Total: 1, Done: 0, Active: 1},
			RunElapsedSeconds: ptr(int64(4320)),
		},
		EpicTitle: ptr("the failed epic's run"),
		Recent: []runfeed.Event{
			{SchemaVersion: 1, At: "2026-09-28T19:09:00Z", RunID: "epic-fld",
				TickID: ptr("f2"), Attempt: &two, Stage: reconcile.StageGateFailed,
				Detail: "the integrated gate refused attempt 2 of f2 (go)"},
			{SchemaVersion: 1, At: "2026-09-28T19:12:00Z", RunID: "epic-fld",
				Stage: reconcile.StageRunFinished, Detail: "failed: f2 did not pass"},
		},
		Waves: &[]statusmodel.Wave{{
			Wave: 1, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{
				{TickID: "f1", Title: "the landed tick", Gloss: "the landed tick",
					State: "closed", Status: statusmodel.WordMerged, DurationSeconds: ptr(int64(900)),
					Try: &one, Attempt: &one, Tier: &strong,
					Pipeline: donePipeline(statusmodel.PipelineImplement),
					Tries: []statusmodel.Try{{Try: 1, Attempt: 1, Outcome: statusmodel.TryClosed,
						DispatchedAt: "2026-09-28T18:10:00Z", Tier: &strong}}},
				{TickID: "f2", Title: "the refused tick", Gloss: "the refused tick",
					State: "dispatched", Status: statusmodel.WordFailedPrefix + "the integrated gate refused attempt 2",
					DurationSeconds: ptr(int64(1500)),
					Try:             &two, Attempt: &two, Tier: &strong,
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStateFailed},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
					},
					Tries: []statusmodel.Try{
						{Try: 1, Attempt: 1, Outcome: statusmodel.TryRejected,
							DispatchedAt: "2026-09-28T18:20:00Z", Tier: &strong,
							Reason: ptr("the migration was missing")},
						{Try: 2, Attempt: 2, Outcome: statusmodel.TryGateFailed,
							DispatchedAt: "2026-09-28T18:50:00Z", Tier: &strong,
							Reason: ptr("the integrated gate refused attempt 2 (go)")}}},
				{TickID: "f3", Title: "the waiting tick", Gloss: "the waiting tick",
					State: "ready", Status: statusmodel.WordWaitingPrefix + "the run is not going"},
				{TickID: "f4", Title: "the other waiting tick", Gloss: "the other waiting tick",
					State: "ready", Status: statusmodel.WordWaitingPrefix + "the run is not going"},
			},
		}},
		Groups:  &statusmodel.TickGroups{Done: []string{"f1"}, UpNext: []string{"f3", "f4"}, Held: []string{"f2"}},
		Workers: &[]statusmodel.Worker{},
		WaitsOn: &statusmodel.Wait{
			Kind: statusmodel.WaitDeadRun, NeedsPerson: true,
			What:           "run epic-fld failed: f2 did not pass",
			UnblockCommand: ptr("ticfac run-epic fld --resume"),
		},
		Attention: []statusmodel.Attention{{
			Kind: statusmodel.WaitDeadRun, NeedsPerson: true,
			What:           "run epic-fld failed: f2 did not pass",
			UnblockCommand: ptr("ticfac run-epic fld --resume"),
		}},
		Health: statusmodel.Health{
			Verdict: statusmodel.HealthVerdict{State: statusmodel.VerdictStopped},
		},
	}
}

// donePipeline is every stage of a pipeline done — the shape a closed tick's
// cell carries.
func donePipeline(stages []string) []statusmodel.PipelineStage {
	out := make([]statusmodel.PipelineStage, len(stages))
	for i, stage := range stages {
		out[i] = statusmodel.PipelineStage{Stage: stage, State: statusmodel.StageStateDone}
	}
	return out
}

// dashRowMatches says whether one rendered frame line is the row of one
// named tick — the row's leading mark ("▸ " for the cursor's row, the
// two-space indent otherwise), then the id (with the absorbed "+" when the
// run absorbed it). Matching is by the row's own head, never by content the
// row could share with another line.
func dashRowMatches(line, id string) bool {
	rest, ok := strings.CutPrefix(line, "▸ ")
	if !ok {
		rest, ok = strings.CutPrefix(line, "  ")
		if !ok {
			return false
		}
	}
	if rest == id {
		return true // the id alone: a row whose other columns were dropped
	}
	return strings.HasPrefix(rest, id+" ") || strings.HasPrefix(rest, "+"+id+" ")
}

// dashRowIDs is the sequence in which the named ticks' rows appear in the
// frame — the row order the grouping tests read.
func dashRowIDs(frame []string, ids []string) []string {
	order := []string{}
	for _, line := range frame {
		for _, id := range ids {
			if dashRowMatches(line, id) {
				order = append(order, id)
				break
			}
		}
	}
	return order
}

// dashRowLine is the frame line one named tick's row rendered on, or -1.
func dashRowLine(frame []string, id string) int {
	for i, line := range frame {
		if dashRowMatches(line, id) {
			return i
		}
	}
	return -1
}

// TestDashboardScenarioGolden: the five scenarios the design names render at
// 80x24 and at 120x40, and every frame matches its pinned testdata file byte
// for byte — the whole layout, spacing, grouping and truncation included,
// because a dashboard a person reads is a layout, and a layout that drifts
// silently is a layout nobody agreed on. `-update` regenerates the files.
func TestDashboardScenarioGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		scenario func() statusmodel.Model
	}{
		{"fresh", scenarioFresh},
		{"busy", scenarioBusy},
		{"held", scenarioHeld},
		{"landed", scenarioLanded},
		{"failed", scenarioFailed},
	} {
		for _, size := range []struct {
			width, height int
			suffix        string
		}{
			{120, 40, "120"},
			{80, 24, "80"},
		} {
			t.Run(tc.name+"_"+size.suffix, func(t *testing.T) {
				t.Parallel()
				frame := renderWatchFrame(tc.scenario(), plainStyles(), size.width, size.height, "")
				got := strings.Join(frame, "\n") + "\n"
				path := filepath.Join("testdata", "watch_scenario_"+tc.name+"_"+size.suffix+".txt")
				if *updateGoldens {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatalf("write %s: %v", path, err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s (regenerate with -update): %v", path, err)
				}
				if got != string(want) {
					t.Errorf("the %s scenario at %dx%d does not match %s byte for byte:\n--- got ---\n%s\n--- want ---\n%s",
						tc.name, size.width, size.height, path, got, want)
				}
			})
		}
	}
}

// TestDashboardScenariosAnswerTheThreeQuestions: each scenario's frame, read
// the way the design says a frame is read — does anything need me, is it
// healthy and how far along, what is happening now — in that order, top to
// bottom.
func TestDashboardScenariosAnswerTheThreeQuestions(t *testing.T) {
	t.Parallel()
	for name, m := range dashboardScenarios() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			frame := renderWatchFrame(m, plainStyles(), 120, 40, "")
			joined := strings.Join(frame, "\n")
			// The needs-you answer is the frame's first question: nothing
			// needs a person and it is said first; a hold stands in a box
			// above the health line.
			needsFirst := false
			needsLine := strings.Index(joined, "Needs you:")
			boxLine := strings.Index(joined, "│ Needs you:")
			healthLine := strings.Index(joined, "● ")
			if needsLine >= 0 && healthLine >= 0 {
				needsFirst = needsLine < healthLine
			}
			if boxLine >= 0 && healthLine >= 0 {
				needsFirst = needsFirst || boxLine < healthLine
			}
			if !needsFirst {
				t.Errorf("the %s frame does not answer needs-you first:\n%s", name, joined)
			}
			// The health line carries the verdict and the progress count.
			if m.Progress.Ticks != nil && m.Progress.Ticks.Total > 0 {
				want := fmt.Sprintf("%d of %d done", m.Progress.Ticks.Closed, m.Progress.Ticks.Total)
				if !strings.Contains(joined, want) {
					t.Errorf("the %s frame's health line does not carry %q:\n%s", name, want, joined)
				}
			}
			// The phase track is on the frame with its marker.
			if !strings.Contains(joined, "▲ here") {
				t.Errorf("the %s frame carries no you-are-here marker:\n%s", name, joined)
			}
		})
	}
}

// TestDashboardGroupsFollowTheModel: the rows are exactly the model's own
// groups, in the model's group order (NOW, DONE, UP NEXT, HELD), each
// group's rows in the order its ids carry — and a successor's rows move
// between groups as the states advance, which is the whole point of
// grouping: the frame answers "what is happening now" first, not "what is
// the plan's order".
func TestDashboardGroupsFollowTheModel(t *testing.T) {
	t.Parallel()
	now := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	if got := dashRowIDs(now, []string{"t2", "t4", "t1", "t1c"}); strings.Join(got, " ") != "t2 t4 t1 t1c" {
		t.Errorf("the rows are not in group order (NOW t2 t4, DONE t1 t1c): %v\n%s", got, strings.Join(now, "\n"))
	}
	// The UP NEXT group is not rows at all: it is one collapsed line naming
	// what is queued.
	if !strings.Contains(strings.Join(now, "\n"), "UP NEXT (1)   t3 the third tick's gloss") {
		t.Errorf("the queued tick is not named on the collapsed UP NEXT line:\n%s", strings.Join(now, "\n"))
	}
	later := renderWatchFrame(dashboardSuccessor(), plainStyles(), 0, 0, "")
	if got := dashRowIDs(later, []string{"t3", "t4", "t1", "t1c", "t2"}); strings.Join(got, " ") != "t3 t4 t1 t1c t2" {
		t.Errorf("the successor's rows are not in its groups' order: %v\n%s", got, strings.Join(later, "\n"))
	}
	// The states did advance between the two frames — the pin holds real
	// change, not two renders of one model.
	if strings.Join(now, "\n") == strings.Join(later, "\n") {
		t.Error("the successor frame is identical to its predecessor: the pin tested nothing")
	}
	// Each group announces itself, and the counted groups carry their
	// counts.
	for _, want := range []string{"NOW", "DONE (2)", "UP NEXT (1)"} {
		if !strings.Contains(strings.Join(now, "\n"), want) {
			t.Errorf("the frame does not announce %q:\n%s", want, strings.Join(now, "\n"))
		}
	}
}

// TestDashboardRowsCarryStatusWords: every row reads its status word — the
// model's own, with the exception note inline in parentheses when one
// applies — and the words sit in their own aligned column.
func TestDashboardRowsCarryStatusWords(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")
	for _, want := range []string{
		"testing (attempt 2, model escalated)",
		"merged",
		"reviewing",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the frame does not carry the status word %q:\n%s", want, joined)
		}
	}
	// The exception rides the status word inline, in its parentheses — never
	// a column of its own.
	t2 := frame[dashRowLine(frame, "t2")]
	if !strings.Contains(t2, "testing (attempt 2, model escalated)") {
		t.Errorf("t2's row does not carry its exception inline: %q", t2)
	}
}

// TestDashboardRowsCarryTheWorkersExcerpt (tick 93): each running worker's
// row shows a one-line excerpt of what it is doing now — the activity's
// last action, else the last turn's sentence with the role prefix
// stripped — and a tick at its gate with no worker shows the run's own word
// for the stage the run itself is driving.
func TestDashboardRowsCarryTheWorkersExcerpt(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, `"ran go test ./internal/reconcile"`) {
		t.Errorf("t2's worker excerpt is not on its row:\n%s", joined)
	}
	// A gate the run itself is driving: no worker, the run's own word.
	m2 := dashboardFixture()
	m2.Workers = &[]statusmodel.Worker{}
	joined2 := strings.Join(renderWatchFrame(m2, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined2, "gate running") {
		t.Errorf("a gate with no worker is not named on its row:\n%s", joined2)
	}
	// A worker whose activity carries no action but a turn: the turn's
	// sentence, role prefix stripped.
	m := dashboardFixture()
	m.Workers = &[]statusmodel.Worker{{
		TickID: "t2", Attempt: 2,
		LastTurn: ptr("assistant: reading the reconciliation code"),
	}}
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, `"reading the reconciliation code"`) {
		t.Errorf("the worker's turn is not excerpted on its row:\n%s", joined)
	}
	// No worker, no gate: no excerpt at all, never a guess.
	m.Workers = &[]statusmodel.Worker{}
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, `"`) {
		t.Errorf("a frame with no standing worker still quotes an excerpt:\n%s", joined)
	}
}

// TestDashboardTrackCarriesTheMarker: the phase track renders the model's
// five steps on one line, with the you-are-here marker under the step the
// model says the epic is in — and the wave the run is in beside the marker
// when the model carries one.
func TestDashboardTrackCarriesTheMarker(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")
	for _, want := range []string{
		"Building", "Reviewing", "Closing out", "PR & CI", "Merged",
		"▲ here (wave 2 of 3)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the track does not carry %q:\n%s", want, joined)
		}
	}
	// The marker sits under the step the model names: the track line's
	// "Building" and the marker's line start at the same column.
	trackLine, markerLine := "", ""
	for _, line := range frame {
		if strings.Contains(line, "Building") && trackLine == "" {
			trackLine = line
		}
		if strings.Contains(line, "▲ here") && markerLine == "" {
			markerLine = line
		}
	}
	if trackLine == "" || markerLine == "" {
		t.Fatalf("the track or its marker is missing:\n%s", joined)
	}
	if at := strings.Index(trackLine, "Building"); at < 0 || !strings.HasPrefix(markerLine, strings.Repeat(" ", at)) {
		t.Errorf("the marker is not under the step the model names:\ntrack  %s\nmarker %s", trackLine, markerLine)
	}
}

// TestDashboardNeedsYou: the first question. Nothing needs a person and the
// header says so, dim and quiet, beside the health line. A hold shows in a
// box above the health line — needs-you is the most prominent thing on
// screen when non-empty — naming what is held and the one command that
// clears it.
func TestDashboardNeedsYou(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	frame := renderWatchFrame(m, plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "Needs you: nothing") {
		t.Errorf("nothing-needs-you is not shown when true:\n%s", joined)
	}
	coloured := strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(coloured, "\x1b[2mNeeds you: nothing\x1b[0m") {
		t.Errorf("nothing-needs-you is not quiet (dim):\n%s", coloured)
	}

	m.Attention = []statusmodel.Attention{{
		Kind:           statusmodel.WaitHeldForPerson,
		What:           "attempt 2 of t2 struck out: the refusal the run recorded",
		NeedsPerson:    true,
		UnblockCommand: ptr(`ticfac settle rmod t2 2 --release "<who>"`),
	}}
	frame = renderWatchFrame(m, plainStyles(), 0, 0, "")
	joined = strings.Join(frame, "\n")
	if strings.Contains(joined, "Needs you: nothing") {
		t.Errorf("a held run still says nothing needs a person:\n%s", joined)
	}
	for _, want := range []string{
		"│ Needs you: attempt 2 of t2 struck out: the refusal the run recorded ",
		`│ clear with: ticfac settle rmod t2 2 --release "<who>" `,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the box does not carry %q:\n%s", want, joined)
		}
	}
	// The box is a box: its borders meet.
	if !strings.Contains(joined, "┌") || !strings.Contains(joined, "└") {
		t.Errorf("the hold is not drawn in a box:\n%s", joined)
	}
	coloured = strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(coloured, "\x1b[31m│ \x1b[1mNeeds you: attempt") {
		t.Errorf("the box's announcement is not red and bold:\n%s", coloured)
	}
	// The box comes before the health line — the first question first.
	if strings.Index(joined, "│ Needs you:") > strings.Index(joined, "● healthy") {
		t.Errorf("the needs-you box is not above the health line:\n%s", joined)
	}
}

// TestDashboardHoldCommandSurvivesNarrowPanes: a hold's clearing command
// cannot fall out of the dashboard because the pane is narrow (tick 9um).
// The box wraps its lines inside the box instead of truncating them, so
// every word of the command stays on the screen.
func TestDashboardHoldCommandSurvivesNarrowPanes(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Attention = []statusmodel.Attention{{
		Kind:           statusmodel.WaitHeldForPerson,
		What:           "attempt 2 of t2 struck out: the refusal the run recorded",
		NeedsPerson:    true,
		UnblockCommand: ptr(`ticfac settle rmod t2 2 --release "<who>"`),
	}}

	// A pane that seats the lines keeps them whole.
	for _, width := range []int{0, 120} {
		frame := renderWatchFrame(m, plainStyles(), width, 0, "")
		joined := strings.Join(frame, "\n")
		for _, want := range []string{
			"│ Needs you: attempt 2 of t2 struck out: the refusal the run recorded ",
			`│ clear with: ticfac settle rmod t2 2 --release "<who>" `,
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("at width %d the box does not carry %q:\n%s", width, want, joined)
			}
		}
	}

	// A 40-column pane: the wrap keeps the announcement and every word of
	// the command, each line inside the box and the box inside the pane,
	// nothing cut mid-word.
	frame := renderWatchFrame(m, plainStyles(), 40, 0, "")
	joined := strings.Join(frame, "\n")
	for _, word := range []string{"ticfac", "settle", "rmod", "--release", `"<who>"`,
		"attempt", "struck", "refusal", "recorded"} {
		if !strings.Contains(joined, word) {
			t.Errorf("at width 40 the clearing command lost %q:\n%s", word, joined)
		}
	}
	for i, line := range frame {
		if w := ansi.StringWidth(line); w > 40 {
			t.Errorf("frame line %d is %d cells wide in a 40-column pane: %q", i, w, line)
		}
	}
	// The box's whole frame is one colour, the announcement bold.
	coloured := renderWatchFrame(m, ansiWatchStyles(), 40, 0, "")
	for i, line := range coloured {
		if i == 0 || !strings.Contains(ansi.Strip(line), "│") {
			continue
		}
		if !strings.Contains(line, "\x1b[31m") {
			t.Errorf("the box's line %d is not red: %q", i, ansi.Strip(line))
		}
	}
}

// TestDashboardShortPaneKeepsHoldBlocksWhole: when a short pane cannot carry
// every hold, the height fit drops a later hold's box whole rather than
// cutting one in half — a hold shown without the command that clears it is
// the defect the wrap exists to prevent (tick 9um), so what the frame shows
// of a hold is all of it, and a hold that does not fit is not shown at all.
func TestDashboardShortPaneKeepsHoldBlocksWhole(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Attention = []statusmodel.Attention{
		{
			Kind:           statusmodel.WaitHeldForPerson,
			What:           "attempt 2 of t2 struck out for release",
			NeedsPerson:    true,
			UnblockCommand: ptr("ticfac settle rmod t2 2 --release x"),
		},
		{
			Kind:           statusmodel.WaitHeldForPerson,
			What:           "a findings draft on t2 waits for triage",
			NeedsPerson:    true,
			UnblockCommand: ptr("ticfac triage rmod"),
		},
	}
	// The second box is four lines; a pane that seats only the first keeps
	// the second off the frame entirely, never half of it — the needs-you
	// boxes outrank even the health line's and the track's seats.
	frame := renderWatchFrame(m, plainStyles(), 80, 8, "")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "attempt 2 of t2 struck out for release") {
		t.Errorf("the first hold's box is missing:\n%s", joined)
	}
	if strings.Contains(joined, "findings draft") {
		t.Errorf("the second hold's box was cut rather than dropped:\n%s", joined)
	}
	if got := len(frame); got > 8 {
		t.Errorf("a 8-line pane rendered %d lines:\n%s", got, joined)
	}
}

// TestDashboardRendersTheMeteredRiverOnly (tick b13): the dashboard's cost
// line shows what is METERED and nothing else. A partially joined river
// renders its measured number alone — its unmeasured remainder is silence
// on the glance, never a fabricated number beside it — and the full split,
// with the coverage bases that say the number covers 2 of 32 attempts, stays
// in the model's own lines where `status --json` and the drill-in surfaces
// read it.
func TestDashboardRendersTheMeteredRiverOnly(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Cost.Lines = []statusmodel.CostLine{
		{Source: statusmodel.CostSourceWorkersAI, Metered: true, USD: ptr(0.12), Attempts: 2,
			Basis: "AI Gateway logs: the calls of 2 of 32 attempts joined the gateway"},
		{Source: statusmodel.CostSourceWorkersAI, Metered: false, USD: nil, Attempts: 30,
			Basis: "not metered: dispatched without the gateway metering join"},
	}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "Workers AI $0.12") {
		t.Errorf("a partially joined river does not render its measured number:\n%s", joined)
	}
	if strings.Contains(joined, "not metered") {
		t.Errorf("the cost line recites what nobody measured (tick b13: nothing when there is no metered cost):\n%s", joined)
	}
	// The model still carries the split: the number claims only its 2 joined
	// attempts, and the 30 unjoined ones keep their honest line.
	if len(m.Cost.Lines) != 2 || m.Cost.Lines[1].Metered || m.Cost.Lines[1].USD != nil {
		t.Errorf("the model's own split was lost: %+v", m.Cost.Lines)
	}
}

// TestDashboardCostLineRendersTheSubscription (tick b13): on a run whose
// jobs lease the claude-sub subscription, the cost line shows the leased
// label and its 5h/7d window utilization — the shape the design names,
// "MAX1 · 34% of 5h" — beside any wallet money the run did spend, and
// instead of a $0.00 when it spent none. A window the factory's proxy has
// not answered for is absent, never 0%, and a run with no lease has no
// subscription segment at all.
func TestDashboardCostLineRendersTheSubscription(t *testing.T) {
	t.Parallel()
	name := "claude"
	m := statusmodel.Model{RunConfig: &name}

	// The subscription alone: the whole line is the label and its windows.
	m.Cost.Subscription = &statusmodel.CostSubscription{
		Label: "MAX1", FiveHour: ptr(0.34), SevenDay: ptr(0.08)}
	if got := dashCost(m, plainStyles()); got != "config claude · MAX1 · 34% of 5h · 8% of 7d" {
		t.Errorf("a claude-sub run's cost line is %q, want the leased label and its window use", got)
	}

	// A window not yet measured is absent, never a measured zero.
	m.Cost.Subscription = &statusmodel.CostSubscription{Label: "MAX1", FiveHour: ptr(0.34)}
	if got := dashCost(m, plainStyles()); got != "config claude · MAX1 · 34% of 5h" {
		t.Errorf("the 7d window without a measurement is %q, want only the 5h window", got)
	}
	m.Cost.Subscription = &statusmodel.CostSubscription{Label: "MAX1"}
	if got := dashCost(m, plainStyles()); got != "config claude · MAX1" {
		t.Errorf("a leased label with no utilization yet renders %q, want the label alone", got)
	}

	// Fractional percents round to the whole percent the number's own noise
	// sits inside.
	m.Cost.Subscription = &statusmodel.CostSubscription{Label: "MAX1", FiveHour: ptr(0.343), SevenDay: ptr(0.004)}
	if got := dashCost(m, plainStyles()); got != "config claude · MAX1 · 34% of 5h · 0% of 7d" {
		t.Errorf("fractional utilization rounds oddly: %q", got)
	}

	// Wallet money beside the subscription: the money first, the windows
	// after — the spend, then what the subscription it rode looks like.
	m.Cost.Lines = []statusmodel.CostLine{{Source: statusmodel.CostSourceWorkersAI,
		Metered: true, USD: ptr(0.02), Attempts: 2, Basis: "AI Gateway logs"}}
	m.Cost.Subscription = &statusmodel.CostSubscription{Label: "MAX1", FiveHour: ptr(0.34), SevenDay: ptr(0.08)}
	if got := dashCost(m, plainStyles()); got != "config claude · cost Workers AI $0.02 · MAX1 · 34% of 5h · 8% of 7d" {
		t.Errorf("the cost line with money and a subscription is %q", got)
	}

	// No lease, no segment: a GLM run never grows one.
	m.Cost.Subscription = nil
	if got := dashCost(m, plainStyles()); got != "config claude · cost Workers AI $0.02" {
		t.Errorf("a run with no lease renders %q, want money only", got)
	}
}

// TestDashboardCostLineRendersMeteredOnly (tick b13's three cases): the cost
// line shows the metered cost when there is one, the leased subscription's
// window use on a claude-sub run, and NOTHING when there is neither — no
// fabricated $0.00 for unmetered spend (hn6 rule 7), no "not metered"
// recital, and no config name floating on an otherwise empty line.
func TestDashboardCostLineRendersMeteredOnly(t *testing.T) {
	t.Parallel()
	name := "claude"
	m := dashboardFixture()
	m.RunConfig = &name
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "config claude · cost Workers AI $0.41") {
		t.Errorf("a metered cost does not read beside the config that spent it:\n%s", joined)
	}
	if strings.Contains(joined, "claude not metered") {
		t.Errorf("an unmetered river is recited on the glance line:\n%s", joined)
	}
	if strings.Contains(joined, "$0.00") {
		t.Errorf("an unmetered cost wears a fabricated $0.00:\n%s", joined)
	}

	// No lines at all: nothing renders, not even the config on its own.
	m.Cost.Lines = nil
	m.Cost.Subscription = nil
	if got := dashCost(m, plainStyles()); got != "" {
		t.Errorf("a run with no metered cost renders %q, want nothing at all (tick b13)", got)
	}

	// A metered zero is silence: the gateway measured nothing, and a $0.00
	// beside nothing else is the number the tick replaces. The measured zero
	// stays in the model's own line, with its basis.
	m.Cost.Lines = []statusmodel.CostLine{{
		Source: statusmodel.CostSourceWorkersAI, Metered: true, USD: ptr(0.0), Attempts: 4,
		Basis: "gateway usage for 4 dispatches",
	}}
	if got := dashCost(m, plainStyles()); got != "" {
		t.Errorf("a metered zero renders %q, want nothing (tick b13: a $0.00 is no cost)", got)
	}
	if m.Cost.Lines[0].USD == nil || *m.Cost.Lines[0].USD != 0.0 || !m.Cost.Lines[0].Metered {
		t.Errorf("the model's measured zero was lost: %+v", m.Cost.Lines[0])
	}
}

// TestDashboardVerdictColours: health as a verdict, coloured as the verdict
// it is (hn6 rule 3) — green healthy, amber degraded with its why, red
// stopped with its why — and what the run recovered from by itself riding in
// brackets as calm, with a measured span through the frame's own clock and a
// count as "×n" where no span was stated.
func TestDashboardVerdictColours(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		verdict statusmodel.HealthVerdict
		word    string
		code    string
	}{
		{statusmodel.HealthVerdict{State: statusmodel.VerdictHealthy}, "● healthy", "32"},
		{statusmodel.HealthVerdict{State: statusmodel.VerdictDegraded,
			Summary: "degraded: t2 nudged as stuck (5m ago)"}, "● degraded: t2 nudged as stuck (5m ago)", "33"},
		{statusmodel.HealthVerdict{State: statusmodel.VerdictStopped,
			Summary: "pid 4242 is gone without its own terminal line"}, "● stopped: pid 4242 is gone without its own terminal line", "31"},
	} {
		m := dashboardFixture()
		m.Health.Verdict = tc.verdict
		m.Health.Verdict.Recovered = nil
		coloured := strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
		if !strings.Contains(coloured, "\x1b["+tc.code+"m"+tc.word+"\x1b[0m") {
			t.Errorf("the %s verdict is not in its colour:\n%s", tc.word, coloured)
		}
		plain := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
		if !strings.Contains(plain, tc.word) {
			t.Errorf("the verdict word %q does not render with identity styles:\n%s", tc.word, plain)
		}
	}

	// The recovered list, both shapes: a count where no span was stated,
	// a span through humanDuration where one was.
	m := dashboardFixture()
	plain := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(plain, "(recovered: net ×14, sleep 41m)") {
		t.Errorf("the recoveries do not render as counts and spans:\n%s", plain)
	}
}

// TestDashboardNarrowWidths: the pane's width drops columns in the tick's
// fixed order, and no line is ever wider than the pane. At 63 the worker's
// excerpt is gone (the name may follow when the status needs the room); at
// 47 the name is gone too — and the tick's identity and its status word
// never go at any width.
func TestDashboardNarrowWidths(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	ids := []string{"060", "823", "46x", "v7z"}

	w80 := renderWatchFrame(m, plainStyles(), 80, 0, "")
	joined80 := strings.Join(w80, "\n")
	if !strings.Contains(joined80, `"ran go te`) {
		t.Errorf("an 80-column pane dropped the excerpt column entirely:\n%s", joined80)
	}
	if !strings.Contains(joined80, "port sandb") {
		t.Errorf("an 80-column pane dropped the name column:\n%s", joined80)
	}

	w63 := renderWatchFrame(m, plainStyles(), 63, 0, "")
	joined63 := strings.Join(w63, "\n")
	if strings.Contains(joined63, `"ran go te`) {
		t.Errorf("a 63-column pane still shows the workers' excerpts:\n%s", joined63)
	}
	if !strings.Contains(joined63, "testing (attempt 2, model escalated)") {
		t.Errorf("a 63-column pane lost the status word:\n%s", joined63)
	}

	w47 := renderWatchFrame(m, plainStyles(), 47, 0, "")
	joined47 := strings.Join(w47, "\n")
	if strings.Contains(joined47, "port sandb") {
		t.Errorf("a 47-column pane still shows the name column:\n%s", joined47)
	}
	if !strings.Contains(joined47, "testing (attempt 2, model escalated)") {
		t.Errorf("a 47-column pane lost the status word:\n%s", joined47)
	}

	for _, tc := range []struct {
		width int
		frame []string
	}{
		{80, w80}, {63, w63}, {47, w47},
	} {
		for i, line := range tc.frame {
			if got := ansi.StringWidth(line); got > tc.width {
				t.Errorf("line %d is %d cells wide in a %d-column pane: %q", i, got, tc.width, line)
			}
		}
		if got := dashRowIDs(tc.frame, ids); len(got) != len(ids) {
			t.Errorf("a %d-column pane lost tick rows: %v\n%s", tc.width, got, strings.Join(tc.frame, "\n"))
		}
	}
}

// TestDashboardColumnsStayAligned: the rows align on their shared columns —
// the id, the status word, the elapsed time and the excerpt all start at the
// same display column in every row — so the table reads as a table.
func TestDashboardColumnsStayAligned(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	frame := renderWatchFrame(m, plainStyles(), 0, 0, "")
	ids := []string{"060", "823", "46x", "v7z"}
	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		line := dashRowLine(frame, id)
		if line < 0 {
			t.Fatalf("tick %s lost its row:\n%s", id, strings.Join(frame, "\n"))
		}
		rows = append(rows, frame[line])
	}
	// The status word column: every row's status starts at the same column.
	first := strings.Index(rows[0], "merged")
	if first < 0 {
		t.Fatalf("the first row carries no status:\n%s", rows[0])
	}
	for _, row := range rows[1:] {
		// The status column starts where the first row's does, wherever the
		// row's own status word sits.
		if at := strings.Index(row, "testing"); at >= 0 && at != first {
			t.Errorf("the rows do not align on the status column:\n%s\n%s", rows[0], row)
		}
	}
}

// TestDashboardFitsHeight: a pane shorter than the frame keeps the headline
// and the tail, and compresses the middle in place — the phase track yields
// first (a pane crowded with content owes its rows a seat before it owes
// the map one), then the DONE rows fold into their group's header, then
// rows trim from the bottom with one "+N more" at the fold — and the whole
// frame is never taller than the pane.
func TestDashboardFitsHeight(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	full := renderWatchFrame(m, plainStyles(), 120, 0, "")

	// A pane that seats the whole level-1 frame: everything visible.
	frame := renderWatchFrame(m, plainStyles(), 120, 15, "")
	if len(frame) > 15 || !strings.Contains(strings.Join(frame, "\n"), "▲ here") {
		t.Errorf("a 15-line pane does not seat the whole frame:\n%s", strings.Join(frame, "\n"))
	}

	// A slightly shorter pane: the track yields, the rows keep their seat.
	frame = renderWatchFrame(m, plainStyles(), 120, 12, "")
	joined := strings.Join(frame, "\n")
	if len(frame) > 12 {
		t.Errorf("a 12-line pane rendered %d lines:\n%s", len(frame), joined)
	}

	for _, want := range []string{"46x", "v7z", "DONE (2)", "─ latest", "[enter] details"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a 12-line pane dropped %q:\n%s", want, joined)
		}
	}

	// Shorter still: rows trim from the bottom, one count at the fold, and
	// the questions and the last words keep the pane.
	frame = renderWatchFrame(m, plainStyles(), 120, 11, "")
	joined = strings.Join(frame, "\n")
	if len(frame) > 11 {
		t.Errorf("a 11-line pane rendered %d lines:\n%s", len(frame), joined)
	}
	if !strings.Contains(joined, "+2 more") {
		t.Errorf("the fold does not count what it dropped:\n%s", joined)
	}
	for _, want := range []string{"Needs you: nothing", "● healthy", "─ latest", "[enter] details"} {
		if !strings.Contains(joined, want) {
			t.Errorf("a 11-line pane dropped %q:\n%s", want, joined)
		}
	}

	// Unknown height (0): everything, nothing folded.
	if len(renderWatchFrame(m, plainStyles(), 120, 0, "")) != len(full) {
		t.Error("an unknown height changed the frame")
	}
}

// TestTheFrameTruncatesToThePane: no line is wider than the pane — width is
// honest even after styling, so a frame never wraps into the rows below it.
func TestTheFrameTruncatesToThePane(t *testing.T) {
	t.Parallel()
	for _, width := range []int{120, 80, 40, 20} {
		frame := renderWatchFrame(dashboardFixture(), ansiWatchStyles(), width, 0, "")
		for i, line := range frame {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("line %d is %d cells wide in a %d-column pane: %q", i, got, width, line)
			}
		}
	}
}

// TestTheFrameSaysAnUnreadableTracker: a tracker that could not be read is a
// fact the frame says, never a silence a reader could mistake for an empty
// epic — and the health verdict carries the degraded source, because a
// renderer that silently skipped it would be a renderer that looked healthy
// while guessing.
func TestTheFrameSaysAnUnreadableTracker(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Degraded = []string{"tracker"}
	m.Waves = nil
	m.Groups = nil
	m.Progress = statusmodel.Progress{}
	m.Health.Verdict = statusmodel.HealthVerdict{
		State:   statusmodel.VerdictDegraded,
		Summary: "degraded: tracker unreadable",
	}
	coloured := strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(coloured, "● degraded: tracker unreadable") {
		t.Errorf("a degraded source is not carried by the health verdict:\n%s", coloured)
	}
	plain := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(plain, "the epic's shape could not be read (the tracker did not answer)") {
		t.Errorf("an unreadable epic shape shows nothing at all:\n%s", plain)
	}
}

// TestTheFrameSeatsTheDrillInMarker: the selected tick's row renders with
// the "▸" marker — the cursor the drill-in keys move — and no other row
// does, so a row's identity never shifts when the cursor moves onto or off
// it.
func TestTheFrameSeatsTheDrillInMarker(t *testing.T) {
	t.Parallel()
	noMarker := func(frame []string) bool {
		for _, line := range frame {
			if strings.HasPrefix(line, "▸ ") {
				return false
			}
		}
		return true
	}
	frame := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "t2")
	joined := strings.Join(frame, "\n")
	if !dashRowMatches(frame[dashRowLine(frame, "t2")], "t2") || !strings.Contains(joined, "▸ t2") {
		t.Errorf("the selected tick does not carry the cursor marker:\n%s", joined)
	}
	other := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	if !noMarker(other) {
		t.Errorf("a frame with no selection still carries a cursor marker:\n%s", strings.Join(other, "\n"))
	}
	if dashRowLine(frame, "t2") != dashRowLine(other, "t2") {
		t.Errorf("the selected row moved lines:\n%s", joined)
	}
}

// TestTheFrameRendersTheModelsOwnElapsed: the header's clock is the model's
// own field, not a renderer derivation — the golden's measured span renders
// in the identity line (2h9m: the contract golden's earliest dispatch to its
// own generated_at), and a model that states no elapsed renders none: the
// renderer derives nothing, so the field cannot lie and the TUI cannot drift
// from the phone page that reads the same model (tick e6g).
func TestTheFrameRendersTheModelsOwnElapsed(t *testing.T) {
	t.Parallel()
	golden := dashboardContractGolden(t)
	if golden.Progress.RunElapsedSeconds == nil {
		t.Fatal("the dashboard golden carries no run elapsed: the fixture for the header's clock is missing")
	}
	joined := strings.Join(renderWatchFrame(golden, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "running 2h9m") {
		t.Errorf("the model's own elapsed does not render in the identity line:\n%s", joined)
	}

	golden.Progress.RunElapsedSeconds = nil
	joined = strings.Join(renderWatchFrame(golden, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "2h9m") {
		t.Errorf("a model that states no elapsed still renders one:\n%s", joined)
	}
}

// TestTheFrameNamesTheRunsState: the identity line's right side answers
// whether the run is going — "running" while it is, the run's own terminal
// word when it is not — beside the config and the host.
func TestTheFrameNamesTheRunsState(t *testing.T) {
	t.Parallel()
	m := scenarioFailed()
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "failed 1h12m") {
		t.Errorf("a failed run's header does not name the failure beside the elapsed:\n%s", joined)
	}
	m.Lifecycle.Phase = statusmodel.PhaseCancelled
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "stopped 1h12m") {
		t.Errorf("a cancelled run's header does not name the stop:\n%s", joined)
	}
}

// TestTheFrameShowsTheETAOnlyWhenMeasured: the approximate time left shows
// as "~… left" only where the model measured it — a number nobody measured
// is a number that lies.
func TestTheFrameShowsTheETAOnlyWhenMeasured(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Remaining = &statusmodel.Remaining{ApproximateSeconds: 2400, Basis: "3 open ticks × the 20m median of closed ones"}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "~40m left") {
		t.Errorf("a measured remaining time does not render as an ETA:\n%s", joined)
	}

	m.Remaining = nil
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "left") {
		t.Errorf("an unmeasured run still claims an ETA:\n%s", joined)
	}
}

// TestTheFrameMarksAbsorbedAndDuplicateRows: a tick the run absorbed by
// absorbing a finding is a marked row ("+" before its id), and a duplicate
// renders dimmed whole with the tick its work belongs to in its name — the
// facts a person reads without drilling in.
func TestTheFrameMarksAbsorbedAndDuplicateRows(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	if !strings.Contains(strings.Join(frame, "\n"), "+t1c") {
		t.Errorf("an absorbed tick's row is not marked:\n%s", strings.Join(frame, "\n"))
	}

	// A duplicate: dimmed whole, and named — the tick the work belongs to
	// is in its name, so a person scanning the rows reads the fact without
	// drilling in. The epic-state fixture's duplicate is queued, so it shows
	// on the collapsed up-next line; this one is closed, so it renders as a
	// row and carries the dim.
	m := epicStateFixture()
	m.Groups.UpNext = nil
	m.Groups.Done = append(m.Groups.Done, "dup")
	for wi := range *m.Waves {
		for ti := range (*m.Waves)[wi].Ticks {
			tick := &(*m.Waves)[wi].Ticks[ti]
			if tick.TickID == "dup" {
				tick.Status = statusmodel.WordMerged
				tick.Pipeline = donePipeline(statusmodel.PipelineImplement)
			}
		}
	}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "duplicate of at2") {
		t.Errorf("the duplicate row does not name the tick the work belongs to:\n%s", joined)
	}
	coloured := renderWatchFrame(m, ansiWatchStyles(), 0, 0, "")
	var dupLine string
	for _, line := range coloured {
		if strings.Contains(ansi.Strip(line), "duplicate of at2") && strings.HasPrefix(ansi.Strip(line), "  ") {
			dupLine = line
			break
		}
	}
	if dupLine == "" {
		t.Fatalf("the duplicate row is missing:\n%s", joined)
	}
	if !strings.HasPrefix(dupLine, "\x1b[2m") {
		t.Errorf("the duplicate row is not dimmed whole: %q", dupLine)
	}
}

// TestTheFrameUpNextIsCollapsed: the UP NEXT group is one line — the header
// with the next ticks' names seated after it, folded with an ellipsis where
// the pane ends — plus the "then:" line naming the track steps still ahead.
func TestTheFrameUpNextIsCollapsed(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(scenarioBusy(), plainStyles(), 120, 0, "")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "UP NEXT (12)") {
		t.Errorf("the UP NEXT group does not announce its count:\n%s", joined)
	}
	if !strings.Contains(joined, "s3r kanpla-etl /sync/intraday") {
		t.Errorf("the up-next line does not name its first tick:\n%s", joined)
	}
	if !strings.Contains(joined, "…") {
		t.Errorf("the up-next line does not fold what the pane does not seat:\n%s", joined)
	}
	// Unknown width: no fold, every item.
	joined = strings.Join(renderWatchFrame(scenarioBusy(), plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "a2f alerting hooks · …") {
		t.Errorf("unknown width folded a list it could show whole:\n%s", joined)
	}
	if !strings.Contains(joined, "then: Reviewing → Closing out → PR & CI → Merged") {
		t.Errorf("the up-next line does not name the steps still ahead:\n%s", joined)
	}

	// A pane that seats nothing after the header keeps the header alone.
	frame = renderWatchFrame(scenarioBusy(), plainStyles(), 30, 0, "")
	joined = strings.Join(frame, "\n")
	if !strings.Contains(joined, "UP NEXT (12)") {
		t.Errorf("a 30-column pane lost the UP NEXT header:\n%s", joined)
	}
	if strings.Contains(joined, "kanpla-etl") {
		t.Errorf("a 30-column pane still seats the up-next items:\n%s", joined)
	}
}

// TestTheFrameTailIsReadableSentences (tick 47j): the tail is the feed's
// last two READABLE events, newest first, each as the clock, the tick's own
// id (or try) and its sentence — never the raw stage and detail the [e]
// view still carries. The try each line's prefix names is the MODEL's own
// try for the event's attempt (tick s71): the whole try history the rows
// carry, not the two-line window the tail itself is.
func TestTheFrameTailIsReadableSentences(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	frame := renderWatchFrame(m, plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")

	modelTry := func(e runfeed.Event) (int, bool) {
		if e.TickID == nil || e.Attempt == nil || m.Waves == nil {
			return 0, false
		}
		for wi := range *m.Waves {
			for ti := range (*m.Waves)[wi].Ticks {
				tick := &(*m.Waves)[wi].Ticks[ti]
				if tick.TickID != *e.TickID {
					continue
				}
				for _, try := range tick.Tries {
					if try.Attempt == *e.Attempt {
						return try.Try, true
					}
				}
			}
		}
		return 0, false
	}
	seen := 0
	for _, e := range m.Recent[len(m.Recent)-2:] {
		who := "run"
		if e.TickID != nil && *e.TickID != "" {
			who = *e.TickID
			if try, ok := modelTry(e); ok {
				who = fmt.Sprintf("%s#%d", who, try)
			}
		}
		sentence, ok := feedSentenceFor(e)
		if !ok {
			t.Fatalf("the fixture's own event for %s is not one feedSentences covers — fix the fixture or the map", e.Stage)
		}
		line := fmt.Sprintf("%s  %s  %s", clockOf(e.At), who, sentence)
		if !strings.Contains(joined, line) {
			t.Errorf("the tail does not carry the readable sentence for %s:\n%s", e.Stage, joined)
		}
		if strings.Contains(joined, e.Stage+": "+e.Detail) {
			t.Errorf("the tail still carries the raw stage and detail for %s, not its sentence:\n%s", e.Stage, joined)
		}
		seen++
	}
	if seen != 2 {
		t.Errorf("the tail is not two lines: saw %d of the model's recent events\n%s", seen, joined)
	}
	if !strings.Contains(joined, "─ latest") {
		t.Errorf("the tail does not carry its section rule:\n%s", joined)
	}
	if !strings.Contains(joined, "[enter] details  [e] all events  [q] quit") {
		t.Errorf("the tail does not carry the key hints:\n%s", joined)
	}

	// An empty feed still carries the rule and the hints.
	m.Recent = nil
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "─ latest") || !strings.Contains(joined, "[enter] details") {
		t.Errorf("an empty feed lost the rule or the hints:\n%s", joined)
	}
}

// TestTheTailDropsPureMechanicsAndReachesBack (tick 47j): a pushed/
// push_queued/policy_stated/cleaned_up event is dropped from the tail
// entirely rather than shown, and the tail reaches further back into the
// feed to still show two readable lines.
func TestTheTailDropsPureMechanicsAndReachesBack(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Recent = append(m.Recent,
		runfeed.Event{SchemaVersion: 1, At: "2026-09-28T19:05:00Z", RunID: "epic-rmod", Stage: reconcile.StagePushed,
			Detail: "push 1 of this incarnation to origin landed"},
		runfeed.Event{SchemaVersion: 1, At: "2026-09-28T19:06:00Z", RunID: "epic-rmod", Stage: reconcile.StageCleanedUp,
			Detail: "attempt 2 of t2 cleaned up"},
	)
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "push 1 of this incarnation") || strings.Contains(joined, "cleaned up") {
		t.Errorf("a pure-mechanic stage reached the tail instead of being dropped:\n%s", joined)
	}
	wantFirst, ok := feedSentenceFor(m.Recent[len(m.Recent)-4]) // "dispatched" (t2 try 2)
	if !ok {
		t.Fatal("fixture's own event is missing from feedSentences")
	}
	wantSecond, ok := feedSentenceFor(m.Recent[len(m.Recent)-3]) // "closeout_held"
	if !ok {
		t.Fatal("fixture's own event is missing from feedSentences")
	}
	if !strings.Contains(joined, wantFirst) || !strings.Contains(joined, wantSecond) {
		t.Errorf("the tail did not reach back past the two mechanics for two readable lines:\n%s", joined)
	}
}

// TestTheTailCountsTheTryFromTheWholeModel: the tail's "<tick>#<n>" prefix
// names the tick's own try as the WHOLE model states it (tick s71), not as
// the window the tail itself is — a tick on its third try whose recent
// events are all its third attempt counted #1 there, disagreeing with the
// model's own row and with the [e] feed, which count the whole run.
func TestTheTailCountsTheTryFromTheWholeModel(t *testing.T) {
	t.Parallel()
	strong := "strong"
	m := statusmodel.Model{
		RunID:       "epic-rmod",
		EpicID:      "rmod",
		Host:        statusmodel.HostLocal,
		GeneratedAt: "2026-10-04T12:30:00Z",
		Liveness: statusmodel.Liveness{
			Alive: true, State: "alive", Reason: "pid 4242", Source: "run.pid",
		},
		Health: statusmodel.Health{Verdict: statusmodel.HealthVerdict{
			State: statusmodel.VerdictHealthy,
		}},
		// The recent window carries only the third attempt — five lines of
		// it, exactly what the model keeps, and none of the two tries before.
		Recent: []runfeed.Event{
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:04:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: reconcile.StageDispatched,
				Detail: "t9 dispatched again"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:05:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: reconcile.StageCollected,
				Detail: "t9 collected its attempt"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:06:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: reconcile.StagePublished,
				Detail: "t9 wrote its report"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:07:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: reconcile.StageGatePassed,
				Detail: "t9 passed the integrated gate"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:08:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: reconcile.StageClosed,
				Detail: "t9 merged"},
		},
		Waves: &[]statusmodel.Wave{{
			Wave: 1, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{{
				TickID: "t9", Title: "the retried tick", State: "dispatched",
				Status:  statusmodel.WordMerged,
				Attempt: ptr(3), Try: ptr(3), Tier: &strong,
				Tries: []statusmodel.Try{
					{Try: 1, Attempt: 1, Outcome: statusmodel.TryRejected,
						DispatchedAt: "2026-10-04T10:00:00Z", Tier: &strong,
						Reason: ptr("gofmt drifted in two files")},
					{Try: 2, Attempt: 2, Outcome: statusmodel.TryGateFailed,
						DispatchedAt: "2026-10-04T11:00:00Z", Tier: &strong},
					{Try: 3, Attempt: 3, Outcome: statusmodel.TryClosed,
						DispatchedAt: "2026-10-04T12:04:00Z", Tier: &strong},
				},
			}},
		}},
		Groups: &statusmodel.TickGroups{Done: []string{"t9"}},
	}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	// The tail's lines (the closed and gate_passed events) both name the
	// tick's THIRD try — the number the model's own row and the [e] feed say.
	if !strings.Contains(joined, "t9#3") {
		t.Errorf("the tail does not name the model's own try for its events:\n%s", joined)
	}
	if !strings.Contains(joined, "finished and merged") || !strings.Contains(joined, "tests passed") {
		t.Errorf("the tail is not the feed's own readable sentences:\n%s", joined)
	}
	if strings.Contains(joined, "t9#1") || strings.Contains(joined, "t9#2") {
		t.Errorf("the tail counted the try from the window, not the model:\n%s", joined)
	}
}

// TestHumanDurationRoundsForAPerson: the frame's timers are for glancing,
// so they read like a person reads a clock.
func TestHumanDurationRoundsForAPerson(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{45, "45s"},
		{59, "59s"},
		{60, "1m"},
		{31 * 60, "31m"},
		{3600, "1h"},
		{3 * 3600, "3h"},
		{3*3600 + 60, "3h1m"},
		{25 * 3600, "1d1h"},
	} {
		if got := humanDuration(tc.seconds); got != tc.want {
			t.Errorf("humanDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// TestTheWatchAndThePhoneSpellOneVerdictWord (hn6 h7w, A5/rule 8): the
// contract's degraded and stopped goldens are the two states a person most
// needs to read the same way on every surface, and their summaries are the
// exact shapes that used to double-render — a degraded summary the model
// builder already prefixes ("degraded: …") and a stopped run whose probe
// said nothing (empty summary). This is the Go half of the cross-renderer
// test: the phone page runs the same goldens through its own headline in
// cloudflare/test/phone-page.test.ts, and the words must agree — the
// verdict is one vocabulary, spelled once in the model, never prefixed
// twice by a renderer.
func TestTheWatchAndThePhoneSpellOneVerdictWord(t *testing.T) {
	goldens := statusModelGoldens(t)

	// Degraded: the summary already carries its prefix, so the renderer
	// spells it exactly once.
	degraded := goldens["dashboard_degraded"]
	degradedWord := dashVerdict(degraded, plainStyles())
	if degradedWord != "● degraded: the remote exhausted its retries (recovered: net ×14)" {
		t.Errorf("the degraded golden's verdict reads %q", degradedWord)
	}
	if strings.Count(degradedWord, "degraded:") != 1 {
		t.Error("the degraded verdict spells its prefix more than once")
	}

	// Stopped with nothing to say: the bare word, never a dangling colon.
	stopped := goldens["dashboard_stopped"]
	if got := dashVerdict(stopped, plainStyles()); got != "● stopped" {
		t.Errorf("the stopped golden's verdict reads %q, want the bare word (the summary is empty)", got)
	}
	if strings.HasSuffix(dashVerdict(stopped, plainStyles()), "stopped: ") {
		t.Error("the stopped verdict ends in a colon with nothing behind it")
	}

	// The sibling shapes every renderer must also agree on, so the shared
	// vocabulary is pinned whole: a degraded summary WITHOUT the builder's
	// prefix gains it, a stopped summary keeps its own words, and a stopped
	// run that recovered things still says them calmly.
	prefixed := degraded
	prefixed.Health.Verdict.Summary = "the tracker is unreadable"
	if got := dashVerdict(prefixed, plainStyles()); got != "● degraded: the tracker is unreadable (recovered: net ×14)" {
		t.Errorf("an unprefixed degraded summary reads %q, want the renderer's own prefix", got)
	}
	withSummary := stopped
	withSummary.Health.Verdict.Summary = "the orchestrator container was evicted"
	if got := dashVerdict(withSummary, plainStyles()); got != "● stopped: the orchestrator container was evicted" {
		t.Errorf("a stopped run's own reason reads %q", got)
	}
}

// TestTheWatchCostLineNamesTheRunConfig (tick tda): the run's spend is read
// beside the config that spent it, so two epics on two configs compare on
// the one line that answers what each cost. A run that selected no config
// (a repository declaring none) says nothing about one — and, since tick
// b13, a run with nothing to say about cost renders no line at all, config
// included.
func TestTheWatchCostLineNamesTheRunConfig(t *testing.T) {
	name := "claude"
	m := statusmodel.Model{RunConfig: &name, Cost: statusmodel.Cost{
		Lines: []statusmodel.CostLine{{Source: statusmodel.CostSourceWorkersAI,
			Metered: true, USD: ptr(0.41), Attempts: 4, Basis: "gateway usage for 4 dispatches"}},
	}}
	if got := dashCost(m, plainStyles()); !strings.HasPrefix(got, "config claude · cost") {
		t.Errorf("the cost line on a claude run is %q, want it to name the config first", got)
	}
	if got := dashCost(statusmodel.Model{}, plainStyles()); strings.Contains(got, "config") {
		t.Errorf("the cost line of a run with no config is %q", got)
	}
}

// TestTheFrameSeatsACensusItCannotTake: a census this machine could not take
// is said, never faked as an empty one; a census that read and found nothing
// says nothing.
func TestTheFrameSeatsACensusItCannotTake(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Workers = nil
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "workers: no census could be taken") {
		t.Errorf("a census this machine could not take is not said:\n%s", joined)
	}
	m.Workers = &[]statusmodel.Worker{}
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "no census could be taken") {
		t.Errorf("an empty census still renders the failure note:\n%s", joined)
	}
}

// TestTheFrameKeepsTheDrillInsAlive: the drill-in views the keys open still
// answer over the new dashboard — the tick view renders the selected tick's
// own story, the feed view the whole feed — because the keys' contract did
// not change with the layout.
func TestTheFrameKeepsTheDrillInsAlive(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	tick := renderTickView(m, "t2", plainStyles(), 120, 0)
	joined := strings.Join(tick, "\n")
	for _, want := range []string{"the second tick's gloss", "try 1", "try 2", "[esc] back"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the tick view does not carry %q:\n%s", want, joined)
		}
	}
	feed := renderFeedView(m.Recent, modelTries(m), &m, 0, 120, 0, plainStyles())
	if !strings.Contains(strings.Join(feed, "\n"), "gate_failed") {
		t.Errorf("the feed view does not carry the raw events:\n%s", strings.Join(feed, "\n"))
	}
}

// TestDashWrapWordsKeepsEveryWord: the wrap the box and the overview lean on
// keeps every word whole — a word that does not fit starts the next line,
// and a word wider than the line is cut where it stands, never dropped.
func TestDashWrapWordsKeepsEveryWord(t *testing.T) {
	t.Parallel()
	lines := dashWrapWords("the quick brown fox jumps over", "start: ", 12)
	joined := strings.Join(lines, "\n")
	for _, word := range []string{"quick", "brown", "jumps", "over"} {
		if !strings.Contains(joined, word) {
			t.Errorf("the wrap lost %q:\n%s", word, joined)
		}
	}
	for _, line := range lines {
		if w := ansi.StringWidth(line); w > 12 {
			t.Errorf("a wrapped line is %d cells wide in a 12-column wrap: %q", w, line)
		}
	}
	// Width 0: one line, everything.
	if lines := dashWrapWords("the quick brown fox", "start: ", 0); len(lines) != 1 {
		t.Errorf("an unknown width wrapped into %d lines: %v", len(lines), lines)
	}
}

// TestTheFrameCarriesTheContractGolden: the contract's dashboard golden —
// the model every surface renders — renders through the new layout with its
// groups, statuses and track intact.
func TestTheFrameCarriesTheContractGolden(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	frame := renderWatchFrame(m, plainStyles(), 120, 40, "")
	joined := strings.Join(frame, "\n")
	for _, want := range []string{
		"ticfac Phase 7",
		"2 of 4 done",
		"▲ here",
		"NOW",
		"DONE (2)",
		"testing (attempt 2, model escalated)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the contract golden's frame does not carry %q:\n%s", want, joined)
		}
	}
}

// TestDashFoldItemsFoldsAtTheWidth: the fold keeps as many items as the
// width seats and names the rest with one ellipsis — never dropping an item
// silently and never overflowing the width it was given.
func TestDashFoldItemsFoldsAtTheWidth(t *testing.T) {
	t.Parallel()
	items := []string{"s3r kanpla-etl /sync/intraday", "wmd paired report capture", "k4f intraday reconciliation"}
	folded := dashFoldItems(items, 40)
	if !strings.Contains(folded, "s3r") {
		t.Errorf("the fold dropped the first item: %q", folded)
	}
	if !strings.Contains(folded, "…") {
		t.Errorf("the fold does not name what it dropped: %q", folded)
	}
	if w := ansi.StringWidth(folded); w > 40 {
		t.Errorf("the folded line is %d cells wide over the 40 it was given: %q", w, folded)
	}
	if got := dashFoldItems(items, 0); got != strings.Join(items, " · ") {
		t.Errorf("an unknown width folded a list that needed no fold: %q", got)
	}
	if got := dashFoldItems(nil, 40); got != "" {
		t.Errorf("an empty list folded to %q, want silence", got)
	}
}

// TestTheFrameLandsAndFails: the two ended scenarios carry their endings in
// the header's own word — done and failed — and the failed run's refused
// tick stands in the HELD group with the run's own reason, the resume a
// person can type in the box.
func TestTheFrameLandsAndFails(t *testing.T) {
	t.Parallel()
	landed := strings.Join(renderWatchFrame(scenarioLanded(), plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(landed, "done 2h9m") {
		t.Errorf("a landed run's header does not say done beside the elapsed:\n%s", landed)
	}
	if !strings.Contains(landed, "config claude · cost Workers AI $0.41") {
		t.Errorf("a landed run's metered cost is not on its frame:\n%s", landed)
	}

	failed := strings.Join(renderWatchFrame(scenarioFailed(), plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(failed, "HELD (1)") {
		t.Errorf("the failed run's refused tick is not in a HELD group:\n%s", failed)
	}
	if !strings.Contains(failed, "failed: the integrated gate refused attempt 2") {
		t.Errorf("the refused tick's row does not carry the run's own reason:\n%s", failed)
	}
	if !strings.Contains(failed, "clear with: ticfac run-epic fld --resume") {
		t.Errorf("the failed run's box does not name the resume:\n%s", failed)
	}
	if !strings.Contains(failed, "UP NEXT (2)   f3 the waiting tick · f4 the other waiting tick") {
		t.Errorf("the failed run's untouched ticks are not named on the up-next line:\n%s", failed)
	}
}
