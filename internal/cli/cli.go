// Package cli is ticfac's command surface.
//
// `run-epic` is the reconciler's entry point (SPEC §12 Phase 1). It FAILS
// CLOSED: a build with no executor behind contracts/job-protocol.json's four
// operations refuses rather than doing something plausible, because a run that
// half-starts is the failure mode Appendix A was written out of.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/factory/credentials"
	"github.com/pengelbrecht/ticfac/internal/gatewaytrace"
	"github.com/pengelbrecht/ticfac/internal/jev"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runsignal"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// tempSweepAge is how long a dead process's temp dir is left alone before
// run-epic removes it (tick w9j): a day, well past any leg that could still
// be reading one.
const tempSweepAge = 24 * time.Hour

// newTracker builds the tracker a command works through, as the tk client
// against one checkout. It is a seam for exactly one proof — the SIGTERM
// evacuation test (tick ppt) — which runs a REAL run-epic as a real child
// process without a tk binary on PATH: a fake answers the tracker interface
// the reconciler reads, and everything above that seam — the reconciler, the
// executor, the supervisor, the signal handler — is the production code the
// acceptance criterion is about.
var newTracker = func(repo string) (reconcile.Tracker, error) {
	return tk.New(tk.Options{Dir: repo})
}

// Version is this build's version, set with -ldflags "-X
// github.com/pengelbrecht/ticfac/internal/cli.Version=<v>". "dev" is an
// untagged build, exactly as tk reports it.
var Version = "dev"

// ExitNoExecutor is the exit code for a refusal to run: the request was
// understood and this build cannot serve it. It has its own slot so that
// "nothing is configured to run this" is distinguishable from a usage error
// without parsing stderr.
const ExitNoExecutor = 2

// NoExecutorMessage is the fail-closed refusal. It is asserted by a test: a
// silent or differently-worded refusal is the thing an operator misreads as a
// run that started.
const NoExecutorMessage = reconcile.NoExecutorMessage

// Run's old hand-rolled dispatcher is gone (tick nwj): the switch lived in
// cli.go and became the cobra tree in root.go — and the usage text it
// printed, a hand-rolled const that said every command and flag a second
// time beside the tree, is gone too (tick fi3). The bare invocation now
// refuses with the tree's own help, derived from the same declarations the
// help, the completions and the man pages render, so no operator-facing text
// is maintained by hand beside the tree.

// runEpicFlags is `run-epic`'s flag surface: the same definitions the command
// has always parsed, now declared once per invocation so cobra's parse (and
// the help, completions and man pages derived from the tree) and the body's
// reads share one declaration.
type runEpicFlags struct {
	repo, remote, branch, base, runID, owner, runner, tier, profiles, stateRoot, gate *string
	budget, ceiling                                                                   *float64
	wall, maxResumes, stallWarn, evacuateSeconds, absorptionDepth, stuckAfter         *int
	supervise, statusPush                                                             *bool
	asJSON                                                                            *bool
}

// defineRunEpicFlags declares every run-epic flag on fs — defaults, usage
// strings and the comments that explain the defaults, verbatim from the
// body that parsed them.
func defineRunEpicFlags(fs *flag.FlagSet) *runEpicFlags {
	return &runEpicFlags{
		repo:      fs.String("repo", "", "the checkout attempts branch from"),
		remote:    fs.String("remote", "origin", "the remote holding the run's durable authority"),
		branch:    fs.String("branch", "", "the EpicRun integration branch"),
		base:      fs.String("base", "HEAD", "what the integration branch is cut from"),
		runID:     fs.String("run-id", "", "the run's id"),
		owner:     fs.String("owner", "ticfac", "who claims a tick in the tracker"),
		runner:    fs.String("runner", os.Getenv("TICFAC_RUNNER"), "claude | codex | pi"),
		tier:      fs.String("tier", "", "pin a [roles.*.tiers.<name>] overlay for every dispatch of this run (by default the tier is DERIVED per tick from [tier_policy])"),
		profiles:  fs.String("profiles", "", "resolve role profiles from this directory (\"herdr\" names the herdr set embedded in this binary)"),
		stateRoot: fs.String("state-root", "", "where attempt state lives, outside the repository"),
		gate:      fs.String("gate", "", "the runners.toml the integrated gate is read from"),
		budget:    fs.Float64("budget", 0, "the budget an operator asks for"),
		ceiling:   fs.Float64("ceiling", 0, "the deployment ceiling it is clamped to"),
		wall: fs.Int("wall", reconcile.DefaultWallSeconds, "the runaway backstop one job is bounded by, in seconds "+
			"(a stuck worker is found by the stuck watch, not by this); [tier_policy.wall_seconds] and a tick's "+
			"wall_minutes:<n> label override it"),
		stuckAfter: fs.Int("stuck-after", int(reconcile.DefaultStuckAfter/time.Second),
			"how many seconds a worker may show no activity — no transcript event, no tool-process CPU, no worktree "+
				"or branch change — before it is nudged in its own session, and again before it is stopped; "+
				"negative disables the stuck watch"),
		// Supervision is ON by default (tick go6), and the default is the
		// argument. The behaviour it replaces is not "the run stops" — it is
		// "the run stops and a person retypes the identical command", which
		// the operator did about fifteen times in one day. Defaulting to off
		// would keep exactly that behaviour with worse latency (the person has
		// to notice first: two of the day's stalls were multi-hour) and no
		// record that the loop happened at all. Neither is safer. What IS
		// safer is that the loop is now bounded, classified and counted: it
		// continues only across the closed set of stops that need nobody, it
		// halts on a repeat over an unchanged tree, and every continuation is
		// RECORDED as an intervention.
		supervise: fs.Bool("supervise", true,
			"continue across stops that only need resuming — a rejected attempt that left nothing, a tracker "+
				"width refusal, gate evidence that went stale, a transient remote failure — adopting the "+
				"in-flight attempts by identity, with bounded backoff and a cap. Every continuation is RECORDED "+
				"as an intervention. A stop that needs a person still stops. --supervise=false stops at the "+
				"first refusal"),
		maxResumes: fs.Int("max-resumes", reconcile.DefaultAutoResumeCap,
			"the most automatic continuations one supervised run makes before it stops with the refusal it "+
				"stopped over; the cap is the safety against a run that resumes forever over the same stop"),
		stallWarn: fs.Int("stall-warn", int(reconcile.DefaultStallWarnAfter/time.Second),
			"how many seconds an in-flight attempt may produce nothing durable (branch unmoved, worktree unchanged) "+
				"before the run says so in the feed — an early warning, never a verdict; 0 is the default, negative disables"),
		// The eviction flush's bound (tick ppt). The platform sends SIGTERM,
		// waits up to fifteen minutes, then SIGKILLs; the flush commits and
		// pushes the in-flight work and writes the checkpoint inside THIS many
		// seconds, so a hung push cannot spend the whole window and reach
		// SIGKILL anyway. Zero and below disable the flush — the pre-ppt
		// behaviour, an immediate exit that leaves the work to whatever the
		// last timer push carried away.
		evacuateSeconds: fs.Int("evacuate-seconds", int(reconcile.DefaultEvacuationBudget/time.Second),
			"how many seconds a SIGTERM's final flush may spend committing and pushing the in-flight work "+
				"and writing the checkpoint before the process exits anyway (0 disables the flush)"),
		// The absorption recursion's bound (tick qjj). Depth rather than wall
		// clock: depth counts how far the run has travelled from the epic
		// anyone asked for, and only a person can judge that. The default's
		// reasoning lives on the constant; here the operator reads what the
		// number governs and where to raise it when the stop is wrong.
		//
		// The default here is 0 — NOT NAMED — rather than the constant,
		// because the reconciler cannot tell an operator who wrote
		// --absorption-depth 3 from one who wrote nothing (tick wz0, finding
		// 95f5ee1a): a named bound is the person's raise over the recorded
		// one, and an unnamed one adopts what the run branch records, so a
		// cold restart honours the bound the warm run ran with. Zero and
		// below still mean the DEFAULT inside the options' own normalising,
		// never an unbounded recursion.
		absorptionDepth: fs.Int("absorption-depth", 0,
			"how many absorptions ONE chain of the recursion may carry — a gating defect found in the "+
				"epic's own ground is the first link, one found while fixing an absorbed defect the next — "+
				"before the run stops for a person carrying the whole chain (0, the default, means not "+
				"named: the run adopts the bound recorded on the run branch, defaulting to 3 — the first "+
				"link is what the epic exists to absorb, the second is a defect in the absorbed fix's own "+
				"ground, a third is already far from home, and past that a person should judge the chain "+
				"rather than let the run keep going)"),
		// The remote view (tick i1r): opted in, the run pushes its status model
		// to the configured factory on a short cadence, so the factory's phone
		// page (/status) lists it beside the cloud runs it hosts - and a run
		// needing a person pages the operator's Telegram through the same
		// factory. Off by default: the factory's snapshot door is authenticated
		// by the operator's own factory token, so the opt-in is the operator's -
		// asked per run with the flag, or once for the machine with
		// $TICFAC_STATUS_PUSH (the flag always wins).
		statusPush: fs.Bool("status-push", statusPushEnvDefault(os.Getenv),
			"push this run's status model to the factory every 30s (and once more at each ending), so it is "+
				"followable from the factory's /status phone page and its stops page the operator's "+
				"Telegram. Needs a configured factory (ticfac factory setup); without one this is a no-op, "+
				"and it is always best-effort: a push that fails is a line in run.log, never a failure of the run "+
				"(default: $TICFAC_STATUS_PUSH, read as a strict bool - set it once to follow every run; "+
				"the flag always wins)"),
		asJSON: fs.Bool("json", false,
			"answer as one versioned document (ticfac.run-epic.v1) when the run ends: the run's state, "+
				"every tick's state, the refusal's reason class when it stopped, and the interventions it made — "+
				"the run's own prose goes to stderr, so stdout is the document's alone"),
	}
}

// newRunEpicCommand builds the cobra command: the flags on the tree, the
// body behind it.
func newRunEpicCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run-epic <epic-id>",
		Short: "run one epic through the reconciler",
		Long: `Run one epic: reconcile the tracker against Git, dispatch each ready tick
through the executor its resolved profile names, and integrate what the
workers push.

Each dispatch goes through the executor its resolved profile names — a profile
naming the herdr executor launches the attempt in a herdr workspace, a profile
naming cloudflare-sandbox asks the factory's per-tick sandbox door to boot one
attempt's worker container (the factory's base URL and the run's own gateway
token come from TICKS_FACTORY_URL and TICKS_FACTORY_TOKEN), and the wall
clock, the report and the boundary are judged from the branch and the report
in git at collect. This build honours three executors — local-subprocess,
herdr and cloudflare-sandbox — and refuses a profile naming any other before
anything is claimed.

The effective budget — what an operator asked for, clamped to the deployment
ceiling — is printed before the run starts, while it can still be cancelled
cheaply. It binds a METERED credential; the local subprocess executor issues a
flat-rate one, so on this host the number travels with the job and is reported
everywhere, and the wall clock is what actually stops one.

Each role-less implementation tick is classified through Jev (typesafe/jev on
Cloudflare Workers AI) before its first dispatch, and the credential that call
rides is resolved and printed at startup: inside a cloud sandbox it is the
run's own gateway route (AI_GATEWAY_BASE_URL/jev) with the run token, locally
it is the Cloudflare API token and account 'ticfac factory setup' stored in
~/.ticfacrc (factory_cloudflare_api_token, and the account in
factory_gateway_url; $TICFAC_JEV_API_TOKEN and $TICFAC_JEV_ACCOUNT_ID override
them). 'ticfac doctor' asks Jev one tiny question to say whether it answers.
No credential — or an unreachable, refused or
misconfigured classifier — is the documented fallback, said at startup and
recorded per ask: the run classifies nothing or records its no-answer, and
every dispatch starts at [tier_policy.start].

A long run wants the machine awake end to end. A machine that sleeps mid-run
kills workers without settling them, and what it leaves — a held attempt, a
missing report, a run stopped at nothing — looks exactly like a worker defect
when it is the host's: wrap the run in caffeinate -i on macOS, or the
equivalent elsewhere, rather than letting the machine sleep (tick 0z0).

A target repository may declare, in .tick/config.md's Rules section, that an
epic integrates through a PR + CI gate: the run opens the epic PR itself,
holds the close-out until CI is green on it, and refuses typed — naming the
failing job — when CI is red. That rule needs a code-hosting surface: the
GitHub one is built from the remote and a credential resolved from one
ladder — GITHUB_TOKEN in the environment, or gh's own auth (tick vo4) —
and a repo declaring the rule is refused at startup until one of them
answers (tick 0iz).`,
	}
	fs := flag.NewFlagSet("run-epic", flag.ContinueOnError)
	fl := defineRunEpicFlags(fs)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(runEpic(args, fl, stdout, stderr))
	}
	return cmd
}

func runEpic(args []string, fl *runEpicFlags, stdout, stderr io.Writer) (code int) {
	rest := args
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(stderr, "ticfac run-epic: exactly one epic id is required\n")
		return 2
	}
	epicID := rest[0]

	// The document's writer and the prose's, kept apart from the first line
	// (tick 8v3): with --json the run's own words — the startup line, the
	// classifier's note, the per-tick report — all go to STDERR (and to
	// run.log through the wrap below), and stdout carries exactly one
	// document at the end. Without --json, prose is stdout's as it always
	// was, byte-for-byte.
	answer := stdout
	prose := stdout
	if *fl.asJSON {
		prose = stderr
	}

	// The refusal, and the reason it is a refusal rather than a no-op: without
	// a host behind the four-operation protocol there is nothing that could
	// start, inspect, cancel or collect a job, and reporting success here would
	// be the first of Appendix A's failures — a run recorded as done that never
	// ran.
	if err := reconcile.CheckExecutor(); err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %s.\n", epicID, NoExecutorMessage)
		fmt.Fprintf(stderr, "%v\n", err)
		return ExitNoExecutor
	}

	// Appendix A #12, at the surface an operator actually reads: the number
	// that will GOVERN, said at submission while the run can still be
	// cancelled cheaply. An operator who asked for 40 under an 8 ceiling is
	// told 8 here and not in a journal nobody sees — and told what the number
	// does on this host, because a budget printed like an enforced limit is
	// worse than no line at all.
	if *fl.budget > 0 || *fl.ceiling > 0 {
		clamped := reconcile.ClampBudget(*fl.budget, *fl.ceiling)
		fmt.Fprintf(prose, "%s\n", budgetLine(clamped))
	}

	// The classifier's credential, resolved BEFORE anything is dispatched (tick
	// x0k): inside a cloud sandbox the source is the run's own gateway route
	// with the run token; locally it is the operator's Cloudflare credential
	// from ~/.ticfacrc (tick tum). An unconfigured source is the documented fallback —
	// the run classifies nothing and every dispatch starts at [tier_policy.start]
	// — and the note saying so is printed on the run's own stdout below, so a
	// redirected invocation and run.log both carry it. Resolution happens here
	// because the client is a constructor input; the SAY waits until the run
	// exists, so a refusal writes no stdout of any kind.
	classifier, classifierNote := classifierForRun()

	if *fl.runner == "" {
		*fl.runner = "claude"
	}
	tracker, err := newTracker(*fl.repo)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: the tracker is not usable: %v\n", epicID, err)
		return 1
	}

	// The code-hosting surface behind the PR + CI close-out rule (tick 0iz):
	// built only when the target repo's own .tick/config.md declares the
	// rule (tick hio), from the remote and the token, handed to the
	// reconciler, and nil — with a note, not a crash — when neither resolves.
	// A target repo that declares no rule needs no surface and no credential
	// resolved at all; one that does is refused by the reconciler at
	// construction, naming the credential.
	repoDir := *fl.repo
	if repoDir == "" {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			repoDir = wd
		}
	}
	// The code-hosting surface behind the PR + CI close-out rule (tick 0iz),
	// built with the note naming which rung answered (tick 9sz). The note is
	// SAID on the wrapped stdout below — beside the classifier's — so it
	// lands in run.log with the run's other startup facts rather than on a
	// stdout `ticfac run`'s child has already redirected elsewhere.
	pulls, forgeNote, pullsErr := pullRequestsForRun(repoDir, *fl.remote)
	if pullsErr != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: no code-hosting surface: %v. "+
			"A repository that declares the PR + CI close-out rule in .tick/config.md will be refused "+
			"until one is configured.\n", epicID, pullsErr)
	}

	// The one client, when a credential built one, is handed to BOTH exchanges
	// that ask it: the work-type classification and the gating prediction the
	// absorption decision drives (tick npq). The wrap is a nil CHECK and not
	// a plain field assignment because a typed nil *jev.Client inside an
	// interface is not nil — the seam would dial a client that does not exist,
	// and the nil check each exchange runs would pass it straight through.
	opts := reconcile.Options{
		Repo:                 *fl.repo,
		Remote:               *fl.remote,
		EpicID:               epicID,
		RunID:                *fl.runID,
		IntegrationBranch:    *fl.branch,
		BaseRef:              *fl.base,
		Owner:                *fl.owner,
		Tracker:              tracker,
		NewExecutor:          executorFactory(*fl.runner, *fl.gate),
		NewSweeper:           sweeperFactory(*fl.gate),
		Executors:            knownExecutors(),
		ExecStateRoot:        *fl.stateRoot,
		GateConfig:           *fl.gate,
		ProfileDir:           *fl.profiles,
		Tier:                 *fl.tier,
		WallSeconds:          *fl.wall,
		StuckAfter:           time.Duration(*fl.stuckAfter) * time.Second,
		StallWarnAfter:       time.Duration(*fl.stallWarn) * time.Second,
		BudgetUSD:            *fl.budget,
		CeilingUSD:           *fl.ceiling,
		PullRequests:         pulls,
		AutoResumeCap:        autoResumeCap(*fl.supervise, *fl.maxResumes),
		AbsorptionDepthBound: *fl.absorptionDepth,
		// The person's raise (tick wz0): a bound named on the command line —
		// and only one named there, never the default the flag merely carries
		// — overrides the bound recorded on the run branch.
		AbsorptionDepthExplicit: *fl.absorptionDepth > 0,
	}
	if classifier != nil {
		opts.Classifier = classifier
		opts.GatingClassifier = classifier
	}
	reconciler, err := reconcile.New(opts)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		return 1
	}

	// The process's own account of itself (ticks udp, ix9). run.pid makes
	// "is this run alive?" a fact `ticfac status` answers, and run.log keeps
	// what this process says whether or not whoever launched it captured its
	// output. The pwp production run died once with nothing captured, and its
	// cause was never recovered.
	liveRun := reconciler.RunID()
	life, err := runlife.Claim(repoDir, liveRun)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		return 1
	}
	operatorStderr := stderr
	stderr = io.MultiWriter(stderr, life.Log())
	// The prose destination is the one chosen at the top: stdout for a
	// person, stderr under --json where the document owns stdout.
	stdout = io.MultiWriter(prose, life.Log())

	// A redirected run-epic was silent until the run ended (tick bzx), so a
	// redirected output was no monitoring signal. The run's id and the two
	// commands that follow it, said HERE — before anything is dispatched —
	// and on the wrapped stdout, so the same line lands in run.log whether
	// or not whoever launched the run captured its output.
	fmt.Fprintf(stdout, "%s\n", startupLine(liveRun))

	// Which credential the classifier rides — or that no source resolved and
	// every dispatch starts at [tier_policy.start] — said on the same wrapped
	// stdout, so it lands in run.log beside the startup line (tick x0k). A
	// credential story belongs with the run it governs, and a redirected run
	// that silently classifies nothing is exactly the silence the startup line
	// exists to break.
	fmt.Fprintf(stdout, "%s\n", classifierNote)

	// Which rung the forge's credential came from — or nothing at all, said
	// by hio's gate itself: a repo that declares no close-out rule resolves
	// no credential and needs no note. This is the "says so" half of the
	// one-command run (tick 9sz): a token fetched from gh's own auth is
	// otherwise invisible, and a run that used it without naming it is a run
	// whose credential story starts with a question.
	if forgeNote != "" {
		fmt.Fprintf(stdout, "%s\n", forgeNote)
	}

	// What killed processes left in the temp directory (tick w9j): a SIGKILL
	// runs no cleanup at all. Conservative — only ticfac-* names, never the
	// gate's slot roots, only a directory whose owning pid is gone and whose
	// contents nobody has touched for a day. A swept tree that was a worktree
	// of this checkout leaves a registration with no directory, which the
	// reconciler's own `git worktree prune` drops as the run starts.
	if swept, err := tempdir.Sweep(os.TempDir(), tempSweepAge, time.Now()); err != nil {
		life.Logf("could not sweep stale temp dirs: %v", err)
	} else if len(swept) > 0 {
		life.Logf("swept %d stale temp dir(s) a killed process left behind", len(swept))
	}

	// A death is a terminal feed line, never a feed that simply stops on an
	// ordinary success. Every path through Run that writes run_finished returns
	// without an error, so this is the only terminal line on the paths below.
	died := func(detail string) {
		event := runfeed.NewEvent(time.Now(), liveRun, "", nil, reconcile.StageRunDied, detail)
		if appendErr := runfeed.Open(repoDir, liveRun).Append(event); appendErr != nil {
			life.Logf("could not write run_died to the feed: %v", appendErr)
		}
	}

	// The remote view (tick i1r): opted in with --status-push and a factory
	// configured, the run pushes its status model to the factory on a short
	// cadence while it works, so the factory's phone page (/status) lists it
	// beside the cloud runs it hosts. Nil — the default — is a run that pushes
	// nothing and costs nothing. The Stop defer is registered BEFORE the
	// pidfile release on purpose, so its ending push is written LAST — after
	// the release — and carries the run's terminal answer (a probe that still
	// saw the pidfile would make "done" read "running" forever on the page).
	pusher := startStatusPusher(repoDir, liveRun, operatorStderr, *fl.statusPush)
	defer func() {
		if pusher != nil {
			pusher.Stop()
		}
	}()

	// Registered before everything below so the release runs after it: whatever
	// path the process leaves by, the pidfile is released and the log says how.
	// Release is idempotent, so the specific outcomes below win.
	defer life.Release("returned")
	// Every temp tree still open when the run returns — a gate abandoned
	// mid-command, a leg that errored past its own cleanup — goes with it.
	defer tempdir.ReleaseAll()
	defer func() {
		if p := recover(); p != nil {
			detail := fmt.Sprintf("panicked: %v", p)
			life.Logf("%s\n%s", detail, debug.Stack())
			died(detail)
			life.Release(detail)
			fmt.Fprintf(operatorStderr, "ticfac run-epic %s: %s (stack in %s)\n", epicID, detail,
				runlife.Dir(repoDir, liveRun)+"/"+runlife.LogName)
			code = 2
		}
	}()

	finished := make(chan struct{})
	defer close(finished)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-finished:
		case sig := <-signals:
			detail := signalStopDetail(sig)
			life.Logf("%s", detail)
			// The eviction flush (tick ppt): SIGTERM is the platform saying the
			// container is going away, so the run spends a BOUNDED slice of the
			// grace window making the disk's loss survivable — commit what is in
			// the worktrees, push, write the checkpoint — then exits. The window
			// is not used for anything else; the run does not try to finish
			// work, and a step that cannot be done is said and skipped, so no
			// hung push can eat the window the bound exists to protect.
			//
			// SIGINT keeps the immediate exit it always had: that is a person
			// at a terminal, not a platform eviction, and a person's stop wants
			// no ceremony. The flush is SIGTERM's. But no ceremony is not the
			// failed class: since tick vqc the SIGINT death line is LED by the
			// cancelled state word (signalStopDetail), so the watch answers a
			// deliberate stop with the cancelled class (7) — the same
			// distinction the run_finished cancelled words got (tick rix) —
			// while the SIGTERM eviction stays a death like a panic.
			if sig == syscall.SIGTERM && *fl.evacuateSeconds > 0 {
				budget := time.Duration(*fl.evacuateSeconds) * time.Second
				lines := reconciler.Evacuate(sig.String(), budget)
				for _, line := range lines {
					life.Logf("evacuation: %s", line)
					fmt.Fprintf(operatorStderr, "ticfac run-epic %s: evacuation: %s\n", epicID, line)
				}
				// The summary line is the one fact the run's death record
				// carries forward — the last line of the account, folded into
				// the run_died a subscriber reads from the feed.
				if summary := lines[len(lines)-1]; summary != "" {
					detail += "; " + summary
				}
			}
			// os.Exit runs no defer, and every temp tree the run had open —
			// tracker, merge, gate — was waiting on one (tick w9j). After the
			// flush, which may still need them.
			tempdir.ReleaseAll()
			died(detail)
			life.Release(detail)
			status := 130
			if sig == syscall.SIGTERM {
				status = 143
			}
			os.Exit(status)
		}
	}()

	// Supervise, not Run: the run continues across the stops that need nobody
	// and stops for the ones that need a person (tick go6). With
	// --supervise=false the cap is negative and this is exactly Run.
	result, err := reconciler.Supervise(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %v\n", epicID, err)
		died(err.Error())
		life.Release("died: " + err.Error())
		// The completion signal (tick 7eq): a dead run wants its supervisor
		// woken just as much as a finished one, so the reboot does not wait out
		// a whole cadence to learn what the container already knew. The branch
		// may never have landed — the door takes a signal with no head.
		runsignal.FromEnv(stderr).Done(context.Background(), repoDir, *fl.remote, reconciler.IntegrationBranch())
		if *fl.asJSON {
			emitRunEpicFailureJSON(epicID, err, answer)
		}
		return 1
	}
	defer life.Release(string(result.State))
	fmt.Fprintf(stdout, "run %s of epic %s: %s\n%s\n", result.RunID, result.EpicID, result.State, result.Reason)
	for _, tick := range result.Ticks {
		fmt.Fprintf(stdout, "  %-8s %s\n", tick.TickID, tick.State)
	}
	// The intervention count, said to the operator's face rather than left in
	// the feed (ticks go6, zi2). It is printed whenever it is non-zero, and it
	// is printed BEFORE any verdict line, because a run that reached
	// "completed" after four automatic continuations has not demonstrated what
	// an unattended run demonstrates, and the number is the whole of that
	// distinction.
	if len(result.Resumes) > 0 {
		fmt.Fprintf(stdout, "%s\n", resumeLine(result))
	}
	if result.Halt != "" {
		fmt.Fprintf(stderr, "ticfac run-epic %s: the run was not continued automatically: %s\n", epicID, result.Halt)
	}
	// Not a verdict about the work, so it does not change the exit code —
	// but never silent: the operator who would subscribe to this run has to
	// know there is nothing to subscribe to (tick d6s).
	if result.FeedError != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %s\n", epicID, feedFailureLine(result.RunID, result.FeedError))
	}
	// The same shape, about the other file in that directory (tick dh1): the
	// per-poll account of what each attempt was doing. Also not a verdict,
	// also never silent — a run whose liveness record is missing is a run
	// nobody can ask afterwards why an attempt produced nothing.
	if result.LivenessError != nil {
		fmt.Fprintf(stderr, "ticfac run-epic %s: %s\n", epicID, livenessFailureLine(result.RunID, result.LivenessError))
	}
	// The completion signal (tick 7eq), sent on this path and the error path
	// both: a run that errored has as much reason to wake its supervisor
	// immediately as one that finished — the replacement (or the refusal) is a
	// cadence look away otherwise. Best effort by construction: the pushed
	// branch is the source of truth, so FromEnv's nil — a local run with no
	// factory in its environment — makes this a no-op, and a signal that cannot
	// be delivered is said to the log (which is run.log here, the stream the
	// Workflow drains to R2) and swallowed, never an exit code.
	runsignal.FromEnv(stderr).Done(context.Background(), repoDir, *fl.remote, reconciler.IntegrationBranch())
	if *fl.asJSON {
		// The one document (tick 8v3): the run's whole answer as fields, so
		// an agent branching on the exit code can read the same verdict's
		// reason class — the refusal vocabulary, never prose — without a
		// second command.
		emitRunEpicResultJSON(result, answer)
	}
	return resultExitCode(result)
}

// runEpicFailureJSON is `run-epic --json`'s answer for a run that died
// without a result: the error, as the one field it is.
type runEpicFailureJSON struct {
	agentDoc
	EpicID string `json:"epic_id"`
	Error  string `json:"error"`
}

func emitRunEpicFailureJSON(epicID string, err error, stdout io.Writer) {
	doc := runEpicFailureJSON{
		agentDoc: agentDoc{Schema: agentSchemaID("run-epic"), State: agentStateFailed},
		EpicID:   epicID,
		Error:    err.Error(),
	}
	_ = emitAgentJSON(stdout, doc)
}

// runEpicRefusalJSON is a run's typed refusal as the result document carries
// it. Reason is the REASON CLASS — the stable refusal vocabulary
// (reconcile's Refused* constants: finding_untriaged, closeout_ci_failed,
// merge_failed...) an agent branches on; Message is the human sentence.
type runEpicRefusalJSON struct {
	Reason  string `json:"reason"`
	TickID  string `json:"tick_id,omitempty"`
	Message string `json:"message"`
}

// runEpicResumeJSON is one automatic continuation, the intervention record
// the prose resume line counts.
type runEpicResumeJSON struct {
	Reason string `json:"reason"`
	TickID string `json:"tick_id,omitempty"`
}

// runEpicTickJSON is one tick's durable state as the result names it.
type runEpicTickJSON struct {
	TickID string `json:"tick_id"`
	State  string `json:"state"`
}

// runEpicResultJSON is `run-epic --json`'s answer, ticfac.run-epic.v1: the
// whole Result — the run's own state word, every tick, the refusal that
// stopped it with its REASON CLASS, the supervisor's halt, the automatic
// continuations (each an intervention a caller reporting "unattended" must
// count), and the never-silent notes about the feed and liveness records.
// The state word is the table's — done when the run completed, held when the
// run stopped holding something only a person can move (tick 4mv), cancelled
// when it was stopped deliberately (tick rix: the resume path replays an
// already-terminal checkpoint as a Result, and a cancelled replay must
// answer the same word the watch answers), failed otherwise — so the exit
// code and the document cannot disagree; the run's own terminal word
// travels as run_state.
type runEpicResultJSON struct {
	agentDoc
	RunID         string              `json:"run_id"`
	EpicID        string              `json:"epic_id"`
	RunState      string              `json:"run_state"`
	Reason        string              `json:"reason,omitempty"`
	Failure       *runEpicRefusalJSON `json:"failure,omitempty"`
	Halt          string              `json:"halt,omitempty"`
	Resumes       []runEpicResumeJSON `json:"resumes,omitempty"`
	Ticks         []runEpicTickJSON   `json:"ticks"`
	FeedError     string              `json:"feed_error,omitempty"`
	LivenessError string              `json:"liveness_error,omitempty"`
}

func emitRunEpicResultJSON(result *reconcile.Result, stdout io.Writer) {
	doc := runEpicResultJSON{
		agentDoc: agentDoc{Schema: agentSchemaID("run-epic"), State: agentStateFailed},
		RunID:    result.RunID,
		EpicID:   result.EpicID,
		RunState: string(result.State),
		Reason:   result.Reason,
		Halt:     result.Halt,
		Ticks:    make([]runEpicTickJSON, 0, len(result.Ticks)),
	}
	doc.State = runEpicStateWord(result)
	if result.Failure != nil {
		doc.Failure = &runEpicRefusalJSON{
			Reason:  result.Failure.Reason,
			TickID:  result.Failure.TickID,
			Message: result.Failure.Message,
		}
	}
	for _, resume := range result.Resumes {
		doc.Resumes = append(doc.Resumes, runEpicResumeJSON{Reason: resume.Reason, TickID: resume.TickID})
	}
	for _, tick := range result.Ticks {
		doc.Ticks = append(doc.Ticks, runEpicTickJSON{TickID: tick.TickID, State: tick.State})
	}
	if result.FeedError != nil {
		doc.FeedError = result.FeedError.Error()
	}
	if result.LivenessError != nil {
		doc.LivenessError = result.LivenessError.Error()
	}
	_ = emitAgentJSON(stdout, doc)
}

// autoResumeCap turns the operator's two flags into the one number the
// reconciler reads: the cap when supervision is on, and a negative — which is
// supervision off — when it is not. The flag is the surface; the number is the
// configuration, so there is exactly one thing to read when asking whether a
// run was supervised.
func autoResumeCap(supervise bool, cap int) int {
	if !supervise {
		return -1
	}
	return cap
}

// resumeLine is the intervention count an operator reads (tick zi2): how many
// times this run continued by itself, and what it stopped over each time. A
// run that says "completed" after four of these completed a loop a person used
// to perform, which is a real and reportable gain — and it is NOT the same
// claim as a run that never stopped, so the line names each stop rather than
// only counting them.

// signalStopDetail is the run_died line a signal writes (tick vqc): the
// plain sentence for SIGTERM — the platform's eviction, a death like a
// panic — and, for SIGINT, the cancelled state word LED in front of the
// same sentence, because a person at a terminal stopping the run is the
// cancelled class's own case, not a failure to fix. The watch reads the
// state word — the same vocabulary run_finished's details are led by — and
// never has to parse the prose; the eviction keeps the failed class a
// run's death has always had.
func signalStopDetail(sig os.Signal) string {
	detail := fmt.Sprintf("stopped by a signal (%s) before the run finished", sig)
	if sig == syscall.SIGINT {
		detail = string(runstate.StateCancelled) + ": " + detail
	}
	return detail
}

func resumeLine(result *reconcile.Result) string {
	stops := make([]string, 0, len(result.Resumes))
	for _, resume := range result.Resumes {
		reason := resume.Reason
		if resume.TickID != "" {
			reason += " on " + resume.TickID
		}
		stops = append(stops, reason)
	}
	return fmt.Sprintf("interventions: %d automatic continuation(s), which nobody typed and which are counted "+
		"as interventions all the same — %s. A run that continued across these did not run unattended",
		len(result.Resumes), strings.Join(stops, ", "))
}

// startupLine is the one line a redirected run-epic now says while the run
// is still starting (tick bzx): the run's id, and the two commands that
// follow the run — `ticfac status` for is-it-alive, `ticfac events --follow`
// for what it is doing — printed before anything is dispatched, so a
// redirected invocation is a monitoring signal from its first line instead of
// an empty file until the run ends.
func startupLine(runID string) string {
	return fmt.Sprintf("run %s starting — follow it: ticfac status %s (is it alive), "+
		"ticfac events %s --follow (what it is doing, as it does it)", runID, runID, runID)
}

// classifierForRun builds the classifier this run classifies with, from the
// credential source the process found (tick x0k, rewired to Workers AI by tick
// tum): the run's own gateway route inside a cloud sandbox, the operator's
// Cloudflare API token and account from ~/.ticfacrc locally, and nil — with
// the note saying what the run does without one — when neither resolves,
// which is the documented degradation to [tier_policy.start]. The one client
// is handed to BOTH exchanges that ask it: the work-type classification
// (reconcile.Classifier) and the gating prediction the absorption decision
// drives (gating.Classifier, tick npq) — the same credential, the same client,
// two different questions. It is a function of its own so the wiring is the
// same under test as in production: what run-epic hands the reconciler is
// exactly what these tests build.
func classifierForRun() (classifier *jev.Client, note string) {
	source := resolveClassifierCredential()
	if !source.Configured {
		return nil, source.Note
	}
	return jev.New(source.Config, nil), source.Note
}

// resolveClassifierCredential resolves the classifier's credential from this
// process's environment and ~/.ticfacrc: the Cloudflare API token the factory
// setup ladder stored (factory_cloudflare_api_token) and the account its AI
// Gateway URL carries (factory_gateway_url). A ~/.ticfacrc that cannot be read
// is no stored credential, and the note that follows says what is missing —
// classification is a degradation, never a reason to refuse a run.
func resolveClassifierCredential() jev.CredentialSource {
	var stored jev.Stored
	if file, err := credentials.Load(); err == nil {
		stored.APIToken = file.Get(credentials.KeyCloudflareAPIToken)
		if account, _, ok := gatewaytrace.GatewayIDs(file.Get(credentials.KeyGatewayURL)); ok {
			stored.AccountID = account
		}
	}
	return jev.ResolveCredential(os.Getenv, stored)
}

// feedFailureLine is the one sentence a run whose feed could not be written
// owes its operator: what failed, that it is not a verdict about the work,
// and what cannot happen as a result — `ticfac events <run-id> --follow` has
// nothing to follow. The run's own records and the report above remain the
// evidence; the feed is a hint, and this is the hint channel saying it is
// deaf (tick d6s).
func feedFailureLine(runID string, err error) string {
	return fmt.Sprintf("the run's event feed could not be written (%v): this is not a verdict about the work — "+
		"the run's records and the report above are the evidence — but nothing will appear under "+
		".ticfac/logs/%s/, so `ticfac events %s --follow` has nothing to follow", err, runID, runID)
}

// livenessFailureLine is feedFailureLine's other half: the record that says
// what each attempt was DOING could not be written, so the run's own answer
// to "was it working or wedged" is missing for this run (tick dh1).
func livenessFailureLine(runID string, err error) string {
	return fmt.Sprintf("the run's liveness record could not be written (%v): this is not a verdict about the work "+
		"— the run's records and the report above are the evidence — but nothing was written to "+
		".ticfac/logs/%s/liveness.jsonl, so there is no per-poll account of what each attempt produced", err, runID)
}

// budgetLine is the one sentence A12 is about. It says the effective number
// first, because that is the one that will govern, and says what it binds:
// `max_cost_usd` binds a metered credential, and the local subprocess executor
// issues a flat-rate one, so on this host the number is carried and reported
// rather than enforced — the wall clock is what stops a job.
func budgetLine(budget reconcile.Budget) string {
	line := fmt.Sprintf("budget: $%.2f effective", budget.Effective)
	if budget.Clamped {
		line += fmt.Sprintf(" (asked $%.2f, clamped to the deployment ceiling $%.2f)",
			budget.Requested, budget.Ceiling)
	} else if budget.Ceiling > 0 {
		line += fmt.Sprintf(" (asked $%.2f, under the deployment ceiling $%.2f)",
			budget.Requested, budget.Ceiling)
	}
	return line + "; it is issued with every job and reported in every record, and on the local subprocess " +
		"executor it is informational — a flat-rate credential meters nothing, and the wall clock is what bounds a job"
}

// settle releases one attempt nobody can address, on a person's word. See
// internal/reconcile/settle.go for why a person is the next actor at all.
// settleFlags is `settle`'s flag surface, declared once per invocation.
type settleFlags struct {
	repo, remote, branch, runID, runner, tier, profiles, stateRoot, gate, release *string
	carryWork                                                                     *bool
	asJSON                                                                        *bool
}

func defineSettleFlags(fs *flag.FlagSet) *settleFlags {
	return &settleFlags{
		repo:      fs.String("repo", "", "the checkout the run works in"),
		remote:    fs.String("remote", "origin", "the remote holding the run's durable authority"),
		branch:    fs.String("branch", "", "the EpicRun integration branch"),
		runID:     fs.String("run-id", "", "the run's id"),
		runner:    fs.String("runner", os.Getenv("TICFAC_RUNNER"), "claude | codex | pi"),
		tier:      fs.String("tier", "", "the tier the released attempt was dispatched at, as for run-epic"),
		profiles:  fs.String("profiles", "", "resolve role profiles from this directory"),
		stateRoot: fs.String("state-root", "", "where attempt state lives, outside the repository"),
		gate:      fs.String("gate", "", "the runners.toml the run's gate is read from"),
		release:   fs.String("release", "", "the person releasing the attempt"),
		carryWork: fs.Bool("carry-work", false, "base the next attempt of this tick on the released attempt's branch, so the next worker starts from its commits rather than redoing them (the gate still decides)"),
		asJSON:    fs.Bool("json", false, "print one versioned document (ticfac.settle.v1) recording the release: the decision, where the released attempt's work lives, and whether the next run carries it"),
	}
}

// newSettleCommand builds the cobra command for `settle`.
func newSettleCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settle <epic-id> <tick-id> <attempt>",
		Short: "release an attempt nobody can address",
		Long: `Release one attempt nobody can address, on a person's word.

An attempt whose supervisor died without settling it reads as lost, and every
restart holds it rather than starting a second job over the same identity
(Appendix A #6). "settle" is how a PERSON releases one: it refuses an attempt
the executor can still address, records the release durably as a decision
naming who made it, and the next run dispatches a NEW attempt instead of
adopting the released one. Whatever the released attempt committed stays on
its own write ref — and the release says where that ref lives: on the remote,
or only as a local branch in the checkout that holds it when the push never
landed there.

It releases one other attempt: one this run REJECTED while it was holding
commits nothing merged. No run collects that attempt again (the teardown the
refusal ran removed its worktree) and no run dispatches over it (that would
orphan the only copy of the work), so a person reads the branch and then says
here that the run may go on.

--carry-work is the third option that situation actually needs: release the
attempt AND base the next one on its branch, so the next worker starts from the
work rather than redoing it. Nothing merges unproven — the gate still decides —
but the evidence the interrupted attempt produced is not thrown away, and the
next attempt's provenance records that its source is the released attempt's
ref and commit (tick 0z0).`,
	}
	fs := flag.NewFlagSet("settle", flag.ContinueOnError)
	fl := defineSettleFlags(fs)
	commandFlags(cmd, fs)
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(settle(args, fl, stdout, stderr))
	}
	return cmd
}

// settle releases one attempt nobody can address, on a person's word. See
// internal/reconcile/settle.go for why a person is the next actor at all.
func settle(args []string, fl *settleFlags, stdout, stderr io.Writer) int {
	rest := args
	if len(rest) != 3 {
		fmt.Fprintf(stderr, "ticfac settle: exactly one epic id, tick id and attempt number are required\n")
		return 2
	}
	epicID, tickID := rest[0], rest[1]
	attempt, err := strconv.Atoi(rest[2])
	if err != nil || attempt < 1 {
		fmt.Fprintf(stderr, "ticfac settle: %q is not an attempt number\n", rest[2])
		return 2
	}
	if *fl.release == "" {
		fmt.Fprintf(stderr, "ticfac settle: --release names who is releasing the attempt; a release with no "+
			"author is the clock release Appendix A #11 refuses\n")
		return 2
	}
	if parseOnly {
		return 0
	}
	if err := reconcile.CheckExecutor(); err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s: %s.\n%v\n", epicID, NoExecutorMessage, err)
		return ExitNoExecutor
	}
	if *fl.runner == "" {
		*fl.runner = "claude"
	}
	tracker, err := newTracker(*fl.repo)
	if err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s: the tracker is not usable: %v\n", epicID, err)
		return 1
	}

	// A release never reaches the close-out: it writes one settlement record
	// and asks nothing of the epic PR. So it is built release-only, without
	// the code-hosting surface the PR + CI rule needs — a person releasing a
	// stuck attempt is not refused for a GITHUB_TOKEN the release never uses.
	reconciler, err := reconcile.New(reconcile.Options{
		Repo:              *fl.repo,
		Remote:            *fl.remote,
		EpicID:            epicID,
		RunID:             *fl.runID,
		IntegrationBranch: *fl.branch,
		Owner:             "ticfac",
		Tracker:           tracker,
		NewExecutor:       executorFactory(*fl.runner, *fl.gate),
		Executors:         knownExecutors(),
		ExecStateRoot:     *fl.stateRoot,
		GateConfig:        *fl.gate,
		ProfileDir:        *fl.profiles,
		Tier:              *fl.tier,
		ReleaseOnly:       true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s: %v\n", epicID, err)
		return 1
	}

	var settled *reconcile.Settlement
	if *fl.carryWork {
		settled, err = reconciler.SettleCarry(context.Background(), tickID, attempt, *fl.release)
	} else {
		settled, err = reconciler.Settle(context.Background(), tickID, attempt, *fl.release)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s %s %d: %v\n", epicID, tickID, attempt, err)
		return 1
	}
	if *fl.asJSON {
		return emitSettleJSON(epicID, tickID, attempt, settled, stdout, stderr)
	}
	// The released attempt is named the way every line written for a person
	// names one (tick h58): the tick's own try first, the run-wide dispatch
	// number — the one this command was addressed by — labelled after it.
	released := reconcile.AttemptLabel(settled.TickID, settled.Try, settled.Attempt)
	if !settled.Recorded {
		fmt.Fprintf(stdout, "%s was already released by %s; nothing was written\n",
			released, settled.ReleasedBy)
		return 0
	}
	if settled.Carried {
		fmt.Fprintf(stdout, "%s (%s) is released by %s, recorded as decision %d of run %s, "+
			"CARRYING its work:\n"+
			"the next run dispatches a new attempt based on the released commits at %s, so the next worker "+
			"starts from them rather than redoing them. The gate still decides — nothing merges unproven — and "+
			"the new attempt's records state where its work came from.\n",
			released, settled.State, settled.ReleasedBy, settled.Decision, settled.RunID, settled.CarryRef)
		return 0
	}
	// Where the released work can be found, said plainly (ticfac tick 55i):
	// "stays on its own write ref" is only true when the ref reached the
	// remote, and a release that did not say which sent a person hunting for
	// work that was one worktree removal away from gone.
	switch {
	case settled.WorkSHA == "":
		fmt.Fprintf(stdout, "%s (%s) is released by %s, recorded as decision %d of run %s.\n"+
			"The next run dispatches a new attempt; this one left no commit beyond its base anywhere \u2014 \n"+
			"add --carry-work to have said otherwise.\n",
			released, settled.State, settled.ReleasedBy, settled.Decision, settled.RunID)
	case settled.WorkDurable:
		fmt.Fprintf(stdout, "%s (%s) is released by %s, recorded as decision %d of run %s.\n"+
			"The next run dispatches a new attempt; whatever this one committed is durable at %s on the remote \u2014 \n"+
			"add --carry-work to have the next attempt start from it instead.\n",
			released, settled.State, settled.ReleasedBy, settled.Decision, settled.RunID, settled.WorkRef)
	default:
		fmt.Fprintf(stdout, "%s (%s) is released by %s, recorded as decision %d of run %s.\n"+
			"The next run dispatches a new attempt. Whatever this one committed is NOT on the remote: the commits \n"+
			"are only the LOCAL branch %s in the checkout at %s — the worktree is gone and the teardown kept the \n"+
			"branch, so that checkout is the only place they exist.\n",
			released, settled.State, settled.ReleasedBy, settled.Decision, settled.RunID,
			settled.WorkRef, settled.WorkIn)
	}
	return 0
}

// settleJSON is `settle --json`'s answer, ticfac.settle.v1: the release a
// person made — attributed, recorded as the decision it landed as — and
// WHERE the released attempt's work lives, the facts the prose paragraphs
// say, as fields an agent can branch on without parsing them.
type settleJSON struct {
	agentDoc
	EpicID      string `json:"epic_id"`
	TickID      string `json:"tick_id"`
	Attempt     int    `json:"attempt"`
	Try         int    `json:"try"`
	ReleasedBy  string `json:"released_by"`
	State       string `json:"executor_state"`
	Recorded    bool   `json:"recorded"`
	Decision    int    `json:"decision"`
	RunID       string `json:"run_id"`
	Carried     bool   `json:"carried"`
	CarryRef    string `json:"carry_ref,omitempty"`
	WorkRef     string `json:"work_ref,omitempty"`
	WorkSHA     string `json:"work_sha,omitempty"`
	WorkDurable bool   `json:"work_durable"`
	WorkIn      string `json:"work_in,omitempty"`
}

// emitSettleJSON prints the one document. An already-released attempt is a
// done answer, not a failure: `recorded: false` is the fact a repeat caller
// reads, and the exit code stays 0 either way.
func emitSettleJSON(epicID, tickID string, attempt int, settled *reconcile.Settlement, stdout, stderr io.Writer) int {
	doc := settleJSON{
		agentDoc:    agentDoc{Schema: agentSchemaID("settle"), State: agentStateDone},
		EpicID:      epicID,
		TickID:      tickID,
		Attempt:     attempt,
		Try:         settled.Try,
		ReleasedBy:  settled.ReleasedBy,
		State:       settled.State,
		Recorded:    settled.Recorded,
		Decision:    settled.Decision,
		RunID:       settled.RunID,
		Carried:     settled.Carried,
		CarryRef:    settled.CarryRef,
		WorkRef:     settled.WorkRef,
		WorkSHA:     settled.WorkSHA,
		WorkDurable: settled.WorkDurable,
		WorkIn:      settled.WorkIn,
	}
	if err := emitAgentJSON(stdout, doc); err != nil {
		fmt.Fprintf(stderr, "ticfac settle %s %s %d: %v\n", epicID, tickID, attempt, err)
		return 1
	}
	return 0
}

// buildInfo is what `version --json` prints. The contract bundle is part of
// the answer: a consumer holding only this executable can ask which contracts
// it was built against, without a checkout.
type buildInfo struct {
	Schema           string `json:"schema"`
	Ticfac           string `json:"ticfac"`
	ContractBundle   string `json:"contract_bundle"`
	TicksRepository  string `json:"ticks_repository"`
	TicksRef         string `json:"ticks_ref"`
	TicksBundle      string `json:"ticks_bundle"`
	ContractsPinPath string `json:"contracts_pin"`
}

// newVersionCommand builds the cobra command for `version`, which reports
// this build and the contract bundle it serves — a different, richer answer
// than the --version flag fang feeds from the same Version variable.
func newVersionCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "report this build and the contract bundle it serves",
		Long:  "Report this build's version, the contract bundle ticfac authors, and the\nticks release the two ticks-owned contracts were vendored from — without\nneeding a checkout.",
	}
	asJSON := cmd.Flags().Bool("json", false, "print machine-readable output")
	cmd.RunE = func(c *cobra.Command, args []string) error {
		return codeToErr(version(args, asJSON, stdout, stderr))
	}
	return cmd
}

func version(args []string, asJSON *bool, stdout, stderr io.Writer) int {

	info, err := BuildInfo()
	if err != nil {
		fmt.Fprintf(stderr, "ticfac version: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(stderr, "ticfac version: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "ticfac %s\ncontract bundle %s (ticks contracts from %s@%s, ticks bundle %s)\n",
		info.Ticfac, info.ContractBundle, info.TicksRepository, info.TicksRef, info.TicksBundle)
	return 0
}

// BuildInfo reads the embedded pin and manifest. They are embedded, not read
// from disk, so what the binary reports and what it was compiled against
// cannot disagree.
//
// Two version claims travel in the answer, and they are different claims
// since the ownership split (tick 4i8): ContractBundle is TICFAC's bundle —
// the one every consumer pins by exact value — and TicksBundle is the ticks
// bundle the two vendored ticks-owned files (tk-json-manifest.json,
// tracker-layout.json) were fetched from. They coincide only by accident of
// history, so they are reported side by side rather than asserted equal.
func BuildInfo() (buildInfo, error) {
	var pin struct {
		BundleVersion string `json:"bundleVersion"`
		Repository    string `json:"repository"`
		Ref           string `json:"ref"`
	}
	if err := json.Unmarshal(ticfac.PinJSON, &pin); err != nil {
		return buildInfo{}, fmt.Errorf("the embedded contracts.pin.json is unreadable: %w", err)
	}
	var bundle struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(ticfac.BundleJSON, &bundle); err != nil {
		return buildInfo{}, fmt.Errorf("the embedded contracts/bundle.json is unreadable: %w", err)
	}
	if pin.Repository == "" || len(pin.Ref) != 40 {
		return buildInfo{}, fmt.Errorf(
			"the embedded contracts.pin.json names no immutable ticks ref: repository %q, ref %q",
			pin.Repository, pin.Ref)
	}

	return buildInfo{
		Schema:           agentSchemaID("version"),
		Ticfac:           Version,
		ContractBundle:   bundle.Version,
		TicksRepository:  pin.Repository,
		TicksRef:         pin.Ref,
		TicksBundle:      pin.BundleVersion,
		ContractsPinPath: "contracts.pin.json",
	}, nil
}
