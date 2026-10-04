package statusmodel

import (
	"fmt"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
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

// SettleCommand is the one command that releases a held attempt: the epic,
// tick and run-wide attempt number it is addressed by — and, when the
// holding run is NOT the run the model answers for, the --run-id that names
// it, because attempt numbers are per run and the default a settle without
// the flag uses (epic-<epic-id>) cannot name another run's dispatch. The
// empty runID is the holding run's own word, spelled as every renderer has
// spelled it so far.
func SettleCommand(epicID, tickID string, attempt int, runID string) string {
	if runID == "" {
		return fmt.Sprintf("ticfac settle %s %s %d --release \"<who>\"", epicID, tickID, attempt)
	}
	return fmt.Sprintf("ticfac settle %s %s %d --run-id %s --release \"<who>\"", epicID, tickID, attempt, runID)
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
