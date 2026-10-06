package refsweep

import (
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// short: pure name parsing, no git.
func TestClassifyOwnsOnlyTicfacNames(t *testing.T) {
	t.Parallel()
	owned := map[string]Owned{
		"refs/heads/ticfac/run-epic-2jn/tick-a1/attempt-1":                   {Family: FamilyRunBranch, Run: "epic-2jn"},
		"refs/heads/ticfac/run-run_5c7c/tick-a1/attempt-2":                   {Family: FamilyRunBranch, Run: "run_5c7c"},
		"refs/ticfac/start/run-run_5c7c/tick-a1/0123abcd":                    {Family: FamilyStart, Run: "run_5c7c"},
		"refs/ticfac/wip/run-epic-2jn/tick-a1/attempt-1":                     {Family: FamilyWip, Run: "epic-2jn"},
		"refs/heads/tick/hn6/attempt-1/r5i":                                  {Family: FamilyLanding, Epic: "hn6"},
		"refs/heads/tick/hn6/attempt-1-resolve-1-868ca692/378":               {Family: FamilyLanding, Epic: "hn6"},
		"refs/heads/tick/hn6/attempt-3/378-boot-stopped":                     {Family: FamilyBootMarker, Epic: "hn6"},
		"refs/heads/ticfac/run-run_5c7c/tick-a1/attempt-2-boot-stopped":      {Family: FamilyBootMarker, Run: "run_5c7c"},
		"refs/heads/tick/hn6/attempt-1/r5i-run_0f64adbcecb5484fb20cbe33d5e9": {Family: FamilyLanding, Epic: "hn6"},
	}
	for ref, want := range owned {
		got, ok := Classify(ref)
		if !ok || got != want {
			t.Errorf("Classify(%q) = %+v, %v; want %+v", ref, got, ok, want)
		}
	}
	for _, ref := range []string{
		"refs/heads/main", "refs/heads/epic/hn6", "refs/tags/ticfac/run-epic-hn6",
		"refs/heads/tick/7mj", "refs/heads/tick/plw/x", "refs/heads/tick/hn6/attemptx/a",
		"refs/heads/ticfac/run-epic-2jn", "refs/heads/ticfac/runner/x", "refs/heads/fix/sweep-boot-stopped",
		"refs/heads/tick-run/hn6", "refs/ticfac/start/run-x", "refs/heads/umq/v1d-start-watch",
	} {
		if got, ok := Classify(ref); ok {
			t.Errorf("Classify(%q) owns it as %+v; it is not ticfac's", ref, got)
		}
	}
}

func verdictsByName(vs []Verdict) map[string]Verdict {
	m := map[string]Verdict{}
	for _, v := range vs {
		m[v.Name] = v
	}
	return m
}

// short: the plan is pure; this is a table over it.
func TestPlanRetiresOnlyEndedRuns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	closedAt := now.Add(-20 * 24 * time.Hour)
	merged := map[string]bool{"m": true}
	in := Inputs{
		Now: now,
		Runs: map[string]Run{
			"epic-done": {ID: "epic-done", Epic: "done", State: runstate.StateCompleted, UpdatedAt: now.Add(-48 * time.Hour)},
			"epic-shut": {ID: "epic-shut", Epic: "shut", State: runstate.StateFailed, UpdatedAt: closedAt},
			"epic-stop": {ID: "epic-stop", Epic: "stop", State: runstate.StateFailed, UpdatedAt: now.Add(-72 * time.Hour)},
			"run_old":   {ID: "run_old", Epic: "sup", State: runstate.StateRunning, UpdatedAt: now.Add(-72 * time.Hour)},
			"run_new":   {ID: "run_new", Epic: "sup", State: runstate.StateFailed, UpdatedAt: now.Add(-24 * time.Hour)},
			"run_warm":  {ID: "run_warm", Epic: "warm", State: runstate.StateRunning, UpdatedAt: now.Add(-time.Hour)},
			"run_warm2": {ID: "run_warm2", Epic: "warm", State: runstate.StateRunning, UpdatedAt: now.Add(-30 * time.Minute)},
		},
		Epics: map[string]Epic{
			"shut": {Known: true, Closed: true, ClosedAt: closedAt},
		},
		Merged: func(sha, _ string) bool { return merged[sha] },
		Refs: []Ref{
			{"refs/heads/ticfac/run-epic-done/tick-a/attempt-1", "m"},
			{"refs/heads/ticfac/run-epic-done/tick-b/attempt-1", "u"},
			{"refs/heads/ticfac/run-epic-shut/tick-a/attempt-1", "u"},
			{"refs/heads/ticfac/run-epic-stop/tick-a/attempt-1", "m"},
			{"refs/heads/ticfac/run-run_old/tick-a/attempt-1", "m"},
			{"refs/heads/ticfac/run-run_new/tick-a/attempt-2", "m"},
			{"refs/heads/ticfac/run-run_warm/tick-a/attempt-1", "m"},
			{"refs/heads/ticfac/run-run_nobody/tick-a/attempt-1", "m"},
			{"refs/heads/tick/sup/attempt-1/a", "m"},
			{"refs/heads/tick/done/attempt-1/a", "m"},
			{"refs/heads/tick/done/attempt-1/b-boot-stopped", "u"},
			{"refs/heads/main", "m"},
			{"refs/heads/someones-branch", "m"},
		},
	}
	got := verdictsByName(Plan(in))
	want := map[string]bool{
		"refs/heads/ticfac/run-epic-done/tick-a/attempt-1":  true,  // completed, merged
		"refs/heads/ticfac/run-epic-done/tick-b/attempt-1":  false, // completed, unmerged, epic open
		"refs/heads/ticfac/run-epic-shut/tick-a/attempt-1":  true,  // closed 20d ago: past grace
		"refs/heads/ticfac/run-epic-stop/tick-a/attempt-1":  false, // stopped, resumable
		"refs/heads/ticfac/run-run_old/tick-a/attempt-1":    true,  // superseded
		"refs/heads/ticfac/run-run_new/tick-a/attempt-2":    false, // the newest run
		"refs/heads/ticfac/run-run_warm/tick-a/attempt-1":   false, // superseded but warm
		"refs/heads/ticfac/run-run_nobody/tick-a/attempt-1": false, // no checkpoint
		"refs/heads/tick/sup/attempt-1/a":                   false, // run_new may resume
		"refs/heads/tick/done/attempt-1/a":                  true,
		"refs/heads/tick/done/attempt-1/b-boot-stopped":     true,
	}
	if len(got) != len(want) {
		t.Errorf("the plan judged %d refs, want %d (main and a person's branch get no verdict): %+v", len(got), len(want), got)
	}
	for ref, del := range want {
		v, ok := got[ref]
		if !ok {
			t.Errorf("no verdict for %s", ref)
			continue
		}
		if v.Delete != del {
			t.Errorf("%s: delete=%v, want %v (%s)", ref, v.Delete, del, v.Why)
		}
	}

	// The grace: the same closed epic 13 days after its close keeps the
	// unmerged branch, and says until when.
	in.Epics["shut"] = Epic{Known: true, Closed: true, ClosedAt: now.Add(-13 * 24 * time.Hour)}
	v := verdictsByName(Plan(in))["refs/heads/ticfac/run-epic-shut/tick-a/attempt-1"]
	if v.Delete || !strings.Contains(v.Why, "kept until") {
		t.Errorf("an unmerged branch inside the grace was not kept: %+v", v)
	}
}
