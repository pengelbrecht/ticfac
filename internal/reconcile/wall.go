package reconcile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The wall clock is a runaway BACKSTOP, not the stuck detector (tick wv2).
//
// epic-6in (2026-09-28): two strong-tier implement attempts ran out a fixed
// 3600s bound while they were working (dz1 attempt 5's transcript has events
// until six seconds before the stop). Whether a worker is stuck is now the
// executors' stuck watch's question (subprocess/activity.go), answered from
// what the worker is visibly doing. What is left for the wall clock is the
// worker that is busy forever, so the default is generous — DefaultWallSeconds,
// eight hours — and it can be set:
//
//   - per role and tier, in the runners config's [tier_policy.wall_seconds]
//     ("implement.strong", "implement", "strong"; the most specific wins);
//   - per tick, with a `wall_minutes:<n>` label, which wins over all of them.
//
// The bound an attempt was issued is recorded on its marker, so an adopting
// run measures the attempt against the bound it was ISSUED, not the one
// today's config would derive.

// wallLabelPrefix is the label namespace a tick's own backstop rides in, the
// way tier overrides ride "tier:" and file declarations "touch:".
const wallLabelPrefix = "wall_minutes:"

// parseWallLabel reads a tick's `wall_minutes:<n>` label. A malformed one is
// an error naming the tick and the label: a label is a weakly typed field the
// tracker cannot validate, so it surfaces here or nowhere.
func parseWallLabel(tick string, labels []string) (int, bool, error) {
	found, minutes := false, 0
	for _, raw := range labels {
		value, ok := strings.CutPrefix(raw, wallLabelPrefix)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 1 {
			return 0, false, fmt.Errorf("tick %s carries label %q, which is not a whole number of minutes (>= 1)", tick, raw)
		}
		if found && n != minutes {
			return 0, false, fmt.Errorf("tick %s carries two %s labels (%d and %d): one tick has one backstop", tick, wallLabelPrefix, minutes, n)
		}
		found, minutes = true, n
	}
	return minutes, found, nil
}

// wallFor is the backstop one dispatch is issued, and where it came from.
func (r *Reconciler) wallFor(entry planEntry, tier string) (int, string, error) {
	minutes, found, err := parseWallLabel(entry.TickID, entry.Labels)
	if err != nil {
		return 0, "", err
	}
	if found {
		return minutes * 60, fmt.Sprintf("the tick's %s%d label", wallLabelPrefix, minutes), nil
	}
	if seconds, where, ok := r.tierPolicy.WallSecondsFor(entry.Role, runconfig.Tier(tier)); ok {
		return seconds, where, nil
	}
	return r.opts.WallSeconds, "the run's --wall", nil
}

// wallOf is the backstop an attempt was issued: its marker's, or the run's
// own for a marker from before the bound was recorded on it.
func (r *Reconciler) wallOf(marker attemptHandle) int {
	if marker.WallSeconds > 0 {
		return marker.WallSeconds
	}
	return r.opts.WallSeconds
}

// wallOfDispatch is the bound a JobSpec is issued.
func (r *Reconciler) wallOfDispatch(d Dispatch) int {
	if d.WallSeconds > 0 {
		return d.WallSeconds
	}
	return r.opts.WallSeconds
}

// StagePolledAtResume is the line a resumed run writes for each live worker it
// polled BEFORE its slow startup work (tick wv2): the fold of the base
// branch and the sweep can take many minutes — epic-6in's dz1 restart spent
// 31 of them there without once polling its live worker — and the poll is
// where a herdr worker's stuck watch and wall clock run.
const StagePolledAtResume = "polled_at_resume"

// pollStanding polls every attempt of this run whose executor state stands
// on this host and that no decision has settled, once, before anything slow:
// an executor's Inspect is where the herdr stuck watch and wall clock run,
// and a run that spends half an hour folding and sweeping first is half an
// hour in which no bound is enforced. It changes nothing about adoption:
// the plan still adopts each attempt by identity afterwards.
func (r *Reconciler) pollStanding() {
	if r.store == nil {
		return
	}
	attempts, err := r.store.Attempts()
	if err != nil {
		return
	}
	// The LATEST attempt of each tick: an earlier one exists only because
	// the run rejected it, and is spent.
	latest := map[string]runstate.Attempt{}
	for _, a := range attempts {
		if prev, ok := latest[a.TickID]; !ok || a.Attempt > prev.Attempt {
			latest[a.TickID] = a
		}
	}
	ticks := make([]string, 0, len(latest))
	for tick := range latest {
		ticks = append(ticks, tick)
	}
	sort.Strings(ticks)
	for _, tick := range ticks {
		marker := handleFromMap(latest[tick].JobHandle)
		marker.StateRoot = r.execStateDir(marker.TickID, marker.Attempt)
		state, found := findAttemptState(marker.StateRoot)
		if !found {
			continue
		}
		dispatch, err := r.dispatchFor(marker)
		if err != nil {
			continue
		}
		executor, _, err := r.opts.NewExecutor(dispatch)
		if err != nil {
			continue
		}
		status, err := executor.Inspect(&subprocess.JobHandle{
			SchemaVersion: subprocess.SchemaVersion, JobID: marker.JobID, Attempt: marker.Attempt,
			Executor: marker.Executor, Handle: map[string]any{"state": state},
		}, "")
		if err != nil || status.Terminal {
			// A settled attempt has nothing a poll could enforce; the plan
			// collects it as before.
			continue
		}
		r.record(marker.TickID, StagePolledAtResume,
			"%s was polled before the run's startup work (the base fold, the sweep), so its stuck watch and its "+
				"backstop run on time across the restart: it reads %s — %s",
			r.attemptName(marker.TickID, marker.Attempt), status.State, lastObservation(status))
	}
}

// The feed lines the stuck watch owes (tick wv2). Each is written from the
// executor's own observation, which carries the evidence, so a watcher reads
// why without opening the attempt's store.
const (
	// StageStuckNudged: the worker showed no activity for the stuck window
	// and was nudged in its own session.
	StageStuckNudged = "stuck_nudged"
	// StageStuckStopped: still no activity a window after the nudge; the
	// worker was stopped, its work snapshotted, and the attempt settles
	// failed for the retry and the tier ladder.
	StageStuckStopped = "stuck_stopped"
	// StageWipNudged: a large uncommitted change on a branch that has not
	// moved for a while; the worker was asked to commit it.
	StageWipNudged = "wip_nudged"
)

// alreadySaid says whether this incarnation's journal already carries the
// same stop for the tick: the executor's record of it and the settlement
// sentence it answers inspect with open the same way, one extending the
// other.
func (r *Reconciler) alreadySaid(tick, stage, detail string) bool {
	for _, event := range r.journal {
		if event.Tick == tick && event.Stage == stage &&
			(strings.HasPrefix(detail, event.Detail) || strings.HasPrefix(event.Detail, detail)) {
			return true
		}
	}
	return false
}

// announceActivity puts the stuck watch's observations on the feed.
func (r *Reconciler) announceActivity(tick string, status *subprocess.JobStatus) {
	if status == nil {
		return
	}
	for _, o := range status.Observations {
		switch {
		case subprocess.IsStuckNudge(o):
			r.record(tick, StageStuckNudged, "%s", o.Detail)
		case subprocess.IsStuckStop(o):
			// Once per tick: the executor's record of the stop and its
			// settlement sentence both say it.
			if !r.alreadySaid(tick, StageStuckStopped, o.Detail) {
				r.record(tick, StageStuckStopped, "%s", o.Detail)
			}
		case subprocess.IsWipNudge(o):
			r.record(tick, StageWipNudged, "%s", o.Detail)
		}
	}
}
