package herdr

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The report check's pushback, herdr's half (tick 4m6): a report that fails
// the check is typed back into the agent's own pane with the checker's
// errors, at most subprocess.MaxLintPushbacks times, before it settles.

func pushbacksIn(observations []subprocess.Observation) []subprocess.Observation {
	var out []subprocess.Observation
	for _, o := range observations {
		if subprocess.IsNudge(o) && strings.Contains(o.Detail, "pushback") {
			out = append(out, o)
		}
	}
	return out
}

// Before 4m6 this attempt settled on its unreadable findings block and the
// reconciler held the run for a person.
func TestAnUnreadableReportIsPushedBackInThePaneAndAcceptedWhenFixed(t *testing.T) {
	shorttest.EndToEnd(t)
	h := nudgeHarness(t, "bad_findings_then_fixed")
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	status, seen := pollUntilTerminal(t, h, handle, 30*time.Second)
	if status.State != subprocess.StateSucceeded {
		t.Fatalf("state %s\n%s", status.State, formatObs(seen))
	}
	if got := pushbacksIn(seen); len(got) != 1 || !strings.Contains(got[0].Detail, "pushback 1 of 2") {
		t.Fatalf("%d pushbacks, want exactly one saying so:\n%s", len(got), formatObs(seen))
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatal(err)
	}
	if collected.Verdict != subprocess.VerdictReadyToMerge || len(collected.Findings) != 1 || collected.Findings[0].Title != "fixed" {
		t.Fatalf("verdict %s (%s), findings %v: want the corrected answer", collected.Verdict, collected.Message, collected.Findings)
	}
}

// A report that stays unreadable is pushed back twice and then collects as
// missing-result: failed and retried, never a hold.
func TestAReportThatStaysUnreadableIsMissingResultAfterThePanePushbacks(t *testing.T) {
	shorttest.EndToEnd(t)
	h := nudgeHarness(t, "bad_findings_forever")
	handle, err := h.start("t1")
	if err != nil {
		t.Fatal(err)
	}
	status, seen := pollUntilTerminal(t, h, handle, 30*time.Second)
	if got := pushbacksIn(seen); len(got) != subprocess.MaxLintPushbacks {
		t.Fatalf("%d pushbacks, want %d (state %s):\n%s", len(got), subprocess.MaxLintPushbacks, status.State, formatObs(seen))
	}
	raw, _ := os.ReadFile(h.promptFile + ".pushbacks")
	if strings.TrimSpace(string(raw)) != "2" {
		t.Errorf("the agent received %q pushbacks in its pane, want 2", strings.TrimSpace(string(raw)))
	}
	collected, err := h.ex.CollectDetail(handle)
	if err != nil {
		t.Fatalf("collect refused a settled attempt: %v", err)
	}
	if collected.Verdict != subprocess.VerdictMissingResult || collected.Result.Outcome != subprocess.OutcomeFailed ||
		!strings.Contains(collected.Message, "fails the report check") {
		t.Errorf("verdict %s/%s (%s), want missing-result/failed naming the check", collected.Verdict,
			collected.Result.Outcome, collected.Message)
	}
}

// short: renders a prompt in memory; no herdr server and no agent
func TestTheHerdrPromptNamesTheReportCheck(t *testing.T) {
	t.Parallel()
	prompt := renderWorkerPrompt(&attemptRecord{TickID: "abc", Branch: "b", BaseSHA: "0123", ResultPath: "/w/RESULT-abc.md", Worktree: "/w"},
		&subprocess.JobSpec{Role: "implement-tick", ArtifactPrefix: "runs/r/abc"})
	for _, want := range []string{"lint-report /w/RESULT-abc.md --role implement-tick --tick abc --repo /w",
		"```findings v2"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the herdr prompt does not carry %q:\n%s", want, prompt)
		}
	}
}
