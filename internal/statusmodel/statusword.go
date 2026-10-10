package statusmodel

import (
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The status words (epic ymf, tick lck): every tick's answer in the
// operator's words, derived from the pipeline cell the records already fill
// — never a second opinion about the work, and never a worker's own claim.
// The dashboard groups the rows by them, the phase track folds the
// lifecycle's phases into the four steps a person reads, and the exception
// note carries the one thing that is unusual about an unfinished attempt.
// Everything here reads the same sources pipeline.go reads; where nothing
// states a fact the word stays quiet rather than guessing.

// The status word vocabulary, the words the watch redesign's layout names.
// The three prefixed words carry a reason after the prefix — a hold's own
// detail, a refusal's own sentence, the blocker a tick waits behind.
const (
	WordUpNext       = "up next"
	WordClaimed      = "claimed"
	WordWritingCode  = "writing code"
	WordReviewing    = "reviewing"
	WordClosingOut   = "closing out"
	WordTesting      = "testing"
	WordMerging      = "merging"
	WordMerged       = "merged"
	WordWaitingForCI = "waiting for CI"
	WordDone         = "done"
	WordFailed       = "failed"

	WordHeldPrefix    = "held: "
	WordFailedPrefix  = "failed: "
	WordWaitingPrefix = "waiting: "

	// Exception components, joined with ", " when more than one applies.
	ExceptionAttempt  = "attempt" // followed by the try number
	ExceptionEscalate = "model escalated"
	ExceptionStalled  = "stalled" // followed by the idle span in minutes
)

// TrackLabels is the epic phase track's fixed vocabulary, in the order the
// watch redesign's layout draws it: building ── reviewing ── closing out ──
// PR & CI ── merged. The first folds the plan and waves phases, the rest
// mirror one phase each (PR & CI folds the CI phase and the merge into the
// chapter the PR owns, and merged is the merge phase's own state).
var TrackLabels = []string{"building", "reviewing", "closing out", "PR & CI", "merged"}

// decorateStatus lays the status word and the exception note on one tick.
// It runs after the cell and the try vocabulary are filled, because both are
// its inputs: the word reads the cell's live stage, the note reads the try
// history's tiers and the census's idle gaps.
func (p *pipelineIndex) decorateStatus(tick *Tick) {
	tick.Status = p.statusWordOf(tick)
	tick.Exception = p.exceptionOf(tick, tick.Status)
}

// statusWordOf answers one tick's status word. The order is the operator's:
// a hold on the tick is the first thing to say (it needs a person NOW),
// then whatever stage the cell names as live or refused, then the queued
// and finished words the cell's shape states.
func (p *pipelineIndex) statusWordOf(tick *Tick) string {
	if hold := p.holdOn(tick.TickID); hold != nil {
		return WordHeldPrefix + cutToWordBoundary(hold.Detail, reasonDetailLimit)
	}
	stage, state := liveStageOf(tick.Pipeline)
	if state == StageStateFailed {
		return p.failedWord(tick, stage)
	}
	if stage == "" {
		// Every stage done: the cell's last stop names the word — merged for
		// an implement tick's work, done for a role tick's answer.
		if len(tick.Pipeline) > 0 && tick.Pipeline[len(tick.Pipeline)-1].Stage == StageMerged {
			return WordMerged
		}
		return WordDone
	}
	var word string
	switch stage {
	case StageClaim:
		word = p.upNextWord(tick)
	case StageWork, StageReview:
		if state == StageStatePending {
			// Claimed but not yet seen working: the run took the tick and
			// nothing of its attempt has happened yet.
			word = WordClaimed
		} else {
			word = activeWorkWord(tick.Role, stage)
		}
	case StageGate:
		word = WordTesting
	case StageCI:
		word = WordWaitingForCI
	default:
		// Work answered and its gate passed: the run is filing the finished
		// work — merging it into the epic or closing the tick on it. The one
		// word the design's list does not name, for the moment between an
		// all-pass gate and the integrate it triggers.
		word = WordMerging
	}
	// A word that says something is happening now is only true while
	// something can still happen: a run that is not going and holds no live
	// attempt for the tick is not writing code, not testing, not merging —
	// the tick waits for the run to be started again, and the word says what
	// the run's own ending was (tick jkb: never a lie the header has to
	// correct).
	if isActiveWord(word) && !p.attemptWorking(tick) {
		return WordWaitingPrefix + endingPhrase(p.ending)
	}
	return word
}

// activeWorkWord is the in-flight work word for one role: a review tick is
// reviewing, a close-out is closing out, and an implement tick is writing
// code — the role's own stage name in the operator's words.
func activeWorkWord(role, stage string) string {
	if stage == StageReview {
		return WordReviewing
	}
	switch role {
	case "closeout":
		return WordClosingOut
	}
	return WordWritingCode
}

// isActiveWord says whether a status word claims the tick is being worked
// right now — the words a stopped run would turn into lies, and the ones
// statusWordOf re-words when nothing can move the tick.
func isActiveWord(word string) bool {
	switch word {
	case WordClaimed, WordWritingCode, WordReviewing, WordClosingOut, WordTesting, WordMerging:
		return true
	}
	return false
}

// endingPhrase is the wait reason for a tick nothing can move: the run's own
// ending, in the words the run's records stated it — and "the run is not
// going" when no record states one (a run that died without a word, tick
// jkb). A completed run's open ticks are the same quiet case: nothing is
// working on them either.
func endingPhrase(ending string) string {
	switch ending {
	case endFailed:
		return "the run failed"
	case endStopped:
		return "the run is stopped"
	case endCancelled:
		return "the run was cancelled"
	case endCompleted:
		return "the run has finished"
	}
	return "the run is not going"
}

// attemptWorking says whether anything can still move this tick's current
// attempt: the run is alive, or the attempt stands in the census. A dead
// run whose attempt is gone has nothing in flight, whatever the checkpoint's
// last row says.
func (p *pipelineIndex) attemptWorking(tick *Tick) bool {
	if p.alive {
		return true
	}
	current := 0
	if tick.Attempt != nil {
		current = *tick.Attempt
	}
	return p.standing[tryKey(tick.TickID, current)]
}

// upNextWord is the queued word: up next for a tick its wave has not reached
// or whose blockers have all closed, and "waiting: X" for one the tracker's
// own blocked_by edges hold behind open work — X naming each blocker by its
// title, and saying which ones are held (a tick waits behind a held question
// until that question is answered, tick tyd).
func (p *pipelineIndex) upNextWord(tick *Tick) string {
	if p.waveState[tick.TickID] == WaveUpcoming {
		return WordUpNext
	}
	task, ok := p.tasks[tick.TickID]
	if !ok {
		return WordUpNext
	}
	blockers := make([]string, 0, len(task.BlockedBy))
	for _, id := range task.BlockedBy {
		if id != "" && !p.closedTicks[id] {
			blockers = append(blockers, id)
		}
	}
	if len(blockers) == 0 {
		return WordUpNext
	}
	parts := make([]string, 0, len(blockers))
	for _, id := range blockers {
		name := id
		if blocker, ok := p.tasks[id]; ok && blocker.Title != "" {
			name = blocker.Title
		}
		if p.holdOn(id) != nil {
			name += " is held"
		}
		parts = append(parts, name)
	}
	return cutToWordBoundary(WordWaitingPrefix+strings.Join(parts, ", "), reasonDetailLimit)
}

// failedWord is the refused word: the run's own sentence for why (the same
// reason the try carries, with a stage-name prefix the word already says
// stripped), CI's own red for the close-out's gate, and the bare "failed"
// when no record states why — a refusal nobody wrote a reason for is not
// given one.
func (p *pipelineIndex) failedWord(tick *Tick, stage string) string {
	if stage == StageCI {
		return WordFailedPrefix + "CI is red on the epic PR"
	}
	current := 0
	if tick.Attempt != nil {
		current = *tick.Attempt
	}
	if try := tryOfAttempt(tick.Tries, current); try != nil && try.Reason != nil {
		reason := strings.TrimPrefix(*try.Reason, reconcile.StageGateFailed+": ")
		reason = strings.TrimPrefix(reason, reconcile.StageRejected+": ")
		if reason != "" {
			return WordFailedPrefix + reason
		}
	}
	return WordFailed
}

// exceptionOf is the inline note for the things that are unusual about an
// unfinished attempt, each only when it applies: the tick's current work is
// not its first try; the tier ladder re-cut the model higher (or the model
// itself changed) between this tick's tries; a standing worker's durable
// output went quiet past the stall threshold the run itself warns at. A tick
// that has finished carries none: its history is its try list, and a done
// row stays calm.
func (p *pipelineIndex) exceptionOf(tick *Tick, status string) *string {
	switch tick.State {
	case tickClosed, tickIntegrated:
		return nil
	}
	parts := []string{}
	if tick.Try != nil && *tick.Try > 1 {
		parts = append(parts, fmt.Sprintf("%s %d", ExceptionAttempt, *tick.Try))
	}
	if p.escalated(tick) {
		parts = append(parts, ExceptionEscalate)
	}
	if idle, ok := p.stalled(tick, status); ok {
		parts = append(parts, fmt.Sprintf("%s %dm", ExceptionStalled, int(idle.Minutes())))
	}
	if len(parts) == 0 {
		return nil
	}
	note := strings.Join(parts, ", ")
	return &note
}

// escalated says whether this tick's current attempt is a re-cut: its tier
// differs from the first try's, or — where the tiers agree or a side is
// unstated — its model differs from the first try's. The tier is what the
// ladder moves; the model is what the rung routed.
func (p *pipelineIndex) escalated(tick *Tick) bool {
	if len(tick.Tries) < 2 {
		return false
	}
	first := tick.Tries[0]
	if first.Tier != nil && tick.Tier != nil && *first.Tier != *tick.Tier {
		return true
	}
	firstModel, ok := p.modelOf(tick.TickID, first.Attempt)
	if !ok || tick.Model == nil {
		return false
	}
	return *firstModel != *tick.Model
}

// modelOf is one attempt's provenance model, from its dispatch marker.
func (p *pipelineIndex) modelOf(tickID string, attempt int) (*string, bool) {
	marker, ok := p.markersByTry[tryKey(tickID, attempt)]
	if !ok || marker.Provenance.Model == nil {
		return nil, false
	}
	return marker.Provenance.Model, true
}

// stallThreshold is how quiet a standing worker must be before the status
// word carries a stall note: the same threshold the run itself warns at, so
// the note and the warning cannot disagree about what counts as stalled.
var stallThreshold = reconcile.DefaultStallWarnAfter

// stalled answers whether the tick's current attempt is standing and quiet:
// the census's own idle gap — how long since the branch last moved or the
// worktree last changed, whichever is newer — past the run's stall
// threshold, while the word still says the worker is the thing working. A
// gate that is running is the run's own process, not the worker's, so its
// word carries no stall note; a gap the census could not measure states
// nothing.
func (p *pipelineIndex) stalled(tick *Tick, status string) (time.Duration, bool) {
	switch status {
	case WordWritingCode, WordReviewing, WordClosingOut:
	default:
		return 0, false
	}
	current := 0
	if tick.Attempt != nil {
		current = *tick.Attempt
	}
	idle, ok := p.standingIdle[tryKey(tick.TickID, current)]
	if !ok || idle < stallThreshold {
		return 0, false
	}
	return idle, true
}

// holdOn is the newest hold that stands over one tick, or nil: a run_held
// line the current incarnation wrote and no resume answered, an attempt
// struck out for a person that no settle released, or — for a tick whose
// hold an earlier run left — that run's own unanswered line (tick z3p). The
// newest of the run's own candidates wins, because the feed is append-only
// and position is the clock; the prior runs' holds answer only where the
// current run's feed says nothing.
func (p *pipelineIndex) holdOn(tickID string) *runfeed.Event {
	best := -1
	var hold *runfeed.Event
	for i := range p.feed {
		line := &p.feed[i]
		if line.TickID == nil || *line.TickID != tickID {
			continue
		}
		switch line.Stage {
		case reconcile.StageRunHeld:
			// A hold the current incarnation wrote; one a later resume made
			// history is not standing (the same rule the waits read holds by).
			if resumeStandsAfter(p.feed, i) {
				continue
			}
		case reconcile.StageHeld:
			// An attempt struck out for a person; a hold the settle released
			// is history.
			if p.settledAfter(tickID, line.Attempt, i) {
				continue
			}
		default:
			continue
		}
		if i > best {
			best = i
			hold = line
		}
	}
	if hold != nil {
		return hold
	}
	if prior, ok := p.priorHold[tickID]; ok {
		return &prior.Event
	}
	return nil
}

// resumeStandsAfter says whether a resume line stands after position i in
// the feed: a resume adopts the run, and the holds before it belong to the
// incarnation it replaced.
func resumeStandsAfter(feed []runfeed.Event, i int) bool {
	for j := i + 1; j < len(feed); j++ {
		switch feed[j].Stage {
		case reconcile.StageResumed, reconcile.StageResumedAutomatically:
			return true
		}
	}
	return false
}

// settledAfter says whether the attempt a held line names was released after
// that line: the settle is the release a person typed, and a hold it
// answered is history.
func (p *pipelineIndex) settledAfter(tickID string, attempt *int, from int) bool {
	if attempt == nil {
		return false
	}
	for i := from + 1; i < len(p.feed); i++ {
		line := &p.feed[i]
		if line.TickID == nil || *line.TickID != tickID || line.Stage != reconcile.StageSettled {
			continue
		}
		if line.Attempt != nil && *line.Attempt == *attempt {
			return true
		}
	}
	return false
}

// liveStageOf is the cell's first stage that is not done — the one stage the
// tick is in, or the first one ahead of it — with that stage's own state.
// An all-done cell answers empty with done.
func liveStageOf(cell []PipelineStage) (string, string) {
	for _, stage := range cell {
		if stage.State != StageStateDone {
			return stage.Stage, stage.State
		}
	}
	return "", StageStateDone
}

// groupsOf buckets every tick by its status word, in the waves' own order:
// held ticks and — on a run that is not going — refused ones are stuck where
// a person is wanted; finished ones are done; queued and blocked ones are up
// next; everything the run is on now, failures it is recovering from
// included, is NOW. Null when the tracker answered no waves: there is
// nothing to group and nothing to claim.
func (p *pipelineIndex) groupsOf(m *Model) *TickGroups {
	if m.Waves == nil {
		return nil
	}
	groups := &TickGroups{
		Now:    []string{},
		Done:   []string{},
		UpNext: []string{},
		Held:   []string{},
	}
	for _, wave := range *m.Waves {
		for i := range wave.Ticks {
			tick := &wave.Ticks[i]
			switch {
			case strings.HasPrefix(tick.Status, WordHeldPrefix):
				groups.Held = append(groups.Held, tick.TickID)
			case tick.Status == WordFailed || strings.HasPrefix(tick.Status, WordFailedPrefix):
				if p.notGoing {
					groups.Held = append(groups.Held, tick.TickID)
				} else {
					groups.Now = append(groups.Now, tick.TickID)
				}
			case tick.Status == WordMerged || tick.Status == WordDone:
				groups.Done = append(groups.Done, tick.TickID)
			case tick.Status == WordUpNext || strings.HasPrefix(tick.Status, WordWaitingPrefix):
				groups.UpNext = append(groups.UpNext, tick.TickID)
			default:
				groups.Now = append(groups.Now, tick.TickID)
			}
		}
	}
	return groups
}

// phaseTrackOf folds the lifecycle's phases into the track the watch
// redesign draws. Building is the plan and the waves together; the rest
// mirror one phase each, except PR & CI, which is done once the forge's
// answer is in or no PR remains — the chapter the PR owns is over either
// way, and the merge phase's own state is what merged reads.
func phaseTrackOf(phases []PhaseState) ([]TrackStep, int) {
	state := map[string]string{}
	for _, phase := range phases {
		state[phase.Phase] = phase.State
	}
	track := []TrackStep{
		{Label: TrackLabels[0], State: buildingState(state[PhasePlan], state[PhaseWaves])},
		{Label: TrackLabels[1], State: state[PhaseReview]},
		{Label: TrackLabels[2], State: state[PhaseCloseout]},
		{Label: TrackLabels[3], State: prCIState(state[PhaseCI], state[PhaseMerge])},
		{Label: TrackLabels[4], State: state[PhaseMerge]},
	}
	// The marker sits on the newest step the epic is in; ahead of that, on
	// the first step not yet reached; and on the last when everything is
	// behind it — a merged epic is here at merged, a fresh one at building.
	here := -1
	for i := range track {
		if track[i].State == PhaseStateActive {
			here = i
		}
	}
	if here < 0 {
		for i := range track {
			if track[i].State != PhaseStateDone {
				here = i
				break
			}
		}
	}
	if here < 0 {
		here = len(track) - 1
	}
	return track, here
}

// buildingState folds two phases into one step's state: done when both are,
// active while either is, pending before either.
func buildingState(plan, waves string) string {
	switch {
	case plan == PhaseStateDone && waves == PhaseStateDone:
		return PhaseStateDone
	case plan == PhaseStateActive || waves == PhaseStateActive:
		return PhaseStateActive
	}
	return PhaseStatePending
}

// prCIState is the PR chapter's own state: done once the forge is green or
// no PR remains, active while either the close-out's CI gate or the merge is
// the thing happening, pending before the PR exists.
func prCIState(ci, merge string) string {
	switch {
	case ci == PhaseStateDone || merge == PhaseStateDone:
		return PhaseStateDone
	case ci == PhaseStateActive || merge == PhaseStateActive:
		return PhaseStateActive
	}
	return PhaseStatePending
}
