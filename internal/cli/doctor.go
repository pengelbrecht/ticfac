package cli

// `ticfac doctor` (tick o0d): say what a run of this repository needs, and
// for each thing missing, the fix.
//
// init makes the REPOSITORY ready; doctor answers the other half — the
// machine a run starts on. A run needs tk (the tracker binary the run reads
// and writes through), a git identity (commits made by nobody are commits
// nobody can attribute), a herdr server when the substrate would dispatch
// through panes, and — when the repository declares the PR + CI close-out
// rule — BOTH halves of a forge: a GitHub remote to open the epic PR on and
// a credential to speak with (tick 6vp: a rule over a non-GitHub origin was
// an ok here that the run then refused to start on). A cloud run needs
// more: docker (the sandbox image builds from one), wrangler, and a
// configured factory.
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
	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// doctorFlags is `doctor`'s flag surface.
type doctorFlags struct {
	repo   *string
	cloud  *bool
	asJSON *bool
}

func defineDoctorFlags(fs *flag.FlagSet) *doctorFlags {
	return &doctorFlags{
		repo:   fs.String("repo", "", "the repository a run is checked for (default: cwd)"),
		cloud:  fs.Bool("cloud", false, "check the cloud prerequisites too, whatever the repository declares"),
		asJSON: fs.Bool("json", false, "print one versioned document (ticfac.doctor.v1): every check with its verdict and, when it fails, its fix"),
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
  github              the GitHub remote and credential the PR + CI close-out
                      rule needs — checked when the repository declares the rule
  git identity        the user.email and user.name a run's commits are attributed to
  classifier          Jev (typesafe/jev on Cloudflare Workers AI) answers one tiny
                      question on the credential a run resolves — without it a
                      run classifies nothing and every dispatch starts at
                      [tier_policy.start]
  docker             cloud only: the sandbox image builds from one
  wrangler            cloud only: the factory is driven through it
  factory             cloud only: a configured factory to boot the containers from,
                      and its GitHub rung checked live — on the App rung, a
                      read-only token minted for this repository

The cloud checks run when the repository declares the cloud (its
.tick/runners.toml substrate, or a .tick/runners.cloud.toml) or --cloud is
passed. Exit 0 when everything a run needs is present; exit 1 when any line
is missing — a script can branch without parsing the words. --json answers
the same checks as one versioned document (ticfac.doctor.v1), each missing
thing still carrying its fix.`,
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

	// doctorGitHub answers for a forge credential through the SAME ladder
	// the run's own surface resolves its token from (tick vo4): GITHUB_TOKEN
	// first, then gh's own auth — so this check's ok is the answer the run
	// gets, not an optimism the run then refuses at startup. The detail names
	// the rung that answered, because a "github ok" without its source is
	// the half-answer that hid which credential a run would speak with.
	doctorGitHub = func() (string, error) {
		token, source, err := resolveForgeToken()
		if err != nil {
			return "", err
		}
		if token == "" {
			return "", fmt.Errorf("no %s is set, and gh auth token did not answer", forge.TokenEnv)
		}
		if source == forge.TokenSourceEnv {
			return "GITHUB_TOKEN is set", nil
		}
		return "gh auth token answers", nil
	}

	// doctorForgeRemote answers for the half of the forge the credential is
	// not (tick 6vp): the remote the rule's epic PR opens on, resolved
	// through the SAME reader the run's own surface resolves it through
	// (forge.ParseRepo) — so this check's ok is the repository the run will
	// actually address, and a declared rule over a non-GitHub origin is a
	// missing line here, not an ok the run refuses to start on.
	doctorForgeRemote = func(repo string) (string, error) {
		url, err := exec.Command("git", "-C", repo, "remote", "get-url", "origin").Output()
		if err != nil {
			return "", fmt.Errorf("no origin remote to open the epic PR on — read the origin remote: %v", err)
		}
		return forge.ParseRepo(string(url))
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

	// doctorClassifier answers for the classifier (tick tum): whether Jev —
	// typesafe/jev on Cloudflare Workers AI — ANSWERS on the credential a
	// local run would resolve, by asking it one tiny question (a couple of
	// hundred input tokens). Resolving is not answering: a token without
	// Workers AI permission, or a wrong account, resolves fine and classifies
	// nothing, and the run finds out only by recording a no-answer per tick.
	doctorClassifier = func(ctx context.Context) (string, error) {
		source := resolveClassifierCredential()
		if !source.Configured {
			return "", fmt.Errorf("%s", source.Note)
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		detail, err := jev.New(source.Config, nil).Probe(ctx)
		if err != nil {
			return "", fmt.Errorf("Jev did not answer: %v — every dispatch would start at [tier_policy.start]", err)
		}
		return detail, nil
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
		// The GitHub rung IS checked live (epic dm6): which rung the factory
		// uses is the factory's own fact — it may hold a GitHub App this
		// machine has no record of — and on the App rung the check is a
		// read-only token minted for the repository, which is the one thing
		// that proves a run could clone and push.
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		github, err := factory.GitHubRung(ctx, factory.StatusOptions{})
		if err != nil {
			return "", err
		}
		if github.Configured && github.Checked && !github.OK {
			return "", fmt.Errorf("github %s: %s", github.Summary, github.Detail)
		}
		rung := "github: not configured"
		if github.Configured {
			rung = "github " + github.Summary + " — " + orUncheckedDetail(github.Detail)
		}
		return fmt.Sprintf("configured (re-check it all live: ticfac factory status) — %s; %s", report.ConfigPath, rung), nil
	}
)

// The fix each missing check names. Kept beside the checks that use them, so
// the remedy travels with its diagnosis.
const (
	doctorFixTK          = "install tk (github.com/pengelbrecht/ticks) and make sure `tk version` answers"
	doctorFixHerdr       = "start herdr in this checkout (`herdr`)"
	doctorFixGitHub      = "gh auth login, or export GITHUB_TOKEN"
	doctorFixForgeRemote = "point origin at GitHub (`git remote add origin git@github.com:owner/name.git`), or remove the close-out rule from .tick/config.md"
	doctorFixGit         = `git config --global user.email "you@example.com" && git config --global user.name "Your Name"`
	doctorFixDocker      = "install Docker and start it (https://docs.docker.com/get-docker/)"
	doctorFixWrangler    = "pnpm add -g wrangler"
	doctorFixFactory     = "ticfac factory setup"
	// The classifier runs on the Cloudflare credential the setup ladder
	// already stores; the token needs Workers AI permission on the account
	// the gateway URL names.
	doctorFixClassifier = "ticfac factory setup --cloudflare-api-token <token> (a Cloudflare API token with Workers AI access, on the account factory_gateway_url names)"
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

	// The close-out rule decides the forge check (ticks hio and 6vp): the
	// run resolves a GitHub surface when and only when the rule is declared,
	// so doctor checks exactly that. A repository that declares no rule is
	// not told its credential is missing — a fix for a problem no run would
	// have — and one that declares it is checked for BOTH halves the rule
	// needs: the remote the epic PR opens on, then the credential.
	rule, err := reconcile.ReadCloseoutRule(reconcile.RepoConfigPath(repo))
	if err != nil {
		fmt.Fprintf(stderr, "ticfac doctor: the repository's close-out rule could not be read: %v\n", err)
		return exitGeneric
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
	}
	// The github line carries both halves the rule needs, each with its own
	// fix: a missing remote is not cleared by `gh auth login`, and a missing
	// credential is not cleared by a new origin.
	if rule.Declared {
		checks = append(checks, func() doctorCheck {
			slug, err := doctorForgeRemote(repo)
			if err != nil {
				return doctorCheck{Name: "github", Problem: err.Error(), Fix: doctorFixForgeRemote}
			}
			detail, err := doctorGitHub()
			if err != nil {
				return doctorCheck{Name: "github", Problem: err.Error(), Fix: doctorFixGitHub}
			}
			return doctorCheck{Name: "github", OK: true,
				Detail: fmt.Sprintf("origin is %s; %s", slug, detail)}
		}())
	}
	checks = append(checks, check("git identity", doctorFixGit, func() (string, error) { return doctorGitIdentity(repo) }))
	// Every run classifies its role-less ticks before their first dispatch,
	// local or cloud, so the classifier is checked whatever the substrate.
	checks = append(checks, check("classifier", doctorFixClassifier, func() (string, error) { return doctorClassifier(ctx) }))
	// The herdr line is honest about what a missing herdr means: the
	// substrate degrades to a plain harness, it does not stop the run — the
	// fix is still on the line, because panes are where an operator watches
	// work happen, which is the experience this epic is about.
	for i := range checks {
		if checks[i].Name == "herdr" && !checks[i].OK {
			checks[i].Problem += " — runs degrade to a plain harness without it"
		}
	}
	// The classifier line is honest the same way: without it a run
	// classifies nothing and routes every dispatch at [tier_policy.start];
	// it does not stop. (The probe's own notes already say so.)
	if cloudish {
		checks = append(checks,
			check("docker", doctorFixDocker, func() (string, error) { return doctorDocker(ctx) }),
			check("wrangler", doctorFixWrangler, doctorWrangler),
			check("factory", doctorFixFactory, func() (string, error) { return doctorFactory(ctx) }),
		)
	}

	return doctorReport(checks, substrate, cloudish, *fl.asJSON, stdout, stderr)
}

// doctorCheckJSON is one check as the --json document carries it: the
// verdict, the passing detail, and — when it fails — the problem and the
// fix that clears it, the same fields the prose line prints.
type doctorCheckJSON struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Problem string `json:"problem,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// doctorJSON is `doctor --json`'s answer, ticfac.doctor.v1.
type doctorJSON struct {
	agentDoc
	Substrate string            `json:"substrate"`
	Cloud     bool              `json:"cloud_checks"`
	Missing   int               `json:"missing"`
	Checks    []doctorCheckJSON `json:"checks"`
}

// doctorReport prints the checks and answers the exit code: 0 everything a
// run needs, 1 anything missing — a script branches on the code without
// parsing the words. With --json the same answer is one versioned document,
// and the state word agrees with the code: done when everything is present,
// failed when a line is missing — the command's work is the diagnosis, and
// a missing prerequisite is the thing the caller must fix.
func doctorReport(checks []doctorCheck, substrate string, cloudish bool, asJSON bool, stdout, stderr io.Writer) int {
	missing := 0
	for _, c := range checks {
		if !c.OK {
			missing++
		}
	}
	if asJSON {
		doc := doctorJSON{
			agentDoc:  agentDoc{Schema: agentSchemaID("doctor")},
			Substrate: substrate,
			Cloud:     cloudish,
			Missing:   missing,
			Checks:    make([]doctorCheckJSON, 0, len(checks)),
		}
		if missing == 0 {
			doc.State = agentStateDone
		} else {
			doc.State = agentStateFailed
		}
		for _, c := range checks {
			doc.Checks = append(doc.Checks, doctorCheckJSON{
				Name: c.Name, OK: c.OK, Detail: c.Detail, Problem: c.Problem, Fix: c.Fix,
			})
		}
		if err := emitAgentJSON(stdout, doc); err != nil {
			fmt.Fprintf(stderr, "ticfac doctor: %v\n", err)
			return exitGeneric
		}
		if missing == 0 {
			return exitSuccess
		}
		return exitGeneric
	}

	fmt.Fprintf(stdout, "ticfac doctor — what a run of this repo needs (substrate: %s)\n", substrate)
	if cloudish {
		fmt.Fprintf(stdout, "cloud checks included\n")
	}
	for _, c := range checks {
		if c.OK {
			fmt.Fprintf(stdout, "  ok       %-15s %s\n", c.Name, c.Detail)
			continue
		}
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

// orUncheckedDetail is a rung's detail, or what its absence means.
func orUncheckedDetail(detail string) string {
	if detail == "" {
		return "not checked"
	}
	return detail
}
