package subprocess

import (
	"os"
	"path/filepath"
	"strings"
)

// The report pushback, the local executor's half (tick 4m6; the herdr
// executor's is herdr/pushback.go).
//
// A runner that exited 0 WITH a report whose report check (lint.go) fails is
// re-prompted in its own session with the checker's errors — the same path
// the report nudge (nudge.go, #78) takes: the runner's own resume where it has
// one, a fresh run on the same worktree where it has none — at most
// MaxLintPushbacks times. After that the attempt settles and collect decides:
// a report with FATAL problems still left collects as missing-result (retried
// like any attempt that never said what it did); one with only repairable
// problems is accepted as read.
//
// The argv is rendered at Start like the nudge's, with a placeholder where the
// errors go: the supervisor that uses it has no executor options, and the
// errors exist only once the report does.

// lintErrorsPlaceholder is replaced in the pushback argv by the checker's
// errors. It is not a string a prompt or a path can contain by accident.
const lintErrorsPlaceholder = "\x00TICFAC_LINT_ERRORS\x00"

// lintPushbackArgv is the argv that pushes a failing report back to this
// attempt's runner, with the errors still a placeholder.
func lintPushbackArgv(name string, override []string, at launch, record *attemptRecord) ([]string, error) {
	text := LintPushbackPrompt(record.ResultPath, record.LintCommand, lintErrorsPlaceholder)
	if at.Session != "" && len(override) == 0 {
		resume := at
		resume.Resume = true
		resume.Prompt = text
		return resolveRunner(name, nil, resume)
	}
	fresh := at
	fresh.Session = ""
	fresh.Prompt = at.Prompt + "\n## This job already ran once on this worktree\n\n" +
		"A run before you wrote its report and ended; its work is committed on " + record.Branch +
		" already. Do not redo it.\n\n" + text
	return resolveRunner(name, override, fresh)
}

// withLintErrors fills the placeholder in a pushback argv.
func withLintErrors(argv []string, errors string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		out[i] = strings.ReplaceAll(arg, lintErrorsPlaceholder, errors)
	}
	return out
}

// lintAttemptReport runs the report check over the attempt's report as it
// stands in the worktree, with the context the worktree gives.
func lintAttemptReport(record *attemptRecord) (LintResult, bool) {
	raw, err := os.ReadFile(record.ResultPath)
	if err != nil {
		return LintResult{}, false
	}
	return LintReport(string(raw), LoadLintContext(record.Worktree, record.Spec.Role, record.TickID)), true
}

// lintPushbackDue is the supervisor's decision after a runner exits with its
// report written: push the report back, or settle. Every condition is a
// reason a pushback cannot help — or a bound already spent.
func lintPushbackDue(st *store, record *attemptRecord, code, pushed int) (LintResult, bool, string) {
	switch {
	case len(record.LintArgv) == 0:
		return LintResult{}, false, "this attempt carries no pushback argv"
	case code != 0:
		return LintResult{}, false, "the runner failed"
	case pushed >= MaxLintPushbacks:
		return LintResult{}, false, "the pushbacks are spent"
	case st.wallClockExceeded():
		return LintResult{}, false, "the wall clock stopped it"
	}
	if _, cancelled := st.cancelled(); cancelled {
		return LintResult{}, false, "the attempt is cancelled"
	}
	if _, err := os.Stat(record.Worktree); err != nil {
		return LintResult{}, false, "the worktree is gone"
	}
	result, ok := lintAttemptReport(record)
	switch {
	case !ok:
		return LintResult{}, false, "there is no report to check"
	case result.Clean():
		return result, false, "the report passes the check"
	}
	return result, true, ""
}

// LintBinary is the report checker's path for a prompt rendered by a process
// that is not the checker itself (the herdr executor runs inside ticfac): the
// ticfac-exec-subprocess beside this executable when there is one, else the
// bare name for PATH to resolve — which is how the sandbox image ships it.
func LintBinary() string {
	self, err := os.Executable()
	if err != nil {
		return LintCommandName
	}
	if filepath.Base(self) == LintCommandName {
		return self
	}
	beside := filepath.Join(filepath.Dir(self), LintCommandName)
	if info, err := os.Stat(beside); err == nil && !info.IsDir() {
		return beside
	}
	return LintCommandName
}
