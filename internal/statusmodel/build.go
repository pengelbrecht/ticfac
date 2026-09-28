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

	// Graph is the epic's own graph — the same layering `tk graph` computes.
	// Nil when the tracker could not be read.
	Graph *tk.Graph
	// Records is the run's durable state, as gathered. Nil when the run's
	// records could not be read at all.
	Records *Records
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
	absorbed := absorbedTicks(recs.Absorptions)
	m.Waves, m.Progress = buildWaves(src, recs, absorbed)
	decorateTicks(src, recs, &m)
	m.Workers = buildWorkers(src, recs)
	decorateWorkers(src, recs, &m)
	decorateReports(src, &m)
	m.Health = buildHealth(src.Feed)
	m.Gates = buildGates(recs.Evidence)
	m.CI = buildCI(src.CI)
	m.Cost = buildCost(recs)
	m.Lifecycle = buildLifecycle(src, recs, m)
	m.WaitsOn, m.Attention = buildWaits(src, recs, m)
	m.Health.Verdict = buildVerdict(src, m)
	m.Remaining = buildRemaining(src, recs, m)
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

// absorbedTicks is the set of tick ids the run itself created by absorbing a
// finding into the epic it was running — the mid-run shape change the
// absorption records exist to make reconstructible.
func absorbedTicks(absorptions []runstate.Absorption) map[string]bool {
	out := map[string]bool{}
	for _, a := range absorptions {
		if a.TickID != "" && a.Gating {
			out[a.TickID] = true
		}
	}
	return out
}

// buildWaves lays the epic out as the tracker itself layers it, with every
// tick's state from the durable records. The wave states are derived, not
// stored: a wave is done when every tick in it is closed, and the first
// wave that is not done is the frontier the run works on.
func buildWaves(src Sources, recs Records, absorbed map[string]bool) (*[]Wave, Progress) {
	progress := Progress{}
	if src.Graph == nil || len(src.Graph.Waves) == 0 {
		return nil, progress
	}

	// The checkpoint's own tick states, by id.
	states := map[string]runstate.TickState{}
	if recs.Checkpoint != nil {
		for _, ts := range recs.Checkpoint.Ticks {
			states[ts.TickID] = ts
		}
	}
	// The attempt markers grouped per tick: the dispatches that happened.
	attemptsByTick := map[string][]runstate.Attempt{}
	for _, a := range recs.Attempts {
		attemptsByTick[a.TickID] = append(attemptsByTick[a.TickID], a)
	}
	// The gate evidence grouped per (tick, attempt): what each try produced.
	evidenceByTry := map[string][]runstate.Evidence{}
	for _, e := range recs.Evidence {
		if e.Provenance.TickID == nil || e.Provenance.Attempt == nil {
			continue
		}
		key := fmt.Sprintf("%s#%d", *e.Provenance.TickID, *e.Provenance.Attempt)
		evidenceByTry[key] = append(evidenceByTry[key], e)
	}
	// Which (tick, attempt) pairs still stand: the census's own answer.
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
			state, attempt := tickStateOf(task, states)
			t := buildTick(src, task, state, attempt, attemptsByTick[task.ID],
				evidenceByTry, standing, absorbed[task.ID])
			if t.State != tickClosed {
				waveDone = false
			}
			tickProgress.Total++
			if t.State == tickClosed {
				tickProgress.Closed++
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

// tickStateOf reads one tick's state and current attempt from the durable
// records: the checkpoint's own row where it has one, the tracker's closed
// status where it does not. A tick the checkpoint never mentioned is ready —
// the run has not touched it.
func tickStateOf(task tk.GraphTask, states map[string]runstate.TickState) (string, *int) {
	if ts, ok := states[task.ID]; ok {
		if ts.Attempt > 0 {
			attempt := ts.Attempt
			return ts.State, &attempt
		}
		return ts.State, nil
	}
	if task.Status == "closed" {
		return tickClosed, nil
	}
	return tickReady, nil
}

// buildTick assembles one tick's whole entry: state, try history, the
// current attempt's provenance and its elapsed time.
func buildTick(src Sources, task tk.GraphTask, state string, attempt *int, attempts []runstate.Attempt,
	evidenceByTry map[string][]runstate.Evidence, standing map[string]bool, absorbed bool) Tick {

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
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].Attempt < attempts[j].Attempt })
	numbers := make([]int, 0, len(attempts))
	byNumber := map[int]runstate.Attempt{}
	for _, a := range attempts {
		numbers = append(numbers, a.Attempt)
		byNumber[a.Attempt] = a
	}

	// The current attempt: the checkpoint's own row names it; a tick the
	// checkpoint no longer mentions keeps its highest recorded dispatch.
	current := 0
	if attempt != nil {
		current = *attempt
	} else if len(numbers) > 0 {
		current = numbers[len(numbers)-1]
	}

	for rank, n := range numbers {
		outcome := tryOutcome(state, current, n,
			evidenceByTry[fmt.Sprintf("%s#%d", task.ID, n)],
			standing[fmt.Sprintf("%s#%d", task.ID, n)] && n == current)
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
			if at, err := time.Parse(time.RFC3339, a.DispatchedAt); err == nil &&
				isLive(state, standing[fmt.Sprintf("%s#%d", task.ID, current)]) {
				elapsed := int64(src.Now.Sub(at).Round(time.Second).Seconds())
				t.ElapsedSeconds = &elapsed
			}
		}
	}
	return t
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

// isLive says whether the tick's current attempt is one the run is still
// working: the checkpoint's state says dispatched or reported, or a standing
// worktree answers for it.
func isLive(state string, stands bool) bool {
	return stands || state == tickDispatched || state == tickReported
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
func buildWaits(src Sources, recs Records, m Model) (*Wait, []Attention) {
	attention := []Attention{}
	claim := func(w Wait) {
		if m.WaitsOn == nil {
			m.WaitsOn = &w
		}
		if w.NeedsPerson {
			attention = append(attention, Attention(w))
		}
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
	if held := latestStage(src.Feed, "", reconcile.StageRunHeld); held != nil &&
		!holdSettledByAResume(src.Feed) {
		w := Wait{
			Kind:        WaitHeldForPerson,
			What:        held.Detail,
			NeedsPerson: true,
		}
		since := held.At
		w.Since = &since
		// The command is named by WHAT the run is holding, not by the line's
		// own shape (tick gtk): the close-out's untriaged-findings hold is
		// cleared by triage — settle releases an attempt, and this hold
		// holds a person's decision about findings, not an attempt. Every
		// other hold is the settle command the run-wide dispatch number
		// addresses — the same sentence `ticfac watch` prints.
		if strings.HasPrefix(held.Detail, reconcile.RefusedFindingUntriaged+":") {
			unblock := TriageCommand(m.EpicID)
			w.UnblockCommand = &unblock
		} else if held.TickID != nil && held.Attempt != nil {
			unblock := fmt.Sprintf("ticfac settle %s %s %d --release \"<who>\"", m.EpicID, *held.TickID, *held.Attempt)
			w.UnblockCommand = &unblock
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
			// still held.
			unblock := TriageCommand(m.EpicID)
			w.UnblockCommand = &unblock
			attention = append(attention, Attention(w))
		}
	}
	return m.WaitsOn, attention
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
// support it: the median of what closed ticks measurably took (dispatch
// marker to last gate evidence), times the ticks still open. Fewer than
// three measured closes support nothing, and the estimate names its basis.
func buildRemaining(src Sources, recs Records, m Model) *Remaining {
	if m.Progress.Ticks == nil || m.Progress.Ticks.Open == 0 {
		return nil
	}
	attemptsByKey := map[string]time.Time{}
	for _, a := range recs.Attempts {
		if at, err := time.Parse(time.RFC3339, a.DispatchedAt); err == nil {
			attemptsByKey[fmt.Sprintf("%s#%d", a.TickID, a.Attempt)] = at
		}
	}
	finishedByKey := map[string]time.Time{}
	for _, e := range recs.Evidence {
		if e.Provenance.TickID == nil || e.Provenance.Attempt == nil || e.FinishedAt == "" {
			continue
		}
		finished, err := time.Parse(time.RFC3339, e.FinishedAt)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%s#%d", *e.Provenance.TickID, *e.Provenance.Attempt)
		if prior, ok := finishedByKey[key]; !ok || finished.After(prior) {
			finishedByKey[key] = finished
		}
	}
	durations := []time.Duration{}
	for _, w := range deref(m.Waves) {
		for _, t := range w.Ticks {
			if t.State != tickClosed || t.Attempt == nil {
				continue
			}
			started, ok := attemptsByKey[fmt.Sprintf("%s#%d", t.TickID, *t.Attempt)]
			finished, ok2 := finishedByKey[fmt.Sprintf("%s#%d", t.TickID, *t.Attempt)]
			if ok && ok2 && finished.After(started) {
				durations = append(durations, finished.Sub(started))
			}
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
