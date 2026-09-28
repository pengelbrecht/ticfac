package cli

// The command tree (tick nwj): every ticfac command runs on cobra — the
// framework tk already uses, so the two tools behave alike — and the tree is
// executed through fang, which wraps it with styled help and errors, a
// --version flag fed from the build, a completion command and man pages, all
// DERIVED from the one tree rather than hand-rolled beside it. That one tree
// is the foundation the watch (89m) and skills/MCP surfaces derive from too.
//
// fang is marked experimental upstream; it is pinned in go.mod and kept at
// arm's length here by a thin seam: fang is called from exactly one place
// (Run), commands communicate with it through three small types
// (printedExit, exitError and the flag-error func), and dropping the wrapper
// is deleting the one call plus those helpers — the command bodies, their
// flags, their exit codes and their output contracts are untouched by it.
//
// The bodies keep their own refusals byte-for-byte: a command that has always
// printed "ticfac <name>: <reason>" to stderr and exited N still does, and it
// returns a printedExit so fang's styled error does not say the same thing
// twice. What fang styles is what cobra reports before a body ever runs — an
// unknown command, an unknown flag, a malformed argument count — and fang
// still renders those through a colorprofile writer, so anything that is not
// a terminal (a test buffer, a piped script, CI) sees exactly the words.

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
)

// Run executes one invocation and returns the process exit code. Everything
// is passed in rather than reached for, so the behaviour under test is the
// behaviour that ships: the tree is built fresh per invocation (no package
// state for flag values to survive between calls in one process, which is
// the trap tk's ResetFlags papered over), the writers are the caller's, and
// the exit code is the contract a script branches on.
func Run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}

// runContext is Run under a parent context: a test that leaves a
// subscription open (`events --follow`) ends it by cancelling the parent,
// and waits for it, instead of leaking a goroutine that outlives the test
// and races the next test's seams.
func runContext(parent context.Context, args []string, stdout, stderr io.Writer) int {
	// Signal-aware, as the hand-rolled dispatcher was for the subscription
	// commands (status/events/watch/factory/herd/cloud): a subscription is a
	// thing a person leaves open, and Ctrl-C has to shut it down cleanly.
	// One NotifyContext for the whole tree is a superset of what the
	// dispatcher installed — it adds nothing a command would notice, because
	// the commands that manage signals themselves (run-epic's evacuation
	// flush) install their own handlers and ignore the context cobra passes.
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	defer stop()

	root := newRootCommand(stdout, stderr)
	root.SetArgs(args)
	// fang.Execute: styled help and errors, --version, completions and man
	// pages — the wrapper, and the one place it is called. Its version is
	// the build's own Version variable, so `ticfac --version` and
	// `ticfac version` answer from the same source.
	if err := fang.Execute(ctx, root,
		fang.WithVersion(Version),
		fang.WithErrorHandler(fangErrorHandler),
	); err != nil {
		return exitCodeOf(err)
	}
	return exitSuccess
}

// newRootCommand builds the whole tree: eleven commands, the same names,
// flags and bodies the hand-rolled dispatcher dispatched to, now as cobra
// commands whose help, completion and man pages derive from the declaration
// rather than from a hand-maintained usage string.
func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	// The overview's own flags, declared before the tree so the root's RunE
	// can close over them. They sit on the root's LOCAL set, so they parse
	// only when the root itself runs (the bare invocation) and no
	// subcommand's flag surface changes.
	fs := flag.NewFlagSet("ticfac", flag.ContinueOnError)
	overviewRepo := fs.String("repo", "", "the checkout whose runs the overview lists (default: cwd)")
	overviewJSON := fs.Bool("json", false, "print the versioned overview model: one status model per run, attention first")
	overviewAll := fs.Bool("all", false, "list every run, history included (runs of closed epics, runs a later run superseded, runs finished more than a week ago)")
	root := &cobra.Command{
		Use:   "ticfac",
		Short: "execution and orchestration for ticks",
		Long: `ticfac — execution and orchestration for ticks: a reconciler over the
ticks tracker and Git (including .ticfac/ run state), a four-operation
executor protocol (start / inspect / cancel / collect), role jobs for review
and closeout, and the hosts behind it — the local subprocess executor,
herdr, and the cloud factory's sandbox door.

Run 'ticfac' with no arguments for the overview: every run this checkout and
the factory know, attention first — every run held or failed names its
reason and the one command that clears it. Run 'ticfac <command> --help'
for a command's flags; 'ticfac help <command>' for any subcommand's. The
exit codes are the contract a script branches on — 0 done, 1 failed,
2 usage, 3 held for a person (or, in the tk-ported cloud and factory
family, not inside a repository), 4 a lookup that came back empty, 5 the
command ended while the run is still going, 6 an unreadable local file,
7 a run stopped deliberately (cancelled) —
and every command takes --json: one versioned document, the schema named
inside it, so an agent never parses prose.`,
		// The bare invocation IS the overview (tick 2qz): every run this
		// checkout and the factory know, attention first, every stop named
		// with the one command that clears it. fi3's contract — a bare call
		// means usage, because a program with no default action refuses — is
		// deliberately replaced: ticfac now has a default action, the one
		// screen an unattended factory is glanced at with, and `ticfac --help`
		// remains the place a person reads the whole tree. The Args validator
		// below still types the unknown-command refusal for everything the
		// dispatcher's default arm used to catch.
		Args: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				return newExitError(exitUsage,
					"unknown command %q for %q — `ticfac --help` lists the commands this build serves",
					args[0], c.CommandPath())
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			return codeToErr(overviewCommand(c.Context(), *overviewRepo, *overviewJSON, *overviewAll, stdout, stderr))
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	commandFlags(root, fs)
	// Flag-parse failures are usage errors (exit 2), typed at the source.
	// Subcommands inherit this through cobra's FlagErrorFunc parent lookup.
	root.SetFlagErrorFunc(usageFlagError)
	root.AddCommand(
		newRunCommand(stdout, stderr),
		newRunEpicCommand(stdout, stderr),
		newInitCommand(stdout, stderr),
		newDoctorCommand(stdout, stderr),
		newSettleCommand(stdout, stderr),
		newFindingsCommand(stdout, stderr),
		newFindingCommand(stdout, stderr),
		newTriageCommand(stdout, stderr),
		newStatusCommand(stdout, stderr),
		newEventsCommand(stdout, stderr),
		newWatchCommand(stdout, stderr),
		newVersionCommand(stdout, stderr),
		newSkillsCommand(stdout, stderr),
		newFactoryCommand(stdout, stderr),
		newHerdCommand(stdout, stderr),
		newCloudCommand(stdout, stderr),
	)
	return root
}

// usageFlagError converts a cobra/pflag flag-parsing failure ("unknown
// flag", "invalid argument") into an explicit usage exit, the code the
// hand-rolled flag sets already gave a parse error (2). Installed on the
// root; cobra's FlagErrorFunc lookup walks up to the parent, so every
// subcommand — including ones added later — inherits it without opting in.
func usageFlagError(_ *cobra.Command, err error) error {
	if err == nil {
		return nil
	}
	// pflag's help sentinel is control flow, not a usage failure.
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	var exitErr *exitError
	if errors.As(err, &exitErr) {
		return err
	}
	return newExitError(exitUsage, "%v", err)
}

// printedExit is the error a command body that has ALREADY printed its own
// refusal returns: the body keeps the output contract it has always had
// (stderr text and exit code byte-for-byte), and fang's styled error stays
// silent for it rather than saying the same thing twice. A silent error is
// deliberate — the words are the body's, and the code is all fang needs.
type printedExit struct {
	code int
}

func (e *printedExit) Error() string { return "" }

// codeToErr turns a body's exit code into the error cobra carries back to
// fang: success is nil, everything else is a printedExit with the code.
func codeToErr(code int) error {
	if code == exitSuccess {
		return nil
	}
	return &printedExit{code: code}
}

// fangErrorHandler is fang's, minus one case: a printedExit has already been
// said by the command that produced it, in the wording its contract fixes,
// so repeating it styled would be the "repeated usage dump" this wrapper
// exists to remove — applied to errors. Everything cobra itself reports
// (unknown command, unknown flag, a body that returned a typed error) keeps
// fang's rendering.
func fangErrorHandler(w io.Writer, styles fang.Styles, err error) {
	var printed *printedExit
	if errors.As(err, &printed) {
		return
	}
	fang.DefaultErrorHandler(w, styles, err)
}

// commandFlags registers one command's stdlib flag set on the cobra command
// so a single declaration serves both halves: cobra parses the invocation
// (help, completion and man pages list the flags), and the body reads the
// same pointers the stdlib flag set binds — the definitions the bodies have
// always carried, consumed by the tree instead of parsed in place.
//
// The stdlib flag set is the declaration FORMAT, not a second parser: after
// AddGoFlagSet, pflag writes the parsed values through the same pointers,
// and the body's `*repo` reads what the invocation passed. pflag also gives
// single-letter go flags (`-f`) their shorthand back, so cloud logs' -f
// survives the move.
func commandFlags(cmd *cobra.Command, fs *flag.FlagSet) {
	cmd.Flags().AddGoFlagSet(fs)
}
