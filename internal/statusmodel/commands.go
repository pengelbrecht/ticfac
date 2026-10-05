package statusmodel

import "fmt"

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

// AmendmentsCommand is the one command that settles a run's unconfirmed
// epic amendments — the close-out's amendments gate and the run_held line
// it raises (tick 7sn) are both cleared through it, addressed by the epic
// whose record was amended. It names the LISTING, which teaches the settle
// command beside each amendment's short key prefix, for the same reason
// TriageCommand does: a command that sends the operator to type the 64-hex
// key by hand is the friction the surface exists to remove.
func AmendmentsCommand(epicID string) string {
	return fmt.Sprintf("ticfac amendments %s", epicID)
}
