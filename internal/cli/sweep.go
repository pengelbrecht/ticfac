package cli

// `ticfac sweep refs` (tick 6is): the whole-remote pass of the ref sweep.
// A run's end retires its own merged refs (internal/reconcile's
// retireRemoteRefs); this pass is the one that sees every run — the cloud
// runs whose container is gone, the runs a later run superseded, the
// unmerged refs whose epic's grace has run out — and is what an operator or
// a schedule runs. The rule itself lives in internal/refsweep.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/refsweep"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

const sweepUsage = `usage: ticfac sweep refs [--dry-run] [--repo <dir>] [--remote <name>] [--base <branch>] [--json]

Retire from the remote the refs of runs that have ENDED — completed, superseded
by a later run of the same epic, or their epic closed. Only ticfac's own names
are judged (ticfac/run-<run>/..., tick/<epic>/attempt-*, *-boot-stopped,
refs/ticfac/start/run-<run>/..., refs/ticfac/wip/run-<run>/...), each deleted by
exact name under a lease. A ref holding commits nothing merged is kept until
14 days after its epic closed; epic/*, main, tags and every branch ticfac does
not name are never touched.
`

func newSweepCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "retire what ended runs left behind",
		Long:  sweepUsage,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				fmt.Fprint(stderr, sweepUsage)
				return &printedExit{code: exitUsage}
			}
			fmt.Fprintf(stderr, "ticfac sweep: unknown subcommand %q\n\n%s", args[0], sweepUsage)
			return &printedExit{code: exitUsage}
		},
	}
	cmd.AddCommand(newSweepRefsCommand(stdout, stderr))
	return cmd
}

type sweepRefsFlags struct {
	repo, remote, base *string
	dryRun, asJSON     *bool
	graceDays          *int
}

func newSweepRefsCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refs",
		Short: "delete the remote refs of ended runs (--dry-run lists them)",
		Long:  sweepUsage,
		Args:  cobra.NoArgs,
	}
	fs := flag.NewFlagSet("sweep refs", flag.ContinueOnError)
	fl := sweepRefsFlags{
		repo:      fs.String("repo", ".", "the checkout to sweep from"),
		remote:    fs.String("remote", "origin", "the remote to sweep"),
		base:      fs.String("base", "main", "the base branch: a ref reachable from it is merged"),
		dryRun:    fs.Bool("dry-run", false, "list what would be deleted and delete nothing"),
		asJSON:    fs.Bool("json", false, "print machine-readable output"),
		graceDays: fs.Int("grace-days", int(refsweep.DefaultGrace/(24*time.Hour)), "how many days after its epic closed an unmerged ref of an ended run is kept"),
	}
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(sweepRefs(c.Context(), fl, stdout, stderr))
	}
	return cmd
}

// sweepEpicLookup is the tracker read the sweep asks about each epic; a seam
// for tests.
var sweepEpicLookup = func(repo string) func(ctx context.Context, id string) (refsweep.Epic, error) {
	var client *tk.Client
	var clientErr error
	made := false
	return func(ctx context.Context, id string) (refsweep.Epic, error) {
		if !made {
			client, clientErr = tk.NewContext(ctx, tk.Options{Dir: repo})
			made = true
		}
		if clientErr != nil {
			return refsweep.Epic{}, clientErr
		}
		tick, err := client.Show(ctx, id)
		if err != nil {
			return refsweep.Epic{}, err
		}
		closedAt, _ := time.Parse(time.RFC3339, tick.ClosedAt)
		return refsweep.Epic{Known: true, Closed: tick.Status == "closed", ClosedAt: closedAt}, nil
	}
}

type sweepRefsDoc struct {
	agentDoc
	DryRun  bool                      `json:"dry_run"`
	Listed  int                       `json:"listed"`
	Planned int                       `json:"planned"`
	Deleted int                       `json:"deleted"`
	Kept    int                       `json:"kept"`
	Failed  map[string]string         `json:"failed,omitempty"`
	Counts  map[string]sweepFamilyTal `json:"by_family"`
	Refs    []refsweep.Verdict        `json:"refs"`
}

type sweepFamilyTal struct {
	Delete int `json:"delete"`
	Keep   int `json:"keep"`
}

func sweepRefs(ctx context.Context, fl sweepRefsFlags, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	opts := refsweep.Options{
		Git:    refsweep.Git{Repo: *fl.repo, Remote: *fl.remote},
		Base:   *fl.base,
		Epic:   sweepEpicLookup(*fl.repo),
		Grace:  time.Duration(*fl.graceDays) * 24 * time.Hour,
		DryRun: *fl.dryRun,
	}
	report, err := refsweep.Sweep(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac sweep refs: %v\n", err)
		return exitGeneric
	}
	doc := sweepRefsDoc{
		agentDoc: agentDoc{Schema: agentSchemaID("sweep-refs"), State: agentStateDone},
		DryRun:   *fl.dryRun, Listed: report.Listed, Deleted: len(report.Deleted), Failed: report.Failed,
		Counts: map[string]sweepFamilyTal{}, Refs: report.Verdicts,
	}
	for _, v := range report.Verdicts {
		tally := doc.Counts[v.Family]
		if v.Delete {
			doc.Planned++
			tally.Delete++
		} else {
			doc.Kept++
			tally.Keep++
		}
		doc.Counts[v.Family] = tally
	}
	if len(report.Failed) > 0 {
		doc.State = agentStateFailed
	}
	if *fl.asJSON {
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac sweep refs: %v\n", err)
			return exitGeneric
		}
		return stateExitClass(doc.State)
	}

	verb := "deleted"
	if *fl.dryRun {
		verb = "would delete"
	}
	for _, v := range report.Verdicts {
		if v.Delete {
			if _, failed := report.Failed[v.Name]; failed {
				continue
			}
			fmt.Fprintf(stdout, "%s %s  (%s)\n", verb, v.Name, v.Why)
		}
	}
	// The kept refs, one line per reason: a run's dozens of branches share it.
	kept := map[string]int{}
	for _, v := range report.Verdicts {
		if !v.Delete {
			kept[v.Why]++
		}
	}
	reasons := make([]string, 0, len(kept))
	for why := range kept {
		reasons = append(reasons, why)
	}
	sort.Strings(reasons)
	for _, why := range reasons {
		fmt.Fprintf(stdout, "keep %d: %s\n", kept[why], why)
	}
	failed := make([]string, 0, len(report.Failed))
	for ref := range report.Failed {
		failed = append(failed, ref)
	}
	sort.Strings(failed)
	for _, ref := range failed {
		fmt.Fprintf(stderr, "not deleted %s: %s\n", ref, report.Failed[ref])
	}
	families := make([]string, 0, len(doc.Counts))
	for family := range doc.Counts {
		families = append(families, family)
	}
	sort.Strings(families)
	fmt.Fprintf(stdout, "\n%d ticfac-owned refs on %s; %s %d, keep %d\n", report.Listed, *fl.remote, verb,
		map[bool]int{true: doc.Planned, false: doc.Deleted}[*fl.dryRun], doc.Kept)
	for _, family := range families {
		fmt.Fprintf(stdout, "  %-26s %s %d, keep %d\n", family, verb, doc.Counts[family].Delete, doc.Counts[family].Keep)
	}
	if len(report.Failed) > 0 {
		return exitGeneric
	}
	return exitSuccess
}
