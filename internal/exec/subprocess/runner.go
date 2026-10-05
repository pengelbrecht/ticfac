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
	sessionPlaceholder      = "{{session}}"
	harnessDirPlaceholder   = "{{harness_dir}}"
	stateDirPlaceholder     = "{{state_dir}}"
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

	// SessionStart and SessionResume are how this runner is given a session
	// of the executor's choosing, and how that same session is prompted again
	// after its process has exited (the nudge, nudge.go). Each is inserted in
	// front of the prompt with sessionPlaceholder substituted. Empty means the
	// runner has no session this executor can name, and a nudge is a fresh
	// run on the same worktree instead.
	SessionStart  []string
	SessionResume []string

	// DurableResume names a runner whose conversation survives its process
	// in storage of the attempt's own (epic 43y, tick hpk): a re-prompt is
	// the SAME argv with a different message, and the relaunched process
	// reopens the conversation from where the last one left it — no session
	// flag to insert, and no "this job already ran once" section to append,
	// because the relaunched runner reads the whole history from its
	// storage rather than starting blind on the worktree.
	DurableResume bool

	// Env is what this runner is launched with beyond the job's own
	// variables: settings that belong to the CLI, not to the job.
	Env []string
}

// runners is the headless, full-auto invocation of each runner. The prompt
// is passed as an argument, never on stdin: a runner whose stdin is a pipe
// that never closes is a runner that waits forever.
var runners = map[string]runnerDef{
	// `-p` is claude's headless print mode; the permission mode is what makes
	// it non-interactive rather than blocked on a prompt nobody will answer.
	// `--model <name>` takes an alias (sonnet, opus) or a full model name.
	//
	// Print mode ends the process when the model ends its turn, so a claude
	// that starts the gate as a BACKGROUND task and ends its turn "to wait for
	// the notification" exits 0 with nothing reported (epic-2jn vqc, the
	// resolve job of 2026-09-27). CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1 is
	// Claude Code's documented switch for exactly that: it disables "all
	// background task functionality, including the run_in_background
	// parameter on Bash and subagent tools, auto-backgrounding, and the
	// Ctrl+B shortcut" (code.claude.com/docs/en/env-vars). Verified locally on
	// claude 2.1.283: with it set, the Bash tool no longer offers
	// run_in_background at all.
	//
	// `--session-id <uuid>` names the session up front and `--resume <uuid>`
	// prompts it again in print mode with its whole history, both verified on
	// 2.1.283: that is what the nudge re-prompts through.
	"claude": {
		Argv:          []string{"claude", "-p", "--permission-mode", "bypassPermissions", promptPlaceholder},
		ModelFlag:     "--model",
		SessionStart:  []string{"--session-id", sessionPlaceholder},
		SessionResume: []string{"--resume", sessionPlaceholder},
		Env:           []string{"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1"},
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

	// `pi` IS the pi-durable Node harness since epic 43y (tick hpk): the
	// operator's decision — all-in, one harness, no flag — so a local run's
	// pi-kind workers run on the durable host and there is no parallel local
	// pi-CLI path. What the argv names is the harness package's own entry
	// (harness/src/local/main.ts), run from source under plain `node` through
	// the register shim (harness/runtime/register.mjs) — the harness is
	// package source in the repository this run works on, and the runner
	// table is the one place a future packaging change (an embedded bundle,
	// say) would land.
	//
	// `--experimental-strip-types` because type stripping is flagged on the
	// Node this tree's CI pins (22) and default only from 23.6; passing it
	// explicitly is a no-op where it is already default.
	//
	// `--config` names the worker.json the executor writes beside the attempt
	// record — storage, worktree, branch, report, steer socket, the wall
	// deadline: the WHOLE per-attempt interface, so the argv stays short and
	// the attempt record's RunnerArgv reads as what it is. `--message` is the
	// input: the job prompt for the attempt's first process, the supervisor's
	// follow-up text (nudge, report pushback, stuck re-prompt) for a relaunch
	// — the relaunch reopens the same local SQLite storage and continues the
	// same conversation, which is this runner's "session".
	//
	// Model credentials are the host's own environment (CLOUDFLARE_API_KEY /
	// CLOUDFLARE_ACCOUNT_ID, resolved by pi-ai the same way the deleted pi CLI
	// resolved them); a container's gateway token is the cloud host's, not
	// this one.
	//
	// The pi CLI is not a runner (epic 43y, tick jhp): the image ships no pi
	// binary and no profile routes to one. TICFAC_RUNNER_ARGV remains ONLY as
	// the cross-process seam the e2e tests use to inject their fake runners
	// into the real executor binary — nothing in the repository points it at
	// the pi CLI, and the table is the one place a future runner belongs.
	"pi": {
		Argv: []string{
			"node", "--experimental-strip-types",
			"--import", harnessDirPlaceholder + "/runtime/register.mjs",
			harnessDirPlaceholder + "/src/local/main.ts",
			"--config", stateDirPlaceholder + "/worker.json",
			"--message", promptPlaceholder,
		},
		ModelFlag:     "--model",
		DurableResume: true,
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

	// HarnessDir is the harness package this runner runs from, resolved from
	// the checkout the executor works against (or $TICFAC_HARNESS_DIR): the
	// pi-durable host is package source in the repository, and the argv points
	// at the entry and register shim inside it.
	HarnessDir string

	// StateDir is the attempt's state directory, where worker.json, the
	// conversation's SQLite storage and the steer socket live.
	StateDir string

	// Session is the runner session this attempt runs in, empty for a runner
	// with none this executor can name. Resume says the argv prompts that
	// session again rather than starting it (the nudge, nudge.go).
	Session string
	Resume  bool
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
		// The session flags go in front of the prompt for the same reason
		// the model flag does, below.
		if at.Session != "" {
			flags := def.SessionStart
			if at.Resume {
				flags = def.SessionResume
			}
			def.Argv = insertBeforePrompt(def.Argv, flags)
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
		case sessionPlaceholder:
			if at.Session == "" {
				return nil, fmt.Errorf("the %s argv needs %s and this attempt named no session", name, sessionPlaceholder)
			}
			out = append(out, at.Session)
		default:
			// The harness and state directories reach the argv as PATH
			// PREFIXES ({{harness_dir}}/runtime/register.mjs), not whole
			// arguments, so they are substituted wherever they appear in an
			// argument — and a placeholder whose value this attempt resolved
			// none of is a refusal, not an argv that would fail in a subprocess
			// two steps later.
			for _, ph := range []struct{ placeholder, value, missing string }{
				{
					harnessDirPlaceholder, at.HarnessDir,
					"the pi-durable harness runs from the repository's harness/ package — the checkout this " +
						"executor works against has none, and $TICFAC_HARNESS_DIR names none",
				},
				{stateDirPlaceholder, at.StateDir, "this attempt resolved no state directory"},
			} {
				if !strings.Contains(arg, ph.placeholder) {
					continue
				}
				if ph.value == "" {
					return nil, fmt.Errorf("the %s argv needs %s and this attempt resolved none: %s", name, ph.placeholder, ph.missing)
				}
				arg = strings.ReplaceAll(arg, ph.placeholder, ph.value)
			}
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
	// The model flag goes in FRONT of the prompt's place. For a CLI the
	// prompt is a positional argument, so "in front of it" is directly
	// before it; for a runner that takes the prompt as a FLAG'S VALUE —
	// `--message {{prompt}}` — "directly before it" would land BETWEEN the
	// flag and its value, so the insertion point moves back to the flag
	// instead. Either shape puts the model before the prompt, and no
	// runner's argv is special-cased.
	before := len(def.Argv)
	for i, arg := range def.Argv {
		if arg == promptPlaceholder {
			before = i
			if i > 0 && strings.HasPrefix(def.Argv[i-1], "-") {
				before = i - 1
			}
			break
		}
	}
	for i, arg := range def.Argv {
		if i == before && !inserted {
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
