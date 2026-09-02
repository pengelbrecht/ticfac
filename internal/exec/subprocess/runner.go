package subprocess

import (
	"fmt"
	"sort"
	"strings"
)

// The runners this executor knows how to launch headless.
//
// SPEC §12 Phase 1 step 3: claude, codex and pi are RUNNERS on one
// worktree-per-attempt executor, which is why Appendix A is tested once here
// rather than once per runner — the runner is the only thing that differs, and
// it differs in exactly one place: this table.
//
// The runner is NOT a JobSpec field. The protocol's records are closed, and a
// field invented on this side would be a field the reconciler ignores; which
// agent CLI is installed on this machine is a property of the host, so it is
// configuration (Options.Runner, `--runner`).

// EnvRunnerArgv overrides the argv table for a runner whose headless flags
// this build has wrong — newline-separated, the prompt marked by
// promptPlaceholder or appended last. It is an escape hatch, not a
// configuration surface: a runner that needs it permanently belongs in the
// table above.
const EnvRunnerArgv = "TICFAC_RUNNER_ARGV"

// promptPlaceholder is where the rendered worker prompt goes in a runner's
// argv. It is a placeholder rather than a trailing append because not every
// CLI takes the prompt last.
const promptPlaceholder = "{{prompt}}"

// runnerArgv is the headless, full-auto invocation of each runner. The prompt
// is passed as an argument, never on stdin: a runner whose stdin is a pipe
// that never closes is a runner that waits forever.
var runnerArgv = map[string][]string{
	// `-p` is claude's headless print mode; the permission mode is what makes
	// it non-interactive rather than blocked on a prompt nobody will answer.
	"claude": {"claude", "-p", "--permission-mode", "bypassPermissions", promptPlaceholder},

	// `codex exec` is the non-interactive mode; `--full-auto` is its
	// low-friction sandboxed execution.
	"codex": {"codex", "exec", "--full-auto", promptPlaceholder},

	// pi's headless flag is the one of the three this repository has not yet
	// run for itself. Override it with Options.RunnerArgv until the live test
	// has confirmed it.
	"pi": {"pi", "-p", promptPlaceholder},
}

// KnownRunners is the closed set, sorted, for a caller that wants to say what
// it accepts.
func KnownRunners() []string {
	out := make([]string, 0, len(runnerArgv))
	for name := range runnerArgv {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// resolveRunner turns a runner name and an optional override into the argv the
// supervisor will exec, with the prompt substituted in.
func resolveRunner(name string, override []string, prompt string) ([]string, error) {
	argv := override
	if len(argv) == 0 {
		known, ok := runnerArgv[name]
		if !ok {
			return nil, fmt.Errorf("runner %q is not one of %s", name, strings.Join(KnownRunners(), ", "))
		}
		argv = known
	}
	out := make([]string, 0, len(argv)+1)
	substituted := false
	for _, arg := range argv {
		if arg == promptPlaceholder {
			out = append(out, prompt)
			substituted = true
			continue
		}
		out = append(out, arg)
	}
	if !substituted {
		// An argv with no placeholder gets the prompt last, which is what a
		// bare command in Options.RunnerArgv means.
		out = append(out, prompt)
	}
	return out, nil
}
