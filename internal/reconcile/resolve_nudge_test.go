package reconcile

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// epic-2jn, 2026-09-27 19:27Z: vqc's resolve-conflict job (claude, print
// mode) committed its resolution, started the gate as a background task and
// ended its turn to "wait for the completion notification". Print mode ended
// the process: exit 0, no report. The job was judged missing-result, the
// merge failed and the run halted with the resolution already committed.
//
// The executor now prompts a runner that exits 0 without its report again
// before it is judged (subprocess/nudge.go), so the same worker finishes and
// the run completes — and the nudge is on the feed.
//
// serial: the conflict fixture states the process environment for the fake
// runner's workers (conflictSync), and t.Setenv forbids a parallel test.
func TestAResolveJobThatEndsItsTurnEarlyIsNudgedAndTheRunCompletes(t *testing.T) {
	conflictSync(t)
	opts := fixtureOptions{mode: "conflict_resolve_stops_early", gate: resolveGate}
	f := newFixture(t, opts)
	seedSharedFile(t, f)

	r, result, err := f.run(f.Repo, opts)
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%s): a resolve job that ended its turn before its report was judged "+
			"on that exit instead of being prompted to finish", result.State, result.Reason)
	}

	nudged := false
	for _, event := range r.Journal() {
		if event.Stage == StageWaiting && strings.Contains(event.Detail, "nudge 1 of 2") {
			nudged = true
			if event.Tick != "a2" {
				t.Errorf("the nudge was announced for %s, not the tick whose resolve job it was", event.Tick)
			}
		}
	}
	if !nudged {
		t.Error("the feed never said the resolve job was nudged")
	}
}
