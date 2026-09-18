package herdr

import (
	"strings"
	"testing"
)

// Every herdr workspace label identifies the ATTEMPT, not just the tick
// (ticfac tick 55i): everything else about an attempt is attempt-scoped —
// the branch, the worktree path, the state directory, the agent's own name —
// but the workspace label was the tick id alone, so attempt 2 and attempt 3
// of one tick appeared in an operator's list as two workspaces with the same
// name. The one an operator would reasonably close is the one holding the
// only copy of the work.
//
// The label follows the agent's own name convention — "tick-<id>-a<n>" — so
// the sidebar's workspace and its agent read as one attempt.

func TestTheWorkspaceLabelIdentifiesTheAttempt(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}
	h.ex.opts.Attempt = 2
	if _, err := h.start("t1"); err != nil {
		t.Fatal(err)
	}

	h.mu.Lock()
	labels := make([]string, 0, len(h.workspaces))
	for _, ws := range h.workspaces {
		labels = append(labels, ws.label)
	}
	h.mu.Unlock()
	if len(labels) != 2 {
		t.Fatalf("the harness holds %d workspaces, want one per attempt: %v", len(labels), labels)
	}
	seen := map[string]bool{}
	for _, label := range labels {
		seen[label] = true
		if !strings.HasPrefix(label, "tick-t1-a") {
			t.Errorf("the workspace label %q does not identify the tick AND the attempt", label)
		}
	}
	if seen["tick-t1-a1"] && seen["tick-t1-a2"] {
		return
	}
	t.Errorf("the two attempts of one tick are not distinguishable in an operator's list: %v", labels)
}

// The attempt-scoped label must not cost the reclaimer its weakest evidence:
// a label "tick-<id>-a<n>" still says which TICK the workspace is about, so
// a workspace attributed only by its label reports the tick, never the
// attempt-suffixed string.
func TestTickOfFactsReadsTheAttemptScopedLabelAsTheTick(t *testing.T) {
	for label, want := range map[string]string{
		"tick-t1-a2":   "t1",
		"tick-55i-a11": "55i",
		"tick-x":       "x",
	} {
		if got, _ := tickOfFacts("", "", label); got != want {
			t.Errorf("tickOfFacts(label %q) = %q, want the tick %q", label, got, want)
		}
	}
}
