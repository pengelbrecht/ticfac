package statusmodel

import (
	"fmt"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The health verdict (epic hn6, wave 2 — tick 7uv): the headline a dashboard
// answers "is it healthy" with — one word a person reads, a one-line why
// beside it, and what the run got past by itself listed as calm, never as
// alarms (hn6 rule 3: health as a verdict; the raw counts stay in Health as
// the reader's drill-in).
//
// The derivation is a pure function of the Sources, the Model built so far
// and the run's own TYPED lines: every state is read off the liveness answer,
// the lifecycle, the degraded list or a feed stage nobody but the reconciler
// writes. The one place a line's DETAIL is read is the host suspension's
// duration — a number only that sentence carries, and the recovered list's
// one measured span.

// nudgeResolvers is the closed set of feed stages that resolve a stuck nudge
// for one attempt: the lines that record the attempt or its worker MOVING
// after the nudge — its gate, its travels through the pipeline, its settle
// and the runner's turn-end ask (both ride StageWaiting), and the change
// the run saw it produce (wip_nudged). The run's own later words about the
// same silence do not resolve it: another stuck_nudged, the stuck_stopped
// that CONFIRMS the nudge ("still no activity a window after"), a stall
// warning, a wall clock, the polled_at_resume bookkeeping — a worker still
// silent after a nudge is exactly the degraded case the nudge rule exists
// to state.
var nudgeResolvers = map[string]bool{
	reconcile.StageGateStarted:  true,
	reconcile.StageGateRunning:  true,
	reconcile.StageGateStalled:  true,
	reconcile.StageGatePassed:   true,
	reconcile.StageGateFailed:   true,
	reconcile.StageWaiting:      true,
	reconcile.StageWipNudged:    true,
	reconcile.StageCollected:    true,
	reconcile.StageRejected:     true,
	reconcile.StageIntegrated:   true,
	reconcile.StagePublished:    true,
	reconcile.StageSettled:      true,
	reconcile.StageClosed:       true,
	reconcile.StageCleanedUp:    true,
	reconcile.StageRedispatched: true,
}

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
	h.Pushes, h.PeakPushesPerMinute, h.GitHubErrors = PushHealth(feed)
	return h
}

// buildVerdict derives the headline, in the order the words exclude each
// other:
//
//   - stopped: the run is not going — not alive, and its own lifecycle has
//     neither finished (done), been cancelled, nor reached the merge, the
//     one phase a completed run stands in while its PR waits for a person
//     (tick jkb): that run's work is over and the merge is the person's
//     by design, so reading it "stopped" paged a failure nobody has to
//     fix. The summary is the liveness answer's own reason, or — when the
//     probe said nothing — the dead-run wait's own sentence.
//   - degraded: the run is going and something is wrong — a source that
//     could not be read, a worker nudged as stuck with nothing after the
//     nudge, a remote that exhausted its retries, a stall warning fresh
//     enough to still be news. The first cause in that order is the summary.
//   - healthy: otherwise, and the word itself is the summary.
//
// Recovered rides in every state: what the run got past by itself is calm
// worth showing beside whatever else is true of it.
func buildVerdict(src Sources, m Model) HealthVerdict {
	recovered := buildRecovered(src, m)
	if !src.Liveness.Alive && m.Lifecycle.Phase != PhaseDone && m.Lifecycle.Phase != PhaseCancelled &&
		m.Lifecycle.Phase != PhaseMerge {
		return HealthVerdict{State: VerdictStopped, Summary: stoppedSummary(src, m), Recovered: recovered}
	}
	if src.Liveness.Alive {
		if cause := degradedCause(src, m); cause != "" {
			return HealthVerdict{State: VerdictDegraded, Summary: cause, Recovered: recovered}
		}
	}
	return HealthVerdict{State: VerdictHealthy, Summary: VerdictHealthy, Recovered: recovered}
}

// stoppedSummary is the stopped headline's why: the probe's own reason first
// — the sentence that says what "not alive" meant — and, when the probe said
// nothing, the dead-run wait's what, which is the same fact in the model's
// own words.
func stoppedSummary(src Sources, m Model) string {
	if src.Liveness.Reason != "" {
		return src.Liveness.Reason
	}
	if m.WaitsOn != nil && m.WaitsOn.Kind == WaitDeadRun {
		return m.WaitsOn.What
	}
	return ""
}

// degradedCause names the first thing that is wrong with a live run, in the
// order a reader should hear it. Empty when nothing is.
func degradedCause(src Sources, m Model) string {
	if len(m.Degraded) > 0 {
		return "degraded: " + strings.Join(m.Degraded, ", ") + " unreadable"
	}
	if nudge := unresolvedNudge(src, m); nudge != nil {
		return "degraded: " + names(nudge) + " nudged as stuck" + ageAgo(src, nudge.At)
	}
	if latestStage(src.Feed, "", reconcile.StageRemoteExhausted) != nil {
		// The reconciler writes this line only when the run stopped rather
		// than retrying past its bound; a live run carrying one is a run
		// whose remote gave out and is going anyway — worth the word.
		return "degraded: the remote exhausted its retries"
	}
	if warned := freshStall(src); warned != nil {
		return "degraded: " + names(warned) + " warned as stalled" + ageAgo(src, warned.At)
	}
	return ""
}

// names is who a per-attempt feed line is about, for a summary that reads.
func names(e *runfeed.Event) string {
	if e.TickID == nil || *e.TickID == "" {
		return "a worker"
	}
	return *e.TickID
}

// unresolvedNudge is the stuck nudge of a live worker that no later line for
// the same attempt resolved — the worker was told it looked stuck, and
// nothing has been heard of it since. Only standing attempts count: a nudge
// of an attempt that no longer stands was resolved by whatever ended it, and
// the first worker in census order is the one named.
func unresolvedNudge(src Sources, m Model) *runfeed.Event {
	if m.Workers == nil {
		return nil
	}
	for _, w := range *m.Workers {
		last := -1
		for i := range src.Feed {
			if src.Feed[i].Stage == reconcile.StageStuckNudged && namesAttempt(src.Feed[i], w.TickID, w.Attempt) {
				last = i
			}
		}
		if last < 0 {
			continue
		}
		resolved := false
		for _, later := range src.Feed[last+1:] {
			if nudgeResolvers[later.Stage] && namesAttempt(later, w.TickID, w.Attempt) {
				resolved = true // this attempt moved after its nudge
				break
			}
		}
		if !resolved {
			return &src.Feed[last]
		}
	}
	return nil
}

// namesAttempt says whether a feed line is about this (tick, attempt).
func namesAttempt(e runfeed.Event, tick string, attempt int) bool {
	return e.TickID != nil && *e.TickID == tick && e.Attempt != nil && *e.Attempt == attempt
}

// freshStall is the newest stall warning young enough to still be news. The
// reconciler writes the line once per tick per incarnation, so the window —
// its own stall threshold, the same fifteen minutes it waits before warning
// at all — is what keeps a warning a run has long since shrugged off from
// reading as degraded forever.
func freshStall(src Sources) *runfeed.Event {
	var freshest *runfeed.Event
	for i := range src.Feed {
		e := src.Feed[i]
		if e.Stage != reconcile.StageStallWarned {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil || src.Now.Sub(at) > reconcile.DefaultStallWarnAfter {
			continue
		}
		freshest = &src.Feed[i]
	}
	return freshest
}

// buildRecovered lists what the run got past by itself, in the order the
// dashboard prints it: the network retries it waited through, the host
// suspensions it slept through, the interventions it resumed across, the
// wall clocks that fired and were answered. Every entry is a count of the
// run's own typed lines — calm, whatever word the verdict states beside it.
func buildRecovered(src Sources, m Model) []Recovery {
	recovered := []Recovery{}
	if m.Health.RemoteRetries > 0 {
		recovered = append(recovered, Recovery{What: "net", Count: m.Health.RemoteRetries})
	}
	var sleep Recovery
	for _, e := range src.Feed {
		if e.Stage != reconcile.StageHostSuspended {
			continue
		}
		sleep.Count++
		if seconds, ok := suspendedFor(e.Detail); ok {
			total := int64(0)
			if sleep.Seconds != nil {
				total = *sleep.Seconds
			}
			total += seconds
			sleep.Seconds = &total
		}
	}
	if sleep.Count > 0 {
		sleep.What = "sleep"
		recovered = append(recovered, sleep)
	}
	if m.Health.Interventions > 0 {
		recovered = append(recovered, Recovery{What: "interventions", Count: m.Health.Interventions})
	}
	if m.Health.WallClocksFired > 0 {
		recovered = append(recovered, Recovery{What: "wall clocks", Count: m.Health.WallClocksFired})
	}
	return recovered
}

// suspendedForAbout is the prefix the reconciler's own host_suspended line
// states its duration with — "the host was suspended for about 1m30s: …" —
// the one place the verdict reads a number out of a line's detail, because
// the span is a fact only that sentence carries.
const suspendedForAbout = "suspended for about "

// suspendedFor reads one host suspension's duration off its line's detail,
// in the reconciler's own wording. False when the detail states none — a
// span nobody stated is not guessed.
func suspendedFor(detail string) (int64, bool) {
	at := strings.Index(detail, suspendedForAbout)
	if at < 0 {
		return 0, false
	}
	rest := detail[at+len(suspendedForAbout):]
	if end := strings.Index(rest, ":"); end >= 0 {
		rest = rest[:end]
	}
	d, err := time.ParseDuration(strings.TrimSpace(rest))
	if err != nil || d < 0 {
		return 0, false
	}
	return int64(d.Round(time.Second).Seconds()), true
}

// ageAgo renders the suffix " <age> ago" from a line's own stamp against the
// model's one clock, compactly the way a headline reads: 40s, 4m, 2h5m.
// Empty when the line carries no parseable stamp — an age nobody measured is
// not guessed at.
func ageAgo(src Sources, stamp string) string {
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	age := src.Now.Sub(at)
	if age < 0 {
		age = 0
	}
	var word string
	switch {
	case age < time.Minute:
		word = fmt.Sprintf("%ds", int64(age.Round(time.Second).Seconds()))
	case age < time.Hour:
		word = fmt.Sprintf("%dm", int64(age.Minutes()))
	default:
		word = fmt.Sprintf("%dh%dm", int64(age.Hours()), int64(age.Minutes())%60)
	}
	return " " + word + " ago"
}
