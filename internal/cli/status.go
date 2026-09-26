package cli

// The live status view (tick k7p, item b): the table the operator asked for —
// one line per tick with its stage, its attempt and how long it has been
// there, plus the run's own liveness — refreshed in place until Ctrl-C.
//
// It is `status --follow`, and it works identically against a run on this
// machine and one the Workflow hosts, because everything it shows is read
// through the same [runfeed.Source] selection `events` and `watch` read
// through: the feed's own lines give every tick's stage and the moment it
// landed there, and the liveness line is the same answer one-shot status
// gives — run.pid for a local run, the Workflow's own state for a cloud one.
//
// The refresh cadence is a flag (--interval, default 2s): a table that
// refreshes is the surface, and its rhythm is the operator's own, never a
// guess about the work — the same rule the feed's contract holds for a
// subscription. A run that reaches its own end while watched ends the table:
// the follow's answer is that it ended, and the exit code says the surface
// did its job (0), never a verdict about the work.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
)

// defaultStatusFollowInterval is how often the live table re-renders. Two
// seconds: fast enough that "what is it doing right now" is right, slow
// enough that a cloud refresh (one or two factory reads) stays polite.
const defaultStatusFollowInterval = 2 * time.Second

// statusFollow renders the live table until the run ends or the caller
// interrupts. One frame per interval; between frames the cursor is moved up
// over the previous frame and the lines rewritten in place.
func statusFollow(ctx context.Context, repo, runID string, interval time.Duration, stdout, stderr io.Writer) int {
	source, kind, err := feedSource(ctx, repo, runID, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac status: %v\n", err)
		return 1
	}
	cloudSource, _ := source.(*cloudFeedSource)
	if interval <= 0 {
		interval = defaultStatusFollowInterval
	}

	// Labels are read once per follow, not once per frame: a frame every two
	// seconds must not spawn tk every two seconds, and a tick added mid-run
	// only costs its label (it shows as its bare id), never its line.
	labels := tickLabels(ctx, repo)

	previous := 0
	for {
		ended, lines, err := renderStatusFrame(ctx, source, cloudSource, kind, repo, runID, labels, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 1
		}
		if previous > 0 {
			// Redraw in place: up over the last frame, clear to the end of
			// the screen, print. A table that scrolls is a log, and a log is
			// what `events --follow` is for.
			fmt.Fprintf(stdout, "\x1b[%dA\r\x1b[J", previous)
		}
		for _, line := range lines {
			fmt.Fprintf(stdout, "%s\n", line)
		}
		previous = len(lines)
		if ended {
			return 0
		}
		select {
		case <-ctx.Done():
			fmt.Fprintf(stderr, "ticfac status: the follow was interrupted; `ticfac status %s` asks whether %s is still alive\n",
				runID, runID)
			return 1
		case <-time.After(interval):
		}
	}
}

// renderStatusFrame builds one frame of the live table: the run's own
// liveness first, then one line per tick — its stage, its attempt, and how
// long it has been where it is. The first return says the run reached its own
// end, which ends the follow.
func renderStatusFrame(ctx context.Context, source runfeed.Source, cloudSource *cloudFeedSource, kind, repo, runID string, labels map[string]string, out io.Writer) (ended bool, lines []string, err error) {
	located, _, err := feedStanding(ctx, source)
	if err != nil {
		return false, nil, err
	}

	var alive bool
	var liveness string
	switch {
	case kind == "cloud":
		state := cloudSource.State()
		answer := cloudRunLiveness(ctx, runID, state)
		alive = answer.Alive
		stateWord := "alive"
		if !alive {
			stateWord = "not alive"
		}
		liveness = fmt.Sprintf("%s — %s", stateWord, answer.Reason)
		ended = !cloudRunStillGoing(state)
	default:
		probe := runlife.Probe(repo, runID, time.Now())
		alive = probe.State == runlife.Alive
		liveness = fmt.Sprintf("%s — %s", probe.State, probe.Reason)
		for _, line := range located {
			if line.Stage == reconcile.StageRunFinished || line.Stage == reconcile.StageRunDied {
				ended = true
			}
		}
	}

	lines = append(lines, fmt.Sprintf("run %s: %s", runID, liveness))

	// One line per tick, in the order the run first mentioned it — the wave
	// order the run dispatches in, and the order a table that refreshes
	// without jumping around needs. The stage, attempt and age on each line
	// are the latest event's own typed fields, never its prose.
	type tickLine struct {
		tickID string
		event  runfeed.Event
	}
	var order []string
	latest := map[string]runfeed.Event{}
	var tries runfeed.Tries
	for _, line := range located {
		tries.Observe(line.Event)
		if runLevel(line.Event) {
			continue
		}
		id := *line.TickID
		if _, seen := latest[id]; !seen {
			order = append(order, id)
		}
		latest[id] = line.Event
	}
	// A tick's line leads with its own TRY and labels the run-wide number as
	// the dispatch number it is (tick h58): "tick w9b attempt 5" read as w9b's
	// fifth try when it was its third.
	for _, id := range order {
		event := latest[id]
		who := "tick " + tickRef(labels, id)
		if event.Attempt != nil {
			try, _ := tries.Of(id, *event.Attempt)
			who = "tick " + reconcile.AttemptLabel(tickRef(labels, id), try, *event.Attempt)
		}
		lines = append(lines, fmt.Sprintf("%s: %s for %s — %s",
			who, event.Stage, ageOf(event.At, time.Now()), event.Detail))
	}

	// The run's own last word, when it has said one at run level: what the
	// run said about ITSELF, with the same age every tick line carries.
	for i := len(located) - 1; i >= 0; i-- {
		if !runLevel(located[i].Event) {
			continue
		}
		lines = append(lines, fmt.Sprintf("run: %s for %s — %s",
			located[i].Stage, ageOf(located[i].At, time.Now()), located[i].Detail))
		break
	}
	return ended, lines, nil
}

// feedTries counts each tick's tries from the run's local feed (tick h58). A
// feed that cannot be read counts nothing, and the lines that asked fall back
// to the dispatch number alone: the feed is exhaust, and an unreadable one may
// cost a line its try but never the line.
func feedTries(repo, runID string) *runfeed.Tries {
	tries := &runfeed.Tries{}
	events, err := runfeed.Read(runfeed.Path(repo, runID))
	if err != nil {
		return tries
	}
	for _, event := range events {
		tries.Observe(event)
	}
	return tries
}

// ageOf says how long ago a stamp was, as a person reads it. A stamp that
// does not parse is "?" rather than a guess — the writer's clock is the only
// clock there is, and a number nobody measured is a number that lies.
func ageOf(stamp string, now time.Time) string {
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return "?"
	}
	return now.Sub(at).Round(time.Second).String()
}

// statusCommand is `ticfac status`: the one-shot liveness answer (and, with
// --follow, the live table above). For a run the Workflow hosts it answers
// from the Workflow's own state, never from "is there a process here" — the
// question status could not answer off-host until tick k7p.
func statusCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	asJSON := fs.Bool("json", false, "print the full status as JSON")
	follow := fs.Bool("follow", false, "keep the status table updated in place, one line per tick, until the run ends or Ctrl-C")
	interval := fs.Duration("interval", defaultStatusFollowInterval, "with --follow, how often the table refreshes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac status: exactly one run id is required\n")
		return 2
	}
	if *asJSON && *follow {
		fmt.Fprintf(stderr, "ticfac status: --json prints one answer; a table that refreshes is not a JSON stream — pipe one frame or follow the other\n")
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
	runID := rest[0]
	if *follow {
		return statusFollow(ctx, *repo, runID, *interval, stdout, stderr)
	}

	status := runlife.Probe(*repo, runID, time.Now())

	// A run the Workflow hosts is answered by the Workflow, and its id says
	// which host that is: `run_` plus hex names a cloud run, and no local
	// pidfile was ever its claim to life.
	if status.State != runlife.Alive && looksLikeCloudRunID(runID) {
		return cloudRunStatus(ctx, runID, *asJSON, stdout, stderr)
	}

	if *asJSON {
		raw, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "%s\n", raw)
	} else {
		fmt.Fprintf(stdout, "run %s: %s — %s\n", status.RunID, status.State, status.Reason)
		// Labels only when a line will name a tick: a run with nothing in
		// flight does not pay for a tk call.
		labels := map[string]string{}
		if len(status.Attempts) > 0 || (status.LastEvent != nil && status.LastEvent.TickID != nil) {
			labels = tickLabels(ctx, *repo)
		}
		if status.LastEvent != nil {
			tick := "-"
			if status.LastEvent.TickID != nil {
				tick = tickRef(labels, *status.LastEvent.TickID)
			}
			fmt.Fprintf(stdout, "last event %s ago: %s %s %s\n", status.EventAge, status.LastEvent.Stage, tick, status.LastEvent.Detail)
		}
		// One line per in-flight attempt, both facts on it, the same numbers
		// the feed's stall line carries — so the two surfaces a watcher reads
		// cannot disagree about what was measured. "?" is the honest answer
		// for a fact this attempt cannot show (tick 7zs).
		//
		// Each attempt is named by the tick's own try first (tick h58), counted
		// from the run's feed; an attempt the feed never showed is named by its
		// dispatch number alone rather than by a guessed try.
		tries := feedTries(*repo, runID)
		for _, a := range status.Attempts {
			try, _ := tries.Of(a.TickID, a.Attempt)
			line := fmt.Sprintf("%s: branch %s last moved %s ago; worktree %s last changed %s ago",
				reconcile.AttemptLabel(tickRef(labels, a.TickID), try, a.Attempt), a.Branch, gapOf(a.BranchIdle), a.Worktree,
				gapOf(a.WorktreeIdle))
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

// cloudRunStatus is the one-shot answer for a run the Workflow hosts: the run
// record's state — the Workflow's own durable claim, written by the run
// itself — checked against the Workflow instance when the operator's own
// Cloudflare credentials allow it, and said plainly when they do not.
//
// The feed is read best effort for the last event, exactly the way one-shot
// status reports it for a local run: a feed that cannot be served costs the
// table its last-event line, never the liveness answer — the feed is exhaust
// and may never gate anything.
func cloudRunStatus(ctx context.Context, runID string, asJSON bool, stdout, stderr io.Writer) int {
	resolved, note, err := resolveCloudRunID(ctx, runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac status: %v\n", err)
		return 1
	}
	if note != "" {
		fmt.Fprintln(stderr, note)
	}
	runID = resolved

	state, err := readCloudRunState(ctx, runID)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac status: %v\n", err)
		return 1
	}
	answer := cloudRunLiveness(ctx, runID, state)

	status := struct {
		RunID          string         `json:"run_id"`
		Host           string         `json:"host"`
		Alive          bool           `json:"alive"`
		State          string         `json:"state"`
		LivenessSource string         `json:"liveness_source"`
		Reason         string         `json:"reason"`
		CurrentStep    string         `json:"current_step,omitempty"`
		LastEvent      *runfeed.Event `json:"last_event,omitempty"`
		EventAge       string         `json:"last_event_age,omitempty"`
	}{
		RunID:          runID,
		Host:           "cloud",
		Alive:          answer.Alive,
		State:          answer.State,
		LivenessSource: answer.Source,
		Reason:         answer.Reason,
		CurrentStep:    answer.Current,
	}
	if client, err := newCloudClient(); err == nil {
		source := &cloudFeedSource{client: client, runID: runID, warn: stderr}
		if located, absent, err := feedStanding(ctx, source); err == nil && !absent && len(located) > 0 {
			last := located[len(located)-1].Event
			status.LastEvent = &last
			if at, err := time.Parse(time.RFC3339, last.At); err == nil {
				status.EventAge = time.Since(at).Round(time.Second).String()
			}
		}
	}

	if asJSON {
		raw, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "%s\n", raw)
	} else {
		stateWord := "alive"
		if !answer.Alive {
			stateWord = "not alive"
		}
		fmt.Fprintf(stdout, "run %s: %s — %s\n", runID, stateWord, answer.Reason)
		if status.LastEvent != nil {
			tick := "-"
			if status.LastEvent.TickID != nil {
				tick = *status.LastEvent.TickID
			}
			fmt.Fprintf(stdout, "last event %s ago: %s %s %s\n",
				status.EventAge, status.LastEvent.Stage, tick, status.LastEvent.Detail)
		}
	}
	if answer.Alive {
		return 0
	}
	return 1
}

// readCloudRunState reads the run record's own state from the factory — the
// durable claim the Workflow's run writes about itself. Unlike the
// best-effort read `cloud supervisor` makes beside its own verdict, a
// failure here is a failure to answer liveness at all, and is reported.
func readCloudRunState(ctx context.Context, runID string) (string, error) {
	client, err := newCloudClient()
	if err != nil {
		return "", err
	}
	data, err := client.request(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		return "", err
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return "", err
	}
	return strings.TrimSpace(response.Run.State), nil
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
