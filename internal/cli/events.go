package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

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
// newEventsCommand builds the cobra command for `events`.
func newEventsCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events <run-id>",
		Short: "a run's event feed: what it did, as it does it",
		Long: `Print the run's event feed — one JSONL line per event, with the
run/tick/attempt identity on every line — or, with --follow, subscribe from
now: each event as it lands, until Ctrl-C.

The one rule this command must not bury in its help text: a line means WORTH
LOOKING NOW, never "the work is finished". The verdict stays with the evidence
on the integration branch; a subscriber that reads run_finished goes and looks.`,
	}
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	follow := fs.Bool("follow", false, "keep the stream open: each event as it lands, from now, until Ctrl-C")
	fromStart := fs.Bool("from-start", false, "with --follow, replay the standing feed first — including the terminal "+
		"events of earlier incarnations of this run id")
	interval := fs.Duration("interval", defaultCloudFeedInterval, "with --follow on a CLOUD run, how often to ask the factory again "+
		"(a local feed is read at file-follow cadence)")
	asJSON := fs.Bool("json", false, "print the standing feed as one versioned document (ticfac.events.v1); with --follow it refuses — a live stream is JSONL lines, not one document")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(eventsCommand(c.Context(), args, repo, follow, fromStart, interval, asJSON, stdout, stderr))
	}
	return cmd
}

func eventsCommand(ctx context.Context, args []string, repo *string, follow, fromStart *bool, interval *time.Duration, asJSON *bool, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac events: exactly one run id is required\n")
		return 2
	}
	if *asJSON && *follow {
		// The same refusal status --json gives its --follow: one document is
		// one answer, and a subscription that stays open printing as things
		// land is a different shape — the plain --follow's JSONL lines, each
		// a versioned event, ARE the streaming answer.
		fmt.Fprintf(stderr, "ticfac events: --json prints one document, the feed as it stands — a live stream is "+
			"not one document. Read it with `ticfac events %s --json`, follow it with `ticfac events %s --follow` "+
			"(its lines are versioned JSONL)\n", rest[0], rest[0])
		return 2
	}
	runID := rest[0]
	if parseOnly {
		return 0
	}
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
	source, kind, resolved, err := feedSource(ctx, *repo, runID, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac events: %v\n", err)
		return 1
	}
	// The events answer for the run the id names, resolved: an epic id that
	// named a factory run answers for that run's own id (tick nyi).
	runID = resolved

	print := func(event runfeed.Event) {
		line, err := json.Marshal(event)
		if err != nil {
			return
		}
		fmt.Fprintf(stdout, "%s\n", line)
	}

	// Without --follow, the feed as it stands is the answer: print it and
	// stop. With --json, the same standing feed is ONE document — every
	// event inside it, each already a versioned JSON object, wrapped in one
	// answer an agent parses without splitting lines. With --follow, the
	// subscription starts at the cursor below and prints each line ONCE — the
	// standing feed is not printed first, because this command without
	// --follow is how a subscriber that wants history gets it.
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
		if *asJSON {
			events := make([]runfeed.Event, 0, len(located))
			for _, line := range located {
				events = append(events, line.Event)
			}
			doc := struct {
				agentDoc
				RunID  string          `json:"run_id"`
				Host   string          `json:"host"`
				Events []runfeed.Event `json:"events"`
			}{
				agentDoc: agentDoc{Schema: agentSchemaID("events"), State: agentStateDone},
				RunID:    runID,
				Host:     kind,
				Events:   events,
			}
			if err := emitAgentJSON(stdout, doc); err != nil {
				fmt.Fprintf(stderr, "ticfac events: %v\n", err)
				return 1
			}
			return 0
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
