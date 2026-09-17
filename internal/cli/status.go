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
// Since tick q1e it also reports, for an in-flight attempt, the fact the run
// has already said on its feed: this attempt's wall clock FIRED, and it has
// not settled — the moment the run stops making progress on its own. The
// fact is the feed line's typed stage and attempt identity, never its
// prose, and never its position: the firing joins the attempt's own line
// even when later events follow, because a watcher reading status while the
// run polls on cannot be expected to have seen the line when it was last.
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
			line := fmt.Sprintf("attempt %d of %s: branch %s last moved %s ago; worktree %s last changed %s ago",
				a.Attempt, a.TickID, a.Branch, gapOf(a.BranchIdle), a.Worktree, gapOf(a.WorktreeIdle))
			// The attempt's own wall clock firing joins the line by the identity
			// both carry (tick q1e): the run said the bound passed and the
			// attempt is still in flight, so a watcher reading status — not
			// only one watching the feed — is told, with the executor's last
			// word the run put on the line.
			if w := wallClockOf(status.WallClocks, a); w != nil {
				fired := "?"
				if w.FiredAgo != nil {
					fired = gapOf(w.FiredAgo)
				}
				line += fmt.Sprintf("; wall clock fired %s ago", fired)
				if w.Detail != "" {
					line += " — " + w.Detail
				}
			}
			fmt.Fprintf(stdout, "%s\n", line)
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

// wallClockOf finds the one firing that belongs to an attempt, by the tick
// and attempt identity both carry — never by prose, and never by order: a
// firing the run reported for another attempt, or a line that merely names
// the wall clock in its detail, does not join this attempt's line (tick q1e).
func wallClockOf(wall []runlife.WallClock, a runprogress.Attempt) *runlife.WallClock {
	for i := range wall {
		if wall[i].TickID == a.TickID && wall[i].Attempt == a.Attempt {
			return &wall[i]
		}
	}
	return nil
}
