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
)

// The executor: start and inspect over the door, collect from git (the
// decided placement, tick xev), and the two operations this side of the
// boundary has nothing to act on, refused typed.
//
// Nothing here crosses the protocol seam that is not already crossed by the
// records internal/exec/subprocess owns: the factory's base URL, the run's
// own gateway token, the epic, the base ref and the tick's title are host
// configuration (Options), because the protocol records are closed and a
// field invented here would be one the reconciler ignores. The REPOSITORY
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
		Epic:     e.opts.EpicID,
		TickID:   tickID,
		Attempt:  attempt,
		Role:     spec.Role,
		WriteRef: spec.Source.WriteRef,
		BaseRef:  e.opts.BaseRef,
		Title:    e.opts.Title,
		BaseSHA:  spec.Source.BaseSHA,
		Model:    e.opts.Model,
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
	status, err := e.client.attemptStatus(ctx, tickID, attempt)
	if err != nil {
		return nil, err
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

// ---------------------------------------------------------------- inspect ---

// Inspect re-addresses a handle and reports what can be seen. It never
// dispatches, and it asks the door BY IDENTITY: run from the credential, tick
// and attempt from the path — so a handle survives the process that created
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
	tickID := full.TickID
	if tickID == "" {
		if resolveErr != nil {
			return nil, fmt.Errorf("the handle states no tick id and its state record cannot be read: %w", resolveErr)
		}
		return nil, fmt.Errorf("the handle states no tick id: the door is addressed by identity, and this handle states none")
	}
	status, err := e.client.attemptStatus(context.Background(), tickID, h.Attempt)
	if err != nil {
		return nil, err
	}
	// The door derives the job id from the credential; the handle (or the
	// record it resolved against) states what this client asked about. A
	// disagreement is the client and the door differing about whose attempt
	// this is, and it is never a status to act on.
	want := h.JobID
	if want == "" && record != nil {
		want = record.JobID
	}
	if want != "" && status.JobID != want {
		return nil, fmt.Errorf("the door answered for %s, not %s: the credential names a run this handle does not",
			status.JobID, want)
	}
	return status, nil
}

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
