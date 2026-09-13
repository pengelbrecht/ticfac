package factory

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The orchestrator image's tk and the entrypoint that drives it ship in one
// bundle, and a deploy is only honest if the two agree.
//
// Once they did not. The image pinned a *released* tk that predated the
// `tk sandbox` subcommands the entrypoint calls, so a real container booted,
// streamed its first lines, and then died with "unknown command: sandbox" — a
// green deploy standing in front of a factory that could never finish a run.
// The list below is DERIVED from the scripts rather than typed out, because
// the failure recurs on every future epic that teaches the entrypoint a new
// subcommand — and a hand-maintained list is exactly the thing that goes
// stale.
//
// THE GATE ITSELF IS THE DOCKERFILE'S, decided at the ticfac move (ticks tick
// ek7). In ticks, `tk factory deploy` could ask a caller-supplied predicate
// whether THIS binary implements every entrypoint subcommand — a fast
// preflight, meaningful because the image built tk from the same source as
// the deploying binary. ticfac does not ship tk: there is no cobra tree to
// ask, and shelling out to a `tk` on PATH would test a DIFFERENT binary from
// the one the image actually runs. A false "it has the subcommand" from a
// developer's newer tk is worse than a late one from the image build, so the
// preflight is gone by decision. What remains is the check that runs against
// the tk that will ACTUALLY be in the image: the last layer of the Dockerfile
// copies this list into the build context and runs every line against the tk
// it just built, so an image whose tk predates an entrypoint subcommand
// fails to build instead of booting a container that dies mid-run. If a fast
// preflight is ever wanted back, it must run against the PINNED ticks ref
// (factory.pin.json), never against PATH.

// RequiredTkCommandsFile is the derived list, shipped in the image build
// context so the image build itself can assert the same thing against the tk
// it actually produced. It is committed rather than generated at deploy time
// so a plain `docker build cloud/sandbox` proves the same property, and
// TestRequiredTkCommandsFileMatchesTheEntrypoint keeps it from drifting.
const RequiredTkCommandsFile = "required-tk-commands"

// entrypointScripts are the shell scripts the image installs and runs.
//
// All of them, in both roles: one image plays orchestrator and per-tick worker
// (tick x3v), and common.sh — which both entrypoints source — is where most of
// the `tk` invocations now live. A scanner that read only the orchestrator's
// file would let a worker-only subcommand ship in an image whose tk does not
// have it, which is the exact failure this gate exists to make impossible.
var entrypointScripts = []string{"entrypoint.sh", "worker.sh", "common.sh", "preflight.sh"}

// tkInvocation matches `tk <sub> [<sub> [<sub>]]` in COMMAND position: at the
// start of a line, inside a command substitution, after a pipeline or list
// operator, or after exec/then/do/else. Prose that merely mentions a command
// — a die message telling the operator to run `tk factory setup`, say — is
// quoted or commented, so it never sits in one of those positions.
//
// The chain stops at the first word that is not a bare lowercase token, which
// is what keeps flags (`--root`), redirections (`2>/dev/null`) and operators
// (`||`) out of it.
var tkInvocation = regexp.MustCompile(
	`(?m)(?:^|\$\(|[;&|]|\bexec[ \t]+|\bthen[ \t]+|\bdo[ \t]+|\belse[ \t]+)[ \t]*` +
		`tk[ \t]+((?:[a-z][a-z0-9-]*)(?:[ \t]+[a-z][a-z0-9-]*){0,2})`)

// commentLine is a whole-line shell comment. Dropping these first is what lets
// the scanner ignore the prose in this repository's heavily commented scripts,
// including the backticked command names in it.
var commentLine = regexp.MustCompile(`(?m)^[ \t]*#.*$`)

// EntrypointTkCommands returns every `tk` subcommand chain the image's run
// scripts invoke, as space-joined paths ("sandbox environment"), sorted and
// deduplicated.
//
// It reads the embedded image context, which this build does not carry yet:
// the payload lands with ticks tick b3a (see the seam at the top of
// bundle.go), and until it does this returns the missing-payload stop.
func EntrypointTkCommands() ([]string, error) {
	seen := make(map[string]bool)
	for _, name := range entrypointScripts {
		data, err := ReadSandboxFile(name)
		if err != nil {
			return nil, fmt.Errorf("reading the embedded %s: %w", name, err)
		}
		script := commentLine.ReplaceAllString(string(data), "")
		for _, m := range tkInvocation.FindAllStringSubmatch(script, -1) {
			seen[strings.Join(strings.Fields(m[1]), " ")] = true
		}
	}
	commands := make([]string, 0, len(seen))
	for c := range seen {
		commands = append(commands, c)
	}
	sort.Strings(commands)
	return commands, nil
}
