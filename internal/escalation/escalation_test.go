package escalation

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The fixture feed is one run in miniature, its lines in the reconciler's own
// shapes: a1 closes on its first tier; a2 climbs after the worker answered
// BLOCKED (model — though its prose names a gateway and a boot), and a
// resolve-conflict job collected under its try-2 number does not hide it; a3 climbs
// after a push that never landed (infrastructure); a4's boot failed on the
// gateway and it was redispatched at the SAME tier (no climb, a first-tier
// success); a5 climbs after a merge conflict (other) and is still open; a6 was
// picked up at try 3 from an earlier run; rv is a review job at its base
// values; a7's second dispatch is a resolve-conflict JOB, not a climb; a8
// climbs after an unreadable report (model); a9 after a boot that stopped on
// exit 15 (infrastructure).
func TestASummaryFromAFixtureFeed(t *testing.T) {
	events, err := runfeed.Read("testdata/feed.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got := FromFeed(events)
	want := Summary{
		Dispatched: 9, Started: 8, Escalated: 5,
		Model: 2, Infrastructure: 2, Other: 1,
		FirstTier: 3, Open: 1,
	}
	if got != want {
		t.Fatalf("FromFeed = %+v\nwant        %+v", got, want)
	}
	line := got.Line()
	for _, part := range []string{
		"8 implementation tick(s) started here (+1 picked up past their first try)",
		"5 needed a higher tier (2 model, 2 infrastructure, 1 other)",
		"3 of 8 succeeded on their first tier (38%)",
		"1 not closed",
	} {
		if !strings.Contains(line, part) {
			t.Errorf("the line %q does not say %q", line, part)
		}
	}
}

func TestAnEmptyFeedSaysNothingWasDispatched(t *testing.T) {
	s := FromFeed(nil)
	if s != (Summary{}) || s.FirstTierShare() != -1 {
		t.Fatalf("an empty feed summarised as %+v", s)
	}
	if !strings.Contains(s.Line(), "no implementation tick dispatched") {
		t.Errorf("the empty line reads %q", s.Line())
	}
}

// A retry at the ceiling is not a climb: the local ladder's second failure at
// frontier re-runs at frontier.
func TestARetryAtTheSameTierIsNotAnEscalation(t *testing.T) {
	tick := "c1"
	one, two := 1, 2
	events := []runfeed.Event{
		{TickID: &tick, Stage: "tier_derived", Detail: `c1 try 1 (run dispatch #1) runs at tier "frontier" (x)`},
		{TickID: &tick, Attempt: &one, Stage: "rejected", Detail: "no-commits: nothing"},
		{TickID: &tick, Attempt: &one, Stage: "tier_derived", Detail: `c1 try 2 (run dispatch #2) runs at tier "frontier" (x)`},
		{TickID: &tick, Attempt: &two, Stage: "closed", Detail: "closed"},
	}
	s := FromFeed(events)
	if s.Escalated != 0 || s.FirstTier != 1 || s.Started != 1 {
		t.Fatalf("a same-tier retry summarised as %+v", s)
	}
}

// The stages this package reads are the reconciler's own spellings.
func TestTheStagesAreTheReconcilersOwn(t *testing.T) {
	for mine, theirs := range map[string]string{
		stageTierDerived:     reconcile.StageTierDerived,
		stageCollected:       reconcile.StageCollected,
		stageRejected:        reconcile.StageRejected,
		stageInfraRedispatch: reconcile.StageInfrastructureRedispatched,
		stageGateFailed:      reconcile.StageGateFailed,
		stageWallClock:       reconcile.StageWallClock,
		stageClosed:          reconcile.StageClosed,
		stageClosedCarrying:  reconcile.StageClosedCarrying,
	} {
		if mine != theirs {
			t.Errorf("escalation reads stage %q where the reconciler writes %q", mine, theirs)
		}
	}
	if got := reconcile.AttemptLabel("t", 2, 7) + ` runs at tier "strong"`; !tierDerivedPattern.MatchString(got) {
		t.Errorf("the tier_derived head %q no longer parses", got)
	}
}
