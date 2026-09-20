package cli

// The transport half of "watch a run live from anywhere" (tick k7p): where a
// run's feed is read from, when the run may not be on this machine.
//
// A LOCAL run's feed is the append-only file the reconciler writes under
// .ticfac/logs/; a CLOUD run publishes the SAME lines, against the SAME
// contract, as R2 segments the factory serves at
// GET /api/runs/<run-id>/events. The operator should not have to know which
// host a run is on, so [feedSource] decides — this checkout when it holds
// the run's feed, else the factory when it knows the run — and every command
// that follows a run ([events], [watch], the [status] table) reads through
// the same [runfeed.Source] and the same one subscription loop,
// runfeed.FollowSource. There is deliberately no second implementation of
// "follow a run" anywhere: two is exactly the drift b9w and 5bp each had to
// settle, and the transport is the half this tick exists for.
//
// The transport choice itself (the tick's item 2, weighed not prescribed):
// a plain authenticated read of the standing stream with byte totals, the
// same shape `cloud logs -f` already proved follows a live run from
// anywhere. An SSE or WebSocket endpoint would be live-er and would cost a
// long-lived connection surface the factory has no other use for; the
// run-state store on the branch is durable but coarse AND would put exhaust
// on the integration branch, which the feed's own contract refuses. A
// bounded read of the run's own artifact prefix costs a run nothing and is
// readable by any client that can issue a GET.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// defaultCloudFeedInterval is how often a --follow of a CLOUD run's feed asks
// the factory again — the same cadence `cloud logs -f` asks at, for the same
// reason: each poll is one remote read, and the feed's own contract says the
// follower's read cadence is the follower's implementation detail, never a
// guess about the work. A LOCAL feed is followed at runfeed.FollowTick, the
// file-stat cadence the local subscription has always used.
const defaultCloudFeedInterval = defaultCloudLogsInterval

// cloudFeedSource is a cloud run's feed as one [runfeed.Source]: the standing
// segment stream the factory serves, read as bytes a cursor walks.
//
// Transient read failures are reported and retried rather than ending the
// subscription — a follow is a long-lived read of a factory that may be
// redeploying under it, the same policy `cloud logs -f` carries. A route's
// own refusal (an unknown run, a deployment with no artifacts bucket) IS
// terminal: it is an answer about the feed, not a blip on the way to one.
type cloudFeedSource struct {
	client *cloudClient
	runID  string
	warn   io.Writer

	mu    sync.Mutex
	state string // the run record's own claim, carried from the last read
}

func (s *cloudFeedSource) Where() string {
	return fmt.Sprintf("the factory's run feed for %s", s.runID)
}

// State is the run record's own claim, as the feed route served it: written
// by the Workflow, so it is the Workflow's own durable word about the run —
// the liveness answer a cloud run's status reports (tick k7p item 4).
func (s *cloudFeedSource) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

type cloudFeedResponse struct {
	RunID      string `json:"run_id"`
	Project    string `json:"project"`
	State      string `json:"state"`
	TraceID    string `json:"trace_id"`
	Text       string `json:"text"`
	Bytes      int    `json:"bytes"`
	TotalBytes int    `json:"total_bytes"`
}

func (s *cloudFeedSource) ReadAt(ctx context.Context, cursor int64) ([]byte, int64, error) {
	path := "/api/runs/" + url.PathEscape(s.runID) + "/events"
	data, err := s.client.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		// A redeploy mid-read is one failed poll, not the end of the feed.
		// The warning is printed by the source (which holds the warn
		// writer) rather than swallowed, so a follower that silently kept
		// going after the feed became unreadable is not what this is.
		var apiErr cloudAPIError
		if errors.As(err, &apiErr) {
			return nil, 0, err
		}
		fmt.Fprintf(s.warn, "# the factory could not be read: %v\n", err)
		return nil, cursor, nil
	}
	var response cloudFeedResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	s.state = strings.TrimSpace(response.State)
	s.mu.Unlock()

	size := int64(response.TotalBytes)
	if size == 0 && response.Bytes > 0 {
		size = int64(response.Bytes)
	}
	text := response.Text
	if int64(len(text)) != size {
		// The route serves the whole feed; a mismatch is a read that
		// bounded it — reported and answered honestly at the bytes that
		// did arrive, never guessed past.
		fmt.Fprintf(s.warn, "# the feed read for %s carried %d bytes against a %d-byte standing size; following only what arrived\n",
			s.runID, len(text), size)
		size = int64(len(text))
	}
	if size <= cursor {
		return nil, size, nil
	}
	return []byte(text[cursor:]), size, nil
}

// feedSource picks where run runID's feed is read from: this checkout when it
// holds the run's feed, else the factory when the run's own id names a cloud
// run. The run's ID names its host — a local run is named by its epic id, a
// cloud one by `run_` plus hex (newRunID in the factory's runs.ts) — so a
// local id is never asked of the factory, and a cloud id is never answered
// by a checkout it could not have run in. A local feed standing here is the
// run's own; everything else is decided by the id's shape.
//
// When no factory is configured, or the factory does not know the run, the
// LOCAL source is returned: the command then answers exactly as it did
// before this tick.
func feedSource(ctx context.Context, repo, runID string, stderr io.Writer) (runfeed.Source, string, error) {
	path := runfeed.Path(repo, runID)
	if _, err := os.Stat(path); err == nil {
		return runfeed.FileSource(path), "local", nil
	}
	if !looksLikeCloudRunID(runID) {
		// An id that cannot be a cloud run's can only be answered here.
		return runfeed.FileSource(path), "local", nil
	}

	// A truncated cloud run id is resolved before any read is made, the
	// same rule `cloud logs` holds (tick c5i: a confident negative that is
	// true of the prefix reads as a verdict on the run).
	resolved, note, err := resolveCloudRunID(ctx, runID)
	if err != nil {
		return nil, "", err
	}
	if note != "" {
		fmt.Fprintln(stderr, note)
	}
	runID = resolved

	client, err := newCloudClient()
	if err != nil {
		// The id names a cloud run and no factory is configured: that is
		// not a missing local feed, and saying so would be a confident
		// negative about a run this checkout never held.
		return nil, "", err
	}
	data, err := client.request(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		// The id names a cloud run: a factory that cannot be asked, or one
		// that does not know the run, is a fact about THIS run's feed, not
		// something a local misspelling should paper over.
		return nil, "", err
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return nil, "", err
	}
	return &cloudFeedSource{client: client, runID: runID, warn: stderr}, "cloud", nil
}

// looksLikeCloudRunID reports whether an id can only name a cloud run:
// `run_` plus hex, whole or truncated (a truncated one is resolved against
// the factory's run index before any read). A local run's id — its epic id —
// never looks like this, which is what keeps a local id from being asked of
// a factory at all.
func looksLikeCloudRunID(id string) bool {
	body, ok := strings.CutPrefix(id, cloudRunIDMarker)
	if !ok || body == "" {
		return false
	}
	for _, digit := range body {
		switch {
		case digit >= '0' && digit <= '9':
		case digit >= 'a' && digit <= 'f':
		case digit >= 'A' && digit <= 'F':
		default:
			return false
		}
	}
	return true
}

// followFeed is the one subscription every command opens: the same
// runfeed.FollowSource loop over whichever source the run lives behind, at
// that source's own read cadence — the follower's implementation detail,
// named once here so no command grows its own. The caller's --interval
// overrides the cloud cadence the way `cloud logs -f` lets an operator ask
// more or less often; zero means the default.
func followFeed(ctx context.Context, source runfeed.Source, kind string, interval time.Duration, cursor int64, fn func(runfeed.Event)) error {
	cadence := runfeed.FollowTick
	if kind == "cloud" {
		cadence = defaultCloudFeedInterval
		if interval > 0 {
			cadence = interval
		}
	}
	return runfeed.FollowSource(ctx, source, cadence, cursor, fn)
}

// feedStanding is the feed as it stands through whichever source serves it:
// every event, with byte ranges, judged by the same strict parsing whether the
// bytes came from a file or a factory. The second return says the feed is
// ABSENT — the run-has-not-written case, which for a local feed is the
// file's own absence and for a cloud one a stream with no line in it yet:
// a run that has not written an event is what a run that has not started
// looks like, on either host.
func feedStanding(ctx context.Context, source runfeed.Source) (located []runfeed.Located, absent bool, err error) {
	chunk, size, err := source.ReadAt(ctx, 0)
	if err != nil {
		return nil, false, err
	}
	if size == 0 && len(chunk) == 0 {
		return nil, true, nil
	}
	located, err = runfeed.ParseLocated(chunk)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", source.Where(), err)
	}
	if len(located) == 0 {
		return nil, true, nil
	}
	return located, false, nil
}

// cloudRunLiveness answers "is this run alive" for a run the Workflow hosts,
// from the Workflow's own state (tick k7p item 4): never from "is there a
// process here", which was the question status could not answer off-host.
//
// Two answers, in the order of their authority:
//
//   - the WORKFLOW itself, read through Cloudflare outside the factory, when
//     the operator's own Cloudflare credentials allow it — the instance's own
//     state is the thing the run's life is.
//   - the run RECORD's state, which the Workflow writes — its own durable
//     claim, and the honest answer when the instance cannot be asked.
//
// The two can disagree, and the disagreement is the finding: a record frozen
// at `running` by a supervisor that never got to write its last word, with
// the instance errored, terminated or gone. That run is not alive, whatever
// its record claims.
type cloudLiveness struct {
	Alive   bool   `json:"alive"`
	State   string `json:"state"`
	Source  string `json:"liveness_source"`
	Reason  string `json:"reason"`
	Current string `json:"current_step,omitempty"`
}

func cloudRunLiveness(ctx context.Context, runID, state string) cloudLiveness {
	state = strings.TrimSpace(state)
	if state == "" {
		// No state is no claim of life, and "unknown" must not read as
		// alive: a record that says nothing is a run nobody vouches for.
		return cloudLiveness{
			Alive:  false,
			State:  "unknown",
			Source: "workflow-record",
			Reason: "the factory's record carries no state for this run, so nothing claims it is alive",
		}
	}
	active := !isFinishedCloudRun(state)
	liveness := cloudLiveness{Alive: active, State: state, Source: "workflow-record"}
	switch {
	case !active:
		liveness.Reason = fmt.Sprintf("the factory's record says %s — written by the Workflow, and a finished run is not alive", state)
	default:
		liveness.Reason = fmt.Sprintf("the factory's record says %s — the Workflow's own durable claim, written by the run itself", state)
	}

	opts, err := cloudSupervisorOptions()
	if err != nil {
		// No Cloudflare credentials: the record's claim is the answer, and
		// its limit is stated rather than hidden — the run may still be
		// checked directly with `ticfac cloud supervisor %s`.
		liveness.Reason += fmt.Sprintf("; the Workflow instance itself could not be asked (%v)", err)
		return liveness
	}
	supervisor, err := factory.ReadSupervisor(ctx, runID, opts)
	if err != nil {
		liveness.Reason += fmt.Sprintf("; the Workflow instance itself could not be asked (%v)", err)
		return liveness
	}
	liveness.Source = "workflow-supervisor"
	liveness.Alive = supervisor.Alive()
	liveness.Current = currentStepName(supervisor)
	switch {
	case supervisor.Alive():
		liveness.Reason = fmt.Sprintf("the Workflow instance is %s — %s", stateOrUnknown(supervisor.Status), supervisor.Explain())
	case active:
		// The record claims life; the instance is gone. The record is
		// written BY the supervisor, so it is frozen at the last value one
		// wrote — nothing is advancing this run.
		liveness.Reason = fmt.Sprintf("the run record says %q, but its Workflow instance is %s: the record is written by the supervisor, so it is frozen at the last value one wrote — nothing is advancing this run",
			state, stateOrUnknown(supervisor.Status))
		if detail := supervisor.Error.String(); detail != "" {
			liveness.Reason += fmt.Sprintf(" (%s)", detail)
		}
	default:
		liveness.Reason = fmt.Sprintf("the Workflow instance is %s and the run record says %s", stateOrUnknown(supervisor.Status), state)
	}
	return liveness
}

func currentStepName(supervisor *factory.Supervisor) string {
	if step := supervisor.CurrentStep(); step != nil {
		return step.Name
	}
	return ""
}

// runLevel reports whether a feed line belongs to the run itself rather than
// to one of its ticks.
func runLevel(event runfeed.Event) bool {
	return event.TickID == nil
}

// cloudRunStillGoing is whether a state is one a live run holds, in the
// factory's own vocabulary (runs.ts ACTIVE_RUN_STATES): starting, running,
// stopping. A run stopping is still a run that exists as work.
func cloudRunStillGoing(state string) bool {
	return !isFinishedCloudRun(state)
}
