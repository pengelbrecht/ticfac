package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The findings triage surface (tick 7vn): the person's half of the channel a
// worker's report is the other half of.
//
// `findings` lists the drafts a run has filed — with the key, the triage
// state, the target and the attempt that discovered each. `finding` records
// ONE decision: promote (naming the tick that was created, and the repository
// it was routed to when the finding targeted another one), discard, or FIXED
// — repaired inside the epic, naming the commit that repaired it (tick her),
// which is the verdict a repaired finding needs: there is no tick to promote
// it to, and a discard would mean the opposite of what happened. The
// reconciler's close gate reads the same records, so a tick whose findings are
// untriaged stays open until somebody runs one of these.
//
// Nothing here writes the tracker. The promotion records the tick the OPERATOR
// created — with the draft's `discovered_from` printed for it to be filed
// under — because a draft is not a tick, and making it one is the one decision
// this surface will not make for you.

// findingRunOptions are the run-addressing flags both commands share, with the
// same defaults `run-epic` derives: the same run, addressed the same way.
func findingRunOptions(fs *flag.FlagSet) (repo, remote, branch, runID *string) {
	repo = fs.String("repo", "", "the checkout the run works in")
	remote = fs.String("remote", "origin", "the remote holding the run's durable authority")
	branch = fs.String("branch", "", "the EpicRun integration branch (default: epic/<epic-id>)")
	runID = fs.String("run-id", "", "the run's id (default: epic-<epic-id>)")
	return repo, remote, branch, runID
}

// findingsFlags is the --json flag `findings` carries (tick 8v3): the
// listing is data — one versioned document with every draft's full record,
// the same fields `ticfac triage --json` lists and decides by.
type findingsFlags struct {
	asJSON *bool
}

// newFindingsCommand builds the cobra command for `findings`.
func newFindingsCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "findings <epic-id>",
		Short: "list the worker findings drafted for triage",
		Long: `List the findings a run's workers drafted for triage: the key, the triage
state, the target and the attempt that discovered each. --json answers the
same listing as one versioned document (ticfac.findings.v1).`,
	}
	fs := flag.NewFlagSet("findings", flag.ContinueOnError)
	repo, remote, branch, runID := findingRunOptions(fs)
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.findings.v1): every draft's full record")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(findingsCommand(args, repo, remote, branch, runID, asJSON, stdout, stderr))
	}
	return cmd
}

// newFindingCommand builds the cobra command for `finding`.
func newFindingCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finding <epic-id> <key>",
		Short: "triage one drafted finding",
		Long: `Record ONE decision on a drafted finding: promote (naming the tick that
was created, and the repository it was routed to when the finding targeted
another one), discard, or FIXED — repaired inside the epic, naming the commit
that repaired it (tick her), which is the verdict a repaired finding needs:
there is no tick to promote it to, and a discard would mean the opposite of
what happened. The reconciler's close gate reads the same records, so a tick
whose findings are untriaged stays open until somebody runs one of these.

Nothing here writes the tracker. The promotion records the tick the OPERATOR
created — with the draft's discovered_from printed for it to be filed under —
because a draft is not a tick, and making it one is the one decision this
surface will not make for you.`,
	}
	fs := flag.NewFlagSet("finding", flag.ContinueOnError)
	repo, remote, branch, runID := findingRunOptions(fs)
	promoteAs := fs.String("promote-as", "", "the tick the promotion created: a bare tick id, or <owner/name>:<tick-id> for a routed finding")
	discard := fs.Bool("discard", false, "record that a person looked and said no")
	fixedAs := fs.String("fixed-as", "", "record that the finding was repaired inside this epic, naming the commit that repaired it")
	by := fs.String("by", "", "the person triaging this draft")
	asJSON := fs.Bool("json", false, "print one versioned document (ticfac.finding.v1) recording the decision")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(findingCommand(args, repo, remote, branch, runID, promoteAs, discard, fixedAs, by, asJSON, stdout, stderr))
	}
	return cmd
}

// resolveFindingsRun fills the run-addressing defaults the findings commands
// and the triage surface share: the same derivation `run-epic` performs, so
// every surface that addresses a run's drafts names the same branch for the
// same epic. The epic id is accepted with its own `epic-` prefix everywhere
// `ticfac run` accepts it — an operator who types what every run's own
// output says (`epic-<id>`) is right — so the prefix is stripped here, never
// doubled into epic/epic-<id>, and the canonical id is the first value
// returned: the tick an absorb files names the epic the tracker knows, not
// the spelling that was typed.
func resolveFindingsRun(epicID, repo, remote, branch, runID string) (string, string, string, string, string, error) {
	epicID = strings.TrimPrefix(epicID, "epic-")
	if branch == "" {
		branch = "epic/" + epicID
	}
	if runID == "" {
		runID = "epic-" + epicID
	}
	if repo == "" {
		var err error
		repo, err = os.Getwd()
		if err != nil {
			return "", "", "", "", "", err
		}
	}
	return epicID, repo, remote, branch, runID, nil
}

// openFindingsStore opens the run's draft store the way the reconciler opened
// it: the same repository, remote, integration branch and run id — the drafts
// live on the branch the run owns. The canonical epic id comes back with it,
// so the commands that address a run by its prefixed spelling still name the
// epic the tracker knows in what they print and create.
func openFindingsStore(epicID, repo, remote, branch, runID string) (*runstate.Store, string, error) {
	epicID, repo, remote, branch, runID, err := resolveFindingsRun(epicID, repo, remote, branch, runID)
	if err != nil {
		return nil, "", err
	}
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: remote, Branch: branch, RunID: runID})
	if err != nil {
		return nil, "", err
	}
	if _, err := store.Fetch(); err != nil {
		return nil, "", fmt.Errorf("read the findings of run %s: %w", runID, err)
	}
	return store, epicID, nil
}

func findingsCommand(args []string, repo, remote, branch, runID *string, asJSON *bool, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac findings: exactly one epic id is required\n")
		return 2
	}
	epicID := rest[0]
	// The epic id is accepted with its own `epic-` prefix everywhere; a
	// prefix with nothing behind it names no epic.
	if strings.TrimPrefix(epicID, "epic-") == "" {
		fmt.Fprintf(stderr, "ticfac findings: %q names no epic\n", epicID)
		return exitUsage
	}
	if parseOnly {
		return 0
	}

	store, epicID, err := openFindingsStore(epicID, *repo, *remote, *branch, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac findings %s: %v\n", epicID, err)
		return 1
	}
	findings, err := store.Findings()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac findings %s: %v\n", epicID, err)
		return 1
	}
	if *asJSON {
		// One versioned document, every draft's FULL record — the same shape
		// the triage surface lists and decides by, so an agent pipes one into
		// the other without a translation: `ticfac findings --json` to read,
		// `ticfac triage <epic> <prefix>=<decision>` to settle.
		listed := make([]triageFindingJSON, 0, len(findings))
		for _, finding := range findings {
			listed = append(listed, newTriageFindingJSON(finding))
		}
		untriaged := 0
		for _, finding := range findings {
			if finding.Status == runstate.FindingProposed {
				untriaged++
			}
		}
		doc := struct {
			agentDoc
			RunID     string              `json:"run_id"`
			Untriaged int                 `json:"untriaged"`
			Findings  []triageFindingJSON `json:"findings"`
		}{
			agentDoc:  agentDoc{Schema: agentSchemaID("findings"), State: agentStateDone},
			RunID:     store.RunID(),
			Untriaged: untriaged,
			Findings:  listed,
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac findings %s: %v\n", epicID, err)
			return 1
		}
		return 0
	}
	if len(findings) == 0 {
		fmt.Fprintf(stdout, "run %s has no findings drafted for triage.\n", store.RunID())
		return 0
	}
	untriaged := 0
	for _, finding := range findings {
		if finding.Status == runstate.FindingProposed {
			untriaged++
		}
		fmt.Fprintf(stdout, "%s  %-9s %-12s %-6s  for %-19s  %s\n",
			finding.Key, finding.Status, finding.Kind, finding.Severity,
			findingTarget(finding.Target), finding.Title)
		fmt.Fprintf(stdout, "    discovered by %s (tick %s, run dispatch #%d)\n",
			finding.DiscoveredFrom, finding.TickID, finding.Attempt)
		// The linkage mark (tick nfo): the claim against the epic's definition
		// of done — which [A<n>] item the reporter says is broken, demonstrated
		// by what — or the unlinked mark, so a finding nobody linked reads as
		// making no claim rather than as claiming to break nothing.
		fmt.Fprintf(stdout, "    %s\n", finding.LinkageText())
		switch finding.Status {
		case runstate.FindingPromoted:
			fmt.Fprintf(stdout, "    promoted as %s by %s at %s\n", finding.PromotedAs, finding.TriagedBy, finding.TriagedAt)
		case runstate.FindingDiscarded:
			fmt.Fprintf(stdout, "    discarded by %s at %s\n", finding.TriagedBy, finding.TriagedAt)
		case runstate.FindingFixed:
			fmt.Fprintf(stdout, "    fixed as %s by %s at %s\n", finding.FixedAs, finding.TriagedBy, finding.TriagedAt)
		default:
			// The pointer teaches the everyday path (tick 8yn): the draft
			// addressed by the SHORTEST key prefix that names it alone among
			// the drafts this listing shows — never the old `ticfac finding
			// <epic> <64-hex> --promote-as ...` shape, which is the friction
			// the triage surface exists to remove. A finding routed to another
			// repository keeps the old command for its promotion: the tick it
			// becomes lives in the repository it targets, and the triage
			// surface refuses to create it here — its own refusal names the full
			// command at the moment of need.
			prefix := triageKeyPrefix(findings, finding.Key)
			if finding.Target == "" {
				fmt.Fprintf(stdout, "    triage: ticfac triage %s %s=absorb|file|fixed:<commit>|discard\n",
					epicID, prefix)
			} else {
				fmt.Fprintf(stdout, "    triage: ticfac triage %s %s=discard — or promote it into %s with ticfac finding\n",
					epicID, prefix, finding.Target)
			}
		}
	}
	if untriaged == 0 {
		fmt.Fprintf(stdout, "%d finding(s), none waiting for a person; the triage gate is down.\n", len(findings))
	} else {
		fmt.Fprintf(stdout, "%d finding(s), %d waiting for a person; ticfac triage %s settles each by short key "+
			"prefix, and the epic's close-out does not hand over while a finding is untriaged.\n",
			len(findings), untriaged, epicID)
	}
	return 0
}

// triageKeyPrefix is the prefix the listing hands the person: the shortest
// that names this draft alone among the drafts the listing shows — the
// triage surface's own rule, short and unambiguous — floored at a few
// characters so it reads as a prefix, with the whole key when nothing
// shorter is unambiguous. A person who copies the prefix into `ticfac triage`
// must land on the draft they read, not on an ambiguity the walk refuses.
func triageKeyPrefix(findings []runstate.Finding, key string) string {
	const floor = 3
	for n := floor; n < len(key); n++ {
		prefix := key[:n]
		ambiguous := false
		for _, f := range findings {
			if f.Key != key && strings.HasPrefix(f.Key, prefix) {
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

func findingTarget(target string) string {
	if target == "" {
		return "this repository"
	}
	return target
}

func findingCommand(args []string, repo, remote, branch, runID, promoteAs *string, discard *bool, fixedAs, by *string, asJSON *bool, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 2 || rest[0] == "" || rest[1] == "" {
		fmt.Fprintf(stderr, "ticfac finding: exactly one epic id and one finding key are required\n")
		return 2
	}
	epicID, key := rest[0], rest[1]
	if *by == "" {
		fmt.Fprintf(stderr, "ticfac finding %s %s: --by names who is triaging; a decision nobody can attribute is "+
			"one nobody can audit\n", epicID, key)
		return 2
	}
	verdicts := 0
	if *promoteAs != "" {
		verdicts++
	}
	if *discard {
		verdicts++
	}
	if *fixedAs != "" {
		verdicts++
	}
	if verdicts != 1 {
		fmt.Fprintf(stderr, "ticfac finding %s %s: say exactly one of --promote-as <tick>, --discard or --fixed-as <commit>\n",
			epicID, key)
		return 2
	}
	// The epic id is accepted with its own `epic-` prefix everywhere; a
	// prefix with nothing behind it names no epic.
	if strings.TrimPrefix(epicID, "epic-") == "" {
		fmt.Fprintf(stderr, "ticfac finding: %q names no epic\n", epicID)
		return exitUsage
	}
	if parseOnly {
		return 0
	}

	store, epicID, err := openFindingsStore(epicID, *repo, *remote, *branch, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac finding %s %s: %v\n", epicID, key, err)
		return 1
	}
	finding, ok, err := store.Finding(key)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac finding %s %s: %v\n", epicID, key, err)
		return 1
	}
	if !ok {
		fmt.Fprintf(stderr, "ticfac finding %s %s: run %s has no findings draft %s. "+
			"`ticfac findings %s` lists the drafts that exist.\n", epicID, key, store.RunID(), key, epicID)
		return 1
	}
	if finding.Status != runstate.FindingProposed {
		if *asJSON {
			emitFindingJSON(stdout, key, finding.Status, *by, finding.PromotedAs, finding.FixedAs, "a decision is never made twice, and a repeat finding proposes nothing new")
			return 0
		}
		fmt.Fprintf(stdout, "finding %s is already %s", key, finding.Status)
		if finding.TriagedBy != "" {
			fmt.Fprintf(stdout, " (by %s at %s)", finding.TriagedBy, finding.TriagedAt)
		}
		if finding.PromotedAs != "" {
			fmt.Fprintf(stdout, ", as %s", finding.PromotedAs)
		}
		if finding.FixedAs != "" {
			fmt.Fprintf(stdout, ", fixed as %s", finding.FixedAs)
		}
		fmt.Fprintf(stdout, ": a decision is never made twice, and a repeat finding proposes nothing new.\n")
		return 0
	}

	triage := runstate.Triage{Status: runstate.FindingPromoted, By: *by, PromotedAs: *promoteAs}
	if *discard {
		triage = runstate.Triage{Status: runstate.FindingDiscarded, By: *by}
	} else if *fixedAs != "" {
		triage = runstate.Triage{Status: runstate.FindingFixed, By: *by, FixedAs: *fixedAs}
	} else if err := checkPromotedAs(finding, *promoteAs); err != nil {
		fmt.Fprintf(stderr, "ticfac finding %s %s: %v\n", epicID, key, err)
		return 1
	}

	outcome, decided, err := store.TriageFinding(key, triage)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac finding %s %s: %v\n", epicID, key, err)
		return 1
	}
	if outcome.IsConflict() {
		if *asJSON {
			emitFindingJSON(stdout, key, decided.Status, decided.TriagedBy, decided.PromotedAs, decided.FixedAs,
				"decided while you were deciding it: their decision stands")
			return 0
		}
		fmt.Fprintf(stdout, "finding %s was decided while you were deciding it: it is %s", key, decided.Status)
		if decided.TriagedBy != "" {
			fmt.Fprintf(stdout, " (by %s)", decided.TriagedBy)
		}
		fmt.Fprintf(stdout, ". Their decision stands.\n")
		return 0
	}
	if triage.Status == runstate.FindingFixed {
		if *asJSON {
			emitFindingJSON(stdout, key, "fixed", *by, "", decided.FixedAs, "")
			return 0
		}
		fmt.Fprintf(stdout, "finding %s is recorded as fixed, repaired as %s, %s's decision of run %s.\n"+
			"Ticks of the run whose findings are all triaged can now close. The same finding reported again is not suppressed: "+
			"if it comes back, the fix did not hold, and that is exactly when the run must hear it.\n",
			key, decided.FixedAs, *by, store.RunID())
		return 0
	}
	if triage.Status == runstate.FindingPromoted {
		if *asJSON {
			emitFindingJSON(stdout, key, "promoted", *by, decided.PromotedAs, "",
				fmt.Sprintf("file the tick carrying `discovered_from %s`", finding.DiscoveredFrom))
			return 0
		}
		fmt.Fprintf(stdout, "finding %s is promoted as %s, recorded as %s's decision of run %s.\n"+
			"File the tick carrying `discovered_from %s` so the attempt that found it is never lost again.\n"+
			"Ticks of the run whose findings are all triaged can now close.\n",
			key, decided.PromotedAs, *by, store.RunID(), finding.DiscoveredFrom)
		return 0
	}
	if *asJSON {
		emitFindingJSON(stdout, key, "discarded", *by, "", "", "")
		return 0
	}
	fmt.Fprintf(stdout, "finding %s is discarded, recorded as %s's decision of run %s.\n"+
		"The same finding reported again proposes nothing new, whatever was decided here.\n",
		key, *by, store.RunID())
	return 0
}

// checkPromotedAs enforces the routing: a finding whose target is another
// repository is promoted INTO that repository, and a finding that belongs here
// is promoted here. A promotion that filed the wrong repository's tick would
// be the routing the finding carried, silently undone.
func checkPromotedAs(finding *runstate.Finding, promotedAs string) error {
	if finding.Target == "" {
		if strings.Contains(promotedAs, ":") {
			return fmt.Errorf("this finding belongs to this repository: promote it as a bare tick id, not %q", promotedAs)
		}
		return nil
	}
	want := finding.Target + ":"
	if !strings.HasPrefix(promotedAs, want) {
		return fmt.Errorf("this finding is routed to %s: promote it as \"%s:<tick-id>\", not %q — routing it "+
			"elsewhere drops it where nobody will find it", finding.Target, finding.Target, promotedAs)
	}
	if strings.TrimPrefix(promotedAs, want) == "" {
		return fmt.Errorf("this finding is routed to %s: promote it as \"%s:<tick-id>\", naming the tick", finding.Target, finding.Target)
	}
	return nil
}

// findingJSON is `finding --json`'s answer, ticfac.finding.v1: the decision
// as it was recorded — the verdict word, the actor, the tick a promotion
// created, the commit a fix named — the same fields the prose sentence
// carries, so an agent never parses a sentence to learn what its own
// command did.
type findingJSON struct {
	agentDoc
	Key        string `json:"key"`
	Decision   string `json:"decision"`
	By         string `json:"by"`
	PromotedAs string `json:"promoted_as,omitempty"`
	FixedAs    string `json:"fixed_as,omitempty"`
	Note       string `json:"note,omitempty"`
}

func emitFindingJSON(stdout io.Writer, key, decision, by, promotedAs, fixedAs, note string) {
	_ = emitAgentJSON(stdout, findingJSON{
		agentDoc:   agentDoc{Schema: agentSchemaID("finding"), State: agentStateDone},
		Key:        key,
		Decision:   decision,
		By:         by,
		PromotedAs: promotedAs,
		FixedAs:    fixedAs,
		Note:       note,
	})
}
