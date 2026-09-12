package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The reconciler's half of the findings channel (tick 7vn).
//
// A worker that discovers something outside its tick reports it in its
// RESULT-<tick-id>.md as a typed block; collect lifts it into the collection
// and the role-result envelope. What happens HERE is the part prose never
// carried reliably:
//
//   - every finding becomes a DRAFT, durably, on origin — the funnel shape
//     ticks already runs for declared sources, deduplicated on
//     (source, external_ref), where a redelivery proposes nothing new
//     whatever the human did with the original;
//   - every draft is stamped with the ATTEMPT that discovered it, so
//     `discovered_from` is never empty again;
//   - a finding targeting ANOTHER repository keeps its target and is routed
//     there at promotion, rather than dropped;
//   - a tick whose findings are untriaged is REFUSED its close, which is
//     what stops one falling on the floor.
//
// The worker never writes the tracker: `.tick/` is a protected prefix, and the
// draft this package files lives in `.ticfac/`, on the integration branch the
// run owns. The DRAFT is not a tick: a tick is what a person's promotion
// creates, which keeps the scope decision human (the 9t0 case — a genuinely
// useful tick, filed with no provenance and no review — is the failure both
// halves of that rule exist for).

// findingSource is the funnel source a worker's report arrives as. A source
// name is half the dedup key: the same finding, from the same channel, for the
// same subject, is one draft however many attempts report it.
const findingSource = "ticfac-worker"

// findingKey is the draft's dedup identity: the external_ref half of
// (source, external_ref) — the funnel's key, computed over the SUBJECT the
// finding is about (kind, title, target) rather than over the delivery,
// because the delivery changes with every attempt and the subject does not.
func findingKey(f subprocess.Finding) string {
	sum := sha256.Sum256([]byte(findingSource + "\x00" + f.Kind + "\x00" + f.Title + "\x00" + f.Target))
	return hex.EncodeToString(sum[:])
}

// targetName is the finding's target for a record: the repository being run
// when the worker named none.
func targetName(target string) string {
	if target == "" {
		return "this repository"
	}
	return target
}

// fileFindings drafts every finding the attempt reported, before anything is
// decided about the attempt itself.
//
// It runs BEFORE the verdict checks on purpose: a BLOCKED answer is exactly
// the report that carries a proposal (the v3i shape), and a finding is
// discovery, not a deliverable — it must be drafted whether the attempt
// passes, fails or asks for a person. A findings block that does not parse is
// the one thing refused here rather than drafted: closing a tick behind
// findings nobody could read is the 604 failure with one more step in it.
func (r *Reconciler) fileFindings(ctx context.Context, marker attemptHandle, collected *subprocess.Collection) error {
	if collected == nil {
		return nil
	}
	if collected.FindingsProblem != "" {
		r.setTick(marker.TickID, "rejected")
		r.record(marker.TickID, StageRejected, "the report carried a findings block that could not be read: %s",
			collected.FindingsProblem)
		return r.refuse(RefusedFindingInvalid, marker.TickID,
			"attempt %d of %s reported a findings block this reconciler cannot read: %s. The tick is NOT closed: "+
				"dropping findings nobody could parse is the failure the channel exists to remove — fix the block and "+
				"the run again dispatches the tick",
			marker.Attempt, marker.TickID, collected.FindingsProblem)
	}
	if len(collected.Findings) == 0 {
		return nil
	}

	if _, err := r.store.Fetch(); err != nil {
		return err
	}
	dispatch, err := r.dispatchFor(marker)
	if err != nil {
		return err
	}
	for _, finding := range collected.Findings {
		if err := finding.Validate(); err != nil {
			return r.refuse(RefusedFindingInvalid, marker.TickID,
				"attempt %d of %s reported a finding this reconciler cannot draft: %v", marker.Attempt, marker.TickID, err)
		}
		key := findingKey(finding)
		draft := runstate.Finding{
			Key:            key,
			Source:         findingSource,
			DiscoveredFrom: marker.JobID,
			Kind:           finding.Kind,
			Title:          finding.Title,
			Body:           finding.Body,
			Severity:       finding.Severity,
			Target:         finding.Target,
			TickID:         marker.TickID,
			Attempt:        marker.Attempt,
			Status:         runstate.FindingProposed,
			ProposedAt:     r.now().UTC().Format("2006-01-02T15:04:05Z"),
			Provenance:     r.attemptProvenance(dispatch),
		}
		outcome, err := r.store.PutFinding(draft)
		if err != nil {
			return fmt.Errorf("draft the finding %q for %s: %w", finding.Title, marker.TickID, err)
		}
		if !outcome.EffectPermitted() {
			// The dedup: a draft already exists under this key, from an
			// earlier attempt of this tick or a triage that already decided
			// it. The ORIGINAL stands — including the attempt that first
			// reported it — and nothing new is proposed, whatever the human
			// did with the original. That is the funnel's rule, and it is
			// what stops a chatty worker re-proposing something a person
			// already declined.
			original, ok, err := r.store.Finding(key)
			if err != nil || !ok {
				return fmt.Errorf("the finding %s of %s is a duplicate whose original cannot be read: %v",
					key, marker.TickID, err)
			}
			r.record(marker.TickID, StageFindingDuplicate,
				"finding %s (%q) was already drafted by attempt %d of %s and is %s: nothing new is proposed",
				key, finding.Title, original.Attempt, original.TickID, original.Status)
			continue
		}
		r.record(marker.TickID, StageFindingFiled,
			"finding %s drafted for triage: %s %q, severity %s, for %s (discovered by attempt %d)",
			key, finding.Kind, finding.Title, finding.Severity, targetName(finding.Target), marker.Attempt)
		// The tick's own record names the draft, so a person reading the
		// tracker — not only the run state — sees that a finding is waiting
		// for them, and sees where to triage it.
		note := fmt.Sprintf("ticfac run %s: attempt %d reported a finding drafted for triage — %s %q "+
			"(key %s, severity %s, for %s). Triage with `ticfac finding %s %s --promote-as <tick> --by "+
			"\"<who>\"` or `--discard --by \"<who>\"`; the tick cannot close while it is untriaged.",
			r.runID, marker.Attempt, finding.Kind, finding.Title, key, finding.Severity,
			targetName(finding.Target), r.opts.EpicID, key)
		if _, err := r.tracker.Note(ctx, marker.TickID, note); err != nil {
			return fmt.Errorf("note the drafted finding %s on %s: %w", key, marker.TickID, err)
		}
	}
	return nil
}

// untriagedFindings is every finding this tick's attempts reported that is
// still waiting for a person. Read from ORIGIN, fetched first: a close that
// checked this run's memory of the drafts rather than the durable record is a
// close that survives a triage made while the run was stopped.
func (r *Reconciler) untriagedFindings(tickID string) ([]runstate.Finding, error) {
	if r.store == nil {
		return nil, nil
	}
	if _, err := r.store.Fetch(); err != nil {
		return nil, err
	}
	findings, err := r.store.Findings()
	if err != nil {
		return nil, err
	}
	var out []runstate.Finding
	for _, finding := range findings {
		if finding.TickID == tickID && finding.Status == runstate.FindingProposed {
			out = append(out, finding)
		}
	}
	return out, nil
}

// gateOnFindings is the close's other gate: a tick whose findings have not
// been triaged does not close. It returns the refusal that stops the close, or
// a nil refusal when there is nothing waiting — and an error only when nobody
// can say, because an unreadable draft store must not read as "no findings",
// the same way an unreachable remote never reads as "not merged".
func (r *Reconciler) gateOnFindings(tick string) (*Refusal, error) {
	untriaged, err := r.untriagedFindings(tick)
	if err != nil {
		return nil, fmt.Errorf("read the findings drafted for %s: %w", tick, err)
	}
	if len(untriaged) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(untriaged))
	titles := make([]string, 0, len(untriaged))
	for _, finding := range untriaged {
		keys = append(keys, finding.Key)
		titles = append(titles, fmt.Sprintf("%q (%s, for %s)", finding.Title, finding.Kind, targetName(finding.Target)))
	}
	return r.refuse(RefusedFindingUntriaged, tick,
		"%s reported %d finding(s) nobody has triaged — %s — and the tick is NOT closed: a finding that falls "+
			"on the floor is the failure this gate exists to stop. Triage each with `ticfac finding %s %s "+
			"--promote-as <tick> --by \"<who>\"` (promote into the repository it targets) or `ticfac finding %s "+
			"%s --discard --by \"<who>\"`, then run the epic again under this run id: the gate has already passed, "+
			"so the close is the only step left. The drafts are key %s under .ticfac/runs/%s/findings/ on %s",
		tick, len(untriaged), strings.Join(titles, "; "), r.opts.EpicID, strings.Join(keys, "|"), r.opts.EpicID,
		strings.Join(keys, "|"), strings.Join(keys, ", "), r.runID, r.opts.Remote), nil
}
