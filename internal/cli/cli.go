// Package cli is ticfac's command surface.
//
// `run-epic` is the reconciler's entry point (SPEC §12 Phase 1). It FAILS
// CLOSED: a build with no executor behind contracts/job-protocol.json's four
// operations refuses rather than doing something plausible, because a run that
// half-starts is the failure mode Appendix A was written out of.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// Version is this build's version, set with -ldflags "-X
// github.com/pengelbrecht/ticfac/internal/cli.Version=<v>". "dev" is an
// untagged build, exactly as tk reports it.
var Version = "dev"

// ExitNoExecutor is the exit code for a refusal to run: the request was
// understood and this build cannot serve it. It has its own slot so that
// "nothing is configured to run this" is distinguishable from a usage error
// without parsing stderr.
const ExitNoExecutor = 2

// NoExecutorMessage is the fail-closed refusal. It is asserted by a test: a
// silent or differently-worded refusal is the thing an operator misreads as a
// run that started.
const NoExecutorMessage = reconcile.NoExecutorMessage

const usage = `ticfac — execution and orchestration for ticks

usage:
  ticfac run-epic <epic-id>                     run one epic through the reconciler
  ticfac settle <epic-id> <tick-id> <attempt>   release an attempt nobody can address
  ticfac findings <epic-id>                     list the worker findings drafted for triage
  ticfac finding <epic-id> <key>                triage one drafted finding
  ticfac status <run-id> [--json]              is the run alive, and when did it last say anything
  ticfac events <run-id>                       a run's event feed: what it did, as it does it (--follow to subscribe)
  ticfac version [--json]                       report this build and the contract bundle it serves
  ticfac factory deploy                        put the ticks cloud factory in your own Cloudflare account
  ticfac factory setup                         walk the factory's credential ladder, one verified rung at a time
  ticfac factory <status|dashboard>            what the factory has configured; the read-only board
  ticfac cloud <run|stop|status|logs|trace|supervisor>   drive a self-deployed cloud factory

run-epic flags:
  --repo <dir>        the checkout attempts branch from (default: cwd)
  --remote <name>     the remote holding the run's durable authority (default: origin)
  --branch <name>     the EpicRun integration branch (default: epic/<epic-id>)
  --base <ref>        what the integration branch is cut from (default: HEAD)
  --run-id <id>       the run's id (default: epic-<epic-id>)
  --owner <name>      who claims a tick in the tracker (default: ticfac)
  --runner <name>     claude | codex | pi, when a profile routes none (default: $TICFAC_RUNNER, else claude)
  --tier <name>       pin a [roles.*.tiers.<name>] overlay for EVERY dispatch of the run —
                      an operator's explicit override; by default each dispatch DERIVES its
                      tier from [tier_policy] in the target repo's runners.toml (tick facts,
                      attempt number, declared ladder), or runs at the role's base values
  --profiles <dir>    resolve role profiles from this directory instead of the compiled-in ones
  --state-root <dir>  where attempt state lives, OUTSIDE the repository
  --gate <file>       the runners.toml the integrated gate is read from
  --budget <usd>      the budget an operator asks for
  --ceiling <usd>     the deployment ceiling it is clamped to
  --wall <seconds>    the wall clock one job is bounded by

Each dispatch goes through the executor its resolved profile names — a profile
naming the herdr executor launches the attempt in a herdr workspace, and every
record it produces states herdr's protocol and server version in its
provenance; this build honours two executors, the local subprocess one and
herdr, and refuses a profile naming any other before anything is claimed.

The effective budget — what an operator asked for, clamped to the deployment
ceiling — is printed before the run starts, while it can still be cancelled
cheaply. It binds a METERED credential; the local subprocess executor issues a
flat-rate one, so on this host the number travels with the job and is reported
everywhere, and the wall clock is what actually stops one.

settle flags:
  --release <who>     the person releasing the attempt (required)
  --repo, --remote, --branch, --run-id, --state-root, --gate, --profiles,
  --tier, --runner    as for run-epic: the same run, addressed the same way

An attempt whose supervisor died without settling it reads as lost, and every
restart holds it rather than starting a second job over the same identity
(Appendix A #6). "settle" is how a PERSON releases one: it refuses an attempt
the executor can still address, records the release durably as a decision
naming who made it, and the next run dispatches a NEW attempt instead of
adopting the released one. Whatever the released attempt committed stays on its
own write ref.

It releases one other attempt: one this run REJECTED while it was holding
commits nothing merged. No run collects that attempt again (the teardown the
refusal ran removed its worktree) and no run dispatches over it (that would
orphan the only copy of the work), so a person reads the branch and then says
here that the run may go on.

events flags:
  --repo <dir>        the checkout the run works in (default: cwd)
  --follow            keep the stream open: each event as it lands, until Ctrl-C

"events" is how a NON-PARTICIPANT learns a run finished: the run writes one
append-only JSONL stream at .ticfac/logs/<run-id>/events.jsonl, and this
follows it — every event with its run/tick/attempt identity — instead of
sleeping blind against the run or polling its durable records. A line is a
hint about when to LOOK, never a verdict: completion is still decided by the
evidence on the integration branch, the commits plus the report, so a
subscriber that reads run_finished goes and looks rather than believing it.

findings and finding flags:
  --repo <dir>         as for run-epic (default: cwd)
  --remote <name>      as for run-epic (default: origin)
  --branch <name>      as for run-epic (default: epic/<epic-id>)
  --run-id <id>        as for run-epic (default: epic-<epic-id>)

finding flags:
  --promote-as <tick>  record the tick a promotion created — a bare tick id for a
                       finding that belongs to this repository, <owner/name>:<tick-id>
                       for one routed to the repository its target names
  --discard            record that a person looked and said no
  --by <who>           the person triaging (required): a decision nobody can
                       attribute is one nobody can audit

A worker that discovers something outside its tick reports it as a typed
findings block in its report; the reconciler drafts each finding under
.ticfac/runs/<run-id>/findings/ on the integration branch, stamped with the
attempt that discovered it. A tick whose findings are untriaged is refused its
close, which is what stops one falling on the floor. Promotion keeps the
scope decision human: it records the tick YOU created — pass the draft's
discovered_from to the tracker when you file it, so the attempt that found
it is never lost again — and nothing here writes the tracker for you.
`

// Run executes one invocation and returns the process exit code. Everything is
// passed in rather than reached for, so the behaviour under test is the
// behaviour that ships.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "run-epic":
		return runEpic(args[1:], stdout, stderr)
	case "settle":
		return settle(args[1:], stdout, stderr)
	case "findings":
		return findingsCommand(args[1:], stdout, stderr)
	case "finding":
		return findingCommand(args[1:], stdout, stderr)
	case "status":
		return statusCommand(args[1:], stdout, stderr)
	case "events":
		// Signal-aware so a --follow shuts down cleanly on Ctrl-C: a
		// subscription is a thing a person leaves open.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return eventsCommand(ctx, args[1:], stdout, stderr)
	case "version":
		return version(args[1:], stdout, stderr)
	case "factory":
		// Signal-aware so the dashboard and a --follow shut down cleanly on
		// Ctrl-C, the way tk's cobra contexts did.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return factoryCommand(ctx, args[1:], stdout, stderr)
	case "cloud":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return cloudCommand(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "ticfac: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runEpic(args []string, stdout, stderr io.Writer) (code int) {
	fs := flag.NewFlagSet("run-epic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		repo      = fs.String("repo", "", "the checkout attempts branch from")
		remote    = fs.String("remote", "origin", "the remote holding the run's durable authority")
		branch    = fs.String("branch", "", "the EpicRun integration branch")
		base      = fs.String("base", "HEAD", "what the integration branch is cut from")
		runID     = fs.String("run-id", "", "the run's id")
		owner     = fs.String("owner", "ticfac", "who claims a tick in the tracker")
		runner    = fs.String("runner", os.Getenv("TICFAC_RUNNER"), "claude | codex | pi")
		tier      = fs.String("tier", "", "pin a [roles.*.tiers.<name>] overlay for every dispatch of this run (by default the tier is DERIVED per tick from [tier_policy])")
		profiles  = fs.String("profiles", "", "resolve role profiles from this directory")
		stateRoot = fs.String("state-root", "", "where attempt state lives, outside the repository")
		gate      = fs.String("gate", "", "the runners.toml the integrated gate is read from")
		budget    = fs.Float64("budget", 0, "the budget an operator asks for")
		ceiling   = fs.Float64("ceiling", 0, "the deployment ceiling it is clamped to")
		wall      = fs.Int("wall", reconcile.DefaultWallSeconds, "the wall clock one job is bounded by")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac run-epic: exactly one epic id is required\n")
		return 2
	}
	epicID := rest[0]

	// The refusal, and the reason it is a refusal rather than a no-op: without
	// a host behind the four-operation protocol there is nothing that could
	// start, inspect, cancel or collect a job, and reporting success here would
	// be the first of Appendix A's failures — a run recorded as done that never
	// ran.
	if err := reconcile.CheckExecutor(); err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %s.\n", epicID, NoExecutorMessage)
		fmt.Fprintf(stderr, "%v\n", err)
		return ExitNoExecutor
	}

	// Appendix A #12, at the surface an operator actually reads: the number
	// that will GOVERN, said at submission while the run can still be
	// cancelled cheaply. An operator who asked for 40 under an 8 ceiling is
	// told 8 here and not in a journal nobody sees — and told what the number
	// does on this host, because a budget printed like an enforced limit is
	// worse than no line at all.
	if *budget > 0 || *ceiling > 0 {
		clamped := reconcile.ClampBudget(*budget, *ceiling)
		fmt.Fprintf(stdout, "%s\n", budgetLine(clamped))
	}

	if *runner == "" {
		*runner = "claude"
	}
	tracker, err := tk.New(tk.Options{Dir: *repo})
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: the tracker is not usable: %v\n", epicID, err)
		return 1
	}

	reconciler, err := reconcile.New(reconcile.Options{
		Repo:              *repo,
		Remote:            *remote,
		EpicID:            epicID,
		RunID:             *runID,
		IntegrationBranch: *branch,
		BaseRef:           *base,
		Owner:             *owner,
		Tracker:           tracker,
		NewExecutor:       executorFactory(*runner, *gate),
		Executors:         knownExecutors(),
		ExecStateRoot:     *stateRoot,
		GateConfig:        *gate,
		ProfileDir:        *profiles,
		Tier:              *tier,
		WallSeconds:       *wall,
		BudgetUSD:         *budget,
		CeilingUSD:        *ceiling,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		return 1
	}

	// The process's own account of itself (ticks udp, ix9). run.pid makes
	// "is this run alive?" a fact `ticfac status` answers, and run.log keeps
	// what this process says whether or not whoever launched it captured its
	// output. The pwp production run died once with nothing captured, and its
	// cause was never recovered.
	repoDir := *repo
	if repoDir == "" {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			repoDir = wd
		}
	}
	liveRun := reconciler.RunID()
	life, err := runlife.Claim(repoDir, liveRun)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		return 1
	}
	operatorStderr := stderr
	stderr = io.MultiWriter(stderr, life.Log())
	stdout = io.MultiWriter(stdout, life.Log())

	// A death is a terminal feed line, never a feed that simply stops on an
	// ordinary success. Every path through Run that writes run_finished returns
	// without an error, so this is the only terminal line on the paths below.
	died := func(detail string) {
		event := runfeed.NewEvent(time.Now(), liveRun, "", nil, reconcile.StageRunDied, detail)
		if appendErr := runfeed.Open(repoDir, liveRun).Append(event); appendErr != nil {
			life.Logf("could not write run_died to the feed: %v", appendErr)
		}
	}

	// Registered first so it runs last: whatever path the process leaves by,
	// the pidfile is released and the log says how. Release is idempotent, so
	// the specific outcomes below win.
	defer life.Release("returned")
	defer func() {
		if p := recover(); p != nil {
			detail := fmt.Sprintf("panicked: %v", p)
			life.Logf("%s\n%s", detail, debug.Stack())
			died(detail)
			life.Release(detail)
			fmt.Fprintf(operatorStderr, "ticfac run-epic %s: %s (stack in %s)\n", epicID, detail,
				runlife.Dir(repoDir, liveRun)+"/"+runlife.LogName)
			code = 2
		}
	}()

	finished := make(chan struct{})
	defer close(finished)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-finished:
		case sig := <-signals:
			detail := fmt.Sprintf("stopped by a signal (%s) before the run finished", sig)
			life.Logf("%s", detail)
			died(detail)
			life.Release(detail)
			status := 130
			if sig == syscall.SIGTERM {
				status = 143
			}
			os.Exit(status)
		}
	}()

	result, err := reconciler.Run(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		died(err.Error())
		life.Release("died: " + err.Error())
		return 1
	}
	defer life.Release(string(result.State))
	fmt.Fprintf(stdout, "run %s of epic %s: %s\n%s\n", result.RunID, result.EpicID, result.State, result.Reason)
	for _, tick := range result.Ticks {
		fmt.Fprintf(stdout, "  %-8s %s\n", tick.TickID, tick.State)
	}
	// Not a verdict about the work, so it does not change the exit code —
	// but never silent: the operator who would subscribe to this run has to
	// know there is nothing to subscribe to (tick d6s).
	if result.FeedError != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %s\n", epicID, feedFailureLine(result.RunID, result.FeedError))
	}
	if result.State != "completed" {
		return 1
	}
	return 0
}

// feedFailureLine is the one sentence a run whose feed could not be written
// owes its operator: what failed, that it is not a verdict about the work,
// and what cannot happen as a result — `ticfac events <run-id> --follow` has
// nothing to follow. The run's own records and the report above remain the
// evidence; the feed is a hint, and this is the hint channel saying it is
// deaf (tick d6s).
func feedFailureLine(runID string, err error) string {
	return fmt.Sprintf("the run's event feed could not be written (%v): this is not a verdict about the work — "+
		"the run's records and the report above are the evidence — but nothing will appear under "+
		".ticfac/logs/%s/, so `ticfac events %s --follow` has nothing to follow", err, runID, runID)
}

// budgetLine is the one sentence A12 is about. It says the effective number
// first, because that is the one that will govern, and says what it binds:
// `max_cost_usd` binds a metered credential, and the local subprocess executor
// issues a flat-rate one, so on this host the number is carried and reported
// rather than enforced — the wall clock is what stops a job.
func budgetLine(budget reconcile.Budget) string {
	line := fmt.Sprintf("budget: $%.2f effective", budget.Effective)
	if budget.Clamped {
		line += fmt.Sprintf(" (asked $%.2f, clamped to the deployment ceiling $%.2f)",
			budget.Requested, budget.Ceiling)
	} else if budget.Ceiling > 0 {
		line += fmt.Sprintf(" (asked $%.2f, under the deployment ceiling $%.2f)",
			budget.Requested, budget.Ceiling)
	}
	return line + "; it is issued with every job and reported in every record, and on the local subprocess " +
		"executor it is informational — a flat-rate credential meters nothing, and the wall clock is what bounds a job"
}

// settle releases one attempt nobody can address, on a person's word. See
// internal/reconcile/settle.go for why a person is the next actor at all.
func settle(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("settle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		repo      = fs.String("repo", "", "the checkout the run works in")
		remote    = fs.String("remote", "origin", "the remote holding the run's durable authority")
		branch    = fs.String("branch", "", "the EpicRun integration branch")
		runID     = fs.String("run-id", "", "the run's id")
		runner    = fs.String("runner", os.Getenv("TICFAC_RUNNER"), "claude | codex | pi")
		tier      = fs.String("tier", "", "the tier the released attempt was dispatched at, as for run-epic")
		profiles  = fs.String("profiles", "", "resolve role profiles from this directory")
		stateRoot = fs.String("state-root", "", "where attempt state lives, outside the repository")
		gate      = fs.String("gate", "", "the runners.toml the run's gate is read from")
		release   = fs.String("release", "", "the person releasing the attempt")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 3 {
		fmt.Fprintf(stderr, "ticfac settle: exactly one epic id, tick id and attempt number are required\n")
		return 2
	}
	epicID, tickID := rest[0], rest[1]
	attempt, err := strconv.Atoi(rest[2])
	if err != nil || attempt < 1 {
		fmt.Fprintf(stderr, "ticfac settle: %q is not an attempt number\n", rest[2])
		return 2
	}
	if *release == "" {
		fmt.Fprintf(stderr, "ticfac settle: --release names who is releasing the attempt; a release with no "+
			"author is the clock release Appendix A #11 refuses\n")
		return 2
	}
	if err := reconcile.CheckExecutor(); err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s: %s.\n%v\n", epicID, NoExecutorMessage, err)
		return ExitNoExecutor
	}
	if *runner == "" {
		*runner = "claude"
	}
	tracker, err := tk.New(tk.Options{Dir: *repo})
	if err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s: the tracker is not usable: %v\n", epicID, err)
		return 1
	}

	reconciler, err := reconcile.New(reconcile.Options{
		Repo:              *repo,
		Remote:            *remote,
		EpicID:            epicID,
		RunID:             *runID,
		IntegrationBranch: *branch,
		Owner:             "ticfac",
		Tracker:           tracker,
		NewExecutor:       executorFactory(*runner, *gate),
		Executors:         knownExecutors(),
		ExecStateRoot:     *stateRoot,
		GateConfig:        *gate,
		ProfileDir:        *profiles,
		Tier:              *tier,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s: %v\n", epicID, err)
		return 1
	}

	settled, err := reconciler.Settle(context.Background(), tickID, attempt, *release)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s %s %d: %v\n", epicID, tickID, attempt, err)
		return 1
	}
	if !settled.Recorded {
		fmt.Fprintf(stdout, "attempt %d of %s was already released by %s; nothing was written\n",
			settled.Attempt, settled.TickID, settled.ReleasedBy)
		return 0
	}
	fmt.Fprintf(stdout, "attempt %d of %s (%s) is released by %s, recorded as decision %d of run %s.\n"+
		"The next run dispatches a new attempt; whatever this one committed stays on its own write ref.\n",
		settled.Attempt, settled.TickID, settled.State, settled.ReleasedBy, settled.Decision, settled.RunID)
	return 0
}

// buildInfo is what `version --json` prints. The contract bundle is part of
// the answer: a consumer holding only this executable can ask which contracts
// it was built against, without a checkout.
type buildInfo struct {
	Ticfac           string `json:"ticfac"`
	ContractBundle   string `json:"contract_bundle"`
	TicksRepository  string `json:"ticks_repository"`
	TicksRef         string `json:"ticks_ref"`
	ContractsPinPath string `json:"contracts_pin"`
}

func version(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print machine-readable output")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	info, err := BuildInfo()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac version: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(stderr, "ticfac version: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "ticfac %s\ncontract bundle %s (%s@%s)\n",
		info.Ticfac, info.ContractBundle, info.TicksRepository, info.TicksRef)
	return 0
}

// BuildInfo reads the embedded pin and manifest. They are embedded, not read
// from disk, so what the binary reports and what it was compiled against
// cannot disagree.
func BuildInfo() (buildInfo, error) {
	var pin struct {
		BundleVersion string `json:"bundleVersion"`
		Repository    string `json:"repository"`
		Ref           string `json:"ref"`
	}
	if err := json.Unmarshal(ticfac.PinJSON, &pin); err != nil {
		return buildInfo{}, fmt.Errorf("the embedded contracts.pin.json is unreadable: %w", err)
	}
	var bundle struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(ticfac.BundleJSON, &bundle); err != nil {
		return buildInfo{}, fmt.Errorf("the embedded contracts/bundle.json is unreadable: %w", err)
	}
	if bundle.Version != pin.BundleVersion {
		return buildInfo{}, fmt.Errorf(
			"this build embeds bundle %s and pins %s — they were compiled from a tree that "+
				"had not been synced", bundle.Version, pin.BundleVersion)
	}

	return buildInfo{
		Ticfac:           Version,
		ContractBundle:   bundle.Version,
		TicksRepository:  pin.Repository,
		TicksRef:         pin.Ref,
		ContractsPinPath: "contracts.pin.json",
	}, nil
}
