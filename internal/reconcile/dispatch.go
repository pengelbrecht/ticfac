package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// One tick, from the graph to the close, with the compare-and-swap that proves
// each effect has not already happened in front of it.

// attemptHandle is what the dispatch marker carries. It is the reconciler's
// half of the identity: which job, which attempt, and (in StateRoot) the
// directory this run gave that dispatch so a RESTART ON A FRESH CLONE can find
// the attempt the previous reconciler started without guessing at an
// executor's naming.
//
// StateRoot and Repo are both HOST paths — StateRoot under this run's
// ExecStateRoot, Repo the reconciler's own checkout — and neither rides into
// the durable marker asMap() writes to origin (SPEC's public-repo guard: no
// host-absolute path in a committed record). Neither needs to: StateRoot is a
// pure function of durable facts already on the marker (ExecStateRoot, the run
// id, the tick id, the attempt number), so execStateDir recomputes it
// identically on every restart, and Repo is host configuration a restarted
// reconciler is given again through Options — dispatchFor falls back to
// r.opts.Repo whenever the marker's own Repo is empty. handleFromMap leaves
// both empty; a caller that is about to adopt fills StateRoot in with
// execStateDir first, and Repo resolves itself through that fallback.
//
// The executor's own handle is not written here, and could not be: the marker
// is created BEFORE the dispatch, because a marker written afterwards guards
// nothing.
type attemptHandle struct {
	Executor string `json:"executor"`
	JobID    string `json:"job_id"`
	Attempt  int    `json:"attempt"`
	TickID   string `json:"tick_id"`

	// Try is which try of its own tick this dispatch is (tick vw0). It rides
	// the marker for the same reason Tier and Touch do: a later leg — an
	// adopt that finds the marker landed but nothing started — rebuilds the
	// dispatch and can still START the attempt, and the try it was dispatched
	// as is a fact about the dispatch, not something the next incarnation
	// should re-derive from a run-wide counter that has since moved. Zero on
	// markers written before the field existed.
	Try       int    `json:"try"`
	Role      string `json:"role"`
	Repo      string `json:"-"`
	Remote    string `json:"remote"`
	WriteRef  string `json:"write_ref"`
	BaseSHA   string `json:"base_sha"`
	StateRoot string `json:"-"`

	// Model and PromptDigest are the two halves of the profile that reach the
	// runner PROCESS — the model through the runner's own model flag, the
	// prompt as the role instruction the worker prompt opens with. They are
	// recorded here, in the marker's open handle object, because the closed
	// provenance record can say WHICH profile a job was dispatched under and
	// has nowhere to say that its prompt and model were actually applied.
	Model        string `json:"model"`
	PromptDigest string `json:"prompt_digest"`

	// Tier is the tier this dispatch DERIVED (tick 5eq), "" when it runs at
	// the role's own base values. It is recorded here for the same reason as
	// Model and PromptDigest, and for one more: the closed provenance object
	// ($defs.provenance in the pinned contract bundle) has no tier field, so
	// the marker is the ONLY durable record that says which tier a dispatch
	// used. An over-tiered run is exactly the thing nobody could audit before
	// this existed. The tier is reconstructible from provenance only in
	// digest form (ProfileDigest covers the tier-resolved profile), which is
	// an answer to "is this the same profile" — not to "which tier was this".
	Tier string `json:"tier"`

	// SubstrateProtocol and SubstrateServerVersion are the substrate this
	// dispatch was STARTED under — the versioned thing its executor drives,
	// observed at the build that ran the job (tick to1, epic av8). They ride
	// the marker for the same reason Tier does, and for one more: the
	// attempt was dispatched under THIS substrate, and a later leg — after
	// the substrate was upgraded mid-run — must record what the dispatch
	// used, not what it would observe today. Zero values are a substrate
	// that states no protocol (a local process).
	SubstrateProtocol      int    `json:"substrate_protocol"`
	SubstrateServerVersion string `json:"substrate_server_version"`

	// Touch is the files this tick DECLARED it expects to touch (tick 01u):
	// the tick's touch: labels, parsed and normalised at PLANNING time so the
	// check is a pure function of what the tracker said. It rides the marker
	// for the same reason Tier does — the merge that holds a worker to its
	// declaration runs long after the plan this incarnation read, and an
	// adopted attempt is held to the declaration it was DISPATCHED under, not
	// to whatever the tracker says today.
	Touch []string `json:"touch"`

	// ResumedFrom states that this dispatch starts from the work of a
	// RELEASED attempt — the person's --carry-work settlement (tick 0z0):
	// which attempt, which ref its work is on, the commit this dispatch was
	// cut from, and who released it. It rides the marker for the same reason
	// Tier does: the closed provenance object has no "resumed_from" field and
	// the bundle is not this tick's to change, so the marker's open handle is
	// where the explicit claim lives — beside the two closed fields that
	// already say it (provenance's source_ref and source_sha, which for a
	// carried dispatch ARE the released attempt's ref and commit). A marker
	// that resumed from nothing states it as null, never omits it, for the
	// same reason every required-and-null provenance field does.
	ResumedFrom *resumedFrom `json:"resumed_from"`
}

// resumedFrom is one dispatch's answer to "this work came from a released
// attempt": the attempt a person released, the ref its commits live on, the
// commit the new attempt is cut from, and the person who released it. Every
// field survives the restart through the marker's open handle, exactly as
// Tier and the substrate do — a later leg reads what the dispatch recorded,
// never what it would infer today.
type resumedFrom struct {
	TickID     string `json:"tick_id"`
	Attempt    int    `json:"attempt"`
	WriteRef   string `json:"write_ref"`
	SHA        string `json:"sha"`
	ReleasedBy string `json:"released_by"`
}

// asMap is the durable form of the marker: everything BUT StateRoot and Repo,
// both host paths recovered on the read side (see attemptHandle) rather than
// ever committed.
func (a attemptHandle) asMap() map[string]any {
	return map[string]any{
		"executor": a.Executor, "job_id": a.JobID, "attempt": a.Attempt, "tick_id": a.TickID,
		"try":  a.Try,
		"role": a.Role, "remote": a.Remote, "write_ref": a.WriteRef,
		"base_sha": a.BaseSHA,
		"model":    a.Model, "prompt_digest": a.PromptDigest, "tier": a.Tier, "touch": a.Touch,
		"substrate_protocol": a.SubstrateProtocol, "substrate_server_version": a.SubstrateServerVersion,
		// Null when this dispatch resumed from no released attempt, never
		// omitted — "no resume" is a claim, and a reader that cannot tell it
		// from an unrecorded one is a reader guessing at provenance.
		"resumed_from": a.ResumedFrom,
	}
}

// handleFromMap reads a marker's durable fields back. StateRoot and Repo are
// not among them (see attemptHandle): a caller that is about to adopt this
// marker sets StateRoot from execStateDir, and Repo resolves itself through
// dispatchFor's fallback to r.opts.Repo.
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
	// Touch reaches this reader as []any — the marker is stored and read as
	// JSON on origin — so both shapes are decoded, and anything that is not a
	// string is skipped rather than guessed at.
	var touch []string
	switch value := raw["touch"].(type) {
	case []string:
		touch = value
	case []any:
		for _, one := range value {
			if path, ok := one.(string); ok {
				touch = append(touch, path)
			}
		}
	}
	substrateProtocol := 0
	switch value := raw["substrate_protocol"].(type) {
	case float64:
		substrateProtocol = int(value)
	case int:
		substrateProtocol = value
	}
	// Try rides the marker like Attempt does, so it arrives the same way — as
	// a JSON number — and is absent on markers written before the field
	// existed, which read as the zero value.
	try := 0
	switch value := raw["try"].(type) {
	case float64:
		try = int(value)
	case int:
		try = value
	}
	// ResumedFrom arrives as the nested object the marker stores it as —
	// JSON on origin — or as nil for a dispatch that resumed from nothing.
	var resumed *resumedFrom
	if fields, ok := raw["resumed_from"].(map[string]any); ok {
		resumed = &resumedFrom{}
		resumed.TickID, _ = fields["tick_id"].(string)
		switch value := fields["attempt"].(type) {
		case float64:
			resumed.Attempt = int(value)
		case int:
			resumed.Attempt = value
		}
		resumed.WriteRef, _ = fields["write_ref"].(string)
		resumed.SHA, _ = fields["sha"].(string)
		resumed.ReleasedBy, _ = fields["released_by"].(string)
	}
	return attemptHandle{
		Executor: get("executor"), JobID: get("job_id"), Attempt: attempt, TickID: get("tick_id"),
		Try:  try,
		Role: get("role"), Remote: get("remote"), WriteRef: get("write_ref"),
		BaseSHA: get("base_sha"),
		Model:   get("model"), PromptDigest: get("prompt_digest"), Tier: get("tier"),
		SubstrateProtocol:      substrateProtocol,
		SubstrateServerVersion: get("substrate_server_version"),
		Touch:                  touch,
		ResumedFrom:            resumed,
	}
}

// execStateDir is the directory one dispatch's executor state lives under: a
// pure function of the run's own ExecStateRoot plus the run id, tick id and
// attempt number, all of which are already durable facts elsewhere. Because it
// is deterministic, it is recomputed rather than carried in a durable record —
// which is what keeps a host-specific path out of every record this run pushes
// to origin.
func (r *Reconciler) execStateDir(tickID string, attempt int) string {
	return filepath.Join(r.opts.ExecStateRoot, r.runID, tickID, fmt.Sprintf("%d", attempt))
}

// settleBeforeDispatch is everything the run decides about a tick BEFORE it
// dispatches anything: whether the tracker already closed it, whether a person
// struck it out, and whether it is a role job — which is dispatched and acted
// on in one piece rather than merged like a branch.
//
// It reports whether the entry is finished with; a true means nothing is in
// flight for it and the run moves on.
func (r *Reconciler) settleBeforeDispatch(ctx context.Context, entry planEntry) (bool, error) {
	tick := entry.TickID

	// The tracker is the authority on whether a tick is closed. Reading it
	// before doing anything is the compare-and-swap for the close: a tick that
	// is already closed is an effect that already happened, and a restarted
	// run must not do it again.
	current, err := r.tracker.Show(ctx, tick)
	if err != nil {
		return false, fmt.Errorf("read tick %s: %w", tick, err)
	}
	if current.Status == "closed" {
		r.setTick(tick, "closed")
		r.record(tick, StageSkipped, "already closed in the tracker: %s", current.ClosedReason)
		if _, err := r.checkpoint(runstate.StateRunning, "tick "+tick+" was already closed"); err != nil {
			return true, err
		}
		return true, nil
	}

	// Appendix A #11's read site. A struck-out unit is held until a PERSON
	// releases it, and nothing about this dispatch asks the clock.
	unit := r.opts.EpicID + "/" + tick
	if r.MayDispatch(unit) == Held {
		r.record(tick, StageHeld, "%s is struck out and only a person releases it", unit)
		return true, r.refuse(RefusedHeld, tick, "%s is struck out: a rolling window bounds the window, not the subject, "+
			"so this dispatch waits for a person and not for the clock", unit)
	}

	// The close-out's definition-of-done precondition (tick 3h0), read fresh
	// from the tracker rather than from the plan: the close-out does not
	// START while any child of the epic other than itself is open, whether or
	// not an edge names it. This is the boundary the production incident
	// crossed — a close-out dispatched over four open blockers, which opened
	// the epic PR over an epic whose definition of done was not met — and it
	// is deliberately WIDER than the edges: blocked_by is the plan's own
	// sequencing vocabulary, while this gate is about what the epic IS. It
	// runs before the PR + CI admission because it is the cheaper question and
	// the one whose answer nothing downstream can repair: no PR needs opening
	// for an epic whose own children are still open.
	//
	// The refusal it raises is first offered to the run's own sequencing
	// rather than returned flat, as a blockedTickErr carrying the gate's own
	// refusal (tick 3h0): the incident's ticks landed while the REVIEW ran,
	// and a role job settles inline, so no re-derivation runs between the
	// review's close and this settle — a flat refusal would make every
	// mid-review absorption a failed run and a person's re-run, exactly what
	// the tick exists to remove. requeueBlocked decides: children the fresh
	// graph offers as work this run has not done are worked first, and this
	// settle runs again over a graph that has closed them; only when nothing
	// this run is doing can close a child does the gate's refusal stand.
	if entry.Role == "closeout-epic" {
		open, refusal, err := r.gateCloseoutOnOpenChildren(ctx, entry)
		if err != nil {
			return true, err
		}
		if refusal != nil {
			return false, &blockedTickErr{tick: tick, blockers: open, refusal: refusal}
		}
		// The self-measurement (tick jlv): every predicted absorption whose
		// item became runnable is scored against the run HERE — after the
		// open-children gate has every child closed (the epic's own work is
		// what made the item runnable), and before the close-out job is
		// claimed — so the retro the dispatched close-out writes, and the epic
		// PR's body, report scores that already exist rather than discovering
		// them. The pass is idempotent across resumes: a prediction is scored
		// once, keyed by the finding, and the record is on the run branch for a
		// later measurement across epics.
		if err := r.scorePredictions(ctx, tick); err != nil {
			return true, err
		}
	}

	// A blocked_by edge added mid-run is honoured HERE, at the last moment
	// before the claim (tick 3h0): the tracker's own record for this tick is
	// re-read for its blockers, and each blocker's status is the tracker's
	// answer. The plan's re-derivation refreshes edges on every close, but a
	// close is not guaranteed between an edge landing and this dispatch — a
	// tick can reach the head of an empty window with nothing else to settle
	// — and an edge the plan never saw is precisely the one this read exists
	// for. What happens next is requeueBlocked's: a blocker this run can
	// still close is dispatched first and this tick waits behind it; one it
	// cannot is refused named. An OPEN blocker was, before this, discovered
	// by the worker after an hour of thinking — the answer BLOCKED is the
	// worker's, but the question was the run's to ask for nothing.
	if open, err := r.openBlockersAtDispatch(ctx, current); err != nil {
		return false, err
	} else if len(open) > 0 {
		return false, &blockedTickErr{tick: tick, blockers: open}
	}

	if isRoleJob(entry.Role) {
		// Review and closeout are jobs like any other, on the same executor —
		// what differs is that the reconciler acts on the ANSWER they return
		// rather than on a branch it merges. Close-out additionally has an
		// ADMISSION PRECONDITION the run itself enforces (tick 0iz): a target
		// repo that declares the PR + CI rule in .tick/config.md has its epic
		// PR opened by the run and its close-out held until CI is green — the
		// rule is checked BEFORE the phase is claimed or dispatched, never
		// left to the close-out worker's diligence.
		if entry.Role == "closeout-epic" {
			if err := r.admitCloseout(ctx, entry); err != nil {
				return true, err
			}
		}
		return true, r.processRoleJob(ctx, entry)
	}

	return false, nil
}

// openBlockersAtDispatch is the tracker's own answer about which of this
// tick's blocked_by edges still name an OPEN tick, read at the moment the run
// is about to claim it (tick 3h0). It is the fresh half of the boundary the
// window keeps: mayAdmit reads the PLAN — which a re-derivation refreshes on
// every close — while this reads the TRACKER, so an edge that landed between
// two re-derivations, or a blocker a person reopened behind the plan's back,
// is still seen before anything is claimed or paid for.
//
// Only the tick's own blockers are read, one Show per blocker, because the
// common case carries none and pays nothing — a tick the plan sequenced
// correctly reaches this read with its edges already closed.
func (r *Reconciler) openBlockersAtDispatch(ctx context.Context, current tk.Tick) ([]string, error) {
	if len(current.BlockedBy) == 0 {
		return nil, nil
	}
	var open []string
	for _, id := range current.BlockedBy {
		blocker, err := r.tracker.Show(ctx, id)
		if err != nil {
			// A blocker the tracker cannot answer for is a fact about the world,
			// not a guess to make: the plan's own rule — never guess a blocker
			// closed — is kept here, one edge further out.
			return nil, fmt.Errorf("read the blocker %s of tick %s: %w", id, current.ID, err)
		}
		if blocker.Status != "closed" {
			open = append(open, id)
		}
	}
	return open, nil
}

// beginTick claims the tick and starts its attempt, and stops there.
//
// It is the half of a tick's processing that must happen before anybody can
// wait for it, and the half a dispatch window runs up to `width` times before
// waiting for any of them.
func (r *Reconciler) beginTick(ctx context.Context, entry planEntry) (*inflightAttempt, error) {
	handle, executor, marker, err := r.claimDispatch(ctx, entry)
	if err != nil {
		return nil, err
	}
	fl := r.newInflight(entry, handle, executor, marker)
	// A nil handle with no error is claimDispatch's answer for an attempt that
	// is already integrated: nothing to address, only a finish to run.
	fl.integrated = handle == nil
	return fl, nil
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
	// The attempts a PERSON released (settle.go). Read here rather than once
	// at the start of the run: a settlement made while the run is stopped at
	// an earlier tick is one this dispatch must already see.
	released, err := r.settlements()
	if err != nil {
		return nil, nil, attemptHandle{}, err
	}
	// failed is how many prior attempts of THIS tick earned an escalation
	// rung — the ladder's definition of "failed", decided and logged in
	// tierpolicy.go: an attempt the run REJECTED that left nothing mergeable
	// (the redispatch disposition below). Not a refusal the run made around
	// the worker, not a gate failure (that attempt is ADOPTED and re-gated at
	// the same tier), and not an attempt a PERSON released — the human was
	// the actor, and no rung is earned from somebody else's decision.
	failed := 0
	// carry is the released attempt whose WORK the next dispatch of this tick
	// starts from: a person released it with --carry-work, so the next worker
	// begins at its commits rather than redoing them (settle.go, tick 0z0).
	// At most one can be current — the latest released attempt that carried
	// work — because a carried attempt either merges behind the gate or is
	// itself rejected and settled by its own release.
	var carry *carriedWork
	// The attempts of THIS tick, NEWEST FIRST (tick w1c). Appendix A #6 says
	// an attempt under this identity has already been dispatched, so it is
	// ADOPTED — it does not say the OLDEST is the one, and a disposition is a
	// function of durable state that changes between incarnations: an
	// attempt the run skipped as spent on one resume can resurface as
	// adoptable on the next (the blocker it answered about triaged, the tick
	// state a later attempt's dispatch overwrote). Walking the stored order
	// acted on the FIRST such resurfacing attempt and returned before ever
	// reaching the newer one — whose answer was the one the run had actually
	// paid for last. When several attempts of one tick survive, the adoptable
	// one is the LATEST: the earlier ones are spent by definition, since a
	// later attempt only exists because the run rejected them.
	mine := make([]runstate.Attempt, 0, len(attempts))
	for _, existing := range attempts {
		if existing.TickID == tick {
			mine = append(mine, existing)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Attempt > mine[j].Attempt })
	// adoptable is the attempt the pass will adopt once it has examined every
	// disposition. Adoption is deferred to the end of the pass on purpose: a
	// HELD attempt anywhere in the tick still stops the run, and a pass that
	// returned at the first adoptable attempt would skip a held one in favour
	// of a newer adoptable — the mirror of the bug — so redispatch and hold
	// are evaluated in the same pass, and only a pass with no hold adopts.
	var adoptable *runstate.Attempt
	var adoptableMarker attemptHandle
	// adoptableIntegrated says the attempt the pass will take is one whose
	// rejection something else has since answered by MERGING its work: it is
	// finished from the integration branch, never addressed through an
	// executor again.
	var adoptableIntegrated bool
	for i := range mine {
		existing := &mine[i]
		marker := handleFromMap(existing.JobHandle)
		marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)
		if was, ok := released[attemptKey(existing.TickID, existing.Attempt)]; ok {
			if was.carry {
				// The person released the attempt AND said its work goes
				// forward: the next attempt is cut from the released branch,
				// not from the integration branch. The released attempt is
				// still not adopted — nobody could address it, which is why
				// they were asked — and it still earns the ladder no rung:
				// the human was the actor. Carrying changes only where the
				// next worker STARTS; the gate still decides what merges.
				if carry == nil || existing.Attempt > carry.marker.Attempt {
					carry = &carriedWork{marker: marker, by: was.by, at: was.at}
				}
				r.record(tick, StageSettled,
					"%s was released by %s at %s carrying its work; the next try starts from %s",
					attemptLabel(tick, tryOf(attempts, tick, existing.Attempt), existing.Attempt), was.by, was.at,
					branchOf(marker.WriteRef))
				continue
			}
			// A person settled it. It is not adopted — nobody could address it,
			// which is why they were asked — and whatever it left on its own
			// write ref stays there: a new attempt gets a ref of its own.
			r.record(tick, StageSettled,
				"%s was released by %s at %s; a new try is dispatched rather than the released one adopted",
				attemptLabel(tick, tryOf(attempts, tick, existing.Attempt), existing.Attempt), was.by, was.at)
			continue
		}
		if !r.guarded(guardNeverRedispatchLive) {
			// The guard is off: fall through and dispatch over whatever the
			// previous incarnation started, which is the bug the guard exists
			// for — one tick, two jobs, and the run pays for both.
			break
		}
		disposition, where := r.disposition(*existing, marker)
		switch disposition {
		case redispatchAttempt:
			// SETTLED, and it produced nothing. Adopting it would re-collect
			// the same refusal for as long as the run is restarted, so this is
			// a new ATTEMPT — a new number, a new marker, and a base that is
			// the integration branch as origin has it now, which is what makes
			// a worker that answered BLOCKED about an open blocker worth
			// dispatching again once that blocker is closed.
			r.record(tick, StageRedispatched,
				"%s settled with nothing on %s and was rejected; a new try is dispatched "+
					"rather than the spent one adopted",
				attemptLabel(tick, tryOf(attempts, tick, existing.Attempt), existing.Attempt), branchOf(marker.WriteRef))
			// The rung this attempt earned for the ladder: the work was
			// dispatched, it had its chance, and it did not pass.
			failed++
			continue
		case holdAttemptWork:
			// REJECTED, and the commits are still there. Dispatching over it
			// would orphan the only copy of what a person has to look at, and
			// collecting it again would report a missing report this run
			// deleted itself when it tore the refused attempt down. So the run
			// stops here and says where the work is and who moves it on.
			//
			// The attempt number is set BEFORE these records, so the lines this
			// branch leaves — and the run_held line the failure becomes — say
			// WHICH attempt the run is holding, which is the whole question a
			// person reading the feed is asking.
			r.setAttempt(tick, existing.Attempt)
			r.record(tick, StageRejected,
				"%s was rejected and its commits are still there (%s); it is neither adopted nor "+
					"redispatched", attemptLabel(tick, tryOf(attempts, tick, existing.Attempt), existing.Attempt), where)
			// The teardown, in case the incarnation that rejected this attempt
			// never reached its own: the rejection is recorded on origin before
			// anything is torn down, so a run killed in between leaves a live
			// credential and a registered worktree that nothing else would ever
			// come back for. It is idempotent, and it keeps the branch.
			r.tearDownSettled(marker, fmt.Sprintf(
				"%s was rejected and holds commits nothing merged",
				attemptLabel(tick, tryOf(attempts, tick, existing.Attempt), existing.Attempt)), true)
			return nil, nil, marker, r.refuse(RefusedRejectedWork, tick,
				"%s was rejected and the work it committed is still there — %s — and nothing merged "+
					"it. This run neither collects it again (the teardown that followed the refusal removed the "+
					"attempt's worktree, so a second collect would report a missing report rather than the verdict "+
					"the attempt really had) nor dispatches over it (that would orphan the only copy). Read the "+
					"branch; then take the work, or release the attempt with "+
					"`ticfac settle %s %s %d --release \"<who>\"` and run the epic again for a fresh attempt",
				attemptLabel(tick, tryOf(attempts, tick, existing.Attempt), existing.Attempt), where, r.opts.EpicID,
				tick, existing.Attempt)
		}
		// Appendix A #6: the first ADOPTABLE attempt of a newest-first pass is
		// the highest-numbered one, which is the one the pass remembers — the
		// LATEST of the tick. The pass does not stop here: an older attempt
		// below may still be held, and a hold stops the run wherever it sits.
		if adoptable == nil {
			adoptable = existing
			adoptableMarker = marker
			// A role job is acted on in one piece by processRoleJob, which
			// already finishes an integrated attempt from the branch; only a
			// tick the window finishes takes the executor-free path.
			adoptableIntegrated = disposition == integratedAttempt && !isRoleJob(entry.Role)
		}
	}

	// The attempt was rejected and its work is ALREADY on the integration
	// branch — a person, or a resolve job, merged what this run refused to
	// (epic-yoh: cr4's merge_failed, merged by hand). integrate.go already
	// treats a contained head as integrated "whoever integrated it"; the
	// resume must reach it WITHOUT adopting through the executor: the refusal
	// tore the attempt down, and adopt() on a host that holds no state for it
	// reads "the marker landed and the dispatch did not" and starts the worker
	// again on work that is already merged — which the executor refuses,
	// holding the tick forever. So nothing is addressed: the attempt joins the
	// window settled, and the finish proves the containment, gates the epic
	// head and closes.
	if adoptable != nil && adoptableIntegrated {
		r.setAttempt(tick, adoptable.Attempt)
		// The tracker should say the tick is being worked while it is
		// finished; a claim that cannot be replayed is recorded, never fatal —
		// the work is merged either way.
		_ = r.replayClaim(ctx, tick)
		r.record(tick, StageAdopted,
			"%s was rejected and its work is already on %s — something other than this run merged it; it is "+
				"finished from there (gated on the epic head and closed) rather than addressed again",
			attemptLabel(tick, tryOf(attempts, tick, adoptable.Attempt), adoptable.Attempt), r.branch)
		return nil, nil, adoptableMarker, nil
	}

	// Appendix A #6: an attempt under this identity has already been
	// dispatched, so it is ADOPTED — never redispatched, and of several
	// survivors the LATEST one (tick w1c). The attempt number is the marker's
	// own BEFORE the adoption runs (tick d6s): the lines adoption itself
	// leaves — the replayed claim, the start failure of an attempt whose
	// marker landed but never started — are about that attempt.
	if adoptable != nil {
		r.setAttempt(tick, adoptable.Attempt)
		handle, executor, err := r.adopt(ctx, adoptableMarker)
		if err != nil {
			return nil, nil, adoptableMarker, err
		}
		r.record(tick, StageAdopted, "%s was already dispatched; it is adopted by identity, never redispatched",
			attemptLabel(tick, tryOf(attempts, tick, adoptable.Attempt), adoptable.Attempt))
		return handle, executor, adoptableMarker, nil
	}

	// The classification exchange (tick w9b, epic wne), at the FIRST DISPATCH
	// of a role-less tick and nowhere else: Jev is asked once per tick, the
	// answer — the full probability distribution and the model identity — is
	// written to the run branch as a decision record before anything is
	// dispatched on top of it, and every later pass (including a cold
	// re-derivation from git) reads the record instead of re-asking. It sits
	// after the adoption walk on purpose: an attempt this run already
	// dispatched was planned under whatever its incarnation knew, and an
	// adopted attempt is never re-planned.
	//
	// The ask itself is bounded to the FIRST dispatch (tick sj2): a later
	// attempt that finds no record does not ask — the first attempt was
	// planned under nothing, and asking after the fact would make the
	// re-derivation of this run reach a different dispatch than the run it
	// reconstructs — it routes at the start policy exactly as the first
	// attempt did, and nothing is recorded.
	//
	// The record is an INPUT to the dispatch's own derivation (tick s45): the
	// mass rule routes the start tier on the probability mass over the
	// policy's dear work types, an absent or no-answer record falls back to
	// the start policy, and the ladder and the ceiling still bound whatever
	// the classifier chose. A nil classification here is that fallback, not
	// an error: the exchange's degradations are by design.
	var classification *RecordedClassification
	if entry.Role == "implement-tick" {
		if classification, err = r.classificationFor(ctx, entry, len(mine) == 0); err != nil {
			return nil, nil, attemptHandle{}, err
		}
	}

	number := nextAttemptNumber(attempts)
	for conflicts := 0; conflicts < maxDispatchConflicts; conflicts++ {
		// The tick's own try for this dispatch (tick vw0): the same number the
		// feed lines say, computed once here so the dispatch, the marker and
		// the record all name one number. The run-wide `number` above is the
		// attempt's identity; this one is its ordinal among the tick's own
		// tries, and the two only coincide when nothing was dispatched first.
		try := tryOf(attempts, tick, number)
		dispatch, marker, err := r.planDispatch(entry, number, try, failed, carry, classification)
		if err != nil {
			return nil, nil, attemptHandle{}, err
		}

		// The executor is built BEFORE the marker, because the marker's
		// provenance states the substrate its executor observed at the build
		// that will run the job — and because building one starts nothing: a
		// build failure here is a refusal before the tick is claimed and
		// before any record says a dispatch happened, which is the honest
		// shape for "this host cannot run this profile at all".
		executor, substrate, err := r.opts.NewExecutor(dispatch)
		if err != nil {
			return nil, nil, marker, fmt.Errorf("build the executor for %s: %w", tick, err)
		}
		dispatch.Substrate = substrate
		marker.SubstrateProtocol, marker.SubstrateServerVersion = substrate.Protocol, substrate.ServerVersion

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
			// The loser of a dispatch race is refused by the repository, not by
			// a lock it might have lost — and it must NOT start a job.
			handle, executor, adopted, ok, err := r.adoptConflicted(ctx, tick, number, string(outcome))
			if err != nil {
				return nil, nil, adopted, err
			}
			if ok {
				return handle, executor, adopted, nil
			}
			// The number is somebody else's tick. Recompute it against origin
			// as it stands NOW rather than adopting another tick's job.
			refreshed, err := r.store.Attempts()
			if err != nil {
				return nil, nil, marker, err
			}
			if next := nextAttemptNumber(refreshed); next > number {
				number = next
			} else {
				number++
			}
			continue
		}

		// Appendix A #7: the marker is read back from ORIGIN before anything
		// acts on it. A write that silently did not land must not look like a
		// dispatch somebody can find — and this one is what stops the next
		// incarnation from dispatching again.
		if _, err := r.store.Fetch(); err != nil {
			return nil, nil, marker, err
		}
		if _, ok, err := r.store.Attempt(number); err != nil || !ok {
			return nil, nil, marker, fmt.Errorf(
				"the dispatch marker for %s did not land on %s: nothing is started behind a record that "+
					"does not exist (%v)", attemptLabel(tick, try, number), r.opts.Remote, err)
		}

		// The attempt is THIS dispatch's from the moment its marker is durable
		// on origin (tick d6s): every line from here — the claim, the start
		// failure, the dispatch itself — is about it, and the feed carries
		// run/tick/attempt identity on every line. Setting it only after a
		// successful Start made the claimed and start-failure lines carry the
		// PREVIOUS attempt's number, exactly on the lines a reader needs when
		// an attempt failed to start.
		r.setAttempt(tick, number)

		// The effect, now that the marker proves it has not happened.
		if _, err := r.tracker.Claim(ctx, tick, r.opts.Owner); err != nil {
			// A tracker that REFUSED is not a tracker that broke (tick 3mp).
			// The width guard is a verdict about the world — this epic already
			// has as many claims open as it declared it may — so the run holds
			// for a person with that reason in the feed, and every settled
			// attempt it is holding is announced as adoptable rather than
			// abandoned in silence. Epic dha died here instead, taking three
			// settled ticks' worth of unfinished work down with it.
			//
			// Only the TYPED refusal holds. A tracker that would not answer at
			// all — a missing binary, an unreadable repository — is an
			// operational error and is returned as one, the same rule
			// disposeRefused keeps: an outage is not a verdict on the work.
			var width *tk.ErrDispatchWidth
			if errors.As(err, &width) {
				return nil, nil, marker, r.refuse(RefusedClaimWidth, tick,
					"the tracker refused to claim %s: %v. The run is HELD, not failed: nothing it was holding is "+
						"lost — every dispatched attempt's marker is on %s and its commits are on its own branch, "+
						"and running the epic again under this run id adopts them by identity rather than "+
						"dispatching over them. A claim lives until its tick CLOSES, so the width frees itself as "+
						"the ticks already in flight finish; if it does not, look at what else holds a claim under "+
						"this epic", tick, err, r.opts.Remote)
			}
			return nil, nil, marker, fmt.Errorf("claim %s: %w", tick, err)
		}
		r.record(tick, StageClaimed, "claimed for %s", r.opts.Owner)

		// A dispatch cut from a RELEASED attempt's work says so here, once,
		// on the dispatch that is actually happening (tick 0z0) — not in the
		// planning half, which a dispatch conflict can run twice. The worker
		// starting from the released commits is a fact about THIS attempt, and
		// this line is what a person reading the run reads it from.
		if marker.ResumedFrom != nil {
			r.record(tick, StageCarried,
				"%s starts from the work %s left on %s (released by %s): the next worker "+
					"continues that work rather than redoing it, and the gate still decides what merges",
				attemptLabel(tick, try, number),
				attemptLabel(tick, tryOf(attempts, tick, marker.ResumedFrom.Attempt), marker.ResumedFrom.Attempt),
				branchOf(marker.ResumedFrom.WriteRef), marker.ResumedFrom.ReleasedBy)
		}

		handle, err := executor.Start(r.jobSpec(dispatch))
		if err != nil {
			return nil, nil, marker, r.startFailure(tick, err)
		}
		r.noteAlive(dispatch.JobID)
		r.setTick(tick, "dispatched")
		// The tick's own try leads and the run-wide number is labelled as the
		// dispatch counter it is (tick h58): "w9b try 3 dispatched (run
		// dispatch #5, branch …)" needs no footnote, where "attempt 5 started
		// as …" needed one and was misread anyway.
		r.record(tick, StageDispatched, "%s try %d dispatched (run dispatch #%d, branch %s)",
			tick, try, number, branchOf(marker.WriteRef))
		if _, err := r.checkpoint(runstate.StateRunning, fmt.Sprintf("%s is running as attempt %d", tick, number)); err != nil {
			return nil, nil, marker, err
		}
		return handle, executor, marker, nil
	}
	return nil, nil, attemptHandle{}, fmt.Errorf(
		"%d dispatch numbers running were taken by other ticks of this run while dispatching %s; that is an "+
			"operational problem, not a race to spin on", maxDispatchConflicts, tick)
}

// maxDispatchConflicts bounds the recompute-on-a-taken-number loop, for
// integrate's reason: a number moving forever under a writer is an operational
// problem to report, not a conflict to spin on.
const maxDispatchConflicts = 8

// nextAttemptNumber is the number a new dispatch takes. Attempt numbers are
// RUN-wide, not per tick: the run state store keys attempts by number alone.
// tryOf is how many times THIS tick has been dispatched, counting the attempt
// numbered `number` as the latest.
//
// Attempt numbers count dispatches across the whole RUN, not tries at one tick,
// because an attempt's number is its identity: it names its branch
// (…/tick-nvn/attempt-12), its durable marker, and the argument to
// `ticfac settle <epic> <tick> <n>`. That is right for identity and misleading
// in a sentence — "attempt 12 of nvn" reads as eleven failures at nvn when it
// is nvn's first try and the run's twelfth dispatch. So a line written for a
// person says both, the try FIRST ([AttemptLabel], tick h58), and the feed's
// `attempt` field keeps the identity.
func tryOf(attempts []runstate.Attempt, tick string, number int) int {
	try := 1
	for _, existing := range attempts {
		if existing.TickID == tick && existing.Attempt < number {
			try++
		}
	}
	return try
}

// AttemptLabel is how a line written for a PERSON names one attempt (tick
// h58): the tick's own try first, and the run-wide number after it, labelled
// as what it is — "w9b try 3 (run dispatch #5)". The run-wide number is the
// attempt's IDENTITY and stays so everywhere a program keys on it (branch
// names, WIP refs, adoption, the feed's `attempt` field, `ticfac settle`'s
// argument); what changes is only which number a sentence LEADS with. The
// line an operator misread said "attempt 5 … (w9b try 3; attempt numbers
// count this run's dispatches)" and they read it as w9b's fifth try: a
// message that has to footnote its own headline number has the numbers the
// wrong way round.
//
// A try below 1 is a try nobody could count, and the label then says only
// what it knows — the dispatch number, still labelled as one.
func AttemptLabel(tick string, try, number int) string {
	if try < 1 {
		return fmt.Sprintf("%s (run dispatch #%d)", tick, number)
	}
	return fmt.Sprintf("%s try %d (run dispatch #%d)", tick, try, number)
}

// attemptLabel is [AttemptLabel] for this package's own lines.
func attemptLabel(tick string, try, number int) string { return AttemptLabel(tick, try, number) }

// attemptName is [attemptLabel] for a line that holds only the attempt's
// identity: the tick's try is counted from the attempt records the run state
// holds, which is the same count the dispatch took it from.
func (r *Reconciler) attemptName(tick string, number int) string {
	return attemptLabel(tick, r.tryOfAttempt(tick, number), number)
}

// tryOfAttempt is [tryOf] against the run state this reconciler holds. A
// store that is not there yet, or cannot be read, answers 0 — it costs a line
// its try and never the line: the dispatch number still names the attempt
// exactly.
func (r *Reconciler) tryOfAttempt(tick string, number int) int {
	if r.store == nil {
		return 0
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return 0
	}
	return tryOf(attempts, tick, number)
}

func nextAttemptNumber(attempts []runstate.Attempt) int {
	number := len(attempts) + 1
	for _, existing := range attempts {
		if existing.Attempt >= number {
			number = existing.Attempt + 1
		}
	}
	return number
}

// adoptConflicted resolves the marker origin already held at the number this
// reconciler chose. It reports whether that marker was adopted.
//
// The check that makes it safe is the tick id. Attempt numbers are run-wide,
// so the record that refused this create is not necessarily ABOUT this tick —
// two reconcilers dispatching different ticks of one run race for the same
// number, and the loser reading the winner's marker would adopt another tick's
// job: its worktree, its branch, its report, collected and merged under this
// tick's name. A record for a different tick is therefore not adopted at all;
// the caller recomputes the number against origin as it stands now.
func (r *Reconciler) adoptConflicted(ctx context.Context, tick string, number int, outcome string) (
	*subprocess.JobHandle, Executor, attemptHandle, bool, error) {

	if _, err := r.store.Fetch(); err != nil {
		return nil, nil, attemptHandle{}, false, err
	}
	recorded, ok, err := r.store.Attempt(number)
	if err != nil || !ok {
		return nil, nil, attemptHandle{}, false, fmt.Errorf(
			"run dispatch #%d is on origin and unreadable while %s was being dispatched: %v", number, tick, err)
	}
	if recorded.TickID != tick {
		r.record(tick, StageRedispatched,
			"run dispatch #%d on origin is %s's, not %s's; the number is recomputed rather than another tick's job adopted",
			number, recorded.TickID, tick)
		return nil, nil, attemptHandle{}, false, nil
	}
	marker := handleFromMap(recorded.JobHandle)
	marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)
	handle, executor, err := r.adopt(ctx, marker)
	if err != nil {
		return nil, nil, marker, false, err
	}
	// The checkpoint says which attempt this tick is on however the reconciler
	// got there: a run that adopted rather than dispatched still has to say 1
	// and not 0, because the next incarnation reads that number.
	r.setAttempt(tick, recorded.Attempt)
	r.record(tick, StageAdopted,
		"the dispatch marker was already on origin (%s); this reconciler adopted rather than dispatching", outcome)
	return handle, executor, marker, true, nil
}

// attemptDisposition is what this run does with an attempt of the same tick
// that origin already carries a marker for.
type attemptDisposition int

const (
	// adoptAttempt: the attempt is re-addressed and never redispatched
	// (Appendix A #6). It is the default, and every unknown answer lands here.
	adoptAttempt attemptDisposition = iota

	// redispatchAttempt: it was REJECTED and left no commit anywhere, so
	// adopting it would re-collect the same refusal for as long as the run is
	// restarted. A new attempt takes its place, at a new number and a ref of
	// its own.
	redispatchAttempt

	// holdAttemptWork: it was REJECTED and its commits exist, unmerged. The
	// work is the only copy of what a person has to look at, and this run has
	// nothing left to do to it.
	holdAttemptWork

	// integratedAttempt: it was REJECTED and its head is already CONTAINED in
	// the integration branch on origin — merged by the run itself before a
	// gate refused it, or by a person or a resolve job after a merge_failed.
	// Either way the work is integrated: it is finished from the branch (gate
	// the epic head, close), and nothing is asked of an executor.
	integratedAttempt
)

// disposition decides which of the three an attempt on origin is.
//
// The first question is durable: the checkpoint a restart reads from origin
// says whether this tick's attempt was rejected. The second is about the work,
// and it is asked of BOTH refs the work can be on, which is the repair here.
// Asking origin alone declared an attempt spent whose every push had failed —
// the integrate refusal in durableAttemptHead is exactly that shape — and the
// next run then dispatched over it and orphaned the local branch holding the
// only copy. The local branch is one `git rev-parse` away in the checkout this
// run is working in, so it is read before anything is called spent.
//
// The third question is what separates a gate failure from every other
// refusal: an attempt whose head the integration branch already CARRIES was
// merged, and the thing that refused it was the gate over that merge or the
// freshness check after it — or a person, or a resolve job, merged it after
// this run refused the merge (epic-yoh's cr4). That one is integrated, so that
// a person who fixed the check, the tree or the conflict gets the gate run
// again (gate.go's evidence key) rather than a refusal about work that is
// already integrated. It is finished from the branch and never adopted through
// the executor, whose attempt the refusal already tore down.
func (r *Reconciler) disposition(record runstate.Attempt, marker attemptHandle) (attemptDisposition, string) {
	if r.tickState(record.TickID) != "rejected" {
		return adoptAttempt, ""
	}
	branch := branchOf(marker.WriteRef)

	remote, err := r.remoteWork(branch, marker.BaseSHA)
	if err != nil {
		// Nobody can say what the attempt left. That is not evidence it left
		// nothing, and settling it either way from a failed read would be the
		// guess Appendix A #6 forbids.
		return adoptAttempt, ""
	}
	if remote != "" {
		if r.integrated(remote) {
			return integratedAttempt, ""
		}
		return holdAttemptWork, fmt.Sprintf("%s carries %s on %s, and %s does not have it",
			branch, short(remote), r.opts.Remote, r.branch)
	}
	if local := r.attemptWorkHead(marker); local != "" {
		return holdAttemptWork, fmt.Sprintf(
			"%s carries %s in this checkout, and %s has no commit of this attempt at all",
			branch, short(local), r.opts.Remote)
	}
	return redispatchAttempt, ""
}

// remoteWork is the attempt's head on ORIGIN when that head carries a commit
// beyond the base the attempt was cut from, and "" when it carries none.
//
// "There is no ref" and "the ref carries nothing" are deliberately one answer:
// the executor pushes the branch it was given whether or not the worker
// committed anything to it, which is the same thing the collect vocabulary's
// `no-commits` says.
func (r *Reconciler) remoteWork(branch, base string) (string, error) {
	head, err := r.git.remoteHead(branch)
	if err != nil {
		return "", err
	}
	if head == "" || head == base {
		return "", nil
	}
	if err := r.git.fetch(branch); err != nil {
		return "", err
	}
	if r.git.contains(head, base) {
		// Contained in the base it was cut from: the branch moved nowhere.
		return "", nil
	}
	return head, nil
}

// integrated reports whether the integration branch on origin already carries
// a commit. It is the same containment question integrate asks for its own
// idempotence, and it is asked of ORIGIN rather than of this checkout's memory
// of it.
func (r *Reconciler) integrated(commit string) bool {
	integrated, err := r.integratedOn(commit)
	return err == nil && integrated
}

// integratedOn is integrated with the read error kept. A caller that must not
// turn an unreachable remote into a verdict uses this one.
func (r *Reconciler) integratedOn(commit string) (bool, error) {
	epicHead, err := r.git.remoteHead(r.branch)
	if err != nil {
		return false, fmt.Errorf("read %s on %s: %w", r.branch, r.opts.Remote, err)
	}
	if epicHead == "" {
		return false, nil
	}
	if err := r.git.fetch(r.branch); err != nil {
		return false, fmt.Errorf("fetch %s from %s: %w", r.branch, r.opts.Remote, err)
	}
	return r.git.contains(commit, epicHead), nil
}

// integratedHead is the attempt's head when this run has already merged it,
// and "" otherwise. It is what tells a resumed run not to collect an attempt a
// second time: the refusal that stopped the previous incarnation tore the
// attempt's worktree down, and a collect that reads a worktree the reconciler
// itself removed reports a missing report rather than the verdict the attempt
// really had.
//
// Nothing is merged on the strength of it — integrate only ever reports
// "already contained" for such an attempt — so what it decides is whether a
// second collect happens, not what reaches the integration branch.
//
// A remote that cannot be READ is an error rather than an answer. Reporting
// "not merged" for an unreachable origin is the same guess disposition refuses
// to make twenty lines above, and it is the more expensive direction of the
// two: the run would collect an attempt it already merged, and report the
// worktree it removed itself as a missing report.
func (r *Reconciler) integratedHead(marker attemptHandle) (string, error) {
	head, err := r.remoteWork(branchOf(marker.WriteRef), marker.BaseSHA)
	if err != nil {
		return "", fmt.Errorf("read %s on %s to see whether %s is already integrated: %w",
			branchOf(marker.WriteRef), r.opts.Remote, r.attemptName(marker.TickID, marker.Attempt), err)
	}
	if head == "" {
		return "", nil
	}
	integrated, err := r.integratedOn(head)
	if err != nil {
		return "", err
	}
	if !integrated {
		return "", nil
	}
	return head, nil
}

// tickState is where the run believes one tick stands, as the checkpoint has
// it: seeded from origin at Run, updated by setTick.
func (r *Reconciler) tickState(tickID string) string {
	for _, ts := range r.ticks {
		if ts.TickID == tickID {
			return ts.State
		}
	}
	return ""
}

// preserveAttemptWork puts the attempt's local branch on origin, when that
// branch carries a commit beyond the base it was cut from and origin does
// not already have it at that head.
//
// It is the durability half of every verdict the collect records: the push
// happens before the rejection is recorded, so "rejected" on origin always
// means the work the rejection was about is ALSO on origin — and a
// ready-to-merge attempt has simply had integrate's own push done for it
// early (durableAttemptHead then finds origin already at the collected head
// and merges it). The push is a fast-forward onto the attempt's OWN ref, a
// namespace no other attempt writes; a failure is not the verdict's to
// inherit — the verdict still happens, and the honesty about where the work
// then lives is said out loud, because a worktree the teardown removes is
// not a place a person can be sent to look.
func (r *Reconciler) preserveAttemptWork(marker attemptHandle) {
	head := r.attemptWorkHead(marker)
	if head == "" {
		return
	}
	branch := branchOf(marker.WriteRef)
	if remote, err := r.git.remoteHead(branch); err == nil && remote == head {
		return
	}
	if _, stderr, err := r.git.try("", "push", r.opts.Remote, head+":"+refFor(branch)); err != nil {
		r.record(marker.TickID, StageCollected,
			"%s carries %s which could not be put on %s (%s): whatever this attempt left is only on the "+
				"local branch in this checkout, which the teardown keeps — but origin does not have it",
			branch, short(head), r.opts.Remote, firstLine(stderr))
		return
	}
}

// rejectDurably records that an attempt was rejected, ON ORIGIN, before the
// refusal is returned.
//
// A rejection that lives only in this process's memory is a rejection the next
// incarnation cannot see: it reads the checkpoint, finds the attempt marker,
// adopts the dead job and re-collects the same refusal forever. The checkpoint
// is what makes "this attempt is spent" a fact somebody else can read.
func (r *Reconciler) rejectDurably(marker attemptHandle, verdict, message string) error {
	r.setTick(marker.TickID, "rejected")
	_, err := r.checkpoint(runstate.StateRunning,
		fmt.Sprintf("%s is rejected (%s): %s", r.attemptName(marker.TickID, marker.Attempt), verdict, firstLine(message)))
	return err
}

// startFailure keeps the executor's typed refusals typed. "Nobody can say
// whether it is running" is not "nothing is running", and it never becomes a
// redispatch here either.
//
// The failure is RECORDED, at tick scope, before it is returned (tick d6s):
// a start that fails is the moment a feed reader most needs a line — the
// run's answer to "what happened to attempt 2?" must not be silence, and the
// line carries the attempt it is about because the attempt is set before
// the claim. Without it a subscriber saw a claim and then nothing, with no
// way to tell a failed start from a run that is still going.
func (r *Reconciler) startFailure(tick string, err error) error {
	r.record(tick, StageStartFailed, "the start failed: %s", firstLine(err.Error()))
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
// asked for anything — the DERIVED TIER first (tick 5eq; since tick s45
// derived from the recorded classification too, by probability mass over
// the policy's dear work types), then the budget, which is clamped here so
// the job is issued the number that will govern. It runs BEFORE the marker
// is written and the tick is claimed, so a refusal here (a tier label the
// config cannot honour, say) spends nothing and claims nothing.
func (r *Reconciler) planDispatch(entry planEntry, number, try, failed int, carry *carriedWork, classification *RecordedClassification) (Dispatch, attemptHandle, error) {
	// Where the orchestrator stops choosing and starts deriving: the tier is
	// a pure function of the tick's facts, this attempt's durable state and
	// the declared policy — never a per-dispatch judgement, never a hunch.
	tier, reason, err := r.deriveTier(entry, number, failed, classification)
	if err != nil {
		// The loud refusal: the tick AND the label, never a silent fall-back
		// to the default — a label is a weakly typed field the tracker cannot
		// validate, so this is the one place a typo can ever surface.
		return Dispatch{}, attemptHandle{}, r.refuse(RefusedTierLabel, entry.TickID, "%s: the tick is neither dispatched nor claimed, and the label is the thing to fix", err.Error())
	}
	dispatchProfile, err := r.profileForTier(entry.Role, tier)
	if err != nil {
		return Dispatch{}, attemptHandle{}, r.refuse(RefusedTierLabel, entry.TickID,
			"tick %s was routed to tier %q and no profile resolves against the target repository's runner configuration: %v. "+
				"The tick is neither dispatched nor claimed; declare the tier in [roles.%s.tiers.%s] or remove the label that asked for it",
			entry.TickID, tier, err, entry.Role, tier)
	}

	// WHICH WINS when the policy asks for a tier the budget clamp could
	// refuse: neither does. The tier routes MODELS, the budget clamps DOLLARS,
	// and the dispatch is issued the effective budget at the derived tier.
	// The statement is made here, in the run's own record, beside the tier
	// that explains the spend — not as a silent downgrade of either number.
	budgetNote := ""
	if r.budget.Effective > 0 {
		budgetNote = fmt.Sprintf(", with the effective budget $%.2f (the clamp governs spend, the tier governs routing, and neither silently modifies the other)", r.budget.Effective)
	}
	r.record(entry.TickID, StageTierDerived, "%s runs at tier %q (%s)%s",
		attemptLabel(entry.TickID, try, number), tier, reason, budgetNote)

	jobID := fmt.Sprintf("run-%s/tick-%s/attempt-%d", r.runID, entry.TickID, number)
	stateDir := r.execStateDir(entry.TickID, number)

	// EVERY dispatch is made at the integration branch as origin has it NOW,
	// not at the base the run was cut from. A role job because that is what it
	// is about; an implementation tick because a later wave's worker has to see
	// the waves before it — their merged code, and the tracker records this
	// reconciler pushed as it closed them. A worker that branched from the
	// run's base reads `.tick/issues/<blocker>.json` as it was before the run
	// and answers BLOCKED about a tick that is closed.
	//
	// The one exception is a dispatch CARRIED from a released attempt (tick
	// 0z0): a person released the attempt saying its work goes forward, so
	// the next worker starts from THAT — the released branch's head — and
	// not from the integration branch. The released work contains the
	// integration branch it was cut from, so nothing the ordinary base would
	// give is missing; what carrying adds is the commits the interrupted
	// worker had already made. The gate still decides what merges, exactly as
	// for any attempt: carrying changes where the work STARTS, never what is
	// believed without evidence.
	base := r.controllerBase()
	var resumed *resumedFrom
	if carry != nil {
		head, err := r.carryHead(carry.marker)
		if err != nil {
			return Dispatch{}, attemptHandle{}, err
		}
		base = head
		resumed = &resumedFrom{
			TickID: carry.marker.TickID, Attempt: carry.marker.Attempt,
			WriteRef: carry.marker.WriteRef, SHA: head, ReleasedBy: carry.by,
		}
	}
	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: entry.TickID, Attempt: number,
		Try: try, JobID: jobID, Role: entry.Role, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: attemptWriteRef(jobID), BaseSHA: base, StateDir: stateDir,
		BaseRef: r.opts.BaseRef, Title: entry.Title,
		Profile: dispatchProfile, Tier: tier, Executor: dispatchProfile.Executor,
		ResumedFrom: resumed,
		// What the tick's earlier attempts found (tick nvn): the reports a
		// re-dispatched attempt is shown in its prompt, newest first. Gathered
		// here rather than recorded on the marker because they are re-derivable
		// facts about the state directory — and a dispatch conflict that runs
		// this half twice re-derives the same list.
		PriorReports: r.priorReports(entry.TickID, number),
		// What the tick's earlier attempts left PRESERVED (tick pbb): the
		// uncommitted work of an attempt stopped at its wall clock or rejected
		// before it committed, kept on a wip ref the re-dispatch points the
		// worker at — gathered here for the same reason the reports are.
		PriorSnapshots: r.priorSnapshots(entry.TickID, number),
	}
	if r.budget.Effective > 0 {
		effective := r.budget.Effective
		dispatch.BudgetUSD = &effective
	}
	marker := attemptHandle{
		Executor: dispatchProfile.Executor, JobID: jobID, Attempt: number, TickID: entry.TickID,
		Try:  try,
		Role: entry.Role, Repo: r.opts.Repo, Remote: r.opts.Remote,
		WriteRef: dispatch.WriteRef, BaseSHA: base, StateRoot: stateDir,
		Model: dispatchProfile.Model, PromptDigest: promptDigest(dispatchProfile),
		Tier: tier, ResumedFrom: resumed,
	}
	// The tick's file declaration, copied at PLANNING time (tick 01u): the
	// malformed labels were already refused at admission, so what is left
	// here is only the parsed list the merge holds the worker to.
	marker.Touch, _ = parseTouchLabels(entry.TickID, entry.Labels)
	return dispatch, marker, nil
}

// attemptWriteRef is the ref ONE attempt of one tick may write, in SPEC
// §4.3's golden shape: refs/heads/ticfac/run-<run>/tick-<tick>/attempt-<n>.
// The job id already carries that identity, so the ref is the job id under the
// namespace the source grant bounds. It is runprogress.ParseAttempt's
// vocabulary in reverse: the guard test pins the round trip, so a write ref
// this package mints is always one the progress measurement can read back.
//
// Every attempt of every run gets a ref of its own, and that is the whole
// point. One ref per TICK made the git identity coarser than the dispatch
// identity the rest of this package is built on (repo key, run, tick,
// attempt): a second attempt found the branch already there and was refused
// live by the executor, its pushes collided non-fast-forward, and a merge
// could not tell one attempt's commits from another's.
func attemptWriteRef(jobID string) string {
	return "refs/heads/ticfac/" + jobID
}

// attemptRefPrefix is the namespace ONE RUN's write grade may advance —
// job-protocol.json's `write_ref_prefix`, bounded per run rather than per
// installation so a credential issued for this run cannot advance another
// run's attempt refs. The spelling is runprogress's own, so the run's
// measurement of its attempts — the same refs, read by the stall warning and
// by `ticfac status` — and the refs it mints are one vocabulary by
// construction, not by coincidence (pinned by a test).
func attemptRefPrefix(runID string) string {
	return runprogress.RefPrefix(runID)
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
		Capabilities: subprocess.Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:       []subprocess.Input{{Kind: "tick", ID: d.TickID}, {Kind: "epic", ID: d.EpicID}},
		OutputSchema: outputSchemaFor(d.Role),
		// The one key here that is NOT scoped to an attempt: run and tick, no
		// attempt number. It is safe, but not locally — so, since the run now
		// dispatches a window of attempts at once, the reason is written down.
		//
		// The prefix feeds excludeFromGit, which appends a line to
		// info/exclude, and info/exclude is shared by every linked worktree of
		// the repository. Two attempts sharing one line would mean disposing
		// the first removes it while the second still needs it. That cannot
		// happen: the plan carries one entry per tick and the window admits one
		// entry at a time, so two attempts of the SAME tick are never live
		// together, and two different ticks have two different prefixes.
		ArtifactPrefix: "runs/" + d.RunID + "/" + d.TickID + "/",
		Credentials: subprocess.Credentials{
			Model:  subprocess.ModelCredential{Shorthand: "issued-by-host"},
			Source: sourceCredentialFor(d.Role, d.RunID),
		},
		// The EFFECTIVE budget, not the requested one: a job is issued the
		// number that will govern (Appendix A #12).
		//
		// What governs it in THIS phase is worth saying plainly, because a
		// number in a record reads like an enforced limit: `max_cost_usd`
		// binds a METERED credential (subprocess.Limits), and the model
		// credential a local subprocess attempt is issued is flat-rate
		// ("issued-by-host") — nothing meters what a runner spends against it.
		// So the number here is INFORMATIONAL for this executor: it travels
		// with the job, it is what every record and the operator's own
		// submission line say, and the thing that actually stops a job on this
		// host is the wall clock beside it. A metered executor is where it
		// starts binding, and the JobSpec already carries what such an
		// executor needs.
		Limits: subprocess.Limits{WallSeconds: r.opts.WallSeconds, MaxCostUSD: d.BudgetUSD},
	}
}

// sourceCredentialFor is the source half of the job's credentials. A read-only
// grade carries NO write_ref_prefix — the contract refuses one, and the reason
// is the point: read-only means the issuer hands out no push credential, so
// there is no namespace left to bound. The executor is what keeps that: it
// issues a read-only attempt no credential and launches its runner without the
// environment or the git configuration a push needs
// (internal/exec/subprocess/grade.go).
func sourceCredentialFor(role, runID string) subprocess.SourceCredential {
	if sourceGradeFor(role) == "read-only" {
		return subprocess.SourceCredential{Grant: &subprocess.SourceGrant{Issuer: "host", Grade: "read-only"}}
	}
	return subprocess.SourceCredential{
		Grant: &subprocess.SourceGrant{Issuer: "host", Grade: "write", WriteRefPrefix: attemptRefPrefix(runID)},
	}
}

// adopt re-addresses an attempt somebody else dispatched — including a
// previous incarnation of this reconciler, which is what a restart is.
//
// It never dispatches. If the executor has an attempt under the dispatch's
// private state root, the handle for it is reconstructed and INSPECTED; if
// there is none, nothing was ever started and the job is started now.
func (r *Reconciler) adopt(ctx context.Context, marker attemptHandle) (*subprocess.JobHandle, Executor, error) {
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		return nil, nil, err
	}
	executor, _, err := r.opts.NewExecutor(dispatch)
	if err != nil {
		return nil, nil, fmt.Errorf("build the executor for %s: %w", marker.TickID, err)
	}
	// The marker and the claim are two effects, in that order, and adopting is
	// what happens when a reconciler died between them — or when the tracker
	// REFUSED the claim, which leaves the same shape behind (epic-yoh's a08).
	claimErr := r.replayClaim(ctx, marker.TickID)
	state, found := findAttemptState(marker.StateRoot)
	if !found {
		// A worker is never started behind a claim the tracker refused: the
		// run holds on the refusal exactly as a fresh dispatch does, and the
		// attempt stays unstarted — and unspent — for the next resume.
		if claimErr != nil {
			return nil, nil, r.refuse(RefusedClaimWidth, marker.TickID,
				"the tracker refused to claim %s while adopting %s, which never started: %v. The run is HELD, "+
					"not failed: the attempt is not spent, and running the epic again under this run id starts it "+
					"once the width frees", marker.TickID, r.attemptName(marker.TickID, marker.Attempt), claimErr)
		}
		// The state directory is gone with the disk that held it — the
		// fresh-disk boot a replacement container makes, and the same path a
		// reconciler that died between marker and dispatch takes. They are one
		// branch because they must not be distinguishable: whatever the attempt
		// PUSHED is what it had already done, and origin can say how far that
		// was. The executor cuts the worktree from the pushed head
		// (executor.go startPoint), and the note below is the run's own
		// account of a boot that resumed rather than restarted.
		note := fmt.Sprintf(
			"attempt %d's disk is gone with its container and nothing it pushed survives: it starts over from the base",
			marker.Attempt)
		if pushed, err := r.remoteWork(branchOf(marker.WriteRef), marker.BaseSHA); err != nil {
			// Not "nothing survived": a failed read is not evidence of absence,
			// and saying it was would be the guess the executor refuses too.
			note = fmt.Sprintf(
				"attempt %d's disk is gone with its container, and what it pushed could not be read (%v): it starts from the base and what is on origin is settled by whoever finds it",
				marker.Attempt, err)
		} else if pushed != "" {
			note = fmt.Sprintf(
				"attempt %d's disk is gone with its container, and origin carries %s of it beyond the base: it continues from its own pushed work, and what it never pushed is redone",
				marker.Attempt, short(pushed))
		}
		// Nothing is running, so this one starts it. The resume note lands only
		// if the start did: a start that failed is the failure the run records,
		// and a resume that never happened is not a fact about the run.
		handle, err := executor.Start(r.jobSpec(dispatch))
		if err != nil {
			return nil, nil, r.startFailure(marker.TickID, err)
		}
		r.record(marker.TickID, StageResumed, "%s", note)
		r.noteAlive(marker.JobID)
		r.setTick(marker.TickID, "dispatched")
		return handle, executor, nil
	}
	handle := &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         marker.JobID,
		Attempt:       marker.Attempt,
		Executor:      marker.Executor,
		Handle:        map[string]any{"state": state},
	}
	status, err := executor.Inspect(handle, "")
	if err != nil {
		return nil, nil, fmt.Errorf("inspect the adopted attempt of %s: %w", marker.TickID, err)
	}
	if status.State == subprocess.StateLost && r.guarded(guardSettleFromEvidence) {
		return nil, nil, r.refuse(RefusedUnaddressed, marker.TickID,
			"%s cannot be addressed and has not settled: it is held, never redispatched",
			r.attemptName(marker.TickID, marker.Attempt))
	}
	r.noteAlive(marker.JobID)
	return handle, executor, nil
}

// replayClaim makes the tracker say this tick is being worked, for an attempt
// that is being ADOPTED rather than dispatched.
//
// claimDispatch's order is marker, then claim, then start, because a marker
// written after the dispatch guards nothing. The cost of that order is a
// window: a reconciler that died between the create and the claim left a
// marker on origin and a tick the tracker still reads as open. Nothing else
// closes that window — adopt never dispatches, so it never reaches
// claimDispatch's claim — and a tick worked through a whole run while the
// tracker says nobody holds it is the tracker lying about its own subject.
//
// It is settled from the tracker's OWN answer rather than from a memory of
// having claimed, so it is idempotent: a tick already in progress is left
// alone, and a tracker that cannot be read is reported rather than guessed at.
// A failure here is not fatal — the attempt exists either way, and refusing a
// tick because its claim could not be replayed would strand work that is
// already running.
//
// It still REPORTS a width refusal, typed, because one caller must act on it:
// an attempt whose marker landed and whose job never started is about to be
// STARTED by adopt, and starting a worker the tracker just refused to count is
// exactly the over-claim the refusal exists to stop (epic-yoh: a08's attempt
// was left in that state by a claim_width refusal).
func (r *Reconciler) replayClaim(ctx context.Context, tick string) error {
	current, err := r.tracker.Show(ctx, tick)
	if err != nil {
		r.record(tick, StageAdopted, "the adopted attempt's tick could not be read from the tracker: %v", err)
		return nil
	}
	if current.Status != "open" {
		return nil
	}
	if _, err := r.tracker.Claim(ctx, tick, r.opts.Owner); err != nil {
		r.record(tick, StageAdopted, "the adopted attempt's tick could not be claimed: %v", err)
		var width *tk.ErrDispatchWidth
		if errors.As(err, &width) {
			return err
		}
		return nil
	}
	r.record(tick, StageClaimed,
		"claimed for %s while adopting: the dispatch that made the marker never reached its claim", r.opts.Owner)
	return nil
}

func (r *Reconciler) dispatchFor(marker attemptHandle) (Dispatch, error) {
	dispatch := Dispatch{
		RunID: r.runID, EpicID: r.opts.EpicID, TickID: marker.TickID, Attempt: marker.Attempt,
		Try:   marker.Try,
		JobID: marker.JobID, Role: marker.Role, Repo: marker.Repo, Remote: marker.Remote,
		WriteRef: marker.WriteRef, BaseSHA: marker.BaseSHA, StateDir: marker.StateRoot,
		BaseRef: r.opts.BaseRef, Title: r.titleOf(marker.TickID),
		Tier: marker.Tier,
		// The executor the attempt RAN ON, off the marker — never the one a
		// profile re-resolved today would name (tick d6s): a later leg must
		// record what the attempt used, exactly as it does for the tier and
		// the substrate below.
		Executor:  marker.Executor,
		Substrate: Substrate{Protocol: marker.SubstrateProtocol, ServerVersion: marker.SubstrateServerVersion},
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
	// The work a dispatch CARRIED from a released attempt is read off the
	// marker like everything else a later leg must not re-derive (tick 0z0):
	// a later record of the same dispatch — a finding draft, a settle — says
	// what the DISPATCH resumed from, never what this incarnation would.
	dispatch.ResumedFrom = marker.ResumedFrom
	// The reports of the tick's earlier attempts (tick nvn) are re-derived
	// rather than carried, because a dispatch rebuilt from the marker can
	// still START the attempt — the marker landed, and nothing did — and the
	// prompt that start renders is the one place the predecessors' analysis
	// has to reach.
	dispatch.PriorReports = r.priorReports(marker.TickID, marker.Attempt)
	// The preserved work of the tick's earlier attempts (tick pbb) is
	// re-derived for the same reason: the dispatch a later leg rebuilds can
	// still start the attempt, and the preserved work is the half of a
	// stopped predecessor's legacy the prompt must not start blind over.
	dispatch.PriorSnapshots = r.priorSnapshots(marker.TickID, marker.Attempt)
	// The profile an adopted attempt re-joins is the one it was DISPATCHED
	// under: the marker's own tier, not whatever this incarnation would
	// derive today. A config edited between incarnations does not retro-fit a
	// running attempt with a different model.
	profile, err := r.profileForTier(dispatch.Role, marker.Tier)
	if err != nil {
		return Dispatch{}, fmt.Errorf("%s recorded tier %q and no profile resolves against the "+
			"runner configuration as it stands: %w", r.attemptName(marker.TickID, marker.Attempt), marker.Tier, err)
	}
	dispatch.Profile = profile
	return dispatch, nil
}

// titleOf is the tick's title as the plan read it from the tracker, for a
// dispatch rebuilt from a marker (the marker carries the identity, not the
// prose). Empty when the plan no longer carries the tick — a leg that never
// starts work — and the executor that requires a title refuses such a start
// loudly rather than minting one from anywhere else.
func (r *Reconciler) titleOf(tickID string) string {
	if r.titles == nil {
		return ""
	}
	return r.titles[tickID]
}

// carryHead is the commit a dispatch CARRIED from a released attempt starts
// from: the work the attempt left, read exactly as disposition reads it —
// origin's branch first, the branch in this checkout when every push the
// attempt made failed. A branch that carries nothing beyond the base the
// attempt was cut from is an error rather than a quiet fall-back to the
// integration branch: the person released the attempt asking for its work to
// go forward, and answering that with a base that has none of it would
// strand the work precisely the way the carry exists not to. The work having
// gone is an operational fact to report, not a decision for the run to make.
func (r *Reconciler) carryHead(marker attemptHandle) (string, error) {
	branch := branchOf(marker.WriteRef)
	head, err := r.remoteWork(branch, marker.BaseSHA)
	if err != nil {
		return "", fmt.Errorf("reconcile: read %s on %s to carry the work of %s: %w",
			branch, r.opts.Remote, r.attemptName(marker.TickID, marker.Attempt), err)
	}
	if head == "" {
		head = r.attemptWorkHead(marker)
	}
	if head == "" {
		return "", fmt.Errorf(
			"reconcile: the release of %s carries its work, but %s carries no commit beyond the "+
				"base it was cut from: there is nothing to start the next try from. The settlement stands; "+
				"re-release without --carry-work, or put the work back on %s and run the epic again",
			r.attemptName(marker.TickID, marker.Attempt), branch, branch)
	}
	return head, nil
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

// priorReports gathers the archived reports of a tick's EARLIER attempts,
// newest first (tick nvn) — this run's own AND every previous run's (tick
// n4h).
//
// The reports are what tick 35h made survive teardown: report.md beside the
// attempt record, in the executor state directory a dispatch was assigned.
// findAttemptState locates a predecessor's state the same way every adopt
// and teardown already does — by walking for the attempt record rather than
// by recomputing an executor's internal naming — and the report sits under
// the name that executor exports as part of that seam.
//
// nvn's discovery walked <ExecStateRoot>/<runID>/<tick>/<n> and nothing
// else, so a re-run of the epic under a NEW run id — a fresh run-state
// store, attempt numbers that begin again at 1 — saw none of the previous
// run's reports: the starting-blind case the feature exists for, one level
// up. The discovery here is therefore keyed by the TICK: every OTHER run id
// under the executor state root that holds the tick's attempts is walked
// too, and an attempt whose own record names a different tick is not
// offered, because a state root shared by several runs can hold the
// same-named directory of an attempt that was genuinely another tick's.
// The reports stay local to the executor's state root, which is where the
// executor put them; no worker prose moves into the target repository.
//
// Nothing here can refuse a dispatch: a predecessor with no report (it never
// settled, or settled without saying anything) is simply absent from the list,
// because the prompt's job is to name the analysis that EXISTS, not to narrate
// the attempts that produced none. A first attempt of a first run gathers
// nothing.
func (r *Reconciler) priorReports(tickID string, attempt int) []subprocess.PriorReport {
	out := []subprocess.PriorReport{}
	for n := attempt - 1; n >= 1; n-- {
		if prior, ok := priorReportAt(r.execStateDir(tickID, n), n, tickID, ""); ok {
			out = append(out, prior)
		}
	}
	for _, run := range r.priorRuns(tickID) {
		tickDir := filepath.Join(r.opts.ExecStateRoot, run, tickID)
		for _, n := range attemptNumbers(tickDir) {
			if prior, ok := priorReportAt(filepath.Join(tickDir, strconv.Itoa(n)), n, tickID, run); ok {
				out = append(out, prior)
			}
		}
	}
	return out
}

// priorReportAt reads one predecessor's archived report out of the state
// directory a dispatch was given (tick nvn). run is the run the predecessor
// was dispatched under — empty when it was this run's own attempt, the run's
// id when it was another run's (tick n4h), so a worker handed a cross-run
// report can tell it from this run's numbering — and tickID is the tick the
// attempt's own record must name, the key the cross-run discovery is keyed by.
func priorReportAt(stateDir string, attempt int, tickID, run string) (subprocess.PriorReport, bool) {
	state, found := findAttemptState(stateDir)
	if !found {
		// Never dispatched, or never started: no report to name.
		return subprocess.PriorReport{}, false
	}
	issuedAt, ok := attemptFacts(state, tickID)
	if !ok {
		// An attempt record that does not read, or that names another tick:
		// the record that cannot be parsed is the same as no record.
		return subprocess.PriorReport{}, false
	}
	path := filepath.Join(state, subprocess.FileReportArchive)
	raw, err := os.ReadFile(path)
	if err != nil {
		// Dispatched, but it left no report — settled with nothing said.
		// The attempt is still visible to the worker through the run's own
		// records; it has no analysis to hand over.
		return subprocess.PriorReport{}, false
	}
	report := subprocess.ParseReport(string(raw))
	return subprocess.PriorReport{
		Attempt: attempt, Run: run, Path: path,
		Status: report.Status, Detail: report.Detail, Dispatched: issuedAt,
	}, true
}

// attemptFacts reads the two facts a predecessor's attempt record carries
// that its directory names cannot (tick n4h): when the attempt was dispatched
// — the one fact that orders a list spanning runs, since attempt numbers
// are a run's own count — and WHICH TICK the attempt was for, so a state
// root shared by several runs never hands one tick the same-named attempt
// of another. The record is read loosely, the way the marker reader reads
// it: both executors write it, and this is the reconciler walking their
// seam, not importing their shape.
func attemptFacts(state, tickID string) (issuedAt string, ok bool) {
	raw, err := os.ReadFile(filepath.Join(state, "attempt.json"))
	if err != nil {
		return "", false
	}
	var record struct {
		TickID   string `json:"tick_id"`
		IssuedAt string `json:"issued_at"`
	}
	if json.Unmarshal(raw, &record) != nil || record.TickID != tickID {
		return "", false
	}
	return record.IssuedAt, true
}

// priorRuns lists, deterministically, every OTHER run id under the executor
// state root that holds attempts of this tick (tick n4h). The order is
// alphabetical because it is only a gathering order — the prompt owns the
// order the worker reads — and a deterministic one keeps a dispatch that is
// re-derived half-made identical to itself.
func (r *Reconciler) priorRuns(tickID string) []string {
	entries, err := os.ReadDir(r.opts.ExecStateRoot)
	if err != nil {
		// No state root, or nothing under it: no previous run to read.
		return nil
	}
	var runs []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == r.runID {
			continue
		}
		if info, err := os.Stat(filepath.Join(r.opts.ExecStateRoot, entry.Name(), tickID)); err == nil && info.IsDir() {
			runs = append(runs, entry.Name())
		}
	}
	sort.Strings(runs)
	return runs
}

// attemptNumbers lists the attempt numbers one run's tick directory holds,
// descending. The names are the reconciler's own layout — one numeric
// directory per dispatch, named for the run's own attempt number — so
// reading them back is not recomputing an executor's naming, and every
// number the tick's earlier attempts landed on is visited whatever the
// numbering of the run that used it.
func attemptNumbers(tickDir string) []int {
	entries, err := os.ReadDir(tickDir)
	if err != nil {
		return nil
	}
	var numbers []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		n, err := strconv.Atoi(entry.Name())
		if err != nil || n < 1 {
			continue
		}
		numbers = append(numbers, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(numbers)))
	return numbers
}

// priorSnapshots gathers the preserved work of a tick's EARLIER attempts,
// newest first (tick pbb) — the nvn shape for reports, applied to work.
//
// The snapshot is what a stop and a rejecting teardown preserve before they
// destroy a worktree: the attempt's uncommitted tree on a wip ref of its
// own, recorded beside the attempt record — at a name and in a shape both
// executors export as part of the seam — so the work survives teardown at a
// place the dispatch can find again. A predecessor with no record preserved
// nothing — it committed its work, or nothing destroyed its worktree — and is
// simply absent from the list: the prompt's job is to name the material that
// EXISTS, not to narrate the attempts that produced none. A first attempt
// gathers nothing.
//
// Nothing here validates that the ref still resolves: the worktree's
// repository is the same one the dispatch's own worktree is cut from, and a
// record naming a ref git cannot resolve is a fact the worker reads in its
// own git — naming it with its recorded commit is still more than starting
// blind. The record that cannot be parsed is the same as no record.
func (r *Reconciler) priorSnapshots(tickID string, attempt int) []subprocess.PriorSnapshot {
	out := []subprocess.PriorSnapshot{}
	if attempt <= 1 {
		return out
	}
	for n := attempt - 1; n >= 1; n-- {
		state, found := findAttemptState(r.execStateDir(tickID, n))
		if !found {
			// Never dispatched, or never started: nothing was preserved.
			continue
		}
		raw, err := os.ReadFile(filepath.Join(state, subprocess.FileWIPSnapshot))
		if err != nil {
			// Dispatched, but nothing was preserved — the attempt's worktree
			// was clean or its teardown refused rather than destroyed.
			continue
		}
		var snap subprocess.WIPSnapshot
		if json.Unmarshal(raw, &snap) != nil || snap.SchemaVersion != subprocess.WIPSnapshotSchemaVersion ||
			snap.Ref == "" || snap.Commit == "" {
			continue
		}
		out = append(out, subprocess.PriorSnapshot{Attempt: n, Ref: snap.Ref, Commit: snap.Commit})
	}
	return out
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
	return r.awaitInflight(ctx, r.newInflight(planEntry{TickID: marker.TickID}, handle, executor, marker))
}

// awaitInflight addresses ONE attempt until it settles — the sequential wait,
// expressed as a window of one.
func (r *Reconciler) awaitInflight(ctx context.Context, fl *inflightAttempt) (*subprocess.JobStatus, error) {
	for {
		status, err := r.addressOnce(ctx, fl)
		if err != nil {
			return nil, err
		}
		if status != nil {
			return status, nil
		}
		if err := r.restBetweenPolls(fl); err != nil {
			return nil, err
		}
	}
}

// inflightAttempt is one dispatched attempt the run is addressing, and the
// per-attempt state the addressing carries between polls.
//
// It exists so that the wait can be MULTIPLEXED: one goroutine addresses
// several attempts by taking one poll of each in turn, rather than blocking on
// the first until it settles. Every field here used to be a local variable of
// waitForSettlement, which is exactly why only one attempt could ever be
// waited on at a time.
type inflightAttempt struct {
	entry    planEntry
	handle   *subprocess.JobHandle
	executor Executor
	marker   attemptHandle

	step *Step

	// deadline is when this attempt's ISSUED BUDGET is spent, in CALENDAR
	// time: the dispatch stamp off the durable marker plus the wall clock it
	// was issued. It is deliberately a wall-clock comparison — see
	// settlementDeadline — and on its own it is not enough to refuse anything.
	//
	// overdueAt is when THIS RUN first SAW the budget spent — the first poll
	// at which the calendar half above was already true — read from the run's
	// own clock, so the interval since is monotonic (tick 8jl). It is the
	// other half of the refusal, and the half a suspended host cannot forge.
	//
	// It counts from the bound firing and NOT from the dispatch, because the
	// claim the refusal makes is "this run has watched it, awake, WITHOUT IT
	// SETTLING, since its budget ran out". Counting from dispatch would make
	// both halves come true in the same instant and the grace would delay
	// nothing — which is what it did on the first cut of this tick, refusing
	// a stopped attempt a second past its bound instead of giving the
	// executor's stop time to land and be collected (tick pbb's acceptance
	// caught it).
	//
	// Zero until that first poll, which is what makes a RESUMED run safe: an
	// incarnation that has just adopted an attempt whose budget ran out hours
	// ago starts its own grace at its own first poll, and has watched it for
	// no time at all.
	deadline  time.Time
	overdueAt time.Time

	cursor string
	// interval is the cadence this attempt is addressed at. It is the
	// EXECUTOR's, not one global constant (tick u9l, epic av8), so a window
	// holding a local and a cloud attempt addresses each at its own beat.
	interval time.Duration

	// progress is the last liveness measurement of this attempt and probedAt
	// when it was taken (tick dh1). They live on the attempt rather than in
	// the measurement because the walk is the expensive part: one probe
	// serves the liveness record AND the stall warning, so the two can never
	// report different numbers for the same attempt at the same moment, and
	// the worktree is walked once instead of twice.
	progress *runprogress.Attempt
	probedAt time.Time
	// baseline is the mtime the run's FIRST look at this worktree found —
	// taken when the attempt joins the window, so the first probe already
	// carries a count — and what every probe counts files newer than.
	//
	// The obvious baseline — the attempt's dispatch stamp — cannot be used,
	// and finding out why is what this field is. The durable marker records
	// DispatchedAt as RFC3339 at SECOND resolution, and the worktree is
	// checked out on the other side of the claim: on epic ncv the marker for
	// ef7 sits between 16:51:23 and 16:51:29 while the checkout stamped its
	// files at 16:51:25.15. Counting "files newer than the dispatch stamp"
	// would therefore have counted the whole checkout — every file in the
	// repository — as work the agent did, which is the opposite of the answer
	// and would have been worse than no answer at all.
	//
	// So the run calibrates on a look it took itself, in this worktree,
	// against no clock at all: what the newest file's mtime was when the
	// attempt joined the window. For a fresh dispatch that look is taken
	// seconds after the checkout, so the baseline IS the checkout and the
	// count is "since dispatch" in everything but name; for an ADOPTED
	// attempt it is honestly "since this incarnation first looked", which is
	// the only thing a run that did not dispatch it can claim.
	baseline time.Time

	// integrated marks an attempt that was REJECTED and whose work is already
	// on the integration branch — something other than this run merged it
	// (epic-yoh's cr4, merged by hand after its merge_failed). It has no
	// handle and no executor: there is nothing left to address, so it joins
	// the window already settled and goes straight to the finish, which proves
	// the containment again, gates the epic head and closes.
	integrated bool
}

func (r *Reconciler) newInflight(entry planEntry, handle *subprocess.JobHandle, executor Executor, marker attemptHandle) *inflightAttempt {
	fl := &inflightAttempt{
		entry: entry, handle: handle, executor: executor, marker: marker,
		step:     r.OpenStep(r.stepCap),
		deadline: r.settlementDeadline(marker),
		interval: r.pollIntervalFor(marker.Executor),
	}
	// The baseline the liveness count is measured against is taken HERE, not
	// at the first probe (tick dh1). The worktree exists by now — the
	// executor's Start created it, and an adopted attempt's has stood since
	// its own dispatch — so the run can calibrate before it has ever waited,
	// and the very first probe then carries a count instead of a null. A
	// first look that answers "not measured" is one whole probe interval in
	// which the wedged attempt and the working one still read alike, which is
	// the interval this tick exists to remove.
	r.calibrateProgress(fl)
	return fl
}

// addressOnce takes exactly ONE poll of one in-flight attempt.
//
// It returns the settled status when the attempt is terminal, a refusal when
// the attempt cannot be addressed or has outlived its bounds, and (nil, nil)
// when the honest answer is "still running, ask again later". The caller owns
// the waiting, which is what lets one caller own SEVERAL attempts.
//
// Everything this touches — the feed, the store, the run's own counters — is
// touched from the caller's goroutine. That is deliberate: this run's three
// worst defects were all process-global git state written by two parties at
// once, and a window that polls in one goroutine cannot reproduce them.
func (r *Reconciler) addressOnce(ctx context.Context, fl *inflightAttempt) (*subprocess.JobStatus, error) {
	marker := fl.marker
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	status, err := fl.executor.Inspect(fl.handle, fl.cursor)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", marker.TickID, err)
	}
	if status.Cursor != nil {
		fl.cursor = *status.Cursor
	}
	if status.Terminal {
		// How long it outlived its bound, if it had one and had passed it
		// (tick dh1). Neither ncv attempt honoured the wall-clock interrupt:
		// both were sent it every five seconds for about ninety seconds,
		// ignored it, and had to be stopped by closing the pane — and nothing
		// ticfac kept said so, which made "the grace period works" and "the
		// grace period is theatre" the same run. The run states the half it
		// can observe itself, the overrun, and carries the executor's last
		// sentence for the half only the executor can write.
		if overran := r.recordStop(fl, status.State, lastObservation(status)); overran != nil {
			r.record(marker.TickID, StageWaiting,
				"settled as %s, %s after the wall clock of %ds fired: the stop was not instant, and how long it took "+
					"is the honest measure of whether the interrupt was honoured — %s",
				status.State, overran.Round(time.Second), r.opts.WallSeconds, lastObservation(status))
			return status, nil
		}
		r.record(marker.TickID, StageWaiting, "settled as %s", status.State)
		return status, nil
	}
	{
		// What the attempt has actually PRODUCED, measured from the worktree
		// that is already on disk and recorded where it can be read
		// afterwards (tick dh1, liveness.go). It is taken before the two
		// announcements below because the stall warning reads this probe
		// rather than walking the worktree a second time.
		r.probeProgress(fl)

		if status.State == subprocess.StateLost && r.guarded(guardSettleFromEvidence) {
			return nil, r.refuse(RefusedUnaddressed, marker.TickID,
				"%s cannot be addressed and has not settled: nobody can say whether it is running, "+
					"which is not the same as nothing running", r.attemptName(marker.TickID, marker.Attempt))
		}

		// The wall clock's FIRING is a feed event, not only an observation in
		// the attempt's own store (tick emk): the stop is the executor's to
		// make — the local supervisor kills, the herdr executor delivers
		// herdr's interrupt on this very poll, inside the Inspect above — but
		// whether the stop TOOK is visible only by waiting for the next poll,
		// and a watcher reading the feed saw nothing at all while an agent
		// ran 26 minutes past its bound (the Phase 3 incident: the feed's
		// last line was `dispatched` 85 minutes earlier). So the run says the
		// bound fired the moment it can see the attempt has not settled past
		// it, once, with the executor's own last word about what the
		// substrate was seen doing — a hint about when to look, never a
		// verdict, and no claim the stop did or did not take.
		if wallAt, ok := r.wallClockAt(marker); ok && r.now().After(wallAt) {
			r.announceWall(marker, status, wallAt)
		}

		// The early warning before that bound (tick 7zs): the attempt is
		// alive and liveness was never the question — the question is whether
		// it is getting anywhere, and the two facts that answer it honestly
		// are read out of the repo itself. The wall clock is the bound; this
		// is the reason to look while there is still an attempt to look at.
		r.announceStall(fl)

		// The reconciler's OWN deadline: its budget spent in calendar time AND
		// this run's own patience spent, awake, watching it. Both halves, in
		// the clocks that answer them honestly — unaddressable says why, and
		// tick 8jl says what a host suspend did before it took two.
		//
		// An attempt that satisfies both, still reading `running`, is one
		// nobody can say is running. That is `unaddressed`, not `wiped`: the
		// substrate did not take it away, and a person is the next actor
		// (settle.go).
		if over, watched, unaddressable := r.unaddressable(fl); unaddressable {
			// What the executor last SAW goes in the refusal, because the two
			// shapes need different first moves. A local subprocess whose
			// supervisor died leaves a pid nobody can trust. A herdr agent can
			// be alive and simply not stopping: in the Phase 3 run a pi worker
			// ran 26 minutes past its wall clock while the interrupt was
			// re-delivered at every poll, and a refusal about a dead supervisor
			// sent the reader at the wrong problem (tick emk).
			return nil, r.refuse(RefusedUnaddressed, marker.TickID,
				"%s still reads %s %s past the wall clock of %ds it was issued, and this run has "+
					"watched it for %s without it settling. Its executor could not settle it: %s. Nobody can say "+
					"it is finished; look at it, stop whatever is still running, then release it with "+
					"`ticfac settle %s %s %d --release \"<who>\"`",
				r.attemptName(marker.TickID, marker.Attempt), status.State, over.Round(time.Second),
				r.opts.WallSeconds, watched.Round(time.Second), lastObservation(status),
				r.opts.EpicID, marker.TickID, marker.Attempt)
		}

		// The poll IS the keepalive. Its answer is about the substrate, not
		// about the job: a job that went unaddressed past the threshold is
		// gone, whatever the last status said.
		if r.Poll(marker.JobID) == Wiped {
			return nil, r.refuse(RefusedWiped, marker.TickID,
				"%s went unaddressed for longer than the substrate's wipe threshold of %s",
				r.attemptName(marker.TickID, marker.Attempt), r.wipeThreshold)
		}

	}
	return nil, nil
}

// restBetweenPolls is the pause between one attempt's polls, and the place the
// step cap is spent.
//
// Appendix A #3: no step outlives the host's cap, so a long wait is spread
// across bounded legs and each leg RE-DERIVES what it knows from durable facts
// rather than carrying the previous leg's memory. The re-derivation is the
// run's, not the attempt's — a window of attempts shares one store and one set
// of counters — so a leg that ends re-reads once and every attempt in the
// window continues against what it read.
func (r *Reconciler) restBetweenPolls(fl *inflightAttempt) error {
	return r.restWindow([]*inflightAttempt{fl})
}

// lastObservation is the executor's own last word about an attempt, for a
// refusal that would otherwise have to guess at the cause. The executor is the
// only party that can see the substrate, and its observations are how it says
// what it saw — "the interrupt was delivered but the agent has not exited" is
// a different first move from "the supervisor is gone".
func lastObservation(status *subprocess.JobStatus) string {
	if status == nil || len(status.Observations) == 0 {
		return "the executor recorded no observation about it"
	}
	last := status.Observations[len(status.Observations)-1]
	if strings.TrimSpace(last.Detail) == "" {
		return "the executor's last observation carried no detail"
	}
	return last.Detail
}

// wallClockAt is the moment the bound THIS attempt was issued fires: issued +
// WallSeconds, read from the durable dispatch marker on origin — the same
// arithmetic the settlement deadline starts from, and the same bound the
// executor enforces on its own side (the local supervisor's kill timer, the
// herdr executor's interrupt). The reconciler announced no bound of its own
// when the run carries none (WallSeconds zero) and claims no firing for a
// marker whose issue stamp cannot be read: a line about a bound nobody can
// date is a line a reader cannot act on, and the attempt falls to the
// settlement deadline as before.
func (r *Reconciler) wallClockAt(marker attemptHandle) (time.Time, bool) {
	if r.opts.WallSeconds <= 0 {
		return time.Time{}, false
	}
	issued, ok := r.dispatchedAt(marker)
	if !ok {
		return time.Time{}, false
	}
	return issued.Add(time.Duration(r.opts.WallSeconds) * time.Second), true
}

// announceWall writes the bound's firing to the feed — ONCE per tick, like
// every terminal-shaped fact: the re-delivery at every poll is the
// executor's business, and a feed that repeats one fact at poll cadence
// teaches a watcher to ignore it. A resumed run that adopts the attempt
// again re-announces it, which is correct in the feed's own terms: the line
// is a hint about when to look, and a watcher joining a run that is already
// past the bound needs the hint as much as the first watcher did.
//
// The detail carries the executor's last observation because the line would
// otherwise send the reader at the shape the message assumed — the incident
// again: "interrupted but it has not exited" and "the supervisor is gone"
// demand different first moves, and only the executor can say which it saw.
func (r *Reconciler) announceWall(marker attemptHandle, status *subprocess.JobStatus, wallAt time.Time) {
	for i := len(r.journal) - 1; i >= 0; i-- {
		event := r.journal[i]
		if event.Tick != marker.TickID {
			continue
		}
		if event.Stage == StageWallClock {
			return
		}
		break
	}
	r.record(marker.TickID, StageWallClock,
		"the wall clock of %ds fired %s ago and %s has not settled: the executor is stopping it — %s",
		r.opts.WallSeconds, r.now().Sub(wallAt).Round(time.Second), r.attemptName(marker.TickID, marker.Attempt),
		lastObservation(status))
}

// announceStall is the early warning before the bound (tick 7zs): the feed's
// one line saying an attempt is alive but has produced nothing durable for
// longer than the configured threshold. Liveness answers "is it running";
// nothing answered "is it getting anywhere", and in the Phase 3 run the feed
// was silent for 40 of the worker's 55 minutes precisely because nothing
// happened that the run observes — the run was alive and true, and a person
// caught it by reading the pane.
//
// The two facts are read out of the repo itself — how long since the
// attempt's branch last moved, how long since its worktree last changed —
// through the same executor-neutral measurement `ticfac status` reports, so
// the feed line and the status surface cannot disagree about what they
// measured. Both are measurements and neither is a judgement: a worker
// thinking hard legitimately commits nothing for a while, so the line stops,
// rejects and holds nothing, and a measurement that cannot be made (a
// substrate whose worktree is not local, a worktree being torn down) says
// nothing rather than guessing — the same honesty the gap itself keeps.
//
// It is written ONCE per tick in this incarnation, like every
// terminal-shaped fact: the journal remembers the line, and a feed that
// repeats one fact at poll cadence teaches a watcher to ignore it. A
// restarted run that adopts the attempt again re-warns, which is correct in
// the feed's own terms — a watcher joining a stalled run needs the hint as
// much as the first watcher did.
//
// It now also says WHICH kind of quiet it found, from the liveness probe's
// count of files written since the run first looked (tick dh1). The gaps
// alone could
// not: a worktree nobody has touched carries the mtimes its checkout stamped,
// so "the newest thing here happened at dispatch" is what a wedged agent and
// a thinking one both measure, and the line read the same for 9fc — which
// went on to produce 433 lines — and for ef7, which produced an empty commit.
// Zero files written is a different sentence from eleven, and it is the one an
// operator can act on.
func (r *Reconciler) announceStall(fl *inflightAttempt) {
	if r.opts.StallWarnAfter <= 0 {
		return
	}
	marker := fl.marker
	for i := len(r.journal) - 1; i >= 0; i-- {
		if r.journal[i].Tick == marker.TickID && r.journal[i].Stage == StageStallWarned {
			return
		}
	}
	// Nothing the attempt has done can be older than the attempt: the gap
	// counts from the newest worktree file, and the worktree is created at
	// dispatch, so before issued+threshold the gap is under the threshold by
	// construction and the warning is skipped.
	if issued, ok := r.dispatchedAt(marker); ok && r.now().Before(issued.Add(r.opts.StallWarnAfter)) {
		return
	}
	// The latest probe, never a second walk of the same worktree. It can be
	// up to one probe interval stale, and that is safe in the only direction
	// that matters: an idle gap only grows, so a stale measurement
	// UNDER-reports and the warning can be late by at most a probe — a minute
	// against a fifteen-minute threshold — never early.
	//
	// A measurement that could not be made is not a run event: the hint is
	// only worth a feed line when it is a fact, and the attempt falls to the
	// wall clock and the settlement deadline as before.
	if fl.progress == nil {
		return
	}
	gap := *fl.progress
	idle, ok := gap.Idle()
	if !ok || idle < r.opts.StallWarnAfter {
		return
	}
	r.record(marker.TickID, StageStallWarned,
		"%s is alive but has produced nothing durable for %s: its branch last moved %s ago, its "+
			"worktree last changed %s ago, and %s — a reason to look, not a verdict; the wall clock of %ds is "+
			"still the bound",
		r.attemptName(marker.TickID, marker.Attempt), idle,
		idleOf(gap.BranchIdle), idleOf(gap.WorktreeIdle), writtenOf(gap.ChangedFiles), r.opts.WallSeconds)
}

// writtenOf renders the liveness count for the warning's prose, and says
// "not measured" rather than zero when nobody could count: a run that could
// not read the worktree must not be heard saying the agent wrote nothing.
func writtenOf(n *int) string {
	switch {
	case n == nil:
		return "how much it has written was not measured"
	case *n == 0:
		return "it has written NOTHING into its worktree since the run first looked at it"
	case *n == 1:
		return "it has written 1 file into its worktree since the run first looked at it"
	default:
		return fmt.Sprintf("it has written %d files into its worktree since the run first looked at it", *n)
	}
}

// idleOf renders one measured gap for the feed line, naming the fact rather
// than a guess when the gap could not be read.
func idleOf(d *runprogress.Duration) string {
	if d == nil {
		return "?"
	}
	return d.Round(time.Second).String()
}

// settlementDeadline is the moment after which an unsettled attempt is one
// nobody can say is running.
//
// It is derived from DURABLE facts, not from when this incarnation started
// waiting: the dispatch marker on origin says when the attempt was issued, so
// a restart that adopts an attempt inherits the same deadline rather than
// giving a dead job a fresh hour every time somebody restarts the run. The
// grace on top of the job's own wall clock is one wipe threshold — long enough
// for a supervisor that is merely slow to write its terminal record, and
// bounded, which is the whole point.
//
// A marker that cannot be read leaves the deadline measured from NOW. That is
// weaker and deliberately not fatal: an unbounded wait is the failure this
// exists to remove, and refusing a tick because a timestamp would not parse
// would be a worse one.
func (r *Reconciler) settlementDeadline(marker attemptHandle) time.Time {
	issued := r.now()
	if at, ok := r.dispatchedAt(marker); ok {
		issued = at
	}
	// CALENDAR time, deliberately, and the monotonic reading is stripped so it
	// is calendar time whoever asks (tick 8jl). The dispatch stamp comes off
	// the durable marker as RFC3339 and so has no monotonic reading of its
	// own; making that explicit here means the comparison in addressOnce reads
	// the same way on a fresh dispatch and on an adopted one, instead of
	// silently changing clock depending on whether the marker could be read.
	//
	// The grace that used to be added here — a wipe threshold for the
	// supervisor to write its terminal record — has moved to the OTHER half of
	// the refusal, where it is spent in the run's own clock. What is left is
	// the one thing calendar time answers honestly: the attempt's issued
	// budget is spent.
	return issued.Add(time.Duration(r.opts.WallSeconds) * time.Second).Round(0)
}

// unaddressable is the reconciler's own deadline, and it takes TWO clocks to
// state honestly (tick 8jl).
//
// The bound exists because a job's wall clock is the supervisor's to enforce
// and a supervisor that died without settling enforces nothing: `running` then
// rests on a pid, and a pid is a number the operating system reuses. Nothing in
// the poll loop notices, because the wipe threshold measures the interval
// between polls and not the age of the job, so the run would address a dead
// attempt forever at perfect cadence.
//
// What it must never do is refuse an attempt that was merely not being worked
// on. Until this tick the whole test was `now.After(fl.deadline)` against a
// deadline parsed out of the durable marker — a WALL comparison — so a host
// that suspended aged every live attempt past its bound while it slept, and the
// first poll after the lid opened refused all of them at once. On an operator's
// laptop, which is where these runs live, closing the lid killed healthy runs
// and the refusal named the attempts rather than the sleep. Epic dha woke with
// four attempts 5 to 7 hours past their wall clocks; every one of them settled
// SUCCESSFULLY within a minute of the machine waking.
//
// So the two halves are asked in the clocks that answer them:
//
//   - Has the attempt's issued budget been spent? CALENDAR time, from the
//     durable dispatch stamp. An agent issued an hour has had its hour when an
//     hour of the world has passed; that is what the operator bought.
//   - Has THIS RUN watched it, awake, SINCE THAT MOMENT, long enough to
//     conclude nobody can say it is running? MONOTONIC time, from the first
//     poll that saw the budget spent, for a full wipe threshold — the same
//     grace the formula always carried and the same clock the wipe threshold
//     beside it already spends (guards.go). Time the host spent asleep is time
//     nobody spent watching.
//
// The second half counts from the BOUND FIRING and not from the dispatch, and
// that is load-bearing rather than tidy. The grace exists so the executor's
// stop has time to land and the supervisor has time to write its terminal
// record — all of which happens AFTER the wall clock fires. Counting from
// dispatch makes both halves come true in the same instant, so the grace
// delays nothing and a stopped attempt is refused a second past its bound
// instead of being collected. That was the first cut of this tick, and tick
// pbb's acceptance caught it.
//
// Both, or no refusal. The second half is the one a suspended host cannot
// forge, and it is what makes a RESUMED run safe: an incarnation that has just
// adopted an attempt whose budget ran out hours ago starts its grace at its own
// first poll and has watched it for zero seconds.
//
// Noting the moment is a WRITE, the way probeProgress records probedAt: this is
// the only reader and the only writer of it, and an observation nobody wrote
// down is one the next poll cannot use.
func (r *Reconciler) unaddressable(fl *inflightAttempt) (over time.Duration, watched time.Duration, ok bool) {
	now := r.now()
	if !now.After(fl.deadline) {
		return 0, 0, false
	}
	if fl.overdueAt.IsZero() {
		fl.overdueAt = now
	}
	watched = now.Sub(fl.overdueAt)
	if watched < r.wipeThreshold {
		return 0, 0, false
	}
	return now.Sub(fl.deadline), watched, true
}

// dispatchedAt is when the attempt's own durable marker on origin says it was
// issued. It is the anchor every bound this run derives — the settlement
// deadline and the wall clock's firing — so both read the same fact, and a
// restarted run inherits the same bounds rather than re-issuing them.
func (r *Reconciler) dispatchedAt(marker attemptHandle) (time.Time, bool) {
	record, ok, err := r.store.Attempt(marker.Attempt)
	if err != nil || !ok || record.TickID != marker.TickID {
		return time.Time{}, false
	}
	at, parseErr := time.Parse(time.RFC3339, record.DispatchedAt)
	if parseErr != nil {
		return time.Time{}, false
	}
	return at, true
}

// ---------------------------------------------------------- the collect ---

func (r *Reconciler) collect(ctx context.Context, handle *subprocess.JobHandle, executor Executor, marker attemptHandle, status *subprocess.JobStatus) (*subprocess.Collection, error) {
	if _, err := r.checkpoint(runstate.StateCollecting, fmt.Sprintf("collecting %s attempt %d", marker.TickID, marker.Attempt)); err != nil {
		return nil, err
	}
	collected, err := executor.CollectDetail(handle)
	if err != nil {
		return nil, fmt.Errorf("collect %s: %w", marker.TickID, err)
	}
	r.setTick(marker.TickID, "reported")
	// Tick 19l: what the worker answered and what the run concluded are two
	// claims by two parties, stated separately — never one sentence that reads
	// as the worker declaring the run's verdict.
	r.record(marker.TickID, StageCollected, "%s", collectedLine("the "+marker.Role+" job", collected))

	// The work this collect is about to rule on is made durable on origin
	// BEFORE any verdict is recorded over it (ticfac tick 55i). The only
	// other push lives in integrate's durableAttemptHead, so an attempt that
	// is refused HERE never reaches it — and an attempt whose commits exist
	// only in its worktree is one `git worktree remove --force` away from
	// gone, which is exactly what the teardown that follows a rejection
	// does. Making the write ref durable first is what keeps settle's
	// promise — "whatever this one committed stays on its own write ref" —
	// true for the attempt whose work is most at risk: the one nothing
	// merged.
	r.preserveAttemptWork(marker)

	// Appendix A #10's premise is that compliance is not a property of the
	// model, and a boundary measured from a base the enforced party can choose
	// is not a boundary. The executor reads the base out of the attempt record
	// beside the worker's own worktree, owned by the worker's uid; the
	// reconciler's marker is on ORIGIN, written before the dispatch and never
	// in the worker's reach. So the two are compared, and a diff measured from
	// anything but the base this run dispatched is refused rather than trusted:
	// a base moved forward to the attempt's own head makes every change
	// invisible, boundary violations included.
	if collected.Result != nil && marker.BaseSHA != "" && collected.Result.Source.BaseSHA != marker.BaseSHA {
		if err := r.rejectDurably(marker, collected.Verdict, "the collected base is not the dispatched base"); err != nil {
			return nil, err
		}
		r.record(marker.TickID, StageRejected, "the collect was measured from %s, not from the dispatched base %s",
			short(collected.Result.Source.BaseSHA), short(marker.BaseSHA))
		r.disposeRejected(handle, executor, marker, "the collected base is not the base this run dispatched")
		return nil, r.refuse(RefusedBoundary, marker.TickID,
			"%s was collected against base %s, but this run dispatched it at %s: the diff the boundary "+
				"check read is not the diff of this attempt, so nothing it reports about it can be believed",
			r.attemptName(marker.TickID, marker.Attempt), short(collected.Result.Source.BaseSHA), short(marker.BaseSHA))
	}

	// The findings channel (tick 7vn), BEFORE the verdict checks: a finding is
	// discovery, not a deliverable, and it must be drafted whether the attempt
	// is about to pass, fail, or ask for a person — the BLOCKED answer is
	// exactly the report that carries a proposal. The invalid-block refusal is
	// the only outcome here, and it fails CLOSED: a findings block nobody could
	// read is not a report with no findings.
	if err := r.fileFindings(ctx, marker, collected); err != nil {
		r.disposeRejected(handle, executor, marker, "attempt "+fmt.Sprint(marker.Attempt)+" of "+marker.TickID+
			" reported a findings block that could not be read")
		return nil, err
	}

	// Appendix A #10's reporting half: a boundary that refuses silently tells
	// nobody the model tried. The executor enforced it; the reconciler says so
	// where a person reading the run will find it, and refuses the merge.
	verdict := collected.Verdict
	if len(collected.BoundaryViolations) > 0 {
		if r.guarded(guardSubstrateEnforcesBoundary) {
			r.setTick(marker.TickID, "rejected")
			r.record(marker.TickID, StageRejected, "boundary violation: %s", strings.Join(collected.BoundaryViolations, ", "))
			r.disposeRejected(handle, executor, marker, "the attempt wrote under an authority that is not its own")
			return nil, r.refuse(RefusedBoundary, marker.TickID,
				"%s wrote under an authority that is not its own (%s): %s",
				r.attemptName(marker.TickID, marker.Attempt), strings.Join(collected.BoundaryViolations, ", "), collected.Message)
		}
		// The negative control: nothing is reported and nothing is refused, so
		// the attempt's tracker writes reach the integration branch unnoticed —
		// which is the bug the guard exists for.
		verdict = subprocess.VerdictReadyToMerge
	}
	if verdict != subprocess.VerdictReadyToMerge {
		if err := r.rejectDurably(marker, collected.Verdict, collected.Message); err != nil {
			return nil, err
		}
		r.record(marker.TickID, StageRejected, "%s: %s", collected.Verdict, collected.Message)
		r.disposeRejected(handle, executor, marker, "attempt "+fmt.Sprint(marker.Attempt)+" of "+marker.TickID+
			" is "+collected.Verdict)
		return nil, r.refuse(RefusedCollect, marker.TickID, "%s is %s: %s",
			r.attemptName(marker.TickID, marker.Attempt), collected.Verdict, collected.Message)
	}

	// The branch would merge. What the worker SAID is the other half of the
	// answer, and until repair G nothing read it: `ready-to-merge` says the
	// attempt left commits and a report, not that the report agreed.
	//
	// The seam is here and not in the executor's classify on purpose. The
	// collect vocabulary is four words shared by three implementations
	// (contracts/collect-vocabulary.json), and a fifth invented for this would
	// have to be invented identically in all three or they would disagree about
	// the same tick with nothing failing. The executor is already honest about
	// what the report says — it parses the STATUS line and puts it, with
	// needs_human, in the role-result envelope — and the decision of what an
	// escalation MEANS for a merge is the reconciler's, exactly as it is for a
	// role job (collectRole, same two statuses, same refusal to close).
	//
	// It runs AFTER the verdict check rather than before it because an attempt
	// that is `no-commits` or `missing-result` is already refused and already
	// torn down, and the verdict is the more specific thing to tell a person
	// about it — `blocked-first`, the fixture that answers BLOCKED with nothing
	// committed, keeps reading as `no-commits`, which is what it is.
	if answer := collected.Result.RoleResult; answer != nil && needsHuman(answer.Status) {
		if err := r.rejectDurably(marker, collected.Verdict, answer.Status+": "+answer.Summary); err != nil {
			return nil, err
		}
		r.record(marker.TickID, StageRejected, "the worker answered %s: %s", answer.Status, answer.Summary)
		r.disposeRejected(handle, executor, marker, "attempt "+fmt.Sprint(marker.Attempt)+" of "+marker.TickID+
			" answered "+answer.Status)
		return nil, r.refuse(RefusedNeedsHuman, marker.TickID,
			"%s answered %s: %s. Its work is on %s and is NOT merged and the tick is NOT closed: a "+
				"worker that asks for a person is not answered by merging what it wrote and closing the tick behind it",
			r.attemptName(marker.TickID, marker.Attempt), answer.Status, answer.Summary, branchOf(marker.WriteRef))
	}
	_ = status
	return collected, nil
}

// needsHuman is the escalation set, read from the STATUS the worker wrote: the
// two answers that reach a person whatever the verdict says about the branch.
// It is subprocess.Report.NeedsHuman's rule applied to the status the executor
// already carried into the role-result envelope, which is the value that
// survives into the run's durable records.
func needsHuman(status string) bool {
	return status == subprocess.StatusBlocked || status == subprocess.StatusNeedsContext
}

// --------------------------------------------------------- the clean-up ---

// cleanUp is the last thing that happens to an attempt, and it happens only
// after the close.
//
// The order inside it is Appendix A #1's: the credential dies first, and only
// then is the attempt torn down. A container torn down before its credential is
// revoked can spend on the way out.
func (r *Reconciler) cleanUp(handle *subprocess.JobHandle, executor Executor, marker attemptHandle) {
	reason := fmt.Sprintf("%s is merged into %s and the tick is closed",
		r.attemptName(marker.TickID, marker.Attempt), r.branch)
	if handle == nil || executor == nil {
		// An attempt finished from the integration branch without being
		// addressed (it was already integrated): whatever this host still
		// holds of it is torn down from its state, and a host holding none
		// has nothing to tear down.
		r.tearDownSettled(marker, reason, false)
		return
	}
	r.tearDown(handle, executor, marker, reason, false)
}

// disposeRejected is cleanUp for an attempt that will never be merged.
//
// Nothing used to dispose of a rejection at all — Dispose was reached only
// after a close — so every refused attempt left its worktree and its branch in
// the operator's checkout forever, and the next attempt of the same tick found
// them there. That is the third thing this repair is about, and it is why the
// teardown happens HERE, in the two places a collect refuses, rather than at
// the end of a run that a refusal stops before it gets there.
//
// What it must not do is take the work with it. The refusal is what a person
// reads next, and a branch is where they read it from, so a branch that
// carries commits beyond its base is KEPT and only the worktree goes. Nothing
// about "the attempt failed" makes its commits disposable.
func (r *Reconciler) disposeRejected(handle *subprocess.JobHandle, executor Executor, marker attemptHandle, reason string) {
	if handle == nil || executor == nil {
		return
	}
	r.tearDown(handle, executor, marker, reason, r.attemptCarriesWork(marker))
}

// disposeRefused is disposeRejected for the refusals raised AFTER the collect.
//
// The three the collect itself raises tore their attempt down; the others —
// the merge's four, the gate's, the freshness check's, and a role job's four —
// ran no teardown at all, so a refused attempt left a LIVE credential file, a
// registered worktree and a branch in the operator's checkout, one set per
// refused attempt of every refused tick. The rule is the rejected one's,
// because it is the same rule: revoke first, remove the worktree, and KEEP a
// branch that carries commits — the refusal is what a person reads next and
// the branch is where they read it from.
//
// Only a REFUSAL tears anything down. An operational error — an unreachable
// remote, a tracker that would not answer — is not a verdict on the attempt,
// and a run that disposed of an attempt over one would be throwing work away
// because a network was down.
func (r *Reconciler) disposeRefused(handle *subprocess.JobHandle, executor Executor, marker attemptHandle, err error) {
	var refusal *Refusal
	if !asRefusal(err, &refusal) {
		return
	}
	r.disposeRejected(handle, executor, marker, fmt.Sprintf("%s was refused (%s): %s",
		r.attemptName(marker.TickID, marker.Attempt), refusal.Reason, firstLine(refusal.Message)))
}

// tearDownSettled tears an attempt down without addressing it first.
//
// It is for the one attempt nothing else will ever come back for: one this run
// rejected, whose commits mean it is neither collected again nor dispatched
// over. The handle is rebuilt from the executor state this host holds, the way
// a settlement rebuilds it, and no inspect is asked — there is nothing left to
// ask about. Everything it does is idempotent, so a run that reaches this on
// every resume revokes an already-dead credential and prunes an already-removed
// worktree, and for a rejection the BRANCH is kept: those commits are the only
// copy. The one other caller is cleanUp for an attempt finished from the
// integration branch without being addressed, whose tick is closed and whose
// commits the branch it was merged into already carries — that one passes
// keepBranch false, as every close's cleanup does.
//
// A host that holds no state for the attempt has nothing to tear down, which is
// what a restart on another machine looks like.
func (r *Reconciler) tearDownSettled(marker attemptHandle, reason string, keepBranch bool) {
	state, found := findAttemptState(marker.StateRoot)
	if !found {
		return
	}
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		r.record(marker.TickID, StageCleanedUp, "the executor for the rejected attempt could not be built: %v", err)
		return
	}
	executor, _, err := r.opts.NewExecutor(dispatch)
	if err != nil {
		r.record(marker.TickID, StageCleanedUp, "the executor for the rejected attempt could not be built: %v", err)
		return
	}
	r.tearDown(&subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         marker.JobID,
		Attempt:       marker.Attempt,
		Executor:      marker.Executor,
		Handle:        map[string]any{"state": state},
	}, executor, marker, reason, keepBranch)
}

// attemptCarriesWork asks the same question disposition asks of origin, of the
// LOCAL branch: is there a commit on it that the base it was cut from does not
// already have?
func (r *Reconciler) attemptCarriesWork(marker attemptHandle) bool {
	return r.attemptWorkHead(marker) != ""
}

// attemptWorkHead is the local branch's head when it carries a commit beyond
// the attempt's base, and "" when it carries none or there is no such branch in
// this checkout.
func (r *Reconciler) attemptWorkHead(marker attemptHandle) string {
	branch := branchOf(marker.WriteRef)
	head, err := r.git.resolve(branch)
	if err != nil || head == "" {
		return ""
	}
	if marker.BaseSHA == "" {
		return head
	}
	if head == marker.BaseSHA || r.git.contains(head, marker.BaseSHA) {
		return ""
	}
	return head
}

// tearDown is Appendix A #1's order, and disposal's safety, in one place.
//
// The credential dies first and the attempt second: a container torn down
// before its credential is revoked can spend on the way out. Then the executor
// refuses to delete a branch whose head no remote has, and that refusal is
// HONOURED rather than argued with — the reconciler retries keeping the
// branch, so the commits stay and the worktree still goes. A teardown that
// answered "delete it anyway" would be the reconciler taking back the one
// safety the executor has against a run that thought it was finished.
func (r *Reconciler) tearDown(handle *subprocess.JobHandle, executor Executor, marker attemptHandle,
	reason string, keepBranch bool) {

	if _, err := executor.Cancel(handle); err != nil {
		r.record(marker.TickID, StageCleanedUp, "the attempt's credential could not be revoked: %v", err)
		return
	}
	err := executor.Dispose(handle, subprocess.DisposeOptions{Reason: reason, KeepBranch: keepBranch})
	if err != nil && !keepBranch && isBranchUnsafe(err) {
		// The retry is a teardown of its own, so A1's order is kept for it
		// too: revoke, then tear down. Cancel is idempotent — the credential
		// is already gone and saying so again costs a record that is already
		// there — and the alternative is a dispose with no revoke in front of
		// it, which is the shape the invariant exists to refuse.
		if _, err := executor.Cancel(handle); err != nil {
			r.record(marker.TickID, StageCleanedUp, "the attempt's credential could not be revoked: %v", err)
			return
		}
		if retry := executor.Dispose(handle, subprocess.DisposeOptions{Reason: reason, KeepBranch: true}); retry != nil {
			r.record(marker.TickID, StageCleanedUp, "the attempt was not disposed: %v", retry)
			return
		}
		r.record(marker.TickID, StageCleanedUp,
			"%s; the branch is kept because it holds commits %s does not have", reason, r.opts.Remote)
		return
	}
	if err != nil {
		r.record(marker.TickID, StageCleanedUp, "the attempt was not disposed: %v", err)
		return
	}
	if keepBranch {
		// The branch is kept for the commits on it, and the record says WHERE
		// those commits are (ticfac tick 55i): on the remote, or — when the
		// push never landed — only in this checkout, said plainly, because
		// the worktree this teardown removes is not a place a person can be
		// sent to look and a branch nobody can place is work nobody can find.
		branch := branchOf(marker.WriteRef)
		if local := r.attemptWorkHead(marker); local != "" {
			if remote, err := r.git.remoteHead(branch); err == nil && remote == local {
				r.record(marker.TickID, StageCleanedUp,
					"%s; the worktree is gone and the branch is kept, its commits durable on %s", reason, r.opts.Remote)
				return
			}
			r.record(marker.TickID, StageCleanedUp,
				"%s; the worktree is gone and the branch is kept — its commits are only on the local branch %s in "+
					"this checkout, NOT on %s", reason, branch, r.opts.Remote)
			return
		}
		r.record(marker.TickID, StageCleanedUp, "%s; the worktree is gone and the branch is kept for the commits on it", reason)
		return
	}
	r.record(marker.TickID, StageCleanedUp, "%s", reason)
}

func isBranchUnsafe(err error) bool {
	refusal, ok := subprocess.AsRefusal(err)
	return ok && refusal.Reason == subprocess.RefusedBranchUnsafe
}

// DefaultExecutor is the local subprocess executor's factory: one executor per
// dispatch, pointed at a state directory this run owns. A production run whose
// profiles name OTHER executors composes this into a factory that routes on
// the profile's executor field — the routing lives with the host that can
// build the executors (internal/cli), not here, because a name the reconciler
// cannot spell is a name the reconciler must not spell.
//
// The substrate it reports is the zero Substrate: a local process states no
// protocol and no server version, and its dispatch records say exactly that in
// provenance.
//
// Three of the profile's four fields reach the executor here, as HOST
// configuration: the runner it launches, the model it launches it on, and the
// role prompt the worker prompt opens with. None of them is a JobSpec field —
// the protocol's records are closed, and a field invented on this side would be
// one the reconciler's own contract does not have. `runner` is the fallback an
// operator names on the command line, for a dispatch whose profile resolved
// none.
func DefaultExecutor(runner string, runnerArgv []string, pushInterval time.Duration) func(Dispatch) (Executor, Substrate, error) {
	return func(d Dispatch) (Executor, Substrate, error) {
		supervisor, err := supervisorArgv()
		if err != nil {
			return nil, Substrate{}, err
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
		executor, err := subprocess.New(subprocess.Options{
			Repo:           d.Repo,
			StateDir:       d.StateDir,
			Runner:         dispatched,
			Model:          model,
			RolePrompt:     rolePrompt,
			RunnerArgv:     runnerArgv,
			SupervisorArgv: supervisor,
			Remote:         d.Remote,
			Attempt:        d.Attempt,
			Try:            d.Try,
			PushInterval:   pushInterval,
			PriorReports:   d.PriorReports,
			PriorSnapshots: d.PriorSnapshots,
		})
		if err != nil {
			return nil, Substrate{}, err
		}
		return executor, Substrate{}, nil
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
