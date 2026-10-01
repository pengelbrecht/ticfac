package reconcile

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/sandboximage"
)

// Taking over a dead run's claim, and its work (the hn6 cloud-run stall).
//
// Epic hn6's second cloud run died holding r5i's claim: the factory recorded
// it failed and its Workflow complete, while its checkpoint on the
// integration branch still read "dispatching r5i as attempt 3". `ticfac run
// hn6 --cloud` resumed the epic as a new submission — a new run id, a fresh
// run-state store — and the new run held on foreign_claim, resumed itself,
// held again over an unchanged tree and halted: nothing about a dead run's
// records ever changes, so a hold on it has no event left to wait for. And
// had it dispatched, it would have started r5i from scratch while the dead
// run's attempt 1 sat on origin with two commits and a green gate.
//
// So a claim whose holder is known to have ended (claim.go: its checkpoint
// reads terminal, or its host — the factory, the process table — says it
// ended) is TAKEN OVER, and the takeover is a continuation, not a restart:
//
//   - the holder's newest attempt of the tick that left committed work
//     nothing has merged is CARRIED, exactly as a carried release is (#110,
//     tick 0z0): the new attempt is cut from its head, its marker's
//     resumed_from names the holder run, attempt, ref and commit, and the
//     gate still decides what merges;
//   - the new attempt's marker records the takeover itself (taken_over: the
//     holder and the evidence it was read dead on), whether or not there was
//     work to carry, and the feed says so (claim_taken_over).
//
// Where a holder's work is: its attempt's write ref, on origin or in this
// checkout — and, for a cloudflare-sandbox attempt, the landing branch its
// container pushed, `tick/<epic>/attempt-<n>/<tick>`, or the per-run
// `…-<run id>` the container falls back to when another run's attempt already
// holds that name (image/worker.sh adopt_worker_branch, PR #129). A branch
// that does not descend from the attempt's base is another run's work and is
// never carried.

// takenOver is the takeover a dispatch was made under: the run whose claim it
// took, and the evidence that run was read as ended on. It rides the marker's
// open handle, null when the dispatch took nothing over.
type takenOver struct {
	RunID    string `json:"run_id"`
	Evidence string `json:"evidence"`
}

// StageClaimTakenOver is the line a takeover leaves: the tick, the holder
// whose claim was taken, the evidence it was read as ended on, and whether
// its work is carried.
const StageClaimTakenOver = "claim_taken_over"

// cloudflareSandboxExecutor is the executor whose attempts land on the worker
// container's own branch rather than on the attempt's write ref.
const cloudflareSandboxExecutor = "cloudflare-sandbox"

// takeOverClaim answers the takeover of entry's stale foreign claim: the
// takeover record, and the holder's committed work to carry (nil when it left
// none nothing has merged). Nil, nil when the entry is not a stale foreign
// claim.
//
// When the holder's newest attempt with work is ALREADY ON the integration
// branch (hn6 run_ee8e's 378: run_6d88 merged it as d2f01b18 and died before
// the integrated gate and the close), the work is not carried and the tick is
// not started fresh: the answer is that attempt's delivery as a collectedFrom
// — integrated — and the dispatch finishes the tick from the integration
// branch, gated on the epic head and closed, with no worker and no collect.
func (r *Reconciler) takeOverClaim(entry planEntry) (*carriedWork, *takenOver, *collectedFrom, error) {
	holder := entry.ClaimHolder
	if !entry.StaleClaim || holder == "" || holder == r.runID {
		return nil, nil, nil, nil
	}
	taken := &takenOver{RunID: holder, Evidence: entry.ClaimEvidence}
	tick := entry.TickID

	// The dead run's untriaged findings come with its claim: it will never
	// reach the close-out that decides them, and this run will.
	if err := r.adoptFindings(tick, holder, entry.ClaimEvidence); err != nil {
		return nil, nil, nil, err
	}

	attempts, err := r.store.ForeignAttempts()
	if err != nil {
		r.record(tick, StageClaimTakenOver,
			"%s's claim is taken over from run %s, which ended (%s); its attempts could not be read (%v), so the "+
				"next try starts fresh", tick, holder, entry.ClaimEvidence, err)
		return nil, taken, nil, nil
	}
	var theirs []runstate.Attempt
	for _, attempt := range attempts {
		if attempt.TickID == tick && attempt.Provenance.RunID == holder {
			theirs = append(theirs, attempt)
		}
	}
	// Newest first: the latest attempt that left work is the one whose work
	// is furthest along — a later attempt of the holder's exists only because
	// it rejected or lost an earlier one.
	sort.Slice(theirs, func(i, j int) bool { return theirs[i].Attempt > theirs[j].Attempt })
	for _, attempt := range theirs {
		marker := handleFromMap(attempt.JobHandle)
		if marker.TickID != tick {
			continue
		}
		ref, head := r.foreignAttemptWork(marker, holder)
		if head == "" {
			continue
		}
		if delivered := r.foreignWorkIntegrated(marker, head); delivered != "" && !isRoleJob(entry.Role) {
			r.record(tick, StageClaimTakenOver,
				"%s's claim is taken over from run %s, which ended (%s): the run does not hold on a claim nobody is "+
					"behind. Its attempt %d's work (%s on %s) is ALREADY on %s at %s, so it is neither carried nor "+
					"redone: the tick is finished from the integration branch — gated on the epic head and closed",
				tick, holder, entry.ClaimEvidence, marker.Attempt, short(head), branchOf(ref), r.branch,
				short(delivered))
			return nil, taken, &collectedFrom{RunID: holder, JobID: marker.JobID, Attempt: marker.Attempt,
				WriteRef: ref, SHA: delivered,
				Evidence: fmt.Sprintf("its work is already on %s at %s", r.branch, short(delivered))}, nil
		} else if delivered != "" {
			continue
		}
		carried := marker
		carried.WriteRef = ref
		by := fmt.Sprintf("%s (took over the claim of run %s, which ended: %s)", runReleaser, holder, entry.ClaimEvidence)
		r.record(tick, StageClaimTakenOver,
			"%s's claim is taken over from run %s, which ended (%s): the run does not hold on a claim nobody is "+
				"behind. Its attempt %d left work nothing merged (%s on %s), so the next try starts from it rather "+
				"than redoing it; the gate still decides what merges",
			tick, holder, entry.ClaimEvidence, marker.Attempt, short(head), branchOf(ref))
		return &carriedWork{marker: carried, by: by, at: r.now().UTC().Format(time.RFC3339), runID: holder}, taken, nil, nil
	}
	r.record(tick, StageClaimTakenOver,
		"%s's claim is taken over from run %s, which ended (%s): the run does not hold on a claim nobody is "+
			"behind. None of its %d attempt(s) of %s left work nothing merged, so the next try starts fresh",
		tick, holder, entry.ClaimEvidence, len(theirs), tick)
	return nil, taken, nil, nil
}

// foreignWorkIntegrated is the commit on the integration branch that delivers
// another run's attempt whose work is head, or "" when the integration branch
// does not carry that work.
//
// Two shapes count. The plain one: the integration branch carries head
// itself. And the hn6 one (run_ee8e's 378): the holder's attempt was CARRIED,
// its cloud worker added nothing but its own report on top of the carried
// work, and the holder's collect delivered the carried head (tick isp) and
// merged THAT — so the landing branch's head, report commit and all, is not on
// the integration branch, although every line of work it carries is. That
// attempt is integrated when the holder's own delivery is (integratedHead,
// the question a resume of the holder would ask) and the only commits head
// carries beyond the integration branch are report-only: no merge, and
// nothing touched but the attempt's own RESULT file. Anything else — a single
// line of work the integration branch lacks — is work to carry, as before.
func (r *Reconciler) foreignWorkIntegrated(marker attemptHandle, head string) string {
	integrated, err := r.integratedOn(head)
	if err != nil {
		return ""
	}
	if integrated {
		return head
	}
	delivered, err := r.integratedHead(marker)
	if err != nil || delivered == "" || delivered == head {
		return ""
	}
	if !r.onlyReportBeyondIntegration(head, marker.TickID) {
		return ""
	}
	return delivered
}

// onlyReportBeyondIntegration says every commit head carries beyond the
// integration branch on origin is a report-only commit: not a merge, and
// touching nothing but the tick's RESULT file. False when there is no such
// commit, or the range cannot be read.
func (r *Reconciler) onlyReportBeyondIntegration(head, tick string) bool {
	epicHead, err := r.git.remoteHead(r.branch)
	if err != nil || epicHead == "" {
		return false
	}
	if err := r.git.fetch(r.branch); err != nil {
		return false
	}
	out, err := r.git.run("", "rev-list", "--parents", epicHead+".."+head)
	if err != nil || strings.TrimSpace(out) == "" {
		return false
	}
	report := sandboximage.WorkerResultFile(tick)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false // a merge (or a root): not a report commit
		}
		files, err := r.git.run("", "diff-tree", "--no-commit-id", "--name-only", "-r", "--no-renames", fields[0])
		if err != nil {
			return false
		}
		for _, file := range strings.Fields(files) {
			if file != report {
				return false
			}
		}
	}
	return true
}

// StageFindingAdopted is the line an adopted finding leaves: a finding a run
// that ended left untriaged, taken into this run's own drafts with the claim
// this run took over from it, for this run's close-out to decide.
const StageFindingAdopted = "finding_adopted"

// adoptFindings takes a dead run's untriaged findings into this run's own
// drafts (the hn6 follow-up). A run that died never reaches its close-out, so
// the drafts it left PROPOSED would sit under a run nothing will ever finish
// — neither decided by the run's absorption rules nor held before a person.
// Adopted, they are this run's: decided before its close-out
// (decideUndecidedFindings) — absorbed, backlogged, routed — by exactly the
// rules a finding of its own is, and held for a person by its close-out when
// those leave one standing.
//
// Each draft keeps its discovery (the job that found it, the tick and
// attempt, when it was proposed); only the provenance's run becomes this
// one, as the store requires of anything in its directory. A decision the
// dead run already RECORDED for a draft (an absorption or routing record it
// wrote before it died, leaving the tick uncreated or the triage
// unfinished) is adopted with it, so the close-out finishes behind that
// decision rather than making it again. A key this run already has a draft
// for is left alone: whatever this run knows of it — a triage included —
// stands over a copy. Decided drafts are not adopted: their decision already
// lives in the tracker.
func (r *Reconciler) adoptFindings(tick, holder, evidence string) error {
	if r.adoptedFindingsOf == nil {
		r.adoptedFindingsOf = map[string]bool{}
	}
	if r.adoptedFindingsOf[holder] {
		return nil
	}
	theirs, err := r.store.ForeignFindings(holder)
	if err != nil {
		return fmt.Errorf("reconcile: read the findings of run %s, whose claim on %s is taken over: %w", holder, tick, err)
	}
	for _, finding := range theirs {
		if finding.Status != runstate.FindingProposed {
			continue
		}
		if _, ok, err := r.store.Finding(finding.Key); err != nil {
			return fmt.Errorf("reconcile: read this run's draft of finding %s: %w", finding.Key, err)
		} else if ok {
			continue
		}
		if decision, ok, err := r.store.ForeignAbsorption(holder, finding.Key); err != nil {
			return fmt.Errorf("reconcile: read run %s's decision on finding %s: %w", holder, finding.Key, err)
		} else if ok {
			decision.Provenance.RunID = r.runID
			if _, err := r.store.PutAbsorption(*decision); err != nil {
				return fmt.Errorf("reconcile: adopt run %s's decision on finding %s: %w", holder, finding.Key, err)
			}
		}
		adopted := finding
		adopted.Provenance.RunID = r.runID
		if _, err := r.store.PutFinding(adopted); err != nil {
			return fmt.Errorf("reconcile: adopt finding %s of run %s: %w", finding.Key, holder, err)
		}
		r.record(finding.TickID, StageFindingAdopted,
			"finding %s (%q, discovered by %s) was left untriaged by run %s, which ended (%s); it is adopted with "+
				"the claim on %s this run took over, and this run's close-out decides it as its own",
			finding.Key, finding.Title, finding.DiscoveredFrom, holder, evidence, tick)
	}
	r.adoptedFindingsOf[holder] = true
	return nil
}

// foreignAttemptWork is where another run's attempt left its committed work:
// the ref and its head, or "" when it left none beyond its base. The
// attempt's own write ref is read first — origin, then this checkout, as for
// any rejected attempt — and a cloudflare-sandbox attempt's landing branches
// after it, the per-run fallback before the shared name.
func (r *Reconciler) foreignAttemptWork(marker attemptHandle, holder string) (string, string) {
	if head := r.rejectedWorkHead(marker); head != "" {
		return marker.WriteRef, head
	}
	if marker.Executor != cloudflareSandboxExecutor || marker.BaseSHA == "" {
		return "", ""
	}
	landing := sandboximage.WorkerBranch(fmt.Sprintf("%s/attempt-%d", r.opts.EpicID, marker.Attempt), marker.TickID)
	for _, branch := range []string{landing + "-" + holder, landing} {
		head, err := r.remoteWork(branch, marker.BaseSHA)
		if err != nil || head == "" {
			continue
		}
		// Cut from this attempt's base, or it is another run's attempt that
		// happened to land on the shared name.
		if !r.git.contains(marker.BaseSHA, head) {
			continue
		}
		return refFor(branch), head
	}
	return "", ""
}

// carriedFrom reads the marker a carry names, from the store of the run it
// belongs to: the carry's own RunID when it names one (a claim taken over from
// a run that ended), else run — the run whose marker named the carry, since a
// marker's resumed_from without a run id is about an attempt of its own run.
// The owner it answers is the run the NEXT link of the chain is read against.
func (r *Reconciler) carriedFrom(run string, from *resumedFrom) (*runstate.Attempt, string, bool, error) {
	owner := run
	if from.RunID != "" {
		owner = from.RunID
	}
	if owner == "" || owner == r.runID {
		record, ok, err := r.store.Attempt(from.Attempt)
		return record, r.runID, ok, err
	}
	attempts, err := r.store.ForeignAttempts()
	if err != nil {
		return nil, owner, false, err
	}
	for i := range attempts {
		if attempts[i].Provenance.RunID == owner && attempts[i].Attempt == from.Attempt {
			return &attempts[i], owner, true, nil
		}
	}
	return nil, owner, false, nil
}

// carryKey names one link of a carry chain across runs, for cycle detection:
// two runs' attempt numbers both begin at 1.
func carryKey(run string, from *resumedFrom) string {
	if from.RunID != "" {
		run = from.RunID
	}
	return fmt.Sprintf("%s#%d", run, from.Attempt)
}

// Collecting a dead run's FINISHED attempt (hn6's ltg).
//
// A claim taken over from a run that ended carries the holder's committed
// work into a fresh worker. That is right for work the holder's worker was
// cut off in the middle of, and wasteful — and a gamble on a worker that may
// undo it — for work its worker FINISHED: hn6's run_911b dispatched ltg as
// attempt 4, the worker settled succeeded with its report on
// `tick/hn6/attempt-4/ltg`, and the run died before it collected it. The
// next run took the claim over and dispatched ltg again, "starting from"
// work that only needed a verdict.
//
// So when the holder's attempt with the work SETTLED SUCCEEDED, as the host
// that ran its worker recorded it — its own attempt record on this host (a
// local worker, read through the executor), or the factory's settlement
// record of its container (a cloud worker, Options.SettledAttempt) — this
// run's next attempt of the tick starts no worker. It is cut at the holder
// attempt's own base, and its handle addresses the settled work: the collect
// rules on it exactly as it rules on any attempt — the report linter, the
// boundary, the base check, the gate — and only a rejection there dispatches
// a worker, through the ordinary rejection paths. Nothing is ever guessed
// from the branch alone: a holder whose settlement nobody recorded is
// carried as before.

// collectedFrom is the settled attempt of another run an attempt rules on
// instead of running a worker: the run, its attempt and job, the ref its work
// is on, the commit it had there when this run took it, and the evidence it
// settled succeeded. It rides the marker (collected_from), so a restart
// re-addresses the same work rather than starting a worker.
type collectedFrom struct {
	RunID    string `json:"run_id"`
	JobID    string `json:"job_id"`
	Attempt  int    `json:"attempt"`
	WriteRef string `json:"write_ref"`
	SHA      string `json:"sha"`
	Evidence string `json:"evidence"`
}

// StageSettledWorkCollected is the line an attempt that rules on another
// run's settled work leaves in place of a dispatch: whose work, where, and the
// evidence it had settled succeeded.
const StageSettledWorkCollected = "settled_work_collected"

// foreignStateRoot is where a run that ran on THIS host kept one attempt's
// executor state: execStateDir under that run's id.
func (r *Reconciler) foreignStateRoot(runID, tickID string, attempt int) string {
	if r.opts.ExecStateRoot == "" || runID == "" {
		return ""
	}
	return filepath.Join(r.opts.ExecStateRoot, runID, tickID, fmt.Sprintf("%d", attempt))
}

// foreignLocalHandle addresses another run's attempt through the executor
// state it left on this host, or nil when it left none here.
func (r *Reconciler) foreignLocalHandle(executor, runID, jobID, tickID string, attempt int) *subprocess.JobHandle {
	state, found := findAttemptState(r.foreignStateRoot(runID, tickID, attempt))
	if !found {
		return nil
	}
	return &subprocess.JobHandle{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         jobID,
		Attempt:       attempt,
		Executor:      executor,
		Handle:        map[string]any{"state": state},
	}
}

// settledElsewhere answers whether the work a takeover carries is a FINISHED
// attempt this run's dispatch can rule on without a worker: the collectedFrom
// to put on the marker, or nil — carry it into a worker, as before. It is
// asked with the executor this dispatch was built with, because that is the
// executor that will address the work, and it never guesses: an attempt of
// another executor, or one no host recorded settled succeeded, is nil.
func (r *Reconciler) settledElsewhere(ctx context.Context, executor Executor, dispatch Dispatch, carry *carriedWork) *collectedFrom {
	if carry == nil || carry.runID == "" || isRoleJob(dispatch.Role) {
		return nil
	}
	foreign := carry.marker
	if foreign.Executor == "" || foreign.Executor != dispatch.Executor || foreign.BaseSHA == "" {
		return nil
	}
	head, err := r.carryHead(foreign)
	if err != nil || head == "" {
		return nil
	}
	from := &collectedFrom{RunID: carry.runID, JobID: foreign.JobID, Attempt: foreign.Attempt,
		WriteRef: foreign.WriteRef, SHA: head}

	// The holder ran its worker on this host: its own record says how it
	// settled. A record that reads anything but a clean finish is not
	// evidence of one; a record this executor cannot read is asked of the
	// factory below.
	if handle := r.foreignLocalHandle(foreign.Executor, carry.runID, foreign.JobID, foreign.TickID, foreign.Attempt); handle != nil {
		status, err := executor.Inspect(handle, "")
		if err == nil && status.Terminal {
			if status.State != subprocess.StateSucceeded {
				return nil
			}
			from.Evidence = fmt.Sprintf("its worker's own record on this host reads %s", status.State)
			return from
		}
	}

	// The holder's worker ran in the factory: the factory recorded how its
	// container settled, and this executor can hand the settled work to a
	// collect without asking a door that answers only for its own run.
	if _, ok := executor.(SettledElsewhereAdopter); !ok || r.opts.SettledAttempt == nil {
		return nil
	}
	answer := r.opts.SettledAttempt(ctx, carry.runID, foreign.TickID, foreign.Attempt)
	if !answer.Known || !answer.Succeeded {
		return nil
	}
	from.Evidence = answer.Evidence
	return from
}

// settledHandle addresses the settled work an attempt rules on (its marker's
// collected_from): the holder's own state on this host when it ran here, else
// the executor's adoption of it from its branch. It starts nothing.
func (r *Reconciler) settledHandle(executor Executor, dispatch Dispatch, marker attemptHandle) (*subprocess.JobHandle, error) {
	from := marker.CollectedFrom
	// The holder's own state on this host addresses its work only when this
	// executor can read it: a cloud executor's door answers for its own run
	// alone, so a dead local orchestrator's cloud record is adopted below.
	if handle := r.foreignLocalHandle(marker.Executor, from.RunID, from.JobID, marker.TickID, from.Attempt); handle != nil {
		if status, err := executor.Inspect(handle, ""); err == nil && status.Terminal {
			return handle, nil
		}
	}
	adopter, ok := executor.(SettledElsewhereAdopter)
	if !ok {
		return nil, fmt.Errorf("%s rules on run %s's settled attempt %d, and neither does this host hold that "+
			"attempt's state nor can the %s executor address it from its branch",
			r.attemptName(marker.TickID, marker.Attempt), from.RunID, from.Attempt, marker.Executor)
	}
	return adopter.AdoptSettledElsewhere(r.jobSpec(dispatch), from.RunID, from.JobID, from.Attempt,
		branchOf(from.WriteRef), from.Evidence)
}
