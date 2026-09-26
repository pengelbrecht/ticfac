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
//   - the ABSORPTION DECISION (tick npq) is made where the finding is
//     discovered, by the run, against the epic's own definition of done
//     (absorb.go): a gating finding is promoted into the running epic as a
//     tick placed before the final review, a non-gating one becomes a
//     backlog tick with an owner, and what the run cannot decide — a routed
//     finding, an epic whose done is prose — stays a person's at the
//     close-out. The scope decision is no longer a person's by DEFAULT; it
//     is a person's where the run's own rules say it must be, and every
//     decision the run makes is a record on the run branch;
//   - a tick whose findings are untriaged CLOSES, and the finding rides to the
//     CLOSE-OUT (tick aqm), which does not hand over while any finding of the
//     run is untriaged — one decision point at the end, where a person is
//     already being asked to look, instead of one per tick mid-run.
//
// The worker never writes the tracker: `.tick/` is a protected prefix, and the
// draft this package files lives in `.ticfac/`, on the integration branch the
// run owns. A promoted tick carries `discovered_from` either way — a person's
// promotion records the tick they created, the run's promotion creates the tick
// itself behind a recorded decision — so a promoted tick is never the 9t0
// shape: filed with no provenance and no review.

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
			"%s reported a findings block this reconciler cannot read: %s. The tick is NOT closed: "+
				"dropping findings nobody could parse is the failure the channel exists to remove — fix the block and "+
				"the run again dispatches the tick",
			r.attemptName(marker.TickID, marker.Attempt), collected.FindingsProblem)
	}
	if len(collected.Findings) == 0 {
		return nil
	}

	// The fold, noted in the attempt's records (tick ryv): keys the finding
	// record does not know were kept inside the finding's body as labelled
	// lines, and this line says so — naming them and the attempt — because a
	// fold nobody recorded is indistinguishable from a channel that silently
	// rewrites what a worker wrote. It is a NOTE, not a problem: the attempt
	// stands, the finding is drafted with the fold in its body, and a person
	// triaging the draft reads the unknown half where it rode.
	if len(collected.FindingsFolded) > 0 {
		r.record(marker.TickID, StageFindingFolded,
			"%s reported finding keys the record does not know, folded into the finding bodies rather than refused: %s",
			r.attemptName(marker.TickID, marker.Attempt), strings.Join(collected.FindingsFolded, ", "))
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
				"%s reported a finding this reconciler cannot draft: %v", r.attemptName(marker.TickID, marker.Attempt), err)
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
			// The finding's DONE EVIDENCE (tick nfo) rides the draft field for
			// field as reported: which acceptance item of the epic's definition
			// of done the reporter says is broken ("none" when none), and the
			// command or test that would demonstrate it. This is the half the
			// absorption decision runs on — the done check where the item is
			// runnable, a prediction's input where it is not — and the claim is
			// what the reporter is later scored against, so it is preserved
			// with the discovery, never summarised into it.
			DoneItem:           finding.DoneItem,
			DemonstratingCheck: finding.DemonstratingCheck,
			TickID:             marker.TickID,
			Attempt:            marker.Attempt,
			Status:             runstate.FindingProposed,
			ProposedAt:         r.now().UTC().Format("2006-01-02T15:04:05Z"),
			Provenance:         r.attemptProvenance(dispatch),
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
				"finding %s (%q) was already drafted by %s and is %s: nothing new is proposed",
				key, finding.Title, r.attemptName(original.TickID, original.Attempt), original.Status)
			// The absorption decision still runs on a repeat (tick npq): a
			// draft left PROPOSED by an incarnation that was killed between
			// drafting and deciding is decided by this one, once. A draft a
			// person or an earlier incarnation already decided is left alone
			// by the decision's own standing-draft check.
			if _, err := r.decideFinding(ctx, marker, key, dispatch); err != nil {
				return err
			}
			continue
		}
		r.record(marker.TickID, StageFindingFiled,
			"finding %s drafted for triage: %s %q, severity %s, for %s (discovered by %s), %s",
			key, finding.Kind, finding.Title, finding.Severity, targetName(finding.Target),
			r.attemptName(marker.TickID, marker.Attempt), draft.LinkageText())
		// THE ABSORPTION DECISION (tick npq): the step that used to wait for a
		// person, taken by the run — a gating finding is promoted into the
		// running epic before the final review, a non-gating one becomes a
		// backlog tick with an owner, and what the run cannot decide stays a
		// person's. The decision is made where the finding is discovered, so
		// the absorbed tick is fixed before the items it gates are asserted
		// rather than appended after the review that asserts them.
		decided, err := r.decideFinding(ctx, marker, key, dispatch)
		if err != nil {
			return err
		}
		// The tick's own record names what happened to the draft, so a person
		// reading the tracker — not only the run state — sees the finding and
		// where it went: the triage commands when the run left it for them,
		// the tick the run promoted it to when it did not.
		note := r.findingLeftNote(marker, finding, key, decided)
		if _, err := r.tracker.Note(ctx, marker.TickID, note); err != nil {
			return fmt.Errorf("note the drafted finding %s on %s: %w", key, marker.TickID, err)
		}
	}
	return nil
}

// untriagedFindings is every finding this run's attempts reported that is
// still waiting for a person, whatever tick reported it. Read from ORIGIN,
// fetched first: a gate that checked this run's memory of the drafts rather
// than the durable record is a gate that survives a triage made while the
// run was stopped.
func (r *Reconciler) untriagedFindings() ([]runstate.Finding, error) {
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
		if finding.Status == runstate.FindingProposed {
			out = append(out, finding)
		}
	}
	return out, nil
}

// gateOnFindings was the per-tick close gate (tick 7vn), and tick aqm
// removed it: a tick whose findings are untriaged CLOSES, and the gate moved
// to the close-out (gateCloseoutOnFindings), where one decision point
// replaces N. What survives here is the count a close RECORDS, so the feed
// says the tick closed carrying findings rather than closing silently
// behind them.
func (r *Reconciler) carriedUntriaged(tick string) int {
	untriaged, err := r.untriagedFindings()
	if err != nil {
		// An unreadable draft store must not read as "no findings" — the
		// same rule the gate kept — but it must not refuse a close the
		// close-out will refuse either: the close-out's gate re-reads the
		// store and fails closed there, where the refusal belongs.
		return 0
	}
	n := 0
	for _, finding := range untriaged {
		if finding.TickID == tick {
			n++
		}
	}
	return n
}

// gateCloseoutOnFindings is the gate the per-tick hold became (tick aqm): the
// close-out does not hand over while any finding of the run is untriaged,
// naming them — one decision point at the end, where a person is already
// being asked to look, instead of one per tick mid-run. The findings are on
// the epic PR when the repository declares the rule (the body was rewritten
// from the final records immediately before this gate), so the person the
// hold asks for reads them where the merge judgement happens; without the
// rule the durable record under .ticfac/ is the view, listed by
// `ticfac findings`.
//
// prNumber is the epic PR's number when one exists, zero when the repository
// declares no PR + CI rule. It returns the refusal that stops the hand-over,
// and an error only when nobody can say — an unreadable draft store must not
// read as "no findings", the same way an unreachable remote never reads as
// "not merged".
func (r *Reconciler) gateCloseoutOnFindings(tick string, prNumber int) (*Refusal, error) {
	untriaged, err := r.untriagedFindings()
	if err != nil {
		return nil, fmt.Errorf("read the run's drafted findings: %w", err)
	}
	if len(untriaged) == 0 {
		return nil, nil
	}
	onThePR := ""
	if prNumber > 0 {
		onThePR = fmt.Sprintf(" and the epic PR #%d carries each one's full text", prNumber)
	}
	keys := make([]string, 0, len(untriaged))
	titles := make([]string, 0, len(untriaged))
	for _, finding := range untriaged {
		keys = append(keys, finding.Key)
		// The linkage mark (tick nfo) rides the hold's naming of each finding:
		// the person triaging sees which acceptance item the reporter says is
		// broken — and which finding made no claim — without opening the draft.
		titles = append(titles, fmt.Sprintf("%q (%s, severity %s, tick %s, for %s, %s)",
			finding.Title, finding.Kind, finding.Severity, finding.TickID, targetName(finding.Target), finding.LinkageText()))
	}
	return r.refuse(RefusedFindingUntriaged, tick,
		"%d finding(s) this run drafted are still waiting for a person — %s — and the close-out does "+
			"not hand over while one is (tick aqm): the ticks that reported them are closed, the findings rode "+
			"here, and this is the one decision point. Triage each with `ticfac finding %s %s --promote-as "+
			"<tick> --by \"<who>\"` (promote into the repository it targets), `ticfac finding %s %s --discard "+
			"--by \"<who>\"`, or — when the finding was repaired inside this epic — `ticfac finding %s %s "+
			"--fixed-as <commit> --by \"<who>\"`, then run the epic again under this run id: the gate has already "+
			"passed, so the close-out's close is the only step left — and the resume closes each role tick "+
			"behind its recorded decision, it does not dispatch the job again (tick 80x). The drafts are keys %s under "+
			".ticfac/runs/%s/findings/ on %s, listed by `ticfac findings %s`%s",
		len(untriaged), strings.Join(titles, "; "), r.opts.EpicID, strings.Join(keys, "|"), r.opts.EpicID,
		strings.Join(keys, "|"), r.opts.EpicID, strings.Join(keys, "|"), strings.Join(keys, ", "), r.runID,
		r.opts.Remote, r.opts.EpicID, onThePR), nil
}
