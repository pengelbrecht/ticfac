package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The subscription surface of the run event feed (tick u9l, epic av8; the
// contract is contracts/run-event-feed.json): the one thing a
// NON-PARTICIPANT — a worker waiting on a run it started, an orchestrator
// watching one, a dashboard — can subscribe to instead of guessing an
// interval or polling the durable records.
//
// `ticfac events <run-id>` prints the feed as it stands; `--follow` opens it
// once and streams each event as it lands, FROM NOW — the standing feed is
// what the command without --follow prints, and replaying it under --follow
// would replay the terminal events of earlier incarnations of the same run
// id: the feed is append-only per RUN ID, so a resumed run appends to a file
// a previous, failed run already ended, and a follower that starts at offset
// zero sees that run_finished FIRST and stops on it, reporting a failure that
// already happened and is no longer true (ticfac tick 55i). `--from-start`
// opts back into the replay deliberately.
//
// The feed is append-only and carries run/tick/attempt identity on every
// line, and the one rule this command must not bury in its help text: a line
// means WORTH LOOKING NOW, never "the work is finished". The verdict stays with
// the evidence on the integration branch; a subscriber that reads
// run_finished goes and looks.
func eventsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	follow := fs.Bool("follow", false, "keep the stream open: each event as it lands, from now, until Ctrl-C")
	fromStart := fs.Bool("from-start", false, "with --follow, replay the standing feed first — including the terminal "+
		"events of earlier incarnations of this run id")
	interval := fs.Duration("interval", defaultCloudFeedInterval, "with --follow on a CLOUD run, how often to ask the factory again "+
		"(a local feed is read at file-follow cadence)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac events: exactly one run id is required\n")
		return 2
	}
	runID := rest[0]
	if *repo == "" {
		var err error
		*repo, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac events: %v\n", err)
			return 1
		}
	}

	// Where the run's feed lives is decided by the run, not the operator
	// (tick k7p): this checkout when it holds the feed, else the factory when
	// it knows the run. Either way the SAME loop follows it, and either way
	// the lines are the same versioned schema — a cloud run's feed is
	// indistinguishable from a local one's, by contract and by test.
	source, kind, err := feedSource(ctx, *repo, runID, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac events: %v\n", err)
		return 1
	}

	print := func(event runfeed.Event) {
		line, err := json.Marshal(event)
		if err != nil {
			return
		}
		fmt.Fprintf(stdout, "%s\n", line)
	}

	// Without --follow, the feed as it stands is the answer: print it and
	// stop. With --follow, the subscription starts at the cursor below and
	// prints each line ONCE — the standing feed is not printed first, because
	// this command without --follow is how a subscriber that wants history
	// gets it.
	if !*follow {
		located, absent, err := feedStanding(ctx, source)
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "ticfac events: %v\n", err)
			return 1
		case absent:
			// The same fact on either host: nothing has landed. A local feed
			// names its path; a cloud one names the run a factory that knows
			// it still has no line from.
			if kind == "local" {
				fmt.Fprintf(stderr, "ticfac events: no feed for run %s at %s — the run has not written an event, which is what a run that has not started looks like\n", runID, runfeed.Path(*repo, runID))
			} else {
				fmt.Fprintf(stderr, "ticfac events: the factory knows run %s, but the run has not written an event — which is what a run that has not started looks like\n", runID)
			}
			return 1
		}
		for _, line := range located {
			print(line.Event)
		}
		return 0
	}

	// The subscription, from NOW: the cursor is the feed's standing size, so
	// the follower is shown only what the current incarnation of the run
	// writes from here on — a resumed run appends to the same file an earlier
	// one already ended, and replaying that ending is the defect --from-start
	// exists to name (ticfac tick 55i). A feed that does not exist yet is the
	// run-has-not-written case: its cursor is zero, everything is still to
	// come. Errors end the stream rather than being swallowed: a follower that
	// quietly kept going after the feed became unreadable would be one more
	// watcher that reports nothing and looks alive.
	cursor := int64(0)
	if !*fromStart {
		located, _, err := feedStanding(ctx, source)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac events: %v\n", err)
			return 1
		}
		if len(located) > 0 {
			cursor = located[len(located)-1].End
		}
	}
	if err := followFeed(ctx, source, kind, *interval, cursor, print); err != nil {
		fmt.Fprintf(stderr, "ticfac events: %v\n", err)
		return 1
	}
	return 0
}
