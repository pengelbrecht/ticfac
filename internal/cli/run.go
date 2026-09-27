package cli

// `ticfac run <epic-id>` (tick 9sz): the one command for a local run.
//
// It replaces the incantation the operator was reduced to —
//
//	GITHUB_TOKEN=$(gh auth token) nohup ticfac run-epic -profiles <path> -wall 86400 <epic> &
//
// — with `ticfac run <epic>`. Everything that line carried by hand is
// decided here:
//
//   - the run starts in the BACKGROUND, detached from this terminal (its own
//     session, output to the run's own start.log beside its feed), and the
//     live view attaches to it — the same one `ticfac watch` renders, so
//     "three commands to follow one run" is one command. Ctrl-C detaches the
//     view and NEVER stops the run: a detached child is not in the
//     terminal's session, so the terminal's signals cannot reach it.
//
//   - a SECOND invocation attaches to a live run and resumes a stopped one.
//     Liveness is the run's own claim — runlife's pidfile, `ps`-verified —
//     never a guess about processes: the same answer `ticfac status` gives.
//
//   - herdr is DETECTED (a live socket, resolved the same way the run's own
//     herdr executor dials it) and a live server dispatches into herdr
//     panes with the profile set EMBEDDED in this binary — no filesystem
//     path. --no-herdr opts out of both the probe and the panes; --profiles
//     stays as an expert override, forwarded verbatim.
//
//   - the GitHub token: resolved when, and only when, the repository's own
//     close-out rule needs a forge (tick hio), and the run-epic this command
//     starts SAYS which rung answered — the note rides the child's own
//     stdout, into its run.log.
//
//   - no wall clock is injected: --wall N is an opt-in per-job backstop.
//     Unnamed, the per-job bound stays the reconciler's own documented
//     default — making `run` carry one of its own would be a wall clock by
//     another door; the bound that replaces it (liveness, tick b1t) is a
//     reconciler change, not a flag.
//
// The epic id is accepted everywhere it was spelled as a run id: `ticfac run
// epic-gvc` and `ticfac run gvc` drive the same run, whose internal identity
// stays epic-<id>. `ticfac run-epic` remains the foreground form scripts
// drive; everything this command starts IS that command, in the background.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/herd/client"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// startLogName is where this command keeps the started run's first words:
// everything the child prints before it claims the run's life, and its
// refusal if it never does. runlife's run.log takes over at the claim; the
// two live beside each other under the feed's own directory, which the
// run-state gitignore fragment already marks as exhaust.
const startLogName = "start.log"

// runClaimWait bounds how long the attach waits for the started run to
// CLAIM its life (runlife's pidfile) before saying so and letting go. The
// claim is made after the reconciler's own construction — tracker, git,
// profiles — which is seconds, not minutes; a run past this bound without
// a claim is a fact to report, never one to keep silently waiting on.
const runClaimWait = 60 * time.Second

// runClaimPoll is how often the attach looks for the claim while it waits.
// It is the interval the CONDITION is looked at, not a guess about the
// work: the observables are the pidfile and the child's exit, and both are
// cheap to ask.
const runClaimPoll = 250 * time.Millisecond

// runFlags is `run`'s flag surface: the defaults ARE the design. --repo for
// driving another checkout; --no-herdr to opt out of the detection; the two
// expert overrides the description keeps (--profiles, --wall). Everything
// else the old incantation named is decided, not asked. --cloud is the one
// cloud parity flag (tick ejw): the same command against the factory.
type runFlags struct {
	repo     *string
	noHerdr  *bool
	profiles *string
	wall     *int
	cloud    *bool
	asJSON   *bool
}

func defineRunFlags(fs *flag.FlagSet) *runFlags {
	return &runFlags{
		repo: fs.String("repo", "", "the checkout the run works in (default: cwd)"),
		noHerdr: fs.Bool("no-herdr", false,
			"skip the herdr detection and dispatch with the profiles embedded as this binary's default (the local set)"),
		profiles: fs.String("profiles", "",
			"resolve role profiles from this directory, or \"herdr\" for the set embedded in this binary — "+
				"the default when a live herdr is detected (an expert override, forwarded to the run verbatim)"),
		wall: fs.Int("wall", 0,
			"an opt-in backstop: the wall clock one job is bounded by. Unnamed, no bound is injected and the "+
				"reconciler's own per-job bound applies"),
		cloud: fs.Bool("cloud", false,
			"run the epic in your cloud factory: submit it to the configured factory and attach the same live view "+
				"— the same verbs, view and triage as a local run (the expert `ticfac cloud ...` commands stay for the rest)"),
		asJSON: fs.Bool("json", false,
			"answer as one versioned document (ticfac.run.v1) when the command ends: what it did — attached, started, resumed — and how that ended, with the exit-table state word (done, running, held, failed, cancelled). The run's own prose goes to stderr, so stdout is the document's alone"),
	}
}

// newRunCommand builds the cobra command for `run`.
func newRunCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <epic-id>",
		Short: "start one epic in the background and attach to it — run it again to attach or resume",
		Long: `Start one epic and attach to it: the one command for a local run.

The run starts in the background, detached from this terminal, and the live
view attaches to it — the same one ` + "`ticfac watch`" + ` renders. Ctrl-C detaches
the view; it never stops the run. Running the command again attaches to a
live run and resumes a stopped one, so the epic id (also accepted spelled as
its run id, epic-<id>) is the only thing to type: the run's internal identity
stays epic-<id>.

What the old incantation carried by hand, decided here:

  - herdr is detected — a live socket, resolved the same way the run's own
    herdr executor dials it — and a live server dispatches into herdr panes
    with the profile set embedded in this binary. No filesystem path.
    --no-herdr opts out; --profiles names another set, verbatim.
  - the GitHub token is resolved when, and only when, this repository's
    close-out rule needs a forge — GITHUB_TOKEN, then gh auth token — and
    the run says which one answered.
  - no wall clock is injected. --wall N is an opt-in per-job backstop;
    unnamed, the reconciler's own per-job bound applies.

` + "`ticfac run-epic`" + ` remains the foreground form scripts drive; everything
this command starts is that command, in the background.

With --cloud, the same command drives the factory instead of this machine:
it submits the epic to the configured factory and attaches the same live
view to the cloud run, and running it again attaches to this project's live
cloud run or resumes a finished or frozen one with a new submission. The
herdr detection, the profile set and the wall clock drive LOCAL jobs, so
they do not apply; the expert ` + "`ticfac cloud ...`" + ` commands keep the rest (a
queued submission, a budget ceiling, a hard stop).`,
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fl := defineRunFlags(fs)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		// cobra carries the context fang was executed with; a bare
		// construction (a test driving the body) has none.
		ctx := c.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return codeToErr(runCommand(ctx, args, fl, stdout, stderr))
	}
	return cmd
}

// runChild is the background start's own observable: whether the child has
// exited, its exit code once it has, and its pid. The CLAIM — runlife's
// pidfile — is the RUN's fact, and it is what the attach waits on; this is
// the STARTER's fact about the process it started, so the wait can tell "the
// run is starting" from "the run refused and left".
type runChild interface {
	Exited() bool
	ExitCode() int
	Pid() int
}

// The seams, for the same reason every probe in this package is one: the
// herdr socket, the detached spawn and the attach are the environment's and
// the process's, and a test must answer each with a controlled value. The
// production value of each is the real thing — the real socket dial, the
// real detached process, the real watch — and the one test that runs them
// all for real proves the detached child actually survives the terminal.

// runHerdrLive answers whether a live herdr server can be dialled the way
// the run itself would dial it: the socket resolved through the SAME
// configuration the herdr executor resolves (orchestration.socket,
// $HERDR_SOCKET_PATH, then herdr's default), then pinged through the real
// client. A server that does not answer is not an error of this command —
// the run then dispatches with the local profiles, and --no-herdr exists to
// skip the probe.
var runHerdrLive = func(ctx context.Context, repo string) (string, error) {
	cfg, err := runconfig.LoadRepo(repo)
	if err != nil {
		// A file that exists and fails validation is the reconciler's own
		// construction refusal waiting to happen; here it is one more way
		// "no live herdr" can be true, reported with its cause.
		return "", fmt.Errorf("the repository's %s does not validate: %w", runconfig.FileName, err)
	}
	socket, err := runconfig.ResolveSocket(cfg)
	if err != nil {
		return "", err
	}
	cl, err := client.New(ctx, client.Options{SocketPath: socket})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("herdr %s answers at %s", cl.ServerInfo().Version, socket), nil
}

// runStartDetached starts one detached child: this executable running argv,
// in its OWN SESSION (Setsid — the terminal's Ctrl-C and hangup cannot reach
// it), with its output to out. It is the seam the whole "Ctrl-C detaches,
// never stops" property rests on: the child the production value starts is
// not in the invocation's session, which is what makes detaching free.
var runStartDetached = func(argv []string, out io.Writer) (runChild, error) {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, argv...)
	cmd.Stdout, cmd.Stderr = out, out
	// A session of its own: the foreground process group a terminal's Ctrl-C
	// signals does not contain this child, and the hangup a closing terminal
	// sends finds no controlling terminal to deliver it to. That is the
	// nohup-and-more of the old incantation, by construction.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &detachedChild{pid: cmd.Process.Pid}
	go func() {
		child.recordExit(cmd.Wait())
	}()
	return child, nil
}

// detachedChild is the production runChild: the process's exit, learned by
// the only one who can — the goroutine that Wait()s it.
type detachedChild struct {
	mu     sync.Mutex
	pid    int
	exited bool
	code   int
}

func (c *detachedChild) recordExit(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exited = true
	switch {
	case err == nil:
		c.code = 0
	default:
		// A signaled child has no exit code of its own; the convention is
		// the shell's, and any code is only ever relayed, never interpreted.
		c.code = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			if code := exitErr.ExitCode(); code >= 0 {
				c.code = code
			}
		}
	}
}

func (c *detachedChild) Exited() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exited
}

func (c *detachedChild) ExitCode() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.code
}

func (c *detachedChild) Pid() int { return c.pid }

// runAttach is the live view `run` attaches with: watch's own command body,
// so the two surfaces cannot drift — attaching to a run through this
// command renders exactly what `ticfac watch <run-id>` renders, on a
// terminal and on a pipe alike. Under `run --json` the caller points the
// attach's stdout at stderr, so the live view is prose beside the one
// document the run command itself answers with.
var runAttach = func(ctx context.Context, repo, runID string, stdout, stderr io.Writer) int {
	interval := defaultWatchInterval
	repoArg := repo
	plainJSON := false
	return watchCommand(ctx, []string{runID}, &repoArg, &interval, &plainJSON, stdout, stderr)
}

// runCommand is `run`'s body.
func runCommand(ctx context.Context, args []string, fl *runFlags, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac run: exactly one epic id is required\n")
		return 2
	}
	// The epic id everywhere the run id was: status, events and the feed all
	// say epic-<id>, and an operator who types what they read is right. The
	// run's internal identity stays epic-<epic-id> either way — run-epic
	// derives it from the epic id it is given, so the prefix is stripped
	// here and never doubled.
	epicID := strings.TrimPrefix(rest[0], "epic-")
	if epicID == "" {
		fmt.Fprintf(stderr, "ticfac run: %q names no epic\n", rest[0])
		return 2
	}
	// Prose and document, kept apart (tick 8v3): with --json the command's
	// own lines and the attached live view's go to stderr, and stdout
	// carries exactly one document at the end. Without it, everything is
	// stdout's as it always was.
	prose := stdout
	if *fl.asJSON {
		prose = stderr
	}
	// finish emits the one document and returns the code the state names —
	// every --json path below ends through here, so the document and the
	// exit code cannot disagree.
	finish := func(action, state, note string) int {
		if *fl.asJSON {
			if err := emitRunJSON(action, state, note, epicID, "epic-"+epicID, fl, stdout); err != nil {
				fmt.Fprintf(stderr, "ticfac run %s: %v\n", epicID, err)
				return 1
			}
		}
		return stateExitClass(state)
	}
	repo := *fl.repo
	if repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac run: cannot read the working directory: %v\n", err)
			return 1
		}
		repo = wd
	}
	if abs, err := filepath.Abs(repo); err == nil {
		// The child is started with an explicit --repo either way; absolute
		// means the recorded argv is a stable fact rather than a cwd-shaped
		// one, wherever the child was started from.
		repo = abs
	}

	// Cloud parity (tick ejw): the same command, the same verbs, against the
	// configured factory. The decision is made before any local fact is
	// read — a cloud run has no local life to probe — and the local-only
	// flags are refused rather than ignored, because a flag that silently
	// does nothing is a lie an everyday command must not tell.
	if *fl.cloud {
		if *fl.noHerdr || *fl.profiles != "" || *fl.wall > 0 {
			fmt.Fprintf(stderr, "ticfac run: --no-herdr, --profiles and --wall drive a local run's jobs "+
				"and do not apply to a --cloud run; the expert `ticfac cloud run` keeps its own budget and "+
				"queue flags\n")
			return 2
		}
	}
	if parseOnly {
		return 0
	}
	if *fl.cloud {
		return runCloudCommand(ctx, epicID, repo, fl, stdout, stderr)
	}

	runID := "epic-" + epicID

	// The liveness that decides attach from resume is the run's own claim,
	// never a guess: runlife's probe, the same answer `ticfac status` gives.
	probe := runlife.Probe(repo, runID, time.Now())
	if probe.State == runlife.Alive {
		fmt.Fprintf(prose, "run %s is alive (%s) — attaching; Ctrl-C detaches without stopping it\n",
			runID, probe.Reason)
		code := attachRun(ctx, epicID, repo, runID, prose, stderr)
		return finish("attached", runAttachState(repo, runID, code), "")
	}

	// Not running here — it never started, it released on its way out, or it
	// died without saying so. All three are one action: start the run in the
	// background; the reconciler's own boot path resumes a run that has
	// state to resume (tick lkd) and starts one that does not.
	action := "starting"
	if runRanBefore(repo, runID) {
		action = "resuming"
	}
	fmt.Fprintf(prose, "run %s is not running here (%s) — %s it in the background\n",
		runID, probe.Reason, action)

	// The profile set: the herdr decision, or the operator's word over it.
	// The PROBE is skipped whenever its answer cannot change what is
	// dispatched — an override names the set, --no-herdr declines the panes.
	profileDir := ""
	switch {
	case *fl.profiles != "":
		profileDir = *fl.profiles
		fmt.Fprintf(prose, "profiles: %s (named on the command line)\n", profileDir)
	case *fl.noHerdr:
		fmt.Fprintf(prose, "herdr skipped (--no-herdr): dispatching with the profiles embedded as this binary's default\n")
	default:
		detail, err := runHerdrLive(ctx, repo)
		if err != nil {
			fmt.Fprintf(prose, "no live herdr (%v) — dispatching with the profiles embedded as this binary's default; "+
				"--no-herdr skips this probe\n", err)
		} else {
			profileDir = profile.EmbeddedHerdr
			fmt.Fprintf(prose, "%s — dispatching into herdr panes with the profile set embedded in this binary\n", detail)
		}
	}

	// The detached start: this executable, run-epic, the flags decided above
	// and nothing else. The header line in start.log is the starter's own
	// account of what it started and when — run.log belongs to the run, and
	// begins at its claim.
	dir := runlife.Dir(repo, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "ticfac run %s: the run's log directory could not be created: %v\n", epicID, err)
		return finish(action, agentStateFailed, "the run's log directory could not be created")
	}
	argv := []string{"run-epic", epicID, "--repo", repo}
	if profileDir != "" {
		argv = append(argv, "--profiles", profileDir)
	}
	if *fl.wall > 0 {
		argv = append(argv, "--wall", strconv.Itoa(*fl.wall))
	}
	logPath := filepath.Join(dir, startLogName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s: %s could not be opened: %v\n", epicID, logPath, err)
		return finish(action, agentStateFailed, "the run's start log could not be opened")
	}
	fmt.Fprintf(logFile, "ticfac run started this run detached at %s: %s\n",
		time.Now().UTC().Format(time.RFC3339), strings.Join(argv, " "))
	child, err := runStartDetached(argv, logFile)
	logFile.Close()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run %s: the background run could not be started: %v\n", epicID, err)
		return finish(action, agentStateFailed, "the background run could not be started")
	}
	fmt.Fprintf(prose, "run %s starting in the background (pid %d; its first words are in %s)\n",
		runID, child.Pid(), logPath)

	// The attach waits on the CLAIM, never on a guess about the child: the
	// run's own pidfile, checked until the run claims its life, the child
	// exits without one, or the bound runs out. A child that refuses (no
	// executor, an unusable tracker) exits fast and its own words are
	// relayed, with the code it exited by.
	deadline := time.Now().Add(runClaimWait)
	for {
		if p := runlife.Probe(repo, runID, time.Now()); p.State == runlife.Alive {
			fmt.Fprintf(prose, "run %s claimed its life (pid %d) — attaching; Ctrl-C detaches without stopping it\n",
				runID, p.Record.PID)
			code := attachRun(ctx, epicID, repo, runID, prose, stderr)
			return finish(action, runAttachState(repo, runID, code), "")
		}
		if child.Exited() {
			fmt.Fprintf(stderr, "ticfac run %s: the background run exited %d without claiming the run — what it said:\n",
				epicID, child.ExitCode())
			if raw, readErr := os.ReadFile(logPath); readErr == nil {
				fmt.Fprintf(stderr, "%s", raw)
			} else {
				fmt.Fprintf(stderr, "(its log %s could not be read: %v)\n", logPath, readErr)
			}
			code := child.ExitCode()
			if code <= 0 {
				code = 1
			}
			// The child's refusal is the run's first word relayed — prose on
			// stderr — and the document's note says the shape of it without
			// pasting the whole log into a field a script cannot use.
			if *fl.asJSON {
				if err := emitRunJSON(action, agentStateFailed,
					fmt.Sprintf("the background run exited %d without claiming the run; its words are on stderr and in %s", code, logPath),
					epicID, runID, fl, stdout); err != nil {
					fmt.Fprintf(stderr, "ticfac run %s: %v\n", epicID, err)
				}
			}
			return code
		}
		if ctx.Err() != nil {
			// The operator left before the run said its first word. The
			// child keeps starting — that is the point of a detached start —
			// and the honest answer is the table's running class (5): the run
			// is in flight, stdout's document says so, and the line below
			// names where to ask about it.
			fmt.Fprintf(prose, "detached before run %s claimed its life — it is still starting; "+
				"`ticfac status %s` asks whether it is alive\n", runID, runID)
			return finish(action, agentStateRunning, "detached before the run claimed its life; the background start continues")
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(stderr, "ticfac run %s: the background run did not claim its life within %s — it may still be "+
				"starting; `ticfac status %s` asks whether it is alive, and its first words are in %s\n",
				epicID, runClaimWait, runID, logPath)
			return finish(action, agentStateFailed, fmt.Sprintf("the background run did not claim its life within %s", runClaimWait))
		}
		time.Sleep(runClaimPoll)
	}
}

// runAttachState maps the attached watch's exit code to the state word the
// document and the exit class share — the same mapping the table defines,
// so `ticfac run` and `ticfac watch` cannot answer the same ending with
// different words. A cancelled end (tick rix) keeps its own word and code:
// the run was stopped deliberately, neither done nor failed.
func runAttachState(repo, runID string, code int) string {
	switch code {
	case exitSuccess:
		return agentStateDone
	case ExitHeld:
		return agentStateHeld
	case exitRunning:
		return agentStateRunning
	case exitCancelled:
		return agentStateCancelled
	}
	return agentStateFailed
}

// runJSON is `run --json`'s answer, ticfac.run.v1: what the command did —
// attached, started, resumed — and how that ended, in the exit table's
// state words. The note carries what prose says in one line: where the
// child's refusal is, what the detach left running.
type runJSON struct {
	agentDoc
	EpicID   string `json:"epic_id"`
	RunID    string `json:"run_id"`
	Action   string `json:"action"`
	Profiles string `json:"profiles,omitempty"`
	Note     string `json:"note,omitempty"`
}

func emitRunJSON(action, state, note, epicID, runID string, fl *runFlags, stdout io.Writer) error {
	return emitAgentJSON(stdout, runJSON{
		agentDoc: agentDoc{Schema: agentSchemaID("run"), State: state},
		EpicID:   epicID,
		RunID:    runID,
		Action:   action,
		Profiles: *fl.profiles,
		Note:     note,
	})
}

// attach runs the live view and says what detaching means. The interrupted
// codes are the running class (tick 8v3): an attach that ended because the
// caller left — while the run keeps going — exits 5, not 0, so an agent
// waiting on `ticfac run` never reads a live epic as a finished one; the
// line that says so names the one command that comes back.
func attachRun(ctx context.Context, epicID, repo, runID string, stdout, stderr io.Writer) int {
	code := runAttach(ctx, repo, runID, stdout, stderr)
	if code == exitRunning {
		fmt.Fprintf(stdout, "detached from run %s — the run keeps going in the background; "+
			"`ticfac run %s` attaches again, `ticfac status %s` asks whether it is alive\n", runID, epicID, runID)
		return exitRunning
	}
	if ctx.Err() != nil && code == 1 {
		// A seam's answer or an interrupted watch that never learned the
		// run's own claim: the probe decides, and a live run is the running
		// class here too, never a silent 0.
		if probe := runlife.Probe(repo, runID, time.Now()); probe.State == runlife.Alive {
			fmt.Fprintf(stdout, "detached from run %s — the run keeps going in the background; "+
				"`ticfac run %s` attaches again, `ticfac status %s` asks whether it is alive\n", runID, epicID, runID)
			return exitRunning
		}
	}
	return code
}

// runRanBefore answers whether any earlier incarnation of this run left
// anything durable behind — its run records or its logs — so the start line
// says "resuming" for a run that has run before and "starting" for one that
// never has. Wording only: the reconciler decides what a resume actually is,
// from the state on the integration branch.
func runRanBefore(repo, runID string) bool {
	if _, err := os.Stat(filepath.Join(repo, runstate.Root, "runs", runID)); err == nil {
		return true
	}
	_, err := os.Stat(runlife.Dir(repo, runID))
	return err == nil
}
