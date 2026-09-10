// Command ticfac runs epics: the reconciler over the ticks tracker and Git,
// and the executors behind the four-operation job protocol.
package main

import (
	"os"

	"github.com/pengelbrecht/ticfac/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
