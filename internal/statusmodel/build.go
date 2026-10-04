package statusmodel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The checkpoint's own tick-state vocabulary, spelled once so the model and
// the record it reads cannot drift apart over a string.
const (
	tickReady      = "ready"
	tickDispatched = "dispatched"
	tickReported   = "reported"
	tickClosed     = "closed"
	tickRejected   = "rejected"
)

// Sources is everything the model reads, injected by the caller so a test
// fakes any of them and so the model itself stays a pure assembly of what
// exists — it reads nothing, it is read to. Every source is best-effort at
// the CALLER's choice: a source that cannot be read is passed as its empty
// value and named in Degraded, because the model's job is to answer with
// what exists, never to refuse because one thing did not.
type Sources struct {
	// Now is the moment the model is generated. Every age and elapsed the
	// model states is measured against it, so one answer carries one clock.
	Now time.Time

	RunID string
	// Host is HostLocal (the default) or HostCloud.
	Host string
	// EpicID names the epic when the caller already knows it (the checkpoint
	// carries it; a cloud run's record carries it). Derived from the run id
	// when neither does.
	EpicID   string
	Degraded []string

	// Graph is the epic's own graph — the same layering `tk graph` computes,
	// with the tracker's closed tasks included where the read can serve
	// them. Nil when the tracker could not be read.
	Graph *tk.Graph
	// Records is the NEWEST run's durable state, as gathered. Nil when the
	// run's records could not be read at all.
	Records *Records
	// PriorRecords is every EARLIER run's records for the same epic, oldest
	// first — the runs whose work the tracker already carries. The dashboard
	// answers for the epic, not for the newest run's own checkpoint: a fresh
	// run seeds its plan "ready" before it settles the tracker's answer, so
	// a run that failed at boot must not erase the ticks earlier runs
	// closed. Each tick's row is read from the last run that touched it (its
	// dispatch markers, gate evidence and provenance); the run SECTION —
	// alive, workers, cost, feed, waits — is still the newest run's alone,
	// with the one exception the waits themselves make: a hold an earlier
	// run left for a person (PriorFeeds).
	PriorRecords []Records
	// PriorFeeds is every EARLIER run's own event feed, keyed by the run id
	// the records are keyed by (the checkpoint's run_id) — the feeds of the
	// runs PriorRecords carries, walked in the same oldest-first order. A
	// hold is a fact the feed is the only writer of: the records say the
	// tick was rejected, but only the line says the run held it FOR A
	// PERSON, and a hold an earlier run left stands until somebody answers
	// it no matter what newer runs did elsewhere (tick z3p). A run whose
	// feed could not be read is absent from the map — the model answers
	// with what exists, and a missing feed is a missing hint, not a
	// degraded source.
	PriorFeeds map[string][]runfeed.Event
	// Feed is the run's own event feed, whole. The feed is exhaust and
	// hints: nothing in the model takes a VERDICT from a line, but the
	// waits, the health counts and the wall clock firings are facts the
	// lines are the only writer of.
	Feed []runfeed.Event

	// Standing is the live attempts census. StandingRead says the census
	// was taken (and Standing may legitimately be empty): a cloud run's
	// workers are not on this machine, and "could not be counted" is a
	// different claim from "none stand".
	Standing     []runprogress.Attempt
	StandingRead bool

	// Liveness is the probe's own answer, carried as data.
	Liveness LivenessInput

	// Session answers one worker's runner session log: when it last grew
	// and what its last turn said. Nil when there is no reader (a run whose
	// runners keep no session log this machine can read).
	Session func(worktree string) *Turn

	// Activity answers one worker's measured activity window: the events
	// of its transcript and its last action. Nil when there is no reader —
	// every caller is nil-safe, and the model leaves activity null.
	Activity func(executor, worktree string) *ActivityInput

	// Handle answers one (tick, attempt) worker's executor-own name from
	// where this machine keeps it: the attempt record in the dispatch's
	// state directory — herdr's agent name, the pane it runs in, a local
	// supervisor's pid (zl1). The durable attempt marker cannot carry the
	// name: it is cut before the start, and the executor's own handle is
	// host paths a public repository must never commit. Nil when there is
	// no reader — a cloud run's workers are not on this machine — and the
	// model falls back to whatever the attempt record's own job handle
	// spells, null when nothing names the worker.
	Handle func(tickID string, attempt int) *string

	// Report answers one (tick, attempt) report: its summary and its diff
	// stats. Nil when there is no reader — the model leaves report null,
	// which is the honest "the report was not read".
	Report func(tickID string, attempt int) *ReportInput

	// WorkerCost is what the run's host states about what the workers spent —
	// the factory's own ground-truth number and the river it came from. Nil
	// when no host stated one, and the model's cost lines answer empty.
	WorkerCost *WorkerCostInput

	// CI is the forge's answer for the epic PR, per check per head. Nil
	// when there is no PR or the forge could not be asked.
	CI *CIInput
}

// LivenessInput is the probe's answer, carried as data so the model takes no
// position on how liveness was probed.
type LivenessInput struct {
	Alive  bool
	State  string
	Reason string
	// Source is the probe's own name for where the answer came from.
	Source string
}

// CIInput is the forge's answer for the epic PR, carried as data.
type CIInput struct {
	State  string
	PR     *PR
	Checks []CheckState
}

// Turn is one runner's session-log half-answer: the moment the log last grew
// and a one-line summary of the last turn in it.
type Turn struct {
	At      time.Time
	Summary string
}

// Build assembles one run's whole model from its sources. It decides
// nothing about the work: every state is read off a record, every count off
// a line, every gap off a measurement — and where nothing states a fact,
// the model says null rather than guess.
func Build(src Sources) Model {
	if src.Now.IsZero() {
		src.Now = time.Now()
	}
	host := src.Host
	if host == "" {
		host = HostLocal
	}

	recs := Records{}
	if src.Records != nil {
		recs = *src.Records
	}

	m := Model{
		SchemaVersion: SchemaVersion,
		RunID:         src.RunID,
		Host:          host,
		GeneratedAt:   src.Now.UTC().Format(time.RFC3339),
		Degraded:      src.Degraded,
		WaitsOn:       nil,
		Attention:     []Attention{},
		Gates:         []Gate{},
	}
	if m.Degraded == nil {
		m.Degraded = []string{}
	}
	if m.RunID == "" && recs.Checkpoint != nil {
		m.RunID = recs.Checkpoint.RunID
	}
	m.EpicID = src.EpicID
	if m.EpicID == "" && recs.Checkpoint != nil {
		m.EpicID = recs.Checkpoint.EpicID
	}
	if m.EpicID == "" {
		if rest, ok := strings.CutPrefix(m.RunID, "epic-"); ok && rest != "" {
			m.EpicID = rest
		}
	}

	m.Liveness = buildLiveness(src)
	// EpicTitle and Recent are direct carries: the graph's own title and the
	// feed's own last five lines, oldest first — the one dashboards datum
	// this tick computes for real (hn6 wave 1).
	if src.Graph != nil {
		title := src.Graph.Epic.Title
		m.EpicTitle = &title
	}
	m.Recent = append([]runfeed.Event{}, src.Feed[max(0, len(src.Feed)-5):]...)
	merged := newMergedRuns(recs, src.PriorRecords)
	absorbed := merged.absorbed
	m.Waves, m.Progress = buildWaves(src, merged, absorbed)
	// The epic's own clock, beside the counts it shares progress with: the
	// span is read off the dispatch markers, not the waves, so an unread
	// tracker costs the model the counts but not the clock (tick e6g).
	m.Progress.RunElapsedSeconds = buildRunElapsed(src, merged, recs)
	// The prior runs' standing holds, answered once by the rules buildWaits
	// applies — and the same answer handed to the pipeline index, so a row's
	// next step and the header's command are wordings of one answer, not two
	// derivations that can drift apart (tick eli).
	priorHolds := standingPriorHolds(src, func(id string) string { return mergedStateOf(m, id) })
	decorateTicks(src, merged, priorHolds, &m)
	m.Workers = buildWorkers(src, recs)
	decorateWorkers(src, recs, &m)
	decorateReports(src, &m)
	m.Health = buildHealth(src.Feed)
	m.Gates = buildGates(recs.Evidence)
	m.CI = buildCI(src.CI)
	m.Cost = buildCost(src, recs)
	m.Lifecycle = buildLifecycle(src, recs, m)
	m.WaitsOn, m.Attention = buildWaits(src, recs, m, priorHolds)
	m.Health.Verdict = buildVerdict(src, m)
	m.Remaining = buildRemaining(src, m)
	return m
}

// buildLiveness carries the probe's answer and adds the feed's own last
// word beside it — the same two facts `ticfac status` has always answered
// with, on one object.
func buildLiveness(src Sources) Liveness {
	l := Liveness{
		Alive:  src.Liveness.Alive,
		State:  src.Liveness.State,
		Reason: src.Liveness.Reason,
		Source: src.Liveness.Source,
	}
	if len(src.Feed) > 0 {
		last := src.Feed[len(src.Feed)-1]
		l.LastEvent = &last
		if at, err := time.Parse(time.RFC3339, last.At); err == nil {
			age := int64(src.Now.Sub(at).Round(time.Second).Seconds())
			l.LastEventAgeSeconds = &age
		}
	}
	return l
}

// buildRunElapsed measures the epic's whole span from the records the model
// merges: the earliest dispatch any tick's try history states, to the
// model's own now — frozen at the run's end when the run's own records say
// it ended. The header's clock was once a renderer derivation (the exact
// defect tick e6g absorbed): measured here, one field carries it to every
// surface, and the freeze lands with the measurement instead of never.
func buildRunElapsed(src Sources, merged *mergedRuns, recs Records) *int64 {
	start := time.Time{}
	for _, markers := range merged.markers {
		for _, marker := range markers {
			at, err := time.Parse(time.RFC3339, marker.DispatchedAt)
			if err != nil {
				continue
			}
			if start.IsZero() || at.Before(start) {
				start = at
			}
		}
	}
	if start.IsZero() {
		return nil
	}
	end := src.Now
	if stopped, ok := runEndedAt(src, recs); ok && stopped.Before(end) {
		end = stopped
	}
	if end.Before(start) {
		return nil
	}
	elapsed := int64(end.Sub(start).Round(time.Second).Seconds())
	return &elapsed
}

// runEndedAt answers when the run's own records say it ended. A run's end is
// its own word, in the order of the authorities: the terminal checkpoint it
// wrote (the moment of the state change is its own updated_at), else its own
// terminal line — run_finished or run_died — when no resume stands after it
// in the feed, because the feed is append-only per run id and a resumed run
// still carries its previous incarnation's terminal line as history. No
// end is answered for a run whose records state none: a run that died
// without a word has an end nobody measured, and an elapsed nobody measured
// is an elapsed nobody prints.
func runEndedAt(src Sources, recs Records) (time.Time, bool) {
	if recs.Checkpoint != nil && recs.Checkpoint.State.Terminal() {
		if at, err := time.Parse(time.RFC3339, recs.Checkpoint.UpdatedAt); err == nil {
			return at, true
		}
	}
	var terminal *runfeed.Event
	for i := range src.Feed {
		switch src.Feed[i].Stage {
		case reconcile.StageRunFinished, reconcile.StageRunDied:
			terminal = &src.Feed[i]
		case reconcile.StageResumed, reconcile.StageResumedAutomatically:
			// A resume standing after a terminal line makes that line the
			// previous incarnation's history — position in the feed is the
			// clock, never the line's own stamp (the file is append-only).
			terminal = nil
		}
	}
	if terminal == nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, terminal.At)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// buildWaves lays the epic out as the tracker itself layers it, with every
// tick's state from the durable records of every run that worked the epic:
// the newest run's own where it has one, the last run that touched the tick
// where it does not, the tracker's closed status behind them both. The wave
// states are derived, not stored: a wave is done when every tick in it is
// closed, and the first wave that is not done is the frontier the run works
// on. Duplicates — ticks closed as the duplicate of another — get rows like
// any tick, but no place in the progress counts: they are not work the epic
// still owes.
func buildWaves(src Sources, merged *mergedRuns, absorbed map[string]bool) (*[]Wave, Progress) {
	progress := Progress{}
	if src.Graph == nil || len(src.Graph.Waves) == 0 {
		return nil, progress
	}

	// The gate evidence grouped per (tick, attempt): what each try produced.
	// The owner run's evidence only — attempt numbers are per run.
	evidenceByTry := map[string][]runstate.Evidence{}
	for _, evidence := range merged.evidence {
		for _, e := range evidence {
			if e.Provenance.TickID == nil || e.Provenance.Attempt == nil {
				continue
			}
			key := fmt.Sprintf("%s#%d", *e.Provenance.TickID, *e.Provenance.Attempt)
			evidenceByTry[key] = append(evidenceByTry[key], e)
		}
	}
	// Which (tick, attempt) pairs still stand: the census's own answer. The
	// census is the newest run's; another run's attempts never stand here.
	standing := map[string]bool{}
	for _, a := range src.Standing {
		standing[fmt.Sprintf("%s#%d", a.TickID, a.Attempt)] = true
	}

	waves := []Wave{}
	tickProgress := TickProgress{}
	waveProgress := WaveProgress{Total: len(src.Graph.Waves)}
	for _, w := range src.Graph.Waves {
		wave := Wave{Wave: w.Wave, Ticks: []Tick{}}
		waveDone := true
		for _, task := range w.Tasks {
			state, attempt, owner := merged.stateOf(task)
			t := buildTick(src, merged, task, state, attempt, owner, absorbed[task.ID],
				evidenceByTry, standing)
			t.DuplicateOf = duplicateOf(task)
			if t.State != tickClosed {
				// A duplicate that is not closed is a dedup the epic has not
				// done yet: it is still work, and it holds the wave open.
				waveDone = false
			}
			if t.DuplicateOf == nil {
				tickProgress.Total++
				if t.State == tickClosed {
					tickProgress.Closed++
				}
			}
			wave.Ticks = append(wave.Ticks, t)
		}
		switch {
		case waveDone:
			wave.State = WaveDone
			waveProgress.Done++
		case waveProgress.Active == 0:
			wave.State = WaveActive
			waveProgress.Active = w.Wave
		default:
			wave.State = WaveUpcoming
		}
		waves = append(waves, wave)
	}
	tickProgress.Open = tickProgress.Total - tickProgress.Closed
	progress.Ticks = &tickProgress
	progress.Waves = &waveProgress
	return &waves, progress
}

// buildTick assembles one tick's whole entry: state, try history, the
// current attempt's provenance and its elapsed time — read from the LAST
// run that has records for the tick, never across runs (attempt numbers are
// per run, so the same number in two runs names two dispatches).
func buildTick(src Sources, merged *mergedRuns, task tk.GraphTask, state string, attempt *int, owner int,
	absorbed bool, evidenceByTry map[string][]runstate.Evidence, standing map[string]bool) Tick {

	t := Tick{
		TickID:   task.ID,
		Title:    task.Title,
		Gloss:    task.Gloss,
		Role:     task.Role,
		State:    state,
		Absorbed: absorbed,
		Tries:    []Try{},
	}

	// The dispatches that happened, in order — the try history's spine.
	attempts := merged.markersOf(task.ID)
	numbers := make([]int, 0, len(attempts))
	byNumber := map[int]runstate.Attempt{}
	for _, a := range attempts {
		numbers = append(numbers, a.Attempt)
		byNumber[a.Attempt] = a
	}

	// The current attempt: the owning run's own row names it; a tick the
	// owning run no longer mentions keeps its highest recorded dispatch.
	current := 0
	if attempt != nil {
		current = *attempt
	} else if len(numbers) > 0 {
		current = numbers[len(numbers)-1]
	}

	for rank, n := range numbers {
		outcome := tryOutcome(state, current, n,
			evidenceByTry[fmt.Sprintf("%s#%d", task.ID, n)],
			standing[fmt.Sprintf("%s#%d", task.ID, n)])
		t.Tries = append(t.Tries, Try{
			Try:          rank + 1,
			Attempt:      n,
			Outcome:      outcome,
			DispatchedAt: byNumber[n].DispatchedAt,
		})
	}

	if current > 0 {
		try := rankOf(current, numbers)
		t.Try = &try
		t.Attempt = &current
		if a, ok := byNumber[current]; ok {
			t.Tier, t.Model, t.Executor = a.Provenance.Tier, a.Provenance.Model, a.Provenance.Executor
			// Elapsed measures a LIVE attempt only: the newest run's own
			// in-flight work, or an attempt the census says stands. An
			// earlier run's last dispatch — its run has ended, whatever its
			// row still says — is history, and a duration from its stamp to
			// now would be a countdown nobody asked for.
			if at, err := time.Parse(time.RFC3339, a.DispatchedAt); err == nil && isLiveAttempt(src, state, owner, standing[fmt.Sprintf("%s#%d", task.ID, current)]) {
				elapsed := int64(src.Now.Sub(at).Round(time.Second).Seconds())
				t.ElapsedSeconds = &elapsed
			}
		}
	}
	return t
}

// isLiveAttempt says whether the tick's current attempt is one the epic is
// still working: an attempt the newest run's census says stands, or one the
// NEWEST run dispatched or reported while it is not over. A state the
// newest run wrote is the newest run's own present tense; the same state in
// an earlier run's records is history — that run's attempt is not live
// here, whatever its own census would have said when it ran.
func isLiveAttempt(src Sources, state string, owner int, stands bool) bool {
	if stands {
		return true
	}
	if owner != len(src.PriorRecords) {
		return false
	}
	return state == tickDispatched || state == tickReported
}

// rankOf is a dispatch number's try rank among the numbers the records show
// for one tick: 1 for the lowest, 2 for the next — the h58 language. A
// number the records never wrote is one more than they show, the same rule
// the feed's own try counting holds.
func rankOf(n int, numbers []int) int {
	for rank, number := range numbers {
		if number == n {
			return rank + 1
		}
	}
	return len(numbers) + 1
}

// tryOutcome resolves one dispatch's outcome from the durable records
// alone, in the order of their authority: the checkpoint's own word about
// the tick, then the gate evidence that dispatch produced, then the census.
func tryOutcome(tickState string, current, n int, evidence []runstate.Evidence, stands bool) string {
	if n == current {
		switch tickState {
		case tickClosed:
			return TryClosed
		case tickRejected:
			return TryRejected
		}
	}
	any, allPass := false, true
	for _, e := range evidence {
		any = true
		if e.Result != "pass" {
			allPass = false
		}
	}
	if any {
		if allPass {
			return TryReported
		}
		return TryGateFailed
	}
	if stands {
		return TryInFlight
	}
	return TryDispatched
}

// buildWorkers is the live-worker half: one entry per standing attempt, with
// the gaps the census measured, the wall clock's typed firing, the runner's
// own session-log silence and its last turn.
func buildWorkers(src Sources, recs Records) *[]Worker {
	if !src.StandingRead {
		return nil
	}
	// The dispatch markers by (tick, attempt): the elapsed facts.
	dispatchedAt := map[string]time.Time{}
	for _, a := range recs.Attempts {
		if at, err := time.Parse(time.RFC3339, a.DispatchedAt); err == nil {
			dispatchedAt[fmt.Sprintf("%s#%d", a.TickID, a.Attempt)] = at
		}
	}
	// The run's own typed wall clock firings, latest per attempt.
	fired := map[string]runfeed.Event{}
	for _, e := range src.Feed {
		if e.Stage != reconcile.StageWallClock || e.TickID == nil || e.Attempt == nil {
			continue
		}
		fired[fmt.Sprintf("%s#%d", *e.TickID, *e.Attempt)] = e
	}

	workers := []Worker{}
	for _, a := range src.Standing {
		key := fmt.Sprintf("%s#%d", a.TickID, a.Attempt)
		w := Worker{
			TickID:   a.TickID,
			Attempt:  a.Attempt,
			Branch:   a.Branch,
			Worktree: a.Worktree,
		}
		if at, ok := dispatchedAt[key]; ok {
			elapsed := int64(src.Now.Sub(at).Round(time.Second).Seconds())
			w.ElapsedSeconds = &elapsed
		}
		if a.BranchIdle != nil {
			seconds := int64(time.Duration(*a.BranchIdle).Round(time.Second).Seconds())
			w.BranchIdleSeconds = &seconds
		}
		if a.WorktreeIdle != nil {
			seconds := int64(time.Duration(*a.WorktreeIdle).Round(time.Second).Seconds())
			w.WorktreeIdleSeconds = &seconds
		}
		if line, ok := fired[key]; ok {
			w.WallClock = &WallClock{FiredAt: line.At, Detail: line.Detail}
		}
		if src.Session != nil && a.Worktree != "" {
			if turn := src.Session(a.Worktree); turn != nil {
				silence := int64(src.Now.Sub(turn.At).Round(time.Second).Seconds())
				at := turn.At.UTC().Format(time.RFC3339)
				summary := turn.Summary
				w.SilenceSeconds = &silence
				w.LastTurnAt = &at
				w.LastTurn = &summary
			}
		}
		workers = append(workers, w)
	}
	return &workers
}

// buildGates carries the run's gate evidence per check per head, keyed by the
// SOURCE the check ran on — the rule the gate's own evidence learned the
// hard way (a run writes .ticfac/ to the branch it gates).
func buildGates(evidence []runstate.Evidence) []Gate {
	gates := []Gate{}
	for _, e := range evidence {
		g := Gate{
			Key:        e.Key,
			Check:      e.Check.ID,
			Result:     e.Result,
			Acceptance: e.Acceptance,
			Phase:      string(e.Provenance.Phase),
			TickID:     e.Provenance.TickID,
			Attempt:    e.Provenance.Attempt,
			StartedAt:  e.StartedAt,
			FinishedAt: e.FinishedAt,
		}
		if e.Provenance.SourceSHA != "" {
			sha := e.Provenance.SourceSHA
			g.Head = &sha
		}
		g.IntegrationRef = e.Provenance.IntegrationRef
		gates = append(gates, g)
	}
	sort.Slice(gates, func(i, j int) bool {
		if gates[i].Key != gates[j].Key {
			return gates[i].Key < gates[j].Key
		}
		return gates[i].StartedAt < gates[j].StartedAt
	})
	return gates
}

// buildCI carries the forge's answer for the epic PR.
func buildCI(input *CIInput) *CI {
	if input == nil {
		return nil
	}
	checks := input.Checks
	if checks == nil {
		checks = []CheckState{}
	}
	return &CI{State: input.State, PR: input.PR, Checks: checks}
}

// buildLifecycle derives where the epic stands, and each phase's state, from
// the graph and the durable records: the plan is behind the run when its
// first dispatch happened, the waves when their ticks closed, the review and
// the close-out by their own role-carrying ticks, CI by the close-out's own
// typed line and the forge's answer, and the merge — always a person's —
// active while the PR stands open behind a completed run.
func buildLifecycle(src Sources, recs Records, m Model) Lifecycle {
	planState := PhaseStatePending
	if recs.Checkpoint != nil || len(recs.Attempts) > 0 {
		planState = PhaseStateActive
		if len(recs.Attempts) > 0 || (recs.Checkpoint != nil && recs.Checkpoint.State != "admitted") {
			planState = PhaseStateDone
		}
	}

	// Waves: every role-less child closed is the waves phase done. Without
	// a graph the checkpoint's own rows answer — a dispatched row is a wave
	// in flight whatever the tracker would have layered around it.
	wavesState := PhaseStateDone
	for _, w := range deref(m.Waves) {
		for _, t := range w.Ticks {
			if t.Role != "" || t.State == tickClosed {
				continue
			}
			wavesState = PhaseStateActive
		}
	}
	if m.Waves == nil && recs.Checkpoint != nil {
		for _, row := range recs.Checkpoint.Ticks {
			if row.State != tickClosed {
				wavesState = PhaseStateActive
			}
		}
	}

	reviewState := roleState(m, "review")
	closeoutState := roleState(m, "closeout")

	// CI: the close-out's own typed held line says the run is waiting on it;
	// the forge's answer says where it stands now.
	ciState := PhaseStatePending
	if latestStage(src.Feed, "", reconcile.StageCloseoutHeld) != nil {
		ciState = PhaseStateActive
	}
	if m.CI != nil {
		switch m.CI.State {
		case "green":
			ciState = PhaseStateDone
		case "red", "pending":
			ciState = PhaseStateActive
		}
	}

	// Merge: a person's, always. Active while the PR stands open behind a
	// run that finished its own work; done when no open PR remains.
	mergeState := PhaseStatePending
	if runCompleted(src, recs) {
		mergeState = PhaseStateActive
		if m.CI == nil || m.CI.PR == nil {
			mergeState = PhaseStateDone
		}
	}

	phases := []PhaseState{
		{Phase: PhasePlan, State: planState},
		{Phase: PhaseWaves, State: wavesState},
		{Phase: PhaseReview, State: reviewState},
		{Phase: PhaseCloseout, State: closeoutState},
		{Phase: PhaseCI, State: ciState},
		{Phase: PhaseMerge, State: mergeState},
	}

	lifecycle := Lifecycle{Phases: phases}
	switch {
	case recs.Checkpoint != nil && recs.Checkpoint.State == "failed":
		lifecycle.Phase = PhaseFailed
	case recs.Checkpoint != nil && recs.Checkpoint.State == "cancelled":
		lifecycle.Phase = PhaseCancelled
	case runCompleted(src, recs):
		// The run's own work is finished. What is left is the person's: the
		// merge while the PR stands open, and nothing once it is gone.
		if mergeState == PhaseStateActive {
			lifecycle.Phase = PhaseMerge
		} else {
			lifecycle.Phase = PhaseDone
		}
	default:
		for _, p := range phases {
			if p.State != PhaseStateDone {
				lifecycle.Phase = p.Phase
				break
			}
		}
		if lifecycle.Phase == "" {
			lifecycle.Phase = PhaseDone
		}
	}
	if lifecycle.Phase == PhaseWaves && m.Progress.Waves != nil && m.Progress.Waves.Active > 0 {
		lifecycle.Wave = &WaveRef{Active: m.Progress.Waves.Active, Total: m.Progress.Waves.Total}
	}
	return lifecycle
}

// roleState is one role's phase state from the ticks that carry it: done
// when every one closed, active while one is dispatched or reported, pending
// while they stand untouched. A role no tick carries is a phase the epic
// does not have, and it is done — a phase that costs nothing is not one a
// renderer should show as waiting.
func roleState(m Model, role string) string {
	states := []string{}
	for _, w := range deref(m.Waves) {
		for _, t := range w.Ticks {
			if t.Role == role {
				states = append(states, t.State)
			}
		}
	}
	if len(states) == 0 {
		return PhaseStateDone
	}
	state := PhaseStatePending
	closed := true
	for _, s := range states {
		switch s {
		case tickClosed:
		case tickDispatched, tickReported:
			state = PhaseStateActive
			closed = false
		default:
			closed = false
		}
	}
	if closed {
		return PhaseStateDone
	}
	return state
}

// deref keeps the range loops readable over the nullable waves slice.
func deref(waves *[]Wave) []Wave {
	if waves == nil {
		return nil
	}
	return *waves
}

// latestStage is the newest line of one stage the feed carries, run-level or
// for one tick. The feed is a hint about when to look, and that is exactly
// how it is used here: the wait the model states is the RECORD's fact; the
// line only says when it began.
func latestStage(feed []runfeed.Event, tickID, stage string) *runfeed.Event {
	var latest *runfeed.Event
	for i := range feed {
		e := feed[i]
		if e.Stage != stage {
			continue
		}
		if tickID != "" && (e.TickID == nil || *e.TickID != tickID) {
			continue
		}
		latest = &feed[i]
	}
	return latest
}

// runCompleted says whether the run's own records say it finished: the
// checkpoint's terminal completion, or its own run_finished line. The
// feed's own LAST run_finished line is the run's word, and only its word
// (tick bkg): a failed run is resumable under the same run id, so a resumed
// run's feed still carries the failed incarnation's run_finished, and ANY
// line would read a run that stopped failed and just began again as
// finished — surfacing a person's merge wait for work that is still going.
// The reconciler writes the line's detail LED by the runstate word it
// checkpointed ("failed: the integrated gate refused ...", "completed:
// every tick closed ..."), the same authority `ticfac watch`'s own
// ended-failed question reads, so a line that names a failure is an ending
// that is not a completion.
func runCompleted(src Sources, recs Records) bool {
	if recs.Checkpoint != nil && recs.Checkpoint.State == "completed" {
		return true
	}
	last := latestStage(src.Feed, "", reconcile.StageRunFinished)
	if last == nil {
		return false
	}
	return !strings.HasPrefix(last.Detail, string(runstate.StateFailed)+":")
}

// buildWaits states what the run is blocked on, and everything a person must
// do. The one wait is chosen in the order of what stops the run hardest: a
// hold only a person releases, a dead run nobody advances, a merge that is
// a person's by design, the close-out's CI gate, and the ordinary wait on
// live workers. The attention list carries every person-needing fact beside
// it, findings triage included.
func buildWaits(src Sources, recs Records, m Model, priorHolds []PriorHold) (*Wait, []Attention) {
	attention := []Attention{}
	claim := func(w Wait) {
		if m.WaitsOn == nil {
			m.WaitsOn = &w
		}
		if w.NeedsPerson {
			attention = append(attention, Attention(w))
		}
	}
	// The run id the current run's own store lives at: the id its durable
	// records were written under, falling back to the id the surface names
	// when the records name none. This is the store the run's untriaged
	// findings settle in — the same derivation the prior-runs half reads
	// from each holding run's checkpoint (tick z3p) — and NOT always the id
	// the surface names: a cloud run is addressed by the factory's run_<hex>
	// (tick ulw) and writes its records under that same id, so the bare
	// triage command's default (the local spelling epic-<epic-id>) names a
	// store a cloud run never wrote (tick q8m). A run whose records were
	// read under an older layout's epic spelling is addressed by that
	// spelling — the spelling its store actually lives at.
	ownRunID := m.RunID
	if recs.Checkpoint != nil && recs.Checkpoint.RunID != "" {
		ownRunID = recs.Checkpoint.RunID
	}

	// A run whose process is gone without its own terminal record: nothing
	// advances it, and only a person can say whether it resumes. The
	// liveness answer itself may carry the run's own durable word that it
	// ENDED — a cloud run's record state, written by the Workflow it lives
	// in — and a run whose own record says it ended is not dead, whatever a
	// checkout that cannot read that record fails to hold. Without this, the
	// factory's finished runs — the ones this checkout holds no run state
	// for at all, because they belong to other projects — were every one of
	// them "dead" on the surface that aggregates every run (tick 2qz).
	if !src.Liveness.Alive && !runTerminal(recs) && !livenessNamesAnEnd(src.Liveness.State) {
		w := Wait{
			Kind:        WaitDeadRun,
			What:        fmt.Sprintf("run %s is %s: %s", m.RunID, src.Liveness.State, src.Liveness.Reason),
			NeedsPerson: true,
		}
		// The resume is named by the host the run lives on (tick gtk): a
		// cloud run's is a new submission to its factory — run --cloud —
		// because run-epic here would restart the epic LOCALLY, in the
		// foreground, on the machine that happens to be reading.
		unblock := ResumeCommand(m.Host, m.EpicID)
		w.UnblockCommand = &unblock
		claim(w)
	}

	// The run's own line that it is holding an attempt for a person — one the
	// CURRENT incarnation wrote. A hold a later resume made history is
	// history: the feed is append-only per RUN ID, so a resumed run still
	// carries the previous incarnation's run_held line, and a hold somebody
	// already settled by resuming the run must not read as standing — the
	// same rule the watch's subscription start made for lines (tick usx),
	// stated here once for every surface that renders the model.
	if holds := unansweredHolds(src.Feed); len(holds) > 0 {
		held := holds[len(holds)-1]
		w := Wait{
			Kind:        WaitHeldForPerson,
			What:        held.Detail,
			NeedsPerson: true,
		}
		since := held.At
		w.Since = &since
		// The command is named by WHAT the run is holding, one decision per
		// hold kind (tick gf0, the same decision the watch's hold alert and
		// the rows' next steps read): the close-out's untriaged-findings hold
		// and the absorption bound's are cleared by the triage addressed to
		// the run's own store (tick q8m) — settle releases an attempt, and
		// these holds hold a person's decision, not one; the holds about the
		// world — the width, a foreign claim — by the run again, addressed by
		// the host (tick gtk), because they fire before the tick's first
		// dispatch, end when the world does, and no release clears them; the
		// final-review hold by the same run again (tick quz), because although
		// it fires after the close-out's dispatch and its line carries that
		// attempt, releasing it clears nothing — the hold is the review's
		// NOT READY verdict on the PR, which a resume re-reads, and the
		// refusal's own moves (fix and run again, merge the PR by hand, close
		// it) end at a resume or never need the run again; every other hold is
		// the settle command the run-wide dispatch number addresses — the same
		// sentence `ticfac watch` prints.
		if command := HoldClearingCommand(m.EpicID, m.Host, ownRunID, m.RunID, held); command != nil {
			w.UnblockCommand = command
		}
		claim(w)
	}

	// Prior runs' holds (tick z3p), answered by standingPriorHolds — the
	// same function decorateTicks handed the pipeline index, so the header
	// and the rows cannot disagree about which hold stands and what clears
	// it (tick eli). This loop only words the waits the answer carries.
	for _, held := range priorHolds {
		w := Wait{
			Kind:        WaitHeldForPerson,
			NeedsPerson: true,
		}
		if held.TickID != "" {
			w.What = fmt.Sprintf("run %s held %s for a person: %s", held.RunID, held.TickID, held.Event.Detail)
		} else {
			w.What = fmt.Sprintf("run %s held for a person: %s", held.RunID, held.Event.Detail)
		}
		since := held.Event.At
		w.Since = &since
		if command := priorHoldCommand(m.EpicID, m.Host, held); command != nil {
			w.UnblockCommand = command
		}
		claim(w)
	}

	// A completed run's open PR is the person's to merge.
	if runCompleted(src, recs) && m.CI != nil && m.CI.PR != nil {
		w := Wait{
			Kind:        WaitMerge,
			What:        fmt.Sprintf("the epic PR is ready for a person to merge: %s", m.CI.PR.URL),
			NeedsPerson: true,
		}
		claim(w)
	}

	// The close-out's CI gate: a wait the run itself watches, not a person.
	if held := latestStage(src.Feed, "", reconcile.StageCloseoutHeld); held != nil {
		w := Wait{
			Kind:        WaitCI,
			What:        held.Detail,
			NeedsPerson: false,
		}
		since := held.At
		w.Since = &since
		claim(w)
	}

	// The ordinary wait: live workers doing their work.
	if src.StandingRead && m.Workers != nil && len(*m.Workers) > 0 {
		w := Wait{
			Kind:        WaitWorkers,
			What:        fmt.Sprintf("%d in-flight attempt(s)", len(*m.Workers)),
			NeedsPerson: false,
		}
		claim(w)
	}

	// Untriaged findings beside a run that cannot triage them itself: a
	// person's decision, attention even when something else blocks harder.
	// EVERY run's drafts, not only the newest run's (tick d23): a run that
	// died before its close-out raised no run_held line — the hold the
	// close-out's findings gate would have raised never happened — so its
	// untriaged drafts are invisible to a block that read only the newest
	// run's records, although a person's decision about them is standing.
	// Each prior run's attention names the run, and the triage command is
	// addressed to IT (TriageCommandForRun): the drafts live in that run's
	// own records, so they settle in its store, never the one the bare
	// command's default spells — and they are a question regardless of the
	// newest run's liveness, because a prior run cannot triage its own
	// drafts whatever the newest run is doing.
	//
	// A finding is one record across runs, keyed by content, so the copies
	// dedupe NEWEST-FIRST: a key a newer run's records carry — adopted into
	// the live run's own store, or decided, its decision standing — answers
	// for every older run's copy of it, and the older copy is history. The
	// newest run's own drafts stay the newest run's question, asked only
	// when it can no longer triage them itself: while it runs, they are its
	// own close-out's to decide or hold.
	seen := map[string]bool{}
	if !src.Liveness.Alive {
		untriaged, earliest := 0, ""
		for _, f := range recs.Findings {
			if f.Status != runstate.FindingProposed {
				continue
			}
			untriaged++
			if earliest == "" || (f.ProposedAt != "" && f.ProposedAt < earliest) {
				earliest = f.ProposedAt
			}
		}
		if untriaged > 0 {
			w := Wait{
				Kind:        WaitFinding,
				What:        fmt.Sprintf("%d untriaged finding(s) await triage", untriaged),
				NeedsPerson: true,
			}
			if earliest != "" {
				since := earliest
				w.Since = &since
			}
			// The command that settles the findings, not the one that only
			// lists them (tick gtk): `ticfac findings` walks away having
			// changed nothing, and a person following it finds the close-out
			// still held. Addressed to the run's own store (tick q8m): the
			// drafts live where the run's records were written.
			unblock := TriageCommandForCurrentRun(m.EpicID, ownRunID)
			w.UnblockCommand = &unblock
			attention = append(attention, Attention(w))
		}
	}
	for _, f := range recs.Findings {
		seen[f.Key] = true
	}
	for i := len(src.PriorRecords) - 1; i >= 0; i-- {
		prior := src.PriorRecords[i]
		// A run whose records name no run id cannot be triaged and answers
		// for no other run's copy: there is no command to spell for it, and
		// an unnamed word settles nothing.
		if prior.Checkpoint == nil || prior.Checkpoint.RunID == "" {
			continue
		}
		untriaged, earliest := 0, ""
		for _, f := range prior.Findings {
			if f.Status == runstate.FindingProposed && !seen[f.Key] {
				untriaged++
				if earliest == "" || (f.ProposedAt != "" && f.ProposedAt < earliest) {
					earliest = f.ProposedAt
				}
			}
		}
		if untriaged > 0 {
			w := Wait{
				Kind: WaitFinding,
				What: fmt.Sprintf("run %s has %d untriaged finding(s) awaiting triage",
					prior.Checkpoint.RunID, untriaged),
				NeedsPerson: true,
			}
			if earliest != "" {
				since := earliest
				w.Since = &since
			}
			// The triage addressed to the holding run's own store: the
			// drafts are that run's records, and the bare command's default
			// spells this run's — the same address rule the prior holds
			// keep (z3p).
			unblock := TriageCommandForRun(m.EpicID, prior.Checkpoint.RunID)
			w.UnblockCommand = &unblock
			attention = append(attention, Attention(w))
		}
		for _, f := range prior.Findings {
			seen[f.Key] = true
		}
	}
	return m.WaitsOn, attention
}

// PriorHold is one prior run's hold that still stands: the run that holds,
// the tick it holds (empty for a hold the run left on itself, which no row
// can carry), and the run_held line itself — the record the wait's words and
// its clearing command read.
type PriorHold struct {
	RunID  string
	TickID string
	Event  runfeed.Event
}

// standingPriorHolds answers which of the prior runs' holds still stand, by
// the same rules the waits have always applied them (tick z3p): a hold an
// earlier run left — an attempt struck out for a person, findings nobody
// triaged — stands until somebody answers it, and the newest run's own feed
// says nothing about whether anyone did. A newest run that failed at boot
// writes no run_held line of its own, so without this a person's decision
// that is actually standing answers "nothing needs you". The records say the
// tick was rejected; only the FEED says the run held it FOR A PERSON — so
// the answer reads each prior run's feed the same way it reads the newest
// run's, newest run first: the newest unanswered word about a tick's held
// state is the live one, and a hold behind it (an earlier run's, or an older
// line of the same run's) is history. Per tick, one hold: the last one the
// run left unanswered.
//
// The answer is computed ONCE and given to both of its readers — buildWaits,
// which words the header's needs-you entries from it, and the pipeline
// index, which lets a standing hold outrank the generic non-newest-owner
// resume line in a try's next step (tick eli) — so the two surfaces are two
// renderings of one derivation, never two derivations that can drift.
// stateOf is the merged tick state (mergedStateOf's answer): the epic having
// moved past a tick closes any hold on it.
func standingPriorHolds(src Sources, stateOf func(string) string) []PriorHold {
	holds := []PriorHold{}
	// Which ticks the newest run's own feed speaks of — it dispatched, held
	// or saw the attempt settled. Its word about a tick is the live one.
	held := map[string]bool{}
	for i := range src.Feed {
		line := &src.Feed[i]
		if line.TickID == nil {
			continue
		}
		switch line.Stage {
		case reconcile.StageDispatched, reconcile.StageRedispatched, reconcile.StageRepairDispatched,
			reconcile.StageRunHeld, reconcile.StageSettled:
			held[*line.TickID] = true
		}
	}
	for i := len(src.PriorRecords) - 1; i >= 0; i-- {
		prior := src.PriorRecords[i]
		if prior.Checkpoint == nil || prior.Checkpoint.RunID == "" {
			continue
		}
		feed := src.PriorFeeds[prior.Checkpoint.RunID]
		if len(feed) == 0 {
			continue
		}
		// Per tick, the last hold the run left unanswered: a resume standing
		// after a hold means the run continued past it — the hold it
		// answered is history (the same position rule the newest run's own
		// wait reads). An earlier unanswered line of the same tick is
		// superseded by the later one.
		byTick := map[string]runfeed.Event{}
		var order []string
		for _, hold := range unansweredHolds(feed) {
			id := ""
			if hold.TickID != nil {
				id = *hold.TickID
			}
			if _, seen := byTick[id]; !seen {
				order = append(order, id)
			}
			byTick[id] = hold
		}
		for _, id := range order {
			hold := byTick[id]
			if id != "" {
				if held[id] {
					continue
				}
				// The epic moved past the hold: the merged records close
				// the tick, so whatever decision the hold waited on was
				// made or overtaken.
				if stateOf(id) == tickClosed {
					held[id] = true
					continue
				}
				// The person already released the attempt: the settlement
				// the release recorded, read from the holding run's own
				// records — the only store a release can land in, since
				// attempt numbers are per run.
				if hold.Attempt != nil && settledByRelease(prior.Decisions, id, *hold.Attempt) {
					held[id] = true
					continue
				}
			}
			holds = append(holds, PriorHold{RunID: prior.Checkpoint.RunID, TickID: id, Event: hold})
		}
		// This run's own words about a tick are newer than any older run's:
		// a tick it dispatched, held or saw settled is answered or taken up
		// from here back.
		for j := range feed {
			line := &feed[j]
			if line.TickID == nil {
				continue
			}
			switch line.Stage {
			case reconcile.StageDispatched, reconcile.StageRedispatched, reconcile.StageRepairDispatched,
				reconcile.StageRunHeld, reconcile.StageSettled:
				held[*line.TickID] = true
			}
		}
	}
	return holds
}

// priorHoldCommand is the unblock command a standing prior hold carries, in
// the header's needs-you entry and in the try's next step alike: named by
// WHAT the run held — the same per-kind decision the newest run's own hold
// answers with (HoldClearingCommand) — and by WHICH run holds it, because
// the drafts and the attempt numbers are per run, so the clearing command
// addresses the holding run, never the run the model answers for. Nil when
// nothing a command addresses is named.
func priorHoldCommand(epicID, host string, hold PriorHold) *string {
	return HoldClearingCommand(epicID, host, hold.RunID, hold.RunID, hold.Event)
}

// holdSettledByAResume answers whether a resume made the newest run_held
// line history: any StageResumed or StageResumedAutomatically line standing
// AFTER the newest hold means a later incarnation adopted the run — the hold
// it answered was the previous incarnation's, settled by whoever resumed it.
// Position in the feed is the clock (the file is append-only), never the
// line's own stamp: a resumed line and the hold it succeeds may carry any
// clocks, but they cannot swap places in the file.
func holdSettledByAResume(feed []runfeed.Event) bool {
	lastHold, lastResume := -1, -1
	for i := range feed {
		switch feed[i].Stage {
		case reconcile.StageRunHeld:
			lastHold = i
		case reconcile.StageResumed, reconcile.StageResumedAutomatically:
			lastResume = i
		}
	}
	return lastHold >= 0 && lastResume > lastHold
}

// unansweredHolds is every run_held line no resume answers, oldest first:
// position in the feed is the clock (the file is append-only), so the holds
// standing after the last resume line are the ones nobody has settled by
// resuming the run — and everything before it is history. This is the one
// rule the waits read holds by, for the run's own feed and for a prior
// run's alike (tick z3p); holdSettledByAResume is its boolean for the
// newest line alone.
func unansweredHolds(feed []runfeed.Event) []runfeed.Event {
	lastResume := -1
	for i := range feed {
		switch feed[i].Stage {
		case reconcile.StageResumed, reconcile.StageResumedAutomatically:
			lastResume = i
		}
	}
	holds := []runfeed.Event{}
	for i := lastResume + 1; i < len(feed); i++ {
		if feed[i].Stage == reconcile.StageRunHeld {
			holds = append(holds, feed[i])
		}
	}
	return holds
}

// settledByRelease says whether a person's release answers one (tick,
// attempt) of the run whose decisions are given: the settlement decision
// the release recorded, which only ever lands in the holding run's own
// records — attempt numbers are per run, so a release addressed to another
// run's number finds no dispatch under it and refuses. The decision's
// fields are read as FIELDS, never matched in prose (settle.go's own rule),
// and a JSON number decodes as a float64.
func settledByRelease(decisions []runstate.Decision, tickID string, attempt int) bool {
	for _, d := range decisions {
		if op, _ := d.Request["op"].(string); op != reconcile.SettleOp {
			continue
		}
		if tick, _ := d.Request["tick_id"].(string); tick != tickID {
			continue
		}
		switch value := d.Request["attempt"].(type) {
		case float64:
			if int(value) == attempt {
				return true
			}
		case int:
			if value == attempt {
				return true
			}
		}
	}
	return false
}

// mergedStateOf is one tick's state as the merged waves already state it:
// the newest run's own row where it has one, the last run that touched the
// tick where it does not, the tracker's closed status behind them both.
// Empty when no wave named the tick — a tracker that did not read answers
// nothing, and the hold is then judged without it.
func mergedStateOf(m Model, tickID string) string {
	for _, w := range deref(m.Waves) {
		for _, t := range w.Ticks {
			if t.TickID == tickID {
				return t.State
			}
		}
	}
	return ""
}

// runTerminal says whether the run's own records say it ended by its own
// word — the states a run only reaches by writing them.
func runTerminal(recs Records) bool {
	if recs.Checkpoint == nil {
		return false
	}
	return recs.Checkpoint.State.Terminal()
}

// livenessNamesAnEnd says whether the liveness state IS the run's own
// durable word that it ended: the cloud record's finished vocabulary
// (completed, stopped, failed), written by the Workflow the run lives in and
// carried verbatim by the probe. The local probe's states never name an end
// — for a local run, dead means gone without a terminal word, which is
// exactly the claim [buildWaits] makes for it.
func livenessNamesAnEnd(state string) bool {
	switch state {
	case "completed", "stopped", "failed":
		return true
	}
	return false
}

// buildRemaining estimates the time left ONLY where measured tick durations
// support it: the median of what the epic's closed ticks measurably took —
// each row's own duration, the last run that touched it, dispatch to gate —
// times the ticks still open. Fewer than three measured closes support
// nothing, and the estimate names its basis.
func buildRemaining(src Sources, m Model) *Remaining {
	if m.Progress.Ticks == nil || m.Progress.Ticks.Open == 0 {
		return nil
	}
	durations := []time.Duration{}
	for _, w := range deref(m.Waves) {
		for _, t := range w.Ticks {
			if t.State != tickClosed || t.DurationSeconds == nil || t.DuplicateOf != nil {
				continue
			}
			durations = append(durations, time.Duration(*t.DurationSeconds)*time.Second)
		}
	}
	if len(durations) < 3 {
		return nil
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	median := durations[len(durations)/2]
	return &Remaining{
		ApproximateSeconds: int64(median.Seconds()) * int64(m.Progress.Ticks.Open),
		Basis:              fmt.Sprintf("median of %d measured tick durations (dispatch to gate), marked approximate", len(durations)),
	}
}
