package statusmodel

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The status word suite (epic ymf, tick lck): every word the dashboard
// renders, derived from the same recorded fixtures the pipeline derivation
// reads, one test per word — plus the exception note beside it, the groups
// the words bucket into, and the phase track the lifecycle folds. Each
// case's whole model still validates against the contract, the way the
// pipeline suite's do.

// statusSources assembles Sources for one status-word case: the graph's
// waves, the records, the feed, the census and the liveness the case states.
func statusSources(waves []tk.GraphWave, recs *Records, feed []runfeed.Event, standing []runprogress.Attempt, alive bool) Sources {
	if recs == nil {
		recs = &Records{}
	}
	return Sources{
		Now:          testNow,
		RunID:        "run-pip",
		Host:         HostLocal,
		EpicID:       "pip",
		Graph:        &tk.Graph{Epic: tk.GraphEpic{ID: "pip", Title: "the status fixture epic"}, Waves: waves},
		Records:      recs,
		Feed:         feed,
		Standing:     standing,
		StandingRead: true,
		Liveness: LivenessInput{
			Alive: alive, State: map[bool]string{true: "alive", false: "not_running"}[alive],
			Reason: "the fixture's probe answered", Source: "run.pid",
		},
	}
}

// statusCase is one scenario's records: the checkpoint's tick rows, the
// dispatch markers, the gate evidence, the findings.
type statusCase struct {
	rows     []runstate.TickState
	markers  []runstate.Attempt
	evidence []runstate.Evidence
	findings []runstate.Finding
}

func (c statusCase) records() *Records {
	return &Records{
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
		Attempts: c.markers,
		Evidence: c.evidence,
		Findings: c.findings,
	}
}

// statusTick finds one tick's row in the built model.
var statusTick = pipelineTick

// TestTheStatusWords: one case per word of the vocabulary, each derived from
// the records the pipeline cell is derived from — so the word and the cell
// cannot disagree, and a word no record states is a word no case can produce.
func TestTheStatusWords(t *testing.T) {
	t.Parallel()

	closed := func() statusCase {
		return statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "closed", Attempt: 1}},
			markers: []runstate.Attempt{attemptMarker(1, "aaa", "2026-09-27T03:00:00Z", "strong", "claude-opus-5", "local-subprocess")},
		}
	}
	closedFeed := func(tick string) []runfeed.Event {
		return []runfeed.Event{
			line(testNow.Add(-40*time.Minute), tick, 0, reconcile.StageClaimed, "claimed for the fixture"),
			line(testNow.Add(-30*time.Minute), tick, 1, reconcile.StageCollected, "attempt 1 of "+tick+" collected"),
			line(testNow.Add(-25*time.Minute), tick, 1, reconcile.StageGateStarted, "the integrated gate started"),
			line(testNow.Add(-20*time.Minute), tick, 1, reconcile.StageGatePassed, "the integrated gate passed"),
			line(testNow.Add(-19*time.Minute), tick, 1, reconcile.StageClosed, "closed behind the integrated gate"),
		}
	}

	t.Run("merged is a closed implement tick", func(t *testing.T) {
		t.Parallel()
		c := closed()
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), closedFeed("aaa"), nil, true))
		if got := statusTick(t, model, "aaa").Status; got != WordMerged {
			t.Errorf("a closed implement tick reads %q, want %q", got, WordMerged)
		}
	})

	t.Run("done is a closed role tick", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "rrl", State: "closed", Attempt: 9}},
			markers: []runstate.Attempt{attemptMarker(9, "rrl", "2026-09-27T03:00:00Z", "strong", "claude-opus-5", "local-subprocess")},
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "rrl", Title: "R", Status: "open", Role: "closeout"}}}},
			c.records(), closedFeed("rrl"), nil, true))
		if got := statusTick(t, model, "rrl").Status; got != WordDone {
			t.Errorf("a closed role tick reads %q, want %q", got, WordDone)
		}
	})

	t.Run("writing code is a live implement attempt", func(t *testing.T) {
		t.Parallel()
		idle := runprogress.Duration(2 * time.Minute)
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "claude-opus-5", "local-subprocess")},
		}
		standing := []runprogress.Attempt{{
			TickID: "aaa", Attempt: 3,
			Branch:     "refs/heads/ticfac/run-pip/tick-aaa/attempt-3",
			BranchIdle: &idle, WorktreeIdle: &idle,
		}}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), nil, standing, true))
		if got := statusTick(t, model, "aaa").Status; got != WordWritingCode {
			t.Errorf("a live implement attempt reads %q, want %q", got, WordWritingCode)
		}
	})

	t.Run("reviewing and closing out are the role ticks' live work", func(t *testing.T) {
		t.Parallel()
		for role, want := range map[string]string{"review": WordReviewing, "closeout": WordClosingOut} {
			c := statusCase{
				rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 2}},
				markers: []runstate.Attempt{attemptMarker(2, "aaa", "2026-09-27T05:00:00Z", "strong", "claude-opus-5", "local-subprocess")},
			}
			model := Build(statusSources(
				[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open", Role: role}}}},
				c.records(), nil, nil, true))
			if got := statusTick(t, model, "aaa").Status; got != want {
				t.Errorf("a live %s tick reads %q, want %q", role, got, want)
			}
		}
	})

	t.Run("testing is the gate's running", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "reported", Attempt: 5}},
			markers: []runstate.Attempt{attemptMarker(5, "aaa", "2026-09-27T05:00:00Z", "frontier", "@cf/zai-org/glm-5.3", "herdr")},
		}
		feed := []runfeed.Event{
			line(testNow.Add(-28*time.Minute), "aaa", 5, reconcile.StageCollected, "attempt 5 of aaa collected"),
			line(testNow.Add(-27*time.Minute), "aaa", 5, reconcile.StageGateStarted, "the integrated gate started on aaa"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), feed, nil, true))
		if got := statusTick(t, model, "aaa").Status; got != WordTesting {
			t.Errorf("a tick at its gate reads %q, want %q", got, WordTesting)
		}
	})

	t.Run("merging is the moment between an all-pass gate and the integrate", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "reported", Attempt: 5}},
			markers: []runstate.Attempt{attemptMarker(5, "aaa", "2026-09-27T05:00:00Z", "frontier", "@cf/zai-org/glm-5.3", "herdr")},
		}
		feed := []runfeed.Event{
			line(testNow.Add(-28*time.Minute), "aaa", 5, reconcile.StageCollected, "attempt 5 of aaa collected"),
			line(testNow.Add(-27*time.Minute), "aaa", 5, reconcile.StageGateStarted, "the integrated gate started on aaa"),
			line(testNow.Add(-25*time.Minute), "aaa", 5, reconcile.StageGatePassed, "the integrated gate passed"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), feed, nil, true))
		if got := statusTick(t, model, "aaa").Status; got != WordMerging {
			t.Errorf("a gate-passed, not-yet-integrated tick reads %q, want %q", got, WordMerging)
		}
	})

	t.Run("claimed is the run's grip before any work is seen", func(t *testing.T) {
		t.Parallel()
		feed := []runfeed.Event{
			line(testNow.Add(-2*time.Minute), "aaa", 0, reconcile.StageClaimed, "claimed for the fixture"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			nil, feed, nil, true))
		if got := statusTick(t, model, "aaa").Status; got != WordClaimed {
			t.Errorf("a claimed, not-yet-dispatched tick reads %q, want %q", got, WordClaimed)
		}
	})

	t.Run("up next is an unclaimed tick", func(t *testing.T) {
		t.Parallel()
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "aaa", Title: "A", Status: "open"},
				{ID: "bbb", Title: "B", Status: "open"},
			}}},
			statusCase{
				rows:    []runstate.TickState{{TickID: "aaa", State: "closed", Attempt: 1}},
				markers: []runstate.Attempt{attemptMarker(1, "aaa", "2026-09-27T03:00:00Z", "strong", "m", "local-subprocess")},
			}.records(), closedFeed("aaa"), nil, true))
		if got := statusTick(t, model, "bbb").Status; got != WordUpNext {
			t.Errorf("an unclaimed tick in the frontier wave reads %q, want %q", got, WordUpNext)
		}
	})

	t.Run("up next is also a tick its wave has not reached", func(t *testing.T) {
		t.Parallel()
		c := closed()
		model := Build(statusSources(
			[]tk.GraphWave{
				{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}},
				{Wave: 2, Tasks: []tk.GraphTask{{ID: "bbb", Title: "B", Status: "open", BlockedBy: []string{"aaa"}}}},
			},
			c.records(), closedFeed("aaa"), nil, true))
		if got := statusTick(t, model, "bbb").Status; got != WordUpNext {
			t.Errorf("an upcoming wave's tick reads %q, want %q: the wave is the blocker", got, WordUpNext)
		}
	})

	t.Run("waiting names the open work a tick is blocked behind", func(t *testing.T) {
		t.Parallel()
		c := closed()
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "aaa", Title: "A", Status: "open"},
				{ID: "ccc", Title: "the blocker", Status: "open"},
				{ID: "bbb", Title: "B", Status: "open", BlockedBy: []string{"ccc"}},
			}}},
			c.records(), closedFeed("aaa"), nil, true))
		if got := statusTick(t, model, "bbb").Status; got != WordWaitingPrefix+"the blocker" {
			t.Errorf("a blocked tick reads %q, want %q", got, WordWaitingPrefix+"the blocker")
		}
	})

	t.Run("waiting says which blockers are held", func(t *testing.T) {
		t.Parallel()
		c := closed()
		feed := append(closedFeed("aaa"),
			line(testNow.Add(-2*time.Minute), "ccc", 0, reconcile.StageRunHeld, "claim_width: the width pip declares is full"))
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "aaa", Title: "A", Status: "open"},
				{ID: "ccc", Title: "the blocker", Status: "open"},
				{ID: "bbb", Title: "B", Status: "open", BlockedBy: []string{"ccc"}},
			}}},
			c.records(), feed, nil, true))
		if got := statusTick(t, model, "bbb").Status; got != WordWaitingPrefix+"the blocker is held" {
			t.Errorf("a tick behind a held one reads %q, want %q", got, WordWaitingPrefix+"the blocker is held")
		}
		if got := statusTick(t, model, "ccc").Status; !strings.HasPrefix(got, WordHeldPrefix) {
			t.Errorf("the held tick reads %q, want the held word", got)
		}
	})

	t.Run("waiting for CI is the close-out's gate on the PR", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "co", State: "reported", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "co", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		feed := []runfeed.Event{
			line(testNow.Add(-10*time.Minute), "", 0, reconcile.StageCloseoutHeld, "the close-out waits for CI green on the PR"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "co", Title: "CO", Status: "open", Role: "closeout"}}}},
			c.records(), feed, nil, true))
		if got := statusTick(t, model, "co").Status; got != WordWaitingForCI {
			t.Errorf("a close-out held on CI reads %q, want %q", got, WordWaitingForCI)
		}
	})

	t.Run("failed carries the run's own sentence for why", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "ccc", State: "rejected", Attempt: 4}},
			markers: []runstate.Attempt{attemptMarker(4, "ccc", "2026-09-27T03:00:00Z", "strong", "@cf/zai-org/glm-5.3", "local-subprocess")},
		}
		feed := []runfeed.Event{
			line(testNow.Add(-80*time.Minute), "ccc", 4, reconcile.StageGateStarted, "the integrated gate started on ccc"),
			line(testNow.Add(-75*time.Minute), "ccc", 4, reconcile.StageGateFailed, pipelineLongDetail),
			line(testNow.Add(-74*time.Minute), "ccc", 4, reconcile.StageRejected, pipelineLongDetail),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "ccc", Title: "C", Status: "open"}}}},
			c.records(), feed, nil, true))
		want := WordFailedPrefix + strings.TrimPrefix(pipelineCutDetail, "gate_failed: ")
		if got := statusTick(t, model, "ccc").Status; got != want {
			t.Errorf("a gate-refused tick reads %q, want %q", got, want)
		}
	})

	t.Run("failed alone when no record states why", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "ccc", State: "rejected", Attempt: 4}},
			markers: []runstate.Attempt{attemptMarker(4, "ccc", "2026-09-27T03:00:00Z", "strong", "m", "local-subprocess")},
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "ccc", Title: "C", Status: "open"}}}},
			c.records(), nil, nil, true))
		if got := statusTick(t, model, "ccc").Status; got != WordFailed {
			t.Errorf("an unexplained refusal reads %q, want %q: a reason nobody wrote is never invented", got, WordFailed)
		}
	})

	t.Run("failed is CI's own red on the close-out", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "co", State: "reported", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "co", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		src := statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "co", Title: "CO", Status: "open", Role: "closeout"}}}},
			c.records(), nil, nil, true)
		src.CI = &CIInput{State: "red"}
		model := Build(src)
		if got := statusTick(t, model, "co").Status; got != WordFailedPrefix+"CI is red on the epic PR" {
			t.Errorf("a close-out behind a red CI reads %q, want the failed word", got)
		}
	})

	t.Run("held carries the hold's own words", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		four := 4
		feed := []runfeed.Event{
			runfeed.NewEvent(testNow.Add(-10*time.Minute), "run-pip", "aaa", &four, reconcile.StageRunHeld,
				"attempt 3 of aaa struck out: the refusal the run recorded"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), feed, nil, true))
		want := WordHeldPrefix + "attempt 3 of aaa struck out: the refusal the run recorded"
		if got := statusTick(t, model, "aaa").Status; got != want {
			t.Errorf("a held tick reads %q, want %q", got, want)
		}
	})

	t.Run("a struck-out attempt that a settle released is not held", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "rejected", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		three := 3
		feed := []runfeed.Event{
			runfeed.NewEvent(testNow.Add(-10*time.Minute), "run-pip", "aaa", &three, reconcile.StageHeld, "attempt 3 struck out: the refusal"),
			runfeed.NewEvent(testNow.Add(-9*time.Minute), "run-pip", "aaa", &three, reconcile.StageSettled, "settled by the operator"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), feed, nil, true))
		if got := statusTick(t, model, "aaa").Status; got != WordFailed {
			t.Errorf("a released hold reads %q, want %q: the settle answered the hold", got, WordFailed)
		}
	})

	t.Run("a hold a prior run left is still held", func(t *testing.T) {
		t.Parallel()
		src := statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			statusCase{
				rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
				markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
			}.records(), nil, nil, true)
		three := 3
		src.PriorRecords = []Records{{Checkpoint: priorCheckpoint("run_prior", "failed")}}
		src.PriorFeeds = map[string][]runfeed.Event{"run_prior": {
			runfeed.NewEvent(testNow.Add(-2*time.Hour), "run_prior", "aaa", &three, reconcile.StageRunHeld,
				"attempt 3 of aaa struck out: the refusal the run recorded"),
		}}
		model := Build(src)
		want := WordHeldPrefix + "attempt 3 of aaa struck out: the refusal the run recorded"
		if got := statusTick(t, model, "aaa").Status; got != want {
			t.Errorf("a tick with a standing prior hold reads %q, want %q", got, want)
		}
	})

	t.Run("a hold a resume settled is history", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		three := 3
		feed := []runfeed.Event{
			runfeed.NewEvent(testNow.Add(-40*time.Minute), "run-pip", "aaa", &three, reconcile.StageRunHeld, "attempt 3 struck out: the refusal"),
			runfeed.NewEvent(testNow.Add(-39*time.Minute), "run-pip", "", nil, reconcile.StageResumed, "the run is resumed under the same run id"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), feed, nil, true))
		if got := statusTick(t, model, "aaa").Status; got != WordWritingCode {
			t.Errorf("a tick whose hold a resume settled reads %q, want %q", got, WordWritingCode)
		}
	})

	t.Run("a stopped run's in-flight work is waiting, not writing code", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		recs := c.records()
		recs.Checkpoint.State = "running"
		src := statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			recs, nil, nil, false)
		src.Liveness = LivenessInput{Alive: false, State: "stopped", Reason: "the operator stopped the run", Source: "workflow-record"}
		src.Feed = []runfeed.Event{
			runfeed.NewEvent(testNow.Add(-35*time.Minute), "run-pip", "", nil, reconcile.StageRunFinished, "stopped: the operator stopped the run"),
		}
		model := Build(src)
		if got := statusTick(t, model, "aaa").Status; got != WordWaitingPrefix+"the run is stopped" {
			t.Errorf("a stopped run's dispatched tick reads %q, want %q", got, WordWaitingPrefix+"the run is stopped")
		}
	})

	t.Run("a standing attempt is still working even when the probe lost the run", func(t *testing.T) {
		t.Parallel()
		idle := runprogress.Duration(time.Minute)
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		standing := []runprogress.Attempt{{
			TickID: "aaa", Attempt: 3,
			Branch:     "refs/heads/ticfac/run-pip/tick-aaa/attempt-3",
			BranchIdle: &idle, WorktreeIdle: &idle,
		}}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), nil, standing, false))
		if got := statusTick(t, model, "aaa").Status; got != WordWritingCode {
			t.Errorf("a standing attempt reads %q, want %q: the worker is the truth", got, WordWritingCode)
		}
	})

	t.Run("a failed run's waiting names the failure", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		recs := c.records()
		recs.Checkpoint.State = "failed"
		recs.Checkpoint.Reason = "the integrated gate refused attempt 2 of 6dh"
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			recs, nil, nil, false))
		if got := statusTick(t, model, "aaa").Status; got != WordWaitingPrefix+"the run failed" {
			t.Errorf("a failed run's dispatched tick reads %q, want %q", got, WordWaitingPrefix+"the run failed")
		}
	})

	t.Run("a dead run's waiting says so plainly", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		src := statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), nil, nil, false)
		src.Liveness.State = "dead"
		src.Liveness.Reason = "pid 4242 is gone and never released the run"
		model := Build(src)
		if got := statusTick(t, model, "aaa").Status; got != WordWaitingPrefix+"the run is not going" {
			t.Errorf("a dead run's dispatched tick reads %q, want %q", got, WordWaitingPrefix+"the run is not going")
		}
	})
}

// TestTheStatusExceptions: the inline note beside the word, each component
// only when it applies — and never on a tick that has finished.
func TestTheStatusExceptions(t *testing.T) {
	t.Parallel()

	inFlight := func(tier1, model1, tier2, model2 string) statusCase {
		return statusCase{
			rows: []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 2}},
			markers: []runstate.Attempt{
				attemptMarker(1, "aaa", "2026-09-27T04:00:00Z", tier1, model1, "local-subprocess"),
				attemptMarker(2, "aaa", "2026-09-27T05:00:00Z", tier2, model2, "local-subprocess"),
			},
		}
	}

	t.Run("a retry names its attempt", func(t *testing.T) {
		t.Parallel()
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			inFlight("strong", "claude-opus-5", "strong", "claude-opus-5").records(), nil, nil, true))
		tick := statusTick(t, model, "aaa")
		if tick.Exception == nil || *tick.Exception != "attempt 2" {
			t.Errorf("a second try's exception is %v, want \"attempt 2\"", tick.Exception)
		}
	})

	t.Run("an escalated tier says so", func(t *testing.T) {
		t.Parallel()
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			inFlight("strong", "claude-opus-5", "frontier", "claude-opus-5").records(), nil, nil, true))
		tick := statusTick(t, model, "aaa")
		if tick.Exception == nil || *tick.Exception != "attempt 2, model escalated" {
			t.Errorf("an escalated retry's exception is %v, want \"attempt 2, model escalated\"", tick.Exception)
		}
	})

	t.Run("a changed model says so even at the same tier", func(t *testing.T) {
		t.Parallel()
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			inFlight("strong", "@cf/zai-org/glm-5.3", "strong", "claude-opus-5").records(), nil, nil, true))
		tick := statusTick(t, model, "aaa")
		if tick.Exception == nil || *tick.Exception != "attempt 2, model escalated" {
			t.Errorf("a model change's exception is %v, want \"attempt 2, model escalated\"", tick.Exception)
		}
	})

	t.Run("a first try carries no note", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 1}},
			markers: []runstate.Attempt{attemptMarker(1, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), nil, nil, true))
		if got := statusTick(t, model, "aaa").Exception; got != nil {
			t.Errorf("a first try's exception is %v, want nil", got)
		}
	})

	// The compact form, read off the notes the builder itself writes — the
	// spelling a glance surface owes its width (tick az1), pinned here so the
	// two spellings of one note cannot drift apart.
	t.Run("the compact form shortens the escalated retry", func(t *testing.T) {
		t.Parallel()
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			inFlight("strong", "claude-opus-5", "frontier", "claude-opus-5").records(), nil, nil, true))
		tick := statusTick(t, model, "aaa")
		if tick.Exception == nil {
			t.Fatalf("the escalated retry carries no note")
		}
		if got, want := CompactException(*tick.Exception), "attempt 2 · escalated"; got != want {
			t.Errorf("the compact form of %q is %q, want %q", *tick.Exception, got, want)
		}
	})

	t.Run("a single component keeps its own words", func(t *testing.T) {
		t.Parallel()
		if got, want := CompactException("attempt 2"), "attempt 2"; got != want {
			t.Errorf("a single component's compact form is %q, want %q", got, want)
		}
		if got, want := CompactException("stalled 20m"), "stalled 20m"; got != want {
			t.Errorf("a stall's compact form is %q, want %q", got, want)
		}
		if got := CompactException(""); got != "" {
			t.Errorf("no note compacts to %q, want the empty string", got)
		}
	})

	t.Run("every component's compact form is never wider than its own words", func(t *testing.T) {
		t.Parallel()
		// The whole vocabulary the note is built from, alone and joined —
		// the compact form is a rendering of the same facts, so it can never
		// cost a surface more cells than the note itself.
		notes := []string{
			"attempt 3",
			ExceptionEscalate,
			"stalled 45m",
			"attempt 3, " + ExceptionEscalate,
			"attempt 3, " + ExceptionEscalate + ", stalled 45m",
		}
		for _, note := range notes {
			if got := CompactException(note); ansi.StringWidth(got) > ansi.StringWidth(note) {
				t.Errorf("the compact form %q is wider than its own note %q", got, note)
			}
		}
	})

	t.Run("a finished tick stays calm", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows: []runstate.TickState{{TickID: "aaa", State: "closed", Attempt: 3}},
			markers: []runstate.Attempt{
				attemptMarker(1, "aaa", "2026-09-27T04:00:00Z", "strong", "@cf/zai-org/glm-5.3", "local-subprocess"),
				attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "frontier", "claude-opus-5", "local-subprocess"),
			},
		}
		feed := []runfeed.Event{
			line(testNow.Add(-20*time.Minute), "aaa", 3, reconcile.StageClosed, "closed behind the integrated gate"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), feed, nil, true))
		tick := statusTick(t, model, "aaa")
		if tick.Status != WordMerged {
			t.Errorf("the finished tick reads %q, want %q", tick.Status, WordMerged)
		}
		if tick.Exception != nil {
			t.Errorf("a finished tick carries exception %v, want nil: its history is its try list", tick.Exception)
		}
	})

	t.Run("a stalled worker says how long", func(t *testing.T) {
		t.Parallel()
		idle := runprogress.Duration(20 * time.Minute)
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 2}},
			markers: []runstate.Attempt{attemptMarker(2, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		standing := []runprogress.Attempt{{
			TickID: "aaa", Attempt: 2,
			Branch:       "refs/heads/ticfac/run-pip/tick-aaa/attempt-2",
			WorktreeIdle: &idle,
		}}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), nil, standing, true))
		tick := statusTick(t, model, "aaa")
		if tick.Exception == nil || *tick.Exception != "stalled 20m" {
			t.Errorf("a stalled worker's exception is %v, want \"stalled 20m\"", tick.Exception)
		}
	})

	t.Run("a quiet worker under the threshold says nothing", func(t *testing.T) {
		t.Parallel()
		idle := runprogress.Duration(5 * time.Minute)
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 2}},
			markers: []runstate.Attempt{attemptMarker(2, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		standing := []runprogress.Attempt{{
			TickID: "aaa", Attempt: 2,
			Branch:     "refs/heads/ticfac/run-pip/tick-aaa/attempt-2",
			BranchIdle: &idle, WorktreeIdle: &idle,
		}}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), nil, standing, true))
		if got := statusTick(t, model, "aaa").Exception; got != nil {
			t.Errorf("a worker thinking hard carries exception %v, want nil", got)
		}
	})

	t.Run("a gate that is running is the run's business, not the worker's", func(t *testing.T) {
		t.Parallel()
		idle := runprogress.Duration(20 * time.Minute)
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "reported", Attempt: 5}},
			markers: []runstate.Attempt{attemptMarker(5, "aaa", "2026-09-27T05:00:00Z", "frontier", "m", "herdr")},
		}
		standing := []runprogress.Attempt{{
			TickID: "aaa", Attempt: 5,
			Branch:       "refs/heads/ticfac/run-pip/tick-aaa/attempt-5",
			WorktreeIdle: &idle,
		}}
		feed := []runfeed.Event{
			line(testNow.Add(-27*time.Minute), "aaa", 5, reconcile.StageGateStarted, "the integrated gate started on aaa"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), feed, standing, true))
		if got := statusTick(t, model, "aaa").Exception; got != nil {
			t.Errorf("a running gate's exception is %v, want nil", got)
		}
	})
}

// TestAHeldTickPointsAtTheSameNeedsYouCommand (tick lck, the same rule the
// try next steps hold — tick eli): the dashboard's held rows and the
// header's needs-you box must answer ONE hold with ONE clearing command —
// the row's status word comes from the same hold line the attention entry
// words, so a person reading "held: ..." on the row and the command in the
// box are reading the same fact twice, not two derivations that can drift.
func TestAHeldTickPointsAtTheSameNeedsYouCommand(t *testing.T) {
	t.Parallel()
	c := statusCase{
		rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 3}},
		markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
	}
	four := 4
	feed := []runfeed.Event{
		runfeed.NewEvent(testNow.Add(-10*time.Minute), "run-pip", "aaa", &four, reconcile.StageRunHeld,
			"attempt 3 of aaa struck out: the refusal the run recorded"),
	}
	model := Build(statusSources(
		[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
		c.records(), feed, nil, true))

	tick := statusTick(t, model, "aaa")
	want := WordHeldPrefix + "attempt 3 of aaa struck out: the refusal the run recorded"
	if tick.Status != want {
		t.Fatalf("the held tick reads %q, want %q", tick.Status, want)
	}
	var attention *Attention
	for i := range model.Attention {
		if model.Attention[i].Kind == WaitHeldForPerson {
			attention = &model.Attention[i]
		}
	}
	if attention == nil {
		t.Fatalf("the hold is not attention: %+v", model.Attention)
	}
	if attention.What != "attempt 3 of aaa struck out: the refusal the run recorded" {
		t.Errorf("the attention entry reads %q, want the same hold line the row words", attention.What)
	}
	if attention.UnblockCommand == nil ||
		*attention.UnblockCommand != `ticfac settle pip aaa 4 --run-id run-pip --release "<who>"` {
		t.Errorf("the clearing command is %+v, want the settle command addressed to this run's store", attention.UnblockCommand)
	}
}

// TestTheStatusGroups: the four buckets, each tick in exactly one, in the
// waves' own order — and null when the tracker answered nothing.
func TestTheStatusGroups(t *testing.T) {
	t.Parallel()

	t.Run("every kind lands in its bucket", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows: []runstate.TickState{
				{TickID: "done", State: "closed", Attempt: 1},
				{TickID: "live", State: "dispatched", Attempt: 2},
				{TickID: "refused", State: "rejected", Attempt: 3},
				{TickID: "held", State: "dispatched", Attempt: 4},
			},
			markers: []runstate.Attempt{
				attemptMarker(1, "done", "2026-09-27T03:00:00Z", "strong", "m", "local-subprocess"),
				attemptMarker(2, "live", "2026-09-27T04:00:00Z", "strong", "m", "local-subprocess"),
				attemptMarker(3, "refused", "2026-09-27T04:30:00Z", "strong", "m", "local-subprocess"),
				attemptMarker(4, "held", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess"),
			},
		}
		four := 4
		feed := []runfeed.Event{
			line(testNow.Add(-30*time.Minute), "done", 1, reconcile.StageClosed, "closed behind the integrated gate"),
			line(testNow.Add(-10*time.Minute), "refused", 3, reconcile.StageRejected, "gate_failed: the integrated gate refused attempt 3"),
			runfeed.NewEvent(testNow.Add(-5*time.Minute), "run-pip", "held", &four, reconcile.StageRunHeld, "attempt 4 struck out: the refusal the run recorded"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{
				{ID: "done", Title: "D", Status: "open"},
				{ID: "live", Title: "L", Status: "open"},
				{ID: "refused", Title: "R", Status: "open"},
				{ID: "held", Title: "H", Status: "open"},
				{ID: "next", Title: "N", Status: "open", BlockedBy: []string{"live"}},
			}}},
			c.records(), feed, nil, true))
		groups := model.Groups
		if groups == nil {
			t.Fatal("the tracker answered and the model carries no groups")
		}
		// Wave order within each bucket; "next" is blocked behind the live
		// tick, so it waits.
		want := &TickGroups{
			Now:    []string{"live", "refused"},
			Done:   []string{"done"},
			UpNext: []string{"next"},
			Held:   []string{"held"},
		}
		if !reflect.DeepEqual(groups, want) {
			t.Errorf("the groups are %+v, want %+v", *groups, *want)
		}
	})

	t.Run("a refused tick is held when nothing will retry it", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "rejected", Attempt: 3}},
			markers: []runstate.Attempt{attemptMarker(3, "aaa", "2026-09-27T04:30:00Z", "strong", "m", "local-subprocess")},
		}
		recs := c.records()
		recs.Checkpoint.State = "failed"
		recs.Checkpoint.Reason = "the integrated gate refused attempt 3"
		feed := []runfeed.Event{
			line(testNow.Add(-10*time.Minute), "aaa", 3, reconcile.StageRejected, "gate_failed: the integrated gate refused attempt 3"),
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			recs, feed, nil, false))
		if got := model.Groups.Held; len(got) != 1 || got[0] != "aaa" {
			t.Errorf("the stopped run's refused tick is %v, want HELD: only a person can move it", model.Groups.Held)
		}
		if got := model.Groups.Now; len(got) != 0 {
			t.Errorf("the stopped run's NOW group is %v, want empty", model.Groups.Now)
		}
	})

	t.Run("no waves to group is a null claim", func(t *testing.T) {
		t.Parallel()
		model := Build(statusSources(nil, nil, nil, nil, true))
		if model.Groups != nil {
			t.Errorf("a model with no waves carries groups %+v, want null", model.Groups)
		}
	})
}

// TestThePhaseTrack: the lifecycle folded into the track's five steps, the
// marker on the newest step the epic is in.
func TestThePhaseTrack(t *testing.T) {
	t.Parallel()
	phases := func(states ...string) []PhaseState {
		out := make([]PhaseState, 0, len(states))
		for i, s := range states {
			out = append(out, PhaseState{Phase: Phases[i], State: s})
		}
		return out
	}
	cases := []struct {
		name   string
		phases []PhaseState
		here   int
		want   []string // "label:state" pairs
	}{
		{
			name:   "a fresh epic is here at building",
			phases: phases("pending", "pending", "pending", "pending", "pending", "pending"),
			here:   0,
			want:   []string{"building:pending", "reviewing:pending", "closing out:pending", "PR & CI:pending", "merged:pending"},
		},
		{
			name:   "the waves are building",
			phases: phases("done", "active", "pending", "pending", "pending", "pending"),
			here:   0,
			want:   []string{"building:active", "reviewing:pending", "closing out:pending", "PR & CI:pending", "merged:pending"},
		},
		{
			name:   "the review is its own step",
			phases: phases("done", "done", "active", "pending", "pending", "pending"),
			here:   1,
			want:   []string{"building:done", "reviewing:active", "closing out:pending", "PR & CI:pending", "merged:pending"},
		},
		{
			name:   "the close-out too",
			phases: phases("done", "done", "done", "active", "pending", "pending"),
			here:   2,
			want:   []string{"building:done", "reviewing:done", "closing out:active", "PR & CI:pending", "merged:pending"},
		},
		{
			name:   "CI held is the PR & CI step",
			phases: phases("done", "done", "done", "active", "active", "pending"),
			here:   3,
			want:   []string{"building:done", "reviewing:done", "closing out:active", "PR & CI:active", "merged:pending"},
		},
		{
			name:   "a green CI with the PR open is here at merged",
			phases: phases("done", "done", "done", "done", "done", "active"),
			here:   4,
			want:   []string{"building:done", "reviewing:done", "closing out:done", "PR & CI:done", "merged:active"},
		},
		{
			name:   "a merged epic is here at merged",
			phases: phases("done", "done", "done", "done", "done", "done"),
			here:   4,
			want:   []string{"building:done", "reviewing:done", "closing out:done", "PR & CI:done", "merged:done"},
		},
		{
			name:   "a failed run's marker sits where the epic stands",
			phases: phases("done", "active", "pending", "pending", "pending", "pending"),
			here:   0,
			want:   []string{"building:active", "reviewing:pending", "closing out:pending", "PR & CI:pending", "merged:pending"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			track, here := phaseTrackOf(tc.phases)
			if len(track) != len(tc.want) {
				t.Fatalf("the track has %d steps, want %d", len(track), len(tc.want))
			}
			for i, pair := range tc.want {
				label, state, _ := strings.Cut(pair, ":")
				if track[i].Label != label || track[i].State != state {
					t.Errorf("track[%d] is %q/%q, want %q", i, track[i].Label, track[i].State, pair)
				}
			}
			if here != tc.here {
				t.Errorf("the marker is at %d, want %d", here, tc.here)
			}
		})
	}

	t.Run("the track is in the built model", func(t *testing.T) {
		t.Parallel()
		c := statusCase{
			rows:    []runstate.TickState{{TickID: "aaa", State: "dispatched", Attempt: 1}},
			markers: []runstate.Attempt{attemptMarker(1, "aaa", "2026-09-27T05:00:00Z", "strong", "m", "local-subprocess")},
		}
		model := Build(statusSources(
			[]tk.GraphWave{{Wave: 1, Tasks: []tk.GraphTask{{ID: "aaa", Title: "A", Status: "open"}}}},
			c.records(), nil, nil, true))
		if len(model.Lifecycle.Track) != len(TrackLabels) || model.Lifecycle.Here != 0 {
			t.Errorf("the built track is %+v at %d, want five steps at building", model.Lifecycle.Track, model.Lifecycle.Here)
		}
		if model.Lifecycle.Track[0].Label != TrackLabels[0] || model.Lifecycle.Track[0].State != PhaseStateActive {
			t.Errorf("the built track's first step is %+v, want building active", model.Lifecycle.Track[0])
		}
	})
}
