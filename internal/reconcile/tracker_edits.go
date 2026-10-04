package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// The tracker-edit delivery (hn6 run_d51a, tick yjq).
//
// A worker never writes the tracker — `.tick/` is a protected prefix and tk
// is not a worker's to run — because the tracker is the run's authority. That
// left one kind of tick no worker could ever finish: one whose DELIVERABLE is
// a tracker edit. yjq's was a one-field re-flow of epic hn6's
// acceptance_criteria; three attempts answered BLOCKED with the exact text in
// prose ("the run's tracker authority or the operator must apply it"), and
// the run redispatched it up the ladder into the same wall, to be held once
// the ladder was spent.
//
// The orchestrator owns tracker state, so it owns this path. The worker
// PROPOSES the edit in a typed `tracker-edits` block (subprocess's
// tracker_edits.go reads its shape, the report checker holds it to the
// checkout in-session), and the run:
//
//   - at COLLECT, holds every proposal to the tracker as it stands now
//     (trackerEditRefusal): the record exists, it is the attempt's own tick,
//     its epic or another tick of the epic, it is not closed, and an
//     acceptance edit drops no item the record marks. A proposal the run
//     refuses makes the report one it cannot act on — missing-result,
//     retried like any report nobody can read, never applied in part;
//   - takes a DONE answer whose only deliverable is the proposal as a
//     delivery, not an empty branch: the collect's no-commits becomes
//     ready-to-merge with no head;
//   - at INTEGRATE, applies the edits through its own durable tracker writer
//     (each one a commit of `.tick/` onto the integration branch, pushed) —
//     BEFORE the attempt's own commits merge, so a run killed between the two
//     re-collects and re-applies the same edits as no-ops — and gates and
//     closes the tick over the result like any other delivery;
//   - in TRIAGE, applies the exact change a finding carries as its
//     `tracker_edit` instead of absorbing a tick no worker could do, and
//     triages the draft FIXED with the commit that applied it.
//
// Every write is on the feed as tracker_edited; every refusal as
// tracker_edit_refused.

const (
	// StageTrackerEdited: the run applied a tracker edit a worker proposed.
	StageTrackerEdited = "tracker_edited"
	// StageTrackerEditRefused: the run refused a proposed tracker edit, and
	// why — it is never applied.
	StageTrackerEditRefused = "tracker_edit_refused"
)

// tickEditor is the tracker's half of a field edit: one prose field of one
// record replaced. A test tracker implements it; the tk client publishes no
// verb for it, so the durable wrapper rewrites the record the way it adopts
// and places (adoptTick, placeBlocker).
type tickEditor interface {
	EditTick(ctx context.Context, tickID, field, value string) (tk.Tick, error)
}

// EditTick replaces one prose field of one record durably: the write, then
// the commit of `.tick/` onto the integration branch and the push, in the
// same step every tracker write here takes.
func (d *durableTracker) EditTick(ctx context.Context, tickID, field, value string) (tk.Tick, error) {
	return d.write(tickID, "edit "+field+" of "+tickID, func() (tk.Tick, error) {
		if w, ok := d.inner.(tickEditor); ok {
			return w.EditTick(ctx, tickID, field, value)
		}
		return editTickRecord(d.tree, tickID, field, value, time.Now().UTC().Format(time.RFC3339))
	})
}

// editTickRecord rewrites one record with one prose field replaced, for the
// tracker that has no verb of its own for it.
func editTickRecord(tree *trackerTree, tickID, field, value, at string) (tk.Tick, error) {
	raw, err := os.ReadFile(trackerRecordPath(tree.dir, tickID))
	if err != nil {
		return tk.Tick{}, fmt.Errorf("read %s to edit its %s: %w", tickID, field, err)
	}
	var tick tk.Tick
	if err := json.Unmarshal(raw, &tick); err != nil {
		return tk.Tick{}, fmt.Errorf("the record of %s does not read back as a tick: %w", tickID, err)
	}
	switch field {
	case subprocess.TrackerFieldAcceptance:
		tick.AcceptanceCriteria = value
	case subprocess.TrackerFieldDescription:
		tick.Description = value
	default:
		return tk.Tick{}, fmt.Errorf("the tracker's record edit replaces %s or %s, not %q",
			subprocess.TrackerFieldAcceptance, subprocess.TrackerFieldDescription, field)
	}
	tick.UpdatedAt = at
	return tick, writeTrackerRecord(tree, tick)
}

// trackerEditRefusal holds one proposed edit to the tracker as it stands:
// "" when the run will apply it, else why it will not.
func (r *Reconciler) trackerEditRefusal(ctx context.Context, marker attemptHandle, edit subprocess.TrackerEdit) string {
	if err := edit.Validate(); err != nil {
		return err.Error()
	}
	current, err := r.tracker.Show(ctx, edit.Tick)
	if err != nil {
		return fmt.Sprintf("%s is not a record this tracker can read (%v)", edit.Tick, err)
	}
	if edit.Tick != marker.TickID && edit.Tick != r.opts.EpicID && current.Parent != r.opts.EpicID {
		return fmt.Sprintf("%s is outside epic %s: a worker proposes edits to its own tick (%s), its epic, or "+
			"another tick of the epic", edit.Tick, r.opts.EpicID, marker.TickID)
	}
	if current.Status == "closed" {
		return fmt.Sprintf("%s is closed: a closed record is history, never rewritten by a later attempt", edit.Tick)
	}
	if edit.Field == subprocess.TrackerFieldAcceptance {
		if missing := subprocess.DroppedAcceptanceItems(current.AcceptanceCriteria, edit.Value); len(missing) > 0 {
			return fmt.Sprintf("the new acceptance criteria of %s drop %s, which the record marks: an edit may "+
				"re-flow or add to a definition of done, never drop an item from it", edit.Tick, strings.Join(missing, ", "))
		}
	}
	return ""
}

// acceptTrackerEdits is the collect's half. An implement-tick attempt whose
// report proposes tracker edits has every proposal held to the tracker now;
// a DONE answer whose only deliverable is the proposal is a delivery —
// ready-to-merge with no head — rather than an empty branch, and a proposal
// the run refuses behind a DONE answer makes the report one it cannot act on
// (missing-result, retried).
//
// Since tick l89 a BLOCKED answer whose fix is the tracker write itself is
// that same delivery. A worker cannot write the tracker, so a blocked answer
// over a fix that is a record write is the boundary wall named — yjq's three
// attempts each said exactly that with the edit in hand, and the ladder
// answered every one by redispatching into the same wall. When the blocked
// answer carries the fix in the typed block, the run applies it through its
// own writer and closes the tick, instead of entering the ladder. The
// disposition keeps two edges: a BLOCKED answer that also committed work keeps
// the ladder (its question rides on work a person may still have to settle,
// not on an empty branch), and a question the standing orders reserve for a
// person is never answered by applying what the report happened to carry.
func (r *Reconciler) acceptTrackerEdits(ctx context.Context, marker attemptHandle, collected *subprocess.Collection) *subprocess.Collection {
	if collected == nil || collected.Result == nil || marker.Role != "implement-tick" ||
		len(collected.Report.TrackerEdits) == 0 {
		return collected
	}
	// What the proposal IS depends on the status: the delivery of a DONE
	// answer or its companion; a BLOCKED answer's named fix, applied by the
	// run itself when the answer committed nothing; never the companion of a
	// NEEDS_CONTEXT question, which the ladder answers.
	blocked := collected.Report.Status == subprocess.StatusBlocked
	switch {
	case collected.Report.Status == subprocess.StatusDone, collected.Report.Status == subprocess.StatusDoneWithConcerns:
	case blocked && collected.Verdict == subprocess.VerdictNoCommits:
		if class := r.standingOrders().alwaysAskClass(collected.Report.Detail); class != "" {
			r.record(marker.TickID, StageTrackerEditRefused, "%s answered %s naming %s, but the question is in the "+
				"always-ask class %q of the standing orders: the run does not apply it, and the question holds for a person",
				r.attemptName(marker.TickID, marker.Attempt), collected.Report.Status,
				trackerEditList(collected.Report.TrackerEdits), class)
			return collected
		}
	default:
		return collected
	}
	if collected.Verdict != subprocess.VerdictNoCommits && collected.Verdict != subprocess.VerdictReadyToMerge {
		return collected
	}
	name := r.attemptName(marker.TickID, marker.Attempt)
	for _, edit := range collected.Report.TrackerEdits {
		if why := r.trackerEditRefusal(ctx, marker, edit); why != "" {
			r.record(marker.TickID, StageTrackerEditRefused, "%s proposed an edit of the %s, which the run refuses "+
				"and does not apply: %s", name, edit, why)
			if blocked {
				// The blocked answer stands: its question is real and its
				// named fix is not one the tracker admits, so the ladder —
				// not a missing report — answers it.
				return collected
			}
			return trackerEditsRefused(collected, edit, why)
		}
	}
	if collected.Verdict == subprocess.VerdictReadyToMerge {
		// Work AND edits: the edits are applied before the work merges
		// (integrateTrackerEdits), and everything else is the collect's.
		return collected
	}

	result := *collected.Result
	result.Source.HeadSHA = nil
	result.Source.Commits = 0
	result.Outcome = subprocess.OutcomeSucceeded
	result.FailureClass = ""
	if collected.Result.RoleResult != nil {
		role := *collected.Result.RoleResult
		role.Summary = subprocess.RoleSummary(role.Role, collected.Report, subprocess.VerdictReadyToMerge)
		payload := make(map[string]any, len(role.Result))
		for k, v := range role.Result {
			payload[k] = v
		}
		if _, ok := payload["verdict"]; ok {
			payload["verdict"] = subprocess.VerdictReadyToMerge
		}
		role.Result = payload
		result.RoleResult = &role
	}
	delivered := *collected
	delivered.Result = &result
	delivered.Verdict = subprocess.VerdictReadyToMerge
	delivered.Message = ""
	if blocked {
		r.record(marker.TickID, StageCollected, "%s answered %s over a fix that is itself a tracker-record write and "+
			"committed no work: the run applies the %d proposed edit(s) (%s) itself, through its own tracker writer, "+
			"instead of dispatching the question up the blocked ladder, then gates and closes the tick over them",
			name, collected.Report.Status, len(collected.Report.TrackerEdits), trackerEditList(collected.Report.TrackerEdits))
	} else {
		r.record(marker.TickID, StageCollected, "%s committed no work and proposed %d tracker edit(s) (%s): that is its "+
			"delivery, not an empty branch — the run applies the edits through its own tracker writer, then gates and "+
			"closes the tick over them", name, len(collected.Report.TrackerEdits), trackerEditList(collected.Report.TrackerEdits))
	}
	return &delivered
}

// trackerEditsRefused is the collection of an attempt whose proposal the run
// refused: the closed vocabulary's missing-result — a report the run cannot
// act on, retried like any report nobody can read, its work (if any) carried.
func trackerEditsRefused(collected *subprocess.Collection, edit subprocess.TrackerEdit, why string) *subprocess.Collection {
	result := *collected.Result
	result.Outcome = subprocess.OutcomeFailed
	result.FailureClass = subprocess.FailureRunnerError
	refused := *collected
	refused.Result = &result
	refused.Verdict = subprocess.VerdictMissingResult
	refused.Message = fmt.Sprintf("the report proposes an edit of the %s the run refuses (it is retried like a "+
		"report nobody can act on, and nothing of it is applied): %s", edit, why)
	return &refused
}

func trackerEditList(edits []subprocess.TrackerEdit) string {
	names := make([]string, 0, len(edits))
	for _, edit := range edits {
		names = append(names, edit.String())
	}
	return strings.Join(names, "; ")
}

// trackerEditDelivery says the run has taken this attempt's proposed tracker
// edits as its delivery (acceptTrackerEdits): the collect reads ready-to-merge
// over a branch with no head of its own, and the proposals are on the report.
// That state is reachable only through the conversion — deliverCarriedWork
// names a head, and the executor's own no-commits never reads ready-to-merge —
// so it is the delivery's signature. A BLOCKED or NEEDS_CONTEXT answer beside
// such a delivery was answered by applying it: the collect's "what the worker
// SAID" check must not re-litigate it into the ladder the delivery exists to
// skip (tick l89).
func trackerEditDelivery(collected *subprocess.Collection) bool {
	return collected != nil && collected.Result != nil &&
		collected.Verdict == subprocess.VerdictReadyToMerge &&
		collected.Result.Source.HeadSHA == nil &&
		len(collected.Report.TrackerEdits) > 0
}

// integrateTrackerEdits is the integrate's half: the proposed edits applied
// through the run's own writer BEFORE the attempt's commits merge. delivered
// says the edits were the whole delivery — the attempt has no head to merge —
// and merged is then what the gate runs over: the integration branch with the
// edits on it.
func (r *Reconciler) integrateTrackerEdits(ctx context.Context, marker attemptHandle,
	collected *subprocess.Collection) (delivered bool, merged merge, err error) {

	if collected == nil || collected.Result == nil || marker.Role != "implement-tick" ||
		len(collected.Report.TrackerEdits) == 0 || collected.Verdict != subprocess.VerdictReadyToMerge {
		return false, merge{}, nil
	}
	head, err := r.applyTrackerEdits(ctx, marker, collected.Report.TrackerEdits,
		r.attemptName(marker.TickID, marker.Attempt))
	if err != nil {
		return false, merge{}, err
	}
	if collected.Result.Source.HeadSHA != nil {
		// The attempt's own commits merge next, onto the edited branch.
		return false, merge{}, nil
	}
	attemptHead, _ := r.git.remoteHead(branchOf(marker.WriteRef))
	r.setTick(marker.TickID, "integrated")
	r.record(marker.TickID, StageIntegrated, "%s's delivery is its tracker edits, applied onto %s at %s",
		r.attemptName(marker.TickID, marker.Attempt), r.branch, short(head))
	return true, merge{AttemptHead: attemptHead, EpicHead: head, GateSHA: head, Merged: false}, nil
}

// applyTrackerEdits applies proposals through the run's durable tracker
// writer, each held to the tracker once more as it stands at the write, and
// answers the integration branch's head with them on it. An edit already in
// place is a no-op — a resumed run re-applies what a killed one already did
// without writing anything twice.
func (r *Reconciler) applyTrackerEdits(ctx context.Context, marker attemptHandle, edits []subprocess.TrackerEdit,
	source string) (string, error) {

	for _, edit := range edits {
		if why := r.trackerEditRefusal(ctx, marker, edit); why != "" {
			r.record(marker.TickID, StageTrackerEditRefused, "%s proposed an edit of the %s, which the run refuses "+
				"and does not apply: %s", source, edit, why)
			return "", r.refuse(RefusedCollect, marker.TickID,
				"%s proposed an edit of the %s that the tracker no longer admits: %s. Nothing of it is applied",
				source, edit, why)
		}
		changed, err := r.applyTrackerEdit(ctx, edit)
		if err != nil {
			return "", fmt.Errorf("apply the edit of the %s that %s proposed: %w", edit, source, err)
		}
		if changed {
			r.record(marker.TickID, StageTrackerEdited, "the run applied the edit of the %s that %s proposed "+
				"(%d characters), through its own tracker writer onto %s", edit, source, len(edit.Value), r.branch)
		} else {
			r.record(marker.TickID, StageTrackerEdited, "the edit of the %s that %s proposed is already in place; "+
				"nothing is written twice", edit, source)
		}
	}
	return r.git.remoteHead(r.branch)
}

// applyTrackerEdit makes one write, or none when the record already says it.
func (r *Reconciler) applyTrackerEdit(ctx context.Context, edit subprocess.TrackerEdit) (bool, error) {
	current, err := r.tracker.Show(ctx, edit.Tick)
	if err != nil {
		return false, err
	}
	switch edit.Field {
	case subprocess.TrackerFieldNotes:
		if strings.Contains(current.Notes, strings.TrimSpace(edit.Value)) {
			return false, nil
		}
		_, err := r.tracker.Note(ctx, edit.Tick, edit.Value)
		return err == nil, err
	case subprocess.TrackerFieldAcceptance:
		if current.AcceptanceCriteria == edit.Value {
			return false, nil
		}
	case subprocess.TrackerFieldDescription:
		if current.Description == edit.Value {
			return false, nil
		}
	}
	editor, ok := r.tracker.(tickEditor)
	if !ok {
		return false, fmt.Errorf("the run's tracker cannot edit a record's %s", edit.Field)
	}
	_, err = editor.EditTick(ctx, edit.Tick, edit.Field, edit.Value)
	return err == nil, err
}

// decideOrApplyFinding is the triage route for a finding whose whole fix is
// a tracker edit it carries: the run applies the exact change and triages the
// draft FIXED with the commit that applied it, rather than absorbing a tick
// that no worker could ever do (yjq was such a tick). A finding without an
// edit, routed elsewhere, already decided, or carrying an edit the run
// refuses is decided the way every finding is.
func (r *Reconciler) decideOrApplyFinding(ctx context.Context, marker attemptHandle, key string, dispatch Dispatch,
	finding subprocess.Finding) (findingDecision, error) {

	if finding.TrackerEdit == nil || (finding.Target != "" && !r.isThisRepository(finding.Target)) {
		return r.decideFinding(ctx, marker, key, dispatch)
	}
	if _, err := r.store.Fetch(); err != nil {
		return findingDecision{}, err
	}
	standing, ok, err := r.store.Finding(key)
	if err != nil {
		return findingDecision{}, err
	}
	if !ok || standing.Status != runstate.FindingProposed {
		return r.decideFinding(ctx, marker, key, dispatch)
	}
	if _, recorded, err := r.store.Absorption(key); err != nil {
		return findingDecision{}, err
	} else if recorded {
		// An earlier incarnation decided it the other way and was killed:
		// the record is the decision, and it is finished behind.
		return r.decideFinding(ctx, marker, key, dispatch)
	}
	edit := *finding.TrackerEdit
	if why := r.trackerEditRefusal(ctx, marker, edit); why != "" {
		r.record(marker.TickID, StageTrackerEditRefused, "finding %s (%q) carries an edit of the %s the run refuses "+
			"(%s): it is not applied, and the finding is decided as any finding is", key, finding.Title, edit, why)
		return r.decideFinding(ctx, marker, key, dispatch)
	}
	head, err := r.applyTrackerEdits(ctx, marker, []subprocess.TrackerEdit{edit}, "finding "+key)
	if err != nil {
		return findingDecision{}, err
	}
	if _, _, err := r.store.TriageFinding(key, runstate.Triage{
		Status:  runstate.FindingFixed,
		By:      fmt.Sprintf("ticfac run %s applying the tracker edit the finding carries", r.runID),
		FixedAs: head,
	}); err != nil {
		return findingDecision{}, err
	}
	return findingDecision{FixedAs: head, Edit: edit.String()}, nil
}
