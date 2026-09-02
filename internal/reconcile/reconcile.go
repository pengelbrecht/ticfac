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
	"github.com/pengelbrecht/ticfac/internal/profile"
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

	// DefaultPollInterval is WELL under it (`max_poll_ms`), which is what makes
	// the poll a keepalive rather than a status check.
	DefaultPollInterval = 5 * time.Minute

	// DefaultStepCap is the host's cap on one step of a controller
	// (`step_cap_ms`).
	DefaultStepCap = 8 * time.Minute

	// DefaultWallSeconds bounds one job. An unbounded job is one nothing stops.
	DefaultWallSeconds = 3600
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

	// Profile is the resolved role profile this dispatch is made under —
	// executor, runner, model and prompt, and nothing else (SPEC §4.5). The
	// factory reads the RUNNER off it, because which agent CLI serves a role is
	// executor configuration and not a field of the closed protocol records.
	Profile *profile.Profile
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

	// NewExecutor builds the executor for one dispatch.
	NewExecutor func(Dispatch) (Executor, error)

	// ExecStateRoot is the HOST-level directory dispatch state directories are
	// created under. It is deliberately not inside the repository: a restart
	// from a fresh clone finds the previous run's attempts through it.
	ExecStateRoot string

	// GateConfig is the runners.toml the integrated gate is read from. Empty
	// is `<repo>/.tick/runners.toml`.
	GateConfig string

	// GateTimeout bounds one gate command.
	GateTimeout time.Duration

	// PollInterval is how often a live job is addressed. It IS the keepalive,
	// so it must stay well under WipeThreshold.
	PollInterval time.Duration

	// WipeThreshold is the substrate's: how long a job may go unaddressed.
	WipeThreshold time.Duration

	// StepCap bounds one leg of a long wait.
	StepCap time.Duration

	// WallSeconds bounds one job.
	WallSeconds int

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

	gate       GateCommands
	gateDigest string

	// profiles is the resolved role profile per role, and profileSet digests
	// all of them together: a checkpoint is not about one role, so it names the
	// SET the run was made under.
	profiles   map[string]*profile.Profile
	profileSet string

	pollInterval  time.Duration
	wipeThreshold time.Duration
	stepCap       time.Duration

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
	StageSkipped     = "skipped"
	StageHeld        = "held"
	StageClaimed     = "claimed"
	StageDispatched  = "dispatched"
	StageAdopted     = "adopted"
	StageWaiting     = "waiting"
	StageCollected   = "collected"
	StageRejected    = "rejected"
	StageIntegrated  = "integrated"
	StageGatePassed  = "gate_passed"
	StageGateFailed  = "gate_failed"
	StageStale       = "stale_evidence"
	StageClosed      = "closed"
	StageCleanedUp   = "cleaned_up"
	StageRunFinished = "run_finished"
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

	gate, err := ReadGateCommands(opts.GateConfig)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	if len(gate) == 0 {
		return nil, fmt.Errorf("reconcile: %s declares no [testing.commands]: there is no integrated gate to run, "+
			"and closing a tick behind a gate that does not exist is a close nothing stands behind", opts.GateConfig)
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
		if err := usableProfile(profiles[role]); err != nil {
			return nil, fmt.Errorf("reconcile: %w", err)
		}
	}

	g := &repoGit{dir: opts.Repo, name: "ticfac", email: "ticfac@example.com", remote: opts.Remote}
	if _, err := g.run("", "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("reconcile: %s is not a git repository: %w", opts.Repo, err)
	}

	r := &Reconciler{
		opts:          opts,
		git:           g,
		runID:         opts.RunID,
		branch:        opts.IntegrationBranch,
		gate:          gate,
		gateDigest:    gate.Digest(),
		profiles:      profiles,
		profileSet:    profileSetDigest(profiles),
		pollInterval:  opts.PollInterval,
		wipeThreshold: opts.WipeThreshold,
		stepCap:       opts.StepCap,
		now:           opts.Now,
		sleep:         opts.Sleep,
		guardsOff:     opts.guardsOff,
		lastPolled:    map[string]time.Time{},
		liveness:      map[string]string{},
		holds:         map[string]*hold{},
		evidence:      map[string]Fingerprint{},
	}
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

func (r *Reconciler) record(tick, stage, format string, args ...any) {
	event := Event{At: r.now(), Tick: tick, Stage: stage, Detail: fmt.Sprintf(format, args...)}
	r.journal = append(r.journal, event)
	if r.opts.stopAfter != nil && r.opts.stopAfter(event) {
		panic(stopped{At: event})
	}
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
}

// Run reconciles the epic until there is nothing dispatchable left, or until
// something refuses.
//
// It is restart-safe by construction: everything it needs is either on origin
// under `.ticfac/`, in the tracker, or in the executor's own durable state, and
// every effect is preceded by the compare-and-swap that proves it has not
// already happened.
func (r *Reconciler) Run(ctx context.Context) (*Result, error) {
	// The integration branch has to exist before anything can be recorded
	// about the run: the run-state store's authority is origin's copy of it.
	base, err := r.git.ensureRemoteBranch(r.branch, r.opts.BaseRef)
	if err != nil {
		return nil, fmt.Errorf("reconcile: prepare the integration branch %s: %w", r.branch, err)
	}
	r.base, r.baseRef = base, refFor(r.branch)

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

	// Recovery is a fetch and then a read. A checkpoint that is already
	// terminal is a run somebody finished; replaying it must not restart it.
	if checkpoint, ok, err := store.Checkpoint(); err != nil {
		return nil, err
	} else if ok {
		r.sequence, r.ticks = checkpoint.Sequence, checkpoint.Ticks
		if checkpoint.State.Terminal() {
			r.record("", StageRunFinished, "the run is already %s: %s", checkpoint.State, checkpoint.Reason)
			return r.result(checkpoint.State, checkpoint.Reason), nil
		}
	}

	graph, err := r.opts.Tracker.Graph(ctx, r.opts.EpicID)
	if err != nil {
		return nil, fmt.Errorf("reconcile: read the epic graph: %w", err)
	}
	plan := planFrom(graph)
	if len(plan) == 0 {
		return nil, fmt.Errorf("reconcile: epic %s has no dispatchable tick", r.opts.EpicID)
	}
	r.seedTicks(plan)

	if _, err := r.checkpoint(runstate.StateAdmitted, "the epic graph is read and the run is admitted"); err != nil {
		return nil, err
	}

	// Appendix A #12: the budget is clamped, and the number REPORTED is the
	// one that will govern — said at submission, while the run can still be
	// cancelled cheaply.
	r.SetBudget(r.opts.BudgetUSD, r.opts.CeilingUSD)
	r.ReportBudget()
	if r.budget.Reported > 0 {
		r.record("", StageRunFinished, "the effective budget for this run is $%.2f", r.budget.Reported)
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
		reason = fmt.Sprintf("%s did not pass: the run stopped rather than integrating over an unproven change",
			strings.Join(failed, ", "))
	}
	if _, err := r.checkpoint(state, reason); err != nil {
		return nil, err
	}
	r.record("", StageRunFinished, "%s: %s", state, reason)
	return r.result(state, reason), nil
}

func (r *Reconciler) result(state runstate.State, reason string) *Result {
	out := &Result{RunID: r.runID, EpicID: r.opts.EpicID, State: state, Reason: reason,
		Ticks: r.ticks, Failure: r.failure}
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
		Executor:              runstate.Ptr(subprocess.ExecutorName),
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

	// The two a ROLE job adds. Its deliverable is an answer, so its failures
	// are the answer's: one nobody could validate, and one that validated and
	// asks for a person. Both leave the process tick OPEN, and they are
	// distinct because they send the next repair somewhere different — the
	// first at whatever produced the envelope, the second at the person the
	// answer asked for.
	RefusedRoleResult = "role_result_invalid"     // the role-result envelope did not validate
	RefusedRoleAnswer = "role_answer_needs_human" // the answer is BLOCKED or NEEDS_CONTEXT
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

// collapsedMessage is what a refusal reads like when distinct failure classes
// are allowed to share a sentence.
const collapsedMessage = "the tick did not pass"

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
