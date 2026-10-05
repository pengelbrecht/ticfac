package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The amendments surface (tick 7sn): the operator's half of the channel a
// worker's tracker-edit proposal is the other half of.
//
// A worker may propose a NOTE on the epic's own record — the record the
// close-out scores the acceptance from — and the run applies it through its
// durable tracker writer, visibly. But a worker's words there are a claim,
// never the operator's word: epic 43y's tick 8em closed a gap in acceptance
// item A1 by recording "the omp review boot is excepted, now on the record",
// the operator's recorded exceptions covered only the local claude rung, and
// nothing required the operator to confirm what a worker had written. So every
// applied note on the epic is also filed as an amendment awaiting the
// operator, and the close-out does not hand over while one is undecided.
//
// `amendments` lists them — key, state, the field, the tick that proposed it,
// its own words — and prints the settle command addressed by the SHORTEST key
// prefix that names it alone, the same friction rule the triage surface
// teaches. `amendment` records ONE decision: --confirm (the amendment stands
// as the operator's own) or --reject (the operator disowns it; the close-out
// names that the record must be repaired or the rejection withdrawn, and a
// later --confirm after the repair revisits it). --by names who decided: a
// decision nobody can attribute is one nobody can audit.
//
// Nothing here writes the tracker. The note is already on the record — the
// run applied it — and the decision is about the run's own amendment record,
// read back by the close-out's gate on the resume.

// newAmendmentsCommand builds the cobra command for `amendments`.
func newAmendmentsCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "amendments <epic-id>",
		Short: "list the worker-proposed epic amendments awaiting the operator",
		Long: `List the worker-proposed amendments to the epic's own record that the
run applied and filed for the operator: the key, the decision state, the
tick that proposed each, and its own words. The epic close-out does not
hand over while one is undecided. --json answers the same listing as one
versioned document (ticfac.amendments.v1).`,
	}
	fs := flag.NewFlagSet("amendments", flag.ContinueOnError)
	repo, remote, branch, runID := findingRunOptions(fs)
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.amendments.v1): every amendment's full record")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(amendmentsCommand(args, repo, remote, branch, runID, asJSON, stdout, stderr))
	}
	return cmd
}

// newAmendmentCommand builds the cobra command for `amendment`.
func newAmendmentCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "amendment <epic-id> <key-prefix>",
		Short: "record the operator's decision on one epic amendment",
		Long: `Record the operator's decision on ONE worker-proposed amendment to the
epic's record: --confirm (the amendment stands as the operator's own — the
close-out hands over behind it) or --reject (the operator disowns it — the
close-out does not hand over while the record still carries it, and a later
--confirm after the record was repaired revisits the decision). --by names
who decided. The reconciler's close gate reads the same record, so the
epic's close-out stays held until somebody runs one of these.`,
	}
	fs := flag.NewFlagSet("amendment", flag.ContinueOnError)
	repo, remote, branch, runID := findingRunOptions(fs)
	confirm := fs.Bool("confirm", false, "let the amendment stand as the operator's own")
	reject := fs.Bool("reject", false, "disown the amendment: the close-out holds until the record is repaired or the decision revisited")
	by := fs.String("by", "", "the operator deciding; a decision nobody can attribute is one nobody can audit")
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.amendment.v1) recording the decision")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(amendmentCommand(args, repo, remote, branch, runID, confirm, reject, by, asJSON, stdout, stderr))
	}
	return cmd
}

func amendmentsCommand(args []string, repo, remote, branch, runID *string, asJSON *bool, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac amendments: exactly one epic id is required\n")
		return exitUsage
	}
	epicID := rest[0]
	// The epic id is accepted with its own `epic-` prefix everywhere; a
	// prefix with nothing behind it names no epic.
	if epicIDOfArg(epicID) == "" {
		fmt.Fprintf(stderr, "ticfac amendments: %q names no epic\n", epicID)
		return exitUsage
	}
	if parseOnly {
		return 0
	}

	store, epicID, err := openFindingsStore(epicID, *repo, *remote, *branch, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac amendments %s: %v\n", epicID, err)
		return exitGeneric
	}
	amendments, err := store.Amendments()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac amendments %s: %v\n", epicID, err)
		return exitGeneric
	}
	if *asJSON {
		listed := make([]amendmentJSON, 0, len(amendments))
		for _, amendment := range amendments {
			listed = append(listed, newAmendmentJSON(amendment))
		}
		undecided := 0
		for _, amendment := range amendments {
			if amendment.Status != runstate.AmendmentConfirmed {
				undecided++
			}
		}
		doc := struct {
			agentDoc
			RunID      string          `json:"run_id"`
			Undecided  int             `json:"undecided"`
			Amendments []amendmentJSON `json:"amendments"`
		}{
			agentDoc:   agentDoc{Schema: agentSchemaID("amendments"), State: agentStateDone},
			RunID:      store.RunID(),
			Undecided:  undecided,
			Amendments: listed,
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac amendments %s: %v\n", epicID, err)
			return exitGeneric
		}
		return exitSuccess
	}
	if len(amendments) == 0 {
		fmt.Fprintf(stdout, "run %s has no worker-proposed amendments to the epic's record filed with it.\n", store.RunID())
		return exitSuccess
	}
	undecided := 0
	for _, amendment := range amendments {
		if amendment.Status != runstate.AmendmentConfirmed {
			undecided++
		}
		fmt.Fprintf(stdout, "%s  %-9s %-6s  on %-5s  proposed by tick %-4s  %s\n",
			shortAmendmentKey(amendment.Key), amendment.Status, amendment.Field, amendment.EpicID,
			amendment.ProposedBy, amendment.FirstLine())
		switch amendment.Status {
		case runstate.AmendmentConfirmed:
			fmt.Fprintf(stdout, "    confirmed by %s at %s\n", amendment.DecidedBy, amendment.DecidedAt)
		case runstate.AmendmentRejected:
			fmt.Fprintf(stdout, "    rejected by %s at %s\n", amendment.DecidedBy, amendment.DecidedAt)
		default:
			// The pointer teaches the everyday path, the same rule the triage
			// listing keeps: the amendment addressed by the SHORTEST key prefix
			// that names it alone among the listing — never the 64-hex key —
			// with the confirm verdict spelled out and --by left for the person;
			// --reject is the same command with the other flag.
			prefix := amendmentKeyPrefix(amendments, amendment.Key)
			fmt.Fprintf(stdout, "    settle: ticfac amendment %s %s --confirm --by \"<who>\" — or --reject to disown it\n",
				epicID, prefix)
		}
	}
	if undecided == 0 {
		fmt.Fprintf(stdout, "%d amendment(s), none waiting for the operator; the amendments gate is down.\n",
			len(amendments))
	} else {
		fmt.Fprintf(stdout, "%d amendment(s), %d waiting for the operator; the epic's close-out does not hand "+
			"over while one is undecided.\n", len(amendments), undecided)
	}
	return exitSuccess
}

func amendmentCommand(args []string, repo, remote, branch, runID *string, confirm, reject *bool, by *string,
	asJSON *bool, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 2 || rest[0] == "" || rest[1] == "" {
		fmt.Fprintf(stderr, "ticfac amendment: exactly one epic id and one amendment key are required\n")
		return exitUsage
	}
	epicID, key := rest[0], rest[1]
	if *by == "" {
		fmt.Fprintf(stderr, "ticfac amendment %s %s: --by names who is deciding; a decision nobody can "+
			"attribute is one nobody can audit\n", epicID, key)
		return exitUsage
	}
	verdicts := 0
	if *confirm {
		verdicts++
	}
	if *reject {
		verdicts++
	}
	if verdicts != 1 {
		fmt.Fprintf(stderr, "ticfac amendment %s %s: say exactly one of --confirm or --reject\n", epicID, key)
		return exitUsage
	}
	// The epic id is accepted with its own `epic-` prefix everywhere; a
	// prefix with nothing behind it names no epic.
	if epicIDOfArg(epicID) == "" {
		fmt.Fprintf(stderr, "ticfac amendment: %q names no epic\n", epicID)
		return exitUsage
	}
	if parseOnly {
		return 0
	}

	store, epicID, err := openFindingsStore(epicID, *repo, *remote, *branch, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac amendment %s %s: %v\n", epicID, key, err)
		return exitGeneric
	}
	amendments, err := store.Amendments()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac amendment %s %s: %v\n", epicID, key, err)
		return exitGeneric
	}
	full, ok := amendmentByPrefix(amendments, key)
	if !ok {
		fmt.Fprintf(stderr, "ticfac amendment %s %s: run %s has no amendment %s. `ticfac amendments %s` "+
			"lists the amendments that exist, each addressed by a short key prefix.\n",
			epicID, key, store.RunID(), key, epicID)
		return exitGeneric
	}

	decision := runstate.AmendmentDecision{Status: runstate.AmendmentConfirmed, By: *by}
	if *reject {
		decision.Status = runstate.AmendmentRejected
	}
	outcome, decided, err := store.DecideAmendment(full, decision)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac amendment %s %s: %v\n", epicID, key, err)
		return exitGeneric
	}
	if outcome.IsConflict() {
		if *asJSON {
			emitAmendmentJSON(stdout, decided.Status, *by, "decided while you were deciding it: their decision stands")
			return exitSuccess
		}
		fmt.Fprintf(stdout, "amendment %s was decided while you were deciding it: it is %s", shortAmendmentKey(full), decided.Status)
		if decided.DecidedBy != "" {
			fmt.Fprintf(stdout, " (by %s)", decided.DecidedBy)
		}
		fmt.Fprintf(stdout, ". Their decision stands.\n")
		return exitSuccess
	}
	if outcome != runstate.Updated {
		// Already decided, and the decision stands: the word the close-out
		// hands over behind is never made twice.
		if *asJSON {
			emitAmendmentJSON(stdout, decided.Status, decided.DecidedBy, "a decision is never made twice")
			return exitSuccess
		}
		fmt.Fprintf(stdout, "amendment %s is already %s", shortAmendmentKey(full), decided.Status)
		if decided.DecidedBy != "" {
			fmt.Fprintf(stdout, " (by %s at %s)", decided.DecidedBy, decided.DecidedAt)
		}
		fmt.Fprintf(stdout, ": a decision is never made twice.\n")
		return exitSuccess
	}
	if *asJSON {
		emitAmendmentJSON(stdout, decided.Status, *by, "")
		return exitSuccess
	}
	switch decided.Status {
	case runstate.AmendmentConfirmed:
		fmt.Fprintf(stdout, "amendment %s is CONFIRMED, recorded as %s's decision of run %s: the worker's "+
			"words on the epic's record stand as the operator's own, and the close-out hands over behind them.\n"+
			"Run the epic again under its run id: the close gate re-reads this record.\n",
			shortAmendmentKey(full), *by, store.RunID())
	case runstate.AmendmentRejected:
		fmt.Fprintf(stdout, "amendment %s is REJECTED, recorded as %s's decision of run %s: the operator "+
			"disowned what the worker wrote to the epic's record, and the close-out does not hand over while the "+
			"record still carries it. Remove or rewrite the amendment through the tracker's own writer, or revisit "+
			"the decision with --confirm; run the epic again under its run id afterwards either way.\n",
			shortAmendmentKey(full), *by, store.RunID())
	}
	return exitSuccess
}

// amendmentJSON is one amendment as an agent reads it: the full record the
// decision addresses by prefix, the same fields the listing shows.
type amendmentJSON struct {
	Key        string `json:"key"`
	EpicID     string `json:"epic_id"`
	Field      string `json:"field"`
	Value      string `json:"value"`
	ProposedBy string `json:"proposed_by"`
	Attempt    int    `json:"attempt"`
	ProposedAt string `json:"proposed_at"`
	Status     string `json:"status"`
	DecidedBy  string `json:"decided_by,omitempty"`
	DecidedAt  string `json:"decided_at,omitempty"`
}

func newAmendmentJSON(a runstate.Amendment) amendmentJSON {
	return amendmentJSON{
		Key: a.Key, EpicID: a.EpicID, Field: a.Field, Value: a.Value,
		ProposedBy: a.ProposedBy, Attempt: a.Attempt, ProposedAt: a.ProposedAt,
		Status: a.Status, DecidedBy: a.DecidedBy, DecidedAt: a.DecidedAt,
	}
}

// decisionJSON is `amendment --json`'s answer, ticfac.amendment.v1: the
// decision as it was recorded.
type decisionJSON struct {
	agentDoc
	Key      string `json:"key"`
	Decision string `json:"decision"`
	By       string `json:"by"`
	Note     string `json:"note,omitempty"`
}

func emitAmendmentJSON(stdout io.Writer, decision, by, note string) {
	_ = emitAgentJSON(stdout, decisionJSON{
		agentDoc: agentDoc{Schema: agentSchemaID("amendment"), State: agentStateDone},
		Decision: decision, By: by, Note: note,
	})
}

// shortAmendmentKey is the listing's spelling of a key, for a line a person
// reads; the full key is in `--json` and in the close-out's hold, which the
// settle command's prefix is checked against.
func shortAmendmentKey(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

// amendmentKeyPrefix is the prefix the listing hands the person: the shortest
// that names this amendment alone among the listing — the triage surface's own
// rule (triageKeyPrefix), floored at a few characters so it reads as a prefix,
// with the whole key when nothing shorter is unambiguous.
func amendmentKeyPrefix(amendments []runstate.Amendment, key string) string {
	const floor = 3
	for n := floor; n < len(key); n++ {
		prefix := key[:n]
		ambiguous := false
		for _, a := range amendments {
			if a.Key != key && strings.HasPrefix(a.Key, prefix) {
				ambiguous = true
				break
			}
		}
		if !ambiguous {
			return prefix
		}
	}
	return key
}

// amendmentByPrefix resolves the address the person typed: a short key prefix
// that must name one amendment alone, or the whole key. An ambiguity is a
// refusal, never a guess — the same rule the triage walk keeps.
func amendmentByPrefix(amendments []runstate.Amendment, prefix string) (string, bool) {
	var matched []string
	for _, a := range amendments {
		if a.Key == prefix {
			return a.Key, true
		}
		if strings.HasPrefix(a.Key, prefix) {
			matched = append(matched, a.Key)
		}
	}
	if len(matched) == 1 {
		return matched[0], true
	}
	return "", false
}
