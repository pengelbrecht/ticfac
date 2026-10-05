package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// ExitHeld is `watch`'s own code: the run ended — or stopped — holding
// something only a person can move, and the alert says which tick and why. It
// is watch's and not the shared cloud table's exitNoRepo: a script branching
// on a watch's exit has branched on watch, and 3 here never means "not in a
// git repository" (tick 0z0).
const ExitHeld = 3

// `ticfac watch <run-id>` is the attention surface (tick 89m): a RENDERER of
// the status model (tick 6dh) that answers, in this order — does anything
// need me; is it healthy; how far along the whole epic; what is happening
// now; what happened and what is next.
//
// On a TERMINAL it is a LIVE BLOCK REDRAWN IN PLACE (the docker compose /
// BuildKit pattern), not a stream of lines: a dashboard of the whole epic —
// attention first, the lifecycle as a progress bar with elapsed, ETA and the
// health verdict, one FIXED row per tick in plan order with its pipeline
// cell, the live workers with their activity, CI and cost, and the feed
// shrunk to a two-line tail under the "─ recent" rule. The block keeps its
// place: nothing above it but the attention alert's one durable copy per
// episode, because the feed's lines live in the tail and behind the keys —
// j/k move a cursor over the rows, enter opens the tick's own story (every
// try with its tier, outcome and the run's own reason and next step; the
// report summary and diff stats; the gate evidence; the findings), e opens
// the whole feed scrollable, and esc or q comes back down to the dashboard,
// where q or Ctrl-C ends the watch the way SIGINT always has. NO alternate
// screen, for the reason the full-screen TUIs' users taught: a tool that
// takes over the pane is a tool its users fall back to streams around. The
// frame fits the pane it is given — narrow drops columns (the last turn
// first, then the model), short keeps the active rows and counts the rest.
//
// When stdout is NOT a terminal — a pipe, a log, a test buffer — there is no
// place to redraw into, and the watch streams PLAIN LINES instead: the same
// one-line-per-event stream it has always been, because a stream is what a
// machine or a log wants and a glance is what a person wants, and the same
// command owes both. The hold alert and the exit codes are the same on both
// paths: 0 the run ended done (the last line says how), 7 it ended
// CANCELLED — stopped deliberately, its terminal line naming the stop —
// 1 it ended FAILED — its own terminal line names what did not pass — or
// the feed could not be read, 3 it ended holding something only a person
// can move, 5 the watch was interrupted while the run is still going,
// 2 usage.
//
// WHERE the subscription starts is decided from the run's own liveness claim
// (tick usx). The feed is append-only per RUN ID, so a resumed run appends
// to a file a previous, failed incarnation already ended with a terminal
// line, and a watch that replays the standing feed from offset zero reads
// that ending FIRST — it exits at once and reports a failure that already
// happened, even a hold the release has already settled: the same defect
// `events --follow` carried (ticfac tick 55i), in the command built to be
// alerted by it. So:
//
//   - A LIVE process claims the run: the live view joins the CURRENT
//     incarnation, and the standing feed's earlier endings are history —
//     never an end the watch reports.
//
//   - No live process claims the run — it ended and released, or nobody has
//     claimed it here: the standing feed IS the run's last word, so the
//     watch reports that ending rather than an open-ended silence. A run
//     about to be resumed has not claimed yet; its previous ending was the
//     truth until the resume.
//
// A line is still a hint about when to LOOK, never a verdict: the alert sends
// a person to the durable evidence — the branch, the report, the decisions
// on origin — and the exit code says a decision is needed, not what it is.

// defaultWatchInterval is how often the live view re-renders. Two seconds,
// the same cadence `status --follow` refreshes its table at: fast enough
// that the timers read live, slow enough that a refresh — which re-reads
// the run's durable records the way one `status --json` invocation does —
// stays polite. The expensive sources (the tracker's graph, the forge) are
// cached per watch at watchSourceTTL, because a frame every two seconds
// must not spawn a subprocess every two seconds.
const defaultWatchInterval = defaultStatusFollowInterval

// watchSourceTTL is how long one reading of a source that costs a subprocess
// or a remote call — the tracker's graph, the forge's CI — may serve frames.
// The graph is also invalidated the moment a feed line says the epic's shape
// changed (a finding absorbed, a replan), so an absorbed tick appears as a
// marked new row when it happens, not half a minute after.
const watchSourceTTL = 30 * time.Second

// watchIsTerminal says whether a writer is a terminal: the seam the live
// view's tests fake, and the fact that decides the frame from the stream.
var watchIsTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// watchTerminalSize is the pane's size: the seam the live view's tests fix,
// and the width and height the frame fits itself to. A terminal that will
// not say answers the 80x24 every terminal predates.
var watchTerminalSize = func(w io.Writer) (int, int, bool) {
	f, ok := w.(*os.File)
	if !ok {
		return 0, 0, false
	}
	width, height, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0, 0, false
	}
	return width, height, true
}

// newWatchCommand builds the cobra command for `watch`.
func newWatchCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch <run-id|epic-id>",
		Short: "the whole epic at a glance, live in place — and it says so when a person is needed",
		Long: `The run is named by its run id (epic-6in) or by the epic id it was started
with (6in, as in 'ticfac run 6in'); 'ticfac' alone lists the runs there are.

The whole epic as a dashboard, redrawn in place: attention first (only when
a person is needed, naming the command that moves it on), the lifecycle as a
progress bar with elapsed, ETA and the health verdict, one fixed row per tick
with its pipeline, the live workers with their activity, CI and cost, and the
event feed shrunk to a two-line tail — fitting the pane it is given and never
taking over the screen (scrollback, copy and links keep working).

When stdin is a terminal too, the dashboard answers the keys: j/k move a
cursor over the tick rows, enter opens the cursor's tick — every try with its
tier, outcome and the run's own reason and next step, the report summary and
its diff stats, the gate evidence, the findings — e opens the whole feed
(j/k scroll it), and esc or q comes back down to the dashboard. On the
dashboard, q or Ctrl-C ends the watch the way SIGINT does. When stdout is not
a terminal — a pipe, a log — the same command streams plain lines, one per
event, and says, to a human, when the run stops holding something for one:
which tick, which attempt, why, and the command that moves it on.

When the run ends while the dashboard is up on a keyboarded terminal, the
dashboard does not close: the end is kept above the block once, enter and e
keep answering over the ended run — the attempts, reasons and gate evidence
are most wanted exactly then — and q or Ctrl-C closes the watch through the
run's own ending, with its exit code. The watch 'ticfac run' attaches never
waits: it returns when the run does, so a pane a command owns is never held
for a key nobody will type.

Exit codes: 0 the run ended done (the last line says how), 7 it ended
CANCELLED — stopped deliberately, its terminal line naming the stop —
3 it ended holding something only a person can move, 5 the watch was
interrupted while the run is still going (the run keeps going; come back
with the same command), 1 the run ended FAILED — its own terminal line
names what did not pass — or the feed could not be read, 2 usage.

With --json, the watch answers ONCE, at its end: one document holding the
versioned status model — the same object 'ticfac status --json' gathers —
plus the exit-table state word and, when the run ended holding something,
the wait kind that only a person moves. The document is stdout's only
content.`,
	}
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	interval := fs.Duration("interval", defaultWatchInterval, "how often the live view re-renders (a pipe gets one plain line per event instead)")
	asJSON := fs.Bool("json", false, "answer once, at the watch's end: one versioned document (ticfac.watch.v1) holding the status model, the state word and — when it ended holding — the wait kind only a person moves")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(watchCommand(c.Context(), args, repo, interval, asJSON, true, stdout, stderr))
	}
	return cmd
}

// stayOnEnd (tick 2xk) is whether the LIVE view stands past the run's own
// end, waiting for q: true for `ticfac watch` itself — after a run stops or
// fails is exactly when a person wants the per-tick attempts, reasons and
// gate evidence, so its dashboard stays open for drill-in — and false for
// the attach `ticfac run` and `ticfac run --cloud` put up, whose exit
// belongs to the run command: a pane a command or an agent owns must return
// when the run does, never wait for a key nobody will type. It only ever
// applies on a keyboarded terminal: keyless, the end exits at once exactly
// as it always has.
func watchCommand(ctx context.Context, args []string, repo *string, interval *time.Duration, asJSON *bool, stayOnEnd bool, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac watch: exactly one run id (epic-<id>) or epic id is required\n")
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
	// Where the run's feed lives is the run's own fact (tick k7p): this
	// checkout when it holds the feed, else the factory when it knows the
	// run. The watch then reads through the same one loop `events --follow`
	// reads through, whichever host the run is on.
	// The run id or the epic id, resolved once (runid.go): `ticfac watch
	// 6in` answers for epic-6in, the run `ticfac run 6in` started.
	resolution := resolveRunArg(*repo, runID)
	runID = resolution.RunID
	source, kind, resolved, err := feedSource(ctx, *repo, runID, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac watch: %v\n", err)
		return 1
	}
	// No spelling names a run here and the factory holds none for the epic:
	// an id nobody found, answered as that — never as a run with no feed.
	// A local source under ANOTHER id is a resolution too: the factory's run
	// for the epic, orchestrated on this machine (--cloud-workers, #151).
	if kind == "local" && !resolution.Known && resolved == runID {
		fmt.Fprintln(stderr, unknownRunMessage("watch", *repo, rest[0], resolution))
		return 1
	}
	// The watch answers for the run the id names, resolved: an epic id that
	// named a factory run answers for that run's own id (tick nyi), so the
	// hold alert, the document and the detach lines all name the run an
	// operator can pass back to every other command.
	runID = resolved

	// The standing feed is read once up front: it decides whether there is
	// anything to watch at all and, below, where the subscription starts. The
	// run's own liveness claim is asked with it.
	located, absent, err := feedStanding(ctx, source)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac watch: %v\n", err)
		return 1
	}

	// The run's own liveness claim: it decides both whether there is anything
	// to watch at all and, below, where the subscription starts. For a run
	// the Workflow hosts, the claim is the Workflow's own state, never "is
	// there a process here" — the question status could not answer off-host
	// until this tick.
	var probeState runlife.State
	var probeReason string
	cloudSource, isCloud := source.(*cloudFeedSource)
	if !isCloud {
		probe := runlife.Probe(*repo, runID, time.Now())
		probeState, probeReason = probe.State, probe.Reason
	} else {
		// The route serves the feed and the run record's state together, so
		// the watch's first question cost the standing read above.
		state := cloudSource.State()
		switch {
		case cloudRunStillGoing(state):
			probeState, probeReason = runlife.Alive,
				fmt.Sprintf("the Workflow hosts this run: the factory's record says %s", stateOrUnknown(state))
		case state == "":
			probeState, probeReason = runlife.NotRunning, "the factory's record carries no state for this run"
		default:
			probeState, probeReason = runlife.NotRunning,
				fmt.Sprintf("the Workflow's own record says %s, so the run has ended", stateOrUnknown(state))
		}
	}

	// A watcher may be started beside the run it watches, before the feed's
	// first line exists — that is the good case, and waiting is the point. A
	// LIVE claim is the run's own claim that it will write one, so it is what
	// tells "too early to watch" from "nothing to watch here"; a run with
	// neither a feed nor a live claim never ran anywhere this checkout can
	// see.
	if absent && probeState != runlife.Alive {
		if kind == "local" {
			fmt.Fprintf(stderr, "ticfac watch: no feed for run %s at %s — and no live process claims the run "+
				"here, so there is nothing to watch. %s\n", runID, runfeed.Path(*repo, runID), probeReason)
		} else {
			fmt.Fprintf(stderr, "ticfac watch: nothing to watch for run %s — the factory knows the run, but it has "+
				"written no event and %s\n", runID, probeReason)
		}
		return 1
	}

	// A terminal gets the live view; everything else gets the plain stream.
	// --json is a pipe even on a terminal: the stream path is the one whose
	// end is a single document, and a live block redrawn in place is not a
	// document. Both paths end the same way — on the run's own last word —
	// and their exit codes mean the same things.
	if watchIsTerminal(stdout) && !*asJSON {
		return watchLive(ctx, source, kind, *repo, runID, *interval, stayOnEnd, stdout, stderr)
	}

	// The stream path: subscribe exactly as `events --follow` does — open
	// once, follow the appends, no interval anywhere — and add the one thing
	// a non-participant could not do before: say, to a human, when the run
	// ends HOLDING something. The state below is written only from the
	// callback Follow runs on its own goroutine of control — Follow is
	// synchronous (one loop, one callback), so no lock is needed around held
	// and terminal.
	held := false
	terminal := ""
	terminalDetail := ""
	// The epic id the clearing commands are addressed by (tick gtk): read
	// out of the run's own id for a local run, carried by the factory's run
	// record for a cloud one — never a `<epic-id>` placeholder, which is a
	// second thing to look up, not a command. A local run whose id is NOT
	// the epic spelling — `ticfac run <epic> --run-id run-p` — spells its
	// epic nowhere in the id: the checkpoint carries it, so it is read when
	// a command is spelled, not when the watch starts (the checkpoint may
	// not exist yet then). The same read the model path makes (tick fub),
	// so the two renderers spell one epic, never two (hn6 rule 8).
	epicOf := func() string { return watchEpicID(*repo, runID, source) }
	followCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The '<tick>#<n>' prefix names the tick's own TRY (tick h58), not the
	// run-wide dispatch number the line's `attempt` field carries: "w9b#5"
	// read as w9b's fifth try when it was its third. The try is counted from
	// the feed's own lines, and the WHOLE standing feed is counted before the
	// first line prints — a watch that joins a live run past its earlier
	// incarnations still counts the tries those incarnations dispatched.
	var tries runfeed.Tries
	for _, line := range located {
		tries.Observe(line.Event)
	}
	print := func(event runfeed.Event) {
		tries.Observe(event)
		who := "run"
		if event.TickID != nil && *event.TickID != "" {
			who = *event.TickID
			if event.Attempt != nil {
				if try, ok := tries.Of(who, *event.Attempt); ok {
					who = fmt.Sprintf("%s#%d", who, try)
				}
			}
		}
		if !*asJSON {
			fmt.Fprintf(stdout, "%s %-12s %s: %s\n", clockOf(event.At), who, event.Stage, event.Detail)
		}
		if event.Stage == reconcile.StageRunHeld {
			// The line the whole command exists for, said to a human: which
			// tick, which attempt, why — all read off the line's own fields,
			// never out of its prose — and the command that moves the hold on,
			// decided by WHAT the run holds, one decision per hold kind (tick
			// gf0): the shared spelling in statusmodel, the same one the
			// model's needs-you and the rows' next steps read, so no two
			// surfaces can name two verbs for one hold. The holds that fire
			// before the tick's first dispatch — the width, a foreign claim,
			// the absorption bound — carry a NULL attempt: no settle command
			// can address them ("-" is not an attempt number), and a release
			// would not clear them anyway, so each names the command that
			// actually moves it on. The final-review hold (tick quz) is the
			// opposite shape and the same answer: it CARRIES an attempt — the
			// close-out's — but releasing it clears nothing, because the hold
			// is the review's verdict on the PR, which a resume re-reads; the
			// moves are the refusal's own, and they end at the run again. A
			// hold the closed set does not know names no command at all, never
			// a wrong one a person copies.
			held = true
			tick, what := "-", "-"
			if event.TickID != nil {
				tick = *event.TickID
				what = "tick " + tick
			}
			if event.Attempt != nil {
				// The sentence leads with the tick's try; the command the
				// run-wide dispatch number addresses comes from the shared
				// decision, which carries the number the attempt's identity is.
				try, _ := tries.Of(tick, *event.Attempt)
				what = reconcile.AttemptLabel(tick, try, *event.Attempt)
			}
			host := statusmodel.HostLocal
			if kind == "cloud" {
				host = statusmodel.HostCloud
			}
			// The triage and the settle are addressed to the runs their records
			// live under: the drafts under the run that wrote this line, named
			// by the line's own run id (tick q8m) — a cloud run's store lives
			// under the factory's run_<hex>, one the bare command's default
			// (the local spelling epic-<epic-id>) holds nothing for — and the
			// attempt under the run the watch answers for (tick ulw fixed the
			// model surface; tick qxj this alert).
			address := runID
			if event.RunID != "" {
				address = event.RunID
			}
			clearing := statusmodel.HoldClearingCommand(epicOf(), host, address, runID, event)
			head := fmt.Sprintf("\nticfac watch: run %s is HOLDING %s for a person:\n%s\n", runID, what, event.Detail)
			switch statusmodel.HoldReason(event.Detail) {
			case reconcile.RefusedFindingUntriaged:
				fmt.Fprintf(stderr, "%s"+
					"Nothing proceeds until somebody decides. Triage the finding(s) with `%s` — "+
					"every untriaged finding of the run settles there, by short key prefix — "+
					"or answer what the tick is waiting for. The "+
					"evidence is on the integration branch, not in this line.\n\n", head, *clearing)
			case reconcile.RefusedClaimWidth, reconcile.RefusedForeignClaim:
				fmt.Fprintf(stderr, "%s"+
					"Nothing proceeds until the world changes — and a release is not what changes it: "+
					"no attempt was ever dispatched, so settle refuses the hold's dash attempt. "+
					"The hold ends when the claim it names does, and `%s` runs the epic again, "+
					"re-deriving from the graph and proceeding the moment it has. The evidence is on "+
					"the integration branch, not in this line.\n\n", head, *clearing)
			case reconcile.RefusedAbsorptionDepth:
				fmt.Fprintf(stderr, "%s"+
					"Nothing proceeds until somebody judges the chain the line carries: decide the "+
					"finding with `%s` — or take the escape the line itself names, raising the bound "+
					"with --absorption-depth and running the epic again. The evidence is on the "+
					"integration branch, not in this line.\n\n", head, *clearing)
			case reconcile.RefusedLandReviewNotReady:
				fmt.Fprintf(stderr, "%s"+
					"Nothing proceeds until a person accepts or rejects the work the run's own review "+
					"refused — and the release the attempt above might suggest clears nothing: the "+
					"hold is the review's verdict recorded on the PR, which a resume re-reads. The "+
					"moves are the line's own: fix what the review says would make it ready and run "+
					"the epic again with `%s`, merge the PR by hand to accept it (a re-run then "+
					"finds it merged), or close it. The evidence is on the integration branch, not "+
					"in this line.\n\n", head, *clearing)
			default:
				if clearing != nil {
					fmt.Fprintf(stderr, "%s"+
						"Nothing proceeds until somebody decides. Release it with `%s` — add --carry-work to base the "+
						"next try on the commits the released one left — or answer what the tick is waiting for. The "+
						"evidence is on the integration branch, not in this line.\n\n", head, *clearing)
				} else {
					fmt.Fprintf(stderr, "%s"+
						"Nothing proceeds until somebody decides. No one command clears this hold — "+
						"answer what the tick is waiting for. The evidence is on the integration branch, not in this line.\n\n",
						head)
				}
			}
		}
		if event.Stage == reconcile.StageRunFinished || event.Stage == reconcile.StageRunDied {
			// The run's own last word ends the watch: a watcher must not
			// outlive the run it watches, and it must not decide an end the
			// run did not write. What the terminal line SAYS is the line a
			// person reads above; the exit code below is only ever "ended",
			// "ended holding for a person" or — since tick bot — "ended
			// failed", never a verdict about the work beyond that.
			terminal = event.Stage
			terminalDetail = event.Detail
			cancel()
		}
	}
	// Where the subscription starts (tick usx): offset zero replays the whole
	// standing feed; a live run's cursor is just past the last terminal line,
	// so the watch joins the current incarnation and no earlier one. A
	// terminal line read here is history by construction — it stands BEFORE the
	// cursor — so the watch can still end only on a terminal line the current
	// incarnation writes (or, with no live claim, the run's own last word).
	// For a run the Workflow hosts, the standing read above already served the
	// feed and the run's state together; for a local run the read is the same
	// one `events` prints, because the LINES are the same and so is the parser.
	cursor := int64(0)
	if probeState == runlife.Alive {
		for _, line := range located {
			if line.Stage == reconcile.StageRunFinished || line.Stage == reconcile.StageRunDied {
				cursor = line.End
			}
		}
	}
	if err := followFeed(followCtx, source, kind, 0, cursor, print); err != nil {
		fmt.Fprintf(stderr, "ticfac watch: %v\n", err)
		if *asJSON {
			// A failed stream still answers once: the refusal document is the
			// one thing stdout carries, so an agent's parse never sees silence.
			emitWatchJSON(ctx, source, kind, *repo, runID, agentStateFailed, nil, stdout)
		}
		return 1
	}
	// The end, as one document under --json: the state word the exit code
	// agrees with, the model gathered the way every surface gathers it, and
	// the wait kind when the end holds something for a person.
	finish := func(state string, attention *statusmodel.Attention) int {
		if !*asJSON {
			return stateExitClass(state)
		}
		if err := emitWatchJSON(ctx, source, kind, *repo, runID, state, attention, stdout); err != nil {
			fmt.Fprintf(stderr, "ticfac watch: %v\n", err)
			return 1
		}
		return stateExitClass(state)
	}
	if terminal == "" {
		// The subscription was interrupted before the run wrote its terminal
		// line — Ctrl-C, or the caller's context. That is neither "ended" nor
		// an error, and it is not exit 0: a caller waiting on this command
		// must not read an interrupted watch as a finished run. A run that is
		// still ALIVE is the table's running class (5) — the work continues,
		// nothing is wrong, and `ticfac run`'s detach relies on exactly this
		// word to say “the run keeps going” without prose (tick 8v3).
		fmt.Fprintf(stderr, "ticfac watch: the watch was interrupted before run %s said it ended; "+
			"`ticfac status %s` asks whether it is still alive\n", runID, runID)
		if held {
			return finish(agentStateHeld, nil)
		}
		if watchRunStillAlive(source, kind, *repo, runID) {
			return finish(agentStateRunning, nil)
		}
		if *asJSON {
			emitWatchJSON(ctx, source, kind, *repo, runID, agentStateFailed, nil, stdout)
		}
		return 1
	}
	// The end holds the same authority on every path (tick 4mv): the exit
	// code is watchHoldAttention's answer over the gathered model — the one
	// the live view's last word ends by — so a completed run holding its PR
	// for a person answers 3 on a pipe and in the document, not only on a
	// terminal, and a hold the stream's own run_held line named ends 3 even
	// when the model cannot be gathered (the feed's line is the durable
	// half; the model is the richer one).
	model, gatherErr := watchGatherModel(ctx, source, kind, *repo, runID)
	if gatherErr == nil {
		if attention := watchHoldAttention(model); attention != nil {
			if !held {
				// The holds the stream never alerted mid-run — the merge that
				// is a person's by design, findings nobody triaged — still get
				// the last word the live view says, so a person reading a log
				// learns what the code means. A run_held line already said its
				// hold once; this is the other kinds.
				sayWatchHold(runID, attention, stderr)
			}
			return finish(agentStateHeld, attention)
		}
	} else if held {
		return finish(agentStateHeld, nil)
	}
	// No hold stands: the run's own terminal line decides failed, cancelled
	// or done (ticks bot, rix and vqc), on every path the model was or was
	// not gathered on.
	if watchLineEndedFailed(terminal, terminalDetail) {
		// The run ended in its own failure, holding nothing for a person
		// (tick bot, epic 2jn's A4): that is the exit table's failed class,
		// not done — an agent that branched on 0 here read a failed run as a
		// finished epic. The hold branch above keeps precedence: a run that
		// failed while holding an attempt is the held class, because a
		// person can release that before anything else matters.
		if !*asJSON {
			host := statusmodel.HostLocal
			if kind == "cloud" {
				host = statusmodel.HostCloud
			}
			fmt.Fprintf(stderr, "\nticfac watch: run %s ended FAILED:\n%s\n"+
				"Nothing is held for a person: the work has to be fixed and the epic run again — "+
				"`%s` resumes it under this run id, without redoing what "+
				"already passed. The evidence is on the integration branch, not in this line.\n\n",
				runID, terminalDetail, statusmodel.ResumeCommand(host, epicOf()))
		}
		return finish(agentStateFailed, nil)
	}
	if watchLineEndedCancelled(terminal, terminalDetail) {
		// The run was stopped deliberately, holding nothing and failing
		// nothing (tick rix, the cancelled sibling of the failed branch
		// above): its own class, with its own code — never done/0, which an
		// agent branches on as "the epic finished", and never the failed
		// class, which names a fix nobody needs to make.
		if !*asJSON {
			fmt.Fprintf(stderr, "\nticfac watch: run %s ended CANCELLED:\n%s\n"+
				"The run was stopped deliberately: the work is neither done nor failed, and "+
				"nothing is held for a person. The evidence is on the integration branch, not in this line.\n\n",
				runID, terminalDetail)
		}
		return finish(agentStateCancelled, nil)
	}
	return finish(agentStateDone, nil)
}

// watchLineEndedFailed says whether the run's own terminal line says the run
// FAILED — never the watcher's guess about the work. The reconciler writes
// run_finished's detail LED by the runstate word it checkpointed ("failed:
// the integrated gate refused ...", "completed: every tick closed ..."), and
// a death (run_died) is a run that did not reach its own run_finished: the
// process erred, panicked or was signalled — EXCEPT the one death that is a
// deliberate stop (tick vqc): a person's SIGINT, whose line the handler
// leads with the cancelled state word, the same word run_finished's
// deliberate stops are led by. Evictions, panics and operational errors
// keep the failed class a run's death has always had. Every other ending —
// completed, cancelled, a detail that carries no state word — is "ended",
// and ended without a failure is the done class's own answer; the last line
// says how.
func watchLineEndedFailed(stage, detail string) bool {
	switch stage {
	case reconcile.StageRunDied:
		return !strings.HasPrefix(detail, string(runstate.StateCancelled)+":")
	case reconcile.StageRunFinished:
		return strings.HasPrefix(detail, string(runstate.StateFailed)+":")
	}
	return false
}

// watchLineEndedCancelled says whether the run's own terminal line says the
// run was CANCELLED — stopped deliberately, never failed and never done
// (tick rix, the cancelled sibling of the classifier above). Three words
// spell that ending today, and the classifier must read every one of them,
// or the class stays a word nothing recognizes:
//
//   - "cancelled: …" — the state-led word a reconciler that checkpoints
//     runstate's cancelled word writes (vocabulary only today; the resume
//     path and a future cancel both read it);
//   - "the run is already cancelled: …" — the resume path's already-terminal
//     replay of a cancelled checkpoint, the one shape a cancelled end takes
//     in the feed today;
//   - "stopped: …" — the cloud factory's own word for a deliberate stop,
//     the word its finalize writes to the same run_finished stage — and the
//     word the overview already classifies a run's row by as cancelled.
//   - run_died LED by "cancelled: …" — the signal death a person's Ctrl-C
//     of a foreground run-epic writes (tick vqc): the one death that is a
//     deliberate stop, not a death to fix. The state-led word is the same
//     one run_finished spells, so the classifier reads one vocabulary
//     across both terminal stages; a SIGTERM eviction carries no state word
//     and stays the failed class.
//
// A COMPLETED ending is the done class's answer — "the run is already
// completed: …" is a resume replay that ended the work — so nothing broader
// than these words is classified here.
func watchLineEndedCancelled(stage, detail string) bool {
	// run_died accepts ONLY the state-led word (tick vqc): the deliberate
	// signal stop a person's SIGINT writes. Every other death — an eviction,
	// a panic, an operational error — is the failed class, and the words
	// below are run_finished's, never a death's.
	if stage == reconcile.StageRunDied {
		return strings.HasPrefix(detail, string(runstate.StateCancelled)+":")
	}
	if stage != reconcile.StageRunFinished {
		return false
	}
	return strings.HasPrefix(detail, string(runstate.StateCancelled)+":") ||
		strings.HasPrefix(detail, "the run is already "+string(runstate.StateCancelled)+":") ||
		strings.HasPrefix(detail, "stopped:")
}

// watchModelEndedFailed is the live view's form of the same question (tick
// bot), asked of the model the frames render: the lifecycle's own phase —
// the checkpoint the run wrote — with the feed's last word beside it for a
// run that died before it could write one. The same two authorities the
// overview classifies one run's row by, so no two surfaces answer one
// ending with different words.
func watchModelEndedFailed(model statusmodel.Model) bool {
	if model.Lifecycle.Phase == statusmodel.PhaseFailed {
		return true
	}
	if model.Liveness.LastEvent == nil {
		return false
	}
	return watchLineEndedFailed(model.Liveness.LastEvent.Stage, model.Liveness.LastEvent.Detail)
}

// watchModelEndedCancelled is the live view's form of the cancelled question
// (tick rix), asked of the same two authorities as the failed one — the
// lifecycle's own phase and the feed's last word — so the stream path and
// the live view answer one deliberate stop with the same class.
func watchModelEndedCancelled(model statusmodel.Model) bool {
	if model.Lifecycle.Phase == statusmodel.PhaseCancelled {
		return true
	}
	if model.Liveness.LastEvent == nil {
		return false
	}
	return watchLineEndedCancelled(model.Liveness.LastEvent.Stage, model.Liveness.LastEvent.Detail)
}

// watchEpicID is the epic id the clearing commands are addressed by: read
// out of the run's own id for a local run (`epic-<id>` is the run id's
// shape, and a bare epic id is accepted everywhere too), carried by the
// factory's run record for a cloud one, whose `run_` plus hex names the
// Workflow instance and spells no epic at all. A placeholder is not an
// id — a person copying the command the alert names must not have to
// look the epic up to fill it in (tick gtk).
//
// A LOCAL run whose id spells neither — one started as `ticfac run <epic>
// --run-id run-p` — has its epic only in its checkpoint, so that is where
// the answer comes from: the same records read the model path makes
// (statusRecords with no epic hint — the run directory this checkout
// holds; no tracker, no forge, nothing the one-shot surfaces pay for),
// then the same derivation epicIDOf applies. An id that guesses here and
// a model that reads there spelled two epics for one run (tick fub); the
// checkpoint is the truth both renderers now share. A run whose
// checkpoint cannot be read has no better answer than its own id — the
// same best-effort word the alert has always ended with.
func watchEpicID(repo, runID string, source runfeed.Source) string {
	if rest, ok := strings.CutPrefix(runID, "epic-"); ok && rest != "" {
		return rest
	}
	if cloud, ok := source.(*cloudFeedSource); ok && cloud.epic != "" {
		return cloud.epic
	}
	if records, _, err := statusRecords(repo, runID, ""); err == nil {
		if epic := epicIDOf(runID, records); epic != "" {
			return epic
		}
	}
	return runID
}

// watchRunStillAlive answers whether the run's own claim says it is going,
// the same way the watch's first question did: the pidfile for a local run,
// the factory's record for one the Workflow hosts.
func watchRunStillAlive(source runfeed.Source, kind, repo, runID string) bool {
	if cloudSource, ok := source.(*cloudFeedSource); ok && kind == "cloud" {
		return cloudRunStillGoing(cloudSource.State())
	}
	return runlife.Probe(repo, runID, time.Now()).State == runlife.Alive
}

// watchGatherModel gathers the status model the same way watchLive's frame
// builder does — the one model every surface renders, from the run's own
// durable sources — so the watch's one document and the live view's last
// frame cannot disagree.
func watchGatherModel(ctx context.Context, source runfeed.Source, kind, repo, runID string) (statusmodel.Model, error) {
	gather := modelGatherers{graph: epicGraph, ci: statusCI}
	if cloudSource, ok := source.(*cloudFeedSource); ok && kind == "cloud" {
		record, err := readCloudRunRecord(ctx, cloudSource.client, cloudSource.runID)
		if err != nil {
			return statusmodel.Model{}, err
		}
		liveness := cloudRunLiveness(ctx, cloudSource.runID, record.State)
		// Another project's run keeps this repo's records, tracker and PR
		// unread for it (tick nyi).
		repoProject, _ := cloudProjectOf(repo)
		return cloudStatusModel(ctx, cloudSource.client, repo, cloudSource.runID, record, liveness, io.Discard, gather,
			cloudRecordBelongsToRepo(repoProject, record.Project)), nil
	}
	probe := runlife.Probe(repo, runID, time.Now())
	return localStatusModel(ctx, repo, runID, probe, gather), nil
}

// watchHeldJSON is the one hold the ended run holds for a person, as the
// --json document carries it: the wait KIND — the reason class, e.g.
// held_for_person, merge, finding — what it holds, and the one command
// that moves it on.
type watchHeldJSON struct {
	Kind      string  `json:"kind"`
	What      string  `json:"what"`
	ClearWith *string `json:"clear_with,omitempty"`
}

// emitWatchJSON prints the watch's one document, ticfac.watch.v1: the state
// word, the model, and the hold when there is one. A model that cannot be
// gathered (a read that failed) is a document without it, never a failed
// command: the state word and the exit code carry the verdict.
func emitWatchJSON(ctx context.Context, source runfeed.Source, kind, repo, runID, state string, attention *statusmodel.Attention, stdout io.Writer) error {
	doc := struct {
		agentDoc
		RunID string             `json:"run_id"`
		Held  *watchHeldJSON     `json:"held,omitempty"`
		Model *statusmodel.Model `json:"model,omitempty"`
	}{
		agentDoc: agentDoc{Schema: agentSchemaID("watch"), State: state},
		RunID:    runID,
	}
	if attention != nil {
		clear := (*string)(nil)
		if attention.UnblockCommand != nil {
			clear = attention.UnblockCommand
		}
		doc.Held = &watchHeldJSON{Kind: attention.Kind, What: attention.What, ClearWith: clear}
	}
	if model, err := watchGatherModel(ctx, source, kind, repo, runID); err == nil {
		doc.Model = &model
	}
	return emitAgentJSON(stdout, doc)
}

// watchLive is the live view: one frame of the status model per interval,
// redrawn in place, until the run reaches its own end. The frame is the
// renderer's (watch_view.go); everything here is the mechanics the frame
// needs — the model rebuilt from the run's own sources, the drill-in views
// the keys open (watch_keys.go, watch_drill.go), the attention alert kept
// once per episode, and the end decided by the run's own last word, never
// by the watcher's patience. The feed itself is NOT replayed as lines above
// the block any more: the frame's own two-line tail carries it (epic hn6,
// rule 6), and `e` opens the whole feed — so the block stays one block, and
// a person who wants the stream has the stream path.
//
// The end itself (tick 2xk): on a keyboarded terminal, when stayOnEnd asks
// for it, the run's end does not close the dashboard — the end is kept
// above the block once and the view stands for drill-in until q or Ctrl-C
// closes it through the run's own ending, because after a run stops or
// fails is exactly when a person wants the per-tick attempts, reasons and
// gate evidence. Keyless — or the attach `ticfac run` puts up — the end
// exits at once, exactly as it always has.
func watchLive(ctx context.Context, source runfeed.Source, kind, repo, runID string, interval time.Duration, stayOnEnd bool, stdout, stderr io.Writer) int {
	if interval <= 0 {
		interval = defaultWatchInterval
	}
	width, height := 80, 24
	if w, h, ok := watchTerminalSize(stdout); ok {
		width, height = w, h
	}
	// The style set the environment names: the ANSI palette, unless NO_COLOR
	// or a dumb TERM said the terminal shows no colour (tick 5ba).
	styles := watchStylesForTerminal()
	// Needs-you is the most prominent thing on screen while it stands: the
	// durable copy kept above the block is red and bold, like the frame's
	// own hold lines.
	alertStyle := func(s string) string { return styles.red(styles.bold(s)) }

	// The keyboard, when the watch is running on one: raw mode so j/k and
	// the drill-in keys reach the program, restored on every exit path this
	// function can take — return, context cancellation, panic — by the
	// defer, and the keys themselves read on their own goroutine into a
	// channel the loop drains. A keyboard that is not a terminal (or will
	// not go raw) leaves the watch keyless, exactly as it always was.
	keys, restoreKeys, keyed := watchAttachKeys(ctx)
	// Raw mode takes the newline's carriage return away from the WHOLE
	// terminal — stdout and stderr write to the same device the keyboard was
	// set raw on — so while the keys are attached, every line the live view
	// writes ends \r\n; after the restore, a plain \n is right again, the
	// line discipline's ONLCR returning the carriage it always has. The
	// restore is idempotent and called BEFORE the watch's last words: the
	// frames and the end-of-watch message ride the same device, and the
	// closing message printed in raw mode would staircase just like the
	// frames did (tick r3x).
	eol := "\n"
	if keyed {
		eol = "\r\n"
	}
	restore := sync.OnceFunc(func() {
		if keyed {
			restoreKeys()
		}
	})
	defer restore()

	// The per-watch source caches: a frame every two seconds must not spawn
	// a tracker subprocess or ask a forge every two seconds (the rule
	// `status --follow` already holds for tk). The graph is invalidated the
	// moment the feed says the epic's shape changed.
	graphCache := &watchGraphCache{ttl: watchSourceTTL, read: epicGraph}
	ciCache := &watchCICache{ttl: watchSourceTTL, read: statusCI}
	gather := modelGatherers{graph: graphCache.Graph, ci: ciCache.CI}

	// The model builder: local and cloud gather through their own sources
	// (status_model.go), and both are the same model — the same frame renders
	// either host. A cloud host that cannot be read mid-watch costs the watch
	// a kept warning line and the frame its freshness, never the watch
	// itself: a follow is a long-lived read of a factory that may be
	// redeploying under it (the same policy the cloud feed source carries).
	var cloudSource *cloudFeedSource
	if kind == "cloud" {
		cloudSource, _ = source.(*cloudFeedSource)
	}
	// The checkout's project, read once per watch (tick nyi): a frame every
	// two seconds must not spawn a git subprocess any more than a tracker
	// one — and whether a run's records are this repo's to read does not
	// change while the watch stands.
	repoProject, _ := cloudProjectOf(repo)
	var lastGood *statusmodel.Model
	build := func() (statusmodel.Model, error) {
		if cloudSource == nil {
			probe := runlife.Probe(repo, runID, time.Now())
			return localStatusModel(ctx, repo, runID, probe, gather), nil
		}
		record, err := readCloudRunRecord(ctx, cloudSource.client, cloudSource.runID)
		if err != nil {
			return statusmodel.Model{}, err
		}
		liveness := cloudRunLiveness(ctx, cloudSource.runID, record.State)
		return cloudStatusModel(ctx, cloudSource.client, repo, cloudSource.runID, record, liveness, stderr, gather,
			cloudRecordBelongsToRepo(repoProject, record.Project)), nil
	}

	// Where the standing feed ends TODAY: history is not replayed — the
	// block starts at now. The standing lines are counted for the try
	// numbers the lines' own prefixes name, and kept whole for the feed
	// view the `e` key opens.
	var tries runfeed.Tries
	seen := int64(0)
	var feedEvents []runfeed.Event
	if standing, _, err := feedStanding(ctx, source); err == nil {
		feedEvents = make([]runfeed.Event, 0, len(standing))
		for _, line := range standing {
			tries.Observe(line.Event)
			if line.End > seen {
				seen = line.End
			}
			feedEvents = append(feedEvents, line.Event)
		}
	}

	// The interrupted end: the one end a watch that is still subscribed to a
	// live run can reach without the run's own word — the caller's context,
	// or, since the keys, the person's own q or Ctrl-C on the dashboard.
	// Same words, same codes, whatever interrupted it. The code is the
	// stream path's contract (tick 8v3, restored to the keys' live path by
	// tick iph): a hold stands above everything, a run that is still alive
	// is the running class — 5, so `ticfac run`'s attach reads a key the way
	// it reads a cancelled context, as a DETACH — and only a run that is
	// neither holding nor alive ends 1. The terminal is restored first:
	// the words it says are ordinary cooked output.
	interrupted := func(model statusmodel.Model) int {
		restore()
		fmt.Fprintf(stderr, "ticfac watch: the watch was interrupted before run %s said it ended; "+
			"`ticfac status %s` asks whether it is still alive\n", runID, runID)
		if watchHoldAttention(model) != nil {
			return ExitHeld
		}
		if watchRunStillAlive(source, kind, repo, runID) {
			return exitRunning
		}
		return 1
	}

	// The draw: the view the interaction state names, over the last frame,
	// clear to the end of the screen, write. The dashboard is the frame; the
	// drill views are its keys' answers.
	ui := watchUI{view: watchViewDashboard}
	previous := 0
	draw := func(model statusmodel.Model) {
		var frame []string
		switch ui.view {
		case watchViewFeed:
			frame = renderFeedView(feedEvents, &tries, &model, ui.scroll, width, height, styles)
		case watchViewTick:
			frame = renderTickView(model, ui.selected, styles, width, height)
		default:
			frame = renderWatchFrame(model, styles, width, height, ui.selected)
		}
		if previous > 0 {
			fmt.Fprintf(stdout, "\x1b[%dA\r\x1b[J", previous)
		}
		for _, line := range frame {
			fmt.Fprintf(stdout, "%s%s", line, eol)
		}
		previous = len(frame)
	}

	attentionRaised := false
	// The run's own end, stood past (tick 2xk): once the run has said it
	// ended and the dashboard is staying open for drill-in, the periodic
	// rebuild stops — an ended run's records do not change — and only the
	// keys (and the caller's context) move the view, redrawing the frozen
	// model. endNow is the exit q and Ctrl-C take there: the same restore,
	// the same last word below the final frame and the same exit class the
	// one-frame end has always had — never the interrupted words, which
	// would tell a caller the run is still going.
	ended := false
	var model statusmodel.Model
	endNow := func() int {
		restore()
		if last := watchLastWord(model); last != "" {
			fmt.Fprintf(stdout, "%s\n", last)
		}
		return watchEndHolding(model, runID, stderr)
	}
	for {
		// The standing view over an ended run: nothing below rebuilds, and
		// the select waits on the keys alone — the frozen frame answers
		// drill-in until q closes the watch.
		if ended {
			select {
			case <-ctx.Done():
				return endNow()
			case key, ok := <-keys:
				if !ok {
					// The keyboard is gone; an ended dashboard nobody can close
					// ends itself, the way the keyless end always has.
					return endNow()
				}
				if key == watchKeyCtrlC || (key == watchKeyQuit && ui.view == watchViewDashboard) {
					return endNow()
				}
				ui = ui.key(key, model)
				if ui.view == watchViewFeed {
					ui.scroll = watchClampScroll(ui.scroll, len(feedEvents), height)
				}
				// Redrawn immediately: a key is a person waiting, not a timer.
				draw(model)
			}
			continue
		}
		// The feed, for the shape changes that invalidate the graph cache
		// and the full feed the drill views read. A read that fails here is
		// a blip: the frame still renders (the model degrades "feed" and
		// says so), and the feed view keeps the lines it had.
		var located []runfeed.Located
		readOK := false
		if standing, _, err := feedStanding(ctx, source); err == nil {
			located = standing
			readOK = true
		}
		maxEnd := seen
		if readOK {
			// The feed is append-only, so a good read replaces the lines the
			// drill views show wholesale; a failed one is the blip that
			// keeps them.
			feedEvents = make([]runfeed.Event, 0, len(located))
			for _, line := range located {
				tries.Observe(line.Event)
				if line.End > maxEnd {
					maxEnd = line.End
				}
				feedEvents = append(feedEvents, line.Event)
				// The epic's shape changed mid-run: an absorbed finding became a
				// tick, or a replan moved one between waves. The cached graph is
				// stale from this line on, so the marked new row appears in the
				// next frame, not at the TTL's pleasure.
				if line.End > seen && (line.Event.Stage == reconcile.StageAbsorbed || line.Event.Stage == reconcile.StageReplanned) {
					graphCache.invalidate()
				}
			}
		}
		seen = maxEnd

		// The model: the frame's whole content, rebuilt from the run's own
		// durable sources.
		var buildErr error
		model, buildErr = build()
		switch {
		case buildErr != nil && lastGood == nil:
			// The watch ends here, and the words it says end it with: the
			// terminal is restored first, cooked output is cooked.
			restore()
			fmt.Fprintf(stderr, "ticfac watch: %v\n", buildErr)
			return 1
		case buildErr != nil:
			// Keep the warning where the person reading the block reads the
			// block's own history: above it, in the scrollback.
			keepAboveBlock(stdout, previous, width, eol, styles.red,
				fmt.Sprintf("the run's host could not be read: %v", buildErr))
			model = *lastGood
		default:
			modelCopy := model
			lastGood = &modelCopy
		}

		// The attention alert is kept above the block ONCE per episode: the
		// frame leads with it while it stands, and the scrollback keeps a
		// durable copy for the person who comes back late. It clears with
		// the episode, so a second hold in one run is a second alert.
		if text := watchAttentionAlertText(model); text != "" {
			if !attentionRaised {
				keepAboveBlock(stdout, previous, width, eol, alertStyle, text)
				attentionRaised = true
			}
		} else {
			attentionRaised = false
		}

		draw(model)

		if watchRunEnded(model) {
			if keyed && stayOnEnd {
				// The dashboard stands (tick 2xk): the end is kept above the
				// block once — a person coming back to the pane reads what
				// happened and that the keys still answer — and the standing
				// select above holds the view open for drill-in until q or
				// Ctrl-C closes it through the run's own ending.
				keepAboveBlock(stdout, previous, width, eol, styles.bold, watchEndedHint(runID))
				ended = true
				continue
			}
			// The run's own last word, in the scrollback below the final
			// frame: a person who comes back late reads how it ended. The
			// terminal is restored FIRST — the last word and the summary after
			// it are ordinary cooked output, and a line written in raw mode
			// would staircase just like the frames did (tick r3x).
			return endNow()
		}
		select {
		case <-ctx.Done():
			// Neither "ended" nor an error, and not exit 0: a caller waiting
			// on this command must not read an interrupted watch as a
			// finished run — the same contract the stream path holds.
			return interrupted(model)
		case <-time.After(interval):
		case key, ok := <-keys:
			if !ok {
				// The keyboard is gone (the reader ended); watch on without
				// it, the way a keyless watch runs.
				keys = nil
				continue
			}
			if key == watchKeyCtrlC || (key == watchKeyQuit && ui.view == watchViewDashboard) {
				// q on the dashboard and Ctrl-C anywhere end the watch the
				// way SIGINT always has — the same words, the same exit
				// code — while in a drill view q only comes back down.
				return interrupted(model)
			}
			ui = ui.key(key, model)
			if ui.view == watchViewFeed {
				ui.scroll = watchClampScroll(ui.scroll, len(feedEvents), height)
			}
			// Redrawn immediately: a key is a person waiting, not a timer.
			draw(model)
		}
	}
}

// watchClampScroll bounds the feed view's scroll to what the window can
// show: no further up than the feed's oldest line, never below zero. An
// unknown height shows everything, and there is nothing to scroll.
func watchClampScroll(scroll, events, height int) int {
	if height <= 0 {
		return 0
	}
	top := events - height
	if top < 0 {
		top = 0
	}
	if scroll > top {
		return top
	}
	if scroll < 0 {
		return 0
	}
	return scroll
}

// watchRunEnded is the run's own answer to "is there anything left to
// watch": it must be the run's, never the watcher's. A LIVE run is always
// still going (its earlier endings are history — the resumed-run defect,
// tick usx); a dead one has ended when its own word says so — the last line
// of its feed, or its lifecycle reaching a terminal phase — and a completed
// run whose merge is left (the one thing that is a person's by design) has
// ended too, holding the PR for the person.
//
// The feed's last word is the MODEL's, not the loop's earlier read: the
// model is built after it and re-reads the feed, so a line that lands
// between the two is already in the model's answer — reading it from the
// older snapshot could end the watch on the records and then print no last
// word at all.
func watchRunEnded(model statusmodel.Model) bool {
	if model.Liveness.Alive {
		return false
	}
	switch model.Lifecycle.Phase {
	case statusmodel.PhaseDone, statusmodel.PhaseFailed, statusmodel.PhaseCancelled, statusmodel.PhaseMerge:
		return true
	}
	if model.Liveness.LastEvent != nil {
		stage := model.Liveness.LastEvent.Stage
		return stage == reconcile.StageRunFinished || stage == reconcile.StageRunDied
	}
	return false
}

// watchEndedHint is the line kept above the block when the run's own end
// leaves the dashboard standing (tick 2xk): what happened, and that the
// keys still answer over the ended run until q closes the watch through
// the run's own ending.
func watchEndedHint(runID string) string {
	return fmt.Sprintf("ticfac watch: run %s has ended — the dashboard stays open: enter opens a tick, e the whole feed; q closes the watch", runID)
}

// watchLastWord is the run's own terminal line, said plainly for the
// scrollback below the final frame.
func watchLastWord(model statusmodel.Model) string {
	if model.Liveness.LastEvent == nil {
		return ""
	}
	switch model.Liveness.LastEvent.Stage {
	case reconcile.StageRunFinished, reconcile.StageRunDied:
		var tries runfeed.Tries
		return watchEventLine(*model.Liveness.LastEvent, &tries)
	}
	return ""
}

// watchEventLine is the one-line form the drill views' lines share: the
// line's own clock, the tick's own try, the typed stage and the detail —
// the same words everywhere a feed line is printed for a person, so a
// person reading a log and a person reading the block read one vocabulary.
func watchEventLine(event runfeed.Event, tries *runfeed.Tries) string {
	return fmt.Sprintf("%s %-12s %s: %s", clockOf(event.At), watchEventWho(event, tries), event.Stage, event.Detail)
}

// watchEventLineStyled is the same one-line form carrying the palette
// (tick 5ba): the timestamp dim — secondary text — the tick's own id cyan,
// a refusal's stage red. The identity style set renders it byte for byte as
// the plain form, so the words a pipe prints and the words a terminal
// shows cannot drift apart.
func watchEventLineStyled(event runfeed.Event, tries *runfeed.Tries, st watchStyles) string {
	who := dashPad(watchEventWho(event, tries), 12)
	if event.TickID != nil && *event.TickID != "" {
		who = st.cyan(who)
	}
	stage := event.Stage
	switch stage {
	case reconcile.StageRejected, reconcile.StageGateFailed:
		stage = st.red(stage)
	}
	return fmt.Sprintf("%s %s %s: %s", st.dim(clockOf(event.At)), who, stage, event.Detail)
}

// watchEventWho is the line's own "who": the run, or the tick with its own
// try number when the count has one (tick h58) — never the run-wide
// dispatch number the line's attempt field carries.
func watchEventWho(event runfeed.Event, tries *runfeed.Tries) string {
	who := "run"
	if event.TickID != nil && *event.TickID != "" {
		who = *event.TickID
		if event.Attempt != nil {
			if try, ok := tries.Of(who, *event.Attempt); ok {
				who = fmt.Sprintf("%s#%d", who, try)
			}
		}
	}
	return who
}

// insertAboveBlock writes lines ABOVE the live block, so the block keeps its
// place and the lines keep theirs in the scrollback: up to the top of the
// block, one inserted blank line per kept line (the terminal pushes the
// block down), the line written on it, and back down below the block. The
// lines are cut to the pane's width first: a kept line that wraps pushes the
// block down rows the cursor arithmetic above does not know about, and the
// next frame would draw over the block's own rows (tick r3x). When no block
// stands yet the lines simply print — they become the top of the scrollback
// the block then draws under. `eol` is the line ending the block itself is
// drawn with — \r\n while the keyboard's raw mode is on (tick r3x).
func insertAboveBlock(w io.Writer, previous, width int, eol string, lines []string) {
	if width > 0 {
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, width, "")
		}
	}
	if previous <= 0 {
		for _, line := range lines {
			fmt.Fprintf(w, "%s%s", line, eol)
		}
		return
	}
	fmt.Fprintf(w, "\x1b[%dA\r", previous)
	for _, line := range lines {
		fmt.Fprintf(w, "\x1b[L%s%s", line, eol)
	}
	fmt.Fprintf(w, "\x1b[%dB", previous)
}

// keepAboveBlock wraps plain text to the pane's width, styles each line and
// hands it to insertAboveBlock. The lines kept above the block must each fit
// the pane — a kept line that wrapped itself would push the block down rows
// the redraw's cursor arithmetic does not know about, and the next frame
// would draw over the block's own rows (tick r3x) — but the text is kept
// whole, word-wrapped, because the copy's point is that a person coming back
// late can still read the command that moves the hold on.
func keepAboveBlock(w io.Writer, previous, width int, eol string, style func(string) string, text string) {
	lines := wrapWords(text, width)
	for i, line := range lines {
		lines[i] = style(line)
	}
	insertAboveBlock(w, previous, width, eol, lines)
}

// wrapWords wraps one line of plain text to at most `width` cells, breaking
// at spaces where one fits inside the limit, and hard-breaking a single word
// longer than the width. The texts it wraps are the watch's own plain
// sentences — no escape sequences, no double-width runes — so a rune is a
// cell here.
func wrapWords(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	lines := []string{}
	line := ""
	for _, word := range strings.Split(text, " ") {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		switch {
		case len([]rune(candidate)) <= width:
			line = candidate
		case line != "":
			lines = append(lines, line)
			line = hardWrapWord(word, width, &lines)
		default:
			line = hardWrapWord(word, width, &lines)
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

// hardWrapWord lays one word longer than the width into lines of its own,
// hard-broken every `width` cells, and returns the word's last line — the
// one the wrap's next word continues on.
func hardWrapWord(word string, width int, lines *[]string) string {
	runes := []rune(word)
	for len(runes) > width {
		*lines = append(*lines, string(runes[:width]))
		runes = runes[width:]
	}
	return string(runes)
}

// watchAttentionAlertText is the durable copy of the frame's attention line,
// kept above the block once per episode: the run is holding for a person,
// what it holds, and the command that moves it on. The words are plain —
// keepAboveBlock wraps and styles them — so the text is testable on its own.
func watchAttentionAlertText(m statusmodel.Model) string {
	for _, a := range m.Attention {
		if !a.NeedsPerson {
			continue
		}
		text := fmt.Sprintf("! ticfac watch: run %s is holding for a person: %s", m.RunID, a.What)
		if a.UnblockCommand != nil && *a.UnblockCommand != "" {
			text += " — move it on: " + *a.UnblockCommand
		}
		return text
	}
	return ""
}

// watchHoldAttention is the one thing the ended run holds for a person: a
// hold only a person releases, the merge that is a person's by design, or
// findings nobody triaged. The dead-run wait is deliberately NOT here: a run
// that died without its own terminal word is `ticfac status`'s question (and
// the stream path's never-returning follow), not an end the watch reports —
// and the model's dead-run wait can also be the artifact of records this
// checkout cannot read, which is a fact to say on the frame, not one to
// branch an exit code on.
func watchHoldAttention(m statusmodel.Model) *statusmodel.Attention {
	for i := range m.Attention {
		a := m.Attention[i]
		if !a.NeedsPerson {
			continue
		}
		switch a.Kind {
		case statusmodel.WaitHeldForPerson, statusmodel.WaitMerge, statusmodel.WaitFinding:
			return &a
		}
	}
	return nil
}

// watchEndHolding is the live view's last word: the final frame stands, the
// last line of the feed is in the scrollback, and what the run ended holding
// for a person is said to stderr — where the stream path says it — with the
// command that moves it on. The exit code is the contract a script waits on:
// held (3) when a person can move the end, failed (1) when the run ended in
// its own failure (tick bot), cancelled (7) when it was stopped deliberately
// (tick rix), done (0) otherwise.
func watchEndHolding(model statusmodel.Model, runID string, stderr io.Writer) int {
	attention := watchHoldAttention(model)
	if attention == nil {
		if watchModelEndedFailed(model) {
			detail := ""
			if model.Liveness.LastEvent != nil {
				detail = model.Liveness.LastEvent.Detail
			}
			fmt.Fprintf(stderr, "\nticfac watch: run %s ended FAILED:\n%s\n", runID, detail)
			fmt.Fprintf(stderr, "Nothing is held for a person: the work has to be fixed and the epic run again — "+
				"`%s` resumes it under this run id, without redoing what "+
				"already passed. The evidence is on the integration branch, not in this line.\n\n",
				statusmodel.ResumeCommand(model.Host, model.EpicID))
			return exitGeneric
		}
		if watchModelEndedCancelled(model) {
			detail := ""
			if model.Liveness.LastEvent != nil {
				detail = model.Liveness.LastEvent.Detail
			}
			fmt.Fprintf(stderr, "\nticfac watch: run %s ended CANCELLED:\n%s\n", runID, detail)
			fmt.Fprintf(stderr, "The run was stopped deliberately: the work is neither done nor failed, and "+
				"nothing is held for a person. The evidence is on the integration branch, not in this line.\n\n")
			return exitCancelled
		}
		return 0
	}
	sayWatchHold(runID, attention, stderr)
	return ExitHeld
}

// sayWatchHold is the one wording both watch paths end a hold by — the live
// view's last word and the stream's, the same sentence, so a person reading
// a log and a person reading the block read one vocabulary (tick 4mv: the
// pipe ended 3 for the merge hold while saying nothing; a stop that names
// nothing is a stop an operator has to dig for).
func sayWatchHold(runID string, attention *statusmodel.Attention, stderr io.Writer) {
	fmt.Fprintf(stderr, "\nticfac watch: run %s ended holding something for a person:\n%s\n", runID, attention.What)
	if attention.UnblockCommand != nil && *attention.UnblockCommand != "" {
		fmt.Fprintf(stderr, "move it on: %s\n", *attention.UnblockCommand)
	}
	fmt.Fprintf(stderr, "The evidence is on the integration branch, not in this line.\n\n")
}

// watchGraphCache serves the tracker's graph at most once per TTL: a frame
// every two seconds must not spawn tk every two seconds (the rule
// `status --follow` set for its labels). Whatever the read answers — nil is
// the honest answer of an unreadable tracker — is the answer until the TTL
// or an invalidation says otherwise, so a tracker that heals is picked up
// within half a minute and a shape change within a frame.
type watchGraphCache struct {
	ttl   time.Duration
	read  func(context.Context, string, string) *tk.Graph
	graph *tk.Graph
	at    time.Time
}

func (c *watchGraphCache) Graph(ctx context.Context, repo, epicID string) *tk.Graph {
	if c.graph != nil && time.Since(c.at) < c.ttl {
		return c.graph
	}
	c.graph, c.at = c.read(ctx, repo, epicID), time.Now()
	return c.graph
}

func (c *watchGraphCache) invalidate() { c.graph = nil }

// watchCICache serves the forge's answer at most once per TTL, for the same
// reason the graph is cached — with the extra weight that each ask resolves
// a credential and opens a remote. An error is returned fresh (a forge that
// cannot be asked is a fact the model degrades per frame, never one it
// silently freezes); a successful answer, nil included, is cached.
type watchCICache struct {
	ttl    time.Duration
	read   func(context.Context, string, string) (*statusmodel.CIInput, error)
	ci     *statusmodel.CIInput
	cached bool
	at     time.Time
}

func (c *watchCICache) CI(ctx context.Context, repo, epicID string) (*statusmodel.CIInput, error) {
	if c.cached && time.Since(c.at) < c.ttl {
		return c.ci, nil
	}
	ci, err := c.read(ctx, repo, epicID)
	if err != nil {
		return nil, err
	}
	c.ci, c.cached, c.at = ci, true, time.Now()
	return ci, nil
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
