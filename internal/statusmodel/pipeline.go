package statusmodel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The per-tick pipeline cell (epic hn6, wave 2 — tick 3gk): every tick's
// stages, in the role's own order, each with its own state — the thing a
// dashboard fills left to right (GitHub Actions / Buildkite's shape, hn6
// rule 1: rows never move, the PIPELINE cell fills left to right). Wave 1
// (tick r5i) declared the cell; this tick derives it.
//
// The derivation is PURE over what the model already trusts: the durable
// records (the attempt markers, the gate evidence, the absorptions, the
// findings, the checkpoint), the run's event feed, the graph, and the Model
// built so far. The FEED names the moments (claimed, collected,
// gate_started, gate_passed) and the RECORDS hold the verdicts a stage's
// state rests on — the same division the package's own doc draws: a feed
// line is a hint about when to look, and the durable evidence is what a
// state is read from. Where the feed can state a moment the records have
// not yet reached (a local feed is written live while the records travel
// through origin), the line wins; where the records state a verdict the
// feed has lost (a lost line is harmless by the feed's own contract, and a
// close behind a failing gate is a close nothing stands behind), the record
// wins. Nothing here reads a worker's own claim about its work.

// tickIntegrated is the checkpoint's own word for a tick whose work is on
// the integration branch but whose gate has not run yet — the state
// integrate.go writes at the merge, before gate.go judges it.
const tickIntegrated = "integrated"

// reasonLimit bounds a try's reason: the feed line's own sentence, cut to
// its first 160 characters on a word boundary — a reason a person reads in
// one glance, not the whole refusal the line carried.
const reasonLimit = 160

// decorateTicks lays the pipeline cell on every tick and fills the drill-in
// fields the cell's row carries: the cell itself (the role's stage list,
// each stage with its own state), the parent the row indents under, the
// tick's measured duration, the findings its reports drafted, and each try's
// provenance tier, its recorded reason when it did not close, and what the
// run did next about it.
//
// The cell fills LEFT TO RIGHT: every stage after the first non-done stage
// is pending, and at most one stage is active — the layout rule the
// dashboard's rows-never-move promise rests on, so a renderer draws the
// frontier once and every later stage as not-reached.
func decorateTicks(src Sources, recs Records, m *Model) {
	waves := deref(m.Waves)
	if len(waves) == 0 {
		return
	}
	c := newPipelineContext(src, recs, m)
	for wi := range waves {
		for ti := range waves[wi].Ticks {
			c.decorate(&waves[wi].Ticks[ti])
		}
	}
}

// pipelineContext indexes the durable records and the feed once for the
// whole tick table, so every tick's derivation is a lookup and no record is
// walked per stage. Built by decorateTicks, read by the stage derivations.
type pipelineContext struct {
	now    time.Time
	feed   []runfeed.Event
	epicID string
	ci     *CIInput

	// attemptsByNumber is the dispatch marker per (tick, attempt) — the
	// provenance a try's tier comes from. attemptsByTick holds each tick's
	// dispatch numbers, ascending.
	attemptsByNumber map[string]runstate.Attempt
	attemptsByTick   map[string][]int
	// checkpointAttempt is the checkpoint row's own current attempt per
	// tick — the one later-dispatch observable the records can state.
	checkpointAttempt map[string]int
	// dispatchedByTick is the highest attempt a `dispatched` feed line names
	// per tick — the later-dispatch observable the feed can state ahead of
	// the records (the line is written after the marker is pushed, but a
	// store read between the two answers from behind).
	dispatchedByTick map[string]int
	// standingByTick says the census holds a standing attempt for the tick.
	standingByTick map[string]bool
	// gatingByKey is the absorption decision's verdict per finding key, and
	// absorptionByTick the absorption that created a tick, when one did.
	gatingByKey      map[string]bool
	absorptionByTick map[string]runstate.Absorption
	// parentByID is the graph task's own Parent per tick.
	parentByID map[string]string
	// findingsByTick is the findings whose discovery names the tick, in the
	// records' own order.
	findingsByTick map[string][]runstate.Finding
	// evidenceFinishByTick is the tick's latest gate-evidence finish.
	evidenceFinishByTick map[string]time.Time
}

// newPipelineContext gathers every per-tick index the derivations read.
func newPipelineContext(src Sources, recs Records, m *Model) *pipelineContext {
	c := &pipelineContext{
		now:                  src.Now,
		feed:                 src.Feed,
		epicID:               m.EpicID,
		ci:                   src.CI,
		attemptsByNumber:     map[string]runstate.Attempt{},
		attemptsByTick:       map[string][]int{},
		checkpointAttempt:    map[string]int{},
		dispatchedByTick:     map[string]int{},
		standingByTick:       map[string]bool{},
		gatingByKey:          map[string]bool{},
		absorptionByTick:     map[string]runstate.Absorption{},
		parentByID:           map[string]string{},
		findingsByTick:       map[string][]runstate.Finding{},
		evidenceFinishByTick: map[string]time.Time{},
	}
	for _, a := range recs.Attempts {
		c.attemptsByNumber[fmt.Sprintf("%s#%d", a.TickID, a.Attempt)] = a
		c.attemptsByTick[a.TickID] = append(c.attemptsByTick[a.TickID], a.Attempt)
	}
	for tickID := range c.attemptsByTick {
		sort.Ints(c.attemptsByTick[tickID])
	}
	if recs.Checkpoint != nil {
		for _, row := range recs.Checkpoint.Ticks {
			c.checkpointAttempt[row.TickID] = row.Attempt
		}
	}
	for _, a := range src.Standing {
		c.standingByTick[a.TickID] = true
	}
	for _, a := range recs.Absorptions {
		c.absorptionByTick[a.TickID] = a
		c.gatingByKey[a.Key] = a.Gating
	}
	for _, w := range graphWavesOf(src.Graph) {
		for _, task := range w.Tasks {
			c.parentByID[task.ID] = task.Parent
		}
	}
	for _, e := range recs.Evidence {
		if e.Provenance.TickID == nil || e.FinishedAt == "" {
			continue
		}
		finished, err := time.Parse(time.RFC3339, e.FinishedAt)
		if err != nil {
			continue
		}
		tickID := *e.Provenance.TickID
		if prior, ok := c.evidenceFinishByTick[tickID]; !ok || finished.After(prior) {
			c.evidenceFinishByTick[tickID] = finished
		}
	}
	for _, e := range c.feed {
		if e.Stage != reconcile.StageDispatched || e.TickID == nil || e.Attempt == nil {
			continue
		}
		if *e.Attempt > c.dispatchedByTick[*e.TickID] {
			c.dispatchedByTick[*e.TickID] = *e.Attempt
		}
	}
	for _, f := range recs.Findings {
		if tickID := tickOfDiscoveredFrom(f.DiscoveredFrom); tickID != "" {
			c.findingsByTick[tickID] = append(c.findingsByTick[tickID], f)
		}
	}
	return c
}

// decorate fills one tick's pipeline cell and the fields its row carries.
func (c *pipelineContext) decorate(tick *Tick) {
	tick.Pipeline = c.pipelineOf(tick)
	tick.ParentTickID = c.parentOf(tick)
	tick.DurationSeconds = c.durationOf(tick)
	tick.Findings = c.findingsOf(tick.TickID)
	for i := range tick.Tries {
		c.decorateTry(tick, i)
	}
}

// ---------------------------------------------------------- the cell -----

// stageListOf is the role's own stage list: a tick with no role is an
// implement tick; the review and close-out roles carry their own stages —
// the same three lists the contract documents and wave 1 pinned.
func stageListOf(role string) []string {
	switch role {
	case "review":
		return PipelineReview
	case "closeout":
		return PipelineCloseout
	}
	return PipelineImplement
}

// pipelineOf derives one tick's whole cell: the role's stages, each with its
// own state, then the left-to-right sweep that keeps the cell a cell —
// every stage after the first non-done one reads pending, so at most one
// stage is ever active and a renderer fills the cell from the list alone.
func (c *pipelineContext) pipelineOf(tick *Tick) []PipelineStage {
	stages := stageListOf(tick.Role)
	cell := make([]PipelineStage, 0, len(stages))
	for _, stage := range stages {
		cell = append(cell, PipelineStage{Stage: stage, State: c.stageStateOf(stage, tick)})
	}
	for i := range cell {
		if cell[i].State == StageStateDone {
			continue
		}
		for j := i + 1; j < len(cell); j++ {
			cell[j].State = StageStatePending
		}
		return cell
	}
	return cell
}

// stageStateOf derives one stage's own state before the sweep. Each stage
// reads the facts that are ITS OWN: the claim reads the dispatch markers,
// the work the checkpoint's word and the collect, the gate the gate's own
// lines and verdicts, the ci phase the lifecycle's own derivation, and the
// merged/closed stage the close itself.
func (c *pipelineContext) stageStateOf(stage string, tick *Tick) string {
	switch stage {
	case StageClaim:
		return c.claimState(tick)
	case StageWork, StageReview:
		return c.workState(tick)
	case StageGate:
		return c.gateState(tick)
	case StageCI:
		return c.ciStageState()
	case StageMerged:
		if tick.State == tickClosed || tick.State == tickIntegrated {
			return StageStateDone
		}
		return StageStatePending
	case StageClosed:
		if tick.State == tickClosed {
			return StageStateDone
		}
		return StageStatePending
	}
	return StageStatePending
}

// claimState: done once any dispatch marker or `claimed` line exists for the
// tick — the records' own proof the run took it; pending while nothing does.
func (c *pipelineContext) claimState(tick *Tick) string {
	if len(c.attemptsByTick[tick.TickID]) > 0 ||
		c.lineOf(tick.TickID, nil, reconcile.StageClaimed) != nil {
		return StageStateDone
	}
	return StageStatePending
}

// workState: the work stage (named review on a review tick — the same
// derivation, the role's own word). Done once the checkpoint says the work
// was taken in (reported, integrated or closed) or the feed says the run
// collected the current attempt's report. Active while the current attempt
// is in flight — the checkpoint's dispatched word or a standing attempt.
// Failed when the current try was rejected and nothing was dispatched after
// it: the work is what was refused, and the tick stops there until the run
// tries again.
func (c *pipelineContext) workState(tick *Tick) string {
	switch tick.State {
	case tickReported, tickIntegrated, tickClosed:
		return StageStateDone
	}
	if current := tick.Attempt; current != nil &&
		c.lineOf(tick.TickID, current, reconcile.StageCollected) != nil {
		return StageStateDone
	}
	if tick.State == tickDispatched || c.standingByTick[tick.TickID] {
		return StageStateActive
	}
	if current := currentTryOf(tick); current != nil &&
		current.Outcome == TryRejected && !c.laterDispatch(tick.TickID, current.Attempt) {
		return StageStateFailed
	}
	return StageStatePending
}

// gateState: the integrated gate's own state for the CURRENT attempt — done
// on its `gate_passed` line, failed on its `gate_failed` line, on a
// rejection the gate refusal drove (the detail begins the refusal's own
// word, gate.go's RefusedGate), or on the gate evidence that refused it;
// active from its `gate_started`/`gate_running` lines.
//
// The close is the durable half of gate_passed, read from the checkpoint
// when the feed line is gone: a close behind a failing gate is a close
// nothing stands behind (the gate's own refusal message), so a closed tick
// whose feed lost its lines reads done at the gate, not stuck before it —
// the same backing the tracker's closed status already gives the tick's own
// state. Symmetrically, the evidence that refused the current attempt (the
// record tryOutcome already reads) states failed before any line says so.
func (c *pipelineContext) gateState(tick *Tick) string {
	current := tick.Attempt
	if current != nil && c.lineOf(tick.TickID, current, reconcile.StageGatePassed) != nil {
		return StageStateDone
	}
	if tick.State == tickClosed {
		return StageStateDone
	}
	if current != nil && c.lineOf(tick.TickID, current, reconcile.StageGateFailed) != nil {
		return StageStateFailed
	}
	if current != nil {
		if rejected := c.lineOf(tick.TickID, current, reconcile.StageRejected); rejected != nil &&
			strings.HasPrefix(rejected.Detail, reconcile.RefusedGate) {
			return StageStateFailed
		}
	}
	if current := currentTryOf(tick); current != nil && current.Outcome == TryGateFailed {
		return StageStateFailed
	}
	if current != nil &&
		(c.lineOf(tick.TickID, current, reconcile.StageGateStarted) != nil ||
			c.lineOf(tick.TickID, current, reconcile.StageGateRunning) != nil) {
		return StageStateActive
	}
	return StageStatePending
}

// ciStageState: the close-out's own gate on the epic PR, mirroring the
// lifecycle's ci phase — active while the phase is, done when the phase
// marks it done. The lifecycle is built after the ticks are decorated (one
// call order in Build), so the phase is derived here over the same inputs
// buildLifecycle reads it from: the close-out's own `closeout_held` line and
// the forge's answer — the two cannot disagree because both spell the same
// rule over the same sources. Failed when the forge's answer is a failure
// (the forge's own word for it is "red"; the rule that named this stage
// spelled the class "failure" — both spellings read as failed here).
func (c *pipelineContext) ciStageState() string {
	if ci := buildCI(c.ci); ci != nil && ciFailed(ci.State) {
		return StageStateFailed
	}
	switch c.ciPhaseState() {
	case PhaseStateDone:
		return StageStateDone
	case PhaseStateActive:
		return StageStateActive
	}
	return StageStatePending
}

// ciFailed names the forge answers that are a failure on the epic PR's head.
func ciFailed(state string) bool {
	return state == "red" || state == "failure"
}

// ciPhaseState is the lifecycle's own ci phase state, derived over the same
// inputs buildLifecycle derives it from — kept beside the stage it decides
// so the two spell one rule.
func (c *pipelineContext) ciPhaseState() string {
	state := PhaseStatePending
	if latestStage(c.feed, "", reconcile.StageCloseoutHeld) != nil {
		state = PhaseStateActive
	}
	if ci := buildCI(c.ci); ci != nil {
		switch ci.State {
		case "green":
			state = PhaseStateDone
		case "red", "pending":
			state = PhaseStateActive
		}
	}
	return state
}

// ------------------------------------------------- the row's own fields --

// parentOf derives the tick this row indents under: the graph task's own
// Parent when it names a tick that is not the epic (a repair tick under the
// tick it repairs); otherwise, for a tick the run itself created by
// absorbing a finding, the SOURCE the absorption record names — the
// provenance of the decision is the dispatch whose findings were collected,
// so its tick is the one whose worker reported the finding the promotion
// turned into this row. A direct child of the epic states null: it is a
// row of the epic, indented under nobody.
func (c *pipelineContext) parentOf(tick *Tick) *string {
	if parent := c.parentByID[tick.TickID]; parent != "" && parent != c.epicID {
		return &parent
	}
	if absorption, ok := c.absorptionByTick[tick.TickID]; ok &&
		absorption.Provenance.TickID != nil && *absorption.Provenance.TickID != "" {
		return absorption.Provenance.TickID
	}
	return nil
}

// durationOf measures the tick from its earliest dispatch marker to its
// close — the latest gate evidence the records hold for it, or else its own
// `closed` line; an unstated close is bounded by the model's now, the only
// clock left. While the tick is open the measure runs to the model's now:
// the row states how long the tick has been on the table so far. Null when
// no dispatch marker states a parseable start.
func (c *pipelineContext) durationOf(tick *Tick) *int64 {
	var start time.Time
	for _, n := range c.attemptsByTick[tick.TickID] {
		marker, ok := c.attemptsByNumber[fmt.Sprintf("%s#%d", tick.TickID, n)]
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339, marker.DispatchedAt)
		if err != nil {
			continue
		}
		if start.IsZero() || at.Before(start) {
			start = at
		}
	}
	if start.IsZero() {
		return nil
	}
	end := c.now
	if tick.State == tickClosed {
		if finished, ok := c.evidenceFinishByTick[tick.TickID]; ok {
			end = finished
		} else if line := latestStage(c.feed, tick.TickID, reconcile.StageClosed); line != nil {
			if at, err := time.Parse(time.RFC3339, line.At); err == nil {
				end = at
			}
		}
	}
	seconds := int64(end.Sub(start).Round(time.Second).Seconds())
	return &seconds
}

// findingsOf lists what this tick's reports drafted, in the records' own
// order: every finding whose discovery names the tick (`run-<run>/tick-
// <tick>/attempt-<n>` names the attempt that first reported it, whatever
// try that was). The title is the finding's own; gating is the absorption
// decision's verdict when the run (or a person) recorded one, and null
// while nobody has — a draft has no answer, and null is that no-answer.
func (c *pipelineContext) findingsOf(tickID string) []TickFinding {
	drafted := c.findingsByTick[tickID]
	out := make([]TickFinding, 0, len(drafted))
	for _, f := range drafted {
		var gating *bool
		if decided, ok := c.gatingByKey[f.Key]; ok {
			gating = &decided
		}
		out = append(out, TickFinding{Key: f.Key, Title: f.Title, Gating: gating})
	}
	return out
}

// ------------------------------------------------------ the tries' half --

// decorateTry fills one try's provenance tier, its recorded reason when it
// did not close, and what the run did next about it.
func (c *pipelineContext) decorateTry(tick *Tick, i int) {
	try := &tick.Tries[i]
	if marker, ok := c.attemptsByNumber[fmt.Sprintf("%s#%d", tick.TickID, try.Attempt)]; ok {
		try.Tier = marker.Provenance.Tier
	} else {
		try.Tier = nil
	}
	try.Reason = nil
	if try.Outcome == TryRejected || try.Outcome == TryGateFailed {
		if line := c.reasonLineOf(tick.TickID, try.Attempt); line != nil {
			reason := cutAtWordBoundary(line.Detail, reasonLimit)
			try.Reason = &reason
		}
	}
	// NextStep answers "and then what", a question only the LAST try still
	// has open: an earlier try's answer is the try after it, sitting in the
	// same history, and a try that closed has no next step at all.
	try.NextStep = nil
	if i == len(tick.Tries)-1 && (try.Outcome == TryRejected || try.Outcome == TryGateFailed) {
		step := c.nextStepOf(tick, try)
		try.NextStep = &step
	}
}

// nextStepOf is what the run did next about a try that did not close, in the
// order the facts state it: a redispatch that already happened (the feed
// names the new attempt, or the checkpoint has moved on to it) is "retrying
// (try N+1)"; a hold for a person that names this tick carries the very
// command that clears it — the same unblock command the model's attention
// entry states, spelled by the same rule buildWaits spells it by; any other
// hold for a person points at the header ("held — see needs-you", hn6 rule
// 2); and failing all of those the run's own ladder answers — it will
// retry the tick or escalate the tier the next model runs on.
func (c *pipelineContext) nextStepOf(tick *Tick, try *Try) string {
	if c.laterDispatch(tick.TickID, try.Attempt) {
		return fmt.Sprintf("retrying (try %d)", try.Try+1)
	}
	if held := latestStage(c.feed, "", reconcile.StageRunHeld); held != nil &&
		!holdSettledByAResume(c.feed) {
		if held.TickID != nil && *held.TickID == tick.TickID && held.Attempt != nil {
			return fmt.Sprintf("ticfac settle %s %s %d --release \"<who>\"",
				c.epicID, tick.TickID, *held.Attempt)
		}
		return "held — see needs-you"
	}
	return "the run will retry or escalate the tier"
}

// laterDispatch says whether a dispatch for the tick happened after the
// given attempt: a later marker, the checkpoint's own current attempt, or a
// `dispatched` line naming a higher one — the last is the feed-ahead window
// where the line is written before the records this model read were fetched.
func (c *pipelineContext) laterDispatch(tickID string, attempt int) bool {
	for _, n := range c.attemptsByTick[tickID] {
		if n > attempt {
			return true
		}
	}
	return c.checkpointAttempt[tickID] > attempt || c.dispatchedByTick[tickID] > attempt
}

// ------------------------------------------------------------- readers --

// lineOf is the newest feed line of one stage about one (tick, attempt) —
// the attempt a tick-scoped line names, which the feed writes on every line
// it places. A nil attempt reads the tick's lines as the tick's own, which
// is what a stage that predates any attempt (the claim) or a tick-level
// question needs.
func (c *pipelineContext) lineOf(tickID string, attempt *int, stage string) *runfeed.Event {
	var latest *runfeed.Event
	for i := range c.feed {
		e := &c.feed[i]
		if e.Stage != stage || e.TickID == nil || *e.TickID != tickID {
			continue
		}
		if attempt != nil && (e.Attempt == nil || *e.Attempt != *attempt) {
			continue
		}
		latest = e
	}
	return latest
}

// reasonLineOf is the newest `rejected` or `gate_failed` line for one
// (tick, attempt): the run's own word for why that try did not close,
// whichever vocabulary it answered in. A line that names no attempt claims
// nothing about one and does not answer here.
func (c *pipelineContext) reasonLineOf(tickID string, attempt int) *runfeed.Event {
	var latest *runfeed.Event
	for i := range c.feed {
		e := &c.feed[i]
		if e.Stage != reconcile.StageRejected && e.Stage != reconcile.StageGateFailed {
			continue
		}
		if e.TickID == nil || *e.TickID != tickID ||
			e.Attempt == nil || *e.Attempt != attempt {
			continue
		}
		latest = e
	}
	return latest
}

// currentTryOf is the try the checkpoint's own current attempt names — the
// try whose outcome the work stage's failure reads. Nil when no attempt
// stands recorded for the tick.
func currentTryOf(tick *Tick) *Try {
	if tick.Attempt == nil {
		return nil
	}
	for i := range tick.Tries {
		if tick.Tries[i].Attempt == *tick.Attempt {
			return &tick.Tries[i]
		}
	}
	return nil
}

// tickOfDiscoveredFrom parses the discovery's own shape — `run-<run>/tick-
// <tick>/attempt-<n>`, the job id the run mints for one attempt of one tick
// — and answers the tick it names. Anything else states no tick: a finding
// whose discovery cannot be placed is left for its own record to say, never
// guessed under a row it did not name.
func tickOfDiscoveredFrom(discoveredFrom string) string {
	parts := strings.Split(discoveredFrom, "/")
	if len(parts) != 3 {
		return ""
	}
	if _, ok := strings.CutPrefix(parts[0], "run-"); !ok {
		return ""
	}
	if _, ok := strings.CutPrefix(parts[2], "attempt-"); !ok {
		return ""
	}
	tickID, ok := strings.CutPrefix(parts[1], "tick-")
	if !ok {
		return ""
	}
	return tickID
}

// cutAtWordBoundary bounds a sentence to its first limit characters, ending
// on a word boundary: the cut backs up to the last space inside the bound,
// so a reason never ends mid-word, and a sentence with no boundary inside
// the bound is cut at the bound itself — the reason a person reads is the
// line's own words, never a shard of one.
func cutAtWordBoundary(sentence string, limit int) string {
	runes := []rune(sentence)
	if len(runes) <= limit {
		return sentence
	}
	cut := runes[:limit]
	for i := len(cut) - 1; i > 0; i-- {
		if cut[i] == ' ' {
			return string(cut[:i])
		}
	}
	return string(cut)
}

// graphWavesOf is the graph's own waves, nil-safe.
func graphWavesOf(graph *tk.Graph) []tk.GraphWave {
	if graph == nil {
		return nil
	}
	return graph.Waves
}
