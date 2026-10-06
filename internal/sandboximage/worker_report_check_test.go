//go:build !windows

package sandboximage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The report check, in the cloud worker (hn6 run_d51a747f, u5n).
//
// PR #105 (tick 4m6) gave every executor one report checker and pushed a
// failing report back to the agent's own session — on the local subprocess
// supervisor and in herdr's pane. The cloud worker got only the prompt line
// telling the agent to run the checker itself. u5n's economy-tier pi worker
// did not, finished with a report that carried no STATUS line, and the
// container pushed it as it was: collect read "missing-result: … the worker
// finished and left an answer nobody can read", rejected the attempt and
// spent a rung on a report the agent could have fixed in one turn.
//
// So the container runs the same checker after the harness exits with its
// report written, and a report that fails it is pushed back to the SAME
// session with the checker's own words, at most WorkerReportPushbackMax
// times — the local supervisor's bound. Only then is the report pushed and
// collect decides.

// reportCheckerBinary is the checker the image ships beside ticfac.
const reportCheckerBinary = "ticfac-exec-subprocess"

// WorkerReportPushbackMax is the container's bound; it is the local
// supervisor's, because the same report must get the same number of chances
// on every substrate.
const WorkerReportPushbackMax = subprocess.MaxLintPushbacks

// short: compares two constants; no process runs
func TestTheWorkerRunsTheCheckerTheImageShips(t *testing.T) {
	if reportCheckerBinary != subprocess.LintCommandName {
		t.Fatalf("the worker runs %q, the image ships the checker as %q", reportCheckerBinary, subprocess.LintCommandName)
	}
}

// pushbackStub is a stand-in agent for the report check: every invocation is
// appended to TICKS_TEST_NUDGE_RUNS, it commits work on its first turn, and it
// writes the report it is told to — a report with no STATUS line on its first
// turn (the u5n shape), and a fixed one only when the turn it is given is the
// checker's pushback and TICKS_TEST_FIX_ON_PUSHBACK is set.
const pushbackStub = harnessStubPreamble + `{
  printf 'RUN\n'
  printf '%s\n' "$*"
} >> "$TICKS_TEST_NUDGE_RUNS"
{
  printf 'CWD=%s\n' "$PWD"
  for a in "$@"; do printf 'ARG=%s\n' "$a"; done
} > "$TICKS_TEST_RECORD"
case "$*" in
*"does not pass the report check"*|*"This job already ran once on this checkout"*)
  if [ -n "${TICKS_TEST_FIX_ON_PUSHBACK:-}" ]; then
    printf '# %s\n\nRenderer and tests are committed.\n\nSTATUS: DONE\n' "${TICKS_TICK}" > "RESULT-${TICKS_TICK}.md"
  fi
  ;;
*)
  printf 'work\n' > worked.txt
  git add worked.txt
  git commit -q -m "tick ${TICKS_TICK}: the work"
  printf '# RESULT — tick %s\n\n## Status\n\nRenderer and tests are written in this workspace.\n' "${TICKS_TICK}" > "RESULT-${TICKS_TICK}.md"
  ;;
esac
exit 0
`

func newPushbackFixture(t *testing.T, harness string) (*workerFixture, string) {
	t.Helper()
	f := newWorkerFixture(t)
	f.env[EnvHarness] = harness
	runsPath := filepath.Join(f.root, "harness-runs")
	f.env["TICKS_TEST_NUDGE_RUNS"] = runsPath
	f.env["TICKS_TEST_LINT_RECORD"] = filepath.Join(f.root, "lint-record")
	writeStub(t, filepath.Join(f.binDir, harness), pushbackStub)
	return f, runsPath
}

func (f *workerFixture) lintCalls() []string {
	f.t.Helper()
	b, err := os.ReadFile(f.env["TICKS_TEST_LINT_RECORD"])
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// THE u5n CASE: the harness exits 0 with its work committed and a report that
// carries no STATUS line. The container pushes the report back to the agent's
// own session with the checker's words, the agent fixes the report, and what
// reaches origin is a report collect can read.
func TestWorkerPushesAReportWithNoStatusLineBackToItsOwnSession(t *testing.T) {
	shorttest.EndToEnd(t) // its fixtures are built inside subtests
	for _, harness := range []string{"claude", "omp"} {
		t.Run(harness, func(t *testing.T) {
			f, runsPath := newPushbackFixture(t, harness)
			f.env["TICKS_TEST_FIX_ON_PUSHBACK"] = "1"

			out, code := f.run()
			if code != 0 {
				t.Fatalf("a worker that fixed its report when pushed back gave exit %d:\n%s", code, out)
			}
			mustContain(t, out, "pushback 1 of 2", "the container's pushback of the failing report")
			runs := runBlocks(t, runsPath)
			if len(runs) != 2 {
				t.Fatalf("%d harness run(s), want 2 (the work and one pushback):\n%s", len(runs), out)
			}
			// The pushback carries the checker's own words: what is wrong,
			// and how to fix it, never a generic "try again".
			mustContain(t, runs[1], "does not pass the report check", "the pushback prompt")
			mustContain(t, runs[1], "the report has no STATUS line", "the checker's error")
			if harness == "omp" {
				// omp has no session this entrypoint can name: the pushback is a
				// fresh run with the whole prompt and the section saying a run
				// before it already worked here.
				mustContain(t, runs[1], "This job already ran once on this checkout", "the fresh-run section")
				mustContain(t, runs[1], "implement the tick", "the whole prompt, re-run")
			} else {
				first, second := sessionIDPattern.FindString(runs[0]), sessionIDPattern.FindString(runs[1])
				if first == "" || first != second {
					t.Errorf("the pushback is not in the first run's session (%q then %q): the agent that wrote "+
						"the report is the one that can fix it", first, second)
				}
			}
			branch := WorkerBranch(f.epic, f.tick)
			report, ok := f.remoteFile(branch, WorkerResultFile(f.tick))
			if !ok {
				t.Fatalf("no report reached origin:\n%s", out)
			}
			if status, _, _ := collectParseStatus(report); status != "DONE" {
				t.Errorf("the pushed report's STATUS is %q, want DONE — the fixed report is the one pushed:\n%s", status, report)
			}
			if _, ok := f.remoteFile(branch, "worked.txt"); !ok {
				t.Error("the work the first turn committed is not on the pushed branch")
			}
		})
	}
}

// A report that still fails after the bound is pushed as it is: the pushbacks
// are bounded like the nudges, the work is never held hostage to its report,
// and collect decides what the report is worth.
func TestWorkerSpendsItsReportPushbacksThenPushesTheReport(t *testing.T) {
	shorttest.EndToEnd(t)
	f, runsPath := newPushbackFixture(t, "omp")

	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d, want 0 — the work was committed and the report pushed; collect decides the rest:\n%s", code, out)
	}
	runs := runBlocks(t, runsPath)
	if len(runs) != 1+WorkerReportPushbackMax {
		t.Fatalf("%d harness run(s), want %d (the work plus every pushback):\n%s", len(runs), 1+WorkerReportPushbackMax, out)
	}
	mustContain(t, out, "pushback 2 of 2", "the last pushback")
	mustContain(t, out, "still fails the report check", "the container saying the report goes as it is")
	report, ok := f.remoteFile(WorkerBranch(f.epic, f.tick), WorkerResultFile(f.tick))
	if !ok {
		t.Fatalf("no report reached origin:\n%s", out)
	}
	mustContain(t, report, "Renderer and tests are written", "the agent's own report, pushed as it was")
}

// A report that passes the check is never pushed back, and the check is asked
// about the right report, tick and checkout — as the role the job runs, which
// the role prompt names in the very check it tells the agent to run.
func TestWorkerChecksTheReportAsTheRoleItsPromptNames(t *testing.T) {
	shorttest.EndToEnd(t)
	f := newWorkerFixture(t)
	f.env["TICKS_TEST_LINT_RECORD"] = filepath.Join(f.root, "lint-record")
	f.env[EnvRolePrompt] = "# review-epic\n\nReview the epic.\n\n" +
		"    ticfac-exec-subprocess lint-report RESULT-<tick id>.md --role review-epic --tick <tick id>\n"

	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "pushback") {
		t.Errorf("a report that passes the check was pushed back:\n%s", out)
	}
	calls := f.lintCalls()
	if len(calls) != 1 {
		t.Fatalf("the checker ran %d time(s), want once: %q", len(calls), calls)
	}
	call := calls[0]
	for _, want := range []string{
		"lint-report " + filepath.Join(f.workdir, WorkerResultFile(f.tick)),
		"--role review-epic", "--tick " + f.tick, "--repo " + f.workdir, "--pushback",
	} {
		if !strings.Contains(call, want) {
			t.Errorf("the checker was asked %q, missing %q", call, want)
		}
	}

	// With no role prompt the worker runs the checkout's implement prompt,
	// and is checked as the implement role.
	g := newWorkerFixture(t)
	g.env["TICKS_TEST_LINT_RECORD"] = filepath.Join(g.root, "lint-record")
	if out, code := g.run(); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if calls := g.lintCalls(); len(calls) != 1 || !strings.Contains(calls[0], "--role implement-tick") {
		t.Errorf("a worker on the checkout's prompt was checked as %q, want --role implement-tick", calls)
	}
}

// A checker that cannot answer — absent from an older image, or broken — is
// not a reason to keep the agent or to lose its work: the container says so
// and pushes what there is, and collect decides.
func TestWorkerWhoseCheckerCannotAnswerStillPushes(t *testing.T) {
	shorttest.EndToEnd(t)
	f, runsPath := newPushbackFixture(t, "omp")
	f.env["TICKS_TEST_LINT_EXIT"] = "2"

	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if runs := runBlocks(t, runsPath); len(runs) != 1 {
		t.Fatalf("%d harness run(s), want 1 — a checker that could not answer pushes nothing back:\n%s", len(runs), out)
	}
	mustContain(t, out, "the report check could not run", "the container naming the broken checker")
	if _, ok := f.remoteFile(WorkerBranch(f.epic, f.tick), WorkerResultFile(f.tick)); !ok {
		t.Fatalf("no report reached origin:\n%s", out)
	}
}

// The same u5n case against the REAL checker built from this source — the
// binary the image ships — so the container's reading of its exit status and
// the text it hands the agent are the real checker's, not a stand-in's.
func TestWorkerPushesBackWithTheRealChecker(t *testing.T) {
	shorttest.EndToEnd(t)
	f, runsPath := newPushbackFixture(t, "omp")
	f.env["TICKS_TEST_FIX_ON_PUSHBACK"] = "1"
	checker := filepath.Join(t.TempDir(), reportCheckerBinary)
	build := exec.Command("go", "build", "-o", checker, "./cmd/ticfac-exec-subprocess")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the report checker from this source: %v\n%s", err, out)
	}
	if err := os.Remove(filepath.Join(f.binDir, reportCheckerBinary)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checker, filepath.Join(f.binDir, reportCheckerBinary)); err != nil {
		t.Fatal(err)
	}

	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	runs := runBlocks(t, runsPath)
	if len(runs) != 2 {
		t.Fatalf("%d harness run(s), want 2:\n%s", len(runs), out)
	}
	mustContain(t, runs[1], "the report has no STATUS line", "the real checker's error, handed to the agent")
	mustContain(t, runs[1], "lint-report", "the command the agent can run itself")
	report, _ := f.remoteFile(WorkerBranch(f.epic, f.tick), WorkerResultFile(f.tick))
	if status, _, _ := collectParseStatus(report); status != "DONE" {
		t.Errorf("the pushed report's STATUS is %q, want DONE:\n%s", status, report)
	}
}
