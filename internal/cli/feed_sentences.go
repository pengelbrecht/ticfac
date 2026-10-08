package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// Readable feed sentences for the dashboard's recent-activity section (tick
// 47j): every stage the feed can emit, in the operator's words, with no
// shas and no internal stage names — and a handful of pure mechanics
// (pushed, push_queued, policy_stated, cleaned_up) dropped from this section
// entirely. Raw events are unaffected: the [e] view keeps printing the
// feed's own "stage: detail" form (watchEventLineWith), untouched by this
// file.
//
// feedSentenceGuardTest (feed_sentences_guard_test.go) parses every stage
// constant declared in internal/reconcile and internal/runfeed and fails if
// one is missing from feedSentences below — a nil entry means "filtered",
// an absent one means "nobody taught this section the new stage".
type feedSentenceBuilder func(detail string) string

// feedSentences maps every stage the feed emits to the sentence this
// section shows for it. A nil builder is a pure mechanic this section
// drops entirely: the [e] view still carries it, raw.
var feedSentences = map[string]feedSentenceBuilder{
	// The per-tick lifecycle (reconcile.go's own stage block).
	reconcile.StageRefreshed:        feedConst("pulled in the latest base"),
	reconcile.StageSkipped:          feedPrefixed("skipped: "),
	reconcile.StageHeld:             feedPrefixed("held: "),
	reconcile.StageClaimed:          feedConst("claimed"),
	reconcile.StageDispatched:       feedConst("dispatched, writing code"),
	reconcile.StageAdopted:          feedConst("adopted — already in progress"),
	reconcile.StageWaiting:          feedPrefixed("waiting: "),
	reconcile.StageCollected:        feedConst("work collected"),
	reconcile.StageRejected:         feedPrefixed("rejected: "),
	reconcile.StageIntegrated:       feedConst("merged into the epic, now testing"),
	reconcile.StagePublished:        feedConst("published"),
	reconcile.StageGatePassed:       feedConst("tests passed"),
	reconcile.StageGateFailed:       feedPrefixed("tests failed: "),
	reconcile.StageStale:            feedConst("earlier test evidence no longer applies; testing again"),
	reconcile.StageClosed:           feedConst("finished and merged"),
	reconcile.StageRedispatched:     feedPrefixed("retrying: "),
	reconcile.StageCleanedUp:        nil,
	reconcile.StageResumed:          feedPrefixed("resumed: "),
	reconcile.StageSettled:          feedConst("wrapped up"),
	reconcile.StageRepairDispatched: feedConst("sending a repair after the failed test run"),
	reconcile.StageRunFinished:      feedPrefixed("run finished: "),
	reconcile.StageBudgetSet:        feedPrefixed("budget set: "),
	reconcile.StagePolicyStated:     nil,
	reconcile.StageRunDied:          feedPrefixed("run stopped: "),
	reconcile.StageTierDerived:      feedPrefixed("model tier chosen: "),
	reconcile.StageClassified:       feedClause("classified as "),
	reconcile.StageReplanned:        feedConst("the plan refreshed; new work is ready"),

	// The findings channel.
	reconcile.StageFindingFiled:         feedConst("new finding filed for triage"),
	reconcile.StageFindingDuplicate:     feedConst("a repeated finding — nothing new"),
	reconcile.StageFindingFolded:        feedConst("a worker's finding carried extra notes, folded in"),
	reconcile.StageClosedCarrying:       feedConst("finished; an untriaged finding rides to the close-out"),
	reconcile.StageAbsorbed:             feedConst("a finding was promoted into the epic's plan"),
	reconcile.StageBacklogged:           feedConst("a finding was filed to the backlog"),
	reconcile.StageReviewRound:          feedReviewRound,
	reconcile.StageFindingRouted:        feedConst("a finding was filed in another repository's tracker"),
	reconcile.StageFindingRouteDeferred: feedConst("filing a finding elsewhere failed for now; the close-out will retry"),
	reconcile.StageAbsorptionRefused:    feedConst("a finding is left for a person to decide"),
	reconcile.StageBackloggedPastBound:  feedConst("a finding chain hit its bound; filed to the backlog instead"),
	reconcile.StagePredictionScored:     feedConst("scored an earlier prediction against what happened"),

	// Dispatch, starts and the base fold.
	reconcile.StageStartFailed:     feedPrefixed("a dispatch failed to start: "),
	reconcile.StageStartPublished:  feedConst("published a commit so the next try isn't stuck on it"),
	reconcile.StageRefreshDeferred: feedConst("pulling in the latest base didn't land yet; working ahead and retrying"),
	reconcile.StageRunHeld:         feedConst("paused: needs you"),
	reconcile.StageCarried:         feedConst("retrying, picking up the released work"),

	// The epic PR and the close-out.
	reconcile.StagePROpened:           feedConst("opened the epic's pull request"),
	reconcile.StagePRBodyWritten:      feedConst("updated the pull request's description"),
	reconcile.StageCloseoutAdmitted:   feedConst("CI is green; closing out"),
	reconcile.StageCloseoutHeld:       feedConst("closing out is waiting on CI"),
	reconcile.StageCloseoutCloseGated: feedConst("CI is green after the close-out's own changes; wrapping up"),

	// Keeping the PR ready, and landing it.
	reconcile.StageLanding:          feedConst("getting the pull request ready to merge"),
	reconcile.StageLandBaseMoved:    feedConst("the base moved; redoing the merge-readiness pass"),
	reconcile.StageLandHeld:         feedConst("waiting on CI before the merge"),
	reconcile.StageLandCIGreen:      feedConst("CI is green"),
	reconcile.StagePRReady:          feedConst("the pull request is ready to merge"),
	reconcile.StageLanded:           feedConst("merged into the base branch"),
	reconcile.StageLandVerified:     feedConst("confirmed CI is green after the merge"),
	reconcile.StageLandSkipped:      feedConst("no pull request to keep ready"),
	reconcile.StageEpicClosed:       feedConst("closed the epic"),
	reconcile.StageEpicCloseRefused: feedConst("couldn't close the epic yet — it still has open work"),

	// Resilience: CI, the host, wall clocks, stalls, the network.
	reconcile.StageCIRestarted:          feedConst("restarted CI; its last run gave no verdict"),
	reconcile.StageHostSuspended:        feedConst("this host was asleep for a while; back now"),
	reconcile.StageWallClock:            feedConst("an attempt ran out of time; stopping it"),
	reconcile.StageStallWarned:          feedConst("a worker is alive but has produced nothing in a while"),
	reconcile.StageGateStarted:          feedConst("testing"),
	reconcile.StageGateRunning:          feedConst("still testing"),
	reconcile.StageGateStalled:          feedConst("testing has produced nothing in a while; worth a look"),
	reconcile.StageRemoteRetried:        feedConst("a network hiccup; retrying"),
	reconcile.StageRemoteExhausted:      feedConst("giving up after repeated network failures"),
	reconcile.StageResumedAutomatically: feedConst("picked back up automatically after a stop"),
	reconcile.StageSupervisionHalted:    feedConst("stopped and won't continue on its own: needs you"),

	// A worker that stops to ask.
	reconcile.StageBlockedEscalated: feedConst("a worker asked a question; retrying one tier up"),
	reconcile.StageBlockedDecide:    feedConst("a worker asked a question; retrying with a decision to log"),
	reconcile.StageBlockedHeld:      feedConst("paused: a worker's question needs you"),
	reconcile.StageWaitsBehindHeld:  feedConst("waiting behind a held tick"),
	reconcile.StageTickHeld:         feedConst("held; the run continues with everything else"),

	// Protected-path changes and tracker edits a worker may not make itself.
	reconcile.StageProtectedChangeHeld:    feedConst("a proposed change needs the operator to apply it"),
	reconcile.StageProtectedChangeApplied: feedConst("applied a proposed change for the merger to review"),
	reconcile.StageProtectedChangeRefused: feedPrefixed("a proposed change was refused: "),
	reconcile.StageTrackerEdited:          feedConst("applied a tracker edit a worker proposed"),
	reconcile.StageTrackerEditRefused:     feedPrefixed("refused a proposed tracker edit: "),
	reconcile.StageAmendmentFiled:         feedConst("filed an amendment to the epic for the operator to confirm"),

	// Pushes: the four named mechanics, and the two that are not.
	reconcile.StagePushed:            nil,
	reconcile.StagePushQueued:        nil,
	reconcile.StagePushQueueOverdue:  feedConst("pushing is behind; pushed anyway past the queue's limit"),
	reconcile.StageGitHubErrorPrefix: feedPrefixed("a push hit a GitHub error: "),

	// Infrastructure, rejected work and stuck workers.
	reconcile.StageInfrastructureRedispatched: feedConst("a service outside the job didn't answer; retrying"),
	reconcile.StageRejectedWorkCarried:        feedConst("retrying, carrying over the rejected attempt's work"),
	reconcile.StageRejectedWorkReleased:       feedConst("retrying from scratch; the rejected attempt's work wasn't trusted"),
	reconcile.StageStuckNudged:                feedConst("nudged a worker that had gone quiet"),
	reconcile.StageStuckStopped:               feedConst("stopped a worker that stayed quiet after a nudge"),
	reconcile.StageWipNudged:                  feedConst("asked a worker to commit its uncommitted changes"),

	// Resume, capacity and configuration.
	reconcile.StagePolledAtResume:       feedConst("checked on an attempt while starting back up"),
	reconcile.StageWaitingForCapacity:   feedConst("waiting for a free slot to start"),
	reconcile.StageConfigSelected:       feedPrefixed("running with: "),
	reconcile.StageClaudeSubSteppedDown: feedPrefixed("stepped down from claude to another model: "),

	// A sibling run's leftovers, taken over by this one.
	reconcile.StageInheritUnreadable:    feedConst("skipped an older run's record it couldn't read"),
	reconcile.StageClaimTakenOver:       feedConst("took over a claim an ended run left behind"),
	reconcile.StageFindingAdopted:       feedConst("took over an untriaged finding an ended run left behind"),
	reconcile.StageSettledWorkCollected: feedConst("used work another run already finished, instead of redoing it"),
	reconcile.StageDuplicateClosed:      feedConst("closed a duplicate tick"),
	reconcile.StageDuplicateLeft:        feedPrefixed("a duplicate tick is still open: "),
}

// feedConst is a sentence that ignores the event's own detail: the stage
// alone says everything an operator needs.
func feedConst(sentence string) feedSentenceBuilder {
	return func(string) string { return sentence }
}

// feedPrefixed leads with `prefix` and the detail's own first clause, its
// leading machine reason code (if any, "snake_case: ") and shas stripped —
// the detail is usually already the one sentence worth keeping.
func feedPrefixed(prefix string) feedSentenceBuilder {
	return func(detail string) string { return prefix + feedClauseOf(detail) }
}

// feedClause is feedPrefixed's cousin for a detail whose own prose already
// reads as "verb + object" (StageClassified's "classified as X"): the
// prefix and the clause are joined with no colon.
func feedClause(prefix string) feedSentenceBuilder {
	return func(detail string) string { return prefix + feedClauseOf(detail) }
}

// feedReasonCode matches a detail's leading machine reason code — the
// "snake_case: " a refusal's own line leads with (reconcile.recordRefusal,
// StageRunHeld, StageTrackerEditRefused, …) — which is exactly the internal
// vocabulary this section exists to hide.
var feedReasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]*: `)

// feedClauseOf is a detail cut to the one clause worth showing: its first
// line, its leading reason code stripped, cut again at the first clause
// boundary a long explanation's prose uses.
func feedClauseOf(detail string) string {
	if i := strings.IndexByte(detail, '\n'); i >= 0 {
		detail = detail[:i]
	}
	detail = strings.TrimSpace(detail)
	if loc := feedReasonCode.FindStringIndex(detail); loc != nil && loc[0] == 0 {
		detail = detail[loc[1]:]
	}
	for _, sep := range []string{" — ", "; ", ". "} {
		if i := strings.Index(detail, sep); i >= 0 {
			detail = detail[:i]
		}
	}
	return strings.TrimSpace(detail)
}

// feedReviewRound reads the final review's own verdict out of its detail
// (review_rounds.go always says "judged <epic> NOT READY" or "judged <epic>
// READY") rather than carrying a second spelling of the verdict here.
func feedReviewRound(detail string) string {
	if idx := strings.Index(detail, "NOT READY"); idx >= 0 {
		rest := detail[idx+len("NOT READY"):]
		rest = strings.TrimPrefix(strings.TrimSpace(rest), "—")
		return "review says not ready: " + feedClauseOf(rest)
	}
	return "review says ready; re-reviewing since the tree changed"
}

// feedSentenceFor is one feed event's sentence for the dashboard's
// recent-activity section, and whether this section shows it at all: false
// for a stage feedSentences filters (a nil entry) or does not know — a
// stage no test fixture invented, since feedSentenceGuardTest requires every
// stage the feed can actually emit to have an entry.
func feedSentenceFor(event runfeed.Event) (string, bool) {
	build, ok := feedSentences[event.Stage]
	if !ok && strings.HasPrefix(event.Stage, reconcile.StageGitHubErrorPrefix) {
		build, ok = feedSentences[reconcile.StageGitHubErrorPrefix], true
	}
	if !ok || build == nil {
		return "", false
	}
	return build(event.Detail), true
}

// feedTailLineStyled is one dashboardTail line: the clock and the tick's own
// id or try (the same "who" the raw feed line carries), followed by the
// event's readable sentence instead of its stage and detail.
func feedTailLineStyled(event runfeed.Event, sentence string, tries *runfeed.Tries, st watchStyles) string {
	who := dashPad(watchEventWho(event, tries), 12)
	if event.TickID != nil && *event.TickID != "" {
		who = st.cyan(who)
	}
	return fmt.Sprintf("%s %s %s", st.dim(clockOf(event.At)), who, sentence)
}
