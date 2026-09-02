// Package cli is ticfac's command surface.
//
// Phase 1 ships two commands and one refusal. `run-epic` is the entry point
// the reconciler will hang off (SPEC §12 Phase 1); until an executor is
// configured it FAILS CLOSED rather than doing something plausible, because a
// run that half-starts is the failure mode Appendix A was written out of.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/pengelbrecht/ticfac"
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
const NoExecutorMessage = "no executor configured"

const usage = `ticfac — execution and orchestration for ticks

usage:
  ticfac run-epic <epic-id>   run one epic through the reconciler
  ticfac version [--json]     report this build and the contract bundle it serves
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
		return runEpic(args[1:], stderr)
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

func runEpic(args []string, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintf(stderr, "ticfac run-epic: exactly one epic id is required\n")
		return 2
	}

	// The refusal, and the reason it is a refusal rather than a no-op: there
	// is no host behind the four-operation protocol yet
	// (contracts/job-protocol.json), so there is nothing that could start,
	// inspect, cancel or collect a job. Reporting success here would be the
	// first of Appendix A's failures — a run recorded as done that never ran.
	fmt.Fprintf(stderr, "ticfac run-epic %s: %s.\n", args[0], NoExecutorMessage)
	fmt.Fprintf(stderr, "This build has no executor behind the start/inspect/cancel/collect protocol,\n"+
		"so it refuses rather than reporting a run it did not make.\n")
	return ExitNoExecutor
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
