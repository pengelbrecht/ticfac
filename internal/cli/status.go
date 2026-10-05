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
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/escalation"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// defaultStatusFollowInterval is how often the live table re-renders. Two
// seconds: fast enough that "what is it doing right now" is right, slow
// enough that a cloud refresh (one or two factory reads) stays polite.
const defaultStatusFollowInterval = 2 * time.Second

// statusFollow renders the live table until the run ends or the caller
// interrupts. One frame per interval; between frames the cursor is moved up
// over the previous frame and the lines rewritten in place.
func statusFollow(ctx context.Context, repo, runID string, interval time.Duration, stdout, stderr io.Writer) int {
	source, kind, resolved, err := feedSource(ctx, repo, runID, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac status: %v\n", err)
		return 1
	}
	// The follow answers for the run the id names, resolved: an epic id that
	// named a factory run answers for that run's own id (tick nyi).
	runID = resolved
	cloudSource, _ := source.(*cloudFeedSource)
	if interval <= 0 {
		interval = defaultStatusFollowInterval
	}

	// Labels are read once per follow, not once per frame: a frame every two
	// seconds must not spawn tk every two seconds, and a tick added mid-run
	// only costs its label (it shows as its bare id), never its line.
	//
	// And they are read BESIDE the frames, never before the first one: a
	// label is decoration, and `tk version` plus `tk list` are two process
	// spawns that, on a loaded host, kept the table blank for seconds (the
	// first-frame test in status_firstframe_test.go). Until they arrive every
	// tick shows as its bare id, which is what a tracker that cannot be read
	// costs anyway.
	labelsReady := make(chan map[string]string, 1)
	load := tickLabels // read here, not in the goroutine: it is a seam tests swap
	go func() { labelsReady <- load(ctx, repo) }()
	var labels map[string]string

	// Where the follow's ended-answer starts (tick 4nq, the cursor
	// protection `ticfac watch` got in usx, for the surface that missed
	// it): the feed is append-only per RUN ID, so a resumed local run
	// appends to a file a previous, failed incarnation already ended with a
	// terminal line — and a frame that scans the whole standing feed for ANY
	// terminal line ends the follow at the first frame, exit 0, while the
	// run is alive and dispatching. A LIVE process claims the follow: the
	// cursor moves just past the last terminal line already standing, so
	// only a terminal line the CURRENT incarnation writes can end it. No
	// live claim keeps the cursor at zero: the standing feed is the run's
	// own last word, and the follow reports that ending rather than an
	// open-ended silence — a run about to be resumed has not claimed yet,
	// and its previous ending was the truth until the resume.
	//
	// The liveness answer that decides the cursor is the first frame's too:
	// asked once (one `ps`), never a second time before anything is drawn.
	cursor := int64(0)
	var first *runlife.Status
	if kind != "cloud" {
		live := runlife.Liveness(repo, runID)
		first = &live
	}
	if first != nil && first.State == runlife.Alive {
		located, _, err := feedStanding(ctx, source)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 1
		}
		for _, line := range located {
			if line.Stage == reconcile.StageRunFinished || line.Stage == reconcile.StageRunDied {
				cursor = line.End
			}
		}
	}

	previous := 0
	for {
		if labels == nil {
			select {
			case labels = <-labelsReady:
			default:
			}
		}
		ended, lines, err := renderStatusFrame(ctx, source, cloudSource, kind, repo, runID, labels, cursor, first, stdout)
		first = nil
		if err != nil && errors.Is(err, errFeedRead) && previous > 0 {
			// A follow that has drawn a frame keeps going through a failed
			// read — a factory redeploying under it is one missed frame, said
			// on stderr, and the last frame stands until the next one.
			fmt.Fprintf(stderr, "# %v\n", err)
			select {
			case <-ctx.Done():
				return 1
			case <-time.After(interval):
			}
			continue
		}
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
// end — for a local run, a terminal line the CURRENT incarnation wrote past
// the cursor (tick 4nq): an earlier incarnation's ending is history while a
// live process claims the run, and the run's own last word when nothing
// does — which ends the follow.
//
// live, when the caller already asked, is this frame's liveness answer; nil
// asks now. Only liveness is asked — never Probe's attempt census, which the
// table does not show and which costs a process spawn per frame.
func renderStatusFrame(ctx context.Context, source runfeed.Source, cloudSource *cloudFeedSource, kind, repo, runID string, labels map[string]string, cursor int64, live *runlife.Status, out io.Writer) (ended bool, lines []string, err error) {
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
		switch {
		case answer.State == cloudLivenessOrphaned:
			stateWord = "not alive (orphaned)"
		case !alive:
			stateWord = "not alive"
		}
		liveness = fmt.Sprintf("%s — %s", stateWord, answer.Reason)
		if answer.State == cloudLivenessOrphaned && cloudSource.epic != "" {
			liveness += " — clear with: " + statusmodel.ResumeCommand(statusmodel.HostCloud, cloudSource.epic)
		}
		// An orphaned record will never be written again: nothing more is
		// coming, so a follow ends on it as on the run's own terminal word.
		ended = !cloudRunStillGoing(state) || answer.State == cloudLivenessOrphaned
	default:
		if live == nil {
			answer := runlife.Liveness(repo, runID)
			live = &answer
		}
		probe := *live
		alive = probe.State == runlife.Alive
		liveness = fmt.Sprintf("%s — %s", probe.State, probe.Reason)
		// The ended-answer carries the cursor's protection (tick 4nq): a
		// terminal line the CURRENT incarnation wrote — one standing past the
		// cursor — ends the follow; a terminal line an earlier incarnation
		// wrote is history while a live process claims the run, and the run's
		// own last word when nothing does.
		for _, line := range located {
			if line.Stage != reconcile.StageRunFinished && line.Stage != reconcile.StageRunDied {
				continue
			}
			if probe.State == runlife.Alive && line.End <= cursor {
				continue
			}
			ended = true
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

// pushHealthLine is the run's pushes to its hosted repository and its GitHub
// errors by class, counted from its local feed (tick rlp): empty when the feed
// has none of either, or cannot be read.
func pushHealthLine(repo, runID string) string {
	events, err := runfeed.Read(runfeed.Path(repo, runID))
	if err != nil {
		return ""
	}
	pushes, peak, errs := statusmodel.PushHealth(events)
	return formatPushHealth(pushes, peak, errs)
}

func formatPushHealth(pushes, peak int, errs statusmodel.GitHubErrors) string {
	if pushes == 0 && errs.Total() == 0 {
		return ""
	}
	line := fmt.Sprintf("github: %d push(es), peak %d in a minute", pushes, peak)
	if errs.Total() == 0 {
		return line + "; no GitHub errors"
	}
	var classes []string
	for _, c := range errs.Classes() {
		if c.N > 0 {
			classes = append(classes, fmt.Sprintf("%s %d", c.Class, c.N))
		}
	}
	return line + "; GitHub errors: " + strings.Join(classes, ", ")
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
// newStatusCommand builds the cobra command for `status`.
func newStatusCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <run-id|epic-id>",
		Short: "is the run alive, and when did it last say anything",
		Long: `The run is named by its run id (epic-6in) or by the epic id it was started
with (6in, as in 'ticfac run 6in'); 'ticfac' alone lists the runs there are.

The one-shot liveness answer (and, with --follow, the live table): is the
run alive — a pidfile plus process start time for a local run, the Workflow's
own state for one the cloud hosts — when did it last say anything, and what is
each in-flight attempt doing.

With --json, the answer is the versioned status model (ticfac.status.v1, tick
6dh): the one object every surface renders — the epic's lifecycle and waves,
every tick's durable state with its try history, tier, model and executor, the
live workers' silence and last turns, what the run waits on with the command
that unblocks it, retries and interventions, gate evidence per check per
head, CI on the epic PR, and the cost the records state. Local and cloud runs
alike; the exit code stays liveness's answer alone.`,
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	asJSON := fs.Bool("json", false, "print the versioned status model (ticfac.status.v1): lifecycle, waves, ticks, workers, waits, CI and cost")
	follow := fs.Bool("follow", false, "keep the status table updated in place, one line per tick, until the run ends or Ctrl-C")
	interval := fs.Duration("interval", defaultStatusFollowInterval, "with --follow, how often the table refreshes")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(statusCommand(c.Context(), args, repo, asJSON, follow, interval, stdout, stderr))
	}
	return cmd
}

func statusCommand(ctx context.Context, args []string, repo *string, asJSON, follow *bool, interval *time.Duration, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac status: exactly one run id (epic-<id>) or epic id is required\n")
		return 2
	}
	if *asJSON && *follow {
		fmt.Fprintf(stderr, "ticfac status: --json prints one answer; a table that refreshes is not a JSON stream — pipe one frame or follow the other\n")
		return 2
	}
	if parseOnly {
		return 0
	}
	if *repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 2
		}
		*repo = wd
	}
	// The run id or the epic id, resolved once (runid.go): `ticfac status
	// 6in` answers for epic-6in, the run `ticfac run 6in` started.
	resolution := resolveRunArg(*repo, rest[0])
	runID := resolution.RunID
	if *follow {
		return statusFollow(ctx, *repo, runID, *interval, stdout, stderr)
	}

	status := runlife.Probe(*repo, runID, time.Now())

	// A cloud run whose ORCHESTRATOR is this machine (`ticfac run
	// --cloud-workers`, #151) carries the factory's `run_` id, but its
	// process, pidfile and feed are here: its liveness is that process, and
	// the Workflow instance is only the supervisor of its heartbeat. Asking
	// the Workflow alone answered "the supervisor is queued and has not
	// started executing yet" about a run that was dispatching on this
	// machine.
	orchestratedHere := false
	if looksLikeCloudRunID(runID) {
		if status.State == runlife.Alive {
			orchestratedHere = true
		} else if checkout := localRunCheckout(*repo, runID); checkout != "" {
			orchestratedHere = true
			*repo = checkout
			status = runlife.Probe(checkout, runID, time.Now())
		}
	}

	// A run the Workflow hosts is answered by the Workflow, and its id says
	// which host that is: `run_` plus hex names a cloud run, and no local
	// pidfile was ever its claim to life.
	if !orchestratedHere && looksLikeCloudRunID(runID) {
		return cloudRunStatus(ctx, *repo, runID, *asJSON, stdout, stderr)
	}

	// An epic-shaped id that names nothing HERE may still name the run the
	// factory holds for this checkout's project (tick nyi): `ticfac run
	// <epic> --cloud` is how the epic got there, and `ticfac status
	// <epic-id>` is how the operator asks about it. A local feed standing in
	// this checkout wins first — that is the run's own evidence — and a
	// factory with nothing this project can claim leaves the local answer
	// standing, exactly as before.
	if status.State != runlife.Alive && !orchestratedHere {
		if _, feedErr := os.Stat(runfeed.Path(*repo, runID)); feedErr != nil {
			if resolved, note, err := cloudRunForEpic(ctx, *repo, runID); err == nil && resolved != "" {
				checkout := localRunCheckout(*repo, resolved)
				if checkout == "" {
					fmt.Fprintln(stderr, note)
					return cloudRunStatus(ctx, *repo, resolved, *asJSON, stdout, stderr)
				}
				// The factory's run for this epic is orchestrated HERE: it is
				// answered as the local run it is, under the id it runs as.
				fmt.Fprintf(stderr, "# %s is cloud run %s, orchestrated on this machine (--cloud-workers): its "+
					"liveness is its process here, and its workers are the factory's\n", runID, resolved)
				runID, *repo, orchestratedHere = resolved, checkout, true
				resolution.Known = true
				status = runlife.Probe(checkout, runID, time.Now())
			} else if err != nil {
				// A factory that cannot be read is named, never silent: the
				// run may be there, and the local probe's answer alone would
				// claim more than it knows.
				fmt.Fprintf(stderr, "ticfac status: %s has no run here, and the factory could not be asked for it: %v\n", runID, err)
			}
		}
	}

	// No spelling named a run here and no factory claimed one: that is an
	// id nobody found, not a run that is not running — say which spellings
	// were looked for and where the runs there are can be listed.
	if !resolution.Known && status.State == runlife.NotRunning && status.LastEvent == nil && len(status.Attempts) == 0 {
		fmt.Fprintln(stderr, unknownRunMessage("status", *repo, rest[0], resolution))
		return 1
	}

	if *asJSON {
		// The versioned model (tick 6dh): the one answer every surface
		// renders — lifecycle, waves, ticks, workers, waits, health, CI and
		// cost — with liveness at the top so the questions an unattended
		// factory is glanced at with keep their order. The exit code stays
		// liveness's alone.
		model := localStatusModel(ctx, *repo, runID, status, modelGatherers{graph: epicGraph, ci: statusCI})
		if err := printStatusModel(stdout, stderr, model); err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 2
		}
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
		if line := pushHealthLine(*repo, runID); line != "" {
			fmt.Fprintln(stdout, line)
		}
		if orchestratedHere {
			fmt.Fprintln(stdout, factoryWorkersLine(ctx, runID))
		}
		if line := localEscalationLine(*repo, runID); line != "" {
			fmt.Fprintln(stdout, line)
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
// With --json it emits the same versioned model a local run does, host
// "cloud": the run's records live on origin like any run's, its feed is
// the factory's own stream, and its workers are not on this machine — the
// census is not taken and the model says null, which is a different claim
// from "none stand".
//
// The feed is read best effort for the last event, exactly the way one-shot
// status reports it for a local run: a feed that cannot be served costs the
// table its last-event line, never the liveness answer — the feed is exhaust
// and may never gate anything.
func cloudRunStatus(ctx context.Context, repo, runID string, asJSON bool, stdout, stderr io.Writer) int {
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

	if asJSON {
		client, err := newCloudClient()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 1
		}
		record, err := readCloudRunRecord(ctx, client, runID)
		if err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 1
		}
		// Another project's run keeps this repo's records, tracker and PR
		// unread for it (tick nyi): the model says so in its degraded list.
		repoProject, _ := cloudProjectOf(repo)
		model := cloudStatusModel(ctx, client, repo, runID, record, answer, stderr,
			modelGatherers{graph: epicGraph, ci: statusCI},
			cloudRecordBelongsToRepo(repoProject, record.Project))
		if err := printStatusModel(stdout, stderr, model); err != nil {
			fmt.Fprintf(stderr, "ticfac status: %v\n", err)
			return 2
		}
		if answer.Alive {
			return 0
		}
		return 1
	}

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
	// The run's last event, read from the END of its feed — a long run's
	// feed is never walked whole for one line (run_5c7c16d1). A read that
	// failed is said to have failed, never left out as though the run had
	// said nothing.
	feedProblem := ""
	if client, err := newCloudClient(); err == nil {
		source := &cloudFeedSource{client: client, runID: runID, warn: stderr}
		if located, _, err := feedTail(ctx, source, 1); err != nil {
			feedProblem = err.Error()
		} else if len(located) > 0 {
			last := located[len(located)-1].Event
			status.LastEvent = &last
			if at, err := time.Parse(time.RFC3339, last.At); err == nil {
				status.EventAge = time.Since(at).Round(time.Second).String()
			}
		}
	}

	stateWord := "alive"
	switch {
	case answer.State == cloudLivenessOrphaned:
		stateWord = "not alive (orphaned)"
	case !answer.Alive:
		stateWord = "not alive"
	}
	fmt.Fprintf(stdout, "run %s: %s — %s\n", runID, stateWord, answer.Reason)
	if answer.State == cloudLivenessOrphaned {
		if client, err := newCloudClient(); err == nil {
			if record, err := readCloudRunRecord(ctx, client, runID); err == nil && strings.TrimSpace(record.Epic) != "" {
				fmt.Fprintf(stdout, "clear with: %s\n", statusmodel.ResumeCommand(statusmodel.HostCloud, strings.TrimSpace(record.Epic)))
			}
		}
	}
	if status.LastEvent != nil {
		tick := "-"
		if status.LastEvent.TickID != nil {
			tick = *status.LastEvent.TickID
		}
		fmt.Fprintf(stdout, "last event %s ago: %s %s %s\n",
			status.EventAge, status.LastEvent.Stage, tick, status.LastEvent.Detail)
	}
	if feedProblem != "" {
		fmt.Fprintf(stdout, "last event: unknown — %s\n", feedProblem)
	}
	if client, err := newCloudClient(); err == nil {
		fmt.Fprintln(stdout, sourceEscalationLine(ctx, &cloudFeedSource{client: client, runID: runID, warn: stderr}))
	}
	if answer.Alive {
		return 0
	}
	return 1
}

// localEscalationLine is the run's escalation record (internal/escalation),
// read from the feed this checkout holds for it: implementation ticks
// started, how many climbed to a higher tier and whose failure made them,
// and the share that succeeded on the tier they started at. A run with no
// feed here says nothing; a feed that cannot be read says so — the feed is
// exhaust, and its absence is never reported as "no escalations".
func localEscalationLine(repo, runID string) string {
	path := runfeed.Path(repo, runID)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return sourceEscalationLine(context.Background(), runfeed.FileSource(path))
}

// sourceEscalationLine reads a whole feed from source and renders its
// escalation line. The whole feed, deliberately: an escalation is a relation
// between a tick's tries, which a tail cannot see.
func sourceEscalationLine(ctx context.Context, source runfeed.Source) string {
	located, _, err := feedStanding(ctx, source)
	if err != nil {
		return "escalation: unknown — the feed could not be read: " + err.Error()
	}
	events := make([]runfeed.Event, 0, len(located))
	for _, l := range located {
		events = append(events, l.Event)
	}
	return escalation.FromFeed(events).Line()
}

// factoryWorkersLine is the factory's half of a locally orchestrated cloud
// run's status (`ticfac run --cloud-workers`, #151): the factory hosts its
// workers and supervises this machine's heartbeat, so its record and its
// Workflow instance are said as THAT — never as the run's own liveness, which
// is the process here. A factory that cannot be asked is said so; the local
// answer stands either way.
func factoryWorkersLine(ctx context.Context, runID string) string {
	const lead = "factory (hosts its workers, supervises this machine's heartbeat): "
	state, err := readCloudRunState(ctx, runID)
	if err != nil {
		return lead + "could not be asked: " + err.Error()
	}
	answer := cloudRunLiveness(ctx, runID, state)
	word := "running"
	switch {
	case answer.State == cloudLivenessOrphaned:
		word = "orphaned"
	case !answer.Alive:
		word = "ended"
	}
	return lead + word + " — " + answer.Reason
}

// readCloudRunRecord reads the run record the factory holds — the Workflow's
// own durable claim about the run, plus the epic it runs.
func readCloudRunRecord(ctx context.Context, client *cloudClient, runID string) (cloudRunRecord, error) {
	data, err := client.request(ctx, http.MethodGet, "/api/runs/"+url.PathEscape(runID), nil)
	if err != nil {
		return cloudRunRecord{}, err
	}
	var response cloudStatusResponse
	if err := decodeCloudJSON(data, &response); err != nil {
		return cloudRunRecord{}, err
	}
	return response.Run, nil
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
	record, err := readCloudRunRecord(ctx, client, runID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(record.State), nil
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
