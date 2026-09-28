package statusmodel

import (
	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The health verdict (epic hn6, wave 1 — tick r5i): the headline a dashboard
// answers "is it healthy" with — a word a person reads, not four counters
// they have to interpret (hn6 rule 3: health as a verdict, raw counts behind
// a key). Wave 1 DECLARES it: the counts below still count the run's own
// typed lines exactly as they always did, and the verdict itself states
// healthy with nothing recovered and nothing to say. This file is where the
// wave-2 verdict tick grows the real derivation from the counts, the waits
// and the run's own words; raw counts are a READER's drill-in, never the
// headline.

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

// buildVerdict is the wave-1 verdict: healthy, nothing to say, nothing
// recovered. It is called after buildWaits because the real verdict the
// wave-2 tick derives reads the waits (a run holding something for a person
// is not "healthy" however many retries it shrugged off) — the call site is
// already in place, so the fill changes this function and nothing else.
func buildVerdict(src Sources, m Model) HealthVerdict {
	return HealthVerdict{
		State:     VerdictHealthy,
		Summary:   "",
		Recovered: []Recovery{},
	}
}
