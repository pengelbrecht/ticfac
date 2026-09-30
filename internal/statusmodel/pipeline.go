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
// stages, in its role's own order, each with its own state — the cell a
// dashboard fills left to right (GitHub Actions / Buildkite's shape). Wave 1
// (tick r5i) DECLARED the cell: the stage list is the role's and every stage
// read pending, because which stage a tick is IN is a derivation from the
// durable records. This file is that derivation. Its facts are the records —
// the dispatch markers, the gate evidence, the checkpoint's tick states, the
// absorptions, the findings — with the feed's own typed stage lines for the
// transitions only a line states (claimed, collected, the gate's own pass
// and refusal, the close). Nothing outside this file decides a stage's state,
// and no stage state is ever a worker's claim about its work: the feed is a
// hint about when to look, and the records are what the hint is about.

// The checkpoint's own tick-state vocabulary carries `integrated` too, the
// one state build.go's helper constants do not spell: an attempt whose merge
// landed and whose close is the formality behind it. Spelled here rather
// than in build.go — a file this tick does not own — and once, so the
// derivations below say one word, not a bare string each time they read it.
const tickIntegrated = "integrated"

// decorateTicks lays the pipeline cell, the parent row, the duration, the
// findings and the try vocabulary on every tick. The derivation is pure: it
// reads only the Sources (the graph, the records, the feed) and the model
// built so far — the waves with their ticks, states and try histories — and
// it decides nothing a record did not state. Where nothing states a fact the
// tick says null or pending, never a guess.
func decorateTicks(src Sources, recs Records, m *Model) {
	index := newPipelineIndex(src, recs, m.EpicID)
	for wi := range deref(m.Waves) {
		wave := &(*m.Waves)[wi]
		for ti := range wave.Ticks {
			decorateTick(src, index, &wave.Ticks[ti])
		}
	}
}

// pipelineIndex is every per-tick fact the derivation asks for, grouped the
// ways it asks: the graph's own tasks (the parent row reads their Parent),
// the dispatch markers, the absorptions, the findings by the tick that
// discovered them, and the live census. Built once per model; read per tick.
type pipelineIndex struct {
	epicID         string
	tasks          map[string]tk.GraphTask
	markersByTick  map[string][]runstate.Attempt
	markersByTry   map[string]runstate.Attempt
	evidence       []runstate.Evidence
	absorbedByTick map[string]runstate.Absorption
	absorbedByFind map[string]runstate.Absorption
	findingsByTick map[string][]runstate.Finding
	standing       map[string]bool
	feed           []runfeed.Event
}

// newPipelineIndex groups the sources' per-tick facts once, so decorating a
// hundred ticks costs one pass over each record kind rather than a hundred.
func newPipelineIndex(src Sources, recs Records, epicID string) *pipelineIndex {
	p := &pipelineIndex{
		epicID:         epicID,
		tasks:          map[string]tk.GraphTask{},
		markersByTick:  map[string][]runstate.Attempt{},
		markersByTry:   map[string]runstate.Attempt{},
		evidence:       recs.Evidence,
		absorbedByTick: map[string]runstate.Absorption{},
		absorbedByFind: map[string]runstate.Absorption{},
		findingsByTick: map[string][]runstate.Finding{},
		standing:       map[string]bool{},
		feed:           src.Feed,
	}
	if src.Graph != nil {
		for _, wave := range src.Graph.Waves {
			for _, task := range wave.Tasks {
				p.tasks[task.ID] = task
			}
		}
	}
	for _, marker := range recs.Attempts {
		p.markersByTick[marker.TickID] = append(p.markersByTick[marker.TickID], marker)
		p.markersByTry[tryKey(marker.TickID, marker.Attempt)] = marker
	}
	for _, tickIDs := range p.markersByTick {
		sort.Slice(tickIDs, func(i, j int) bool { return tickIDs[i].Attempt < tickIDs[j].Attempt })
	}
	for _, record := range recs.Absorptions {
		p.absorbedByTick[record.TickID] = record
		p.absorbedByFind[record.Key] = record
	}
	for _, finding := range recs.Findings {
		tickID := discoveredFromTick(finding.DiscoveredFrom)
		if tickID == "" {
			continue
		}
		p.findingsByTick[tickID] = append(p.findingsByTick[tickID], finding)
	}
	for _, tickIDs := range p.findingsByTick {
		sort.Slice(tickIDs, func(i, j int) bool { return tickIDs[i].Key < tickIDs[j].Key })
	}
	for _, attempt := range src.Standing {
		p.standing[tryKey(attempt.TickID, attempt.Attempt)] = true
	}
	return p
}

// decorateTick fills one tick's pipeline cell, parent row, duration, findings
// and try vocabulary. Every field the wave-1 stub left at its honest empty
// value, filled from the records here.
func decorateTick(src Sources, p *pipelineIndex, tick *Tick) {
	tick.Pipeline = p.pipelineCell(src, tick)
	tick.ParentTickID = p.parentOf(tick)
	tick.DurationSeconds = p.durationOf(src, tick)
	tick.Findings = p.findingsOf(tick.TickID)
	p.decorateTries(tick)
}

// ---------------------------------------------------------- the pipeline ---

// roleStages is the stage list one role's cell lays out from — the fixed
// shape wave 1 declared (a tick with no role is an implement tick; the review
// and close-out roles carry their own stages).
func roleStages(role string) []string {
	switch role {
	case "review":
		return PipelineReview
	case "closeout":
		return PipelineCloseout
	}
	return PipelineImplement
}

// pipelineCell derives the tick's whole cell: each stage's own state from the
// records, then the left-to-right fill the dashboard draws — the done stages
// first, at most one live stage (active or failed) behind them, and every
// stage after that first non-done one pending. A stage the rules mark done
// past a live stage reads pending: the cell states where a tick IS, and a
// merge behind a refused gate is a position nobody reached.
func (p *pipelineIndex) pipelineCell(src Sources, tick *Tick) []PipelineStage {
	stages := roleStages(tick.Role)
	stateOf := p.stageStateOf(src, tick)
	cell := make([]PipelineStage, 0, len(stages))
	filled := true
	for _, stage := range stages {
		state := StageStatePending
		if filled {
			state = stateOf(stage)
			if state != StageStateDone {
				filled = false
			}
		}
		cell = append(cell, PipelineStage{Stage: stage, State: state})
	}
	return cell
}

// stageStateOf reads one stage's own state from the records. The stage NAME
// picks the rule — work and a role tick's review share the work rule, the
// gate and ci stages carry their own — because the stage list is the role's
// and the rules are the vocabulary's, not positions in a list.
func (p *pipelineIndex) stageStateOf(src Sources, tick *Tick) func(string) string {
	current := 0
	if tick.Attempt != nil {
		current = *tick.Attempt
	}
	currentTry := tryOfAttempt(tick.Tries, current)
	stands := p.standing[tryKey(tick.TickID, current)]
	return func(stage string) string {
		switch stage {
		case StageClaim:
			return p.claimState(tick.TickID)
		case StageWork, StageReview:
			return p.workState(tick, currentTry, stands)
		case StageGate:
			return p.gateState(tick, currentTry, current)
		case StageCI:
			return p.ciState(src, tick)
		case StageMerged:
			return endState(tick, true)
		case StageClosed:
			return endState(tick, false)
		}
		return StageStatePending
	}
}

// claimState: done once the records state a dispatch — any dispatch marker,
// or the `claimed` line the run writes at the claim itself. Pending before
// that, and never anything else: a claim is not a stage a run fails at.
func (p *pipelineIndex) claimState(tickID string) string {
	if len(p.markersByTick[tickID]) > 0 || p.tickLine(tickID, reconcile.StageClaimed) != nil {
		return StageStateDone
	}
	return StageStatePending
}

// workState is the work stage — and a role tick's review stage, which is that
// role's work. Done once the tick's own state says the work was answered
// (reported, integrated or closed) or a `collected` line states the current
// attempt reported; failed when the current try was rejected and nothing
// redispatched it; active while the current attempt is in flight (the
// checkpoint's dispatched row, or a standing attempt); pending before any of
// that. The done-before-failed order is the load-bearing one: an attempt
// whose COLLECT answered and whose gate then refused reads as work done and
// a failed gate, not as work that failed.
func (p *pipelineIndex) workState(tick *Tick, currentTry *Try, stands bool) string {
	switch tick.State {
	case tickReported, tickIntegrated, tickClosed:
		return StageStateDone
	}
	if currentTry != nil && p.tryLine(tick.TickID, currentTry.Attempt, reconcile.StageCollected) != nil {
		return StageStateDone
	}
	if currentTry != nil && currentTry.Outcome == TryRejected &&
		!p.hasLaterDispatch(tick.TickID, currentTry.Attempt) {
		return StageStateFailed
	}
	if tick.State == tickDispatched || stands {
		return StageStateActive
	}
	return StageStatePending
}

// gateState: done on the gate's own `gate_passed` line, on the durable
// evidence of an all-pass gate (the outcome the records state for a try that
// reported), or on the close that only a passed gate allows — a closed tick
// whose feed line was lost is still a tick that got through its gate, and the
// cell says so rather than parking it one stage short. Failed on the
// `gate_failed` line, or on a rejection whose detail begins "gate_failed" —
// the refusal's own reason word leading the sentence the run left. Active
// from `gate_started`/`gate_running`. All of it scoped to the CURRENT
// attempt: a refused earlier try behind a live one is history the ATTEMPTS
// column counts, not where the tick is now.
func (p *pipelineIndex) gateState(tick *Tick, currentTry *Try, current int) string {
	if current > 0 && p.tryLine(tick.TickID, current, reconcile.StageGatePassed) != nil {
		return StageStateDone
	}
	if currentTry != nil && currentTry.Outcome == TryReported {
		return StageStateDone
	}
	switch tick.State {
	case tickClosed, tickIntegrated:
		return StageStateDone
	}
	if current == 0 {
		return StageStatePending
	}
	if p.tryLine(tick.TickID, current, reconcile.StageGateFailed) != nil {
		return StageStateFailed
	}
	if line := p.tryLine(tick.TickID, current, reconcile.StageRejected); line != nil &&
		strings.HasPrefix(line.Detail, reconcile.RefusedGate) {
		return StageStateFailed
	}
	if p.tryLine(tick.TickID, current, reconcile.StageGateStarted) != nil ||
		p.tryLine(tick.TickID, current, reconcile.StageGateRunning) != nil {
		return StageStateActive
	}
	return StageStatePending
}

// ciState is the close-out's own gate: the PR's CI behind a tick that has
// nothing left of its own to do. Failed when CI names a failure (the forge's
// `red`, the vocabulary's word for it); done when CI is green or the close
// already happened — the close-out closes behind a green CI by definition;
// active while the close-out holds for it (its own `closeout_held` line, or
// a pending CI); pending before the close-out starts. The states mirror
// buildLifecycle's ci phase over the same sources, so the phase bar and the
// cell cannot say two things about one PR.
func (p *pipelineIndex) ciState(src Sources, tick *Tick) string {
	switch tick.State {
	case tickClosed, tickIntegrated:
		return StageStateDone
	}
	if src.CI != nil {
		switch src.CI.State {
		case "green":
			return StageStateDone
		case "red", "failure":
			return StageStateFailed
		case "pending":
			return StageStateActive
		}
	}
	if latestStage(p.feed, "", reconcile.StageCloseoutHeld) != nil {
		return StageStateActive
	}
	return StageStatePending
}

// endState is the merged stage of an implement tick and the closed stage of
// a role tick: done when the checkpoint or the tracker says the tick closed.
// An integration that landed reads as merged too — the merge is the
// substance and the close the formality behind it — but only for merged: a
// role tick closes, and a closed role tick is the only done form of it.
func endState(tick *Tick, integratedCounts bool) string {
	switch tick.State {
	case tickClosed:
		return StageStateDone
	case tickIntegrated:
		if integratedCounts {
			return StageStateDone
		}
	}
	return StageStatePending
}

// ---------------------------------------------------------- the parent -----

// parentOf names the tick this row indents under: the graph's own Parent
// when the task carries one that is not the epic itself, and otherwise, for
// a tick the run itself created by absorbing a finding, the source tick its
// absorption record names — the field of runstate.Absorption that states
// where the decision was recorded from, which is the attempt that reported
// the finding. No field names one (a routed or backlog record can name none)
// and the parent stays null: a row nobody's record places is a row nobody
// gets to guess a place for.
func (p *pipelineIndex) parentOf(tick *Tick) *string {
	if task, ok := p.tasks[tick.TickID]; ok && task.Parent != "" && task.Parent != p.epicID {
		parent := task.Parent
		return &parent
	}
	if record, ok := p.absorbedByTick[tick.TickID]; ok {
		return record.Provenance.TickID
	}
	return nil
}

// --------------------------------------------------------- the duration ----

// durationOf measures the tick's span: from its earliest dispatch marker to
// its close — the latest gate evidence's finish, or else the `closed` line's
// own time — and to the model's now while it is open, because a tick still
// going has no close to measure to and an earlier try's refused evidence is
// not one. Null when no marker's stamp parses: a span nobody can start is a
// span nobody states.
func (p *pipelineIndex) durationOf(src Sources, tick *Tick) *int64 {
	started, ok := p.earliestDispatch(tick.TickID)
	if !ok {
		return nil
	}
	end := src.Now
	if tick.State == tickClosed || tick.State == tickIntegrated {
		if at, ok := p.latestGateFinish(tick.TickID); ok {
			end = at
		} else if at, ok := p.closedLineTime(tick.TickID); ok {
			end = at
		}
	}
	seconds := int64(end.Sub(started).Round(time.Second).Seconds())
	if seconds < 0 {
		// A close before the first dispatch is a clock disagreement between
		// records, not a negative span a renderer would have to explain.
		seconds = 0
	}
	return &seconds
}

// earliestDispatch is the tick's earliest parseable dispatch marker stamp.
func (p *pipelineIndex) earliestDispatch(tickID string) (time.Time, bool) {
	var earliest time.Time
	for _, marker := range p.markersByTick[tickID] {
		at, err := time.Parse(time.RFC3339, marker.DispatchedAt)
		if err != nil {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest, !earliest.IsZero()
}

// latestGateFinish is the latest parseable FinishedAt among the tick's gate
// evidence — the moment its gate last said something, which for a closed
// tick is the close.
func (p *pipelineIndex) latestGateFinish(tickID string) (time.Time, bool) {
	var latest time.Time
	for _, evidence := range p.evidence {
		if evidence.Provenance.TickID == nil || *evidence.Provenance.TickID != tickID || evidence.FinishedAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, evidence.FinishedAt)
		if err != nil {
			continue
		}
		if latest.IsZero() || at.After(latest) {
			latest = at
		}
	}
	return latest, !latest.IsZero()
}

// closedLineTime is the tick's `closed` line's own stamp.
func (p *pipelineIndex) closedLineTime(tickID string) (time.Time, bool) {
	line := p.tickLine(tickID, reconcile.StageClosed)
	if line == nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, line.At)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// --------------------------------------------------------- the findings ----

// findingsOf lists what this tick's reports drafted: every finding whose
// DiscoveredFrom names the tick, each with the absorption's gating verdict
// where the run decided one, and null where nobody has — a draft nobody
// triaged has no answer, and null is that no-answer stated.
func (p *pipelineIndex) findingsOf(tickID string) []TickFinding {
	out := []TickFinding{}
	for _, finding := range p.findingsByTick[tickID] {
		entry := TickFinding{Key: finding.Key, Title: finding.Title}
		if record, ok := p.absorbedByFind[finding.Key]; ok {
			gating := record.Gating
			entry.Gating = &gating
		}
		out = append(out, entry)
	}
	return out
}

// discoveredFromTick reads the tick id out of the reconciler's job-id shape
// (`run-<run>/tick-<tick>/attempt-<n>`) — the one spelling the finding
// record guarantees is never empty. A shape that carries no tick segment
// names no tick, and a finding nobody's attempt names is nobody's row.
func discoveredFromTick(discoveredFrom string) string {
	for _, segment := range strings.Split(discoveredFrom, "/") {
		if id, ok := strings.CutPrefix(segment, "tick-"); ok && id != "" {
			return id
		}
	}
	return ""
}

// ------------------------------------------------------------ the tries ----

// decorateTries fills the try vocabulary: each try's tier from its own
// dispatch marker's provenance, and — for a try the records refused — its
// reason from the feed line that refused it, and, on the last try only, the
// run's own next step. Every field the records do not state stays null: a
// reason nobody wrote is never invented, and a next step is stated only
// where the run's own facts say what happens next.
func (p *pipelineIndex) decorateTries(tick *Tick) {
	for i := range tick.Tries {
		try := &tick.Tries[i]
		try.Tier = nil
		try.Reason = nil
		try.NextStep = nil
		if marker, ok := p.markersByTry[tryKey(tick.TickID, try.Attempt)]; ok {
			try.Tier = marker.Provenance.Tier
		}
		switch try.Outcome {
		case TryRejected, TryGateFailed:
			if line := p.reasonLine(tick.TickID, try.Attempt); line != nil {
				reason := cutToWordBoundary(line.Detail, reasonDetailLimit)
				try.Reason = &reason
			}
			if i == len(tick.Tries)-1 {
				try.NextStep = p.nextStepOf(tick, *try)
			}
		}
	}
}

// reasonDetailLimit is the longest reason a try carries, in characters: the
// dashboard shows one line, and the line's own words are cut at a word
// boundary rather than half-way through one.
const reasonDetailLimit = 160

// reasonLine is the newest `rejected` or `gate_failed` line the feed holds
// for one (tick, attempt) — the run's own word for why a try did not close.
// Position in the feed is the clock, the file is append-only.
func (p *pipelineIndex) reasonLine(tickID string, attempt int) *runfeed.Event {
	var latest *runfeed.Event
	for i := range p.feed {
		line := &p.feed[i]
		if line.TickID == nil || *line.TickID != tickID || line.Attempt == nil || *line.Attempt != attempt {
			continue
		}
		switch line.Stage {
		case reconcile.StageRejected, reconcile.StageGateFailed:
			latest = line
		}
	}
	return latest
}

// nextStepOf states what the run does next about a refused last try, in the
// order the run's own facts answer it: a redispatch that already happened;
// the attention entry that names this tick (the same unblock command the
// header shows, mirrored from the same line buildWaits reads); a hold only
// a person releases; and otherwise the ladder's own answer — the run retries
// or escalates, which is what the tier policy exists to decide.
func (p *pipelineIndex) nextStepOf(tick *Tick, last Try) *string {
	if p.hasLaterDispatch(tick.TickID, last.Attempt) {
		step := fmt.Sprintf("retrying (try %d)", last.Try+1)
		return &step
	}
	if command := p.attentionCommand(tick.TickID); command != nil {
		return command
	}
	if p.heldForPerson(tick.TickID, last.Attempt) {
		step := "held — see needs-you"
		return &step
	}
	step := "the run will retry or escalate the tier"
	return &step
}

// hasLaterDispatch answers whether anything dispatched this tick again past
// the named attempt: a dispatch marker with a higher number, or the feed's
// own dispatched/redispatched line for one — a marker a refused create tore
// down is a dispatch the line still states happened.
func (p *pipelineIndex) hasLaterDispatch(tickID string, attempt int) bool {
	for _, marker := range p.markersByTick[tickID] {
		if marker.Attempt > attempt {
			return true
		}
	}
	for i := range p.feed {
		line := &p.feed[i]
		if line.TickID == nil || *line.TickID != tickID || line.Attempt == nil || *line.Attempt <= attempt {
			continue
		}
		switch line.Stage {
		case reconcile.StageDispatched, reconcile.StageRedispatched, reconcile.StageRepairDispatched:
			return true
		}
	}
	return false
}

// attentionCommand mirrors the attention entry buildWaits states for a hold
// that names this tick: the same newest run_held line (one this incarnation
// left — a hold a later resume settled is history, and holdSettledByAResume
// says so), and the same unblock command that entry carries. Mirrored rather
// than read because decorateTicks runs before buildWaits assembles the list;
// one spelling there, one mirror here, and a disagreement between a row's
// next step and the header's command is a drift this comment points at.
func (p *pipelineIndex) attentionCommand(tickID string) *string {
	held := latestStage(p.feed, "", reconcile.StageRunHeld)
	if held == nil || held.TickID == nil || *held.TickID != tickID {
		return nil
	}
	if holdSettledByAResume(p.feed) {
		return nil
	}
	if strings.HasPrefix(held.Detail, reconcile.RefusedFindingUntriaged+":") {
		command := TriageCommand(p.epicID)
		return &command
	}
	if held.Attempt != nil {
		command := fmt.Sprintf("ticfac settle %s %s %d --release \"<who>\"", p.epicID, tickID, *held.Attempt)
		return &command
	}
	return nil
}

// heldForPerson answers whether the feed leaves this tick holding a wait
// only a person clears: a `held` line for the tick (a struck-out attempt)
// that no later settle of the same attempt released. The settle is the
// release a person types; a hold it answered is history.
func (p *pipelineIndex) heldForPerson(tickID string, attempt int) bool {
	held, settled := -1, -1
	for i := range p.feed {
		line := &p.feed[i]
		if line.TickID == nil || *line.TickID != tickID {
			continue
		}
		if line.Stage == reconcile.StageHeld {
			held = i
		}
		if line.Stage == reconcile.StageSettled && line.Attempt != nil && *line.Attempt == attempt {
			settled = i
		}
	}
	return held >= 0 && held > settled
}

// ------------------------------------------------------ the feed's lines ---

// tryLine is the newest line of one stage the feed holds for one
// (tick, attempt) — the typed fact a stage transition leaves behind, or nil
// when no line of it exists.
func (p *pipelineIndex) tryLine(tickID string, attempt int, stage string) *runfeed.Event {
	var latest *runfeed.Event
	for i := range p.feed {
		line := &p.feed[i]
		if line.Stage != stage || line.TickID == nil || *line.TickID != tickID {
			continue
		}
		if line.Attempt == nil || *line.Attempt != attempt {
			continue
		}
		latest = line
	}
	return latest
}

// tickLine is the newest line of one stage the feed holds for one tick, at
// any attempt — the claim's line belongs to no attempt, and neither does
// the close's.
func (p *pipelineIndex) tickLine(tickID string, stage string) *runfeed.Event {
	var latest *runfeed.Event
	for i := range p.feed {
		line := &p.feed[i]
		if line.Stage != stage || line.TickID == nil || *line.TickID != tickID {
			continue
		}
		latest = line
	}
	return latest
}

// cutToWordBoundary cuts a feed line's detail to its first limit characters
// without splitting a word: the boundary inside the limit is the cut, and a
// detail whose first limit characters carry no boundary is cut hard rather
// than stretched.
func cutToWordBoundary(detail string, limit int) string {
	runes := []rune(detail)
	if len(runes) <= limit {
		return detail
	}
	if runes[limit] == ' ' {
		return string(runes[:limit])
	}
	for i := limit - 1; i > 0; i-- {
		if runes[i] == ' ' {
			return string(runes[:i])
		}
	}
	return string(runes[:limit])
}

// tryOfAttempt is the try the records state for one attempt number, or nil
// when no marker of it exists.
func tryOfAttempt(tries []Try, attempt int) *Try {
	for i := range tries {
		if tries[i].Attempt == attempt {
			return &tries[i]
		}
	}
	return nil
}

// tryKey is the (tick, attempt) spelling the derivation groups by — the same
// key build.go's own maps use, so the two halves of the model name one
// attempt one way.
func tryKey(tickID string, attempt int) string {
	return fmt.Sprintf("%s#%d", tickID, attempt)
}
