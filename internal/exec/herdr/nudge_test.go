package herdr

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// epic-2jn vqc (2026-09-27), the herdr shape of it: a worker commits, starts
// the gate in the background and ends its turn to wait for the notification.
// Nobody delivers one to a turn that has ended, so the agent sits idle with
// no report — read as `running` until the wall clock stops it. An idle agent
// with no report is re-prompted in its own pane now (nudge.go), at most
// subprocess.MaxNudges times inside the wall clock, and only then judged.

// nudgeHarness is a harness whose idle grace is short enough for a test.
func nudgeHarness(t *testing.T, mode string) *harness {
	h := newHarness(t, harnessOptions{spawnAgent: true, agentMode: mode, kind: "claude"})
	h.ex.opts.IdleGrace = 300 * time.Millisecond
	return h
}

// pollUntilTerminal inspects the attempt the way the reconciler does, one
// poll at a time, until it settles; the observations of every poll are kept.
func pollUntilTerminal(t *testing.T, h *harness, handle *subprocess.JobHandle, within time.Duration) (*subprocess.JobStatus, []subprocess.Observation) {
	t.Helper()
	var seen []subprocess.Observation
	cursor := ""
	deadline := time.Now().Add(within)
	for {
		status, err := h.ex.Inspect(handle, cursor)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, status.Observations...)
		if status.Terminal {
			return status, seen
		}
		if status.Cursor != nil {
			cursor = *status.Cursor
		}
		if time.Now().After(deadline) {
			h.dumpAgent(t)
			t.Fatalf("the attempt did not settle within %s; last state %s:\n%s", within, status.State, formatObs(seen))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func nudgesIn(observations []subprocess.Observation) []subprocess.Observation {
	var out []subprocess.Observation
	for _, o := range observations {
		if subprocess.IsNudge(o) {
			out = append(out, o)
		}
	}
	return out
}

func formatObs(observations []subprocess.Observation) string {
	var b strings.Builder
	for _, o := range observations {
		b.WriteString("  " + o.Kind + ": " + o.Detail + "\n")
	}
	return b.String()
}

func TestAnIdleAgentWithNoReportIsNudgedInItsPaneAndFinishes(t *testing.T) {
	shorttest.EndToEnd(t)
	h := nudgeHarness(t, "stop_early")
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	status, seen := pollUntilTerminal(t, h, handle, 30*time.Second)
	if status.State != subprocess.StateSucceeded {
		t.Fatalf("state %s, want succeeded: an agent that ended its turn early was never re-prompted\n%s",
			status.State, formatObs(seen))
	}
	nudges := nudgesIn(seen)
	if len(nudges) != 1 || !strings.Contains(nudges[0].Detail, "nudge 1 of 2") {
		t.Fatalf("%d nudges, want exactly one saying so:\n%s", len(nudges), formatObs(seen))
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge {
		t.Errorf("verdict %s (%s)", collected.Verdict, collected.Message)
	}
}

func TestAnAgentThatNeverReportsIsMissingResultAfterTheNudgesAreSpent(t *testing.T) {
	shorttest.EndToEnd(t)
	h := nudgeHarness(t, "never_report")
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	status, seen := pollUntilTerminal(t, h, handle, 30*time.Second)
	if status.State != subprocess.StateFailed {
		t.Fatalf("state %s, want failed once the nudges are spent\n%s", status.State, formatObs(seen))
	}
	if got := nudgesIn(seen); len(got) != subprocess.MaxNudges {
		t.Fatalf("%d nudges, want exactly %d:\n%s", len(got), subprocess.MaxNudges, formatObs(seen))
	}
	raw, _ := os.ReadFile(h.promptFile + ".nudges")
	if strings.TrimSpace(string(raw)) != "2" {
		t.Errorf("the agent received %q nudges in its pane, want 2", strings.TrimSpace(string(raw)))
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect refused a settled attempt: %v", err)
	}
	if collected.Verdict != subprocess.VerdictMissingResult || collected.Result.Outcome != subprocess.OutcomeFailed {
		t.Errorf("verdict %s/%s, want %s/%s", collected.Verdict, collected.Result.Outcome,
			subprocess.VerdictMissingResult, subprocess.OutcomeFailed)
	}
	// A later poll reads the same settlement, and nudges nothing more.
	again, err := h.ex.Inspect(handle, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != subprocess.StateFailed {
		t.Errorf("a later inspect reads %s", again.State)
	}
	if got := nudgesIn(again.Observations); len(got) != subprocess.MaxNudges {
		t.Errorf("a later inspect's stream holds %d nudges, want %d", len(got), subprocess.MaxNudges)
	}
}

// An agent that is WORKING is never nudged, however long it works.
func TestAWorkingAgentIsNotNudged(t *testing.T) {
	shorttest.EndToEnd(t)
	h := nudgeHarness(t, "sleep")
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	waitForOr(t, "the agent to report working", 10*time.Second, func() bool { return h.currentStatus() == "working" })
	for i := 0; i < 8; i++ {
		status, err := h.ex.Inspect(handle, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := nudgesIn(status.Observations); len(got) != 0 {
			t.Fatalf("a working agent was nudged: %v", got)
		}
		if status.State != subprocess.StateRunning {
			t.Fatalf("state %s", status.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// short: renders a prompt in memory; no herdr server and no agent
func TestTheHerdrPromptSaysEndingTheTurnEndsTheJob(t *testing.T) {
	t.Parallel()
	prompt := renderWorkerPrompt(&attemptRecord{TickID: "abc", Branch: "b", BaseSHA: "0123"},
		&subprocess.JobSpec{ArtifactPrefix: "runs/r/abc"})
	if !strings.Contains(prompt, subprocess.HeadlessLine) {
		t.Errorf("the herdr worker prompt does not say ending the turn ends the job:\n%s", prompt)
	}
}
