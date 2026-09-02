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
// this build has wrong — newline-separated, with the placeholders below, or
// the prompt appended last. It is an escape hatch, not a configuration
// surface: a runner that needs it permanently belongs in the table.
const EnvRunnerArgv = "TICFAC_RUNNER_ARGV"

// The placeholders an argv template may carry. They are substituted per
// ATTEMPT, not per build: the git common directory is a different path in
// every repository, so a table entry that spelled one would be right in
// exactly one checkout.
const (
	promptPlaceholder       = "{{prompt}}"
	gitCommonDirPlaceholder = "{{git_common_dir}}"
)

// runnerDef is one runner's entry: how to launch it headless, and the flag
// that names a model to it.
//
// The model flag lives HERE, beside the argv it belongs to, for the same reason
// the argv does: the runner is the only thing that differs between the three,
// and it differs in exactly one table. ModelFlag empty means this runner cannot
// be told which model to use, which is a refusal when one is routed rather than
// a value quietly dropped — a model recorded as applied and silently not
// applied is provenance that lies.
type runnerDef struct {
	Argv      []string
	ModelFlag string
}

// runners is the headless, full-auto invocation of each runner. The prompt
// is passed as an argument, never on stdin: a runner whose stdin is a pipe
// that never closes is a runner that waits forever.
var runners = map[string]runnerDef{
	// `-p` is claude's headless print mode; the permission mode is what makes
	// it non-interactive rather than blocked on a prompt nobody will answer.
	// `--model <name>` takes an alias (sonnet, opus) or a full model name.
	"claude": {
		Argv:      []string{"claude", "-p", "--permission-mode", "bypassPermissions", promptPlaceholder},
		ModelFlag: "--model",
	},

	// `codex exec` is the non-interactive mode and `-s workspace-write` is its
	// sandbox: writable inside the workspace, and no approval prompt arises,
	// so `-a`/`--ask-for-approval` is the interactive CLI's flag and not this
	// one's. `--dangerously-bypass-approvals-and-sandbox` would also run, and
	// is deliberately not what this table asks for.
	//
	// --add-dir is not optional here, and the reason is this executor's own
	// design: every attempt runs in a LINKED worktree, whose `.git` is a file
	// pointing at the repository's common directory somewhere else entirely.
	// A sandbox rooted at the worktree therefore contains no index, no refs
	// and no object database, and codex can edit files and cannot commit one
	// — which collect reads as `no-commits`, an empty branch that looks
	// exactly like a worker that never did anything.
	// `codex exec` spells the model `-m, --model <MODEL>`.
	"codex": {
		Argv:      []string{"codex", "exec", "-s", "workspace-write", "--add-dir", gitCommonDirPlaceholder, promptPlaceholder},
		ModelFlag: "-m",
	},

	// pi's headless mode is `--print` / `-p`: "process prompt and exit". Its
	// tools are enabled by default in that mode and it has no approval flag to
	// pass — the trust flags it does have (`-a`, `-na`) are about project-local
	// extensions and skills, not about tool use, so this executor does not
	// hand it one.
	// pi spells it `--model <pattern>`, which takes a pattern, a `provider/id`
	// or a bare id.
	"pi": {
		Argv:      []string{"pi", "-p", promptPlaceholder},
		ModelFlag: "--model",
	},
}

// KnownRunners is the closed set, sorted, for a caller that wants to say what
// it accepts.
func KnownRunners() []string {
	out := make([]string, 0, len(runners))
	for name := range runners {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RunnerAcceptsModel reports whether this executor can tell a runner which
// model to use. A caller that routes a model asks BEFORE it dispatches: a
// refusal at construction is a run that did not start, and a refusal at launch
// is a tick already claimed for work nothing will do.
func RunnerAcceptsModel(name string) bool {
	def, ok := runners[name]
	return ok && def.ModelFlag != ""
}

// launch is what one attempt substitutes into its runner's argv template.
type launch struct {
	// Prompt is the rendered worker prompt.
	Prompt string

	// Model is the model the job's PROFILE resolved, empty when none was
	// routed — in which case the runner is launched exactly as it was and
	// chooses its own.
	Model string

	// GitCommonDir is the repository's shared git directory, resolved for THIS
	// attempt's repository. A runner that sandboxes its own file access is
	// given it explicitly, because a linked worktree's git state lives outside
	// the worktree.
	GitCommonDir string
}

// resolveRunner turns a runner name and an optional override into the argv the
// supervisor will exec.
//
// A placeholder whose value is empty is an ERROR rather than an empty
// argument: `--add-dir ""` is a flag that parses, sandboxes nothing, and
// surfaces two steps later as a worker that could not commit.
func resolveRunner(name string, override []string, at launch) ([]string, error) {
	argv := override
	if len(argv) == 0 {
		def, ok := runners[name]
		if !ok {
			return nil, fmt.Errorf("runner %q is not one of %s", name, strings.Join(KnownRunners(), ", "))
		}
		// The model goes in before the prompt is substituted, because the
		// prompt is a POSITIONAL argument to all three of these CLIs and a flag
		// after it would be read as part of it.
		withModelArgv, err := withModel(name, def, at.Model)
		if err != nil {
			return nil, err
		}
		argv = withModelArgv
	}
	// An override is the whole invocation — the escape hatch a build with a
	// runner's flags wrong reaches for — so nothing is inserted into one. The
	// caller who set it owns whether it names a model.

	out := make([]string, 0, len(argv)+1)
	prompted := false
	for _, arg := range argv {
		switch arg {
		case promptPlaceholder:
			out = append(out, at.Prompt)
			prompted = true
		case gitCommonDirPlaceholder:
			if at.GitCommonDir == "" {
				return nil, fmt.Errorf("the %s argv needs %s and this attempt resolved none: "+
					"a linked worktree's git state is outside the worktree, and a runner that cannot reach it "+
					"cannot commit", name, gitCommonDirPlaceholder)
			}
			out = append(out, at.GitCommonDir)
		default:
			out = append(out, arg)
		}
	}
	if !prompted {
		// An argv with no placeholder gets the prompt last, which is what a
		// bare command in Options.RunnerArgv means.
		out = append(out, at.Prompt)
	}
	return out, nil
}

// withModel puts the model in front of the prompt placeholder, or at the end
// of an argv that has none — which is the same position, since a template
// without a placeholder gets the prompt appended last.
//
// A runner with no model flag and a model to apply is refused. Launching it
// anyway would run whatever model that runner defaults to while the attempt
// record, the evidence and the provenance all said something else.
func withModel(name string, def runnerDef, model string) ([]string, error) {
	if model == "" {
		return def.Argv, nil
	}
	if def.ModelFlag == "" {
		return nil, fmt.Errorf("the profile routes model %q to runner %q, and this executor knows no flag that "+
			"tells %s which model to use: launching it anyway would run one model while every record named another",
			model, name, name)
	}
	out := make([]string, 0, len(def.Argv)+2)
	inserted := false
	for _, arg := range def.Argv {
		if arg == promptPlaceholder && !inserted {
			out = append(out, def.ModelFlag, model)
			inserted = true
		}
		out = append(out, arg)
	}
	if !inserted {
		out = append(out, def.ModelFlag, model)
	}
	return out, nil
}
