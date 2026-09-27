package cli

// `ticfac doctor` (tick o0d): say what a run of this repository needs, and
// for each thing missing, the fix.
//
// init makes the REPOSITORY ready; doctor answers the other half — the
// machine a run starts on. A run needs tk (the tracker binary the run reads
// and writes through), a git identity (commits made by nobody are commits
// nobody can attribute), a herdr server when the substrate would dispatch
// through panes, and a GitHub credential when the close-out rule init writes
// holds on a PR. A cloud run needs more: docker (the sandbox image builds
// from one), wrangler, and a configured factory.
//
// Each check is one line, ok or missing, and a missing line carries its fix —
// the command that clears it — because a doctor that names a problem without
// its remedy sends a person to the README, which is the convoluted path this
// epic exists to remove.
//
// The checks are SEAMS (package vars) for the same reason `newTracker` is:
// the probes this command runs are the environment's, and a test must be
// able to answer each with a controlled value — a test that reported the
// host's real docker once and its absence another time is not a test. The
// production value of each seam is the real probe; herdr keeps the real
// client below its seam so its resolution order ($HERDR_SOCKET_PATH, then
// herdr's default) is the one the run itself uses.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac/internal/factory"
	"github.com/pengelbrecht/ticfac/internal/forge"
	herdclient "github.com/pengelbrecht/ticfac/internal/herd/client"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// doctorFlags is `doctor`'s flag surface.
type doctorFlags struct {
	repo  *string
	cloud *bool
}

func defineDoctorFlags(fs *flag.FlagSet) *doctorFlags {
	return &doctorFlags{
		repo:  fs.String("repo", "", "the repository a run is checked for (default: cwd)"),
		cloud: fs.Bool("cloud", false, "check the cloud prerequisites too, whatever the repository declares"),
	}
}

// newDoctorCommand builds the cobra command for `doctor`.
func newDoctorCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "say what a run of this repo still needs, each missing thing with its fix",
		Long: `Check what a run of this repository needs on the machine it starts on.

Every check is one line, ok or missing, and a missing line names the fix —
the command that clears it:

  .tick/runners.toml  the routing and the gate a run reads — init's to write
  tk                  the tracker binary the run reads and writes through
  herdr               a herdr server, when the substrate would dispatch through panes
  github              the credential the PR + CI close-out rule holds on
  git identity        the user.email and user.name a run's commits are attributed to
  docker              cloud only: the sandbox image builds from one
  wrangler            cloud only: the factory is driven through it
  factory             cloud only: a configured factory to boot the containers from

The cloud checks run when the repository declares the cloud (its
.tick/runners.toml substrate, or a .tick/runners.cloud.toml) or --cloud is
passed. Exit 0 when everything a run needs is present; exit 1 when any line
is missing — a script can branch without parsing the words.`,
	}
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fl := defineDoctorFlags(fs)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		if len(args) != 0 {
			fmt.Fprintf(stderr, "ticfac doctor: takes no positional arguments — the flags are the whole surface\n")
			return &printedExit{code: exitUsage}
		}
		// cobra carries the context fang was executed with; a bare
		// construction (a test driving the command) has none, and a probe
		// that dereferences a nil context reports the wrong thing.
		ctx := c.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return codeToErr(runDoctor(ctx, fl, stdout, stderr))
	}
	return cmd
}

// doctorCheck is one line of the report: a passing check's detail, or a
// missing check's problem and the fix that clears it.
type doctorCheck struct {
	Name    string
	OK      bool
	Detail  string
	Problem string
	Fix     string
}

// The probes, as seams. Each production value is the real environment probe;
// tests install controlled answers. See the file comment for why.

var (
	// doctorTK answers for the tracker binary: the detail a passing check
	// reports, or the error a missing one explains itself with.
	doctorTK = func(ctx context.Context, dir string) (string, error) {
		client, err := tk.New(tk.Options{Dir: dir})
		if err != nil {
			return "", err
		}
		v, err := client.Version(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("tk %s answers, contract %d", v.Tk, v.Contract), nil
	}

	// doctorHerdr answers for a herdr server, through the real client and the
	// run's own socket resolution order.
	doctorHerdr = func(ctx context.Context) (string, error) {
		client, err := herdclient.New(ctx, herdclient.Options{})
		if err != nil {
			return "", err
		}
		info, err := client.Ping(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("herdr %s answers at %s", info.Version, client.SocketPath()), nil
	}

	// doctorGitHub answers for a forge credential: GITHUB_TOKEN, or gh's own
	// auth. The run's surface reads the first; gh is how a person gets one.
	doctorGitHub = func() (string, error) {
		if token := forge.ResolveToken(); token != "" {
			return "GITHUB_TOKEN is set", nil
		}
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			return "", fmt.Errorf("gh auth token does not answer: %v", err)
		}
		if strings.TrimSpace(string(out)) == "" {
			return "", fmt.Errorf("gh auth token printed no token")
		}
		return "gh auth token answers", nil
	}

	// doctorGitIdentity answers for the git identity a run's commits carry.
	doctorGitIdentity = func(repo string) (string, error) {
		email, err := exec.Command("git", "-C", repo, "config", "user.email").Output()
		if err != nil || strings.TrimSpace(string(email)) == "" {
			return "", fmt.Errorf("git config user.email is not set in %s", repo)
		}
		name, err := exec.Command("git", "-C", repo, "config", "user.name").Output()
		if err != nil || strings.TrimSpace(string(name)) == "" {
			return "", fmt.Errorf("git config user.name is not set in %s", repo)
		}
		return fmt.Sprintf("%s <%s>", strings.TrimSpace(string(name)), strings.TrimSpace(string(email))), nil
	}

	// doctorDocker answers for a usable docker daemon.
	doctorDocker = func(ctx context.Context) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Output()
		if err != nil {
			return "", fmt.Errorf("docker info does not answer: %v", err)
		}
		if strings.TrimSpace(string(out)) == "" {
			return "", fmt.Errorf("docker info answered with no server version — is the daemon running?")
		}
		return "docker daemon answers, server " + strings.TrimSpace(string(out)), nil
	}

	// doctorWrangler answers for wrangler on PATH.
	doctorWrangler = func() (string, error) {
		out, err := exec.Command("wrangler", "--version").Output()
		if err != nil {
			return "", fmt.Errorf("wrangler --version does not answer: %v", err)
		}
		first := strings.TrimSpace(string(out))
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		return first, nil
	}

	// doctorFactory answers for a configured factory, the same status
	// `ticfac factory status` reads — offline here, because doctor reports
	// what the machine holds, and the live re-check is the status command's
	// own job.
	doctorFactory = func(ctx context.Context) (string, error) {
		report, err := factory.Status(ctx, factory.StatusOptions{
			Offline: true, CurrentVersion: Version,
		})
		if err != nil {
			return "", err
		}
		if !report.Configured() {
			return "", fmt.Errorf("no factory is configured in %s", report.ConfigPath)
		}
		return fmt.Sprintf("configured (re-check it live: ticfac factory status) — %s", report.ConfigPath), nil
	}
)

// The fix each missing check names. Kept beside the checks that use them, so
// the remedy travels with its diagnosis.
const (
	doctorFixTK       = "install tk (github.com/pengelbrecht/ticks) and make sure `tk version` answers"
	doctorFixHerdr    = "start herdr in this checkout (`herdr`)"
	doctorFixGitHub   = "gh auth login, or export GITHUB_TOKEN"
	doctorFixGit      = `git config --global user.email "you@example.com" && git config --global user.name "Your Name"`
	doctorFixDocker   = "install Docker and start it (https://docs.docker.com/get-docker/)"
	doctorFixWrangler = "pnpm add -g wrangler"
	doctorFixFactory  = "ticfac factory setup"
)

// runDoctor is `doctor`'s body.
func runDoctor(ctx context.Context, fl *doctorFlags, stdout, stderr io.Writer) int {
	repo := *fl.repo
	if repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "ticfac doctor: cannot read the working directory: %v\n", err)
			return exitGeneric
		}
		repo = wd
	}

	// The substrate the repository declares, and whether the cloud is in it.
	// A repository with no runconfig at all is not an error here — doctor is
	// the command that runs BEFORE init too, and the runners.toml check
	// below says exactly that, with init as its fix.
	cfg, cfgErr := runconfig.LoadRepo(repo)
	substrate := "none declared"
	cloudish := *fl.cloud
	if cfgErr != nil {
		fmt.Fprintf(stderr, "ticfac doctor: the repository's %s does not validate: %v\n",
			runconfig.FileName, cfgErr)
		return exitGeneric
	}
	if cfg != nil {
		substrate = string(cfg.Substrate())
		cloudish = cloudish || cfg.Substrate() == runconfig.SubstrateCloud
	}
	// A cloud override file is a repository that runs to the cloud even when
	// the common file's substrate is auto (the "both" answer init writes).
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(initCloudName))); err == nil {
		cloudish = true
	}

	// check runs one probe and turns its answer into a report line.
	check := func(name, fix string, probe func() (string, error)) doctorCheck {
		detail, err := probe()
		if err != nil {
			return doctorCheck{Name: name, Problem: err.Error(), Fix: fix}
		}
		return doctorCheck{Name: name, OK: true, Detail: detail}
	}

	// .tick/runners.toml is not one of the machine checks — it is the
	// REPOSITORY's half, init's to write — but a doctor that said nothing
	// about a repository a run would refuse at construction ("declares no
	// [testing.commands]") would be a doctor answering the question it felt
	// like. Its probe is the validated reader plus the gate reader, not a
	// seam: the file is the one thing doctor reads rather than asks.
	runnersCheck := func() doctorCheck {
		if cfg == nil {
			return doctorCheck{Name: runconfig.FileName,
				Problem: "does not exist — a run reads its routing and its gate from it",
				Fix:     "ticfac init"}
		}
		if _, err := reconcile.ReadGateCommands(filepath.Join(repo, filepath.FromSlash(runconfig.FileName))); err != nil {
			return doctorCheck{Name: runconfig.FileName, Problem: err.Error(), Fix: "ticfac init"}
		}
		return doctorCheck{Name: runconfig.FileName, OK: true,
			Detail: fmt.Sprintf("validates; gate declared; substrate %q", cfg.Substrate())}
	}

	checks := []doctorCheck{
		runnersCheck(),
		check("tk", doctorFixTK, func() (string, error) { return doctorTK(ctx, repo) }),
		check("herdr", doctorFixHerdr, func() (string, error) { return doctorHerdr(ctx) }),
		check("github", doctorFixGitHub, doctorGitHub),
		check("git identity", doctorFixGit, func() (string, error) { return doctorGitIdentity(repo) }),
	}
	// The herdr line is honest about what a missing herdr means: the
	// substrate degrades to a plain harness, it does not stop the run — the
	// fix is still on the line, because panes are where an operator watches
	// work happen, which is the experience this epic is about.
	for i := range checks {
		if checks[i].Name == "herdr" && !checks[i].OK {
			checks[i].Problem += " — runs degrade to a plain harness without it"
		}
	}
	if cloudish {
		checks = append(checks,
			check("docker", doctorFixDocker, func() (string, error) { return doctorDocker(ctx) }),
			check("wrangler", doctorFixWrangler, doctorWrangler),
			check("factory", doctorFixFactory, func() (string, error) { return doctorFactory(ctx) }),
		)
	}

	return doctorReport(checks, substrate, cloudish, stdout)
}

// doctorReport prints the checks and answers the exit code: 0 everything a
// run needs, 1 anything missing — a script branches on the code without
// parsing the words.
func doctorReport(checks []doctorCheck, substrate string, cloudish bool, stdout io.Writer) int {
	fmt.Fprintf(stdout, "ticfac doctor — what a run of this repo needs (substrate: %s)\n", substrate)
	if cloudish {
		fmt.Fprintf(stdout, "cloud checks included\n")
	}
	missing := 0
	for _, c := range checks {
		if c.OK {
			fmt.Fprintf(stdout, "  ok       %-15s %s\n", c.Name, c.Detail)
			continue
		}
		missing++
		fmt.Fprintf(stdout, "  missing  %-15s %s\n", c.Name, c.Problem)
		fmt.Fprintf(stdout, "  %-8s %-15s fix: %s\n", "", "", c.Fix)
	}
	switch missing {
	case 0:
		fmt.Fprintf(stdout, "everything a run needs is present\n")
		return exitSuccess
	case 1:
		fmt.Fprintf(stdout, "1 thing missing — run its fix above and doctor again\n")
	default:
		fmt.Fprintf(stdout, "%d things missing — run each fix above and doctor again\n", missing)
	}
	return exitGeneric
}
