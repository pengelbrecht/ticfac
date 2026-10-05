package cli

// parseOnly stops a command right after it has parsed and validated its
// arguments, before it touches anything: the seam remedy_test.go holds every
// printed `ticfac …` remedy to the real parser through. It is false in every
// build that ships; only that test sets it, and only serially.
var parseOnly bool

// parseOnlyCommands are the subcommands that honour parseOnly. A remedy the
// run prints for any other subcommand fails remedy_test.go until the
// subcommand learns to stop after parsing too — so a new printed remedy can
// never escape the check by naming a command nobody taught it.
var parseOnlyCommands = map[string]bool{
	"settle": true, "finding": true, "findings": true, "status": true, "events": true,
	"triage": true, "run": true, "factory": true,
}
