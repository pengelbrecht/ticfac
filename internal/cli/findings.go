package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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

// openFindingsStore opens the run's draft store the way the reconciler opened
// it: the same repository, remote, integration branch and run id — the drafts
// live on the branch the run owns.
func openFindingsStore(epicID, repo, remote, branch, runID string) (*runstate.Store, error) {
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
			return nil, err
		}
	}
	store, err := runstate.Open(runstate.Options{Repo: repo, Remote: remote, Branch: branch, RunID: runID})
	if err != nil {
		return nil, err
	}
	if _, err := store.Fetch(); err != nil {
		return nil, fmt.Errorf("read the findings of run %s: %w", runID, err)
	}
	return store, nil
}

func findingsCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("findings", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo, remote, branch, runID := findingRunOptions(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac findings: exactly one epic id is required\n")
		return 2
	}
	epicID := rest[0]

	store, err := openFindingsStore(epicID, *repo, *remote, *branch, *runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac findings %s: %v\n", epicID, err)
		return 1
	}
	findings, err := store.Findings()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac findings %s: %v\n", epicID, err)
		return 1
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
		fmt.Fprintf(stdout, "    discovered by %s (tick %s, attempt %d)\n",
			finding.DiscoveredFrom, finding.TickID, finding.Attempt)
		switch finding.Status {
		case runstate.FindingPromoted:
			fmt.Fprintf(stdout, "    promoted as %s by %s at %s\n", finding.PromotedAs, finding.TriagedBy, finding.TriagedAt)
		case runstate.FindingDiscarded:
			fmt.Fprintf(stdout, "    discarded by %s at %s\n", finding.TriagedBy, finding.TriagedAt)
		case runstate.FindingFixed:
			fmt.Fprintf(stdout, "    fixed as %s by %s at %s\n", finding.FixedAs, finding.TriagedBy, finding.TriagedAt)
		default:
			fmt.Fprintf(stdout, "    triage: ticfac finding %s %s --promote-as <tick> --by \"<who>\" | --discard --by \"<who>\" | --fixed-as <commit> --by \"<who>\"\n",
				epicID, finding.Key)
		}
	}
	if untriaged == 0 {
		fmt.Fprintf(stdout, "%d finding(s), none waiting for a person; the triage gate is down.\n", len(findings))
	} else {
		fmt.Fprintf(stdout, "%d finding(s), %d waiting for a person; the epic's close-out does not hand over while a finding is untriaged.\n",
			len(findings), untriaged)
	}
	return 0
}

func findingTarget(target string) string {
	if target == "" {
		return "this repository"
	}
	return target
}

func findingCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("finding", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo, remote, branch, runID := findingRunOptions(fs)
	promoteAs := fs.String("promote-as", "", "the tick the promotion created: a bare tick id, or <owner/name>:<tick-id> for a routed finding")
	discard := fs.Bool("discard", false, "record that a person looked and said no")
	fixedAs := fs.String("fixed-as", "", "record that the finding was repaired inside this epic, naming the commit that repaired it")
	by := fs.String("by", "", "the person triaging this draft")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
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

	store, err := openFindingsStore(epicID, *repo, *remote, *branch, *runID)
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
		fmt.Fprintf(stdout, "finding %s was decided while you were deciding it: it is %s", key, decided.Status)
		if decided.TriagedBy != "" {
			fmt.Fprintf(stdout, " (by %s)", decided.TriagedBy)
		}
		fmt.Fprintf(stdout, ". Their decision stands.\n")
		return 0
	}
	if triage.Status == runstate.FindingFixed {
		fmt.Fprintf(stdout, "finding %s is recorded as fixed, repaired as %s, %s's decision of run %s.\n"+
			"Ticks of the run whose findings are all triaged can now close. The same finding reported again is not suppressed: "+
			"if it comes back, the fix did not hold, and that is exactly when the run must hear it.\n",
			key, decided.FixedAs, *by, store.RunID())
		return 0
	}
	if triage.Status == runstate.FindingPromoted {
		fmt.Fprintf(stdout, "finding %s is promoted as %s, recorded as %s's decision of run %s.\n"+
			"File the tick carrying `discovered_from %s` so the attempt that found it is never lost again.\n"+
			"Ticks of the run whose findings are all triaged can now close.\n",
			key, decided.PromotedAs, *by, store.RunID(), finding.DiscoveredFrom)
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
