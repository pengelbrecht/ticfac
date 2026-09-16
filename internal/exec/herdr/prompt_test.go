package herdr

import (
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// TestTheHerdrPromptNamesThePredecessorsToo (tick nvn).
//
// The worker prompt is one job contract however the agent is delivered, and
// the section that names what a tick's earlier attempts found is part of it:
// a herdr worker whose prompt framed its predecessors differently from a
// local one — or dropped them — would be two answers to the same question,
// and the re-dispatches this tick exists for would start blind on exactly
// the substrate that runs the longest attempts.
func TestTheHerdrPromptNamesThePredecessorsToo(t *testing.T) {
	t.Parallel()

	prior := []subprocess.PriorReport{{
		Attempt: 1, Path: "/state/r-fixture/abc/1/repo/aa/report.md",
		Status: subprocess.StatusBlocked, Detail: "the gate config names no runner",
	}}
	record := &attemptRecord{
		TickID:       "abc",
		Branch:       "ticfac/run-r/tick-abc/attempt-2",
		BaseSHA:      "0123456789abcdef0123456789abcdef01234567",
		Worktree:     "/tmp/worktree",
		PriorReports: prior,
	}
	prompt := renderWorkerPrompt(record, &subprocess.JobSpec{Role: "implement-tick", ArtifactPrefix: "runs/r/abc"})

	if !strings.Contains(prompt, subprocess.PriorReportsSection(prior)) {
		t.Errorf("the herdr prompt does not carry the prior-reports section the local executor renders:\n%s", prompt)
	}

	// And a first attempt — the section that must NOT appear when there is
	// nothing to inherit, so the header never teaches a worker to skip it.
	first := renderWorkerPrompt(&attemptRecord{TickID: "abc", Branch: "b", BaseSHA: "0123"},
		&subprocess.JobSpec{ArtifactPrefix: "runs/r/abc"})
	if strings.Contains(first, "Prior attempts") {
		t.Errorf("a first attempt's herdr prompt carries a Prior attempts section:\n%s", first)
	}
}
