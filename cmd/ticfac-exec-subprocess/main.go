// Command ticfac-exec-subprocess is the local subprocess executor behind
// ticfac's four-operation job protocol: start, inspect, cancel and collect
// over a git worktree per attempt and a headless runner process.
//
// One record in on stdin, one record out on stdout — the argv
// contracts/job-protocol.json prescribes for every executor
// (`ticfac-exec-<name> <operation>`), so a reconciler that can address one
// executor can address them all.
package main

import (
	"os"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

func main() {
	os.Exit(subprocess.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
