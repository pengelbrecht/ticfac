package cli

// The agent half of the command surface (tick 8v3): --json on every
// command, one document, versioned, so an agent never parses prose.
//
// The rules every --json surface answers by:
//
//   - ONE document on stdout, indented, and NOTHING on stderr that belongs
//     to the document. A human-facing line the command still wants to say
//     (a refusal's remedy, a progress note) goes to stderr, where prose
//     lives; stdout is the document's alone.
//   - The document carries a `schema` field naming a versioned schema id,
//     `ticfac.<command>.v1` — the same naming ticfac.status.v1 (tick 6dh)
//     and ticfac.job-status.v1 already established. A reader that meets a
//     schema it does not know refuses rather than guessing.
//   - Where the command's answer is an outcome of the work (a run ended, a
//     decision settled, a detach left the run going), the document carries
//     a `state` word from the exit table's vocabulary, and the command's
//     exit code is that state's class — the property the operator's note
//     names for the future property tests: the exit code always agrees
//     with the JSON's state.
//
// The two exceptions are documented in the exit table and kept: `status
// --json` exits liveness's answer alone (0 alive, 1 not — tick 6dh's own
// contract), and the cloud/factory family keeps tk's code 3
// (not-in-a-repository) beside the run surfaces' 3 (held for a person).

import (
	"encoding/json"
	"fmt"
	"io"
)

// The state words a --json document's `state` field carries: the exit
// table's classes as an agent reads them. The six outcomes the table
// separates — done, failed, held-for-a-person, running, cancelled — plus
// the words the run surfaces inherit (completed, stopped) that map onto
// them: completed onto done, and a cloud run's "stopped" onto cancelled.
const (
	agentStateDone      = "done"      // the command did its work
	agentStateFailed    = "failed"    // a failure that is not a usage mistake
	agentStateHeld      = "held"      // the work ended holding something only a person can move
	agentStateRunning   = "running"   // the command ended while the work is still in flight
	agentStateCancelled = "cancelled" // the work was stopped deliberately — neither done nor failed (tick rix)
)

// agentSchemaID names one command's versioned --json document:
// ticfac.<command>.v1. The version is the DOCUMENT's, not the build's — a
// field added to an answer bumps the version in the same change, the way
// the status model's SchemaVersion does.
func agentSchemaID(command string) string {
	return "ticfac." + command + ".v1"
}

// emitAgentJSON writes exactly one document to stdout: marshalled, indented,
// newline-terminated. It writes nothing to stderr — the whole point of the
// surface is that stdout parses and stderr explains.
func emitAgentJSON(stdout io.Writer, doc any) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s\n", raw)
	return err
}

// stateExitClass maps a document's state word to the exit-code class the
// table gives it, so a command that emits a state can derive the code from
// the same word a reader parsed — one authority for both halves of the
// answer, and the property to test: emitAgentExit never disagrees with the
// document it came from.
func stateExitClass(state string) int {
	switch state {
	case agentStateDone:
		return exitSuccess
	case agentStateHeld:
		return ExitHeld
	case agentStateRunning:
		return exitRunning
	case agentStateCancelled:
		return exitCancelled
	case agentStateFailed, "":
		return exitGeneric
	}
	// A state word outside the vocabulary is still an answer that did not
	// succeed in the table's terms; generic, never silently zero.
	return exitGeneric
}

// agentDoc is the envelope every new --json surface stamps first: the
// versioned schema id and, where the answer is an outcome, the state word
// the exit code agrees with. Commands embed it as their document's first
// field and fill the rest with their own answer.
type agentDoc struct {
	Schema string `json:"schema"`
	State  string `json:"state,omitempty"`
}
