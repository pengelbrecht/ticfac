package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	iofs "io/fs"
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
// once and streams each event as it lands, which is the subscription. The
// feed is append-only and carries run/tick/attempt identity on every line,
// and the one rule this command must not bury in its help text: a line means
// WORTH LOOKING NOW, never "the work is finished". The verdict stays with
// the evidence on the integration branch; a subscriber that reads
// run_finished goes and looks.
func eventsCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	follow := fs.Bool("follow", false, "keep the stream open: each event as it lands, until Ctrl-C")
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

	path := runfeed.Path(*repo, runID)
	print := func(event runfeed.Event) {
		line, err := json.Marshal(event)
		if err != nil {
			return
		}
		fmt.Fprintf(stdout, "%s\n", line)
	}

	// Without --follow, the feed as it stands is the answer: print it and
	// stop. With --follow, Follow does the whole job — the standing lines from
	// offset zero and then each line as it lands — so they are not printed
	// twice, and a feed that does not exist yet is a run that has not written,
	// which is what following waits for.
	if !*follow {
		events, err := runfeed.Read(path)
		if err != nil {
			if errors.Is(err, iofs.ErrNotExist) {
				fmt.Fprintf(stderr, "ticfac events: no feed for run %s at %s — the run has not written an event, which is what a run that has not started looks like\n", runID, path)
			} else {
				fmt.Fprintf(stderr, "ticfac events: %v\n", err)
			}
			return 1
		}
		for _, event := range events {
			print(event)
		}
		return 0
	}

	// The subscription. Errors end the stream rather than being swallowed: a
	// follower that quietly kept going after the feed became unreadable would
	// be one more watcher that reports nothing and looks alive.
	if err := runfeed.Follow(ctx, path, print); err != nil {
		fmt.Fprintf(stderr, "ticfac events: %v\n", err)
		return 1
	}
	return 0
}
