package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// One resolver for the two spellings a run is addressed by. `ticfac run 6in`
// starts the run `epic-6in`, and every surface that follows it — status,
// events, watch — used to take only the second spelling and answer the first
// with a confident negative ("run 6in: not_running", "no feed for run 6in")
// while epic-6in was alive. An operator who types the epic id they started
// the run with is right, and so is one who types the run id the run's own
// lines print; every command resolves both through the functions below, so
// the two spellings cannot mean different things on different commands.

// epicIDOfArg is the epic id an argument names, whichever spelling was
// typed: the bare epic id, or the run id `epic-<id>` the run's own output
// prints. The prefix is stripped, never doubled into epic-epic-<id>.
func epicIDOfArg(arg string) string {
	return strings.TrimPrefix(arg, epicRunIDPrefix)
}

// runIDOfEpic is the local run id `run-epic` derives from an epic id.
func runIDOfEpic(epicID string) string {
	return epicRunIDPrefix + epicID
}

// runSpellings are the run ids an argument may name, in the order they are
// preferred: the argument exactly as typed first (a run literally named what
// was typed is that run), then the run id of the epic it names.
func runSpellings(arg string) []string {
	spellings := []string{arg}
	if epicID := epicIDOfArg(arg); epicID != "" && runIDOfEpic(epicID) != arg {
		spellings = append(spellings, runIDOfEpic(epicID))
	}
	return spellings
}

// runResolution is the run a <run-id> argument names.
type runResolution struct {
	// RunID is the id every command answers for: the spelling that names an
	// existing run, or — when none does — the run id `ticfac run` would
	// start for the epic, so a run started after the command (or one the
	// factory hosts for the epic) is still found under it.
	RunID string
	// Known says a spelling named a run this checkout or machine holds: a
	// run directory (its feed, its pidfile, its log) or a registration. A
	// cloud run id is known by its shape; the factory answers for it.
	Known bool
	// Tried are the spellings that were looked for, for the message that
	// says none of them names a run.
	Tried []string
}

// resolveRunArg resolves a <run-id> argument against the runs repo holds:
// an exact existing run first, then the epic spelling. It never probes
// liveness — that is the command's own question, asked of the id resolved.
func resolveRunArg(repo, arg string) runResolution {
	if looksLikeCloudRunID(arg) {
		return runResolution{RunID: arg, Known: true, Tried: []string{arg}}
	}
	spellings := runSpellings(arg)
	for _, spelling := range spellings {
		if localRunExists(repo, spelling) {
			return runResolution{RunID: spelling, Known: true, Tried: spellings}
		}
	}
	return runResolution{RunID: spellings[len(spellings)-1], Tried: spellings}
}

// localRunExists says a run id names a run here: its directory under
// .ticfac/logs (where the feed, run.pid and run.log live) stands, or the
// machine's registry holds a registration for it.
func localRunExists(repo, runID string) bool {
	if info, err := os.Stat(runlife.Dir(repo, runID)); err == nil && info.IsDir() {
		return true
	}
	if _, ok, err := runregistry.Lookup(runID); err == nil && ok {
		return true
	}
	return false
}

// unknownRunMessage is the answer for an argument no spelling resolved: it
// names every spelling looked for and the command that lists the runs there
// are — never "not running", a verdict on a run nobody found.
func unknownRunMessage(command, repo, arg string, r runResolution) string {
	return fmt.Sprintf("ticfac %s: no run answers to %s in %s — looked for %s (a run id, or an epic id whose "+
		"run is epic-<epic-id>). Run `ticfac` to list the runs this machine knows, or `ticfac run %s` to start "+
		"the epic's run",
		command, arg, repo, strings.Join(r.Tried, " and "), epicIDOfArg(arg))
}
