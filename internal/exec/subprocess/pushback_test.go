package subprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The report check's pushback (tick 4m6): a report that fails the check is
// sent back to the SAME session with the checker's errors, at most
// MaxLintPushbacks times, before the attempt settles — and collect then fails
// a report whose fatal problems remain as missing-result, never a hold.

func pushbacks(observations []Observation) []Observation {
	var out []Observation
	for _, o := range observations {
		if IsNudge(o) && strings.Contains(o.Detail, "pushback") {
			out = append(out, o)
		}
	}
	return out
}

// Before 4m6 this attempt collected with a findings problem and the
// reconciler held the run for a person (finding_report_invalid).
func TestAnUnreadableFindingsBlockIsPushedBackAndAcceptedOnTheCorrectedAnswer(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "findings_fixed_on_pushback"})
	handle := f.Start(f.spec("run-4m6/tick-fix/attempt-1", "fix"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	got := pushbacks(status.Observations)
	if len(got) != 1 || !strings.Contains(got[0].Detail, "pushback 1 of 2") {
		t.Fatalf("%d pushback observations, want exactly one saying so:\n%s", len(got), formatObservations(status.Observations))
	}
	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge {
		t.Fatalf("verdict %s (%s), want %s: the corrected report reads", collected.Verdict, collected.Message, VerdictReadyToMerge)
	}
	if collected.FindingsProblem != "" || len(collected.Findings) != 1 || collected.Findings[0].Title != "a block that reads now" {
		t.Fatalf("findings %v, problem %q: want the corrected answer's one finding", collected.Findings, collected.FindingsProblem)
	}

	// The worker was told what was wrong, where, and how to check it again.
	local, _ := handle.Local()
	seen, err := os.ReadFile(filepath.Join(local.State, "pushback-1.txt"))
	if err != nil {
		t.Fatalf("the pushed-back run left no trace: %v", err)
	}
	for _, want := range []string{"does not pass the report check", "findings block", "not a JSON array",
		"lint-report", local.Worktree} {
		if !strings.Contains(string(seen), want) {
			t.Errorf("the pushback prompt does not carry %q:\n%s", want, seen)
		}
	}
	if strings.Contains(string(seen), lintErrorsPlaceholder) {
		t.Errorf("the pushback prompt still carries the placeholder:\n%s", seen)
	}
}

// A report that stays unreadable is pushed back exactly MaxLintPushbacks
// times and then collects as missing-result: the attempt fails and retries
// like any attempt that never said what it did.
func TestAReportThatStaysUnreadableIsMissingResultAfterThePushbacks(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "findings_bad"})
	handle := f.Start(f.spec("run-4m6/tick-bad/attempt-1", "bad"))
	f.waitSettled(handle)

	status := f.inspect(handle)
	if got := pushbacks(status.Observations); len(got) != MaxLintPushbacks {
		t.Fatalf("%d pushbacks, want exactly %d:\n%s", len(got), MaxLintPushbacks, formatObservations(status.Observations))
	}
	collected := f.collect(handle)
	if collected.Verdict != VerdictMissingResult || collected.Result.Outcome != OutcomeFailed {
		t.Fatalf("verdict %s/%s, want %s/%s", collected.Verdict, collected.Result.Outcome, VerdictMissingResult, OutcomeFailed)
	}
	if !strings.Contains(collected.Message, "fails the report check") || !strings.Contains(collected.Message, "findings block") {
		t.Errorf("the refusal does not say what the check found: %q", collected.Message)
	}
}

// Before 4m6 a review with no REVIEW-VERDICT line collected, and the
// reconciler refused its answer and held the run.
func TestAReviewWithNoVerdictIsPushedBack(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "review_verdict_on_pushback"})
	spec := f.spec("run-4m6/tick-rvw/attempt-1", "rvw")
	spec.Role = "review-epic"
	spec.OutputSchema = "ticfac.job-result.review-epic.v1"
	handle := f.Start(spec)
	f.waitSettled(handle)

	status := f.inspect(handle)
	if got := pushbacks(status.Observations); len(got) != 1 {
		t.Fatalf("%d pushbacks, want one:\n%s", len(got), formatObservations(status.Observations))
	}
	collected := f.collect(handle)
	if collected.Verdict != VerdictReadyToMerge || collected.Report.ReviewVerdict != ReviewVerdictReady {
		t.Fatalf("verdict %s, review verdict %q: want the corrected answer's READY", collected.Verdict, collected.Report.ReviewVerdict)
	}
	local, _ := handle.Local()
	seen, err := os.ReadFile(filepath.Join(local.State, "pushback-1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seen), "REVIEW-VERDICT") {
		t.Errorf("the pushback does not name the missing line:\n%s", seen)
	}
}

// A clean report is never pushed back.
func TestACleanReportIsNotPushedBack(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	handle := f.Start(f.spec("run-4m6/tick-cln/attempt-1", "cln"))
	f.waitSettled(handle)
	if got := pushbacks(f.inspect(handle).Observations); len(got) != 0 {
		t.Fatalf("a clean report was pushed back: %v", got)
	}
}

// The prompt names the check, with this job's own role, tick and worktree,
// and states the v2 findings shape.
func TestThePromptTellsTheWorkerToRunTheReportCheck(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "echo_prompt"})
	handle := f.Start(f.spec("run-4m6/tick-pmt/attempt-1", "pmt"))
	f.waitSettled(handle)
	local, _ := handle.Local()
	seen, err := os.ReadFile(filepath.Join(local.Worktree, "prompt-seen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{executorBin + " lint-report", "--role implement-tick", "--tick pmt",
		"Check your report before you stop", "```findings v2"} {
		if !strings.Contains(string(seen), want) {
			t.Errorf("the prompt does not carry %q", want)
		}
	}
}
