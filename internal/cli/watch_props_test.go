package cli

// The dashboard layout's property tests (epic hn6, wave 4 — tick u4l): the
// in-gate half of the epic's acceptance A6. Where watch_view_test.go pins the
// frame byte for byte against one golden and walks one fixture through each
// rule, this file asks the rules as PROPERTIES of the renderer and checks them
// over hundreds of generated models at every pane shape the tick names — the
// inputs a fixture writer never thinks to draw.
//
// The four properties are the ones the epic's spec names for this layout:
//
//	P1  rows never reorder: the tick ids, in the order their rows appear in
//	    frame(m), are a subsequence of plan order; and the ids the two frames
//	    m and advance(m) both show keep their order across the advance.
//	P2  needs-you is never empty while a hold exists: a hold is announced —
//	    with its clearing command wherever the pane seats the whole line, and
//	    wrapped under the announcement where it does not (tick 9um), so a
//	    shown hold is whole — every word of its command — and no frame with a
//	    hold says "needs you: nothing"; a run with no hold says exactly that
//	    (at height 0 or ≥ 8, where the header always survives the height fit).
//	P3  never $0.00 for unmetered spend: when the cost says "not metered"
//	    anywhere, no unmetered river's label wears a number in the frame
//	    (a metered line's measured number prints, zero included — tick 1tm),
//	    and the cost line reads whole wherever the pane seats it.
//	P4  the frame fits the pane: no line wider than the pane, no frame
//	    taller than it.
//
// Each property runs over 500 seeded models at widths {0, 30, 47, 63, 80, 99,
// 120, 200} and heights {0, 8, 12, 24, 60} — 0 meaning unknown on either
// axis. Non-vacuity is proven in the gate, not argued:
// TestDashboardPropertiesCatchSeededBugs runs each property against a
// deliberately broken renderer and requires the property to fail for at
// least one generated model.
//
// The "first frame within its bound" property is deliberately NOT here: it
// is a wall-clock property — it measures how soon a follow draws its first
// frame — and it stays with internal/cli/status_firstframe_test.go, with
// tick z7w's Bombadil suite targeting this same layout from outside the
// process.
//
// Everything here is headless and pure: no git, no processes, no wall clock
// (the models stamp themselves against one fixed GeneratedAt), so the run is
// deterministic, seeds are reproducible with -seed, and the whole thing is
// cheap enough for the per-tick gate.

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// propSeed adds one seed to the property run's fixed list, so a failure named
// in a report can be re-run exactly: `go test -run TestDashboardProperties
// ./internal/cli/ -seed=<n>`.
var propSeed = flag.Int64("seed", 0, "extra seed for the dashboard property models (0 = the fixed list only)")

var (
	propWidths  = []int{0, 30, 47, 63, 80, 99, 120, 200}
	propHeights = []int{0, 8, 12, 24, 60}
	// propProbeWidth is the width the seeded-bug run renders at: wide enough
	// to seat every line the properties oracle, tall enough (height 0) to
	// keep the whole frame — the one pane shape where each breaker's
	// violation is visible.
	propProbeWidth = 120
)

// propSeeds is the run's seed list: 500 fixed seeds, so the population the
// properties hold over is the same population on every run and on every host,
// plus the -seed flag's seed when one is given.
func propSeeds() []int64 {
	seeds := make([]int64, 0, 501)
	for s := int64(1); s <= 500; s++ {
		seeds = append(seeds, s)
	}
	if *propSeed != 0 {
		seeds = append(seeds, *propSeed)
	}
	return seeds
}

// ---------------------------------------------------------------------------
// The generator
// ---------------------------------------------------------------------------

// propNow is every generated model's own `now`: the stamps the generator and
// the advance write are measured back from it, so no property ever reads a
// wall clock and no age drifts between runs.
const propNow = "2026-09-28T19:20:00Z"

func propStamp(minutesAgo int64) string {
	at := time.Date(2026, 9, 28, 19, 20, 0, 0, time.UTC).Add(-time.Duration(minutesAgo) * time.Minute)
	return at.Format(time.RFC3339)
}

// genModel draws one run's model the way a mid-run epic looks: 1–6 waves of
// 1–8 ticks with random states, pipelines consistent with those states, some
// ticks children of an earlier tick, 0–3 attention entries (some needing a
// person, some with the command that clears them), 0–3 workers with activity,
// cost lines on distinct rivers mixing metered and unmetered — the
// decisions river in the shapes tick 1tm made honest, measured zero and
// no-usage records among them, and the workers-ai river with the cloud's
// unsynced record — and 0–5 recent events.
//
// The tick ids are one length ("t01"…"t48"), so a row's id cell is
// unambiguous and the row extractor below can read it back exactly; the rand
// is the caller's, so a seed names a model exactly.
func genModel(r *rand.Rand) statusmodel.Model {
	const epic = "prmod"
	tiers := []string{"strong", "frontier"}
	pickTier := func() *string { return ptr(tiers[r.IntN(len(tiers))]) }

	nw := 1 + r.IntN(6)
	frontier := 1 + r.IntN(nw)
	ids := []string{}
	total, closed, inFlight := 0, 0, 0
	waves := make([]statusmodel.Wave, 0, nw)
	type standing struct {
		id      string
		attempt int
	}
	live := []standing{}

	for w := 1; w <= nw; w++ {
		wave := statusmodel.Wave{Wave: w, State: statusmodel.WaveUpcoming}
		switch {
		case w < frontier:
			wave.State = statusmodel.WaveDone
		case w == frontier:
			wave.State = statusmodel.WaveActive
		}
		for i, nt := 0, 1+r.IntN(8); i < nt; i++ {
			total++
			id := fmt.Sprintf("t%02d", total)
			ids = append(ids, id)
			tick := statusmodel.Tick{
				TickID: id,
				Title:  "property tick " + id,
				Gloss:  "property work for " + id,
			}
			// Some ticks are children of an EARLIER tick — a row that
			// indents under a row the plan already placed.
			if len(ids) > 1 && r.IntN(4) == 0 {
				tick.ParentTickID = ptr(ids[r.IntN(len(ids)-1)])
			}
			switch role := r.IntN(10); role {
			case 0:
				tick.Role = "review"
			case 1:
				tick.Role = "closeout"
			}
			switch state := r.IntN(10); {
			case state < 4:
				tick.State = "closed"
				closed++
			case state < 7:
				tick.State = "dispatched"
				inFlight++
			default:
				tick.State = "ready"
			}
			stages := propStages(tick.Role)
			switch tick.State {
			case "closed":
				tick.Pipeline = propPipelineAll(stages, statusmodel.StageStateDone)
				n := 1 + r.IntN(3)
				for i := 0; i < n; i++ {
					try := statusmodel.Try{
						Try: i + 1, Attempt: i + 1,
						DispatchedAt: propStamp(int64((n-i)*120 + 30 + r.IntN(90))),
						Tier:         pickTier(),
					}
					if i == n-1 {
						try.Outcome = statusmodel.TryClosed
					} else if r.IntN(2) == 0 {
						try.Outcome = statusmodel.TryRejected
						try.Reason = ptr("the gate refused the go check")
					} else {
						try.Outcome = statusmodel.TryGateFailed
						try.Reason = ptr("the integrated gate refused attempt " + fmt.Sprint(i+1))
					}
					tick.Tries = append(tick.Tries, try)
				}
				tick.Try, tick.Attempt = ptr(n), ptr(n)
				tick.Tier = tick.Tries[n-1].Tier
				tick.DurationSeconds = ptr(int64(600 + r.IntN(8400)))
			case "dispatched":
				k := 1 + r.IntN(len(stages)-2) // the live stage: never claim, never the end
				states := make([]string, len(stages))
				for i := 0; i < k; i++ {
					states[i] = statusmodel.StageStateDone
				}
				failed := r.IntN(5) == 0
				if failed {
					states[k] = statusmodel.StageStateFailed
				} else {
					states[k] = statusmodel.StageStateActive
				}
				tick.Pipeline = propPipeline(stages, states...)
				n := 1 + r.IntN(2)
				for i := 0; i < n; i++ {
					try := statusmodel.Try{
						Try: i + 1, Attempt: i + 1,
						DispatchedAt: propStamp(int64((n-i)*240 + 15 + r.IntN(60))),
						Tier:         pickTier(),
					}
					if i == n-1 {
						if failed {
							try.Outcome = statusmodel.TryGateFailed
							try.Reason = ptr("the integrated gate refused attempt " + fmt.Sprint(n))
						} else {
							try.Outcome = statusmodel.TryInFlight
						}
					} else {
						try.Outcome = statusmodel.TryRejected
						try.Reason = ptr("the gate refused the go check")
					}
					tick.Tries = append(tick.Tries, try)
				}
				tick.Try, tick.Attempt = ptr(n), ptr(n)
				tick.Tier = tick.Tries[n-1].Tier
				tick.DurationSeconds = ptr(int64(15+r.IntN(590)) * 60)
				tick.Model = ptr("cloudflare-workers-ai/@cf/zai-org/glm-5.3")
				tick.Executor = ptr("herdr")
				live = append(live, standing{id: id, attempt: n})
			default:
				tick.Pipeline = propPipelineAll(stages, statusmodel.StageStatePending)
			}
			wave.Ticks = append(wave.Ticks, tick)
		}
		waves = append(waves, wave)
	}

	// Workers: 0–3 live workers, each standing on a dispatched tick and
	// carrying a measured activity window. A census this machine cannot take
	// is one of the shapes too.
	var workers *[]statusmodel.Worker
	switch roll := r.IntN(10); {
	case roll == 0:
		workers = nil
	case roll == 1:
		workers = ptr([]statusmodel.Worker{})
	default:
		drawn := r.IntN(4)
		rows := make([]statusmodel.Worker, 0, drawn)
		lastActions := []string{
			"ran go test ./internal/reconcile",
			"edited internal/cli/watch_view.go",
			"ran make gate",
			"read .tick/learnings.md",
			"committed the fix",
			"pushed the branch",
		}
		for i := 0; i < drawn && len(live) > 0; i++ {
			at := live[i%len(live)]
			rows = append(rows, statusmodel.Worker{
				TickID:         at.id,
				Attempt:        at.attempt,
				Branch:         fmt.Sprintf("refs/heads/ticfac/run-epic-%s/tick-%s/attempt-%d", epic, at.id, at.attempt),
				Handle:         ptr(fmt.Sprintf("herdr pane tick-%s-a%d", at.id, at.attempt)),
				ElapsedSeconds: ptr(int64(600 + r.IntN(3600))),
				Activity: &statusmodel.WorkerActivity{
					WindowSeconds: 600,
					Buckets:       []int{r.IntN(9), r.IntN(9), r.IntN(9), r.IntN(9), r.IntN(9), r.IntN(9), r.IntN(9), r.IntN(9), r.IntN(9), r.IntN(9)},
					LastAction:    ptr(lastActions[r.IntN(len(lastActions))]),
					LastActionAt:  ptr(propStamp(int64(1 + r.IntN(10)))),
					Nudges:        r.IntN(3),
				},
			})
		}
		workers = &rows
	}

	// Attention: 0–3 entries, some needing a person — with, when they do and
	// the draw says so, the one command that clears them.
	whats := []func(string) string{
		func(id string) string { return fmt.Sprintf("attempt %s struck out: the gate refused it", id) },
		func(id string) string { return fmt.Sprintf("a findings draft on %s waits for triage", id) },
		func(id string) string { return "the close-out holds for CI green on the PR" },
		func(id string) string { return "the run holds for a released attempt" },
		func(id string) string { return fmt.Sprintf("%s waits on a person's review", id) },
	}
	var attention []statusmodel.Attention
	var n int
	switch roll := r.IntN(10); {
	case roll < 4:
		n = 0
	case roll < 7:
		n = 1
	case roll < 9:
		n = 2
	default:
		n = 3
	}
	for j := 0; j < n; j++ {
		a := statusmodel.Attention{
			Kind: statusmodel.WaitWorkers,
			What: whats[r.IntN(len(whats))](ids[r.IntN(len(ids))]),
		}
		if r.IntN(10) < 7 {
			a.NeedsPerson = true
			a.Kind = statusmodel.WaitHeldForPerson
			if r.IntN(10) < 8 {
				switch c := r.IntN(3); c {
				case 0:
					a.UnblockCommand = ptr(statusmodel.ResumeCommand(statusmodel.HostLocal, epic))
				case 1:
					a.UnblockCommand = ptr(statusmodel.TriageCommand(epic))
				default:
					a.UnblockCommand = ptr(fmt.Sprintf("ticfac settle %s %s %d --release \"<who>\"",
						epic, ids[r.IntN(len(ids))], 1+r.IntN(3)))
				}
			}
		} else if r.IntN(2) == 0 {
			a.Kind = statusmodel.WaitCI
		}
		attention = append(attention, a)
	}

	// Cost: one to three lines on DISTINCT rivers — the split never names
	// one river twice — mixing metered and unmetered. The decisions river
	// draws the shapes tick 1tm made honest: a metered line whose records
	// stated a measured price of zero — a measured zero prints, beside
	// unmetered lines — a metered line with a stated price, and the
	// unmetered line of decision records that carried no usage block or no
	// price. The workers-ai river draws the cloud's UNSYNCED record too — a
	// cloud run before its first cost sync, or one whose telemetry reads
	// "unavailable: …" — beside the gateway's measured number and the local
	// unjoined line; the unsynced shape makes its model a cloud one, the
	// host that record belongs on. The decisions river's measured zero is
	// the shape a decision whose usage states a price of zero asks the
	// RENDERER to print — pinned by TestDashboardNeverPrintsZeroForUnmetered
	// — and P3 must tolerate it beside unmetered lines while refusing a
	// number on any unmetered label: the old whole-frame "$0.00" scan
	// could not tell the two apart (tick 1tm).
	rivers := []string{
		statusmodel.CostSourceDecisions, statusmodel.CostSourceWorkersAI,
		statusmodel.CostSourceClaude, statusmodel.CostSourcePiLocal,
	}
	r.Shuffle(len(rivers), func(i, j int) { rivers[i], rivers[j] = rivers[j], rivers[i] })
	amounts := []float64{0.08, 0.37, 0.41, 0.99, 1.24, 3.02, 12.5}
	lines := make([]statusmodel.CostLine, 0, 3)
	recorded, anyMetered := 0.0, false
	host := statusmodel.HostLocal
	for i, n := 0, 1+r.IntN(3); i < n; i++ {
		line := statusmodel.CostLine{Source: rivers[i], Attempts: 1 + r.IntN(6)}
		switch line.Source {
		case statusmodel.CostSourceDecisions:
			switch roll := r.IntN(3); {
			case roll == 0: // a measured zero: stated by the records, so it prints
				line.Metered, line.USD = true, ptr(0.0)
				recorded, anyMetered = recorded+0.0, true
				line.Basis = "usage recorded on decision records"
			case roll == 1: // a stated price
				usd := amounts[r.IntN(len(amounts))]
				line.Metered, line.USD, recorded = true, ptr(usd), recorded+usd
				anyMetered = true
				line.Basis = "usage recorded on decision records"
			default: // decision records with no usage block, or no price on it
				line.Basis = "not metered: the decision records carry no measured cost"
			}
		case statusmodel.CostSourceWorkersAI:
			switch roll := r.IntN(3); {
			case roll == 0: // the gateway's measured number
				usd := amounts[r.IntN(len(amounts))]
				line.Metered, line.USD, recorded = true, ptr(usd), recorded+usd
				anyMetered = true
				line.Basis = "AI Gateway logs"
			case roll == 1: // the unsynced cloud record: telemetry not answered
				line.Basis = "not metered: the gateway's cost telemetry has not answered for this run"
				host = statusmodel.HostCloud
			default: // locally run: the calls are not joined to any gateway log
				line.Basis = "not metered: this run's Workers AI calls are not joined to gateway logs"
			}
		case statusmodel.CostSourceClaude:
			line.Basis = "not metered (subscription)"
		default:
			line.Basis = "not metered"
		}
		lines = append(lines, line)
	}

	// Recent: the feed's last words, 0–5 of them, stamps inside the run so
	// far, details that never borrow the words the properties look for.
	stages := []string{"dispatched", "gate_failed", "collected", "report_ready", "closeout_held"}
	details := []string{
		"%s try %d dispatched",
		"the integrated gate refused attempt %d of %s (go)",
		"the close-out waits for CI green on the PR",
		"%s collected: STATUS: DONE",
	}
	recent := make([]runfeed.Event, 0, 5)
	for i, n := 0, r.IntN(6); i < n; i++ {
		id := ids[r.IntN(len(ids))]
		try := 1 + r.IntN(3)
		var tickID *string
		var attempt *int
		if r.IntN(4) != 0 {
			tickID, attempt = ptr(id), ptr(try)
		}
		var detail string
		switch d := r.IntN(4); d {
		case 0:
			detail = fmt.Sprintf(details[0], id, try)
		case 1:
			detail = fmt.Sprintf(details[1], try, id)
		case 2:
			detail = details[2]
		default:
			detail = fmt.Sprintf(details[3], id)
		}
		recent = append(recent, runfeed.Event{
			SchemaVersion: runfeed.SchemaVersion,
			At:            propStamp(int64(1 + r.IntN(90))),
			RunID:         "run_props",
			TickID:        tickID,
			Attempt:       attempt,
			Stage:         stages[r.IntN(len(stages))],
			Detail:        detail,
		})
	}

	// Health: mostly healthy with a recovery or two, sometimes degraded or
	// stopped, with the run's own word for why.
	verdict := statusmodel.HealthVerdict{State: statusmodel.VerdictHealthy}
	switch roll := r.IntN(10); {
	case roll < 7:
		if r.IntN(2) == 0 {
			verdict.Recovered = append(verdict.Recovered, statusmodel.Recovery{What: "net", Count: 1 + r.IntN(20)})
		}
		if r.IntN(2) == 0 {
			verdict.Recovered = append(verdict.Recovered, statusmodel.Recovery{
				What: "sleep", Count: 1 + r.IntN(5), Seconds: ptr(int64(60 * (1 + r.IntN(60)))),
			})
		}
	case roll < 9:
		verdict.State = statusmodel.VerdictDegraded
		verdict.Summary = fmt.Sprintf("%s nudged as stuck (%dm ago)", ids[r.IntN(len(ids))], 1+r.IntN(30))
	default:
		verdict.State = statusmodel.VerdictStopped
		verdict.Summary = "pid 4242 is gone without its own terminal line"
	}

	m := statusmodel.Model{
		SchemaVersion: statusmodel.SchemaVersion,
		RunID:         "run_props",
		EpicID:        epic,
		Host:          host,
		GeneratedAt:   propNow,
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
			Wave: &statusmodel.WaveRef{Active: frontier, Total: nw},
		},
		Progress: statusmodel.Progress{
			Ticks: &statusmodel.TickProgress{Total: total, Closed: closed, Open: total - closed},
			Waves: &statusmodel.WaveProgress{Total: nw, Done: frontier - 1, Active: frontier},
		},
		EpicTitle: ptr("the dashboard property epic"),
		Recent:    recent,
		Waves:     &waves,
		Workers:   workers,
		Attention: attention,
		Health:    statusmodel.Health{Verdict: verdict, RemoteRetries: r.IntN(5)},
		Cost: statusmodel.Cost{
			// The roll-up mirrors the builder (tick dm2): the sum of the
			// metered lines, null when none is metered.
			RecordedUSD: func() *float64 {
				if !anyMetered {
					return nil
				}
				return ptr(recorded)
			}(),
			Attempts: total,
			Basis:    "usage recorded on decision records",
			Lines:    lines,
		},
	}
	if r.IntN(2) == 0 {
		m.Remaining = &statusmodel.Remaining{
			ApproximateSeconds: int64(600 + r.IntN(7200)),
			Basis:              "open ticks × the median of closed ones",
		}
	}
	if r.IntN(10) < 7 {
		names := []string{"go", "ts", "lint"}
		checks := make([]statusmodel.CheckState, 0, 3)
		for _, name := range names[:1+r.IntN(3)] {
			check := statusmodel.CheckState{Name: name}
			if r.IntN(2) == 0 {
				check.Status = "in_progress"
				check.StartedAt = propStamp(int64(1 + r.IntN(10)))
			} else {
				check.Status = "completed"
				if r.IntN(4) == 0 {
					check.Conclusion = "failure"
				} else {
					check.Conclusion = "success"
				}
			}
			checks = append(checks, check)
		}
		number := 1 + r.IntN(300)
		m.CI = &statusmodel.CI{
			State: "pending",
			PR: &statusmodel.PR{
				Number:  number,
				URL:     fmt.Sprintf("https://github.com/example/ticfac/pull/%d", number),
				HeadRef: "epic/" + epic,
				HeadSHA: "9f2ab6e0e8f96fc3fdc87c2f681519bb0d191a7",
				BaseRef: "main",
			},
			Checks: checks,
		}
	}
	if len(attention) > 0 {
		first := attention[0]
		if first.NeedsPerson {
			m.WaitsOn = &statusmodel.Wait{
				Kind: first.Kind, What: first.What, NeedsPerson: true, UnblockCommand: first.UnblockCommand,
			}
		} else if inFlight > 0 {
			m.WaitsOn = &statusmodel.Wait{Kind: statusmodel.WaitWorkers, What: "1 in-flight attempt(s)"}
		}
	} else if inFlight > 0 {
		m.WaitsOn = &statusmodel.Wait{Kind: statusmodel.WaitWorkers, What: "1 in-flight attempt(s)"}
	}
	return m
}

// propStages is a tick's pipeline stage list: its role's own.
func propStages(role string) []string {
	switch role {
	case "review":
		return statusmodel.PipelineReview
	case "closeout":
		return statusmodel.PipelineCloseout
	}
	return statusmodel.PipelineImplement
}

// propPipeline zips the role's stage list with per-stage states, defaulting
// the rest to pending.
func propPipeline(stages []string, states ...string) []statusmodel.PipelineStage {
	out := make([]statusmodel.PipelineStage, len(stages))
	for i, stage := range stages {
		state := statusmodel.StageStatePending
		if i < len(states) {
			state = states[i]
		}
		out[i] = statusmodel.PipelineStage{Stage: stage, State: state}
	}
	return out
}

func propPipelineAll(stages []string, state string) []statusmodel.PipelineStage {
	states := make([]string, len(stages))
	for i := range states {
		states[i] = state
	}
	return propPipeline(stages, states...)
}

// advance moves some ticks one step forward along ready → dispatched →
// closed — the run's own next breath — WITHOUT changing the graph's order:
// the waves, the plan order and the parent links are exactly as they were, so
// a tick moves by its cells changing, never by its row moving. It is the
// second frame of P1.
func advance(m statusmodel.Model, r *rand.Rand) statusmodel.Model {
	next := propCopyModel(m)
	if next.Waves == nil {
		return next
	}
	closed := 0
	for wi := range *next.Waves {
		for ti := range (*next.Waves)[wi].Ticks {
			t := &(*next.Waves)[wi].Ticks[ti]
			switch t.State {
			case "ready":
				if r.IntN(2) == 0 {
					continue
				}
				t.State = "dispatched"
				stages := propStages(t.Role)
				states := make([]string, len(stages))
				states[0] = statusmodel.StageStateDone
				states[1] = statusmodel.StageStateActive
				t.Pipeline = propPipeline(stages, states...)
				t.Tries = append(t.Tries, statusmodel.Try{
					Try: 1, Attempt: 1, Outcome: statusmodel.TryInFlight,
					DispatchedAt: propStamp(15), Tier: ptr("frontier"),
				})
				t.Try, t.Attempt = ptr(1), ptr(1)
				t.Tier = ptr("frontier")
				t.Model = ptr("cloudflare-workers-ai/@cf/zai-org/glm-5.3")
				t.Executor = ptr("herdr")
				t.DurationSeconds = ptr(int64(15 * 60))
			case "dispatched":
				if r.IntN(2) == 0 {
					continue
				}
				t.State = "closed"
				stages := propStages(t.Role)
				t.Pipeline = propPipelineAll(stages, statusmodel.StageStateDone)
				tier := "frontier"
				if t.Tier != nil {
					tier = *t.Tier
				}
				t.Tries = append(t.Tries, statusmodel.Try{
					Try: len(t.Tries) + 1, Attempt: len(t.Tries) + 1, Outcome: statusmodel.TryClosed,
					DispatchedAt: propStamp(1), Tier: ptr(tier),
				})
				t.Try, t.Attempt = ptr(len(t.Tries)), ptr(len(t.Tries))
				base := int64(600)
				if t.DurationSeconds != nil {
					base = *t.DurationSeconds
				}
				t.DurationSeconds = ptr(base + 600)
			}
			if t.State == "closed" {
				closed++
			}
		}
	}
	if next.Progress.Ticks != nil {
		total := 0
		for _, wave := range *next.Waves {
			total += len(wave.Ticks)
		}
		next.Progress.Ticks = &statusmodel.TickProgress{Total: total, Closed: closed, Open: total - closed}
	}
	return next
}

// propCopyModel deep-copies a model the way dashboardSuccessor does — a JSON
// round trip, so the shrink and the advance never mutate the model a
// neighbouring frame is rendered from.
func propCopyModel(m statusmodel.Model) statusmodel.Model {
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	var out statusmodel.Model
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Reading a frame's rows back
// ---------------------------------------------------------------------------

// propPlan is the model's plan order: every tick id, in wave order and in the
// wave's own tick order — the order dashboardTable walks.
func propPlan(m statusmodel.Model) []string {
	plan := []string{}
	if m.Waves == nil {
		return plan
	}
	for _, wave := range *m.Waves {
		for _, tick := range wave.Ticks {
			plan = append(plan, tick.TickID)
		}
	}
	return plan
}

// propRowID is the tick id one frame line's row carries, "" when the line is
// not a tick row. It reads the head dashTickRow writes: the optional child
// indent, the mark (" " or the selected "▸"), the optional absorbed "+", then
// the id ending its padded cell. The generator's ids are one length, so the
// cell's end is the id's end.
func propRowID(line string, ids map[string]bool, idLen int) string {
	rest, ok := strings.CutPrefix(line, "  └")
	if !ok {
		rest = line
	}
	mark, size := utf8.DecodeRuneInString(rest)
	if mark != ' ' && mark != '▸' {
		return ""
	}
	body := rest[size:]
	if strings.HasPrefix(body, "+") {
		body = body[1:]
	}
	if len(body) < idLen+1 || !ids[body[:idLen]] || body[idLen] != ' ' {
		return ""
	}
	return body[:idLen]
}

// propRowIDs is the sequence in which the frame's rows carry the plan's ticks.
func propRowIDs(frame []string, ids map[string]bool, idLen int) []string {
	out := []string{}
	for _, line := range frame {
		if id := propRowID(line, ids, idLen); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// propSubsequence says whether sub appears in super in order.
func propSubsequence(super, sub []string) bool {
	i := 0
	for _, s := range super {
		if i < len(sub) && sub[i] == s {
			i++
		}
	}
	return i == len(sub)
}

// ---------------------------------------------------------------------------
// The properties
// ---------------------------------------------------------------------------

// dashProperty is one layout property, with the renderer that deliberately
// breaks it — the non-vacuity half of the tick: a property that cannot fail
// proves nothing, so each one is paired with the wrapper that violates it and
// the seeded-bug test requires the failure.
//
// The check's shape is wider than "func(frame, model)" by the facts the
// harness holds anyway: the pane's width and height (P2's height gate and all
// of P4 need them), `unbounded` — the frame's uncompressed length at this
// width, the length the renderer gives at height 0, which is what tells a
// property whether the pane seats a middle line at all —, the advanced frame
// (P1's second half) and the advanced model. Properties that need none of
// that ignore them.
type dashProperty struct {
	name       string
	check      func(frame []string, advanceFrame func() []string, m, am statusmodel.Model, width, height, unbounded int) error
	breakFrame func(frame []string, m statusmodel.Model) []string
}

// propRowsNeverReorder is P1: the frame's rows are a subsequence of plan
// order — never another order, however the states, the widths and the height
// fit shuffle the cells — and the rows both frames show keep their order
// across the advance.
func propRowsNeverReorder(frame []string, advanceFrame func() []string, m, _ statusmodel.Model, _, _, _ int) error {
	plan := propPlan(m)
	ids := make(map[string]bool, len(plan))
	for _, id := range plan {
		ids[id] = true
	}
	idLen := 0
	if len(plan) > 0 {
		idLen = len(plan[0])
	}
	got := propRowIDs(frame, ids, idLen)
	if !propSubsequence(plan, got) {
		return fmt.Errorf("the rows are not in plan order: plan %v, frame %v", plan, got)
	}
	agot := propRowIDs(advanceFrame(), ids, idLen)
	inFrame, inAdvance := make(map[string]bool, len(got)), make(map[string]bool, len(agot))
	for _, id := range got {
		inFrame[id] = true
	}
	for _, id := range agot {
		inAdvance[id] = true
	}
	before := []string{}
	for _, id := range got {
		if inAdvance[id] {
			before = append(before, id)
		}
	}
	after := []string{}
	for _, id := range agot {
		if inFrame[id] {
			after = append(after, id)
		}
	}
	if len(before) != len(after) {
		return fmt.Errorf("the frames disagree on which rows they share: %v vs %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			return fmt.Errorf("the advanced frame reordered the rows both frames show: %v became %v", before, after)
		}
	}
	return nil
}

// propNeedsYou is P2, the first question. A run with no hold is quiet:
// "needs you: nothing". A run with a hold never says that, announces the
// hold, and shows the whole hold line — what and clearing command — wherever
// the pane seats it (width 0, the unknown width, draws everything, so the
// whole line is pinned there always). Where the pane does not seat the line,
// the renderer wraps it under the announcement instead of truncating it
// (tick 9um), and a hold is then shown whole or not at all: whenever its
// announcement line is in the frame, every line of its block is, and every
// word of the clearing command with them — a hold whose command fell off
// the pane's edge is the defect the wrap exists to prevent.
func propNeedsYou(frame []string, _ func() []string, m, _ statusmodel.Model, width, height, unbounded int) error {
	joined := strings.Join(frame, "\n")
	var holds []statusmodel.Attention
	for _, a := range m.Attention {
		if a.NeedsPerson {
			holds = append(holds, a)
		}
	}
	if len(holds) == 0 {
		if height == 0 || height >= 8 {
			if !strings.Contains(joined, "needs you: nothing") {
				return fmt.Errorf("nothing needs a person and the frame does not say so")
			}
		}
		return nil
	}
	if strings.Contains(joined, "needs you: nothing") {
		return fmt.Errorf("a held run still says nothing needs a person")
	}
	if !strings.Contains(joined, "needs you:") {
		return fmt.Errorf("a hold exists and no line announces it")
	}
	if !strings.Contains(joined, "needs you:") {
		return fmt.Errorf("a hold exists and no line announces it")
	}
	if height <= 0 || height >= unbounded {
		// The pane seats the whole frame — nothing is folded away — so every
		// hold whose line the width seats pins that line exactly.
		for _, a := range holds {
			want := "needs you: " + a.What
			if a.UnblockCommand != nil && *a.UnblockCommand != "" {
				want += " — " + *a.UnblockCommand
			}
			if width <= 0 || width >= ansi.StringWidth(want) {
				if !strings.Contains(joined, want) {
					return fmt.Errorf("the hold line %q is missing where the pane seats it", want)
				}
			}
		}
	}
	// Where the pane does not seat a line, the renderer wraps it under the
	// announcement (tick 9um), and a hold is then shown whole or not at all:
	// whenever its announcement line is in the frame, every line of its
	// block is, and every word of the clearing command with them — a hold
	// whose command fell off the pane's edge is the defect the wrap exists
	// to prevent. The blocks sit in the frame in the model's order; walking
	// them in order keeps a hold whose twin shares its announcement from
	// borrowing the twin's lines, and lets the height fold's whole-block
	// drops pass: a hold the fold dropped has no announcement in the frame
	// and asks nothing.
	pos := 0
	for _, a := range holds {
		block := dashboardHoldLines(a, plainStyles(), width)
		at := propLineFrom(frame, block[0], pos)
		if at < 0 {
			continue // the height fold dropped the block whole
		}
		pos = at + 1
		for _, line := range block[1:] {
			if pos >= len(frame) || frame[pos] != line {
				return fmt.Errorf("the hold's announcement shows but its wrapped line %q is missing", line)
			}
			pos++
		}
		if a.UnblockCommand == nil || *a.UnblockCommand == "" {
			continue
		}
		if width <= 0 || len(block) == 1 {
			// The pane seats the whole line: it was the one line it always
			// was, block[0] among them.
			continue
		}
		for _, word := range strings.Fields(*a.UnblockCommand) {
			if !strings.Contains(joined, word) {
				return fmt.Errorf("the clearing command's word %q fell out of the wrapped hold", word)
			}
		}
	}
	return nil
}

// propLineFrom returns the index of the first line at or after start that
// equals want, -1 when none does.
func propLineFrom(frame []string, want string, start int) int {
	for i := start; i < len(frame); i++ {
		if frame[i] == want {
			return i
		}
	}
	return -1
}

// propCostHonest is P3: no unmetered river wears a number — when the cost
// says "not metered" anywhere, every UNMETERED line's label in the frame
// is bare of a price, while a METERED line prints its measured number,
// zero included (tick 1tm: a decision whose records stated a measured zero
// is honest beside unmetered lines, and the old whole-frame "$0.00" scan
// could not tell the two apart) — and the cost line reads whole wherever
// the pane seats it, on both axes: width that fits the line, and a height
// that keeps the frame whole (`unbounded` is the frame's length at height
// 0; the height fold compresses the middle, and the cost line stands in
// it, so a pane that drops the line cannot be asked to quote it). The
// generator draws DISTINCT rivers, so one label names one line and the
// per-label scan is unambiguous. The oracle is the renderer's own cost
// line for the model, so the property pins the line the model asks for,
// not a paraphrase of it.
func propCostHonest(frame []string, _ func() []string, m, _ statusmodel.Model, width, height, unbounded int) error {
	oracle := dashCost(m, plainStyles())
	if !strings.Contains(oracle, "not metered") {
		return nil
	}
	joined := strings.Join(frame, "\n")
	for _, line := range m.Cost.Lines {
		if line.Metered {
			continue // a measured number prints — zero included
		}
		if strings.Contains(joined, dashCostLabel(line.Source)+" $") {
			return fmt.Errorf("the unmetered %s line wears a fabricated number:\n%s", line.Source, joined)
		}
	}
	if width <= 0 || width >= ansi.StringWidth(oracle) {
		if height > 0 && height < unbounded {
			return nil // the fold compresses the middle; the line is not shown
		}
		for _, line := range frame {
			if strings.Contains(line, oracle) {
				return nil
			}
		}
		return fmt.Errorf("the cost line %q is missing where the pane seats it", oracle)
	}
	return nil
}

// propFitsPane is P4: the frame fits the pane it was drawn for — no line
// wider than it, no frame taller than it. The unknown axis (0) is unbounded
// by definition: the caller that knows nothing draws everything.
func propFitsPane(frame []string, _ func() []string, _, _ statusmodel.Model, width, height, _ int) error {
	if width > 0 {
		for i, line := range frame {
			if got := ansi.StringWidth(line); got > width {
				return fmt.Errorf("line %d is %d cells wide in a %d-column pane: %q", i, got, width, line)
			}
		}
	}
	if height > 0 && len(frame) > height {
		return fmt.Errorf("a %d-line pane rendered %d lines", height, len(frame))
	}
	return nil
}

// dashProperties is the tick's four properties, each with its breaker.
var dashProperties = []dashProperty{
	{
		name:       "rows never reorder",
		check:      propRowsNeverReorder,
		breakFrame: breakReversedRows,
	},
	{
		name:       "needs-you never empty while a hold exists",
		check:      propNeedsYou,
		breakFrame: breakDroppedAttention,
	},
	{
		name:       "never $0.00 for unmetered spend",
		check:      propCostHonest,
		breakFrame: breakUnmeteredAsZero,
	},
	{
		name:       "the frame fits the pane",
		check:      propFitsPane,
		breakFrame: breakPaddedFirstLine,
	},
}

// ---------------------------------------------------------------------------
// The deliberately broken renderers
// ---------------------------------------------------------------------------

// breakReversedRows reverses the table's rows in place — every row keeps a
// row-shaped line, so the frame still looks like a table, and the rows are in
// the wrong order. P1's breaker.
func breakReversedRows(frame []string, m statusmodel.Model) []string {
	plan := propPlan(m)
	ids := make(map[string]bool, len(plan))
	for _, id := range plan {
		ids[id] = true
	}
	idLen := 0
	if len(plan) > 0 {
		idLen = len(plan[0])
	}
	idx := []int{}
	for i, line := range frame {
		if propRowID(line, ids, idLen) != "" {
			idx = append(idx, i)
		}
	}
	if len(idx) < 2 {
		return frame
	}
	out := append([]string{}, frame...)
	for i, j := 0, len(idx)-1; i < j; i, j = i+1, j-1 {
		out[idx[i]], out[idx[j]] = out[idx[j]], out[idx[i]]
	}
	return out
}

// breakDroppedAttention drops every hold's line — the frame answers the first
// question with silence. P2's breaker.
func breakDroppedAttention(frame []string, m statusmodel.Model) []string {
	out := make([]string, 0, len(frame))
	for _, line := range frame {
		drop := false
		for _, a := range m.Attention {
			if a.NeedsPerson && strings.Contains(line, "needs you: "+a.What) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, line)
		}
	}
	return out
}

// breakUnmeteredAsZero replaces "not metered" with "$0.00" — an unmetered
// line wearing a fabricated number. P3's breaker.
func breakUnmeteredAsZero(frame []string, _ statusmodel.Model) []string {
	out := make([]string, len(frame))
	for i, line := range frame {
		out[i] = strings.ReplaceAll(line, "not metered", "$0.00")
	}
	return out
}

// breakPaddedFirstLine pads the frame's first line by 5 columns — a line that
// no longer fits the pane it was drawn for. P4's breaker.
func breakPaddedFirstLine(frame []string, _ statusmodel.Model) []string {
	out := append([]string{}, frame...)
	if len(out) > 0 {
		out[0] = out[0] + "     "
	}
	return out
}

// ---------------------------------------------------------------------------
// The tests
// ---------------------------------------------------------------------------

// TestDashboardProperties: the four layout properties hold over 500 seeded
// models at every pane shape the tick names, for the model and for its
// advance. A failure prints the seed, the pane shape and the minimal failing
// model as JSON, and the seed re-runs the whole population plus exactly that
// model with -seed.
func TestDashboardProperties(t *testing.T) {
	t.Parallel()
	for _, seed := range propSeeds() {
		m := genModel(rand.New(rand.NewPCG(uint64(seed), uint64(seed))))
		am := advance(m, rand.New(rand.NewPCG(uint64(seed), uint64(seed)+1)))
		for _, width := range propWidths {
			unbounded := len(renderWatchFrame(m, plainStyles(), width, 0, ""))
			for _, height := range propHeights {
				frame := renderWatchFrame(m, plainStyles(), width, height, "")
				advanceFrame := func() []string {
					return renderWatchFrame(am, plainStyles(), width, height, "")
				}
				for _, p := range dashProperties {
					if err := p.check(frame, advanceFrame, m, am, width, height, unbounded); err != nil {
						shrunk, which := shrinkPropFailure(p, m, am, width, height)
						t.Fatalf("seed %d: property %q failed at width %d height %d: %v\n"+
							"reproduce with: go test -short -timeout 20m -run TestDashboardProperties ./internal/cli/ -seed=%d\n"+
							"minimal failing model (%s):\n%s",
							seed, p.name, width, height, err, seed, which, propModelJSON(shrunk))
					}
				}
			}
		}
	}
}

// TestDashboardPropertiesCatchSeededBugs: non-vacuity, proven in the gate.
// Each property runs against its deliberately broken renderer and must fail
// for at least one generated model — a property that cannot fail pins
// nothing, so the test refuses one.
func TestDashboardPropertiesCatchSeededBugs(t *testing.T) {
	t.Parallel()
	broken := make(map[string]bool, len(dashProperties))
	for _, seed := range propSeeds() {
		if len(broken) == len(dashProperties) {
			break
		}
		m := genModel(rand.New(rand.NewPCG(uint64(seed), uint64(seed))))
		am := advance(m, rand.New(rand.NewPCG(uint64(seed), uint64(seed)+1)))
		unbounded := len(renderWatchFrame(m, plainStyles(), propProbeWidth, 0, ""))
		for _, p := range dashProperties {
			if broken[p.name] {
				continue
			}
			render := func(mm statusmodel.Model) []string {
				return p.breakFrame(renderWatchFrame(mm, plainStyles(), propProbeWidth, 0, ""), mm)
			}
			if err := p.check(render(m), func() []string { return render(am) }, m, am, propProbeWidth, 0, unbounded); err != nil {
				broken[p.name] = true
			}
		}
	}
	for _, p := range dashProperties {
		if !broken[p.name] {
			t.Errorf("property %q never failed against its seeded broken renderer — the check is vacuous", p.name)
		}
	}
}

// ---------------------------------------------------------------------------
// The failure report
// ---------------------------------------------------------------------------

// shrinkPropFailure reduces a failing model to a minimal one that still fails
// the property — greedy: components first (the tail, the workers, the forge,
// the ETA, the recoveries), then attention entries and cost lines one at a
// time, then whole trailing waves and trailing ticks. It returns the model to
// print and which one it is; when the failure does not reproduce from one
// model alone (P1's second half lives in a pair of frames), the original
// comes back unshrunk and says so.
func shrinkPropFailure(p dashProperty, m, am statusmodel.Model, width, height int) (statusmodel.Model, string) {
	fails := func(cand statusmodel.Model) bool {
		cam := advance(cand, rand.New(rand.NewPCG(1, 2)))
		frame := renderWatchFrame(cand, plainStyles(), width, height, "")
		unbounded := len(renderWatchFrame(cand, plainStyles(), width, 0, ""))
		return p.check(frame, func() []string { return renderWatchFrame(cam, plainStyles(), width, height, "") }, cand, cam, width, height, unbounded) != nil
	}
	cand, which := propCopyModel(m), "m"
	if !fails(cand) {
		cand, which = propCopyModel(am), "advance(m)"
	}
	if !fails(cand) {
		return m, "m and advance(m) — the failure does not reproduce from one model alone"
	}

	step := func(mutate func(*statusmodel.Model)) {
		saved := propCopyModel(cand)
		mutate(&cand)
		propFixGraph(&cand)
		if !fails(cand) {
			cand = saved
		}
	}
	for _, drop := range []func(*statusmodel.Model){
		func(m *statusmodel.Model) { m.Recent = nil },
		func(m *statusmodel.Model) { m.Workers = nil },
		func(m *statusmodel.Model) { m.CI = nil },
		func(m *statusmodel.Model) { m.Remaining = nil },
		func(m *statusmodel.Model) { m.Health.Verdict.Recovered = nil },
		func(m *statusmodel.Model) { m.Lifecycle.Wave = nil },
	} {
		step(drop)
	}
	for i := len(cand.Attention) - 1; i >= 0; i-- {
		i := i
		step(func(m *statusmodel.Model) {
			rest := make([]statusmodel.Attention, 0, len(m.Attention)-1)
			rest = append(rest, m.Attention[:i]...)
			rest = append(rest, m.Attention[i+1:]...)
			m.Attention = rest
		})
	}
	for i := len(cand.Cost.Lines) - 1; i >= 0; i-- {
		i := i
		step(func(m *statusmodel.Model) {
			rest := make([]statusmodel.CostLine, 0, len(m.Cost.Lines)-1)
			rest = append(rest, m.Cost.Lines[:i]...)
			rest = append(rest, m.Cost.Lines[i+1:]...)
			m.Cost.Lines = rest
		})
	}
	if cand.Waves != nil {
		for len(*cand.Waves) > 1 {
			saved := propCopyModel(cand)
			*cand.Waves = (*cand.Waves)[:len(*cand.Waves)-1]
			propFixGraph(&cand)
			if !fails(cand) {
				cand = saved
				break
			}
		}
		for wi := len(*cand.Waves) - 1; wi >= 0; wi-- {
			for len((*cand.Waves)[wi].Ticks) > 1 {
				saved := propCopyModel(cand)
				wave := &(*cand.Waves)[wi]
				wave.Ticks = wave.Ticks[:len(wave.Ticks)-1]
				propFixGraph(&cand)
				if !fails(cand) {
					cand = saved
					break
				}
			}
		}
	}
	return cand, which
}

// propFixGraph recomputes what a shrink's structural drop invalidates: parent
// links that now dangle fall away, and the progress counts recount what is
// kept.
func propFixGraph(m *statusmodel.Model) {
	if m.Waves == nil {
		return
	}
	kept := map[string]bool{}
	for _, wave := range *m.Waves {
		for _, tick := range wave.Ticks {
			kept[tick.TickID] = true
		}
	}
	total, closed := 0, 0
	for wi := range *m.Waves {
		for ti := range (*m.Waves)[wi].Ticks {
			tick := &(*m.Waves)[wi].Ticks[ti]
			total++
			if tick.State == "closed" {
				closed++
			}
			if tick.ParentTickID != nil && !kept[*tick.ParentTickID] {
				tick.ParentTickID = nil
			}
		}
	}
	if m.Progress.Ticks != nil {
		m.Progress.Ticks = &statusmodel.TickProgress{Total: total, Closed: closed, Open: total - closed}
	}
}

// propModelJSON is the failing model as JSON — the form the report prints and
// a reader can paste into a fixture.
func propModelJSON(m statusmodel.Model) string {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Sprintf("the model would not marshal: %v", err)
	}
	return string(raw)
}
