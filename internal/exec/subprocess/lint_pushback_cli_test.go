package subprocess

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// The cloud worker's half of the report pushback (hn6 run_d51a, u5n): the
// container's entrypoint (image/worker.sh) has no Go supervisor to render the
// pushback prompt, so `lint-report --pushback` prints the very prompt the
// local supervisor sends (LintPushbackPrompt) when the report fails, and the
// script hands that text to the harness's own session. One text, one place.
//
// short: one temporary directory, no processes.
func TestTheLintReportCommandPrintsThePushbackPromptWhenAsked(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	report := filepath.Join(repo, "RESULT-u5n.md")
	// The u5n shape: a report with work described and no STATUS line.
	mustWrite(t, report, "# RESULT — tick u5n\n\n## Status\n\nRenderer + tests are written in this workspace.\n")

	var out, errOut bytes.Buffer
	code := Main([]string{"lint-report", report, "--pushback", "--role", "implement-tick", "--tick", "u5n", "--repo", repo},
		nil, &out, &errOut)
	if code != ExitError {
		t.Fatalf("exit %d on a report with no STATUS line, want %d:\n%s\n%s", code, ExitError, out.String(), errOut.String())
	}
	text := out.String()
	for _, want := range []string{
		"Your report at " + report + " does not pass the report check",
		"lint-report " + report + " --role implement-tick --tick u5n --repo " + repo,
		"error: STATUS: the report has no STATUS line",
		"keeping its final STATUS line",
		HeadlessLine,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the pushback prompt does not carry %q:\n%s", want, text)
		}
	}

	// A clean report prints the ok line and exits 0, flag or not: the script
	// reads the exit status, never the text, to decide.
	mustWrite(t, report, "# RESULT\n\n```findings v2\n[]\n```\n\nSTATUS: DONE\n")
	out.Reset()
	if code := Main([]string{"lint-report", "--pushback", report, "--repo", repo}, nil, &out, &errOut); code != ExitOK {
		t.Fatalf("exit %d on a clean report:\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "does not pass") {
		t.Errorf("a clean report printed a pushback:\n%s", out.String())
	}
}
