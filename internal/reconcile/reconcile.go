package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// Defaults. The cadence numbers are contracts/lifecycle-invariants.json's own
// `harness.thresholds`, taken from there and not recomputed here: Appendix A #4
// says the relationship between the poll interval and the substrate's wipe
// threshold is pinned in ONE place, and a second copy of the numbers is exactly
// the arithmetic-in-two-files it forbids. TestTheDefaultCadenceIsTheFixtures
// asserts each of these against the fixture.
const (
	// DefaultWipeThreshold is the substrate's sleep/wipe threshold
	// (`wipe_threshold_ms`): how long a job may go unaddressed before whatever
	// is hosting it takes it away.
	DefaultWipeThreshold = 20 * time.Minute

	// DefaultPollInterval is the CLOUD substrate's cadence (`max_poll_ms`),
	// WELL under DefaultWipeThreshold — which is what makes the poll a
	// keepalive there rather than a status check: on a substrate that takes
	// an unaddressed job away, the beat IS the point. It is NOT laziness and
	// it is not a local number: a local substrate wipes nothing, so waiting
	// five minutes between local Inspects is five blind minutes for a job
	// that may have settled in seconds. The interval belongs to the EXECUTOR
	// (KnownExecutor.PollInterval, tick u9l); this constant is the default an
	// executor states no cadence of its own against, and the keepalive
	// reasoning stays exactly where the cloud needs it.
	DefaultPollInterval = 5 * time.Minute

	// DefaultStepCap is the host's cap on one step of a controller
	// (`step_cap_ms`).
	DefaultStepCap = 8 * time.Minute

	// DefaultWallSeconds bounds one job. An unbounded job is one nothing stops.
	DefaultWallSeconds = 3600

	// DefaultStallWarnAfter is how long an in-flight attempt may produce
	// nothing durable — its branch unmoved, its worktree unchanged — before
	// the run says so in the feed (tick 7zs). It is an EARLY WARNING, not a
	// bound: the wall clock still spends the attempt, and a worker thinking
	// hard legitimately commits nothing for a while. The Phase 3 run's
	// worker produced nothing for 40 of its 55 minutes; fifteen is early
	// enough that a person reading the line still has most of the attempt's
	// budget to spend.
	DefaultStallWarnAfter = 15 * time.Minute
)

// Tracker is the tracker surface the reconciler uses. It is exactly the tk
// client's, narrowed to the commands a run needs, so that *tk.Client satisfies
// it without an adapter and a test can drive the reconciler without a tracker
// binary on PATH.
type Tracker interface {
	Graph(ctx context.Context, epicID string) (tk.Graph, error)
	Show(ctx context.Context, tickID string) (tk.Tick, error)
	Claim(ctx context.Context, tickID, owner string) (tk.Tick, error)
	Note(ctx context.Context, tickID, text string) (tk.Tick, error)
	Close(ctx context.Context, tickID string) (tk.Tick, error)
}

// Executor is contracts/job-protocol.json's four operations plus the two local
// ones a host has to have. *subprocess.Executor satisfies it as it stands: the
// seam is the protocol's, and nothing here widens it.
type Executor interface {
	Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error)
	Inspect(handle *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error)
	CollectDetail(handle *subprocess.JobHandle) (*subprocess.Collection, error)
	Cancel(handle *subprocess.JobHandle) (*subprocess.CancelAck, error)
	Dispose(handle *subprocess.JobHandle, opts subprocess.DisposeOptions) error
}

// Substrate is the versioned substrate a dispatch's executor observed at the
// build that will run the job: its protocol version (herdr's API protocol,
// the number between the client's hard floor and its warn line) and the
// server version (the binary the substrate runs). It is executor-neutral on
// purpose — the reconciler learns a VERSION, never a substrate's addressing
// — and it rides the dispatch and the dispatch marker so a later leg, after
// the substrate was upgraded mid-run, still records the substrate the
// attempt was dispatched under rather than the one it would observe today.
//
// The zero value is a substrate with no versioned protocol — a local
// process — and its records state null in provenance.
type Substrate struct {
	Protocol      int
	ServerVersion string
}

// KnownExecutor is one executor this build can honour, as Options.Executors
// states it: the name a profile may name, the runner names it can launch (a
// herdr-style executor launches agent KINDS; the local one launches runner
// binaries), and whether it can tell each runner which model to use. The
// names are host configuration, passed in by the caller that can build the
// executors — the reconciler only compares against them.
type KnownExecutor struct {
	Name         string
	Runners      []string
	AcceptsModel func(runner string) bool

	// PollInterval is the cadence at which a live job on THIS executor is
	// addressed: the substrate's own truth, supplied by the executor rather
	// than by one global constant (tick u9l, epic av8). The five-minute
	// DefaultPollInterval is a CLOUD number — deliberately well under the
	// wipe threshold, because there the poll IS the keepalive and the beat is
	// the point. A local substrate wipes nothing: its interval is latency to
	// notice a settle, and it wants seconds. Zero means the executor states
	// no cadence of its own and the run-level PollInterval applies — the
	// default honoured set states one, because a cadence is a decision the
	// HOST makes when it wires the executors it can build (internal/cli), not
	// a property of the name alone.
	PollInterval time.Duration
}

// theExecutors is the honoured set, defaulting to the one executor this
// package builds itself.
func theExecutors(opts []KnownExecutor) []KnownExecutor {
	if len(opts) == 0 {
		return []KnownExecutor{{Name: subprocess.ExecutorName, Runners: subprocess.KnownRunners(), AcceptsModel: subprocess.RunnerAcceptsModel}}
	}
	return opts
}

// honoured answers the KnownExecutor a profile's executor name resolves to,
// and whether this build can honour it at all.
func honoured(opts []KnownExecutor, name string) (KnownExecutor, bool) {
	for _, known := range theExecutors(opts) {
		if known.Name == name {
			return known, true
		}
	}
	return KnownExecutor{}, false
}

// The two seams, asserted at COMPILE time. The tracker interface is the tk
// client's own shape and the executor interface is the protocol's four
// operations plus the two local ones — so a change to either that this package
// has not followed is a build failure here rather than a run that discovers it.
var (
	_ Tracker  = (*tk.Client)(nil)
	_ Executor = (*subprocess.Executor)(nil)
)

// Dispatch is everything one dispatch decides before the executor is asked for
// anything. It is passed to the executor factory because which executor serves
// a job, and where its state lives, is the HOST's decision and not the
// protocol's.
type Dispatch struct {
	RunID    string
	EpicID   string
	TickID   string
	Attempt  int
	JobID    string
	Role     string
	Repo     string
	Remote   string
	WriteRef string
	BaseSHA  string

	// StateDir is a directory private to THIS dispatch. The reconciler assigns
	// it so that a restarted run — on a fresh clone, holding nothing but the
	// dispatch marker it reads from origin — can find the attempt the previous
	// one started without guessing at an executor's internal naming.
	StateDir string

	// BudgetUSD is the EFFECTIVE budget: already clamped, because a job is
	// issued the number that will govern (Appendix A #12).
	BudgetUSD *float64

	// Tier is the capability tier this dispatch was DERIVED under (tick 5eq) —
	// the rung of the [tier_policy] ladder that routed the profile, "" when
	// no tier was derived (no policy, base values). It rides on the marker for
	// the executor's benefit and in provenance for the audit's: a run that
	// spent at a high tier says so in its own records, and since bundle 4.0.0
	// that statement is a field of the closed provenance object, not just a
	// fact implied by the tier-resolved profile digest.
	Tier string

	// Profile is the resolved role profile this dispatch is made under —
	// executor, runner, model and prompt, and nothing else (SPEC §4.5). The
	// factory reads the RUNNER off it, because which agent CLI serves a role is
	// executor configuration and not a field of the closed protocol records.
	Profile *profile.Profile

	// Executor is the executor this dispatch RUNS through. At dispatch time
	// it is the profile's own (the marker is built from the same value); on
	// every later leg it is the MARKER's, and that is the difference from
	// Profile: the profile a restart re-resolves is the one it would dispatch
	// with TODAY, while the executor that actually runs this attempt is a
	// fact about the attempt that the marker froze (tick d6s). Provenance
	// reads it from here, never from Profile — Tier and Substrate below are
	// stated off the marker for exactly this reason, and an executor a
	// record names that the attempt never ran on is provenance that lies.
	Executor string

	// Substrate is the versioned substrate this dispatch's executor observed
	// at the build that runs it — protocol and server version. The executor
	// factory reports it when the executor is built; it rides the dispatch so
	// every record the dispatch produces can state it in provenance, and the
	// marker so a LATER leg (after an upgrade) records the substrate the
	// attempt was DISPATCHED under, not the one it would observe today. The
	// zero value is a substrate that states no protocol — a local process.
	Substrate Substrate

	// ResumedFrom states that this dispatch starts from the work of a RELEASED
	// attempt — a person's --carry-work settlement (tick 0z0): which attempt,
	// the ref its work is on, the commit this dispatch was cut from, and who
	// released it. It is the ANSWER to the question a resumed run's records
	// have to survive — "this work came from a released attempt" — in two
	// halves: the explicit claim rides the marker's open handle (the closed
	// provenance object has no field for it), and the closed fields that
	// already say it are stated too — a carried dispatch's source_ref and
	// source_sha ARE the released attempt's ref and commit. Nil when this
	// dispatch resumed from nothing.
	ResumedFrom *resumedFrom

	// PriorReports are the archived reports of this tick's EARLIER attempts
	// (tick nvn), newest first — the analysis a re-dispatched attempt is shown
	// in its prompt instead of starting blind. The reports are re-derived
	// from the executor state directory at every dispatch rather than carried
	// on the marker: they are facts about the attempts the state directory
	// already holds, and the marker — which reaches origin, a public
	// repository — never carries host paths.
	PriorReports []subprocess.PriorReport
}

// carriedWork is a released attempt whose WORK the next dispatch of its tick
// starts from, with the settlement that said so: the marker identifies the
// attempt and the branch its commits are on, and by/at name the person who
// released it and when — carried onto the dispatch's records so "who sent
// this work forward" is not a fact the run has to re-derive.
type carriedWork struct {
	marker attemptHandle
	by, at string
}

// Options configure a reconciler. Everything it talks to is passed in rather
// than reached for, so the behaviour under test is the behaviour that ships.
type Options struct {
	// Repo is the checkout this reconciler works in. It is where attempts
	// branch from and where the merge worktree is created.
	Repo string

	// Remote is the durable authority: run state, the integration branch and
	// the attempts' work all live there. Default "origin".
	Remote string

	// EpicID is the one epic this run is about. There is no multi-epic here.
	EpicID string

	// RunID names the run and its `.ticfac/runs/<run-id>/` directory. Empty
	// derives it from the epic, so that a restart on a fresh clone reads the
	// same run without being told which one it was.
	RunID string

	// IntegrationBranch is the EpicRun integration branch. Empty is
	// `epic/<epic-id>`.
	IntegrationBranch string

	// BaseRef is what the integration branch is cut from when it does not
	// exist yet. Default "HEAD".
	BaseRef string

	// Owner is who claims a tick in the tracker.
	Owner string

	Tracker Tracker

	// NewExecutor builds the executor for one dispatch, and reports the
	// SUBSTRATE it observed at the build: the protocol and server version of
	// the thing the executor drives, stated in the dispatch's provenance so a
	// run that spans a substrate upgrade can be diagnosed from the durable
	// record rather than from the executor's private state (tick to1, epic
	// av8). A substrate with no versioned protocol — a local process —
	// reports the zero Substrate, and its records state null.
	NewExecutor func(Dispatch) (Executor, Substrate, error)

	// Executors is what this build can honour: every executor name a
	// resolved profile may name, each with the runner names it can launch
	// and whether it can tell each runner which model to use. A profile
	// naming an executor outside this list is a refusal at construction —
	// not three ticks into an epic — because a profile naming an executor
	// the run did not use is provenance that lies. Empty is the one executor
	// this package builds itself: the local subprocess executor.
	Executors []KnownExecutor

	// ExecStateRoot is the HOST-level directory dispatch state directories are
	// created under. It is deliberately not inside the repository: a restart
	// from a fresh clone finds the previous run's attempts through it.
	ExecStateRoot string

	// GateConfig is the runners.toml the integrated gate is read from. Empty
	// is `<repo>/.tick/runners.toml`.
	GateConfig string

	// RepoConfig is the target repository's own config — `.tick/config.md` —
	// from which the run reads the rules the repository declares for itself.
	// Today that is one rule: the PR + CI close-out gate (tick 0iz), the
	// precondition a close-out phase is admitted against. Empty is
	// `<repo>/.tick/config.md`; a repository that carries no config.md
	// declares no rule, the way runners.toml's gate reader treats a missing
	// file as no gate rather than inventing one.
	RepoConfig string

	// PullRequests is the code-hosting surface behind the PR + CI close-out
	// rule: find the epic PR, open one, read CI on it. It is passed in rather
	// than reached for for the same reason every other surface is — the
	// behaviour under test is the behaviour that ships — and nil is legal
	// ONLY where the target repo declares no close-out rule: a repository
	// that declares one and a build with no surface behind it is refused at
	// construction, before anything is claimed, because a close-out whose
	// precondition nothing can check is one a whole run of work discovers it
	// cannot complete only at the end.
	PullRequests forge.PullRequests

	// GateTimeout bounds one gate command.
	GateTimeout time.Duration

	// PollInterval is how often a live job is addressed when its executor
	// states no cadence of its own. On a substrate that wipes, it IS the
	// keepalive, so it must stay well under WipeThreshold; on one that does
	// not, the executor supplies seconds through KnownExecutor.PollInterval
	// and this is only the fallback.
	PollInterval time.Duration

	// WipeThreshold is the substrate's: how long a job may go unaddressed.
	WipeThreshold time.Duration

	// StepCap bounds one leg of a long wait.
	StepCap time.Duration

	// WallSeconds bounds one job.
	WallSeconds int

	// StallWarnAfter is how long an in-flight attempt may produce nothing
	// durable — its branch unmoved, its worktree unchanged — before the run
	// writes one feed line about it (tick 7zs). Zero is the default
	// (DefaultStallWarnAfter); negative disables the warning. It is a hint
	// about when to look, never a verdict: it stops, rejects and holds
	// nothing, and the wall clock — not this — is the bound that spends the
	// attempt.
	StallWarnAfter time.Duration

	// BudgetUSD is what an operator asked for, and CeilingUSD is what the
	// deployment allows. The effective number is what is issued AND what is
	// reported.
	BudgetUSD  float64
	CeilingUSD float64

	// ProfileDir is the profiles directory role profiles are resolved from.
	// Empty is the copy compiled into this binary, which is the production
	// path: a profile read off disk at run time could disagree with the binary
	// beside it, and the profile is what every record's provenance cites.
	ProfileDir string

	// Tier selects a `[roles.<name>.tiers.<tier>]` overlay in the target
	// repository's runner configuration. Empty applies none; a tier that
	// configuration does not declare is refused at construction.
	Tier string

	Now func() time.Time

	// Sleep is how the reconciler waits between polls. A test replaces it with
	// a clock advance, so that a cadence measured in minutes is testable in
	// milliseconds without the cadence itself being a test-only number.
	Sleep func(time.Duration)

	// guardsOff disables one named guard, for the invariants suite's negative
	// control. Names are contracts/lifecycle-invariants.json's guard names.
	guardsOff map[string]bool

	// stopAfter kills this reconciler the moment a named stage is reached. It
	// exists for the restart tests, which have to cut the run at a point a
	// crash could genuinely land on — between a dispatch and its record,
	// between a collect and a close — and see the next incarnation pick it up
	// from durable state alone.
	stopAfter func(Event) bool
}

// Reconciler runs one epic.
type Reconciler struct {
	opts   Options
	git    *repoGit
	store  *runstate.Store
	runID  string
	branch string

	base    string
	baseRef string

	// tracker is the tracker as this run uses it: pointed at a worktree on the
	// integration branch, and pushing every write. It is built in Run, because
	// the worktree it runs in is the integration branch's head — which does not
	// exist until Run has made sure the branch does.
	tracker     Tracker
	trackerTree *trackerTree

	gate       GateCommands
	gateDigest string

	// closeoutRule is what the target repository's own .tick/config.md
	// declares about how an epic integrates (tick 0iz). The zero value — a
	// repo that declares no rule — admits close-out as it always did.
	closeoutRule CloseoutRule

	// profiles is the resolved role profile per role, and profileSet digests
	// all of them together: a checkpoint is not about one role, so it names the
	// SET the run was made under.
	profiles   map[string]*profile.Profile
	profileSet string

	pollInterval  time.Duration
	wipeThreshold time.Duration
	stepCap       time.Duration

	// executors is the honoured set, kept past construction so a wait can
	// resolve the cadence of the executor the dispatch's MARKER names — the
	// interval belongs to the executor, not to one global constant (tick u9l).
	executors []KnownExecutor

	// feed is the run event stream a non-participant subscribes to, and
	// feedErr is the first error appending to it — recorded once, never
	// fatal: the feed is a hint about when to look, and a run that cannot be
	// watched is still a run (contracts/run-event-feed.json).
	//
	// feedPrepared guards the ONE thing a run must do before it writes
	// `.ticfac/` into a repository's working tree: install the run-state
	// contract's gitignore fragment, idempotently, so the exhaust is exhaust
	// and not a dirty tree a later `git add -A` sweeps up. A repository that
	// already carries the fragment is left byte for byte alone.
	feed         *runfeed.Feed
	feedErr      error
	feedPrepared bool

	now   func() time.Time
	sleep func(time.Duration)

	guardsOff map[string]bool

	// The lifecycle machinery. Every one of these is read by Run.
	step       *Step
	lastPolled map[string]time.Time
	liveness   map[string]string
	holds      map[string]*hold
	budget     Budget
	evidence   map[string]Fingerprint
	published  []string

	sequence int
	ticks    []runstate.TickState
	journal  []Event
	failure  *Refusal

	// The tier policy half of tick 5eq: the declared mapping from tick facts
	// to a dispatch tier, the tier-resolved profiles it can route to, and
	// the two run-level numbers its records quote. pinnedTier is the
	// operator's --tier: when set, every dispatch runs at it and the ladder
	// does not run. hostWidth is [orchestration].max_parallel — the ONE
	// host-wide number the per-tier bounds narrow.
	tierPolicy   *runconfig.TierPolicy
	tierProfiles map[string]map[string]*profile.Profile
	pinnedTier   string
	hostWidth    int
}

// Event is one thing the run did, in order. It is what makes "the gate ran
// before the tick was closed, and the attempt was cleaned up after" an
// assertion rather than a hope.
type Event struct {
	At     time.Time
	Tick   string
	Stage  string
	Detail string
}

// The stages a tick passes through. They are named because the ORDER is the
// contract: a close before the gate is a close nothing stands behind, and a
// cleanup before the close throws away the only copy of what was closed.
const (
	StageRefreshed    = "base_refreshed"
	StageSkipped      = "skipped"
	StageHeld         = "held"
	StageClaimed      = "claimed"
	StageDispatched   = "dispatched"
	StageAdopted      = "adopted"
	StageWaiting      = "waiting"
	StageCollected    = "collected"
	StageRejected     = "rejected"
	StageIntegrated   = "integrated"
	StagePublished    = "published"
	StageGatePassed   = "gate_passed"
	StageGateFailed   = "gate_failed"
	StageStale        = "stale_evidence"
	StageClosed       = "closed"
	StageRedispatched = "redispatched"
	StageCleanedUp    = "cleaned_up"
	StageResumed      = "resumed"
	StageSettled      = "settled"
	StageRunFinished  = "run_finished"
	// StageBudgetSet is the effective budget, said at ADMISSION while the run
	// can still be cancelled cheaply. It is NOT run_finished: a subscriber to
	// the run feed must not be told the run ended seconds after it started,
	// which is exactly what the feed, `ticfac events --follow` and the worker
	// guidance all exist to avoid.
	StageBudgetSet = "budget_set"
	// StagePolicyStated is a run-level statement of how the run WILL behave,
	// made at admission: the tier pin, the rate-limit stance, a per-tier wave
	// width, the wave-composition rule. Like StageBudgetSet it is NOT
	// run_finished. Four of these were written as run_finished, so a run that
	// declared a [tier_policy] or touch: labels told every feed subscriber it
	// had ended before it dispatched anything — the budget line's defect again,
	// in the lines the feed test's fixture never exercised.
	StagePolicyStated = "policy_stated"
	// StageRunDied is the terminal line for a run that did NOT reach its own
	// run_finished: the process returned an operational error, panicked, or
	// was stopped by a signal. It is written by run-epic around the
	// reconciler, so a death is never a feed that simply stops on an ordinary
	// success line (tick wdb; Phase 3 acceptance A4).
	StageRunDied = "run_died"
	// StageTierDerived is the record of one dispatch's tier DERIVATION —
	// the pure function's answer and reason, written before the tick is
	// claimed, so "why was this expensive" is a question the run's own
	// record answers.
	StageTierDerived = "tier_derived"

	// The findings channel (tick 7vn): one stage for the first draft of a
	// finding, one for every repeat — a repeat proposes nothing new, and the
	// journal says so rather than falling silent, because a dedup nobody
	// can see is indistinguishable from a channel that drops findings.
	StageFindingFiled     = "finding_filed"
	StageFindingDuplicate = "finding_duplicate"

	// StageStartFailed is the line a failed Start leaves (tick d6s): an
	// attempt whose marker is on origin but never started. It is recorded
	// at TICK scope, carrying the attempt it is about, so a feed reader who
	// has just seen the claim learns the dispatch failed rather than reading
	// silence as a run that is still going. The verdict stays with the
	// refusal returned to the caller — the line says when to look, never
	// what happened.
	StageStartFailed = "start_failed"

	// StageRunHeld is the line a run owes a person: it stopped holding one
	// tick for a decision only a person can make (an attempt nobody can
	// address, an attempt rejected with its work unmerged, a worker that
	// answered BLOCKED, a struck-out unit) and cannot proceed without one
	// (tick 0z0). A held attempt is correct and must stay; what makes it a
	// stall is that nobody is told — so the run TELLS the feed, at TICK scope
	// so the line carries the tick and the attempt it is about, and the
	// refusal's own reason leads the detail so a watcher surfaces WHY without
	// matching on a sentence. The distinct stage is the machine fact; the
	// detail is the person's half.
	StageRunHeld = "run_held"

	// StageCarried is the line a dispatch cut from a RELEASED attempt's work
	// leaves (tick 0z0): a person released the attempt with --carry-work, so
	// the new attempt starts from the released commits rather than redoing
	// them. It is recorded once, on the dispatch that actually happened — not
	// in the planning half, which a dispatch conflict can run twice.
	StageCarried = "carried"

	// StagePROpened is the line the run leaves when the epic PR exists — the
	// one it opened, or the one a previous incarnation opened that this one
	// found (tick 0iz). It is recorded at the CLOSE-OUT tick's scope, so the
	// feed line carries the tick whose admission the PR is a precondition
	// of, and it is the run that opens the PR, never the close-out worker's
	// diligence.
	StagePROpened = "pr_opened"

	// StageCloseoutAdmitted is the line the close-out admission leaves when
	// the precondition the target repo declares is met: the epic PR is open
	// and CI is green on it, so the close-out phase is admitted (tick 0iz).
	// The phase it admits then proceeds through the ordinary stages.
	StageCloseoutAdmitted = "closeout_admitted"

	// StageCloseoutHeld is the line the close-out admission leaves while it
	// holds the phase: CI is pending on the epic PR. Which half of the
	// precondition is unmet is in the detail when a refusal follows, and the
	// typed refusal carries the verdict — the line says when to look, never
	// what happened (tick 0iz).
	StageCloseoutHeld = "closeout_held"

	// StageWallClock is the line a bound's firing owes the feed (tick emk):
	// the wall clock fired and the attempt has NOT settled, which is the
	// moment the run stops making progress on its own — the moment a
	// watcher's attention is worth asking for while there is still an agent
	// to stop. It is written by the wait, once per tick, from the reconciler's
	// own bound (the durable marker's issue time plus WallSeconds — the same
	// arithmetic the settlement deadline starts from), and it carries the
	// executor's last observation so the reader is sent at what the
	// substrate was seen doing, not at a guess. The stop itself is the
	// executor's to make (the local supervisor kills; the herdr executor
	// delivers herdr's interrupt); this line only says the bound fired and
	// the attempt is still unresolved — a hint about when to look, exactly
	// like every other feed line, never a verdict.
	StageWallClock = "wall_clock_fired"

	// StageStallWarned is the early warning before the bound (tick 7zs): the
	// attempt is alive — liveness was never in question — and it has produced
	// nothing durable for longer than the run's stall threshold: its branch
	// has not moved and its worktree has not changed. It is written by the
	// wait, once per tick, from facts read out of the repo itself (the branch
	// tip's committer date, the newest file mtime under the worktree), and it
	// is a reason to LOOK, never a verdict: a worker thinking hard
	// legitimately commits nothing for a while, the line stops and rejects
	// nothing, and the wall clock — not this — is the bound that spends the
	// attempt. The Phase 3 run knew its worker was alive for 55 minutes and
	// had no way to know it had produced nothing for 40 of them; a person
	// caught it by reading the pane.
	StageStallWarned = "stall_warned"
)

// New prepares a reconciler. It makes no network call and starts nothing: a
// run that half-starts is the failure Appendix A was written out of, so
// everything that can refuse refuses here.
func New(opts Options) (*Reconciler, error) {
	if opts.Repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.Repo = wd
	}
	if opts.EpicID == "" {
		return nil, fmt.Errorf("reconcile: no epic id: this reconciler runs one epic")
	}
	if opts.Tracker == nil {
		return nil, fmt.Errorf("reconcile: no tracker: the epic graph is read through the tk client")
	}
	if opts.NewExecutor == nil {
		return nil, fmt.Errorf("reconcile: %s: nothing implements start/inspect/cancel/collect", NoExecutorMessage)
	}
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	if opts.BaseRef == "" {
		opts.BaseRef = "HEAD"
	}
	if opts.Owner == "" {
		opts.Owner = "ticfac"
	}
	if opts.RunID == "" {
		opts.RunID = "epic-" + opts.EpicID
	}
	if opts.IntegrationBranch == "" {
		opts.IntegrationBranch = "epic/" + opts.EpicID
	}
	if opts.GateConfig == "" {
		opts.GateConfig = filepath.Join(opts.Repo, ".tick", "runners.toml")
	}
	if opts.RepoConfig == "" {
		opts.RepoConfig = repoConfigPath(opts.Repo)
	}
	if opts.GateTimeout <= 0 {
		opts.GateTimeout = 30 * time.Minute
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.WipeThreshold <= 0 {
		opts.WipeThreshold = DefaultWipeThreshold
	}
	if opts.StepCap <= 0 {
		opts.StepCap = DefaultStepCap
	}
	if opts.WallSeconds <= 0 {
		opts.WallSeconds = DefaultWallSeconds
	}
	if opts.StallWarnAfter == 0 {
		opts.StallWarnAfter = DefaultStallWarnAfter
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = time.Sleep
	}
	if opts.ExecStateRoot == "" {
		opts.ExecStateRoot = filepath.Join(subprocess.DefaultStateDir(), "runs")
	}

	// Appendix A #4, refused at construction rather than discovered at run
	// time: a cadence at or over the substrate's threshold is not a keepalive,
	// and a run configured with one loses jobs it believes are alive.
	if opts.PollInterval*2 > opts.WipeThreshold {
		return nil, fmt.Errorf("reconcile: a poll interval of %s leaves no margin under the substrate's wipe "+
			"threshold of %s: polling IS the keepalive, and this cadence is not one",
			opts.PollInterval, opts.WipeThreshold)
	}

	// The same reasoning, per executor that supplies a cadence of its own
	// (tick u9l): the interval belongs to the executor now, but wherever a
	// wipe threshold stands, an interval that leaves no margin under it is
	// not a keepalive — the guard follows the number, not the constant. A
	// local executor's seconds clear a 20-minute threshold trivially; a
	// cloud executor declaring the same seconds against a short threshold
	// is the configuration error this exists to refuse.
	for _, known := range theExecutors(opts.Executors) {
		if known.PollInterval > 0 && known.PollInterval*2 > opts.WipeThreshold {
			return nil, fmt.Errorf("reconcile: executor %s declares a poll interval of %s, which leaves no margin "+
				"under the substrate's wipe threshold of %s: polling IS the keepalive, and this cadence is not one",
				known.Name, known.PollInterval, opts.WipeThreshold)
		}
	}

	gate, err := ReadGateCommands(opts.GateConfig)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	if len(gate) == 0 {
		return nil, fmt.Errorf("reconcile: %s declares no [testing.commands]: there is no integrated gate to run, "+
			"and closing a tick behind a gate that does not exist is a close nothing stands behind", opts.GateConfig)
	}

	// The close-out rule (tick 0iz), read from the repository's own config
	// rather than hardcoded — the way the gate above is read from
	// runners.toml. A repository that declares the rule and a build with no
	// code-hosting surface behind it is refused HERE, at construction, for
	// the same reason an unusable profile is: the precondition cannot be
	// checked, and discovering that three ticks into an epic — or, worse, at
	// the close-out the rule governs — is the failure this tick exists to
	// remove. The refusal names the fix an operator can make before the run
	// starts.
	rule, err := ReadCloseoutRule(opts.RepoConfig)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	if rule.Declared && opts.PullRequests == nil {
		return nil, fmt.Errorf("reconcile: %s declares the PR + CI close-out rule — %q — and this build has no "+
			"code-hosting surface configured to open or read the epic PR: set %s (the GitHub surface reads it), or "+
			"run against a host that provides one",
			opts.RepoConfig, rule.Stated, forge.TokenEnv)
	}

	// The role profiles, resolved BEFORE anything is dispatched. A profile that
	// does not exist, names an executor this phase does not have or a runner
	// this host cannot launch is a refusal here — three ticks into an epic is
	// not when a run should discover it.
	profiles, err := profile.ResolveAll(profile.Options{
		Dir: opts.ProfileDir, RunnersConfig: opts.GateConfig, Tier: opts.Tier,
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	for _, role := range profile.Roles {
		if err := usableProfile(opts.Executors, profiles[role]); err != nil {
			return nil, fmt.Errorf("reconcile: %w", err)
		}
	}

	// The tier policy (tick 5eq), read from the same runners.toml through the
	// same validated reader — the execution half's config, which is where a
	// policy belongs now the roles and tiers tables are ticfac's. It is
	// loaded BEFORE anything is dispatched so that a policy naming a tier the
	// target repo's roles table declares nothing for is a refusal HERE, at
	// construction, and not three ticks into an epic. An operator's --tier
	// pins the run instead: every dispatch runs at it and the ladder does
	// not run.
	r := &Reconciler{ // assembled early: the tier machinery below hangs off it
		opts:   opts,
		runID:  opts.RunID,
		branch: opts.IntegrationBranch,
	}
	r.pinnedTier = opts.Tier
	if opts.Tier == "" {
		cfg, err := runconfig.Load(opts.GateConfig)
		if err != nil {
			return nil, fmt.Errorf("reconcile: %w", err)
		}
		r.tierPolicy = cfg.TierPolicy
		r.hostWidth = cfg.MaxParallel()
		// Per-role, because a policy is only honest when what it
		// pre-resolves is what it can actually derive (profiles.go):
		// pre-resolving the work default for a review role would demand an
		// overlay the operator was never asked to declare.
		r.tierProfiles = make(map[string]map[string]*profile.Profile, len(profile.Roles))
		for _, role := range profile.Roles {
			perRole := map[string]*profile.Profile{"": profiles[role]}
			tiers, err := r.derivableTiers(role)
			if err != nil {
				return nil, fmt.Errorf("reconcile: %w", err)
			}
			for tier := range tiers {
				resolved, err := profile.Resolve(role, profile.Options{
					Dir: opts.ProfileDir, RunnersConfig: opts.GateConfig, Tier: string(tier),
				})
				if err != nil {
					return nil, fmt.Errorf("reconcile: %w", err)
				}
				if err := usableProfile(opts.Executors, resolved); err != nil {
					return nil, fmt.Errorf("reconcile: %w", err)
				}
				perRole[string(tier)] = resolved
			}
			r.tierProfiles[role] = perRole
		}
	}

	g := &repoGit{dir: opts.Repo, name: "ticfac", email: "ticfac@example.com", remote: opts.Remote}
	if _, err := g.run("", "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("reconcile: %s is not a git repository: %w", opts.Repo, err)
	}

	// The reconciler was assembled above the git check (the tier machinery
	// hangs off it); everything the git check guards joins here.
	r.git = g
	r.gate = gate
	r.gateDigest = gate.Digest()
	r.closeoutRule = rule
	r.profiles = profiles
	r.profileSet = profileSetDigest(profiles)
	r.pollInterval = opts.PollInterval
	r.wipeThreshold = opts.WipeThreshold
	r.stepCap = opts.StepCap
	r.executors = theExecutors(opts.Executors)
	r.feed = runfeed.Open(opts.Repo, opts.RunID)
	r.now = opts.Now
	r.sleep = opts.Sleep
	r.guardsOff = opts.guardsOff
	r.lastPolled = map[string]time.Time{}
	r.liveness = map[string]string{}
	r.holds = map[string]*hold{}
	r.evidence = map[string]Fingerprint{}
	if r.guardsOff == nil {
		r.guardsOff = map[string]bool{}
	}
	return r, nil
}

// NoExecutorMessage is the fail-closed refusal a build with nothing behind the
// four-operation protocol gives. It is asserted by a test: a silent or
// differently-worded refusal is what an operator misreads as a run that
// started.
const NoExecutorMessage = "no executor configured"

// RunID is the run this reconciler reads and writes.
func (r *Reconciler) RunID() string { return r.runID }

// IntegrationBranch is the EpicRun branch this run integrates on.
func (r *Reconciler) IntegrationBranch() string { return r.branch }

// Journal is what the run did, in order.
func (r *Reconciler) Journal() []Event { return append([]Event{}, r.journal...) }

// FeedError is the first error the run event feed produced, if it produced
// one. The feed is exhaust and a hint — a run whose feed cannot be written is
// a run nobody can watch, not a run that cannot run — so the error is
// collected here AND carried on the run's Result (tick d6s): collected alone,
// nothing ever read it, and a feed failure nobody can see is
// indistinguishable from a feed with nothing in it.
func (r *Reconciler) FeedError() error { return r.feedErr }

func (r *Reconciler) record(tick, stage, format string, args ...any) {
	event := Event{At: r.now(), Tick: tick, Stage: stage, Detail: fmt.Sprintf(format, args...)}
	r.journal = append(r.journal, event)
	r.emit(event)
	if r.opts.stopAfter != nil && r.opts.stopAfter(event) {
		panic(stopped{At: event})
	}
}

// emit appends one journal event to the run event feed — the append-only
// JSONL stream at .ticfac/logs/<run-id>/events.jsonl that a non-participant
// subscribes to instead of sleeping blind against the run or polling its
// durable records (contracts/run-event-feed.json, tick u9l).
//
// The feed is a HINT, and nothing here treats it as more: the line says when
// to LOOK, never what happened, and the verdict stays with the durable
// evidence the collect reads. That is also why an append failure is
// collected and not raised — a feed nobody can watch must not take the run
// with it, and a lost line is harmless by design.
func (r *Reconciler) emit(event Event) {
	if r.feed == nil {
		return
	}
	if !r.feedPrepared {
		// The feed is the first thing a run writes inside the repository's
		// own working tree, so it comes with the one obligation the run-state
		// contract attaches to `.ticfac/`: the gitignore fragment that makes
		// the logs exhaust rather than dirt. Idempotent — a compliant
		// repository is left alone — and a failure is a feed failure, never
		// a run failure, for the same reason as the append below.
		r.feedPrepared = true
		if _, err := runstate.EnsureGitignore(r.opts.Repo); err != nil && r.feedErr == nil {
			r.feedErr = fmt.Errorf("prepare the run feed's repository: %w", err)
		}
	}
	line := runfeed.NewEvent(event.At, r.runID, event.Tick, r.attemptOf(event.Tick), event.Stage, event.Detail)
	if err := r.feed.Append(line); err != nil && r.feedErr == nil {
		r.feedErr = err
	}
}

// attemptOf answers the attempt an event about a tick belongs to, from the
// tick state the run maintains: null before the tick's first dispatch is
// recorded — an event that belongs to no attempt yet is honestly null, not
// zero, because attempt 0 is an attempt that was never dispatched.
func (r *Reconciler) attemptOf(tick string) *int {
	if tick == "" {
		return nil
	}
	for i := range r.ticks {
		if r.ticks[i].TickID == tick && r.ticks[i].Attempt > 0 {
			attempt := r.ticks[i].Attempt
			return &attempt
		}
	}
	return nil
}

// pollIntervalFor resolves the cadence a live job on one executor is
// addressed at: the executor's own if it states one, else the run's. The
// interval belongs to the executor (tick u9l) — local substrates want
// seconds, a cloud sandbox wants the slow keepalive beat — and the executor a
// dispatch ran under is the one its marker names, so a run that mixes
// executors across roles keeps each cadence honestly.
func (r *Reconciler) pollIntervalFor(executor string) time.Duration {
	for _, known := range r.executors {
		if known.Name == executor && known.PollInterval > 0 {
			return known.PollInterval
		}
	}
	return r.pollInterval
}

// stopped is the simulated kill. It is a panic rather than an error because a
// process that dies does not unwind: nothing after the cut runs, no deferred
// cleanup happens, and whatever the next incarnation knows it reads from
// origin.
type stopped struct{ At Event }

// Stages returns the stages one tick passed through, in order.
func (r *Reconciler) Stages(tick string) []string {
	out := []string{}
	for _, e := range r.journal {
		if e.Tick == tick {
			out = append(out, e.Stage)
		}
	}
	return out
}

// ------------------------------------------------------------------ run ---

// Result is what the run concluded.
type Result struct {
	RunID    string
	EpicID   string
	State    runstate.State
	Reason   string
	Ticks    []runstate.TickState
	Closed   []string
	Rejected []string

	// Failure is the refusal that stopped the run, with the REASON as its own
	// value. Reading it is how a caller tells a failing gate from a boundary
	// violation without matching on prose.
	Failure *Refusal

	// FeedError is the run event feed's own write failure, if it had one.
	// What it MEANS (tick d6s): not a verdict about the work — the durable
	// records and this result are the evidence, and a run whose feed cannot
	// be written settles exactly as a watched one does — but the run has no
	// feed: nothing will appear under .ticfac/logs/<run-id>/, and a
	// subscriber (`ticfac events <run-id> --follow`) would wait forever on a
	// file that will never appear. So it is surfaced here rather than
	// collected and never read, and the run is NOT failed over it.
	FeedError error
}

// Run reconciles the epic until there is nothing dispatchable left, or until
// something refuses.
//
// It is restart-safe by construction: everything it needs is either on origin
// under `.ticfac/`, in the tracker, or in the executor's own durable state, and
// every effect is preceded by the compare-and-swap that proves it has not
// already happened.
func (r *Reconciler) Run(ctx context.Context) (*Result, error) {
	// What the last incarnation was killed in the middle of. Every worktree
	// this package makes is removed by a defer, and a killed process runs no
	// defer: the directories are gone with the temp filesystem, and only the
	// registrations git keeps for them are left (git.go).
	if pruned, err := r.git.pruneWorktrees(); err != nil {
		return nil, fmt.Errorf("reconcile: prune the worktrees a previous incarnation registered: %w", err)
	} else if pruned != "" {
		r.record("", StageResumed, "pruned worktree registrations a previous incarnation left behind: %s",
			firstLine(pruned))
	}

	// The integration branch has to exist before anything can be recorded
	// about the run: the run-state store's authority is origin's copy of it.
	base, err := r.git.ensureRemoteBranch(r.branch, r.opts.BaseRef)
	if err != nil {
		return nil, fmt.Errorf("reconcile: prepare the integration branch %s: %w", r.branch, err)
	}
	r.base, r.baseRef = base, refFor(r.branch)

	// The tracker's own checkout: a DETACHED worktree on the integration
	// branch, so that a claim, a note and a close are records this run can
	// commit and push rather than uncommitted edits in a checkout on main.
	// Every write through r.tracker publishes before it returns.
	tree, err := openTrackerTree(r.git, r.opts.Remote, r.branch, r.runID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: prepare the tracker's worktree on %s: %w", r.branch, err)
	}
	defer tree.close()
	relocated, ok := relocate(r.opts.Tracker, tree.dir)
	if !ok {
		return nil, fmt.Errorf("reconcile: the tracker cannot be pointed at %s: it would write its records into "+
			"%s, where they are uncommitted edits on whatever that checkout has out — and a tracker record that is "+
			"not on %s is one the next wave's worker cannot read", tree.dir, r.opts.Repo, r.opts.Remote)
	}
	r.trackerTree = tree
	r.tracker = &durableTracker{inner: relocated, tree: tree, r: r}

	store, err := runstate.Open(runstate.Options{
		Repo: r.opts.Repo, Remote: r.opts.Remote, Branch: r.branch, RunID: r.runID, Now: r.now,
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	r.store = store
	if _, err := store.Fetch(); err != nil {
		return nil, fmt.Errorf("reconcile: read the run state: %w", err)
	}

	// Recovery is a fetch and then a read. A checkpoint that is FINISHED is a
	// run somebody completed or cancelled; replaying it must not restart it.
	//
	// A FAILED one is a different thing, and reading it as finished was a hole
	// this repair closed. A run fails because one tick did not pass — the gate
	// refused, the worker answered BLOCKED, the attempt left nothing — and the
	// repair for every one of those is a person doing something (closing the
	// blocker, fixing the check) and running the epic again. The run is
	// therefore RESUMABLE: the same run id, the same integration branch, the
	// same attempt numbering, and the spent attempt redispatched by
	// claimDispatch as a new one. Refusing to resume it forced an operator to
	// invent a new --run-id, which starts a run whose attempt numbers and
	// evidence keys have no relationship to the records of the one it is
	// continuing.
	//
	// What makes this safe is that nothing here trusts the checkpoint's own
	// account of a tick: the tracker is asked whether each tick is closed, the
	// dispatch marker on origin decides whether an attempt is adopted, and the
	// evidence record decides whether a gate has to run again. The checkpoint
	// is where the run stopped, not permission to redo anything.
	if checkpoint, ok, err := store.Checkpoint(); err != nil {
		return nil, err
	} else if ok {
		r.sequence, r.ticks = checkpoint.Sequence, checkpoint.Ticks
		if checkpoint.State.Terminal() && checkpoint.State != runstate.StateFailed {
			r.record("", StageRunFinished, "the run is already %s: %s", checkpoint.State, checkpoint.Reason)
			return r.result(checkpoint.State, checkpoint.Reason), nil
		}
		if checkpoint.State == runstate.StateFailed {
			r.record("", StageResumed, "the run stopped at %s and is resumed under the same run id: %s",
				checkpoint.State, checkpoint.Reason)
		}
	}

	// The epic's BASE branch, folded in before anything is planned. The
	// integration branch is where this run reads its tracker from, and it
	// diverges from the base the moment either side writes: a tick filed on the
	// base after the branch forked is one this run cannot see until the fold
	// happens (refresh.go).
	if err := r.refreshFromBase(ctx); err != nil {
		var refusal *Refusal
		if !asRefusal(err, &refusal) {
			return nil, fmt.Errorf("reconcile: refresh %s from the epic's base branch: %w", r.branch, err)
		}
		r.failure = refusal
		if _, cErr := r.checkpoint(runstate.StateFailed, refusal.Error()); cErr != nil {
			return nil, cErr
		}
		r.record("", StageRunFinished, "%s: %s", runstate.StateFailed, refusal.Error())
		return r.result(runstate.StateFailed, refusal.Error()), nil
	}
	// What the fold pushed is what every later read of this branch must see,
	// including the store's own: its view was fetched before the fold.
	if _, err := store.Fetch(); err != nil {
		return nil, fmt.Errorf("reconcile: read the run state: %w", err)
	}

	graph, err := r.tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: read the epic graph: %w", err)
	}
	plan := planFrom(graph)
	if len(plan) == 0 {
		return nil, fmt.Errorf("reconcile: epic %s has no dispatchable tick", r.opts.EpicID)
	}
	r.seedTicks(plan)

	// The checkpoint rows the plan does not carry because the tracker has
	// closed their ticks: settled from the tracker's own answer before the
	// run checkpoints anything, so the admitted checkpoint a restart would
	// read from already carries them.
	r.settleClosedTicks(ctx, plan)

	if _, err := r.checkpoint(runstate.StateAdmitted, "the epic graph is read and the run is admitted"); err != nil {
		return nil, err
	}

	// Appendix A #12: the budget is clamped, and the number REPORTED is the
	// one that will govern — said at submission, while the run can still be
	// cancelled cheaply.
	r.SetBudget(r.opts.BudgetUSD, r.opts.CeilingUSD)
	r.ReportBudget()
	if r.budget.Reported > 0 {
		r.record("", StageBudgetSet, "the effective budget for this run is $%.2f", r.budget.Reported)
	}
	r.recordTierPolicy(plan)

	// The wave composition (tick 01u): checked at DISPATCH — here, at
	// admission, before any tick is claimed, started or paid for. A wave that
	// cannot merge is refused where it costs nothing, and the decision is
	// logged whether or not it refuses anything, so the planning posture is a
	// record the operator reads while the run can still be cancelled cheaply.
	r.recordWaveCompositionDecision(plan)
	if refusal := r.checkWaveComposition(plan); refusal != nil {
		r.failure = refusal
		if _, cErr := r.checkpoint(runstate.StateFailed, refusal.Error()); cErr != nil {
			return nil, cErr
		}
		r.record("", StageRunFinished, "%s: %s", runstate.StateFailed, refusal.Error())
		return r.result(runstate.StateFailed, refusal.Error()), nil
	}

	var failed []string
	for _, entry := range plan {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := r.processTick(ctx, entry); err != nil {
			var refusal *Refusal
			if !asRefusal(err, &refusal) {
				return nil, err
			}
			failed = append(failed, entry.TickID)
			r.failure = refusal
			r.setTick(entry.TickID, "rejected")
			// Two answers to one question, from tick 0z0 and tick emk, kept
			// together because they are not the same claim.
			//
			// A refusal that HOLDS the run for a person gets its own vocabulary
			// (StageRunHeld, 0z0): the feed is what a non-participant watches,
			// and a hold nobody can see is a stall by definition. Every other
			// refusal still reaches the feed in its own words (emk) — without
			// that, the feed went from `dispatched` straight to a generic
			// run_finished and the reason existed only in the source.
			if holdsForAPerson(refusal.Reason) {
				r.record(entry.TickID, StageRunHeld, "%s: %s", refusal.Reason, refusal.Message)
			} else {
				r.recordRefusal(entry.TickID, refusal)
			}
			if _, cErr := r.checkpoint(runstate.StateFailed, refusal.Error()); cErr != nil {
				return nil, cErr
			}
			// One epic at concurrency one: a tick that did not pass its gate
			// blocks whatever came after it, and the run stops rather than
			// integrating over an unproven change.
			break
		}
	}

	state, reason := runstate.StateCompleted, fmt.Sprintf("every tick of %s is closed behind the integrated gate", r.opts.EpicID)
	if len(failed) > 0 {
		state = runstate.StateFailed
		reason = fmt.Sprintf("%s did not pass: the run stopped rather than integrating over an unproven change. "+
			"Running the epic again under this run id resumes it — a rejected attempt that left nothing is "+
			"redispatched, a gate that FAILED runs again once the check or the tree is fixed (its evidence is "+
			"keyed by the commit it ran on, so a fixed tree is a different commit and a new record), an attempt "+
			"that was rejected holding commits nothing merged is reported rather than dispatched over, and "+
			"nothing that already passed is redone",
			strings.Join(failed, ", "))
		// The refusal that stopped the run rides the terminal reason, because a
		// checkpoint is written on a STATE CHANGE and this one is the last: the
		// durable record a person (and a resumed run) reads names WHAT stopped
		// it — the failing gate's check, the failing CI's JOB on the epic PR —
		// and not the resumption template alone, which says how to continue but
		// not what to fix (tick 0iz).
		if r.failure != nil {
			reason += fmt.Sprintf(". The refusal that stopped the run: %s", r.failure.Message)
		}
	}
	if _, err := r.checkpoint(state, reason); err != nil {
		return nil, err
	}
	r.record("", StageRunFinished, "%s: %s", state, reason)
	return r.result(state, reason), nil
}

func (r *Reconciler) result(state runstate.State, reason string) *Result {
	out := &Result{RunID: r.runID, EpicID: r.opts.EpicID, State: state, Reason: reason,
		Ticks: r.ticks, Failure: r.failure, FeedError: r.feedErr}
	for _, ts := range r.ticks {
		switch ts.State {
		case "closed":
			out.Closed = append(out.Closed, ts.TickID)
		case "rejected":
			out.Rejected = append(out.Rejected, ts.TickID)
		}
	}
	return out
}

// ----------------------------------------------------------------- plan ---

// planEntry is one tick's place in the run.
type planEntry struct {
	TickID string
	Title  string
	Wave   int
	Role   string
	Order  int

	// The tick facts the tier derivation is a function of (tick 5eq): all of
	// them are tracker facts the graph already carries, and nothing here is
	// added to a tick for routing's sake. They are copied at PLANNING time,
	// from the graph, so that the derivation is a pure function of what the
	// tracker said — never of a clock, a die or a model.
	Priority int
	Type     string
	Labels   []string
	// Blocks is how many ticks this one blocks: the graph-position fact.
	Blocks int
}

// planFrom turns the graph into the order this run dispatches in.
//
// EPIC-SKELETON: review and closeout are jobs like any other, and they are
// dispatched LAST — after every tick they are about. Sorting them to the end
// rather than trusting the wave numbers is deliberate: a skeleton tick that
// landed in the wrong wave would otherwise review an epic that is not finished.
func planFrom(graph tk.Graph) []planEntry {
	var out []planEntry
	seen := map[string]bool{}
	for _, wave := range graph.Waves {
		for _, task := range wave.Tasks {
			if seen[task.ID] || task.Status == "closed" {
				continue
			}
			seen[task.ID] = true
			out = append(out, planEntry{
				TickID: task.ID, Title: task.Title, Wave: wave.Wave, Role: RoleOf(task),
				Priority: task.Priority, Type: task.Type, Labels: task.Labels, Blocks: len(task.Blocks),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := skeletonRank(out[i].Role), skeletonRank(out[j].Role); a != b {
			return a < b
		}
		return out[i].Wave < out[j].Wave
	})
	for i := range out {
		out[i].Order = i + 1
	}
	return out
}

// skeletonRank is the ONE place the EPIC-SKELETON's ordering lives: work
// first, then review, then closeout.
func skeletonRank(role string) int {
	switch role {
	case "review-epic":
		return 1
	case "closeout-epic":
		return 2
	default:
		return 0
	}
}

// RoleOf maps a tracker task onto contracts/job-protocol.json's closed role
// vocabulary. The tracker's own role names are shorter, and a name it does not
// know is implement-tick — a task nobody classified is work.
func RoleOf(task tk.GraphTask) string {
	role := strings.ToLower(strings.TrimSpace(task.Role))
	if role == "" {
		role = strings.ToLower(strings.TrimSpace(task.Type))
	}
	switch role {
	case "review", "review-epic":
		return "review-epic"
	case "closeout", "close-out", "closeout-epic":
		return "closeout-epic"
	case "plan", "plan-epic":
		return "plan-epic"
	default:
		return "implement-tick"
	}
}

func (r *Reconciler) seedTicks(plan []planEntry) {
	known := map[string]bool{}
	for _, ts := range r.ticks {
		known[ts.TickID] = true
	}
	for _, entry := range plan {
		if !known[entry.TickID] {
			r.ticks = append(r.ticks, runstate.TickState{TickID: entry.TickID, State: "ready"})
		}
	}
}

// settleClosedTicks settles the checkpoint rows of ticks this plan no longer
// carries because the tracker has already closed them.
//
// planFrom skips whatever the tracker has closed, which is right for planning
// — and is also how the close-to-checkpoint window (tick 48q) went unnoticed:
// a reconciler that died between PUBLISHING a tick's close through the tracker
// and WRITING the checkpoint row that says closed left the two authorities
// disagreeing (the tracker says closed, the row says integrated), and the
// resumed run's plan dropped the tick, so the "already closed" settlement in
// processTick never ran for it and nothing ever wrote the row. The checkpoint
// is what a future resume decides what is done from, so a tick stuck at
// integrated is a tick that resume may try to finish again. The disagreement
// is settled here, from the tracker's OWN answer, by the first incarnation
// that finds it — never by trusting the dead one to have written the row.
//
// Only rows the plan dropped are asked: a tick the plan still carries runs
// processTick, whose already-closed settlement is this same rule. A row
// already reading closed is left alone — the settlement is idempotent and
// writes nothing on a resume that has nothing to settle. A tick the tracker
// cannot answer is left as it stands rather than guessed at.
func (r *Reconciler) settleClosedTicks(ctx context.Context, plan []planEntry) {
	planned := map[string]bool{}
	for _, entry := range plan {
		planned[entry.TickID] = true
	}
	for _, ts := range append([]runstate.TickState{}, r.ticks...) {
		if planned[ts.TickID] || ts.State == "closed" {
			continue
		}
		current, err := r.tracker.Show(ctx, ts.TickID)
		if err != nil || current.Status != "closed" {
			continue
		}
		r.setTick(ts.TickID, "closed")
		r.record(ts.TickID, StageSkipped,
			"already closed in the tracker: %s; the row is settled by this run because the incarnation that "+
				"closed it died before writing it", current.ClosedReason)
	}
}

func (r *Reconciler) setTick(tickID, state string) {
	for i := range r.ticks {
		if r.ticks[i].TickID == tickID {
			r.ticks[i].State = state
			return
		}
	}
	r.ticks = append(r.ticks, runstate.TickState{TickID: tickID, State: state})
}

func (r *Reconciler) setAttempt(tickID string, attempt int) {
	for i := range r.ticks {
		if r.ticks[i].TickID == tickID {
			r.ticks[i].Attempt = attempt
			return
		}
	}
}

// ----------------------------------------------------------- checkpoint ---

// checkpoint writes the run's state, on a STATE CHANGE. A poll that learns
// nothing writes nothing: the store answers no_change and no commit is made.
func (r *Reconciler) checkpoint(state runstate.State, reason string) (runstate.Outcome, error) {
	ticks := append([]runstate.TickState{}, r.ticks...)
	outcome, err := r.store.PutCheckpoint(runstate.Checkpoint{
		RunID:      r.runID,
		EpicID:     r.opts.EpicID,
		State:      state,
		Reason:     reason,
		UpdatedAt:  r.now().UTC().Format(time.RFC3339),
		Ticks:      ticks,
		Provenance: r.provenance(nil, nil, runstate.PhaseWorker, ""),
	})
	if err != nil {
		return "", fmt.Errorf("reconcile: checkpoint %s: %w", state, err)
	}
	if outcome.IsConflict() {
		// Someone else advanced the run. Re-fetch and reconcile from what is
		// actually there — never retry blindly.
		if _, fetchErr := r.store.Fetch(); fetchErr != nil {
			return outcome, fetchErr
		}
		if previous, ok, readErr := r.store.Checkpoint(); readErr == nil && ok {
			r.sequence, r.ticks = previous.Sequence, previous.Ticks
		}
		return outcome, fmt.Errorf("reconcile: the run state moved under this reconciler while writing %s: %s",
			state, outcome)
	}
	if checkpoint, ok, err := r.store.Checkpoint(); err == nil && ok {
		r.sequence = checkpoint.Sequence
	}
	return outcome, nil
}

// provenance is the one place a record says what it was produced against.
// Every field of the contract's $defs.provenance is stated, including the ones
// that are null here: a record that OMITS a field and one that states it as
// null are different claims.
//
// This is the RUN-LEVEL builder — a checkpoint, no single dispatch behind it
// — so executor and its substrate are null: a run may dispatch different
// roles through different executors, and a checkpoint is a reconciler-side
// record, not one an executor produced. The DISPATCH-level builder is
// attemptProvenance, which states the profile's own executor and the
// substrate it observed.
func (r *Reconciler) provenance(tick *string, attempt *int, phase runstate.Phase, sourceSHA string) runstate.Provenance {
	if sourceSHA == "" {
		sourceSHA = r.base
	}
	return runstate.Provenance{
		RunID:                 r.runID,
		TickID:                tick,
		Attempt:               attempt,
		SourceRef:             r.baseRef,
		SourceSHA:             sourceSHA,
		IntegrationRef:        runstate.Ptr(refFor(r.branch)),
		Phase:                 phase,
		Executor:              nil,
		WorkspaceID:           nil,
		Backend:               nil,
		Role:                  nil,
		ProfileDigest:         runstate.Ptr(r.profileSet),
		Model:                 nil,
		ContextManifestDigest: runstate.Ptr(r.gateDigest),
	}
}

// -------------------------------------------------------------- refusal ---

// Refusal is a refusal to proceed with ONE tick, carrying the reason as its
// own value. A refusal whose reason a caller has to recover by matching on
// prose is Appendix A #9's failure.
type Refusal struct {
	Reason  string
	TickID  string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// The reasons a tick does not reach a close. Each sends the next repair
// somewhere different, which is the whole point of their being distinct.
const (
	RefusedHeld        = "held"                // a struck-out unit; only a person releases it
	RefusedUnaddressed = "attempt_unaddressed" // nobody can say whether the attempt is running
	RefusedWiped       = "wiped"               // the job went unaddressed past the substrate's threshold
	RefusedCollect     = "collect_failed"      // the attempt did not produce work that can be merged
	RefusedBoundary    = "boundary_violation"  // the attempt wrote under an authority that is not its own
	RefusedMerge       = "merge_failed"        // the attempt does not integrate onto the epic branch
	RefusedGate        = "gate_failed"         // the integrated gate did not pass
	RefusedStale       = "stale_evidence"      // the gate's evidence is no longer about what would be published

	// The one a RESUME adds. An attempt this run already rejected left commits
	// that nothing merged — on origin's ref, or, when every push it ever made
	// failed, only on the branch in this checkout. It is distinct from
	// RefusedCollect because it sends the next repair somewhere else: not at
	// the worker, which is finished, but at the commits and at the person who
	// decides what happens to them.
	RefusedRejectedWork = "rejected_attempt_carries_work"

	// The one a RUN adds, before any tick is planned: the epic's base branch
	// does not fold into its integration branch. It is distinct from
	// RefusedMerge because it is about a different pair of branches and sends
	// the next repair somewhere else — at the base, and at whoever wrote both
	// sides of the conflicting file, not at an attempt.
	RefusedBaseRefresh = "base_refresh_conflict"

	// The two a ROLE job adds. Its deliverable is an answer, so its failures
	// are the answer's: one nobody could validate, and one that validated and
	// asks for a person. Both leave the process tick OPEN, and they are
	// distinct because they send the next repair somewhere different — the
	// first at whatever produced the envelope, the second at the person the
	// answer asked for.
	RefusedRoleResult = "role_result_invalid"     // the role-result envelope did not validate
	RefusedRoleAnswer = "role_answer_needs_human" // the answer is BLOCKED or NEEDS_CONTEXT

	// The one an IMPLEMENTATION tick adds, and the one that closes repair G's
	// false-close path: the attempt produced a branch that would merge, and its
	// report ends BLOCKED or NEEDS_CONTEXT. It is distinct from RefusedCollect
	// because the two send the next repair somewhere else — collect_failed is
	// about work that is not there or not mergeable, and is answered by
	// dispatching the tick again; this one is about work that IS there and an
	// answer that asks for a PERSON, and dispatching again would only produce
	// the same question. It is distinct from RefusedRoleAnswer because a role
	// job's answer IS its deliverable, while this one arrives beside a branch
	// somebody now has to decide about.
	RefusedNeedsHuman = "attempt_needs_human"

	// The one the TIER derivation adds (tick 5eq): a tick carries a tier
	// label the policy cannot honour — a tier name outside the closed
	// vocabulary, or one the target repo's roles table declares nothing for.
	// A label is a weakly typed field the tracker cannot validate, so this
	// refusal is the only place a typo can ever surface, which is exactly
	// why it names the tick AND the label and never falls back to the default
	// tier: an override that quietly failed is an override nobody can audit.
	RefusedTierLabel = "tier_label_unrecognised"

	// The three the WAVE COMPOSITION adds (tick 01u). The first is the one a RUN
	// adds at admission, before anything is claimed or started: two ticks of
	// one wave declare the same file — a composition that cannot merge,
	// refused at dispatch where it costs nothing rather than discovered at the
	// merge gate after the wave has run. It is run-level like
	// RefusedBaseRefresh because no one tick is at fault: the overlap is a
	// fact about the pair, and the fix is at the ticks. The second is the label
	// itself: malformed in a way that cannot check anything (empty, absolute,
	// leaving the repository), refused in the tier label's shape — loudly,
	// naming the tick and the label — because a weakly typed field surfaces
	// here or nowhere. The third is the one
	// an ATTEMPT adds, at the merge: it touched a file its tick's touch:
	// declaration does not name — the declaration's reporting half, so a
	// worker that crosses its own declared boundary is detectable after the
	// fact rather than merged silently. It is distinct from RefusedBoundary
	// because that one is about the AUTHORITY a write is under; this one is
	// about the tick's own declaration of its scope, and it sends the repair
	// at the label or the tick's scope, not at the write.
	RefusedWaveOverlap     = "wave_composition_conflict"
	RefusedTouchLabel      = "touch_label_invalid"
	RefusedUndeclaredTouch = "undeclared_file_touched"

	// The two the FINDINGS channel adds (tick 7vn). A worker's discoveries
	// outside its tick are discovery, not deliverable: the first is a report
	// this reconciler cannot read — a findings block that does not parse, a
	// finding outside the vocabularies — and closing the tick behind it would
	// be the 604 failure with one more step in it: findings read by nobody.
	// The second is the close's other gate: a tick whose findings nobody has
	// triaged is not closed, which is the one thing that stops a finding
	// falling on the floor. They are distinct because they send the next
	// repair somewhere different — the first at the worker's report block,
	// the second at the person the draft is waiting for.
	RefusedFindingInvalid   = "finding_report_invalid"
	RefusedFindingUntriaged = "finding_untriaged"

	// The five the CLOSE-OUT ADMISSION adds (tick 0iz). The PR + CI rule a
	// target repository declares in .tick/config.md is a precondition the
	// RUN enforces, and each refusal names which half of it is unmet, because
	// the halves send the next repair somewhere different: the first at the
	// HOST, which configured no code-hosting surface for a repo that declares
	// the rule; the second at the FORGE, which could not open or read the
	// PR (a credential, a permission, a network); the third at the WORKFLOW,
	// which never ran on the PR at all — unsatisfiable by waiting, which is
	// the failure the rule exists to surface; the fourth at the CODE, named
	// by the failing job the message carries; the fifth at the CLOCK — the
	// run bounded its wait, and re-running the epic re-derives the admission
	// from the PR rather than rediscovering it.
	RefusedCloseoutForge     = "closeout_forge_absent" // no surface behind the rule
	RefusedCloseoutPR        = "closeout_pr_unmet"     // no PR, or one the forge could not open or read
	RefusedCloseoutCIAbsent  = "closeout_ci_absent"    // CI never ran on the PR head
	RefusedCloseoutCI        = "closeout_ci_failed"    // CI red; the message names the failing job
	RefusedCloseoutCIPending = "closeout_ci_pending"   // CI still pending past the run's bound
)

// refuse names a refusal AND says which problem it is, because Appendix A #9
// is not about the reason field — it is about the MESSAGE a person reads. "The
// tick did not pass" is the message that sends the next repair looking for the
// wrong thing, so a gate that failed, a boundary that was crossed and an
// attempt nobody can address never share one.
func (r *Reconciler) refuse(reason, tick, format string, args ...any) *Refusal {
	if !r.guarded(guardDistinctFailureClasses) {
		// The negative control: every failure reads the same, which is how a
		// failing gate and a boundary violation become one incident report.
		return &Refusal{Reason: reason, TickID: tick, Message: collapsedMessage}
	}
	return &Refusal{Reason: reason, TickID: tick, Message: fmt.Sprintf(format, args...)}
}

// recordRefusal writes a refusal to the feed, unless the path that produced it
// already did. Two paths record their own rejection line with detail this one
// could not add (the boundary violation's paths, the role job's answer), and a
// terminal event said twice is worse than one said plainly.
func (r *Reconciler) recordRefusal(tick string, refusal *Refusal) {
	if refusal == nil {
		return
	}
	for i := len(r.journal) - 1; i >= 0; i-- {
		event := r.journal[i]
		if event.Tick != tick {
			continue
		}
		if event.Stage == StageRejected {
			return
		}
		break
	}
	r.record(tick, StageRejected, "%s: %s", refusal.Reason, refusal.Message)
}

// collapsedMessage is what a refusal reads like when distinct failure classes
// are allowed to share a sentence.
const collapsedMessage = "the tick did not pass"

// holdsForAPerson is the set of refusals whose next actor is a PERSON and not
// another run: the run cannot proceed without a decision somebody has to
// make, which is what makes these holds rather than failures (tick 0z0). The
// set is closed on the refusal REASON — a value, never a sentence — and each
// member says why it is here:
//
//   - RefusedHeld: a struck-out unit, released only by a person (A11);
//   - RefusedUnaddressed and RefusedRejectedWork: the two `ticfac settle`
//     releases — an attempt nobody can address, and one rejected with commits
//     nothing merged;
//   - RefusedNeedsHuman and RefusedRoleAnswer: the worker or the role job
//     answered BLOCKED or NEEDS_CONTEXT, and the answer is its deliverable;
//   - RefusedFindingUntriaged: a tick whose findings nobody has triaged is
//     refused its close, and the triage is a person's.
//
// Every other refusal is a repair another RUN can make — a gate that runs
// again on a fixed tree, an attempt that is redispatched once its blocker
// closed — and those are reported as failures, not held.
func holdsForAPerson(reason string) bool {
	switch reason {
	case RefusedHeld, RefusedUnaddressed, RefusedRejectedWork,
		RefusedNeedsHuman, RefusedRoleAnswer, RefusedFindingUntriaged:
		return true
	}
	return false
}

func asRefusal(err error, into **Refusal) bool {
	if refusal, ok := err.(*Refusal); ok {
		*into = refusal
		return true
	}
	return false
}

// AsRefusal reports whether err is a per-tick refusal, and which one.
func AsRefusal(err error) (*Refusal, bool) {
	var refusal *Refusal
	if asRefusal(err, &refusal) {
		return refusal, true
	}
	return nil, false
}
