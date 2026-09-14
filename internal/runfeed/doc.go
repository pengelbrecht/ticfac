// Package runfeed is the run event feed: one append-only JSONL stream per
// run, at `.ticfac/logs/<run-id>/events.jsonl`, written locally by the
// reconciler and subscribed to by anyone who is not the run.
//
// It is implemented exactly as contracts/run-event-feed.json specifies it,
// and the fixture is the second reader of the line shape: this package's
// tests validate the bytes it writes against the schema vendored there, and
// the golden and negative line documents in the fixture are replayed against
// this package's own parser. Two spellings of one line schema is the drift
// the bundle exists to catch.
//
// # A line is a hint about WHEN TO LOOK, never "the work is finished"
//
// The feed exists because nothing outside a run could learn that it finished:
// a worker slept blind in a loop for runs that finished in seconds, and three
// hand-rolled watchers were each wrong in a different way. The feed is the
// wake-up — the signal. The verdict is durable evidence alone: the commits on
// the attempt branch plus the report with its STATUS line (tick 2xu), which
// is what `collect` reads and what this package never is. A record written by
// the thing that may be gone is not evidence of its liveness (Appendix A2):
// the signal wakes, the evidence decides, and a lost or late line is then
// harmless. A feed that cannot be written must not fail the run for the same
// reason — a run nobody can watch is still a run.
//
// # Exhaust, not a record
//
// The feed is deliberately NOT durable: it is uncommitted exhaust under the
// `.ticfac/logs/` entry contracts/ticfac-run-state.json already carries, like
// the rest of that directory. The durable truth of a run stays on the
// integration branch; a hosted mirror, when one exists, is a subscriber like
// any other and follows this file rather than the run pushing to it.
package runfeed

import (
	"path/filepath"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// SchemaVersion is the line schema's version, `ticfac.run_event.v1`. A
// subscriber that meets a version it does not know refuses the line rather
// than guessing at it, so this number moves only with the contract.
const SchemaVersion = 1

// EventsName is the feed's file name, under the run's exhaust directory.
const EventsName = "events.jsonl"

// Path is where a run's feed lives: `<repo>/.ticfac/logs/<run-id>/events.jsonl`.
// The `<run-id>` segment is the same id that names `.ticfac/runs/<run-id>/`,
// so a subscriber that knows a run's id knows where its feed is without
// asking the run.
func Path(repo, runID string) string {
	return filepath.Join(repo, runstate.Root, "logs", runID, EventsName)
}
