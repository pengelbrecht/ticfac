package cloudflaresandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/profile"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// The executor: start and inspect over the door, collect from git (the
// decided placement, tick xev), and the two operations this side of the
// boundary has nothing to act on, refused typed.
//
// Nothing here crosses the protocol seam that is not already crossed by the
// records internal/exec/subprocess owns: the factory's base URL, the run's
// own gateway token, the epic, the base ref and the tick's title are host
// configuration (Options), because the protocol records are closed and a
// field invented here would be one the reconciler ignores. The model (tick
// a08) and the harness and rendered role prompt (tick 9iz) ride Options for
// the same reason with one difference: they are the dispatch PROFILE's own
// resolution, carried to the door because the container is the only thing
// that can act on them. The REPOSITORY
// enters the same way, at collect: this executor creates no worktree and
// runs no runner in this container — the sandbox container the factory
// boots is the worker's whole environment, and it clones from the write
// ref's upstream itself. An Options with no Repo is refused by collect
// rather than papered over; Start and Inspect need none, and a settle leg
// that only ever asks Cancel and Dispose builds the executor without one.

// Options configure the host.
type Options struct {
	// FactoryURL is the factory's public base URL, the one the deployment
	// recorded for its own sandboxes (FACTORY_BASE_URL on the Worker side).
	// Defaults to $TICKS_FACTORY_URL, which the container boot exports.
	FactoryURL string

	// Token is the run's OWN gateway token — the credential the container
	// holds and the only one it may hold (D17). The door refuses the
	// operator's factory token on purpose, because a container holding it
	// would be the leak that token grade exists to prevent. Defaults to
	// $TICKS_FACTORY_TOKEN.
	Token string

	// RunID is the run the dispatch belongs to — the run the credential
	// names. Start never needs it (the door derives the run from the
	// credential), but ReattachSettled does: an attempt whose record went
	// with a dead orchestrator's disk is re-described from the dispatch, and
	// the container's per-run fallback branch (collect.go attemptHead) is
	// spelled from the run.
	RunID string

	// EpicID is the epic this run works on. The door checks it against the
	// run's own row, so a container that has somehow drifted onto another
	// epic is refused rather than silently dispatching this run's tick
	// under another one's name.
	EpicID string

	// BaseRef is the epic's base branch, carried to the container for a
	// later boot's re-derivation — the same reason the door requires it.
	BaseRef string

	// Title is the tick's title, carried for the same re-derivation.
	Title string

	// Model is the model the dispatch's profile resolved (tick a08). The door
	// boots the worker container on exactly this and names it back in the
	// handle, and Start refuses a handle that names another — so the model a
	// run records for the attempt is the model that ran, never the factory's
	// own default agreeing with it by luck. Required: the door refuses a start
	// without one.
	Model string

	// Harness is the harness the dispatch's profile resolved — the profile's
	// runner, the kind the sandbox image runs a harness for (tick 9iz). The
	// door binds the worker container to exactly this (`TICKS_HARNESS`,
	// outranking RUN_WORKER_HARNESS) and names it back in the handle, and
	// Start refuses a handle that names another — for the model's reason
	// verbatim: a start with no harness would boot on the factory's own
	// standing choice, and the caller's record would name a harness that
	// never ran. Required: the door refuses a start without one.
	Harness string

	// Prompt is the RENDERED role prompt the dispatch's profile resolved (tick
	// 9iz): the profile's own prompt text, not a filename. The container's
	// entrypoint renders its worker prompt from the checkout's tracker and
	// never sees the factory's otherwise, so the door delivers this into the
	// container's boot environment (`TICKS_ROLE_PROMPT`, beside the harness
	// and the model the same boot carries) and the attempt record states it —
	// the prompt whose digest the reconciler's marker records is the prompt
	// that reached the worker. Required: the door refuses a start without one.
	Prompt string

	// WorkBaseSHA is, for a CARRIED dispatch, the base the carried work was
	// cut from (reconcile.Dispatch.WorkBaseSHA), carried through the door as
	// `work_base_sha` so the worker's container can see carried work it added
	// nothing to. Empty for a dispatch that carries nothing.
	WorkBaseSHA string

	// Repo is the ORCHESTRATOR'S OWN CHECKOUT of the project the worker
	// pushed to — the clone the reconciler runs in. It is not among the
	// fields Start needs (that dispatch creates no worktree and runs no
	// runner here), but it is the one thing collect reads the durable layer
	// through: the landing branch the container pushed is fetched into THIS
	// checkout, so the head a collect answers with is a commit the
	// reconciler's own integrate can resolve. The decision that collect is
	// the Go side's, from git, is recorded beside the door's contract
	// (sandbox-dispatch.ts, tick xev).
	Repo string

	// Remote is the name (or URL) of the remote the durable layer lives on
	// — the one the worker container pushed its landing branch to and the
	// one this checkout fetches it from. Defaults to "origin".
	Remote string

	// Attempt is the attempt number this start is for. It comes from the
	// caller because job_id is OPAQUE to an executor — the contract says the
	// reconciler owns its shape, so parsing an attempt out of it would be
	// this side deciding what the other side's identifier means. The
	// attempt is in the container's NAME on purpose: a redispatch after a
	// spent attempt must land in a FRESH container, and reusing the name is
	// how you inherit whatever broke it.
	Attempt int

	// StateDir is the executor's private state root, one directory per
	// attempt under it. The door needs none of it — it re-addresses by
	// identity — but this executor's own Start decisions do. Defaults to
	// $TICFAC_EXEC_STATE_DIR, else ~/.ticfac/exec/cloudflare-sandbox. It is
	// deliberately OUTSIDE the repository: an executor writing run state
	// into the tree it is running a job in would be dirtying the very
	// worktree the boundary diff reads.
	StateDir string

	// RequestTimeout bounds one door call. Zero is DefaultRequestTimeout.
	RequestTimeout time.Duration

	Now func() time.Time

	// writeFile is the state writer, so a test can inject a write that
	// silently does not land — the only way to see whether the read-back
	// after write is doing anything (Appendix A #7).
	writeFile func(path string, data []byte, perm fs.FileMode) error
}

// The reasons this executor refuses that the closed vocabulary does not name.
// A refusal whose reason a caller has to recover by matching on prose is the
// failure Appendix A #9 is about — so each operation this executor declines
// has its own value, and the reconciler records it in the feed as what it
// is: a decision about where the operation LIVES (tick xev, recorded beside
// the door's contract in sandbox-dispatch.ts), not a verdict on the work.
const (
	// RefusedCancelOwnedByFactory is the refusal Cancel answers. Cancel's
	// contract on this seam is revoke-then-signal: revoke the attempt's
	// credential, then stop its process. Neither half exists on this side of
	// the boundary. The credential a sandbox attempt holds is the RUN'S own
	// gateway token (D17) — one credential shared by every attempt of the
	// run, whose revocation is the run-level kill switch the door already
	// honours (403 run_token_revoked), not a per-attempt dispatch to revoke.
	// And stopping one container is the factory's own teardown — the
	// salvage door and teardownWorker the Worker-side executor owns, and
	// the queue-expiry sweep behind them — which no per-tick route crosses.
	// The reconciler's teardown records the refusal and continues; nothing
	// is half-cancelled and nothing pretends to have been stopped.
	RefusedCancelOwnedByFactory = "cancel_owned_by_factory"
	// RefusedNothingLocalToDispose is the refusal Dispose answers. This
	// executor owns no worktree, no local branch and no credential to
	// retire: the container belongs to the factory that booted it, and the
	// work's durability is the landing branch on the remote, which the
	// close retires — so there is nothing on this side of the HTTP
	// boundary to dispose of, and a dispose that answered "done" would be
	// claiming a teardown it never performed.
	RefusedNothingLocalToDispose = "nothing_local_to_dispose"
)

// Executor is one host, pointed at one factory door.
type Executor struct {
	opts   Options
	root   string
	client *Client
	now    func() time.Time
}

// New resolves the state root and the door once, so that every operation
// afterwards names the directory it works in and the endpoint it asks. The
// constructor validates the door's addressing BEFORE any dispatch can reach
// it: a factory URL or a run token that is missing is a dispatch that fails
// before the tick is claimed, not a run that discovers it three ticks in.
func New(opts Options) (*Executor, error) {
	if opts.FactoryURL == "" {
		opts.FactoryURL = os.Getenv("TICKS_FACTORY_URL")
	}
	if opts.Token == "" {
		opts.Token = os.Getenv("TICKS_FACTORY_TOKEN")
	}
	if opts.StateDir == "" {
		opts.StateDir = DefaultStateDir()
	}
	if opts.Attempt <= 0 {
		opts.Attempt = 1
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	client, err := NewClient(opts.FactoryURL, opts.Token, opts.RequestTimeout, opts.Now)
	if err != nil {
		return nil, err
	}
	return &Executor{opts: opts, root: opts.StateDir, client: client, now: opts.Now}, nil
}

// DefaultStateDir is where attempts are recorded when nothing says otherwise.
func DefaultStateDir() string {
	if dir := os.Getenv("TICFAC_EXEC_STATE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ticfac", "exec", ExecutorName)
	}
	return filepath.Join(home, ".ticfac", "exec", ExecutorName)
}

// stamp is one record timestamp.
func (e *Executor) stamp() string { return e.now().UTC().Format(time.RFC3339) }

// refuse is a refusal to act, carrying the REASON as its own value, so a
// caller recovers it without matching on prose. The type is the protocol's
// own — the same one the reconciler already reads — so the reasons this
// package adds ride the same shape as subprocess.Refused*.
func refuse(reason, format string, args ...any) *subprocess.Refusal {
	return &subprocess.Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// stateDirFor is the attempt's state directory beneath the state root. The
// job id already carries run, tick and attempt, so it is the whole key —
// hashed, because it contains the reconciler's own separators and is not a
// path component the reconciler or an operator should have to read.
func (e *Executor) stateDirFor(jobID string, attempt int) string {
	sum := sha256.Sum256([]byte(jobID + "\x00" + fmt.Sprint(attempt)))
	return filepath.Join(e.root, hex.EncodeToString(sum[:])[:16])
}

// storeAt is the store for one attempt's state directory, with the injected
// writer when a test supplied one.
func (e *Executor) storeAt(dir string) *store {
	st := newStore(dir)
	if e.opts.writeFile != nil {
		st.writeFile = e.opts.writeFile
	}
	return st
}

// ----------------------------------------------------------------- start ---

// Start asks the door to boot one attempt's worker container and returns the
// handle the door minted — WITHOUT waiting for the attempt to finish. What
// the door waits for is the dispatch being confirmed, which is a container
// boot, not the tick's work; the work goes on running inside the container.
//
// The order of the questions is the load-bearing part:
//
//   - the door is asked BY IDENTITY first, before anything is booted: an
//     attempt that already finished under this identity is REFUSED — a retry
//     is a new attempt number, and a fresh container booted over a settled
//     one would be a rival that inherits the name while the work it just
//     replaced is the completion contract;
//   - an attempt whose LOCAL record exists and whose container is live is
//     ADOPTED: the record's handle comes back, and no second dispatch is
//     made — the door would adopt it too, but there is no reason to spend a
//     boot-shaped round trip to learn what the record and the status already
//     agree on;
//   - an attempt whose local record exists and whose container nobody can
//     address is HELD (RefusedUnknown), never redispatched: the record is
//     durable evidence the dispatch once landed, and "the container is
//     gone" is a statement about the observer, not a licence to boot a
//     second one under the same name;
//   - no local record and no live container is the one honest fresh
//     dispatch: the door boots the container named by this attempt's
//     identity, and a second start that races the first is adopted by the
//     door rather than answered with a rival.
//
// A failed start records NOTHING. This deviates from the local executors,
// which write their attempt record before launching and leave it behind as
// diagnostic state on a substrate failure — and the deviation is the
// substrate's, not an oversight: the door is identity-addressed and
// adoption-safe, so a start that failed before the door answered can be
// retried with no risk of a rival (the door adopts whatever landed), while a
// pre-written record would hold the attempt for a person forever on a
// transport blip at dispatch time. The diagnostic state a failed boot leaves
// is the door's own — the container record the factory holds.
func (e *Executor) Start(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	attempt := e.opts.Attempt
	tickID := tickOf(spec)
	req := &startRequest{
		Epic:        e.opts.EpicID,
		TickID:      tickID,
		Attempt:     attempt,
		JobID:       spec.JobID,
		Role:        spec.Role,
		WriteRef:    spec.Source.WriteRef,
		BaseRef:     e.opts.BaseRef,
		Title:       e.opts.Title,
		BaseSHA:     spec.Source.BaseSHA,
		Model:       e.opts.Model,
		Harness:     e.opts.Harness,
		Prompt:      e.opts.Prompt,
		WallSeconds: spec.Limits.WallSeconds,
		WorkBaseSHA: e.opts.WorkBaseSHA,
	}
	if err := validateDoorFields(req); err != nil {
		return nil, err
	}
	dir := e.stateDirFor(spec.JobID, attempt)
	st := e.storeAt(dir)
	ctx := context.Background()

	// The door first, by identity — never a boot before the question "did
	// this identity already finish?" is answered. An unreachable door or a
	// door refusal here is an operational error, not a verdict: Start fails
	// and the tick is neither claimed nor dispatched.
	status, err := e.client.attemptStatus(ctx, tickID, attempt, spec.JobID)
	if err != nil {
		return nil, err
	}
	// The answer must be about THIS job. A door that answers for another —
	// one that keys by (tick, attempt) alone and so answers for the implement
	// attempt when asked about the repair of it — is not a verdict on this
	// job, and reading its "settled" as this job's is the hn6 cloud-run
	// stall: every repair refused before one ever booted.
	if status.JobID != spec.JobID {
		return nil, fmt.Errorf("the door answered for %s when asked about %s: a status for another job is no "+
			"verdict on this one, and a start refused or adopted on it would be decided by another job's record",
			status.JobID, spec.JobID)
	}
	if status.Terminal {
		return nil, refuse(subprocess.RefusedSettled,
			"attempt %d of %s already settled as %s under this identity: a retry is a new attempt number, "+
				"not this one again", attempt, spec.JobID, status.State)
	}

	// A6, in this executor's terms: adopt by stable identity. A local
	// record plus a live container is an adoption; a local record plus an
	// unaddressable container is a hold.
	if st.exists(fileAttempt) {
		record, readErr := st.readAttempt()
		switch {
		case readErr != nil:
			// The record EXISTS and this leg cannot read it — a schema
			// version from another release, a half-rewritten file. That is
			// a handle a later leg cannot address, and it is held for a
			// person rather than redispatched: a fresh container booted
			// behind an unreadable record would strand whatever the record
			// was the only address for.
			return nil, refuse(subprocess.RefusedUnknown,
				"the attempt record at %s cannot be read (%v): this attempt is held for a person, never redispatched",
				dir, readErr)
		case status.State == subprocess.StateLost:
			return nil, refuse(subprocess.RefusedUnknown,
				"attempt %d of %s is recorded as dispatched but the door cannot address its container: "+
					"it is held, never redispatched", record.Attempt, record.JobID)
		default:
			// Live. The record's own handle comes back: the container under
			// this identity is this attempt's, by the name nobody else would
			// boot under.
			return handleFor(record), nil
		}
	}

	// No local record. A live container here is the fresh-clone case — the
	// previous incarnation's state is gone, and the door is the only thing
	// that still knows the attempt — and an absent one is the window the
	// dispatch marker exists to make safe. Both go through the door's own
	// start route, which adopts a live work process instead of booting a
	// rival beside it.
	handle, adopted, err := e.client.startAttempt(ctx, req)
	if err != nil {
		return nil, err
	}
	if handle.JobID != spec.JobID {
		return nil, fmt.Errorf("the door minted handle %s for a start of %s: the credential names a run this "+
			"job id does not, and a container booted under it would not be this attempt's", handle.JobID, spec.JobID)
	}
	if handle.Attempt != attempt {
		return nil, fmt.Errorf("the door minted a handle for attempt %d, want %d", handle.Attempt, attempt)
	}
	payload, err := local(handle)
	if err != nil {
		return nil, err
	}
	// nwn's rule, applied to the model the door reports the container is ON —
	// which, since the door records its boots and an adoption reads the record
	// (tick dyo), is the RUNNING container's model for an adopted attempt too,
	// not an echo of what this dispatch asked for. The rule is the profile
	// package's one copy (profile.CloudRule): the cloud substrate runs Workers
	// AI models only, and an adopted container — one a previous incarnation
	// booted, before the rule or against it — is not exempt from the check a
	// fresh boot answers to.
	if !profile.IsWorkersAIModel(payload.Model) {
		return nil, fmt.Errorf("the door reports attempt %d of %s running on model %q, which is not "+
			"a Workers AI model (%s): the cloud substrate runs Workers AI models only, and a start that "+
			"recorded it would name a model this run cannot have dispatched",
			attempt, spec.JobID, payload.Model, strings.Join(profile.CloudRule.ModelNamespaces, ", "))
	}
	// The door names the model it booted the worker on. Anything but the one
	// asked for — an adoption of a container some other start booted, a door
	// that fell back to its own default — is refused before a record is
	// written, because the record's model is what every trace reads.
	if payload.Model != req.Model {
		return nil, fmt.Errorf("the door booted attempt %d of %s on model %q, not the %q its dispatch resolved: "+
			"a record naming the requested model over a worker running another is a provenance that lies",
			attempt, spec.JobID, payload.Model, req.Model)
	}
	// The door names the harness it bound the worker to, for the model's
	// reason (tick 9iz): anything but the one asked for — an adoption of a
	// container some other start booted, a door that fell back to its own
	// standing choice — is refused before a record is written.
	if payload.Harness != req.Harness {
		return nil, fmt.Errorf("the door booted attempt %d of %s on harness %q, not the %q its dispatch resolved: "+
			"a record naming the requested harness over a worker bound to another is a provenance that lies",
			attempt, spec.JobID, payload.Harness, req.Harness)
	}
	record := &attemptRecord{
		SchemaVersion: stateSchemaVersion,
		JobID:         handle.JobID,
		Attempt:       handle.Attempt,
		TickID:        tickID,
		State:         dir,
		Sandbox:       payload.Sandbox,
		ProcessID:     payload.ProcessID,
		BaseSHA:       payload.BaseSHA,
		Branch:        payload.Branch,
		WriteRef:      payload.WriteRef,
		Launched:      payload.Launched,
		Detail:        payload.Detail,
		RunID:         payload.RunID,
		EpicID:        payload.EpicID,
		Role:          payload.Role,
		Project:       payload.Project,
		BaseRef:       payload.BaseRef,
		Title:         payload.Title,
		Model:         payload.Model,
		Harness:       payload.Harness,
		Prompt:        req.Prompt,
		Adopted:       adopted,
		Spec:          spec,
		IssuedAt:      handle.IssuedAt,
	}
	// A7: the record is read back before anything acts on it. A handle for a
	// record that did not land is a job nobody can find.
	if err := st.writeAttempt(record); err != nil {
		return nil, err
	}
	return handleFor(record), nil
}

// ------------------------------------------------------- reattach settled ---

// ReattachSettled re-addresses an attempt that SETTLED while no orchestrator
// was watching it, so it can be collected. It never boots anything.
//
// Start refuses a settled identity (RefusedSettled), and must: a retry is a
// new attempt number. But a restarted orchestrator whose container came up
// on a FRESH disk has no attempt record for what the previous incarnation
// dispatched, so its adoption path asks Start — and an attempt that finished
// in between (epic hn6's cloud run: ltg settled as succeeded at 13:06, the
// orchestrator that dispatched it exited at 13:29, and the next one was
// refused at 13:35 "already settled") was rejected as collect_failed rather
// than collected, while its work sat on its landing branch. This is the
// other half of that refusal: the door says the identity settled, so the
// record the dead disk held is re-described from the dispatch — the same
// spec, the same base, the landing branch the door derives for this
// identity — and its handle is returned for Inspect (terminal) and
// CollectDetail (the branch) to rule on, as if the record had survived.
//
// It is the attempt's OWN job only. A role job run under the attempt number
// (a repair, a resolve) lands on a branch whose name carries a digest of its
// job id, and the reconciler already finishes a settled role job from its
// branch without this executor.
func (e *Executor) ReattachSettled(spec *subprocess.JobSpec) (*subprocess.JobHandle, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	attempt := e.opts.Attempt
	tickID := tickOf(spec)
	status, err := e.client.attemptStatus(context.Background(), tickID, attempt, spec.JobID)
	if err != nil {
		return nil, err
	}
	if status.JobID != spec.JobID {
		return nil, fmt.Errorf("the door answered for %s when asked about %s: a status for another job is no "+
			"verdict on this one", status.JobID, spec.JobID)
	}
	if !status.Terminal {
		return nil, fmt.Errorf("attempt %d of %s has not settled (the door reads it %s): only a settled attempt is "+
			"reattached for its collect, and a live one is adopted through Start", attempt, spec.JobID, status.State)
	}
	dir := e.stateDirFor(spec.JobID, attempt)
	st := e.storeAt(dir)
	if st.exists(fileAttempt) {
		record, readErr := st.readAttempt()
		if readErr != nil {
			return nil, refuse(subprocess.RefusedUnknown,
				"the attempt record at %s cannot be read (%v): this attempt is held for a person", dir, readErr)
		}
		return handleFor(record), nil
	}
	if e.opts.RunID == "" {
		return nil, fmt.Errorf("attempt %d of %s settled and its record is gone, and this executor was given no "+
			"run id to re-describe it from", attempt, spec.JobID)
	}
	if spec.JobID != attemptOwnJobID(e.opts.RunID, tickID, attempt) {
		return nil, fmt.Errorf("%s is a role job under attempt %d of %s, not the attempt's own job: its landing "+
			"branch is not re-derived here, and the reconciler finishes a settled role job from its branch",
			spec.JobID, attempt, tickID)
	}
	record := &attemptRecord{
		SchemaVersion: stateSchemaVersion,
		JobID:         spec.JobID,
		Attempt:       attempt,
		TickID:        tickID,
		State:         dir,
		BaseSHA:       spec.Source.BaseSHA,
		Branch:        attemptLandingBranch(e.opts.EpicID, attempt, tickID),
		WriteRef:      spec.Source.WriteRef,
		Launched:      true,
		Detail: fmt.Sprintf("reattached for its collect: the door reads it settled as %s, and the record the "+
			"orchestrator that dispatched it held went with that orchestrator's disk", status.State),
		RunID:    e.opts.RunID,
		EpicID:   e.opts.EpicID,
		Role:     spec.Role,
		BaseRef:  e.opts.BaseRef,
		Title:    e.opts.Title,
		Model:    e.opts.Model,
		Harness:  e.opts.Harness,
		Prompt:   e.opts.Prompt,
		Adopted:  true,
		Spec:     spec,
		IssuedAt: e.stamp(),
	}
	if err := st.writeAttempt(record); err != nil {
		return nil, err
	}
	return handleFor(record), nil
}

// AdoptSettledElsewhere describes ANOTHER run's settled attempt as this run's
// attempt `spec`, so the reconciler's collect can rule on the work it left.
// It never boots anything and never asks the door, which answers only for the
// credential's own run: the settlement comes in as evidence the caller read
// from the factory's record, and Inspect answers from it.
//
// It is the cross-run half of ReattachSettled (hn6's ltg): run_911b's worker
// settled succeeded with its work on `tick/hn6/attempt-4/ltg`, the run died
// before collecting it, and the run that took its claim over dispatched a
// fresh worker over finished work. The record names that attempt's landing
// branch and base — the spec is cut at it — and the other run's id, so the
// collect's per-run fallback (`…-<run id>`) is that run's too.
func (e *Executor) AdoptSettledElsewhere(spec *subprocess.JobSpec, runID, jobID string, attempt int, branch, evidence string) (*subprocess.JobHandle, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if runID == "" || jobID == "" || attempt < 1 || branch == "" {
		return nil, fmt.Errorf("a settled attempt of another run is addressed by its run, job, attempt and landing "+
			"branch; got run %q, job %q, attempt %d, branch %q", runID, jobID, attempt, branch)
	}
	dir := e.stateDirFor(spec.JobID, e.opts.Attempt)
	st := e.storeAt(dir)
	if st.exists(fileAttempt) {
		record, err := st.readAttempt()
		if err != nil {
			return nil, refuse(subprocess.RefusedUnknown,
				"the attempt record at %s cannot be read (%v): this attempt is held for a person", dir, err)
		}
		if record.SettledElsewhere == nil || record.SettledElsewhere.JobID != jobID {
			return nil, refuse(subprocess.RefusedLive,
				"the attempt record at %s is %s's, not a ruling on %s: nothing is re-described over it",
				dir, record.JobID, jobID)
		}
		return handleFor(record), nil
	}
	record := &attemptRecord{
		SchemaVersion: stateSchemaVersion,
		JobID:         spec.JobID,
		Attempt:       e.opts.Attempt,
		TickID:        tickOf(spec),
		State:         dir,
		BaseSHA:       spec.Source.BaseSHA,
		Branch:        branch,
		WriteRef:      spec.Source.WriteRef,
		Detail: fmt.Sprintf("rules on run %s's attempt %d (%s), which settled succeeded before that run ended: %s",
			runID, attempt, jobID, evidence),
		// The run whose container pushed the branch: the collect's per-run
		// landing fallback is spelled with it.
		RunID:   runID,
		EpicID:  e.opts.EpicID,
		Role:    spec.Role,
		BaseRef: e.opts.BaseRef,
		Title:   e.opts.Title,
		Model:   e.opts.Model,
		Harness: e.opts.Harness,
		Adopted: true,
		Spec:    spec,
		SettledElsewhere: &settledElsewhere{RunID: runID, JobID: jobID, Attempt: attempt,
			State: subprocess.StateSucceeded, Evidence: evidence},
		IssuedAt: e.stamp(),
	}
	if err := st.writeAttempt(record); err != nil {
		return nil, err
	}
	return handleFor(record), nil
}

// attemptOwnJobID is the job id of an attempt's own job — the door's
// attemptJobID (cloudflare/src/sandbox-executor.ts) and the reconciler's,
// one spelling: `run-<run>/tick-<tick>/attempt-<n>`.
func attemptOwnJobID(runID, tickID string, attempt int) string {
	return fmt.Sprintf("run-%s/tick-%s/attempt-%d", runID, tickID, attempt)
}

// attemptLandingBranch is the branch an attempt's own worker container
// pushes: `tick/<epic>/attempt-<n>/<tick>`, the door's attemptLandingBranch
// (cloudflare/src/worker-boot.ts) for a job with no role slot. The
// container's per-run fallback beside it is collect's to find.
func attemptLandingBranch(epicID string, attempt int, tickID string) string {
	return fmt.Sprintf("tick/%s/attempt-%d/%s", epicID, attempt, tickID)
}

// ---------------------------------------------------------------- inspect ---

// Inspect re-addresses a handle and reports what can be seen. It never
// dispatches, and it asks the door BY IDENTITY: run from the credential, tick
// and attempt from the path, the full job id from the query — so a handle survives the process that created
// it precisely because nothing about the answer depends on that process ever
// having existed. This is the operation the acceptance criterion names: a
// handle — even the minimal {"state": …} shape the reconciler's adoption
// path synthesizes from an attempt record, even a handle whose issuing
// process is gone — is re-queried for state.
//
// The cursor parameter is accepted and unused: this substrate has no
// incremental observation stream to resume, and each Inspect answers the
// whole state the door can see. The cursor the door hands back (null) rides
// the status verbatim, so a caller that resumes from it resumes from
// nothing — which is the honest answer, not a bug to paper over.
//
// A door this executor cannot REACH is an error, never a `lost`: reading an
// outage as absence is how an attempt gets written off while its container
// is still running.
func (e *Executor) Inspect(h *subprocess.JobHandle, cursor string) (*subprocess.JobStatus, error) {
	payload, err := local(h)
	if err != nil {
		return nil, err
	}
	if h.Attempt < 1 {
		return nil, fmt.Errorf("handle carries attempt %d: the door addresses an attempt by a positive integer", h.Attempt)
	}
	full, record, resolveErr := payload.resolved()
	// Another run's settled attempt this run's attempt rules on: the door
	// answers only for its own run, and the settlement is already recorded.
	if record != nil && record.SettledElsewhere != nil {
		return &subprocess.JobStatus{
			SchemaVersion: subprocess.SchemaVersion,
			JobID:         h.JobID,
			State:         record.SettledElsewhere.State,
			Terminal:      true,
			ObservedAt:    e.stamp(),
		}, nil
	}
	tickID := full.TickID
	if tickID == "" {
		if resolveErr != nil {
			return nil, fmt.Errorf("the handle states no tick id and its state record cannot be read: %w", resolveErr)
		}
		return nil, fmt.Errorf("the handle states no tick id: the door is addressed by identity, and this handle states none")
	}
	// The handle (or the record it resolved against) states which job this
	// is, and the door is asked about exactly that job: several jobs run
	// under one attempt number, and each is its own container.
	want := h.JobID
	if want == "" && record != nil {
		want = record.JobID
	}
	status, err := e.client.attemptStatus(context.Background(), tickID, h.Attempt, want)
	if err != nil {
		return nil, err
	}
	// The door derives the run from the credential; a job id it answers for
	// that is not the one asked about is the client and the door differing
	// about whose job this is, and it is never a status to act on.
	if want != "" && status.JobID != want {
		return nil, fmt.Errorf("the door answered for %s, not %s: the credential names a run this handle does not",
			status.JobID, want)
	}
	// A container that could not check out its start commit (the commit is
	// not on origin) is marked in the attempt's state before the exit is
	// named, so the collect that follows says so rather than reading the
	// empty landing branch as a job that answered nothing (epic hn6,
	// run_09ebaf29). Best effort: the observation carries the same fact.
	if payload.State != "" && exitedWith(status, sandboximage.ExitStartUnpublished) {
		_ = e.storeAt(payload.State).writeJSON(fileStartUnpublished, map[string]any{
			"observed_at": status.ObservedAt, "exit_code": sandboximage.ExitStartUnpublished,
		})
	}
	// The same for a container that died in its boot on a service outside
	// it — the gateway, origin (epic hn6, run_37b36bfe): the collect says so
	// rather than reading a job that never reached its harness as a failed
	// attempt at the tick.
	if code := bootExit(status); payload.State != "" && code != 0 {
		_ = e.storeAt(payload.State).writeJSON(fileInfrastructure, map[string]any{
			"observed_at": status.ObservedAt, "exit_code": code,
		})
	}
	nameExitClasses(status)
	return status, nil
}

// ChecksOutFromOrigin says this executor's jobs run off a checkout of the
// REMOTE, never of the orchestrator's own repository: the container clones
// origin at the dispatched start commit, so a commit only the orchestrator's
// clone holds is one it cannot start from (epic hn6, run_09ebaf29). The
// reconciler asks, and publishes every start commit before it dispatches here
// (reconcile.RemoteCheckout).
func (e *Executor) ChecksOutFromOrigin() bool { return true }

// JobsLiveAtFactory says this executor's jobs live in the factory, which
// keeps their records (boot, settlement, reclaim) and answers for them by
// identity: a `lost` from the door is its answer for one look, and the door
// settles a job whose container stopped under it. The reconciler re-asks a
// `lost` from such an executor rather than stopping the run for a person
// (reconcile.FactoryJobs; epic hn6, run_6d88e3de).
func (e *Executor) JobsLiveAtFactory() bool { return true }

// ------------------------------------- the operations decided elsewhere ---

// Cancel is refused, by decision (tick xev): the credential a sandbox attempt
// holds is the RUN'S own gateway token (D17) — shared by every attempt of
// the run, and revoked only as the run-level kill switch the door already
// honours — and stopping one container is the factory's own teardown, which
// no per-tick route crosses. Failing closed with a typed reason, because a
// stop nothing acknowledges is a stop the records say happened and the
// container never saw.
func (e *Executor) Cancel(h *subprocess.JobHandle) (*subprocess.CancelAck, error) {
	if _, err := local(h); err != nil {
		return nil, err
	}
	return nil, refuse(RefusedCancelOwnedByFactory,
		"cancel is the factory's, not this executor's (decided, tick xev): the credential a sandbox attempt holds is "+
			"the run's own gateway token, whose revocation is the run-level kill switch the door already honours, and "+
			"the container's teardown belongs to the factory that booted it — so there is no per-attempt dispatch to "+
			"revoke and no process on this side of the boundary to signal")
}

// Dispose is refused, by the same decision: this executor owns no worktree,
// no local branch and no credential to retire, and the container belongs to
// the factory that booted it. The work's durability is the landing branch on
// the remote, which the close retires.
func (e *Executor) Dispose(h *subprocess.JobHandle, opts subprocess.DisposeOptions) error {
	if _, err := local(h); err != nil {
		return err
	}
	return refuse(RefusedNothingLocalToDispose,
		"there is nothing on this side of the boundary to dispose of (decided, tick xev): this executor owns no "+
			"worktree, no local branch and no credential, the container belongs to the factory that booted it, and "+
			"the branch the work landed on is retired by the close")
}
