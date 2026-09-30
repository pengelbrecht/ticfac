package statusmodel

import (
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The health verdict (epic hn6, rule 3): the headline a dashboard answers
// "is it healthy" with — a word a person reads, not four counters they have
// to interpret. Wave 1 (tick r5i) declared the shape with the stub's answer;
// this file is the wave-2 derivation (tick 7uv): the run's own liveness and
// lifecycle decide whether it is going, and a going run is degraded only by
// causes the run's own typed lines state — never a parse of prose. The raw
// counts stay in Health, a reader's drill-in; the verdict is the headline.

// buildHealth counts the run's own typed statements about its health — the
// remote retries, the interventions it resumed by itself, the stall
// warnings, the wall clock firings. Counts of lines, never parses of prose.
func buildHealth(feed []runfeed.Event) Health {
	h := Health{}
	for _, e := range feed {
		switch e.Stage {
		case reconcile.StageRemoteRetried:
			h.RemoteRetries++
		case reconcile.StageResumedAutomatically:
			h.Interventions++
		case reconcile.StageStallWarned:
			h.StallWarnings++
		case reconcile.StageWallClock:
			h.WallClocksFired++
		}
	}
	return h
}

// buildVerdict states the headline, grown from the model built so far — the
// liveness answer, the lifecycle, the waits, the counts and the run's own
// typed lines:
//
//   - stopped: the run is not alive and its lifecycle is not at its own end
//     (done or cancelled) — a dead run or a failed run. The summary is the
//     liveness answer's own reason, or the dead-run wait's what when the
//     probe said nothing.
//   - degraded: the run is going and something is wrong — a source that
//     could not be read, a standing worker nudged as stuck whose attempt
//     never answered, a remote that exhausted its retries, a stall warning
//     inside its window. The summary names the first cause in that order.
//   - healthy: everything else, with the word itself as the summary.
//
// The recovered list is stated in every state: what the run got past by
// itself is a fact wherever it ends up, and it is shown as calm, never as
// alarms.
func buildVerdict(src Sources, m Model) HealthVerdict {
	recovered := buildRecovered(src, m)
	if !src.Liveness.Alive && m.Lifecycle.Phase != PhaseDone && m.Lifecycle.Phase != PhaseCancelled {
		summary := src.Liveness.Reason
		if summary == "" && m.WaitsOn != nil && m.WaitsOn.Kind == WaitDeadRun {
			summary = m.WaitsOn.What
		}
		return HealthVerdict{State: VerdictStopped, Summary: summary, Recovered: recovered}
	}
	if src.Liveness.Alive {
		if cause := degradedCause(src, m); cause != "" {
			return HealthVerdict{State: VerdictDegraded, Summary: cause, Recovered: recovered}
		}
	}
	return HealthVerdict{State: VerdictHealthy, Summary: VerdictHealthy, Recovered: recovered}
}

// stallWindow is how long a stall warning stays a live cause: fifteen
// minutes from its own line, measured against the model's clock. Older than
// that and the run has had its chance to look — the warning is history,
// counted in Health's own counters, not something wrong NOW.
const stallWindow = 15 * time.Minute

// degradedCause names the first thing wrong with a going run, in the fixed
// order the tick sets: an unreadable source, an unanswered stuck nudge, an
// exhausted remote, a stall warning inside its window. Empty when nothing
// is wrong. A cause that is not a typed line the run itself wrote is a
// cause this function cannot state.
func degradedCause(src Sources, m Model) string {
	if len(m.Degraded) > 0 {
		return strings.Join(m.Degraded, ", ") + " unreadable"
	}
	if cause := unansweredNudge(src, m); cause != "" {
		return cause
	}
	if latestStage(src.Feed, "", reconcile.StageRemoteExhausted) != nil {
		return "a remote call exhausted its retries"
	}
	if line := latestStallWithin(src.Feed, src.Now, stallWindow); line != nil {
		cause := "warned as stalled"
		if line.TickID != nil && *line.TickID != "" {
			cause = *line.TickID + " " + cause
		}
		if age := ageOf(src.Now, line.At); age != "" {
			cause += " " + age + " ago"
		}
		return cause
	}
	return ""
}

// unansweredNudge is the first standing worker whose latest stuck nudge its
// attempt never answered, named the way the tick spells it: "v7z nudged as
// stuck 4m ago". A nudge is answered by any line for the same attempt after
// it that is not the watcher's own quiet statement — the run observing the
// attempt act (a report, a wip change, an ordinary prompt-nudge); another
// nudge, a stop, a stall warning and a wall-clock firing say the attempt
// went quiet, which is the nudge's own cause restated, never its answer.
func unansweredNudge(src Sources, m Model) string {
	// m.Workers is nullable — nil is the census that could not be taken (a
	// cloud run's workers are not on this machine) — and it answers no
	// workers, never a crash.
	workers := []Worker{}
	if m.Workers != nil {
		workers = *m.Workers
	}
	for _, w := range workers {
		at := -1
		for i := range src.Feed {
			if src.Feed[i].Stage == reconcile.StageStuckNudged && lineIsAbout(src.Feed[i], w.TickID, w.Attempt) {
				at = i
			}
		}
		if at < 0 || answeredAfter(src.Feed, at, w.TickID, w.Attempt) {
			continue
		}
		cause := fmt.Sprintf("%s nudged as stuck", w.TickID)
		if age := ageOf(src.Now, src.Feed[at].At); age != "" {
			cause += " " + age + " ago"
		}
		return cause
	}
	return ""
}

// answeredAfter says whether any line after the nudge is the attempt doing
// something: every stage but the watcher's own quiet statements is the run
// observing the attempt act, and those answer the stuck nudge.
func answeredAfter(feed []runfeed.Event, at int, tickID string, attempt int) bool {
	for _, e := range feed[at+1:] {
		if !lineIsAbout(e, tickID, attempt) {
			continue
		}
		switch e.Stage {
		case reconcile.StageStuckNudged, reconcile.StageStuckStopped,
			reconcile.StageStallWarned, reconcile.StageWallClock:
			continue
		}
		return true
	}
	return false
}

// lineIsAbout says whether one feed line is about a given (tick, attempt):
// the run stamps every line about a tick with the tick's current attempt,
// so a line for the pair is the run's own word about that dispatch.
func lineIsAbout(e runfeed.Event, tickID string, attempt int) bool {
	return e.TickID != nil && *e.TickID == tickID && e.Attempt != nil && *e.Attempt == attempt
}

// latestStallWithin is the newest stall warning inside its window, if one
// stands. A stamp that does not parse is not a warning inside anything.
func latestStallWithin(feed []runfeed.Event, now time.Time, window time.Duration) *runfeed.Event {
	var latest *runfeed.Event
	for i := range feed {
		if feed[i].Stage != reconcile.StageStallWarned {
			continue
		}
		at, err := time.Parse(time.RFC3339, feed[i].At)
		if err != nil || now.Sub(at) > window {
			continue
		}
		latest = &feed[i]
	}
	return latest
}

// buildRecovered counts what the run got past by itself, from the typed
// lines that stated each recovery — the calm half of the headline
// ("recovered: net ×14, sleep 41m"): the network retries it waited through,
// the host suspensions it slept through (with the wall clock those lines
// state, where they state one), the resumes nobody typed, the wall clocks
// that fired and did not spend the attempt.
func buildRecovered(src Sources, m Model) []Recovery {
	recovered := []Recovery{}
	if m.Health.RemoteRetries > 0 {
		recovered = append(recovered, Recovery{What: "net", Count: m.Health.RemoteRetries})
	}
	sleep, seconds, stated := 0, int64(0), false
	for _, e := range src.Feed {
		if e.Stage != reconcile.StageHostSuspended {
			continue
		}
		sleep++
		if s, ok := suspendedSeconds(e.Detail); ok {
			seconds += s
			stated = true
		}
	}
	if sleep > 0 {
		sleepRecovery := Recovery{What: "sleep", Count: sleep}
		if stated {
			sleepRecovery.Seconds = &seconds
		}
		recovered = append(recovered, sleepRecovery)
	}
	if m.Health.Interventions > 0 {
		recovered = append(recovered, Recovery{What: "interventions", Count: m.Health.Interventions})
	}
	if m.Health.WallClocksFired > 0 {
		recovered = append(recovered, Recovery{What: "wall clocks", Count: m.Health.WallClocksFired})
	}
	return recovered
}

// suspendedSeconds reads how long one host suspension lasted out of its own
// line — the reconciler states it as "the host was suspended for about
// <duration>: …" (suspend.go). False when the line states no duration: the
// suspension still counts, and the seconds stay null rather than guessed.
func suspendedSeconds(detail string) (int64, bool) {
	const lead = "suspended for about "
	at := strings.Index(detail, lead)
	if at < 0 {
		return 0, false
	}
	rest := detail[at+len(lead):]
	if end := strings.IndexAny(rest, ":,;"); end >= 0 {
		rest = rest[:end]
	}
	down, err := time.ParseDuration(strings.TrimSpace(rest))
	if err != nil || down < 0 {
		return 0, false
	}
	return int64(down.Round(time.Second).Seconds()), true
}

// ageOf renders how long ago a feed line's own stamp says it happened,
// measured against the model's own clock. Empty when the stamp does not
// parse: a summary that guessed at an age would be a summary that lied.
func ageOf(now time.Time, at string) string {
	stamped, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return ""
	}
	return shortAge(now.Sub(stamped))
}

// shortAge is the one-word duration the verdict's summaries read: the same
// shape the dashboard's own humanDuration prints (4m, 90s, 2h03m).
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int64(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int64(d.Minutes()))
	case d < 24*time.Hour:
		h, min := int64(d.Hours()), int64(d.Minutes())%60
		if min == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, min)
	default:
		return fmt.Sprintf("%dd%dh", int64(d.Hours())/24, int64(d.Hours())%24)
	}
}
