package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The exit codes the cloud and factory commands share with ticks' tk, so a
// script written against `tk cloud …` keeps its branching when it moves to
// `ticfac cloud …` (the move this code is: ticks' cmd/tk/cmd/cloud*.go,
// factory.go's status half and factory_dashboard.go, ported from cobra to this
// package's plain flag sets with bodies otherwise verbatim).
//
// 2 is also this package's usage code and the reconciler's ExitNoExecutor;
// those collisions are inherited from tk, where usage and "no executor" were
// already both 2, and no caller branches on the difference.
const (
	// exitSuccess is a command that did its work.
	exitSuccess = 0
	// exitGeneric is a failure that is not a usage mistake and not a
	// not-found.
	exitGeneric = 1
	// exitUsage is a malformed invocation: wrong flags, wrong argument count.
	exitUsage = 2
	// exitNoRepo is "not in a git repository" — tk's code 3, kept because an
	// orchestrator branching on it must not retry it as a generic failure.
	// Only the tk-ported family (cloud, factory, skills install) exits it;
	// the run surfaces' 3 is ExitHeld below, and the collision is the
	// documented one: no caller branches on 3 across the two families.
	exitNoRepo = 3
	// exitNotFound is a lookup that honestly came back empty: a missing epic,
	// a missing tick. tk's code 4.
	exitNotFound = 4
	// exitRunning is the work's own answer: the command ended while the run
	// is still in flight (tick 8v3). `ticfac run` detached with the run
	// going exits it — an agent that branched on 0 would read "done" where
	// the epic is still working, which is exactly the conflation the exit
	// table exists to remove. tk has no code 5, so nothing tk-shaped reads
	// it by accident.
	exitRunning = 5
	// exitIO is an unreadable local file the command needs: tk's code 6.
	exitIO = 6
	// exitCancelled is a run that was stopped deliberately before it
	// finished (tick rix, the cancelled sibling of the failed class tick
	// bot gave epic 2jn's A4): its own terminal line names the stop and
	// why, and a watcher that read it as done/0 answered "finished epic"
	// for a run a person stopped. Nothing is held and nothing needs a fix —
	// the work is simply neither done nor failed, and an agent must be able
	// to branch on that without parsing the line. tk has no code 7, so
	// nothing tk-shaped reads it by accident.
	exitCancelled = 7
)

// exitError carries the code a refusal exits with, the way tk's NewExitError
// did. Errors that are not exitErrors are environment faults and exit 1.
type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

// newExitError is tk's NewExitError with the same shape.
func newExitError(code int, format string, args ...any) error {
	return &exitError{code: code, message: fmt.Sprintf(format, args...)}
}

// exitCodeOf reports the process code an error from a command maps to. A nil
// error is success; an error nobody gave a code is generic, because silently
// exiting 0 on an error is the failure class this exists to prevent.
// printedExit is a body that said its own refusal and carries only its code.
func exitCodeOf(err error) int {
	if err == nil {
		return exitSuccess
	}
	var printed *printedExit
	if errors.As(err, &printed) {
		return printed.code
	}
	if exitErr, ok := err.(*exitError); ok {
		return exitErr.code
	}
	return exitGeneric
}

// reportCommand turns a command body's error into the printed refusal and the
// process code. --help on a flag set is success: the flag package has already
// written the defaults to the command's error output.
func reportCommand(name string, err error, stderr io.Writer) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitSuccess
	}
	if err != nil {
		fmt.Fprintf(stderr, "ticfac %s: %v\n", name, err)
		return exitCodeOf(err)
	}
	return exitSuccess
}

// resultExitCode maps a `run-epic` result to the process code, because one of
// its failures is a verdict a caller can branch on (ticfac tick rf3).
//
// A run that stopped because the epic does not exist on the submitted tree
// exits NOT-FOUND — tk's code 4 — which is the code the factory's Run Workflow
// reads as a TERMINAL configuration verdict (TERMINAL_EXIT_CODES,
// cloudflare/src/sandbox.ts) and answers by refusing to reboot the container:
// the tracker's tree is cut from the submitted commit, so the epic is missing
// on every boot, and the first per-tick Cloudflare smoke run re-booted into
// that identical failure until a person stopped it by hand. Any other stop
// stays generic: a merge conflict, a gate that did not pass and a worker that
// answered BLOCKED are all repairs a person makes that a reboot may then
// adopt.
func resultExitCode(result *reconcile.Result) int {
	if result == nil {
		return exitGeneric
	}
	if result.State == "completed" {
		return exitSuccess
	}
	// A cancelled result is the cancelled class (tick rix): the resume path
	// replays an already-terminal checkpoint as a Result, so a cancelled
	// run's replay must answer the same word and code the watch answers —
	// never the generic 1, which names a fix for a run nobody needs to fix.
	if result.State == runstate.StateCancelled {
		return exitCancelled
	}
	if result.Failure != nil && result.Failure.Reason == reconcile.RefusedEpicAbsent {
		return exitNotFound
	}
	return exitGeneric
}

// ExitTable is the documented exit code set (tick 8v3): the codes every
// ticfac command exits with, as data, so the README's table and the code's
// codes are pinned to one authority by a test (exittable_test.go) rather
// than kept in step by hand. A command may exit only a code this table
// names; a code nobody documents is a contract nobody can branch on.
//
// The classes the tick names — done, running, held-for-a-person, failed
// and usage — each have their own code, so an agent distinguishes them
// without parsing prose; cancelled (tick rix) is the sixth class, for the
// same reason on the same terms. The held class carries its REASON CLASS in
// the refusal line and in every --json document (the refusal or wait kind,
// e.g. finding_untriaged, closeout_ci_failed, merge — never prose).
type ExitTableEntry struct {
	Code    int
	Name    string
	Meaning string
}

// ExitTable is ordered by code. Two meanings share one code deliberately,
// each named where it is: 3 is held on the run surfaces and not-in-a-repo
// in the tk-ported family — a collision inherited from tk (usage and
// ExitNoExecutor were already both 2 there) and documented here rather
// than papered over, because no caller branches on 3 across the families.
var ExitTable = []ExitTableEntry{
	{exitSuccess, "done",
		"the command did its work"},
	{exitGeneric, "failed",
		"a failure that is not a usage mistake — a refused action, an unreadable store, a run that stopped needing a person (the refusal names the reason class), a run whose own terminal line says it failed (watch, run: the line names what did not pass)"},
	{exitUsage, "usage",
		"a malformed invocation: wrong flags, wrong argument count, a refusal to guess"},
	{ExitHeld, "held",
		"the run ended holding something only a person can move (watch, run): the reason class is the wait kind in the line and the --json document; in the cloud, factory and skills family this code keeps tk's meaning, not inside a git repository"},
	{exitNotFound, "missing",
		"a lookup that honestly came back empty: a missing epic, a missing tick"},
	{exitRunning, "running",
		"the command ended while the run is still in flight: `ticfac run` detached with the run going, a watch interrupted on a live run — the work continues, nothing is wrong"},
	{exitIO, "io",
		"an unreadable local file the command needs"},
	{exitCancelled, "cancelled",
		"a run that was stopped deliberately before it finished (watch, run): its own terminal line names the stop and why — the work is neither done nor failed, and nothing is held for a person"},
}

// The two documented exceptions to "the exit code is the command's":
//
//  - `ticfac status` exits the RUN's answer, not the command's: 0 the run is
//    alive, 1 it is not. The command did its work either way; a script
//    asking "is it alive" branches on the run, and that contract (tick
//    6dh: "the exit code stays liveness's alone") predates the table and
//    is pinned by its tests.
//  - Signals: a run-epic killed by SIGINT/SIGTERM exits 130/143, the
//    shell's convention, not the table's.
