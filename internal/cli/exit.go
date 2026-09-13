package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
