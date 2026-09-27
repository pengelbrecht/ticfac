package cli

// The --json half of tick 8v3's acceptance: EVERY command has --json. The
// tree is the authority — a command added without the flag fails here, the
// same way the README's command table fails in readme_test.go until it names
// a new command. A command whose work is prose for a person still answers
// for an agent: the flag exists, and the document it prints is versioned.
import (
	"testing"

	"github.com/spf13/cobra"
)

// discardWriter keeps the tree walk silent.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// jsonExemptNames are the surfaces cobra itself synthesises from the tree —
// help and completion are derived (the whole point of nwj's tree: nothing
// operator-facing is maintained by hand beside it), they print a tree that
// is already machine-readable, and they carry no answer of ticfac's to
// version.
var jsonExemptNames = map[string]bool{
	"help":       true, // cobra's derived help viewer
	"completion": true, // cobra's generated completion writer
	"man":        true, // hidden roff output, derived from the tree
}

// jsonCarryingCommands walks the tree and yields every command that owes a
// --json flag: the visible, runnable leaves. Group commands (cloud, factory,
// herd, skills) are namespaces, not verbs — their own bodies are usage
// refusals that print a vocabulary and exit 2, and a usage error carries no
// document. Everything a person or an agent actually RUNS is here.
func jsonCarryingCommands(t *testing.T, cmd *cobra.Command) []*cobra.Command {
	t.Helper()
	var out []*cobra.Command
	if cmd.HasSubCommands() {
		for _, sub := range cmd.Commands() {
			out = append(out, jsonCarryingCommands(t, sub)...)
		}
		return out
	}
	if cmd.Hidden || jsonExemptNames[cmd.Name()] {
		return out
	}
	if cmd.RunE == nil && cmd.Run == nil {
		return out // a stub with no body carries no document
	}
	return append(out, cmd)
}

func TestEveryCommandHasJSON(t *testing.T) {
	t.Parallel()

	root := newRootCommand(discardWriter{}, discardWriter{})
	commands := jsonCarryingCommands(t, root)
	if len(commands) < 15 {
		t.Fatalf("the walk found only %d commands — the tree's runnable leaves are the whole point", len(commands))
	}
	for _, cmd := range commands {
		if cmd.Flags().Lookup("json") == nil {
			t.Errorf("`%s` has no --json flag: every command answers for an agent (tick 8v3)", cmd.CommandPath())
		}
	}
}

// TestStateExitClassAgreesWithTheTable is the agreement property the
// operator's note names for the future property tests, stated where it can
// be checked today: a document's state word maps to one exit-code class,
// and that class is the table's code for the same word — a command that
// emits `state` and derives its code from it cannot disagree with the
// document it printed.
func TestStateExitClassAgreesWithTheTable(t *testing.T) {
	t.Parallel()

	for state, want := range map[string]int{
		agentStateDone:      exitSuccess,
		agentStateFailed:    exitGeneric,
		agentStateHeld:      ExitHeld,
		agentStateRunning:   exitRunning,
		agentStateCancelled: exitCancelled,
	} {
		if got := stateExitClass(state); got != want {
			t.Errorf("stateExitClass(%q) = %d, want %d", state, got, want)
		}
	}
	// An unknown state word is never a silent success.
	if got := stateExitClass("no-such-state"); got != exitGeneric {
		t.Errorf("stateExitClass(unknown) = %d, want %d", got, exitGeneric)
	}
}
