package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"strconv"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
)

// ExitHeld is `watch`'s own code: the run ended — or stopped — holding
// something only a person can move, and the alert says which tick and why. It
// is watch's and not the shared cloud table's exitNoRepo: a script branching
// on a watch's exit has branched on watch, and 3 here never means "not in a
// git repository" (tick 0z0).
const ExitHeld = 3

// `ticfac watch <run-id>` is the consumer the run event feed was built for
// (tick 0z0): the feed says a run stopped holding an attempt for a person,
// and nothing read it — the operator noticed by looking, and the run sat. A
// held attempt is rare and important, and the whole point is that the run
// cannot proceed without a decision, so this command exists to make the hold
// a message rather than a silence.
//
// It subscribes exactly as `events --follow` does — open once, follow the
// appends, no interval anywhere — and adds the one thing a non-participant
// could not do before: it says, to a human, when the run ends HOLDING
// something. A `run_held` line names the tick and the attempt (they are on
// the line, in fields) and the refusal's own reason; the alert repeats all
// three and says the command that moves the hold on. The watch keeps
// following until the run reaches its own terminal line, so it never reports
// an end the run did not write — where "its own" is the point the cursor
// below exists to keep honest on a resumed run.
//
// A line is still a hint about when to LOOK, never a verdict: the alert sends
// a person to the durable evidence — the branch, the report, the decisions on
// origin — and the exit code says a decision is needed, not what it is. A run
// whose process died without a terminal line is `ticfac status`'s question,
// not the feed's; a watch that has not returned is following a run that has
// not said it ended.
//
// WHERE the subscription starts is decided from the run's own liveness claim
// (tick usx). The feed is append-only per RUN ID, so a resumed run appends to
// a file a previous, failed incarnation already ended with a terminal line,
// and a watch that replays the standing feed from offset zero reads that
// ending FIRST — it exits at once and reports a failure that already
// happened, even a hold the release has already settled: the same defect
// `events --follow` carried (ticfac tick 55i), in the command built to be
// alerted by it. So:
//
//   - A LIVE process claims the run: an incarnation is in flight, and every
//     terminal line standing in the feed belongs to an earlier one. The watch
//     joins the CURRENT incarnation — everything after the last terminal
//     line, then each line as it lands — which is also the decided run_held
//     semantics: a watch started while the run is already holding reports
//     the hold it joined, because the current incarnation's run_held line
//     stands after the last terminal line and is delivered, not skipped.
//
//   - No live process claims the run — it ended and released, or nobody has
//     claimed it here: the standing feed IS the run's last word, so the watch
//     replays it whole and ends on its terminal line the way it always did.
//     A run about to be resumed has not claimed yet; its previous ending was
//     the truth until the resume, and a watch started in that gap reports
//     that ending rather than an open-ended silence.
func watchCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac watch: exactly one run id is required\n")
		return 2
	}
	runID := rest[0]
	if *repo == "" {
		var err error
		*repo, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac watch: %v\n", err)
			return 1
		}
	}
	path := runfeed.Path(*repo, runID)

	// The run's own liveness claim, asked once up front: it decides both
	// whether there is anything to watch at all and, below, where the
	// subscription starts.
	probe := runlife.Probe(*repo, runID, time.Now())

	// A watcher may be started beside the run it watches, before the feed's
	// first line exists — that is the good case, and waiting is the point. A
	// LIVE pidfile is the run's own claim that it will write one, so it is
	// what tells "too early to watch" from "nothing to watch here"; a run
	// with neither a feed nor a live claim never ran on this checkout.
	if _, err := os.Stat(path); errors.Is(err, iofs.ErrNotExist) {
		if probe.State != runlife.Alive {
			fmt.Fprintf(stderr, "ticfac watch: no feed for run %s at %s — and no live process claims the run "+
				"here, so there is nothing to watch. %s\n", runID, path, probe.Reason)
			return 1
		}
	}

	// The subscription, and the alert it exists to raise. The state below is
	// written only from the callback Follow runs on its own goroutine of
	// control — Follow is synchronous (one loop, one callback), so no lock is
	// needed around held and terminal.
	held := false
	terminal := ""
	followCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	print := func(event runfeed.Event) {
		who := "run"
		if event.TickID != nil && *event.TickID != "" {
			who = *event.TickID
			if event.Attempt != nil {
				who = fmt.Sprintf("%s#%d", who, *event.Attempt)
			}
		}
		fmt.Fprintf(stdout, "%s %-12s %s: %s\n", clockOf(event.At), who, event.Stage, event.Detail)
		if event.Stage == reconcile.StageRunHeld {
			// The line the whole command exists for, said to a human: which
			// tick, which attempt, why — all read off the line's own fields,
			// never out of its prose — and the command that moves the hold on.
			held = true
			tick, attempt := "-", "-"
			if event.TickID != nil {
				tick = *event.TickID
			}
			if event.Attempt != nil {
				attempt = strconv.Itoa(*event.Attempt)
			}
			fmt.Fprintf(stderr, "\nticfac watch: run %s is HOLDING tick %s (attempt %s) for a person:\n%s\n"+
				"Nothing proceeds until somebody decides. Release it with `ticfac settle <epic-id> %s %s "+
				"--release \"<who>\"` — add --carry-work to base the next attempt on the commits the released "+
				"attempt left — or answer what the tick is waiting for. The evidence is on the integration "+
				"branch, not in this line.\n\n",
				runID, tick, attempt, event.Detail, tick, attempt)
		}
		if event.Stage == reconcile.StageRunFinished || event.Stage == reconcile.StageRunDied {
			// The run's own last word ends the watch: a watcher must not
			// outlive the run it watches, and it must not decide an end the
			// run did not write. What the terminal line SAYS is the line a
			// person reads above; the exit code below is only ever "ended" or
			// "holding for a person", never a verdict about the work.
			terminal = event.Stage
			cancel()
		}
	}
	// Where the subscription starts (tick usx): offset zero replays the whole
	// standing feed; a live run's cursor is just past the last terminal line,
	// so the watch joins the current incarnation and no earlier one. A
	// terminal line read here is history by construction — it stands BEFORE the
	// cursor — so the watch can still end only on a terminal line the current
	// incarnation writes (or, with no live claim, the run's own last word).
	cursor := int64(0)
	if probe.State == runlife.Alive {
		located, err := runfeed.ReadLocated(path)
		switch {
		case errors.Is(err, iofs.ErrNotExist):
			// The live run has not written its first line yet: everything it
			// will say is still to come, from offset zero.
		case err != nil:
			fmt.Fprintf(stderr, "ticfac watch: %v\n", err)
			return 1
		default:
			for _, line := range located {
				if line.Stage == reconcile.StageRunFinished || line.Stage == reconcile.StageRunDied {
					cursor = line.End
				}
			}
		}
	}
	if err := runfeed.FollowFrom(followCtx, path, cursor, print); err != nil {
		fmt.Fprintf(stderr, "ticfac watch: %v\n", err)
		return 1
	}
	if terminal == "" {
		// The subscription was interrupted before the run wrote its terminal
		// line — Ctrl-C, or the caller's context. That is neither "ended" nor
		// an error, and it is not exit 0: a caller waiting on this command
		// must not read an interrupted watch as a finished run.
		fmt.Fprintf(stderr, "ticfac watch: the watch was interrupted before run %s said it ended; "+
			"`ticfac status %s` asks whether it is still alive\n", runID, runID)
		if held {
			return ExitHeld
		}
		return 1
	}
	if held {
		return ExitHeld
	}
	return 0
}

// clockOf is the line's own time, as a person reads it. A stamp that does not
// parse is printed raw rather than guessed at — the writer's clock is the
// only clock there is.
func clockOf(stamp string) string {
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return stamp
	}
	return at.Format("15:04:05")
}
