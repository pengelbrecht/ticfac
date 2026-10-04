package statusmodel

import (
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The one command that clears a stop, spelled once here so every renderer —
// the watch, the bare `ticfac` listing, a phone page, an agent's JSON —
// answers with the same verb. The model is the one source of what a run is
// waiting on; these are the other half of that sentence, the imperative a
// person or an agent types next. A command spelled in two places is two
// commands the first time only one of them changes (the finding this file
// answers: every clearing command named the wrong verb).

// ResumeCommand is the one command that resumes a stopped run, named by the
// host the run lives on. A LOCAL run's resume is the foreground form the
// reconciler itself runs (`run-epic` — `ticfac run` starts the same command
// in the background and attaches to it). A CLOUD run's resume is a NEW
// SUBMISSION to its factory — `ticfac run <epic> --cloud` — because nothing
// on the machine reading the model can restart the factory's Workflow
// except the factory, and `run-epic` here would restart the epic LOCALLY,
// in the foreground, on whatever machine happens to be reading: the same
// stop, a different run.
func ResumeCommand(host, epicID string) string {
	if host == HostCloud {
		return fmt.Sprintf("ticfac run %s --cloud", epicID)
	}
	return fmt.Sprintf("ticfac run-epic %s", epicID)
}

// TriageCommand is the one command that settles a run's untriaged findings —
// the close-out's findings gate and the run_held line it raises are both
// cleared by it, addressed by the epic the findings belong to. It is NOT
// `ticfac findings`, which lists the drafts and settles nothing, and it is
// NOT `ticfac settle`, which releases a held ATTEMPT and refuses a hold
// that holds no attempt.
func TriageCommand(epicID string) string {
	return fmt.Sprintf("ticfac triage %s", epicID)
}

// TriageCommandForRun is the triage command addressed to ONE run's findings:
// the drafts live in the run's own records, so a hold an earlier run left is
// cleared by triaging THAT run's store — the run id the bare command's
// default (epic-<epic-id>) spells is only right when the holding run wrote
// under that spelling.
func TriageCommandForRun(epicID, runID string) string {
	return fmt.Sprintf("ticfac triage %s --run-id %s", epicID, runID)
}

// TriageCommandForCurrentRun is the triage command for a hold the run the
// model answers for left. It names the run whenever that run's id is NOT
// the epic spelling (epic-<epic-id>): triage without --run-id defaults to
// that spelling — the local id run-epic derives — which is the right
// address for a run under it, and a refused one for a run whose drafts live
// under another id: today's cloud runs, whose orchestrator container execs
// `run-epic --run-id` the factory's run_<hex> (tick ulw), so their untriaged
// findings settle in their own store, never the one the bare command's
// default spells (tick q8m). The empty run id says the caller does not know
// it; either way the flag is left off and the spelling every local run's
// needs-you has always carried stands.
func TriageCommandForCurrentRun(epicID, runID string) string {
	if runID == "" || runID == "epic-"+epicID {
		return TriageCommand(epicID)
	}
	return TriageCommandForRun(epicID, runID)
}

// SettleCommandForCurrentRun is the settle command for a hold the run the
// model answers for left. It names the run whenever that run's id is NOT
// the epic spelling (epic-<epic-id>): settle without --run-id defaults to
// that spelling, which is the right address for a run under it — and a
// refused address for a run whose records live under another id, today's
// cloud runs, whose orchestrator container execs `run-epic --run-id` the
// factory's run_<hex> (tick ulw). The empty run id says the caller does
// not know it; either way the flag is left off and the spelling every
// local run's needs-you has always carried stands.
//
// The rule is the reconciler's own (reconcile.SettleReleaseCommand, tick
// qxj), read here rather than mirrored: the model and the run's refusals
// spell one command, not two.
func SettleCommandForCurrentRun(epicID, tickID string, attempt int, runID string) string {
	return reconcile.SettleReleaseCommand(epicID, tickID, attempt, runID)
}

// HoldReason is the refusal reason a run_held line's detail leads with.
// Every StageRunHeld site writes the line one shape — `<reason>: <message>`,
// the refusal's own closed vocabulary — so the reason is a field of the
// line, not prose to parse: the prefix before the first colon. The
// surfaces that decide what a hold is CLEARED BY (HoldClearingCommand) and
// the wordings that explain it read the same prefix, so one hold kind is one
// decision everywhere it is named. An empty answer is a detail that names
// no reason — a line no closed set knows.
func HoldReason(detail string) string {
	if i := strings.IndexByte(detail, ':'); i > 0 {
		return detail[:i]
	}
	return ""
}

// HoldClearingCommand is the one command that moves one run_held line's
// hold on, decided by WHAT the run holds — the refusal reason the line's
// detail leads with (HoldReason) — and never by the line's shape alone
// (tick gf0). The holds a run raises for a person are not cleared by one
// verb:
//
//   - finding_untriaged, the close-out's findings gate, is cleared by the
//     triage addressed to the holding run's own store: settle releases an
//     attempt, and this hold holds a person's decision about findings.
//
//   - absorption_depth_exceeded is cleared by the same triage: the finding
//     the bound refused to absorb is still a person's to decide — absorb,
//     file, fixed or discard — and the refusal's own message names the
//     other road, raising the bound with --absorption-depth and running the
//     epic again. The reason decides, never the attempt: even a line that
//     names one is not released by it — the reporting tick's work is not
//     what the bound stopped.
//
//   - claim_width and foreign_claim are cleared by the RUN AGAIN, addressed
//     by the host the run lives on: they are facts about the world that end
//     when the other holder's tick closes or a slot frees — never by a
//     release — and a resumed run re-derives from the graph and proceeds
//     the moment they do. They are raised before the tick's first dispatch,
//     so no attempt stands behind them and a settle addressed by one
//     refuses ("-" is not an attempt number).
//
//   - every other hold names an attempt the run dispatched, and the release
//     a person types is the settle command that attempt addresses.
//
// A hold the closed set does not know — no reason it recognises and no
// attempt behind it — answers nil: no command is better than a wrong one a
// person copies, and the dash settle this decision replaced was exactly
// that.
//
// storeRunID is the id the HOLDING run's own store lives at — the triage's
// address, where the run's drafts were written; runID is the id the surface
// answers for — the settle's address, the run whose dispatch the attempt
// number counts. They differ only for a run read under one id whose records
// were written under another (tick q8m). Both are per hold: a prior run's
// hold is cleared by commands addressed to THAT run, never the one the
// model answers for.
func HoldClearingCommand(epicID, host, storeRunID, runID string, held runfeed.Event) *string {
	switch HoldReason(held.Detail) {
	case reconcile.RefusedFindingUntriaged, reconcile.RefusedAbsorptionDepth:
		command := TriageCommandForCurrentRun(epicID, storeRunID)
		return &command
	case reconcile.RefusedClaimWidth, reconcile.RefusedForeignClaim:
		command := ResumeCommand(host, epicID)
		return &command
	}
	if held.TickID != nil && held.Attempt != nil {
		command := SettleCommandForCurrentRun(epicID, *held.TickID, *held.Attempt, runID)
		return &command
	}
	return nil
}
