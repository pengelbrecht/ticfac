package reconcile

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Closing a finding promoted twice (hn6: oro and log, yjq and qrl).
//
// Findings are content-hashed, and a key is a finding's identity across every
// run of an epic. Before #164 the dedup held only within one run, so a run
// that collected an earlier run's report again absorbed its findings a second
// time, each as a new tick — and those duplicates stand in the tracker, open,
// gating the final review, after the dedup itself was fixed. Closing them by
// hand is the step a person should never have to take, so the run does it.
//
// Before anything is planned, every promotion any run of the epic recorded
// (runstate EpicPromotions: absorption decisions and promoted drafts) is
// grouped by finding key. A key promoted to more than one tick keeps the
// EARLIEST promotion as the finding's tick; every later one that is still
// OPEN is noted as a duplicate of it — naming the key, both runs and the
// canonical tick — and closed. A later tick already closed is left as it is.
// One that is in progress is left too, and the feed says so: a worker may be
// on it, and the tick it duplicates is no less done for one more pass. A
// routed promotion (another repository's tick) is never closed from here.

// StageDuplicateClosed is the line a closed duplicate leaves: the duplicate,
// the tick it duplicates, and the finding both were promoted from.
const StageDuplicateClosed = "duplicate_closed"

// StageDuplicateLeft is the line a duplicate the run could not close leaves:
// why it stands.
const StageDuplicateLeft = "duplicate_left"

// closeDuplicatePromotions closes every later tick a finding was promoted to
// twice. Errors are operational (the run state or the tracker could not be
// read or written); a tick the tracker does not know is left with a line.
func (r *Reconciler) closeDuplicatePromotions(ctx context.Context) error {
	if r.store == nil {
		return nil
	}
	promotions, err := r.store.EpicPromotions()
	if err != nil {
		return fmt.Errorf("read the epic's finding promotions: %w", err)
	}
	byKey := map[string][]runstate.Promotion{}
	for _, p := range promotions {
		if strings.ContainsAny(p.TickID, "/:") {
			continue // routed: another repository's tick
		}
		byKey[p.Key] = append(byKey[p.Key], p)
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// One entry per tick, at its earliest record.
		first := map[string]runstate.Promotion{}
		for _, p := range byKey[key] {
			if seen, ok := first[p.TickID]; !ok || p.At < seen.At {
				first[p.TickID] = p
			}
		}
		if len(first) < 2 {
			continue
		}
		ticks := make([]runstate.Promotion, 0, len(first))
		for _, p := range first {
			ticks = append(ticks, p)
		}
		sort.Slice(ticks, func(i, j int) bool {
			if ticks[i].At != ticks[j].At {
				return ticks[i].At < ticks[j].At
			}
			return ticks[i].TickID < ticks[j].TickID
		})
		canonical := ticks[0]
		if _, err := r.tracker.Show(ctx, canonical.TickID); err != nil {
			r.record("", StageDuplicateLeft,
				"finding %s was promoted to %d ticks, and the earliest, %s (run %s), cannot be read from the "+
					"tracker (%v): no later one is closed as its duplicate", key, len(ticks), canonical.TickID,
				canonical.RunID, err)
			continue
		}
		for _, dup := range ticks[1:] {
			current, err := r.tracker.Show(ctx, dup.TickID)
			if err != nil {
				r.record(dup.TickID, StageDuplicateLeft,
					"%s duplicates %s (finding %s), and the tracker cannot read it (%v): it is left as it is",
					dup.TickID, canonical.TickID, key, err)
				continue
			}
			switch current.Status {
			case "closed":
				continue
			case "open":
			default:
				r.record(dup.TickID, StageDuplicateLeft,
					"%s duplicates %s (both promoted from finding %s, by run %s and run %s) but is %s: a worker may be "+
						"on it, so it is not closed from under it", dup.TickID, canonical.TickID, key, dup.RunID,
					canonical.RunID, current.Status)
				continue
			}
			note := fmt.Sprintf("ticfac run %s: closed as a duplicate of %s. Both were promoted from finding %s — "+
				"%s by run %s at %s, this one by run %s at %s — and a finding is one tick, so the later promotion "+
				"is closed; the work is %s's", r.runID, canonical.TickID, key, canonical.TickID, canonical.RunID,
				canonical.At, dup.RunID, dup.At, canonical.TickID)
			if _, err := r.tracker.Note(ctx, dup.TickID, note); err != nil {
				return fmt.Errorf("note %s as a duplicate of %s: %w", dup.TickID, canonical.TickID, err)
			}
			if _, err := r.tracker.Close(ctx, dup.TickID); err != nil {
				return fmt.Errorf("close %s as a duplicate of %s: %w", dup.TickID, canonical.TickID, err)
			}
			r.record(dup.TickID, StageDuplicateClosed,
				"%s is closed as a duplicate of %s: both were promoted from finding %s (%s by run %s, %s by run %s), "+
					"and the earlier one stands", dup.TickID, canonical.TickID, key, canonical.TickID, canonical.RunID,
				dup.TickID, dup.RunID)
		}
	}
	return nil
}
