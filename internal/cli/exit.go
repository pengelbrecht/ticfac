package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
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
	exitNoRepo = 3
	// exitNotFound is a lookup that honestly came back empty: a missing epic,
	// a missing tick. tk's code 4.
	exitNotFound = 4
	// exitIO is an unreadable local file the command needs: tk's code 6.
	exitIO = 6
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
func exitCodeOf(err error) int {
	if err == nil {
		return exitSuccess
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
	if result.Failure != nil && result.Failure.Reason == reconcile.RefusedEpicAbsent {
		return exitNotFound
	}
	return exitGeneric
}
