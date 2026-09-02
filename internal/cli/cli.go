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
	"time"

	"github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
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
  ticfac run-epic <epic-id>   run one epic through the reconciler
  ticfac version [--json]     report this build and the contract bundle it serves

run-epic flags:
  --repo <dir>        the checkout attempts branch from (default: cwd)
  --remote <name>     the remote holding the run's durable authority (default: origin)
  --branch <name>     the EpicRun integration branch (default: epic/<epic-id>)
  --base <ref>        what the integration branch is cut from (default: HEAD)
  --run-id <id>       the run's id (default: epic-<epic-id>)
  --owner <name>      who claims a tick in the tracker (default: ticfac)
  --runner <name>     claude | codex | pi (default: $TICFAC_RUNNER, else claude)
  --state-root <dir>  where attempt state lives, OUTSIDE the repository
  --gate <file>       the runners.toml the integrated gate is read from
  --budget <usd>      the budget an operator asks for
  --ceiling <usd>     the deployment ceiling it is clamped to
  --wall <seconds>    the wall clock one job is bounded by
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
	case "version":
		return version(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "ticfac: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func runEpic(args []string, stdout, stderr io.Writer) int {
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
		NewExecutor:       reconcile.DefaultExecutor(*runner, nil, 60*time.Second),
		ExecStateRoot:     *stateRoot,
		GateConfig:        *gate,
		WallSeconds:       *wall,
		BudgetUSD:         *budget,
		CeilingUSD:        *ceiling,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		return 1
	}

	result, err := reconciler.Run(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		return 1
	}
	fmt.Fprintf(stdout, "run %s of epic %s: %s\n%s\n", result.RunID, result.EpicID, result.State, result.Reason)
	for _, tick := range result.Ticks {
		fmt.Fprintf(stdout, "  %-8s %s\n", tick.TickID, tick.State)
	}
	if result.State != "completed" {
		return 1
	}
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
