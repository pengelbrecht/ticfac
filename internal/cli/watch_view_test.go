package cli

// The dashboard half of `ticfac watch` (epic hn6, wave 3 — tick u5n): the
// frame this file pins is the spec's dashboard — the headline (identity,
// progress with the health verdict, phase bar with the needs-you answer),
// one fixed row per tick in plan order with a per-tick pipeline cell, the
// live workers with their activity, the CI and cost lines, and the two-line
// recent tail.
//
// The renderer is a pure function of the model plus the pane's width and
// height: nothing here reads a run's own records, spawns a process or
// measures a host — so the frame is pinned BYTE FOR BYTE against the
// contract bundle's `dashboard` golden (the fixture wave 1 cut, tick r5i) at
// the widths the tick names, and every rule the tick states has its own
// test: rows never move, needs-you is quiet when empty, an unmetered cost
// never wears a number, the verdict has colours, narrow panes drop columns
// in the fixed order, and a short pane collapses closed rows in place.
//
// The wiring (the redraw loop, the keep lines, the exit codes) stays in
// watch_block_test.go.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// updateGoldens regenerates the dashboard golden files from the current
// renderer: `go test -run TestDashboardGolden ./internal/cli/ -update`.
var updateGoldens = flag.Bool("update", false, "rewrite the watch dashboard golden files from the current renderer")

// plainStyles is the identity style set: every line comes back exactly as it
// was built, so a content assertion reads the words and not the escape
// codes. The colour tests use ansiWatchStyles() instead.
func plainStyles() watchStyles {
	id := func(s string) string { return s }
	return watchStyles{dim: id, amber: id, red: id, green: id, bold: id}
}

// ptr is the one-line pointer helper the model fixtures lean on.
func ptr[T any](v T) *T { return &v }

// dashboardContractGolden decodes the contract bundle's `dashboard` golden —
// the rendering fixture wave 1 cut (tick r5i): every dashboard field
// populated, admitted by the schema and held to its anchors by the
// statusmodel suite, so the byte-for-byte goldens below render a model that
// is the shape the whole epic agreed on.
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
		t.Fatal("the status model contract carries no dashboard golden — the wave-3 renderers and the phone page reference it by name")
	}
	var m statusmodel.Model
	if err := json.Unmarshal(golden, &m); err != nil {
		t.Fatalf("the dashboard golden does not decode into the status model: %v", err)
	}
	return m
}

// dashboardFixture is one mid-run epic as the model states it, with every
// field the dashboard renders populated: wave 1 done (t1 closed, its
// absorbed repair child t1c closed under it), wave 2 active (t2 dispatched
// at its gate with a live worker, t3 ready), wave 3 upcoming (t4, a review
// tick at its review stage). Healthy with recoveries, both kinds of cost
// line, a CI with one check running and one green, and a recent tail.
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
					State: "closed",
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
					State: "closed", ParentTickID: ptr("t1"), Absorbed: true,
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
					State: "dispatched", Attempt: &attempt, Try: &attempt,
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
					TickID: "t3", Title: "the third tick", Gloss: "the third tick's gloss", State: "ready",
				},
			}},
			{Wave: 3, State: statusmodel.WaveUpcoming, Ticks: []statusmodel.Tick{
				{
					TickID: "t4", Title: "the review tick", Gloss: "the review tick's gloss",
					Role: "review", State: "dispatched",
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
			RecordedUSD: 0.41, Attempts: 6,
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
// gate, t3 dispatched, t4's review still going. Same ticks, advanced states.
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
				for si := range tick.Pipeline {
					tick.Pipeline[si].State = statusmodel.StageStateDone
				}
				tick.Tries = append(tick.Tries, statusmodel.Try{Try: 3, Attempt: 3,
					Outcome: statusmodel.TryClosed, DispatchedAt: "2026-09-28T19:40:00Z", Tier: &frontier})
			case "t3":
				tick.State = "dispatched"
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
	m.Workers = &[]statusmodel.Worker{{
		TickID: "t3", Attempt: 4, Handle: ptr("herdr pane tick-t3-a4"),
	}}
	return m
}

// dashRowNames says whether one rendered frame line is the row of one named
// tick — the mark cursor, then the id (with the absorbed "+" when the run
// absorbed it), in the format dashTickRow writes. Matching is by the row's
// own head, never by content the row could share with another line.
func dashRowNames(line, id string) bool {
	rest, ok := strings.CutPrefix(line, "  └")
	if !ok {
		rest = line
	}
	for _, mark := range []string{" ", "▸"} {
		for _, prefix := range []string{mark + id + " ", mark + "+" + id + " "} {
			if strings.HasPrefix(rest, prefix) {
				return true
			}
		}
	}
	return false
}

// dashRowIDs is the sequence in which the named ticks' rows appear in the
// frame — the tick-id column the order test reads.
func dashRowIDs(frame []string, ids []string) []string {
	order := []string{}
	for _, line := range frame {
		for _, id := range ids {
			if dashRowNames(line, id) {
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
		if dashRowNames(line, id) {
			return i
		}
	}
	return -1
}

// TestDashboardGolden: the contract's `dashboard` golden renders at width
// 120 and at width 60, and both frames match the pinned testdata files byte
// for byte — the whole layout, glyphs, spacing and truncation included,
// because a dashboard a person reads is a layout, and a layout that drifts
// silently is a layout nobody agreed on. `-update` regenerates the files.
func TestDashboardGolden(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	for _, tc := range []struct {
		width int
		file  string
	}{
		{120, "watch_dashboard_120.txt"},
		{60, "watch_dashboard_60.txt"},
	} {
		frame := renderWatchFrame(m, plainStyles(), tc.width, 0, "")
		got := strings.Join(frame, "\n") + "\n"
		path := filepath.Join("testdata", tc.file)
		if *updateGoldens {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s (regenerate with -update): %v", path, err)
		}
		if got != string(want) {
			t.Errorf("the dashboard at width %d does not match %s byte for byte:\n--- got ---\n%s\n--- want ---\n%s",
				tc.width, path, got, want)
		}
	}
}

// TestDashboardColumnsSizeToContent (tick r3x): the table's columns are as
// wide as the widest content they carry — the header label included — never
// laid out across the pane with gaps no cell fills. Two short ticks size
// WHAT to five cells, leave TIER at its four-cell label and PIPELINE at the
// widest cell it carries, and the header's labels sit exactly over their
// columns.
func TestDashboardColumnsSizeToContent(t *testing.T) {
	t.Parallel()
	m := statusmodel.Model{
		RunID:       "epic-rmod",
		EpicID:      "rmod",
		Host:        statusmodel.HostLocal,
		GeneratedAt: "2026-09-28T19:20:00Z",
		Liveness: statusmodel.Liveness{
			Alive: true, State: "alive", Reason: "pid 4242", Source: "run.pid",
		},
		Health: statusmodel.Health{Verdict: statusmodel.HealthVerdict{
			State: statusmodel.VerdictHealthy,
		}},
		Waves: &[]statusmodel.Wave{{
			Wave: 1, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{
				{
					TickID: "t1", Title: "alpha", State: "closed",
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
					},
				},
				{
					TickID: "t2", Title: "beta", State: "dispatched",
					Pipeline: []statusmodel.PipelineStage{
						{Stage: statusmodel.StageClaim, State: statusmodel.StageStateDone},
						{Stage: statusmodel.StageWork, State: statusmodel.StageStateActive},
						{Stage: statusmodel.StageGate, State: statusmodel.StageStatePending},
						{Stage: statusmodel.StageMerged, State: statusmodel.StageStatePending},
					},
				},
			},
		}},
	}
	frame := renderWatchFrame(m, plainStyles(), 0, 0, "")
	var head, t1, t2 string
	for _, line := range frame {
		switch {
		case strings.HasPrefix(line, " TICK"):
			head = line
		case strings.HasPrefix(line, " t1 "):
			t1 = line
		case strings.HasPrefix(line, " t2 "):
			t2 = line
		}
	}
	for _, tc := range []struct {
		name, got, want string
	}{
		{"the header", head, " TICK  WHAT  TIER PIPELINE          TIME ATTEMPTS"},
		{"t1's row", t1, " t1    alpha      claim ✓                0"},
		{"t2's row", t2, " t2    beta       claim ▸ ●work ▸ …      0"},
	} {
		if tc.got == "" {
			t.Errorf("%s never rendered:\n%s", tc.name, strings.Join(frame, "\n"))
			continue
		}
		if tc.got != tc.want {
			t.Errorf("%s is not sized to its content:\ngot  %q\nwant %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestDashboardRowsKeepTheirOrder: one row per tick, in plan order — and the
// order is a property of the PLAN, not of the states. A successor with every
// state advanced renders the tick ids in the same sequence, on the same
// lines: a tick moves forward by its cells changing, never by its row
// moving, because a table that jumps around cannot be read at a glance.
func TestDashboardRowsKeepTheirOrder(t *testing.T) {
	t.Parallel()
	ids := []string{"t1", "t1c", "t2", "t3", "t4"}
	now := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	later := renderWatchFrame(dashboardSuccessor(), plainStyles(), 0, 0, "")

	if got := dashRowIDs(now, ids); strings.Join(got, " ") != strings.Join(ids, " ") {
		t.Errorf("the rows are not in plan order: %v\n%s", got, strings.Join(now, "\n"))
	}
	if got := dashRowIDs(later, ids); strings.Join(got, " ") != strings.Join(ids, " ") {
		t.Errorf("a successor frame reordered the rows: %v\n%s", got, strings.Join(later, "\n"))
	}
	for _, id := range ids {
		a, b := dashRowLine(now, id), dashRowLine(later, id)
		if a < 0 || b < 0 {
			t.Fatalf("tick %s lost its row (now %d, later %d):\n%s\n%s", id, a, b,
				strings.Join(now, "\n"), strings.Join(later, "\n"))
		}
		if a != b {
			t.Errorf("tick %s moved from line %d to %d between frames:\n%s\n%s", id, a, b,
				strings.Join(now, "\n"), strings.Join(later, "\n"))
		}
	}
	// The states did advance between the two frames — the order is pinned
	// with real change, not with two renders of one model.
	if strings.Join(now, "\n") == strings.Join(later, "\n") {
		t.Error("the successor frame is identical to its predecessor: the pin tested nothing")
	}
}

// TestDashboardNeedsYou: the first question. Nothing needs a person and the
// header says so, dim and quiet, at the phase bar's right. A hold shows in
// the header, amber, on its own line, with the one command that clears it.
func TestDashboardNeedsYou(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	frame := renderWatchFrame(m, plainStyles(), 0, 0, "")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "needs you: nothing") {
		t.Errorf("nothing-needs-you is not shown when true:\n%s", joined)
	}
	coloured := strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(coloured, "\x1b[2mneeds you: nothing\x1b[0m") {
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
	if strings.Contains(joined, "needs you: nothing") {
		t.Errorf("a held run still says nothing needs a person:\n%s", joined)
	}
	want := "needs you: attempt 2 of t2 struck out: the refusal the run recorded — ticfac settle rmod t2 2 --release \"<who>\""
	if !strings.Contains(joined, want) {
		t.Errorf("the hold does not show what and the clearing command:\n%s", joined)
	}
	coloured = strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(coloured, "\x1b[33m"+want+"\x1b[0m") {
		t.Errorf("the hold line is not amber:\n%s", coloured)
	}
}

// TestDashboardHoldCommandSurvivesNarrowPanes: a hold's clearing command
// cannot fall out of the dashboard because the pane is narrow (tick 9um).
// Where the pane seats the whole line it renders as the one line it always
// was; where it does not, the renderer wraps under the announcement — the
// what continues indented and the command keeps every one of its words on
// lines of its own — instead of truncating the line and dropping the
// command off the pane's edge (the old renderer rendered the line below at
// width 30 as "needs you: attempt 2 of t2 str").
func TestDashboardHoldCommandSurvivesNarrowPanes(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Attention = []statusmodel.Attention{{
		Kind:           statusmodel.WaitHeldForPerson,
		What:           "attempt 2 of t2 struck out: the refusal the run recorded",
		NeedsPerson:    true,
		UnblockCommand: ptr(`ticfac settle rmod t2 2 --release "<who>"`),
	}}
	want := "needs you: attempt 2 of t2 struck out: the refusal the run recorded — ticfac settle rmod t2 2 --release \"<who>\""

	// A pane that seats the line keeps it whole, exactly as it was.
	for _, width := range []int{0, 120} {
		frame := renderWatchFrame(m, plainStyles(), width, 0, "")
		if !slices.Contains(frame, want) {
			t.Errorf("at width %d the hold line does not render whole:\n%s", width, strings.Join(frame, "\n"))
		}
	}

	// A 30-column pane: the wrap keeps the announcement and every word of
	// the command, each line inside the pane, nothing cut mid-word.
	frame := renderWatchFrame(m, plainStyles(), 30, 0, "")
	joined := strings.Join(frame, "\n")
	wrapped := []string{
		"needs you: attempt 2 of t2",
		"  struck out: the refusal the",
		"  run recorded",
		"  ticfac settle rmod t2 2",
		`  --release "<who>"`,
	}
	for i, line := range wrapped {
		if !slices.Contains(frame, line) {
			t.Errorf("at width 30 the hold's wrapped line %d is missing:\n%s", i+1, joined)
		}
	}
	for _, word := range []string{"ticfac", "settle", "rmod", "--release", `"<who>"`} {
		if !strings.Contains(joined, word) {
			t.Errorf("at width 30 the clearing command lost %q:\n%s", word, joined)
		}
	}
	for i, line := range frame {
		if w := ansi.StringWidth(line); w > 30 {
			t.Errorf("frame line %d is %d cells wide in a 30-column pane: %q", i, w, line)
		}
	}
	// The wrap colours every line the hold carries, not only its first.
	coloured := renderWatchFrame(m, ansiWatchStyles(), 30, 0, "")
	for i, line := range wrapped {
		if !slices.Contains(coloured, "\x1b[33m"+line+"\x1b[0m") {
			t.Errorf("the hold's wrapped line %d is not amber:\n%s", i+1, strings.Join(coloured, "\n"))
		}
	}
}

// TestDashboardShortPaneKeepsHoldBlocksWhole: when a short pane cannot carry
// every hold, the height fit drops a later hold's block whole rather than
// cutting one in half — a hold shown without the command that clears it is
// the defect the wrap exists to prevent (tick 9um), so what the frame shows
// of a hold is all of it, and a hold that does not fit is not shown at all.
func TestDashboardShortPaneKeepsHoldBlocksWhole(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Attention = []statusmodel.Attention{
		{
			Kind:           statusmodel.WaitHeldForPerson,
			What:           "attempt 2 of t2 struck out: the refusal the run recorded",
			NeedsPerson:    true,
			UnblockCommand: ptr(`ticfac settle rmod t2 2 --release "<who>"`),
		},
		{
			Kind:           statusmodel.WaitHeldForPerson,
			What:           "a findings draft on t2 waits for triage",
			NeedsPerson:    true,
			UnblockCommand: ptr("ticfac triage rmod"),
		},
	}
	frame := renderWatchFrame(m, plainStyles(), 30, 8, "")
	joined := strings.Join(frame, "\n")

	// The first hold fits the pane's height and shows whole, command and
	// all.
	for _, line := range []string{
		"needs you: attempt 2 of t2",
		"  struck out: the refusal the",
		"  run recorded",
		"  ticfac settle rmod t2 2",
		`  --release "<who>"`,
	} {
		if !slices.Contains(frame, line) {
			t.Errorf("the first hold's wrapped line %q is missing:\n%s", line, joined)
		}
	}
	// The second hold does not fit: it is dropped whole — no announcement
	// without the command under it, no half a block.
	for _, line := range []string{
		"needs you: a findings draft on",
		"  t2 waits for triage",
		"  ticfac triage rmod",
	} {
		if slices.Contains(frame, line) {
			t.Errorf("the second hold's block was cut rather than dropped: %q:\n%s", line, joined)
		}
	}
	if got := len(frame); got > 8 {
		t.Errorf("a 8-line pane rendered %d lines:\n%s", got, joined)
	}
}

// TestDashboardNeverPrintsZeroForUnmetered: honest cost (hn6 rule 7). A
// metered line prints its measured number; an unmetered line says "not
// metered" — an unmetered line wearing a $0.00 is a fabricated spend. A
// metered zero is a measured zero and prints as one. No lines at all and
// the whole cost says so.
func TestDashboardNeverPrintsZeroForUnmetered(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	for _, want := range []string{"Workers AI $0.41", "claude not metered"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the cost line does not carry %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "$0.00") {
		t.Errorf("an unmetered cost wears a fabricated $0.00:\n%s", joined)
	}

	// No lines at all: the cost is honestly unmetered, not silently zero.
	m.Cost.Lines = nil
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "cost not metered") {
		t.Errorf("a run with no cost lines does not say the cost is not metered:\n%s", joined)
	}

	// A metered zero is a measured zero: it keeps its number.
	m.Cost.Lines = []statusmodel.CostLine{{
		Source: statusmodel.CostSourceWorkersAI, Metered: true, USD: ptr(0.0), Attempts: 4,
		Basis: "gateway usage for 4 dispatches",
	}}
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "Workers AI $0.00") {
		t.Errorf("a metered zero does not print its measured number:\n%s", joined)
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
// fixed order, and no line is ever wider than the pane. At 99 the pipeline
// cell is one glyph per stage and the worker's LAST action moves under its
// row; at 63 the TIER and ATTEMPTS columns are gone; at 47 the work's own
// name is gone too — and the tick's identity never goes at any width.
func TestDashboardNarrowWidths(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	ids := []string{"060", "823", "46x", "v7z"}

	w99 := renderWatchFrame(m, plainStyles(), 99, 0, "")
	joined99 := strings.Join(w99, "\n")
	if strings.Contains(joined99, "claim ▸") {
		t.Errorf("a 99-column pane still spells the pipeline out in words:\n%s", joined99)
	}
	if !strings.Contains(joined99, "✓ ✓ ✓ ✓") {
		t.Errorf("a 99-column pane does not render one glyph per stage:\n%s", joined99)
	}
	if worker := dashWorkerPanelLine(w99, "herdr pane tick-46x-a6"); worker < 0 ||
		strings.Contains(w99[worker], "ran go test ./internal/reconcile") {
		t.Errorf("a 99-column pane still seats the worker's LAST action on its row:\n%s", joined99)
	} else if !strings.Contains(w99[worker+1], "ran go test ./internal/reconcile") {
		t.Errorf("a 99-column pane did not move the worker's LAST action under its row:\n%s", joined99)
	}

	w63 := renderWatchFrame(m, plainStyles(), 63, 0, "")
	joined63 := strings.Join(w63, "\n")
	if strings.Contains(joined63, "ATTEMPTS") || strings.Contains(joined63, "frontier") {
		t.Errorf("a 63-column pane still shows the TIER and ATTEMPTS columns:\n%s", joined63)
	}
	if !strings.Contains(joined63, "port sandbox verbs to ticfac") {
		t.Errorf("a 63-column pane dropped the WHAT column too early:\n%s", joined63)
	}

	w47 := renderWatchFrame(m, plainStyles(), 47, 0, "")
	joined47 := strings.Join(w47, "\n")
	if strings.Contains(joined47, "port sandbox verbs to ticfac") || strings.Contains(joined47, "WHAT") {
		t.Errorf("a 47-column pane still shows the WHAT column:\n%s", joined47)
	}
	if !strings.Contains(joined47, "TICK") {
		t.Errorf("a 47-column pane dropped the table's identity column:\n%s", joined47)
	}

	for _, tc := range []struct {
		width int
		frame []string
	}{
		{99, w99}, {63, w63}, {47, w47},
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

// dashWorkerPanelLine is the frame line one worker's row rendered on, or -1.
func dashWorkerPanelLine(frame []string, handle string) int {
	for i, line := range frame {
		if strings.Contains(line, handle) {
			return i
		}
	}
	return -1
}

// TestDashboardFitsHeight: a pane shorter than the frame keeps the headline
// and the tail, and compresses the middle in place — contiguous runs of
// closed rows collapse into one dim "✓ N closed" line standing where the
// run's first row stood, so no row moves relative to another — and whatever
// still does not fit is counted by one "+N more" at the fold, the whole
// frame never taller than the pane.
func TestDashboardFitsHeight(t *testing.T) {
	t.Parallel()
	m := dashboardContractGolden(t)
	full := renderWatchFrame(m, plainStyles(), 120, 0, "")
	frame := renderWatchFrame(m, plainStyles(), 120, 12, "")
	joined := strings.Join(frame, "\n")

	if len(frame) > 12 {
		t.Errorf("a 12-line pane rendered %d lines:\n%s", len(frame), joined)
	}
	if !strings.Contains(joined, "✓ 2 closed") {
		t.Errorf("the closed rows did not collapse in place:\n%s", joined)
	}
	if !strings.Contains(joined, "+5 more") {
		t.Errorf("the fold does not count what it dropped:\n%s", joined)
	}
	// In place: the collapsed line stands where the run's FIRST row stood
	// (060's), and the rows the collapse left keep their order around it —
	// 46x after the run, v7z after 46x, exactly as they were.
	if got := dashRowLine(frame, "46x"); got != dashRowLine(full, "060")+1 {
		t.Errorf("the collapsed run did not stand in 060's place: the 46x row sits at %d, want %d:\n%s",
			got, dashRowLine(full, "060")+1, joined)
	}
	if a, b := dashRowLine(frame, "46x"), dashRowLine(frame, "v7z"); !(0 <= a && a < b) {
		t.Errorf("the rows the collapse kept reordered:\n%s", joined)
	}
	// The headline and the tail always survive the cut.
	for _, want := range []string{
		m.EpicID + " " + *m.EpicTitle,
		"2/4 ticks",
		"● healthy",
		"needs you: nothing",
		"─ recent",
		"[e] events  [enter] tick",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("a 12-line pane dropped %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "herdr pane tick-46x-a6") {
		t.Errorf("a 12-line pane kept the workers panel while claiming +5 more:\n%s", joined)
	}

	// Unknown height (0): everything, nothing collapsed or counted.
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
// does, so a row's id never shifts when the cursor moves onto or off it.
func TestTheFrameSeatsTheDrillInMarker(t *testing.T) {
	t.Parallel()
	noMarker := func(frame []string) bool {
		for _, line := range frame {
			rest, _ := strings.CutPrefix(line, "  └")
			if strings.HasPrefix(rest, "▸") || strings.HasPrefix(line, "▸") {
				return false
			}
		}
		return true
	}
	frame := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "t2")
	joined := strings.Join(frame, "\n")
	if !strings.Contains(joined, "▸t2") {
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

// TestTheFrameWorkersPanel: one row per live worker with its model,
// executor and handle, its sparkline scaled to the window's own maximum and
// a dot per bucket where nothing happened, its last action with its age,
// and the nudge count in amber when the run nudged it as stuck. A census
// this machine cannot take is said; a census that read and found nothing
// shows no panel at all.
func TestTheFrameWorkersPanel(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	for _, want := range []string{
		"WORKERS",
		"ACTIVITY (10m)",
		"LAST",
		"t2  glm-5.3 · herdr · herdr pane tick-t2-a2",
		"▁▃▅█▇▅▃▁▂▅",
		"ran go test ./internal/reconcile (1m ago)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the workers panel does not carry %q:\n%s", want, joined)
		}
	}
	coloured := strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(coloured, "\x1b[33mnudged ×1\x1b[0m") {
		t.Errorf("the nudge count is not amber:\n%s", coloured)
	}

	// An empty window reads as dots, one per bucket.
	m.Workers = &[]statusmodel.Worker{{
		TickID: "t2", Attempt: 2, Handle: ptr("herdr pane tick-t2-a2"),
		Activity: &statusmodel.WorkerActivity{WindowSeconds: 600,
			Buckets: []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
	}}
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "··········") {
		t.Errorf("an empty activity window does not read as dots:\n%s", joined)
	}

	// A census this machine cannot take is said, not faked.
	m.Workers = nil
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "workers run in the cloud — not visible from here") {
		t.Errorf("a cloud run's workers are not said to be invisible:\n%s", joined)
	}

	// A census that read and found nothing stands behind no empty panel.
	m.Workers = &[]statusmodel.Worker{}
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "WORKERS") {
		t.Errorf("an empty census still renders a workers panel:\n%s", joined)
	}
}

// TestTheFrameCILine: the forge's answer on the epic PR's head, per check —
// a running check with its age, a green one plain, a red one red — and no PR
// yet said dimly, because a run that has not opened its PR is a fact, not a
// silence.
func TestTheFrameCILine(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "CI #98 go ◐ 6m · ts ✓") {
		t.Errorf("the CI line does not name the PR and its checks:\n%s", joined)
	}
	coloured := strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(coloured, "\x1b[33m◐ 6m\x1b[0m") {
		t.Errorf("a running check is not amber:\n%s", coloured)
	}

	m.CI = nil
	joined = strings.Join(renderWatchFrame(m, ansiWatchStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "\x1b[2mCI: no PR yet\x1b[0m") {
		t.Errorf("a run without a PR does not say so dimly:\n%s", joined)
	}
}

// TestTheFrameTailIsTheFeedOwnWords: the tail is the feed's last two lines
// through the same one-line form the stream path prints — the same words on
// both paths — under the "─ recent" rule, with the key hint at the right.
// The try each line's prefix names is the MODEL's own try for the event's
// attempt (tick s71): the whole try history the rows carry, not the
// five-line window the tail itself is — expected here independently, off
// the tick's own Try row, so the test says the number rather than the
// implementation's way of counting it.
func TestTheFrameTailIsTheFeedOwnWords(t *testing.T) {
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
		line := fmt.Sprintf("%s %-12s %s: %s", clockOf(e.At), who, e.Stage, e.Detail)
		if !strings.Contains(joined, line) {
			t.Errorf("the tail does not carry the feed's own line for %s:\n%s", e.Stage, joined)
		}
		seen++
	}
	if seen != 2 {
		t.Errorf("the tail is not two lines: saw %d of the model's recent events\n%s", seen, joined)
	}
	if !strings.Contains(joined, "─ recent") {
		t.Errorf("the tail does not carry its section rule:\n%s", joined)
	}
	if !strings.Contains(joined, "[e] events  [enter] tick") {
		t.Errorf("the tail does not carry the key hint:\n%s", joined)
	}

	// An empty feed still carries the rule and the hint.
	m.Recent = nil
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "─ recent") || !strings.Contains(joined, "[e] events  [enter] tick") {
		t.Errorf("an empty feed lost the rule or the hint:\n%s", joined)
	}
}

// TestTheTailCountsTheTryFromTheWholeModel: the tail's "<tick>#<n>" prefix
// names the tick's own try as the WHOLE model states it (tick s71), not as
// the five-line window the tail itself is — a tick on its third try whose
// recent events are all its third attempt counted #1 there, disagreeing
// with the model's own row and with the [e] feed, which count the whole
// run. The prefix is counted from the try histories the rows carry, so the
// tail, the table and the feed read one number.
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
				TickID: ptr("t9"), Attempt: ptr(3), Stage: "dispatched",
				Detail: "t9 dispatched again"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:05:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: "collected",
				Detail: "t9 collected its attempt"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:06:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: "report_ready",
				Detail: "t9 wrote its report"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:07:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: "gate_passed",
				Detail: "t9 passed the integrated gate"},
			{SchemaVersion: runfeed.SchemaVersion, At: "2026-10-04T12:08:00Z", RunID: "epic-rmod",
				TickID: ptr("t9"), Attempt: ptr(3), Stage: "merged",
				Detail: "t9 merged"},
		},
		Waves: &[]statusmodel.Wave{{
			Wave: 1, State: statusmodel.WaveActive, Ticks: []statusmodel.Tick{{
				TickID: "t9", Title: "the retried tick", State: "dispatched",
				Attempt: ptr(3), Try: ptr(3), Tier: &strong,
				Tries: []statusmodel.Try{
					{Try: 1, Attempt: 1, Outcome: statusmodel.TryRejected,
						DispatchedAt: "2026-10-04T10:00:00Z", Tier: &strong,
						Reason: ptr("gofmt drifted in two files")},
					{Try: 2, Attempt: 2, Outcome: statusmodel.TryGateFailed,
						DispatchedAt: "2026-10-04T11:00:00Z", Tier: &strong},
					{Try: 3, Attempt: 3, Outcome: statusmodel.TryInFlight,
						DispatchedAt: "2026-10-04T12:04:00Z", Tier: &strong},
				},
			}},
		}},
	}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	// The tail's two lines (the merged and gate_passed events) both name the
	// tick's THIRD try — the number the model's own row and the [e] feed say.
	if !strings.Contains(joined, "t9#3") {
		t.Errorf("the tail does not name the model's own try for its events:\n%s", joined)
	}
	if !strings.Contains(joined, "merged: t9 merged") || !strings.Contains(joined, "gate_passed: t9 passed the integrated gate") {
		t.Errorf("the tail is not the feed's own last words:\n%s", joined)
	}
	if strings.Contains(joined, "t9#1") || strings.Contains(joined, "t9#2") {
		t.Errorf("the tail counted the try from the five-line window, not the model:\n%s", joined)
	}
}

// TestTheFrameRendersTheModelsOwnElapsed: the header's clock is the model's
// own field, not a renderer derivation — the golden's measured span renders
// beside the counts (2h9m: the golden's earliest dispatch to its own
// generated_at), and a model that states no elapsed renders none: the
// renderer derives nothing, so the field cannot lie and the TUI cannot drift
// from the phone page that reads the same model (tick e6g).
func TestTheFrameRendersTheModelsOwnElapsed(t *testing.T) {
	t.Parallel()
	golden := dashboardContractGolden(t)
	if golden.Progress.RunElapsedSeconds == nil {
		t.Fatal("the dashboard golden carries no run elapsed: the fixture for the header's clock is missing")
	}
	joined := strings.Join(renderWatchFrame(golden, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "2/4 ticks · 2h9m") {
		t.Errorf("the model's own elapsed does not render beside the counts:\n%s", joined)
	}

	golden.Progress.RunElapsedSeconds = nil
	joined = strings.Join(renderWatchFrame(golden, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "2h9m") {
		t.Errorf("a model that states no elapsed still renders one:\n%s", joined)
	}
}

// TestTheFrameShowsTheETAOnlyWhenMeasured: the approximate time left shows
// as "ETA ~…" only where the model measured it — a number nobody measured
// is a number that lies.
func TestTheFrameShowsTheETAOnlyWhenMeasured(t *testing.T) {
	t.Parallel()
	m := dashboardFixture()
	m.Remaining = &statusmodel.Remaining{ApproximateSeconds: 2400, Basis: "3 open ticks × the 20m median of closed ones"}
	joined := strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if !strings.Contains(joined, "ETA ~40m") {
		t.Errorf("a measured remaining time does not render as an ETA:\n%s", joined)
	}

	m.Remaining = nil
	joined = strings.Join(renderWatchFrame(m, plainStyles(), 0, 0, ""), "\n")
	if strings.Contains(joined, "ETA") {
		t.Errorf("an unmeasured run still claims an ETA:\n%s", joined)
	}
}

// TestTheFrameMarksChildrenAndAbsorbedRows: a child renders indented under
// the row its parent_tick_id names, and a tick the run absorbed by absorbing
// a finding is a marked new row — the epic's shape changed mid-run, and a
// renderer hides that from nobody.
func TestTheFrameMarksChildrenAndAbsorbedRows(t *testing.T) {
	t.Parallel()
	frame := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "")
	line := dashRowLine(frame, "t1c")
	if line < 0 || !strings.HasPrefix(frame[line], "  └ +t1c") {
		t.Errorf("an absorbed child does not render as an indented marked row:\n%s", strings.Join(frame, "\n"))
	}
	selected := renderWatchFrame(dashboardFixture(), plainStyles(), 0, 0, "t1c")
	if line := dashRowLine(selected, "t1c"); line < 0 || !strings.HasPrefix(selected[line], "  └▸+t1c") {
		t.Errorf("a selected child does not render as an indented marked row:\n%s", strings.Join(selected, "\n"))
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

// TestWatchPluralRetries: the first-use bug's fix, pinned. A word ending in
// consonant+y pluralizes as "ies" — "remote retry" reads "remote retries",
// never "remote retrys" — while a vowel keeps the plain s, and the rest of
// the frame's counts are untouched by the rule.
func TestWatchPluralRetries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		n    int
		what string
		want string
	}{
		{1, "remote retry", "1 remote retry"},
		{2, "remote retry", "2 remote retries"},
		{14, "remote retry", "14 remote retries"},
		{2, "party", "2 parties"},
		{3, "day", "3 days"},
		{2, "attempt", "2 attempts"},
	} {
		if got := watchPlural(tc.n, tc.what); got != tc.want {
			t.Errorf("watchPlural(%d, %q) = %q, want %q", tc.n, tc.what, got, tc.want)
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
