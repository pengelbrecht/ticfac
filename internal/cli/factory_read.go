package cli

// `ticfac factory` — the read half of the factory command group: status and
// the dashboard. Ported from ticks' cmd/tk/cmd/factory.go (its status command
// only — deploy and setup are Factory move B, ticks tick b3a) and
// cmd/tk/cmd/factory_dashboard.go, with the cobra plumbing replaced by this
// package's plain flag sets and every body otherwise verbatim.
//
// The group's help still describes deploy and setup, because that is the
// surface the finished move serves; until b3a lands they answer here with a
// refusal that says so, and b3a's wiring replaces the two cases.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/factory/dashboard"
	"github.com/pengelbrecht/ticfac/internal/gatewaytrace"
)

// factoryCommand is the `factory` group's dispatcher: args[0] names the
// subcommand, the rest are its flags and operands.
func factoryCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, factoryUsage)
		return exitUsage
	}
	name, rest := args[0], args[1:]
	if len(rest) > 0 && (rest[0] == "--help" || rest[0] == "-h") {
		fmt.Fprint(stdout, factoryUsage)
		return exitSuccess
	}
	switch name {
	case "status":
		return runFactoryStatus(ctx, rest, stdout, stderr)
	case "dashboard":
		return runFactoryDashboard(ctx, rest, stdout, stderr)
	case "deploy":
		return factoryDeploy(rest, stdout, stderr)
	case "setup":
		return factorySetup(rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, factoryUsage)
		return exitSuccess
	default:
		fmt.Fprintf(stderr, "ticfac factory: unknown subcommand %q\n\n%s", name, factoryUsage)
		return exitUsage
	}
}

// factoryUsage is the group's help: the four commands the finished factory
// surface serves, and the posture that keeps the operator-to-orchestrator
// vocabulary closed.
const factoryUsage = `ticfac factory — manage your personal cloud factory

The factory is a deployable, not a service: it runs in your own Cloudflare
account, on your compute, with your model keys. See ticks' docs/design/cloud-factory.md.

  deploy  put it in your account   |  status     what is configured, and works
  setup   walk the credentials     |  dashboard  watch it run, read-only

'ticfac factory dashboard' is observation, like 'ticfac cloud status/logs/trace':
it watches a deployed factory from a local terminal and cannot steer one, so
the operator-to-orchestrator command vocabulary stays run/stop/status/answer.

status flags:
  --offline                 skip the live credential checks
  --check                   exit nonzero when a configured credential is rejected
  --github-api-base <url>   override the GitHub API root (testing)
  --cloudflare-api-base <url>   override the Cloudflare API root (testing)

dashboard is the live read-only board of a deployed factory: the runs it is
executing, the phase and boot each one is on, what its container is printing
right now, the gates waiting for an answer, and the submissions it refused and
why. Read-only, and observability rather than authority: it takes no actions,
and every request it makes is a GET. It works when the factory does not — a
read that fails keeps the last frame and labels it STALE with its age and what
went wrong.

dashboard keys
  j / k   move selection / scroll output   g / G   first / last
  enter   open run detail, or fold a row   esc     close detail, or quit
  r       reload now                       q       quit

dashboard flags:
  --project <owner/repo>    project to watch (default: every project with runs)
  --interval <ms>           how often to re-read the factory
  --cost-interval <ms>      how often to re-total AI Gateway spend
  --tail-bytes <n>          how much of the harness output tail each frame reads
  --no-cost                 skip gateway cost telemetry entirely
`

func runFactoryStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := factoryStatus(ctx, args, stdout)
	return reportCommand("factory status", err, stderr)
}

func factoryStatus(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("factory status", nil)
	offline := fs.Bool("offline", false, "skip the live credential checks")
	check := fs.Bool("check", false, "exit nonzero when a configured credential is rejected")
	githubAPI := fs.String("github-api-base", "", "override the GitHub API root (testing)")
	cfAPIBase := fs.String("cloudflare-api-base", "", "override the Cloudflare API root (testing)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(fs.Args()) != 0 {
		return newExitError(exitUsage, "factory status takes no positional arguments")
	}

	report, err := factory.Status(ctx, factory.StatusOptions{
		Offline:           *offline,
		GitHubAPIBase:     *githubAPI,
		CloudflareAPIBase: *cfAPIBase,
		CurrentVersion:    Version,
	})
	if err != nil {
		return newExitError(exitIO, "%v", err)
	}
	report.Write(stdout)

	if failures := report.Failures(); len(failures) > 0 && *check {
		return newExitError(exitGeneric, "credential rejected for: %s", strings.Join(failures, ", "))
	}
	return nil
}

// factoryPinColorProfile is a deliberate COPY of ticks' internal/tui
// PinColorProfile, not an import (see ticks' cmd/tk/cmd/factory_dashboard.go
// for the 5yk reasoning this follows: the board and the tracker TUI are two
// products that may diverge in look, and a shared package is a coupling
// neither wants).
func factoryPinColorProfile(p termenv.Profile) {
	lipgloss.SetColorProfile(p)
}

// defaultFactoryDashboardIntervalMs is how often the board re-reads the
// factory. It is the update mechanism here, unlike the herd board where
// events are: a deployed factory pushes its `run_event` stream to a BOARD, and
// what a local terminal can read is the tail the RunRoom keeps.
const defaultFactoryDashboardIntervalMs = int64(2 * 1000)

// defaultFactoryDashboardCostMs is how often gateway telemetry is re-totalled.
// Each pass pages the AI Gateway logs API, so it is deliberately far slower
// than a frame.
const defaultFactoryDashboardCostMs = int64(30 * 1000)

// defaultFactoryDashboardTailBytes is how much of a run's harness output tail
// `--tail-bytes` reads: a screenful of scrollback, not a log dump. The board
// polls it every couple of seconds, and the operator who wants the whole
// stream has `ticfac cloud logs`.
//
// It lives here, with the flag it is the default for and beside the other two
// defaults for this same command, rather than in internal/factory/dashboard.
// internal/factory/dashboard keeps its own fallback for the different question
// of what a Loader does when a programmatic caller leaves HarnessBytes zero.
const defaultFactoryDashboardTailBytes = 64 << 10

func runFactoryDashboard(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := factoryDashboard(ctx, args, stdout, stderr)
	return reportCommand("factory dashboard", err, stderr)
}

func factoryDashboard(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("factory dashboard", stderr)
	project := fs.String("project", "", "project to watch as owner/repo (default: every project with runs)")
	interval := fs.Int64("interval", defaultFactoryDashboardIntervalMs, "how often to re-read the factory, in milliseconds")
	costInterval := fs.Int64("cost-interval", defaultFactoryDashboardCostMs, "how often to re-total AI Gateway spend, in milliseconds")
	tailBytes := fs.Int("tail-bytes", defaultFactoryDashboardTailBytes, "how much of the harness output tail each frame reads")
	noCost := fs.Bool("no-cost", false, "skip gateway cost telemetry entirely")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return newExitError(exitUsage, "%v", err)
	}
	if len(fs.Args()) != 0 {
		return newExitError(exitUsage, "factory dashboard takes no positional arguments")
	}

	if *interval <= 0 {
		return newExitError(exitUsage, "--interval must be a positive number of milliseconds")
	}
	if *costInterval <= 0 {
		return newExitError(exitUsage, "--cost-interval must be a positive number of milliseconds")
	}
	if *tailBytes <= 0 {
		return newExitError(exitUsage, "--tail-bytes must be a positive number of bytes")
	}

	config, err := factory.LoadCredentials()
	if err != nil {
		return newExitError(exitGeneric, "cannot read factory configuration: %v", err)
	}
	client, err := dashboard.NewClient(
		config.Get(credentials.KeyURL),
		config.Get(credentials.KeyToken),
		&http.Client{Timeout: 15 * time.Second},
	)
	if err != nil {
		return newExitError(exitGeneric, "%v", err)
	}

	// A factory without a log-reading token is a normal, documented state:
	// cost telemetry is optional. The board says so in its header rather than
	// refusing to start, because every other thing it shows still works —
	// and a silently missing cost column reads as a free run.
	var costs dashboard.CostSource
	var notes []string
	if !*noCost {
		traceConfig, err := gatewaytrace.ConfigFrom(config)
		if err != nil {
			notes = append(notes, "cost telemetry unavailable: "+err.Error())
		} else {
			costs = dashboard.GatewayCost{Client: gatewaytrace.New(traceConfig, nil)}
		}
	}

	// Pin the colour profile the way `tk tui` and `tk herd dashboard` do:
	// terminals that misreport under a multiplexer otherwise lose all colour.
	factoryPinColorProfile(termenv.TrueColor)

	var cache dashboard.Cache
	if path, err := dashboard.CachePath(client.Endpoint()); err == nil {
		cache = dashboard.Cache{Path: path}
	}

	model := dashboard.New(ctx, dashboard.Config{
		Source:          client,
		Costs:           costs,
		Project:         strings.TrimSpace(*project),
		Endpoint:        client.Endpoint(),
		RefreshInterval: time.Duration(*interval) * time.Millisecond,
		CostInterval:    time.Duration(*costInterval) * time.Millisecond,
		HarnessBytes:    *tailBytes,
		Cache:           cache,
		Notes:           notes,
	})
	defer model.Close()

	program := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithContext(ctx),
		tea.WithInput(os.Stdin),
		tea.WithOutput(stdout))
	if _, err := program.Run(); err != nil {
		return newExitError(exitGeneric, "factory dashboard: %v", err)
	}
	return nil
}
