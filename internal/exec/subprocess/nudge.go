package subprocess

import (
	"crypto/rand"
	"fmt"
	"os"
	"strings"
)

// The nudge: a runner that exited 0 without its report is prompted again
// before the attempt is judged.
//
// epic-2jn, vqc's resolve job (2026-09-27): claude started the gate as a
// background task, said "I'll wait for its completion notification", and
// ended its turn. In print mode ending the turn ends the process, so the
// runner exited 0 with one commit on its branch and no report, the job was
// judged missing-result, and the run halted on a merge it had nearly made.
// The trigger is claude's (runner.go takes background tasks away from it),
// but the SHAPE is any harness's: a headless worker can end its session
// while it still means to continue, and a clean exit with no report is
// exactly what that looks like from here.
//
// So the supervisor does not settle on that exit. It prompts the SAME session
// again, at most MaxNudges times, saying what is missing and where it goes —
// through the runner's own resume when it has one (claude --resume, pi
// --session-id) and as a fresh run on the same worktree when it has none.
// Only after that is it missing-result. A non-zero exit is not nudged: that
// runner failed, and its own words are what collect classifies.

// MaxNudges bounds how often one attempt is re-prompted. A worker that ends
// without its report twice more after being told what is missing is not
// going to write it, and the wall clock is still the outer bound.
const MaxNudges = 2

// EnvNudge tells a re-prompted runner which nudge this is (1, 2, …). It is
// absent on the first run.
const EnvNudge = "TICFAC_NUDGE"

// nudgeDetailPrefix opens the observation a nudge is recorded as. The
// observation kinds are the job protocol's closed vocabulary, so a nudge is a
// `started` observation — a runner process did start — that says it is one.
const nudgeDetailPrefix = "nudged: "

// IsNudge reports whether an observation records a nudge, for a reader (the
// run's feed) that surfaces them.
func IsNudge(o Observation) bool {
	return o.Kind == ObsStarted && strings.HasPrefix(o.Detail, nudgeDetailPrefix)
}

// HeadlessLine is what every prompt says about the turn ending, because the
// model cannot see that it runs in print mode and every interactive habit it
// has says a background task will call back.
const HeadlessLine = "You run headless: ending your turn ends the job. Run commands in the foreground and wait " +
	"for them; never end your turn while waiting on a background task."

// nudgePrompt is what a runner that resumes its own session is told. It is
// short on purpose: the session still holds the whole job.
func nudgePrompt(record *attemptRecord) string {
	return NudgePrompt(record.Branch, record.ResultPath)
}

// NudgePrompt is the re-prompt every executor sends a worker that ended its
// turn without its report — the herdr executor types it into the agent's own
// pane (herdr/nudge.go).
func NudgePrompt(branch, resultPath string) string {
	return fmt.Sprintf("You ended your turn without writing your report. %s\n\n"+
		"Finish the work you were doing: if you were waiting on a command, run it again in the foreground and "+
		"wait for it. Commit on %s, then write your report to this exact absolute path, ending with its STATUS "+
		"line:\n\n    %s\n", HeadlessLine, branch, resultPath)
}

// NudgeDetail is the observation a nudge is recorded as, the same sentence on
// every executor so the feed reads one shape: nudge n of MaxNudges, what was
// missing, and how the worker was re-prompted.
func NudgeDetail(n int, what, resultPath, how string) string {
	return fmt.Sprintf("%snudge %d of %d: %s without writing its report at %s, and a headless worker that ends "+
		"its turn early ends the job; %s", nudgeDetailPrefix, n, MaxNudges, what, resultPath, how)
}

// freshNudgeSection is appended to the full prompt for a runner with no
// session to resume: the new process starts blind, so it is told a run
// before it already worked here.
func freshNudgeSection(record *attemptRecord) string {
	return fmt.Sprintf("\n## This job already ran once on this worktree\n\n"+
		"A run before you ended without writing its report. Whatever it committed is on %s already: read "+
		"`git log` and the worktree, finish the job, and write the report to %s as described above.\n",
		record.Branch, record.ResultPath)
}

// nudgeArgv is the argv that re-prompts this attempt's runner. It resumes the
// runner's own session when the attempt has one, and otherwise repeats the
// launch with the fresh-run section appended to the prompt — which is also
// what an override argv (the tests' fake runner, TICFAC_RUNNER_ARGV) gets,
// since an override is the whole invocation and nothing is inserted into it.
func nudgeArgv(name string, override []string, at launch, record *attemptRecord) ([]string, error) {
	if at.Session != "" && len(override) == 0 {
		resume := at
		resume.Resume = true
		resume.Prompt = nudgePrompt(record)
		return resolveRunner(name, nil, resume)
	}
	fresh := at
	fresh.Session = ""
	fresh.Prompt = at.Prompt + freshNudgeSection(record)
	return resolveRunner(name, override, fresh)
}

// sessionFor names a new session for a runner that takes one, and answers
// empty for one that does not — or for an override, which is the whole
// invocation.
func sessionFor(name string, override []string) (string, error) {
	def, ok := runners[name]
	if !ok || len(override) > 0 || len(def.SessionStart) == 0 {
		return "", nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// runnerDefEnv is the runner's own environment from the table. It applies to
// an override too: an override replaces the argv of the same CLI, and the
// setting is about that CLI, not about its flags.
func runnerDefEnv(name string) []string {
	return append([]string{}, runners[name].Env...)
}

// insertBeforePrompt puts flags in front of the prompt placeholder, or at the
// end of an argv without one — the same position, since such an argv gets the
// prompt appended last.
func insertBeforePrompt(argv, flags []string) []string {
	if len(flags) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+len(flags))
	inserted := false
	for _, arg := range argv {
		if arg == promptPlaceholder && !inserted {
			out = append(out, flags...)
			inserted = true
		}
		out = append(out, arg)
	}
	if !inserted {
		out = append(out, flags...)
	}
	return out
}

// reportWritten is whether the attempt's report is readable with a status
// line: the one thing a nudge asks for, so the one thing that stops them.
func reportWritten(record *attemptRecord) bool {
	report, ok := readAttemptReport(record)
	return ok && report.Status != ""
}

// nudgeDue is the supervisor's decision after a runner exits: prompt it again,
// or settle. Every condition is a reason the missing report is NOT the
// worker stopping early — or a bound already spent.
func nudgeDue(st *store, record *attemptRecord, code, nudged int) (bool, string) {
	switch {
	case len(record.NudgeArgv) == 0:
		return false, "this attempt carries no nudge argv"
	case code != 0:
		return false, "the runner failed"
	case nudged >= MaxNudges:
		return false, "the nudges are spent"
	case st.wallClockExceeded():
		return false, "the wall clock stopped it"
	case reportWritten(record):
		return false, "the report is written"
	}
	if _, cancelled := st.cancelled(); cancelled {
		return false, "the attempt is cancelled"
	}
	if _, err := os.Stat(record.Worktree); err != nil {
		return false, "the worktree is gone"
	}
	return true, ""
}
