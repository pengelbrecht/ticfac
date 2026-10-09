// Command gate-touched is the integrated gate's full-suite check: it runs the
// complete (non-short) test suites of the Go packages whose files the gated
// tick changed, and of the packages whose code or tests import them.
//
// The gate declares it in .tick/runners.toml beside the whole-repo short
// suite, spelled out with the same -timeout and -parallel numbers:
//
//	go-touched = { command = "go run ./cmd/gate-touched -timeout 45m -parallel 12", ... }
//
// so a reader sees there both halves of the Go gate and the numbers each
// runs with. Run by a person, with no gate above it, it diffs the working
// branch against origin/main instead — the same question, for the tree a
// person is about to push.
//
// The selection — which packages a diff touches, and which packages reach
// them over the import graph — is internal/gatescope, which this command
// only reads flags for and runs.
package main

import (
	"flag"
	"os"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/gatescope"
)

func main() {
	timeout := flag.String("timeout", "45m", "go test's per-package bound, as the whole-repo suite declares")
	parallel := flag.String("parallel", "12", "the tests' parallelism, as the whole-repo suite declares")
	leave := flag.String("leave-to-ci", "",
		"comma-separated import paths whose full suites this gate declares too expensive to pay per tick; "+
			"each is named in the output and left to CI")
	flag.Parse()
	os.Exit(gatescope.Run(gatescope.Options{Timeout: *timeout, Parallel: *parallel, LeaveToCI: list(*leave)},
		os.LookupEnv, gatescope.NewShell(), os.Stdout))
}

// list splits a comma-separated flag value into its names, empty when the
// flag was not set.
func list(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(value, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
