package cli

// `ticfac watch <run> <tick>` and `ticfac steer` (epic 43y step 8, tick
// y03): the operator's live window into ONE worker — its thinking, its tool
// calls and their output, its answers — and the operator's voice in it.
//
// A pi-durable worker's conversation is durable and served live by the
// process that owns it: locally through the attempt's steer socket (the
// harness's own door, harness/src/local/steer-socket.ts), in the cloud
// through the attempt's WorkerAgent watch socket. Both speak one frame
// shape, so internal/workerview folds and draws either; this file decides
// WHICH worker, reaches it, and keeps the window open across the worker's
// own process boundaries:
//
//   - a local worker's process ends between a report pushback or a nudge
//     and its relaunch, and the socket goes with it — the watch waits for
//     the next process on the socket file (a condition, never a guess) and
//     re-attaches; the conversation is the same, so the view continues;
//   - the watch ends, 0, when the attempt has settled (the supervisor
//     recorded the runner's last exit, or the WorkerAgent says settled);
//     Ctrl-C ends it 5, the run's own "still going" class.
//
// A steer is input placed after the worker's running tool round, in the
// same conversation, durably admitted before it is acknowledged — the very
// mechanism the supervisor's stuck nudge uses. It is the one write this
// command family makes, and it is the operator's to make: the worker reads
// it as a message from the person running it.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/workerview"
)

// defaultWorkerHeartbeat is how often a piped worker watch writes f6o's
// activity line: inside the "every 2-5 minutes" f6o asks for, at its fast
// end, because the line is what tells a log reader a quiet worker from a
// stuck one.
const defaultWorkerHeartbeat = 2 * time.Minute

// workerPoll is how often the watch looks for a worker's next process when
// it has none: the socket file's presence and the settle marker are two
// stats, so the cadence is a watcher's reaction time, not a load.
var workerPoll = 500 * time.Millisecond

// workerRedraw is the live frame's tick: the ages it draws advance between
// commits.
var workerRedraw = time.Second

// workerWatchFlags are the flags only the worker view reads.
type workerWatchFlags struct {
	attempt   *int
	heartbeat *time.Duration
	stateRoot *string
}

func addWorkerWatchFlags(fs *flag.FlagSet) workerWatchFlags {
	return workerWatchFlags{
		attempt:   fs.Int("attempt", 0, "with a tick: the attempt to watch (default: the tick's newest)"),
		heartbeat: fs.Duration("heartbeat", defaultWorkerHeartbeat, "with a tick, on a pipe: how often to write the worker's heartbeat line"),
		stateRoot: fs.String("state-root", "", "with a tick: where the run keeps attempt state (default: the run's own default)"),
	}
}

// defaultExecStateRoot is where a run keeps its attempts' state when nothing
// says otherwise — reconcile's own default for ExecStateRoot, spelled from
// the same function.
func defaultExecStateRoot() string { return filepath.Join(subprocess.DefaultStateDir(), "runs") }

// workerSource is one way to reach a worker's live conversation.
type workerSource interface {
	// title names the worker in the frame: "<tick>#<attempt> · <run>".
	title() string
	// attach opens one watch. The frames channel closes when the watch
	// ends; err is non-nil when there is nothing to attach to now.
	attach(ctx context.Context) (frames <-chan workerview.Frame, err error)
	// settled says the attempt is over: nothing will be served again.
	settled(m *workerview.Model) bool
}

// localWorker is a worker on this machine, reached through its door.
type localWorker struct {
	runID string
	door  subprocess.WorkerDoor
}

func (w *localWorker) title() string {
	return fmt.Sprintf("%s#%d · %s", w.door.TickID, w.door.Attempt, w.runID)
}

// errNotListening is a door with no process behind it right now.
var errNotListening = errors.New("no worker process is listening")

func (w *localWorker) attach(ctx context.Context) (<-chan workerview.Frame, error) {
	if !w.door.Listening() {
		return nil, errNotListening
	}
	conn, err := subprocess.OpenWatch(w.door.SteerSock)
	if err != nil {
		return nil, err
	}
	return streamFrames(ctx, conn), nil
}

func (w *localWorker) settled(*workerview.Model) bool { return w.door.Settled() }

// streamFrames reads a line-framed watch until it ends, the reader fails or
// ctx is done, closing the channel then. A read that fails mid-frame is the
// connection's end, not the watch's: the caller re-attaches or finishes.
func streamFrames(ctx context.Context, conn net.Conn) <-chan workerview.Frame {
	out := make(chan workerview.Frame, 64)
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go func() {
		defer close(out)
		defer conn.Close()
		_ = workerview.ReadFrames(conn, func(f workerview.Frame) error {
			select {
			case out <- f:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return out
}

// workerOutput is where the watch says what it sees: a frame redrawn in
// place on a terminal, plain lines on a pipe.
type workerOutput interface {
	update(m *workerview.Model, now time.Time)
	tick(m *workerview.Model, now time.Time)
	note(line string)
}

// followWorker is the watch's loop: attach, fold every frame into the
// model, redraw, and when the stream ends either finish (the attempt
// settled) or wait for the worker's next process and attach again.
func followWorker(ctx context.Context, src workerSource, out workerOutput, now func() time.Time, stderr io.Writer) int {
	model := workerview.New()
	waiting := false
	ticker := time.NewTicker(workerRedraw)
	defer ticker.Stop()
	for {
		frames, err := src.attach(ctx)
		if err != nil {
			if isWorkerRefusal(err) {
				fmt.Fprintf(stderr, "ticfac watch: %s cannot be watched: %v\n", src.title(), err)
				return exitGeneric
			}
			if src.settled(model) {
				out.note(fmt.Sprintf("■ %s settled — its conversation is over", src.title()))
				return exitSuccess
			}
			if !waiting {
				out.note(fmt.Sprintf("… waiting for %s's next process (%v)", src.title(), err))
				waiting = true
			}
			select {
			case <-ctx.Done():
				return interruptedWorkerWatch(src, stderr)
			case <-time.After(workerPoll):
			}
			continue
		}
		waiting = false
		model.Ended = ""
	stream:
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					break stream
				}
				if err := model.Apply(f, now()); err != nil {
					out.note(fmt.Sprintf("! a frame could not be read: %v", err))
					continue
				}
				out.update(model, now())
				if src.settled(model) {
					out.note(fmt.Sprintf("■ %s settled — its conversation is over", src.title()))
					return exitSuccess
				}
			case <-ticker.C:
				out.tick(model, now())
			case <-ctx.Done():
				return interruptedWorkerWatch(src, stderr)
			}
		}
		if ctx.Err() != nil {
			return interruptedWorkerWatch(src, stderr)
		}
		if model.Ended != "" {
			out.note("■ " + model.Ended)
		}
	}
}

func interruptedWorkerWatch(src workerSource, stderr io.Writer) int {
	fmt.Fprintf(stderr, "ticfac watch: the watch of %s was interrupted; the worker keeps going\n", src.title())
	return exitRunning
}

// liveWorkerOutput redraws the frame in place, the run watch's way: up over
// the last frame, clear to the end of the screen, write.
type liveWorkerOutput struct {
	w             io.Writer
	title         string
	width, height int
	previous      int
	lastDraw      time.Time
}

func (o *liveWorkerOutput) draw(m *workerview.Model, now time.Time) {
	frame := workerview.Render(m, workerview.FrameOptions{Title: o.title, Width: o.width, Height: o.height - 1, Now: now})
	if o.previous > 0 {
		fmt.Fprintf(o.w, "\x1b[%dA\r\x1b[J", o.previous)
	}
	for _, line := range frame {
		fmt.Fprintf(o.w, "%s\n", line)
	}
	o.previous = len(frame)
	o.lastDraw = now
}

// update redraws at most ten times a second: a streaming model commits
// faster than a terminal is worth redrawing.
func (o *liveWorkerOutput) update(m *workerview.Model, now time.Time) {
	if now.Sub(o.lastDraw) >= 100*time.Millisecond {
		o.draw(m, now)
	}
}

func (o *liveWorkerOutput) tick(m *workerview.Model, now time.Time) { o.draw(m, now) }

func (o *liveWorkerOutput) note(line string) {
	insertAboveBlock(o.w, o.previous, []string{line})
}

// plainWorkerOutput streams one line per settled item, and f6o's heartbeat
// line every interval: what a log or a pipe wants.
type plainWorkerOutput struct {
	w         io.Writer
	title     string
	heartbeat time.Duration
	// printed is every item said so far, by identity: a re-attaching watch
	// (the worker's next process) and pi-durable's overflow snapshots
	// re-state the conversation, and a log says each thing once.
	printed  map[string]bool
	lastBeat time.Time
	prevBeat *workerview.Heartbeat
}

func (o *plainWorkerOutput) update(m *workerview.Model, now time.Time) {
	if o.printed == nil {
		o.printed = map[string]bool{}
	}
	for _, it := range m.Items {
		// A frame is a whole commit, and the commit that places a steer
		// carries both its entry and the submission that labels it: the
		// line is printed with its label, once.
		if o.printed[it.ID] {
			continue
		}
		o.printed[it.ID] = true
		fmt.Fprintln(o.w, workerview.PlainLine(it))
	}
	if o.lastBeat.IsZero() {
		o.lastBeat = now
	}
}

func (o *plainWorkerOutput) tick(m *workerview.Model, now time.Time) {
	if o.heartbeat <= 0 || o.lastBeat.IsZero() || now.Sub(o.lastBeat) < o.heartbeat {
		return
	}
	fmt.Fprintln(o.w, workerview.HeartbeatLine(o.title, m.Heartbeat, o.prevBeat, now.Sub(o.lastBeat), now))
	beat := m.Heartbeat
	o.prevBeat, o.lastBeat = &beat, now
}

func (o *plainWorkerOutput) note(line string) { fmt.Fprintln(o.w, line) }

// workerWatchEntry is `ticfac watch <run> <tick>`'s argument check: the
// worker view is a live window, not a document, so --json is refused rather
// than answered with something that is not the run watch's document.
func workerWatchEntry(ctx context.Context, args []string, repo *string, asJSON *bool, fl workerWatchFlags, stdout, stderr io.Writer) int {
	if args[0] == "" || args[1] == "" {
		fmt.Fprintln(stderr, "ticfac watch: a run id (or epic id) and a tick id are required")
		return exitUsage
	}
	if *asJSON {
		fmt.Fprintln(stderr, "ticfac watch: a worker's watch is a live stream, not one document — --json answers for a run's watch "+
			"(the run id alone); pipe the worker's watch for plain lines")
		return exitUsage
	}
	dir := *repo
	if dir == "" {
		dir = "."
	}
	return workerWatchCommand(ctx, dir, args[0], args[1], fl, stdout, stderr)
}

// workerWatchCommand is `ticfac watch <run> <tick>`.
func workerWatchCommand(ctx context.Context, repo, runArg, tickID string, fl workerWatchFlags, stdout, stderr io.Writer) int {
	src, code := resolveWorker(ctx, "watch", repo, runArg, tickID, *fl.attempt, *fl.stateRoot, stderr)
	if src == nil {
		return code
	}
	var out workerOutput
	if watchIsTerminal(stdout) {
		width, height := 80, 24
		if w, h, ok := watchTerminalSize(stdout); ok {
			width, height = w, h
		}
		out = &liveWorkerOutput{w: stdout, title: src.title(), width: width, height: height}
	} else {
		out = &plainWorkerOutput{w: stdout, title: src.title(), heartbeat: *fl.heartbeat}
	}
	return followWorker(ctx, src, out, time.Now, stderr)
}

// resolveWorker decides which worker the command addresses: this machine's
// attempt of the tick when the run keeps one here, else the factory's.
func resolveWorker(ctx context.Context, verb, repo, runArg, tickID string, attempt int, stateRoot string, stderr io.Writer) (workerSource, int) {
	if stateRoot == "" {
		stateRoot = defaultExecStateRoot()
	}
	resolution := resolveRunArg(repo, runArg)
	door, err := subprocess.FindWorkerDoor(stateRoot, resolution.RunID, tickID, attempt)
	if err == nil {
		return &localWorker{runID: resolution.RunID, door: door}, exitSuccess
	}
	if errors.Is(err, subprocess.ErrNoDurableWorker) {
		fmt.Fprintf(stderr, "ticfac %s: %v\n", verb, err)
		return nil, exitGeneric
	}
	cloud, cloudErr := resolveCloudWorker(ctx, repo, runArg, tickID, attempt)
	if cloud != nil {
		return cloud, exitSuccess
	}
	fmt.Fprintf(stderr, "ticfac %s: no worker of tick %s to reach: %v", verb, tickID, err)
	if cloudErr != nil {
		fmt.Fprintf(stderr, "; and the factory: %v", cloudErr)
	}
	fmt.Fprintln(stderr)
	return nil, exitGeneric
}

// newSteerCommand builds `ticfac steer`.
func newSteerCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "steer <run-id|epic-id> <tick-id> <text…>",
		Short: "say something to a running worker: placed after its current tool round, in the same conversation",
		Long: `The operator's voice in a running pi-durable worker (tick y03). The text is
placed in the worker's conversation after the tool round it is running — the
same mechanism the stuck nudge uses — and acknowledged only once it is
durably admitted, so an acknowledged steer survives the worker's process
dying. The worker reads it as a message from the person running it.

The worker is the tick's newest attempt (--attempt names another): this
machine's when the run keeps it here, else the factory's. Watch the steer
land with 'ticfac watch <run> <tick>'.

Exit codes: 0 the steer was admitted, 1 it was refused or the worker could
not be reached (the reason is said), 2 usage.`,
	}
	fs := flag.NewFlagSet("steer", flag.ContinueOnError)
	repo := fs.String("repo", "", "the checkout the run works in (default: cwd)")
	attempt := fs.Int("attempt", 0, "the attempt to steer (default: the tick's newest)")
	stateRoot := fs.String("state-root", "", "where the run keeps attempt state (default: the run's own default)")
	requestID := fs.String("request-id", "", "an idempotency key: a retried steer with the same key is placed once (default: a fresh one)")
	asJSON := fs.Bool("json", false, "answer with one versioned document (ticfac.steer.v1)")
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(steerCommand(c.Context(), args, *repo, *attempt, *stateRoot, *requestID, *asJSON, stdout, stderr))
	}
	return cmd
}

// steerDoc is `steer --json`'s document.
type steerDoc struct {
	agentDoc
	RunID     string `json:"run_id"`
	TickID    string `json:"tick_id"`
	Attempt   int    `json:"attempt"`
	Host      string `json:"host"`
	RequestID string `json:"request_id"`
	Error     string `json:"error,omitempty"`
}

func steerCommand(ctx context.Context, args []string, repo string, attempt int, stateRoot, requestID string, asJSON bool, stdout, stderr io.Writer) int {
	if len(args) < 3 || strings.TrimSpace(strings.Join(args[2:], " ")) == "" {
		fmt.Fprintln(stderr, "ticfac steer: a run id (or epic id), a tick id and the text to say are required")
		return exitUsage
	}
	if repo == "" {
		repo = "."
	}
	text := strings.Join(args[2:], " ")
	if requestID == "" {
		requestID = fmt.Sprintf("operator-steer-%d", time.Now().UnixNano())
	}
	src, code := resolveWorker(ctx, "steer", repo, args[0], args[1], attempt, stateRoot, stderr)
	if src == nil {
		if asJSON {
			_ = emitAgentJSON(stdout, steerDoc{agentDoc: agentDoc{Schema: agentSchemaID("steer"), State: agentStateFailed},
				TickID: args[1], RequestID: requestID, Error: "the worker could not be found"})
		}
		return code
	}
	doc := steerDoc{agentDoc: agentDoc{Schema: agentSchemaID("steer"), State: agentStateDone}, TickID: args[1], RequestID: requestID}
	var err error
	switch w := src.(type) {
	case *localWorker:
		doc.RunID, doc.Attempt, doc.Host = w.runID, w.door.Attempt, "local"
		err = subprocess.Steer(w.door.SteerSock, text, requestID)
	case *cloudWorker:
		doc.RunID, doc.Attempt, doc.Host = w.runID, w.attempt, "cloud"
		err = w.steer(ctx, text, requestID)
	}
	if err != nil {
		doc.State, doc.Error = agentStateFailed, err.Error()
		fmt.Fprintf(stderr, "ticfac steer: %s was not steered: %v\n", src.title(), err)
	} else if !asJSON {
		fmt.Fprintf(stdout, "steered %s: placed after its current tool round (%s)\n", src.title(), requestID)
	}
	if asJSON {
		_ = emitAgentJSON(stdout, doc)
	}
	return stateExitClass(doc.State)
}
