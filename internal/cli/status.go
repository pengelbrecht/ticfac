package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
)

// `ticfac status <run-id>` answers the question every stall in the ticks pwp
// production run came down to: is this run alive? The operator's answer that
// day was `pgrep -f "ticfac run-epic"`, which matched the operator's own
// watcher and reported a dead run alive for ten minutes. Liveness is a fact
// ticfac reports now, not a pattern each watcher improvises (tick udp).
//
// Since tick 7zs it answers the question liveness never did — is the run
// GETTING anywhere? For each in-flight attempt, whose worktree still stands
// in this repo, it reports how long since the attempt's branch last moved
// and its worktree last changed: two measured facts, a reason to look, and
// never a verdict. The gap does not change the exit code — that stays
// liveness's answer alone, so a watcher loops over it exactly as before.
//
// It exits 0 only while the run is alive, so a watcher is a loop over the exit
// code — `while ticfac status <run-id> >/dev/null; do sleep 15; done` — with
// nothing to parse and nothing to match. --json is the same answer for a
// program that wants the reason, the recorded process, the last feed event
// and the attempts' gaps.
func statusCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	asJSON := fs.Bool("json", false, "print the full status as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac status: exactly one run id is required\n")
		return 2
	}
	if *repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 2
		}
		*repo = wd
	}

	status := runlife.Probe(*repo, rest[0], time.Now())
	if *asJSON {
		raw, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "%s\n", raw)
	} else {
		fmt.Fprintf(stdout, "run %s: %s — %s\n", status.RunID, status.State, status.Reason)
		if status.LastEvent != nil {
			tick := "-"
			if status.LastEvent.TickID != nil {
				tick = *status.LastEvent.TickID
			}
			fmt.Fprintf(stdout, "last event %s ago: %s %s %s\n", status.EventAge, status.LastEvent.Stage, tick, status.LastEvent.Detail)
		}
		// One line per in-flight attempt, both facts on it, the same numbers
		// the feed's stall line carries — so the two surfaces a watcher reads
		// cannot disagree about what was measured. "?" is the honest answer
		// for a fact this attempt cannot show (tick 7zs).
		for _, a := range status.Attempts {
			fmt.Fprintf(stdout, "attempt %d of %s: branch %s last moved %s ago; worktree %s last changed %s ago\n",
				a.Attempt, a.TickID, a.Branch, gapOf(a.BranchIdle), a.Worktree, gapOf(a.WorktreeIdle))
		}
	}
	if status.State == runlife.Alive {
		return 0
	}
	return 1
}

// gapOf renders one measured gap for the status line, "?" when the fact
// could not be read — a watcher sent at a number that was never measured is
// sent nowhere. Rounded to seconds: the text surface is for a person, and
// the milliseconds live in the JSON.
func gapOf(d *runprogress.Duration) string {
	if d == nil {
		return "?"
	}
	return d.Round(time.Second).String()
}
