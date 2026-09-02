package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// One tick, from the graph to the close, with the compare-and-swap that proves
// each effect has not already happened in front of it.

// attemptHandle is what the dispatch marker carries. It is the reconciler's
// half of the identity: which job, which attempt, and the directory this run
// gave that dispatch so a RESTART ON A FRESH CLONE can find the attempt the
// previous reconciler started without guessing at an executor's naming.
//
// The executor's own handle is not written here, and could not be: the marker
// is created BEFORE the dispatch, because a marker written afterwards guards
// nothing.
type attemptHandle struct {
	Executor  string `json:"executor"`
	JobID     string `json:"job_id"`
	Attempt   int    `json:"attempt"`
	TickID    string `json:"tick_id"`
	Role      string `json:"role"`
	Repo      string `json:"repo"`
	Remote    string `json:"remote"`
	WriteRef  string `json:"write_ref"`
	BaseSHA   string `json:"base_sha"`
	StateRoot string `json:"state_root"`

	// Model and PromptDigest are the two halves of the profile that reach the
	// runner PROCESS — the model through the runner's own model flag, the
	// prompt as the role instruction the worker prompt opens with. They are
	// recorded here, in the marker's open handle object, because the closed
	// provenance record can say WHICH profile a job was dispatched under and
	// has nowhere to say that its prompt and model were actually applied.
	Model        string `json:"model"`
	PromptDigest string `json:"prompt_digest"`
}

func (a attemptHandle) asMap() map[string]any {
	return map[string]any{
		"executor": a.Executor, "job_id": a.JobID, "attempt": a.Attempt, "tick_id": a.TickID,
		"role": a.Role, "repo": a.Repo, "remote": a.Remote, "write_ref": a.WriteRef,
		"base_sha": a.BaseSHA, "state_root": a.StateRoot,
		"model": a.Model, "prompt_digest": a.PromptDigest,
	}
}

func handleFromMap(raw map[string]any) attemptHandle {
	get := func(key string) string {
		if value, ok := raw[key].(string); ok {
			return value
		}
		return ""
	}
	attempt := 0
	switch value := raw["attempt"].(type) {
	case float64:
		attempt = int(value)
	case int:
		attempt = value
	}
	return attemptHandle{
		Executor: get("executor"), JobID: get("job_id"), Attempt: attempt, TickID: get("tick_id"),
		Role: get("role"), Repo: get("repo"), Remote: get("remote"), WriteRef: get("write_ref"),
		BaseSHA: get("base_sha"), StateRoot: get("state_root"),
		Model: get("model"), PromptDigest: get("prompt_digest"),
	}
}

// processTick takes one tick from wherever it already is to closed.
func (r *Reconciler) processTick(ctx context.Context, entry planEntry) error {
	tick := entry.TickID

	// The tracker is the authority on whether a tick is closed. Reading it
	// before doing anything is the compare-and-swap for the close: a tick that
	// is already closed is an effect that already happened, and a restarted
	// run must not do it again.
	current, err := r.opts.Tracker.Show(ctx, tick)
	if err != nil {
		return fmt.Errorf("read tick %s: %w", tick, err)
	}
	if current.Status == "closed" {
		r.setTick(tick, "closed")
		r.record(tick, StageSkipped, "already closed in the tracker: %s", current.ClosedReason)
		if _, err := r.checkpoint(runstate.StateRunning, "tick "+tick+" was already closed"); err != nil {
			return err
		}
		return nil
	}

	// Appendix A #11's read site. A struck-out unit is held until a PERSON
	// releases it, and nothing about this dispatch asks the clock.
	unit := r.opts.EpicID + "/" + tick
	if r.MayDispatch(unit) == Held {
		r.record(tick, StageHeld, "%s is struck out and only a person releases it", unit)
		return r.refuse(RefusedHeld, tick, "%s is struck out: a rolling window bounds the window, not the subject, "+
			"so this dispatch waits for a person and not for the clock", unit)
	}

	if isRoleJob(entry.Role) {
		// Review and closeout are jobs like any other, on the same executor —
		// what differs is that the reconciler acts on the ANSWER they return
		// rather than on a branch it merges.
		return r.processRoleJob(ctx, entry)
	}

	handle, executor, marker, err := r.claimDispatch(ctx, entry)
	if err != nil {
		return err
	}

	status, err := r.waitForSettlement(ctx, handle, executor, marker)
	if err != nil {
		return err
	}

	collected, err := r.collect(handle, executor, marker, status)
	if err != nil {
		return err
	}

	merged, err := r.integrate(marker, collected)
	if err != nil {
		return err
	}

	if err := r.gateAndClose(ctx, entry, marker, collected, merged); err != nil {
		return err
	}

	// Cleanup is LAST, and only after the close. A cleanup before the close
	// throws away the only copy of what was closed.
	r.cleanUp(handle, executor, marker)
	return nil
}

// claimDispatch is the dispatch, and the compare-and-swap in front of it.
//
// The order is the contract's: create the marker on origin, and only then
// claim the tick and start the job. A refused create means another reconciler
// already dispatched this attempt — so this one adopts it and does not start
// anything.
func (r *Reconciler) claimDispatch(ctx context.Context, entry planEntry) (*subprocess.JobHandle, Executor, attemptHandle, error) {
	tick := entry.TickID

	if _, err := r.store.Fetch(); err != nil {
		return nil, nil, attemptHandle{}, err
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return nil, nil, attemptHandle{}, err
	}
	for _, existing := range attempts {
		if existing.TickID != tick {
			continue
		}
		if !r.guarded(guardNeverRedispatchLive) {
			// The guard is off: fall through and dispatch over whatever the
			// previous incarnation started, which is the bug the guard exists
			// for — one tick, two jobs, and the run pays for both.
			break
		}
		// Appendix A #6: an attempt under this identity has already been
		// dispatched, so it is ADOPTED. Nothing is started, and a live one is
		// never redispatched.
		marker := handleFromMap(existing.JobHandle)
		handle, executor, err := r.adopt(marker)
		if err != nil {
			return nil, nil, marker, err
		}
		r.setAttempt(tick, existing.Attempt)
		r.record(tick, StageAdopted, "attempt %d was already dispatched; it is adopted by identity, never redispatched",
			existing.Attempt)
		return handle, executor, marker, nil
	}

	number := len(attempts) + 1
	for _, existing := range attempts {
		if existing.Attempt >= number {
			number = existing.Attempt + 1
		}
	}
	dispatch, marker := r.planDispatch(entry, number)

	r.setTick(tick, "ready")
	if _, err := r.checkpoint(runstate.StateDispatching, fmt.Sprintf("dispatching %s as attempt %d", tick, number)); err != nil {
		return nil, nil, marker, err
	}

	outcome, err := r.store.PutAttempt(runstate.Attempt{
		Attempt:      number,
		TickID:       tick,
		DispatchedAt: r.now().UTC().Format(time.RFC3339),
		JobHandle:    marker.asMap(),
		Provenance:   r.attemptProvenance(dispatch),
	})
	if err != nil {
		return nil, nil, marker, fmt.Errorf("record the dispatch of %s: %w", tick, err)
	}
	if !outcome.EffectPermitted() {
		// The loser of a dispatch race is refused by the repository, not by a
		// lock it might have lost — and it must NOT start a job.
		if _, err := r.store.Fetch(); err != nil {
			return nil, nil, marker, err
		}
		recorded, ok, err := r.store.Attempt(number)
		if err != nil || !ok {
			return nil, nil, marker, fmt.Errorf("attempt %d of %s is on origin and unreadable: %v", number, tick, err)
		}
		marker = handleFromMap(recorded.JobHandle)
		handle, executor, adoptErr := r.adopt(marker)
		if adoptErr != nil {
			return nil, nil, marker, adoptErr
		}
		r.record(tick, StageAdopted, "the dispatch marker was already on origin (%s); this reconciler adopted rather than dispatching", outcome)
		return handle, executor, marker, nil
	}

	// Appendix A #7: the marker is read back from ORIGIN before anything acts
	// on it. A write that silently did not land must not look like a dispatch
	// somebody can find — and this one is what stops the next incarnation from
	// dispatching again.
	if _, err := r.store.Fetch(); err != nil {
		return nil, nil, marker, err
	}
	if _, ok, err := r.store.Attempt(number); err != nil || !ok {
		return nil, nil, marker, fmt.Errorf(
			"the dispatch marker for %s attempt %d did not land on %s: nothing is started behind a record that "+
				"does not exist (%v)", tick, number, r.opts.Remote, err)
	}

	// The effect, now that the marker proves it has not happened.
	if _, err := r.opts.Tracker.Claim(ctx, tick, r.opts.Owner); err != nil {
		return nil, nil, marker, fmt.Errorf("claim %s: %w", tick, err)
	}
	r.record(tick, StageClaimed, "claimed for %s", r.opts.Owner)

	executor, err := r.opts.NewExecutor(dispatch)
	if err != nil {
		return nil, nil, marker, fmt.Errorf("build the executor for %s: %w", tick, err)
	}
	handle, err := executor.Start(r.jobSpec(dispatch))
	if err != nil {
		return nil, nil, marker, r.startFailure(tick, err)
	}
	r.noteAlive(dispatch.JobID)
	r.setAttempt(tick, number)
	r.setTick(tick, "dispatched")
	r.record(tick, StageDispatched, "attempt %d started as %s", number, dispatch.JobID)
	if _, err := r.checkpoint(runstate.StateRunning, fmt.Sprintf("%s is running as attempt %d", tick, number)); err != nil {
		return nil, nil, marker, err
	}
	return handle, executor, marker, nil
}

// startFailure keeps the executor's typed refusals typed. "Nobody can say
// whether it is running" is not "nothing is running", and it never becomes a
// redispatch here either.
func (r *Reconciler) startFailure(tick string, err error) error {
	if refusal, ok := subprocess.AsRefusal(err); ok {
		switch refusal.Reason {
		case subprocess.RefusedUnknown, subprocess.RefusedLive:
			return r.refuse(RefusedUnaddressed, tick,
				"the executor holds attempt of %s rather than starting it: %s", tick, refusal.Message)
		}
		return r.refuse(RefusedCollect, tick, "the executor refused to start %s: %s", tick, refusal.Message)
	}
	return fmt.Errorf("start %s: %w", tick, err)
}

// planDispatch decides everything about a dispatch before the executor is
// asked for anything — including the budget, which is clamped here so the job
// is issued the number that will govern.
func (r *Reconciler) planDispatch(entry planEntry, number int) (Dispatch, attemptHandle) {
	jobID := fmt.Sprintf("run-%s/tick-%s/attempt-%d", r.runID, entry.TickID, number)
	stateDir := filepath.Join(r.opts.ExecStateRoot, r.runID, entry.TickID, fmt.Sprintf("%d", number))

	// A role job is dispatched at the CONTROLLER's state — the integration
	// branch as origin has it now — because that is what it is about. An
	// implementation tick branches from the run's base like any other.
	base := r.base
	if isRoleJob(entry.Role) {
		base = r.controllerBase()
	}
	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: entry.TickID, Attempt: number,
		JobID: jobID, Role: entry.Role, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: "refs/heads/tick/" + entry.TickID, BaseSHA: base, StateDir: stateDir,
		Profile: r.profileFor(entry.Role),
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}
	marker := attemptHandle{
		Executor: subprocess.ExecutorName, JobID: jobID, Attempt: number, TickID: entry.TickID,
		Role: entry.Role, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: dispatch.WriteRef, BaseSHA: base, StateRoot: stateDir,
		Model: dispatch.Profile.Model, PromptDigest: promptDigest(dispatch.Profile),
	}
	return dispatch, marker
}

func (r *Reconciler) jobSpec(d Dispatch) *subprocess.JobSpec {
	return &subprocess.JobSpec{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         d.JobID,
		Role:          d.Role,
		Source: subprocess.Source{
			Repository: d.Repo,
			BaseSHA:    d.BaseSHA,
			WriteRef:   d.WriteRef,
		},
		Capabilities:   subprocess.Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []subprocess.Input{{Kind: "tick", ID: d.TickID}, {Kind: "epic", ID: d.EpicID}},
		OutputSchema:   outputSchemaFor(d.Role),
		ArtifactPrefix: "runs/" + d.RunID + "/" + d.TickID + "/",
		Credentials: subprocess.Credentials{
			Model:  subprocess.ModelCredential{Shorthand: "issued-by-host"},
			Source: sourceCredentialFor(d.Role),
		},
		// The EFFECTIVE budget, not the requested one: a job is issued the
		// number that will govern (Appendix A #12).
		Limits: subprocess.Limits{WallSeconds: r.opts.WallSeconds, MaxCostUSD: d.BudgetUSD},
	}
}

// sourceCredentialFor is the source half of the job's credentials. A read-only
// grade carries NO write_ref_prefix — the contract refuses one, and the reason
// is the point: read-only means the issuer hands out no push credential, so
// there is no namespace left to bound.
func sourceCredentialFor(role string) subprocess.SourceCredential {
	if sourceGradeFor(role) == "read-only" {
		return subprocess.SourceCredential{Grant: &subprocess.SourceGrant{Issuer: "host", Grade: "read-only"}}
	}
	return subprocess.SourceCredential{
		Grant: &subprocess.SourceGrant{Issuer: "host", Grade: "write", WriteRefPrefix: "refs/heads/tick/"},
	}
}

// adopt re-addresses an attempt somebody else dispatched — including a
// previous incarnation of this reconciler, which is what a restart is.
//
// It never dispatches. If the executor has an attempt under the dispatch's
// private state root, the handle for it is reconstructed and INSPECTED; if
// there is none, nothing was ever started and the job is started now.
func (r *Reconciler) adopt(marker attemptHandle) (*subprocess.JobHandle, Executor, error) {
	executor, err := r.opts.NewExecutor(r.dispatchFor(marker))
	if err != nil {
		return nil, nil, fmt.Errorf("build the executor for %s: %w", marker.TickID, err)
	}
	state, found := findAttemptState(marker.StateRoot)
	if !found {
		// The marker landed and the dispatch did not: the previous reconciler
		// died in the window the marker exists to make safe. Nothing is
		// running, so this one starts it.
		handle, err := executor.Start(r.jobSpec(r.dispatchFor(marker)))
		if err != nil {
			return nil, nil, r.startFailure(marker.TickID, err)
		}
		r.noteAlive(marker.JobID)
		return handle, executor, nil
	}
	handle := &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         marker.JobID,
		Attempt:       marker.Attempt,
		Executor:      subprocess.ExecutorName,
		Handle:        map[string]any{"state": state},
	}
	status, err := executor.Inspect(handle, "")
	if err != nil {
		return nil, nil, fmt.Errorf("inspect the adopted attempt of %s: %w", marker.TickID, err)
	}
	if status.State == subprocess.StateLost && r.guarded(guardSettleFromEvidence) {
		return nil, nil, r.refuse(RefusedUnaddressed, marker.TickID,
			"attempt %d of %s cannot be addressed and has not settled: it is held, never redispatched",
			marker.Attempt, marker.TickID)
	}
	r.noteAlive(marker.JobID)
	return handle, executor, nil
}

func (r *Reconciler) dispatchFor(marker attemptHandle) Dispatch {
	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
		JobID: marker.JobID, Role: marker.Role, Repo: marker.Repo, Remote: marker.Remote,
		WriteRef: marker.WriteRef, BaseSHA: marker.BaseSHA, StateDir: marker.StateRoot,
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}
	if dispatch.Repo == "" {
		dispatch.Repo = r.opts.Repo
	}
	if dispatch.Role == "" {
		dispatch.Role = "implement-tick"
	}
	dispatch.Profile = r.profileFor(dispatch.Role)
	return dispatch
}

// findAttemptState locates the executor's own state directory for a dispatch.
//
// The reconciler gave this dispatch a directory of its own, so the search is
// unambiguous: exactly one attempt record can be under it. What it deliberately
// does not do is compute the executor's naming — that is the executor's, and a
// reconciler that recomputed it would be a reconciler that breaks when the
// executor renames a directory.
func findAttemptState(root string) (string, bool) {
	if root == "" {
		return "", false
	}
	found := ""
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !entry.IsDir() && entry.Name() == "attempt.json" {
			found = filepath.Dir(path)
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}

// ------------------------------------------------------------- the wait ---

// waitForSettlement addresses the job until it settles.
//
// Two rules meet here and neither is negotiable. Appendix A #3: no step
// outlives the host's cap, so a long wait is spread across bounded legs, and
// each leg RE-DERIVES what it knows from durable facts rather than carrying
// the previous leg's memory. Appendix A #4: the interval at which a live job is
// addressed is the keepalive, and it stays well under the substrate's wipe
// threshold.
func (r *Reconciler) waitForSettlement(ctx context.Context, handle *subprocess.JobHandle, executor Executor, marker attemptHandle) (*subprocess.JobStatus, error) {
	step := r.OpenStep(r.stepCap)
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		status, err := executor.Inspect(handle, cursor)
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", marker.TickID, err)
		}
		if status.Cursor != nil {
			cursor = *status.Cursor
		}
		if status.Terminal {
			r.record(marker.TickID, StageWaiting, "settled as %s", status.State)
			return status, nil
		}
		if status.State == subprocess.StateLost && r.guarded(guardSettleFromEvidence) {
			return nil, r.refuse(RefusedUnaddressed, marker.TickID,
				"attempt %d of %s cannot be addressed and has not settled: nobody can say whether it is running, "+
					"which is not the same as nothing running", marker.Attempt, marker.TickID)
		}

		// The poll IS the keepalive. Its answer is about the substrate, not
		// about the job: a job that went unaddressed past the threshold is
		// gone, whatever the last status said.
		if r.Poll(marker.JobID) == Wiped {
			return nil, r.refuse(RefusedWiped, marker.TickID,
				"attempt %d of %s went unaddressed for longer than the substrate's wipe threshold of %s",
				marker.Attempt, marker.TickID, r.wipeThreshold)
		}

		if step.Spend(r.pollInterval) == ExceededCap {
			// This leg is over. The next one is a FRESH step that re-derives
			// its state from durable facts rather than continuing this one.
			if _, err := r.store.Fetch(); err != nil {
				return nil, err
			}
			if checkpoint, ok, err := r.store.Checkpoint(); err == nil && ok {
				r.sequence, r.ticks = checkpoint.Sequence, checkpoint.Ticks
			}
			step = r.OpenStep(r.stepCap)
			continue
		}
		r.sleep(r.pollInterval)
	}
}

// ---------------------------------------------------------- the collect ---

func (r *Reconciler) collect(handle *subprocess.JobHandle, executor Executor, marker attemptHandle, status *subprocess.JobStatus) (*subprocess.Collection, error) {
	if _, err := r.checkpoint(runstate.StateCollecting, fmt.Sprintf("collecting %s attempt %d", marker.TickID, marker.Attempt)); err != nil {
		return nil, err
	}
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		return nil, fmt.Errorf("collect %s: %w", marker.TickID, err)
	}
	r.setTick(marker.TickID, "reported")
	r.record(marker.TickID, StageCollected, "verdict %s (%s)", collected.Verdict, collected.Result.Outcome)

	// Appendix A #10's reporting half: a boundary that refuses silently tells
	// nobody the model tried. The executor enforced it; the reconciler says so
	// where a person reading the run will find it, and refuses the merge.
	verdict := collected.Verdict
	if len(collected.BoundaryViolations) > 0 {
		if r.guarded(guardSubstrateEnforcesBoundary) {
			r.setTick(marker.TickID, "rejected")
			r.record(marker.TickID, StageRejected, "boundary violation: %s", strings.Join(collected.BoundaryViolations, ", "))
			return nil, r.refuse(RefusedBoundary, marker.TickID,
				"attempt %d of %s wrote under an authority that is not its own (%s): %s",
				marker.Attempt, marker.TickID, strings.Join(collected.BoundaryViolations, ", "), collected.Message)
		}
		// The negative control: nothing is reported and nothing is refused, so
		// the attempt's tracker writes reach the integration branch unnoticed —
		// which is the bug the guard exists for.
		verdict = subprocess.VerdictReadyToMerge
	}
	if verdict != subprocess.VerdictReadyToMerge {
		r.setTick(marker.TickID, "rejected")
		r.record(marker.TickID, StageRejected, "%s: %s", collected.Verdict, collected.Message)
		return nil, r.refuse(RefusedCollect, marker.TickID, "attempt %d of %s is %s: %s",
			marker.Attempt, marker.TickID, collected.Verdict, collected.Message)
	}
	_ = status
	return collected, nil
}

// --------------------------------------------------------- the clean-up ---

// cleanUp is the last thing that happens to an attempt, and it happens only
// after the close.
//
// The order inside it is Appendix A #1's: the credential dies first, and only
// then is the attempt torn down. A container torn down before its credential is
// revoked can spend on the way out.
func (r *Reconciler) cleanUp(handle *subprocess.JobHandle, executor Executor, marker attemptHandle) {
	reason := fmt.Sprintf("attempt %d of %s is merged into %s and the tick is closed",
		marker.Attempt, marker.TickID, r.branch)
	if _, err := executor.Cancel(handle); err != nil {
		r.record(marker.TickID, StageCleanedUp, "the attempt's credential could not be revoked: %v", err)
		return
	}
	if err := executor.Dispose(handle, subprocess.DisposeOptions{Reason: reason}); err != nil {
		r.record(marker.TickID, StageCleanedUp, "the attempt was not disposed: %v", err)
		return
	}
	r.record(marker.TickID, StageCleanedUp, "%s", reason)
}

// DefaultExecutor is the factory a production run uses: the local subprocess
// executor, one per dispatch, pointed at a state directory this run owns.
//
// Three of the profile's four fields reach the executor here, as HOST
// configuration: the runner it launches, the model it launches it on, and the
// role prompt the worker prompt opens with. None of them is a JobSpec field —
// the protocol's records are closed, and a field invented on this side would be
// one the reconciler's own contract does not have. `runner` is the fallback an
// operator names on the command line, for a dispatch whose profile resolved
// none.
func DefaultExecutor(runner string, runnerArgv []string, pushInterval time.Duration) func(Dispatch) (Executor, error) {
	return func(d Dispatch) (Executor, error) {
		supervisor, err := supervisorArgv()
		if err != nil {
			return nil, err
		}
		// A local, not the captured fallback: a profile that routed one
		// dispatch must not become the default for the next one.
		dispatched, model, rolePrompt := runner, "", ""
		if d.Profile != nil {
			if d.Profile.Runner != "" {
				dispatched = d.Profile.Runner
			}
			model, rolePrompt = d.Profile.Model, d.Profile.Prompt
		}
		return subprocess.New(subprocess.Options{
			Repo:           d.Repo,
			StateDir:       d.StateDir,
			Runner:         dispatched,
			Model:          model,
			RolePrompt:     rolePrompt,
			RunnerArgv:     runnerArgv,
			SupervisorArgv: supervisor,
			Remote:         d.Remote,
			Attempt:        d.Attempt,
			PushInterval:   pushInterval,
		})
	}
}

// CheckExecutor reports whether this build has an executor behind the
// four-operation protocol. It is what `ticfac run-epic` asks before it does
// anything: a build that cannot start, inspect, cancel or collect a job must
// refuse rather than report a run it did not make.
func CheckExecutor() error {
	_, err := supervisorArgv()
	return err
}

// supervisorArgv finds the executor binary that supervises an attempt. The
// reconciler's own executable is not it: `ticfac supervise` is not a command,
// and defaulting to it would spawn a supervisor that exits with a usage error
// and leaves an attempt nobody is watching.
func supervisorArgv() ([]string, error) {
	const name = "ticfac-exec-subprocess"
	if self, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(self), name)
		if info, err := os.Stat(beside); err == nil && !info.IsDir() {
			return []string{beside, "supervise"}, nil
		}
	}
	found, err := osexec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s is not beside this executable or on PATH: it is what supervises an attempt, "+
			"and a run without it would start jobs nothing is watching", name)
	}
	return []string{found, "supervise"}, nil
}
