package cli

// `ticfac herd` — the herdr operator surfaces ticfac owns: the pane badges
// and the blocked/settled chimes tk herd paint and tk herd notify provided
// before ticks' pwp deleted the herd surface from tk (tick glb).
//
// What is deliberately NOT here, and the consequence of each — this is the
// settled answer the ticks-side plugin tick (si0) acts on:
//
//   tk herd guard    DROPPED. The guard judged an AGENT-run orchestrator pane
//                    (nudge an idle orchestrator whose frontier is actionable,
//                    chime when it blocks) — an orchestrator that could stall
//                    on its harness's approval UI. ticfac's orchestrator is a
//                    deterministic program, not an agent in a pane: its
//                    liveness is answered by `ticfac status` (a pidfile plus
//                    process start time), and its human-attention surface is
//                    `ticfac watch`, which says — once, to a human — when a
//                    run stops holding something for one. There is nothing to
//                    nudge: no prompt, no frontier, no approval UI. A
//                    hand-run agent orchestrator inside a herdr pane has no
//                    watchdog, and that is the honest consequence of the
//                    architecture, not a gap to paper over.
//   tk herd plugin   DROPPED. It reported on and installed TICKS' own
//                    herdr-ticks plugin (pengelbrecht/ticks/plugins/
//                    herdr-ticks) and checked it could fire the guard hook —
//                    which is dropped above. The plugin is ticks' to ship or
//                    retire (tick si0); ticfac does not carry it, install it,
//                    or vouch for its hooks. Consequence: the install path is
//                    herdr's own `herdr plugin install`, and the plugin's
//                    hooks call commands that no longer exist until si0
//                    settles them (paint and notify may now be pointed at
//                    `ticfac herd paint` and `ticfac herd notify`).

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/herdr"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/herd/client"
	"github.com/pengelbrecht/ticfac/internal/herd/notify"
	"github.com/pengelbrecht/ticfac/internal/herd/paint"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// defaultHerdStateRoot is the executor state root a herdr run records under
// when the operator pinned none — the same default `ticfac run-epic` uses, so
// a command run beside a run with no --state-root sees that run's attempts.
func defaultHerdStateRoot() string {
	return filepath.Join(subprocess.DefaultStateDir(), "runs")
}

// herdCommand is the `herd` group's dispatcher.
func herdCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, herdUsage)
		return exitUsage
	}
	name, rest := args[0], args[1:]
	if len(rest) > 0 && (rest[0] == "--help" || rest[0] == "-h") {
		fmt.Fprint(stdout, herdUsage)
		return exitSuccess
	}
	switch name {
	case "paint":
		return herdPaintCommand(ctx, rest, stdout, stderr)
	case "notify":
		return herdNotifyCommand(ctx, rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, herdUsage)
		return exitSuccess
	default:
		fmt.Fprintf(stderr, "ticfac herd: unknown subcommand %q\n\n%s", name, herdUsage)
		return exitUsage
	}
}

// herdUsage is the group's help: the two commands this binary owns, and the
// two tk herd surfaces deliberately dropped, with the consequence of each —
// the decision record the ticks-side plugin tick reads.
const herdUsage = `ticfac herd — the herdr operator surfaces ticfac owns

usage:
  ticfac herd paint [flags]    badge this run's herdr workspaces with the tick each worker is on
  ticfac herd notify [flags]   chime when a worker blocks or a wave settles

paint flags:
  --run <id>        the run to paint (default: every run with herdr attempts)
  --repo <dir>      the checkout the run works in, for tick statuses (default: cwd)
  --state-root <dir> where the executor recorded attempts (default: ticfac's own)
  --socket <path>   herdr socket path (default: $HERDR_SOCKET_PATH, then herdr's default)
  --ttl <ms>        how long a painted badge survives without a repaint (default 90000)
  --seq <n>         sequence number ordering this paint against earlier ones (0 omits it)
  --dry-run         build and print the badges without reporting anything to herdr
  --json            print one JSON document instead of the human-readable report

notify flags:
  --run <id>        the run to judge (default: every run with herdr attempts, judged separately)
  --repo <dir>      as for paint (unused until statuses are painted into the chime)
  --state-root <dir> as for paint; the once-semantics state lives beside the run's attempts
  --socket <path>   as for paint
  --dry-run         decide and print without notifying herdr and without consuming the once-semantics state
  --json            print one JSON document instead of the human-readable report

Both are display-only: paint never renames a pane, moves focus or touches an
agent, and notify never answers an approval for you. A badge expires by itself
(the TTL), so a painter that stops running leaves nothing stale behind — which
is what makes these safe to run from an event hook on every status change.

Deliberately not here (the settled answer for the ticks-side plugin, tick si0):

  guard   tk's orchestrator watchdog judged an AGENT-run orchestrator pane:
          nudge it idle with an actionable frontier, chime when it blocks.
          ticfac's orchestrator is a program, not an agent in a pane — its
          liveness is 'ticfac status' and its attention surface 'ticfac
          watch'. Consequence: a hand-run agent orchestrator in a herdr
          pane has no watchdog.
  plugin  tk installed and health-checked ticks' own herdr-ticks plugin.
          The plugin is ticks' to settle (tick si0); the hooks that called
          tk herd paint/notify/guard may be pointed at 'ticfac herd
          paint' and 'ticfac herd notify' here. Consequence: 'herdr
          plugin install' remains the install path, and a stale plugin's
          hooks fail until si0 retires or repoints them.
`

// herdScope is the resolved input half of both commands: which runs, which
// attempts, and where the run's recorded tick statuses are read from.
type herdScope struct {
	RunID  string
	Runs   []string
	Root   string
	Repo   string
	Socket string
	DryRun bool
	AsJSON bool
}

// herdFlags is the flag set the two commands share.
func herdFlags(name string, stderr io.Writer) (*flag.FlagSet, *herdScope) {
	fs := newFlagSet(name, stderr)
	scope := &herdScope{}
	fs.StringVar(&scope.RunID, "run", "", "the run to scope to (default: every run with herdr attempts)")
	fs.StringVar(&scope.Repo, "repo", "", "the checkout the run works in (default: cwd)")
	fs.StringVar(&scope.Root, "state-root", "", "where the executor recorded attempts (default: ticfac's own)")
	fs.StringVar(&scope.Socket, "socket", "", "herdr socket path (default: $HERDR_SOCKET_PATH, then herdr's default)")
	fs.BoolVar(&scope.DryRun, "dry-run", false, "decide and print without reporting to herdr")
	fs.BoolVar(&scope.AsJSON, "json", false, "print one JSON document")
	return fs, scope
}

// resolve fills the defaults a scope's flags left empty: the repo from cwd,
// the state root from ticfac's own, and the run list from the state root —
// every run with attempts when no --run narrowed it.
func (s *herdScope) resolve() error {
	if s.Repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return newExitError(exitGeneric, "cannot read the working directory: %v", err)
		}
		s.Repo = wd
	}
	if s.Root == "" {
		s.Root = defaultHerdStateRoot()
	}
	if s.RunID != "" {
		s.Runs = []string{s.RunID}
		return nil
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			s.Runs = nil
			return nil
		}
		return newExitError(exitGeneric, "reading %s: %v", s.Root, err)
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			s.Runs = append(s.Runs, e.Name())
		}
	}
	sort.Strings(s.Runs)
	return nil
}

// dial connects to herdr. The socket is the flag's, else the client's own
// resolution ($HERDR_SOCKET_PATH, then herdr's default) — the same order tk's
// herd commands used.
func (s *herdScope) dial(ctx context.Context, stderr io.Writer) (*client.Client, error) {
	c, err := client.New(ctx, client.Options{SocketPath: s.Socket, ProtocolWarning: stderr})
	if err != nil {
		return nil, newExitError(exitGeneric, "herdr is not reachable: %v", err)
	}
	// No close: the client's protocol is one-request-per-connection, so
	// the calls above and below have already opened and shut everything.
	return c, nil
}

// tickStatuses reads the run's recorded tick states out of the run
// checkpoint — the one file that states what the run believes each tick is.
// A checkpoint that cannot be read paints every badge "unknown" rather than
// failing: a painter runs from event hooks and must not cost the operator
// the wave's badges over a repo-local read.
func tickStatuses(repo, runID string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(filepath.Join(repo, runstate.CheckpointPath(runID)))
	if err != nil {
		return out
	}
	var checkpoint runstate.Checkpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return out
	}
	for _, ts := range checkpoint.Ticks {
		out[ts.TickID] = string(ts.State)
	}
	return out
}

// herdPaintCommand is `ticfac herd paint`.
func herdPaintCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := herdPaint(ctx, args, stdout, stderr)
	return reportCommand("herd paint", err, stderr)
}

func herdPaint(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, scope := herdFlags("herd paint", stderr)
	ttlMs := fs.Int64("ttl", 90000, "how long a painted badge survives without a repaint, in milliseconds")
	seq := fs.Uint64("seq", 0, "sequence number ordering this paint against earlier ones (0 omits it)")
	rest, err := parseCollectingPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(rest) != 0 {
		return newExitError(exitUsage, "herd paint takes no positional arguments")
	}
	if *ttlMs <= 0 {
		return newExitError(exitUsage, "--ttl must be a positive number of milliseconds")
	}
	if err := scope.resolve(); err != nil {
		return err
	}
	if len(scope.Runs) == 0 {
		if scope.AsJSON {
			return writeJSON(stdout, map[string]any{"badges": []paint.Badge{}, "runs": []string{}})
		}
		fmt.Fprintln(stdout, "no herdr attempts recorded — nothing to paint")
		return nil
	}

	herd, err := scope.dial(ctx, stderr)
	if err != nil {
		return err
	}

	var combined paint.Result
	combined.Badges = []paint.Badge{}
	combined.Source = client.SourceHerdPaint
	combined.TTLMs = uint64(*ttlMs)
	combined.DryRun = scope.DryRun

	labels := tickLabels(ctx, scope.Repo)
	for _, runID := range scope.Runs {
		attempts, err := herdr.Attempts(scope.Root, runID)
		if err != nil {
			return newExitError(exitGeneric, "%v", err)
		}
		if len(attempts) == 0 {
			continue
		}
		res, err := paint.Run(ctx, herd, paint.Options{
			Attempts: attemptsToPaint(attempts, labels),
			Statuses: tickStatuses(scope.Repo, runID),
			TTL:      time.Duration(*ttlMs) * time.Millisecond,
			Seq:      *seq,
			DryRun:   scope.DryRun,
		})
		if err != nil {
			return newExitError(exitGeneric, "%v", err)
		}
		combined.Badges = append(combined.Badges, res.Badges...)
		combined.Painted += res.Painted
		combined.Skipped += res.Skipped
	}

	if scope.AsJSON {
		return writeJSON(stdout, combined)
	}
	herdPaintPrint(stdout, combined)
	return nil
}

// attemptsToPaint converts the executor's attempt facts into paint's worker
// records, each carrying its tick's label from labels when there is one.
func attemptsToPaint(attempts []herdr.AttemptFacts, labels map[string]string) []paint.Attempt {
	out := make([]paint.Attempt, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, paint.Attempt{
			Tick: a.TickID, Epic: a.EpicID, Role: a.Role, Label: labels[a.TickID],
			WorkspaceID: a.WorkspaceID, PaneID: a.PaneID,
		})
	}
	return out
}

// herdPaintPrint renders the badges as evidence: what was painted, on what,
// and why anything was not. Ported from tk's herd_paint.go print half.
func herdPaintPrint(out io.Writer, res paint.Result) {
	verb := "painted"
	if res.DryRun {
		verb = "would paint"
	}
	for _, b := range res.Badges {
		fmt.Fprintf(out, "tick      %s  %s\n", b.Tick, b.Title)
		if b.Skipped != "" {
			fmt.Fprintf(out, "skipped   %s\n", b.Skipped)
			continue
		}
		fmt.Fprintf(out, "workspace %s\n", b.WorkspaceID)
		if b.PaneID != "" {
			fmt.Fprintf(out, "pane      %s  (agent %s)\n", b.PaneID, b.AgentStatus)
		} else {
			fmt.Fprintln(out, "pane      none in this workspace — tokens only")
		}
		fmt.Fprintf(out, "tokens    %s\n", herdPaintTokens(b.Tokens))
		for status, label := range b.StateLabels {
			fmt.Fprintf(out, "label     %s = %s\n", status, label)
		}
	}
	if len(res.Badges) == 0 {
		fmt.Fprintln(out, "no herdr attempts — nothing to paint")
		return
	}
	fmt.Fprintf(out, "\n%d %s, %d skipped (source %s, ttl %dms)\n",
		res.Painted, verb, res.Skipped, res.Source, res.TTLMs)
}

// herdPaintTokens renders the token map in a stable order.
func herdPaintTokens(tokens map[string]string) string {
	var parts []string
	for _, name := range []string{paint.TokenTick, paint.TokenRole, paint.TokenEpic, paint.TokenStatus} {
		if v := tokens[name]; v != "" {
			parts = append(parts, name+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

// herdNotifyCommand is `ticfac herd notify`.
func herdNotifyCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := herdNotify(ctx, args, stdout, stderr)
	return reportCommand("herd notify", err, stderr)
}

func herdNotify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, scope := herdFlags("herd notify", stderr)
	rest, err := parseCollectingPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(rest) != 0 {
		return newExitError(exitUsage, "herd notify takes no positional arguments")
	}
	if err := scope.resolve(); err != nil {
		return err
	}
	if len(scope.Runs) == 0 {
		if scope.AsJSON {
			return writeJSON(stdout, []notify.Result{})
		}
		fmt.Fprintln(stdout, "no herdr attempts recorded — nothing to notify about")
		return nil
	}

	herd, err := scope.dial(ctx, stderr)
	if err != nil {
		return err
	}

	var results []notify.Result
	for _, runID := range scope.Runs {
		attempts, err := herdr.Attempts(scope.Root, runID)
		if err != nil {
			return newExitError(exitGeneric, "%v", err)
		}
		if len(attempts) == 0 {
			continue
		}
		res, err := notify.Run(ctx, herd, notify.Options{
			RunID:     runID,
			Workers:   attemptsToWorkers(attempts),
			StateRoot: scope.Root,
			DryRun:    scope.DryRun,
		})
		if err != nil {
			return newExitError(exitGeneric, "%v", err)
		}
		results = append(results, res)
	}

	if scope.AsJSON {
		if results == nil {
			results = []notify.Result{}
		}
		return writeJSON(stdout, results)
	}
	herdNotifyPrint(stdout, results)
	return nil
}

// attemptsToWorkers converts the executor's attempt facts into notify's
// worker records. The pane id is absent on a record written before the spawn
// confirmed — the agent name is the fallback join key.
func attemptsToWorkers(attempts []herdr.AttemptFacts) []notify.Worker {
	out := make([]notify.Worker, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, notify.Worker{
			Tick: a.TickID, Epic: a.EpicID, Role: a.Role,
			Agent: a.AgentName, PaneID: a.PaneID,
		})
	}
	return out
}

// herdNotifyPrint renders what was decided per run: what was said, what was
// held back and why, and the wave's shape.
func herdNotifyPrint(out io.Writer, results []notify.Result) {
	saidAnything := false
	for _, res := range results {
		if len(res.Workers) == 0 {
			continue
		}
		for _, n := range res.Notifications {
			saidAnything = true
			fmt.Fprintf(out, "notified  %s\n", n.Title)
			if n.Body != "" {
				fmt.Fprintf(out, "          %s\n", n.Body)
			}
			if n.Sent && !n.Shown {
				fmt.Fprintf(out, "          herdr declined to show it (%s)\n", n.Reason)
			}
		}
		for _, s := range res.Suppressed {
			saidAnything = true
			fmt.Fprintf(out, "held      %s %s: %s\n", res.RunID, s.Kind, s.Reason)
		}
		fmt.Fprintf(out, "run %s: %d in flight, %d settled, wave complete %v\n",
			res.RunID, res.InFlight, res.Settled, res.WaveComplete)
	}
	if !saidAnything {
		fmt.Fprintln(out, "no herdr attempts — nothing to notify about")
	}
}

// writeJSON prints one document, single-line, the way tk's writeJSONLine did:
// a hook consumer reads exactly one parseable line.
func writeJSON(out io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return newExitError(exitGeneric, "encode the report: %v", err)
	}
	_, err = fmt.Fprintf(out, "%s\n", raw)
	return err
}
