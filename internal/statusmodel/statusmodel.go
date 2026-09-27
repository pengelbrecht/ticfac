// Package statusmodel is the one model every surface renders (ticfac tick
// 6dh): the versioned answer `ticfac status --json` emits, covering a run's
// lifecycle, its waves and ticks, its live workers and what the run waits on.
//
// THE STATUS VIEW IS A RENDERER, NOT A SOURCE. `ticfac watch` (89m), the bare
// `ticfac` listing (2qz), a phone page (i1r) and an agent checking in all
// answer the same questions of the same run — does anything need a person, is
// it healthy, how far along, what is happening now — and before this package
// each of them would have had to assemble its own answers from the records,
// which is three hand-rolled watchers waiting to happen (the failure the run
// event feed was built to end, tick u9l). One model, versioned and stable
// like every other contract surface, is what those renderers render.
//
// It reads only what exists and adds no source of truth:
//
//   - the tracker's own graph (the same layering `tk graph` computes) for
//     waves and tick identity;
//   - the run's DURABLE records — the checkpoint, the attempt markers, the
//     evidence, the decisions, the absorptions, the findings — for every state
//     it states, never a worker's own claim about its work;
//   - the run's event feed and the liveness facts the surfaces already read,
//     for the hints that are the feed's whole job (when to look) and for the
//     counts nobody else holds (retries, interventions);
//   - the runner's own session log for worker silence (the signal tick b1t
//     moves the stall warning to: pi appends on every turn, so the log's age
//     is a precise, cheap liveness fact that costs nothing to read);
//   - the forge's check runs for CI, per check on the PR head.
//
// The shape is pinned by contract.json in this package, in the house style of
// the cross-language bundle: a closed schema, golden documents that must
// validate, negative documents that must be refused with the named error.
// The fixture lives beside the code because only Go reads it today; when a
// second reader exists (the factory's TypeScript, for the phone page), it
// moves to the pinned bundle the way every two-reader contract does.
package statusmodel

import (
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// SchemaVersion is the model's version. A reader that meets a value it does
// not know refuses the document rather than guessing at it — the rule every
// versioned surface here holds.
const SchemaVersion = 1

// SchemaID names the model's record, `ticfac.status.v1`.
const SchemaID = "ticfac.status.v1"

// Hosts: where the run lives. A local run is named by its epic id and its
// pidfile; a cloud run is named `run_` plus hex and hosted by the factory.
const (
	HostLocal = "local"
	HostCloud = "cloud"
)

// Lifecycle phases, in the order an epic passes through them: plan, the
// waves, the review, the close-out, CI on the epic PR, and the merge — which
// is a person's, deliberately (see .tick/config.md): everything up to it is
// work a run can prove it did, and the merge is the judgement about whether
// to accept it. `done`, `failed` and `cancelled` are the run's own terminal
// answers, named as phases so the renderer never parses prose to learn them.
const (
	PhasePlan      = "plan"
	PhaseWaves     = "waves"
	PhaseReview    = "review"
	PhaseCloseout  = "closeout"
	PhaseCI        = "ci"
	PhaseMerge     = "merge"
	PhaseDone      = "done"
	PhaseFailed    = "failed"
	PhaseCancelled = "cancelled"
)

// Phases is the ordered lifecycle, the vocabulary a renderer walks.
var Phases = []string{PhasePlan, PhaseWaves, PhaseReview, PhaseCloseout, PhaseCI, PhaseMerge}

// Phase states: `pending` (not reached), `active` (the run is in it now) and
// `done` (behind it). No `skipped`: a phase an epic does not carry (a review
// with no review tick) is `done` the moment the run passes it — a phase that
// costs nothing is not a phase a renderer should show as waiting.
const (
	PhaseStatePending = "pending"
	PhaseStateActive  = "active"
	PhaseStateDone    = "done"
)

// Wave states: `done` (every tick in it closed), `active` (the frontier wave)
// and `upcoming` (behind the frontier).
const (
	WaveDone     = "done"
	WaveActive   = "active"
	WaveUpcoming = "upcoming"
)

// Try outcome vocabulary, per (tick, attempt), from durable records alone:
// `closed` behind the gate at that attempt, `rejected` there, `gate-failed`
// when that attempt's gate evidence refused it, `reported` when its evidence
// all passed but the tick did not close on it, `in-flight` while its
// worktree stands, and `dispatched` when the marker is all the records say.
const (
	TryClosed     = "closed"
	TryRejected   = "rejected"
	TryGateFailed = "gate-failed"
	TryReported   = "reported"
	TryInFlight   = "in-flight"
	TryDispatched = "dispatched"
)

// Wait kinds, the closed vocabulary of what a run can be blocked on:
// `held-for-person` (a struck-out attempt only a person releases), `merge`
// (the PR is the person's to merge), `ci` (the close-out's gate on the PR),
// `dead-run` (a run whose process is gone without its own terminal record),
// and `workers` (the ordinary wait: live attempts doing their work).
const (
	WaitHeldForPerson = "held-for-person"
	WaitMerge         = "merge"
	WaitCI            = "ci"
	WaitDeadRun       = "dead-run"
	WaitWorkers       = "workers"
	// WaitFinding is an untriaged findings draft — a person's decision, and
	// attention rather than the run's blocking wait: a live run triages
	// its own findings, so a draft standing while the run is gone is the
	// case that needs the person.
	WaitFinding = "finding"
)

// Model is one run's whole answer, `ticfac.status.v1`. Every field that can
// be genuinely absent is REQUIRED-AND-NULL rather than omittable, the
// convention the bundle's provenance object established: "no waves were
// readable" and "the tracker was not read" are different claims, and only the
// second is a claim at all.
type Model struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	EpicID        string `json:"epic_id"`
	Host          string `json:"host"`
	GeneratedAt   string `json:"generated_at"`

	// Degraded names every source that could not be read, by the name the
	// reader knows them by ("tracker", "run-state", "feed", "forge"). An
	// empty list says every source answered. A source that answers "none"
	// is NOT degraded — that is the source doing its job.
	Degraded []string `json:"degraded"`

	Liveness  Liveness  `json:"liveness"`
	Lifecycle Lifecycle `json:"lifecycle"`
	Progress  Progress  `json:"progress"`

	// Waves is every wave of the epic with its ticks, from the same layering
	// `tk graph` computes. Null when the tracker could not be read — no
	// waves and unread waves are different claims.
	Waves *[]Wave `json:"waves"`

	// Workers is one entry per live worker (a standing attempt), with its
	// own gaps, its runner's silence and its last turn. Null when the
	// census cannot be taken (a cloud run's workers are not on this
	// machine); empty when it read and nothing stands.
	Workers *[]Worker `json:"workers"`

	// WaitsOn is the one thing the run is blocked on, with the command that
	// unblocks it when it needs a person. Null when nothing blocks it.
	WaitsOn   *Wait       `json:"waits_on"`
	Attention []Attention `json:"attention"`

	Health Health `json:"health"`

	// Gates is the run's gate evidence, per check per head, exactly as the
	// records state it. Empty when the run recorded none.
	Gates []Gate `json:"gates"`

	// CI is what the forge says on the epic PR's head, per check. Null when
	// no PR exists or the forge could not be asked.
	CI   *CI  `json:"ci"`
	Cost Cost `json:"cost"`

	// Remaining is the approximate time left, ONLY where measured tick
	// durations support the estimate. Null everywhere else — a number
	// nobody measured is a number that lies.
	Remaining *Remaining `json:"remaining"`
}

// Liveness is the run's own answer to "is it alive", carried from the probe
// the surfaces already trust: run.pid plus the process table for a local run,
// the Workflow's own state for a cloud one.
type Liveness struct {
	Alive  bool   `json:"alive"`
	State  string `json:"state"`
	Reason string `json:"reason"`
	// Source says where the answer came from, the probe's own name for it:
	// "run.pid" for a local process, "workflow-record" or
	// "workflow-supervisor" for a cloud run.
	Source string `json:"source"`

	// LastEvent is the run's own last word from its feed — a hint about
	// when to look, never a verdict (the feed's contract). Null when the
	// feed is empty or unreadable.
	LastEvent *runfeed.Event `json:"last_event"`
	// LastEventAgeSeconds measures the last event against the model's own
	// `now`, so a renderer never does clock arithmetic. Null when there is
	// no parseable stamp to measure.
	LastEventAgeSeconds *int64 `json:"last_event_age_seconds"`
}

// Lifecycle is where the epic stands in its own progress, and each phase's
// state beside it — the progress bar a renderer draws and the phase a person
// glances at.
type Lifecycle struct {
	Phase  string       `json:"phase"`
	Phases []PhaseState `json:"phases"`
	// Wave says which wave the run is in and how many there are, when the
	// run is in the waves phase and the tracker answered. Null otherwise.
	Wave *WaveRef `json:"wave"`
}

// PhaseState is one phase's own state, in the Phases order.
type PhaseState struct {
	Phase string `json:"phase"`
	State string `json:"state"`
}

// WaveRef names the active wave: its 1-based number and the total.
type WaveRef struct {
	Active int `json:"active"`
	Total  int `json:"total"`
}

// Progress is how far along the whole epic is: every tick and every wave,
// counted from the same records the waves are built from. Both counters are
// null when the tracker could not be read — "no ticks" and "unread ticks"
// are different claims.
type Progress struct {
	Ticks *TickProgress `json:"ticks"`
	Waves *WaveProgress `json:"waves"`
}

// TickProgress counts the epic's children: total, closed, and open.
type TickProgress struct {
	Total  int `json:"total"`
	Closed int `json:"closed"`
	Open   int `json:"open"`
}

// WaveProgress counts the waves: total, done, and the active one (0 when
// none is active). Null when the tracker could not be read.
type WaveProgress struct {
	Total  int `json:"total"`
	Done   int `json:"done"`
	Active int `json:"active"`
}

// Wave is one layer of the epic, with its ticks.
type Wave struct {
	Wave  int    `json:"wave"`
	State string `json:"state"`
	Ticks []Tick `json:"ticks"`
}

// Tick is one tick of the epic, as the durable records state it. State is the
// checkpoint's own closed vocabulary; the tracker's closed status backs it
// where the checkpoint has no row. Never a worker's own claim.
type Tick struct {
	TickID string `json:"tick_id"`
	Title  string `json:"title,omitempty"`
	// Role names the tick's role when it carries one (review, closeout) —
	// the fact the lifecycle phases are derived from.
	Role  string `json:"role,omitempty"`
	State string `json:"state"`

	// Absorbed says the run itself created this tick, by absorbing a
	// finding into the epic it was running — the epic's shape changed
	// mid-run, and a renderer shows that as a marked row, not a surprise.
	Absorbed bool `json:"absorbed"`

	// Try is the tick's own try number of its current or last attempt (the
	// h58 language: "w9b#3" is the third try, not dispatch #3), and Attempt
	// is that attempt's run-wide dispatch number — its identity. Null when
	// the records show no dispatch for the tick.
	Try     *int `json:"try"`
	Attempt *int `json:"attempt"`

	// Tries is the whole try history with each one's outcome, from the
	// durable records: the attempt markers, the gate evidence and the
	// checkpoint. Empty when the tick was never dispatched.
	Tries []Try `json:"tries"`

	// Tier, Model and Executor are the current attempt's provenance — how
	// this tick is being worked and by what. Null when no attempt record
	// states them.
	Tier     *string `json:"tier"`
	Model    *string `json:"model"`
	Executor *string `json:"executor"`

	// ElapsedSeconds is how long the current attempt has been running,
	// measured from its dispatch marker to the model's `now`. Null when the
	// tick has no in-flight attempt.
	ElapsedSeconds *int64 `json:"elapsed_seconds"`
}

// Try is one dispatch of one tick, with its outcome as the durable records
// state it.
type Try struct {
	Try     int    `json:"try"`
	Attempt int    `json:"attempt"`
	Outcome string `json:"outcome"`
	// DispatchedAt is the attempt marker's own stamp, RFC3339. Empty when
	// the marker could not be read — the try exists because a record names
	// it, but a stamp that did not parse is stated as empty, never guessed.
	DispatchedAt string `json:"dispatched_at"`
}

// Worker is one live worker: a standing attempt, its measured gaps, its
// runner's silence and that runner's last turn.
type Worker struct {
	TickID  string `json:"tick_id"`
	Attempt int    `json:"attempt"`
	Branch  string `json:"branch"`
	// Worktree is the path git's registration names; empty when the attempt
	// has no standing registration on this machine.
	Worktree string `json:"worktree"`

	// ElapsedSeconds is how long since the attempt's dispatch marker.
	// Null when no marker states a parseable dispatch time.
	ElapsedSeconds *int64 `json:"elapsed_seconds"`

	// BranchIdleSeconds and WorktreeIdleSeconds are the runprogress gaps:
	// how long since the branch last moved and the worktree last changed.
	// Neither is a verdict — a worker thinking hard legitimately commits
	// nothing for a while. Null when the fact could not be measured.
	BranchIdleSeconds   *int64 `json:"branch_idle_seconds"`
	WorktreeIdleSeconds *int64 `json:"worktree_idle_seconds"`

	// WallClock is the run's own typed statement that this attempt's wall
	// clock fired, from the feed's `wall_clock_fired` line — the stage and
	// the identity, with the line's detail carried verbatim. Null when the
	// run said no such thing.
	WallClock *WallClock `json:"wall_clock"`

	// SilenceSeconds is how long since the runner's own session log last
	// grew (pi appends on every turn, so the log's age is the worker's
	// liveness, the signal tick b1t names). Null when the runner has no
	// session log this machine can read.
	SilenceSeconds *int64 `json:"silence_seconds"`
	// LastTurnAt is the stamp of the runner's last turn, and LastTurn a
	// one-line summary of it. Null on the same terms as the silence.
	LastTurnAt *string `json:"last_turn_at"`
	LastTurn   *string `json:"last_turn"`
}

// WallClock is one in-flight attempt's wall clock firing, as the run said it.
type WallClock struct {
	FiredAt string `json:"fired_at"`
	// Detail is the run's own firing line, verbatim — what the executor was
	// last seen doing belongs to the person deciding what to do about an
	// attempt that is past its bound.
	Detail string `json:"detail"`
}

// Wait is what the run is blocked on. Attention is the same shape, one entry
// per thing a person must do.
type Wait struct {
	Kind string `json:"kind"`
	What string `json:"what"`
	// Since is when the wait began, from the record or line that states it.
	// Null when nothing states a moment.
	Since *string `json:"since"`
	// NeedsPerson says only a person can move it. A wait that does not need
	// one (CI, workers) is a wait a watcher watches, not an alarm.
	NeedsPerson bool `json:"needs_person"`
	// UnblockCommand is the one command that moves the wait on, when one
	// exists and the wait needs a person. Null otherwise.
	UnblockCommand *string `json:"unblock_command"`
}

// Attention is one thing a person must do: the first question an unattended
// factory is glanced at with — does anything need me?
type Attention Wait

// Health is the run's interventions and warnings, counted from the typed
// feed lines that stated them: the facts a watcher reads to answer "is it
// healthy" without parsing a single line of prose.
type Health struct {
	RemoteRetries   int `json:"remote_retries"`
	Interventions   int `json:"interventions"`
	StallWarnings   int `json:"stall_warnings"`
	WallClocksFired int `json:"wall_clocks_fired"`
}

// Gate is one gate evidence record, per check per head, exactly as the run
// recorded it. The head is the source sha the gate ran on — the key evidence
// must be keyed by, not by the commit that wrote it.
type Gate struct {
	Key        string `json:"key"`
	Check      string `json:"check"`
	Result     string `json:"result"`
	Acceptance string `json:"acceptance"`
	Phase      string `json:"phase"`
	// Head is the source sha the check ran on; IntegrationRef the ref it
	// gated. Head is null when the record states none — a gate that names
	// no head proves nothing about a ref, and the model says so.
	Head           *string `json:"head"`
	IntegrationRef *string `json:"integration_ref"`
	TickID         *string `json:"tick_id"`
	Attempt        *int    `json:"attempt"`
	StartedAt      string  `json:"started_at"`
	FinishedAt     string  `json:"finished_at"`
}

// CI is what the forge says on the epic PR's head: the state the close-out
// gate reads, and the latest run of every check behind it.
type CI struct {
	State string `json:"state"`
	PR    *PR    `json:"pr"`
	// Checks is the latest run of every check the forge recorded for the
	// head, per check. Empty when the forge recorded none.
	Checks []CheckState `json:"checks"`
}

// PR is the epic integration pull request — the thing the merge belongs to.
type PR struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	HeadRef string `json:"head_ref"`
	HeadSHA string `json:"head_sha"`
	BaseRef string `json:"base_ref"`
}

// CheckState is one forge check's latest run on the head.
type CheckState struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	StartedAt  string `json:"started_at"`
}

// Cost is what the run spent so far, as far as the records state it. The only
// cost any record carries today is the model exchanges' usage (the decisions'
// own `usage.cost_usd`); worker jobs record no cost, and the basis says so —
// a number that quietly claimed more than the records do would be a lie with
// a decimal point.
type Cost struct {
	RecordedUSD float64 `json:"recorded_usd"`
	// Attempts is how many dispatches the run paid for — the count a person
	// multiplies by their own rates when the records carry no prices.
	Attempts int    `json:"attempts"`
	Basis    string `json:"basis"`
}

// Remaining is the approximate time left, ONLY where measured tick durations
// support the estimate: the median of what closed ticks measurably took,
// times the ticks still open. The basis names the estimate for what it is.
type Remaining struct {
	ApproximateSeconds int64  `json:"approximate_seconds"`
	Basis              string `json:"basis"`
}
