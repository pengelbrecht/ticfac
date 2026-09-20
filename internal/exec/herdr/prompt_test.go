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
// short: prompt text assembled from values in memory
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

// TestTheHerdrPromptPointsAtPreservedWorkToo (tick pbb).
//
// The same contract for the preserved work: a herdr worker whose prompt
// pointed at a stopped predecessor's snapshot differently from a local one
// — or dropped it — would be two answers to the same question, and the
// herdr substrate is the one that runs the longest attempts, so it is the
// one whose stops carry the most uncommitted work.
// short: prompt text assembled from values in memory
func TestTheHerdrPromptPointsAtPreservedWorkToo(t *testing.T) {
	t.Parallel()

	prior := []subprocess.PriorSnapshot{{
		Attempt: 1, Ref: "refs/ticfac/wip/run-r/tick-abc/attempt-1",
		Commit: "0123456789abcdef0123456789abcdef01234567",
	}}
	record := &attemptRecord{
		TickID: "abc", Branch: "ticfac/run-r/tick-abc/attempt-2",
		BaseSHA:        "0123456789abcdef0123456789abcdef01234567",
		Worktree:       "/tmp/worktree",
		PriorSnapshots: prior,
	}
	prompt := renderWorkerPrompt(record, &subprocess.JobSpec{Role: "implement-tick", ArtifactPrefix: "runs/r/abc"})

	if !strings.Contains(prompt, subprocess.PriorSnapshotsSection(prior)) {
		t.Errorf("the herdr prompt does not carry the preserved-work section the local executor renders:\n%s", prompt)
	}
	if !strings.Contains(prompt, "never to merge") {
		t.Errorf("the herdr prompt does not frame the snapshot as material:\n%s", prompt)
	}

	// And a first attempt carries no such section.
	first := renderWorkerPrompt(&attemptRecord{TickID: "abc", Branch: "b", BaseSHA: "0123"},
		&subprocess.JobSpec{ArtifactPrefix: "runs/r/abc"})
	if strings.Contains(first, "preserved work") {
		t.Errorf("a first attempt's herdr prompt carries a preserved-work section:\n%s", first)
	}
}
