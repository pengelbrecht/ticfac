package statusmodel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The per-tick report drill-in (epic hn6, wave 2 — tick ltg): what a person
// reads when they press enter on a tick — the attempt report's own summary,
// from the archived report.md beside the attempt record, and the attempt
// branch's diff stats, from the repository the run works in. Everything here
// is headless: a real but tiny git repository (the reader measures real
// diffs, so its fixture is a real repo, kept to a handful of commits), and
// the executor state root pointed at a temp dir so nothing reads the host.

// gitIn runs one git command in a directory, on the git the repository's own
// binaries use, against a neutral global configuration: the fixture states
// everything it needs in the repository itself.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// reportRepo is the shape a local run leaves on the machine the dashboard
// reads from: a repository with a run ref, and one attempt branch cut from
// the base it points at that adds two files of five lines each
// (+2 files, +10/-0). The run ref is either a BRANCH (the integration
// branch a live local run works on) or the run TAG ticfac/run-<run-id> that
// runstate places at terminal state — the only ref of that name git allows
// beside the attempt branches, whose own namespace (refs/heads/ticfac/
// run-<run>/...) makes a branch of exactly that name a file/directory
// conflict git refuses.
func reportRepo(t *testing.T, kind, name string) string {
	t.Helper()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "config", "user.email", "fixture@example.com")
	gitIn(t, repo, "config", "user.name", "the fixture")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the base the run branched from")
	if kind == "tag" {
		gitIn(t, repo, "tag", name)
	} else {
		gitIn(t, repo, "branch", name)
	}
	gitIn(t, repo, "checkout", "-q", "-b", "ticfac/run-epic-2jn/tick-nwj/attempt-1")
	for _, file := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(repo, file), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the attempt's work")
	gitIn(t, repo, "checkout", "-q", "main")
	return repo
}

// reportBody is one archived report.md, the shape the workers write: a
// heading, the opening paragraph a person reads first, and the rest.
const reportBody = `# RESULT-nwj

The activity window is read from the runner's own session transcript, and
its buckets cover ten one-minute slots.

## What changed

- internal/statusmodel/activity.go
- internal/statusmodel/report.go

STATUS: DONE
`

// archiveReport writes one attempt's archived report the way the collect
// phase leaves it: report.md beside the attempt record, under the runs root
// of one executor's state directory — the state root the environment names.
func archiveReport(t *testing.T, stateRoot, runID, tickID string, attempt int, body string) {
	t.Helper()
	dir := filepath.Join(stateRoot, "runs", runID, tickID, strconv.Itoa(attempt))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attempt.json"),
		[]byte(fmt.Sprintf(`{"tick_id": %q}`, tickID)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReportReadsSummaryAndDiff: the reader answers a settled attempt's
// report — the archived report.md's first non-heading paragraph as the
// summary, and the attempt branch's diff against its merge base with the
// run branch as three counts. The run ref is tried as the run's own
// namespace ref first (the run tag runstate places at terminal state) and
// as the integration branch a local run derives from its run id second: the
// attempt branch's spelling is pinned in internal/reconcile, and both fork
// the attempt from the same base.
func TestReportReadsSummaryAndDiff(t *testing.T) {
	const want = "The activity window is read from the runner's own session transcript, and its buckets cover ten one-minute slots."
	for _, tc := range []struct {
		name string
		kind string
		ref  string
	}{
		{name: "the integration branch a local run derives from its run id", kind: "branch", ref: "epic/2jn"},
		{name: "the run tag, the only ticfac/run-<run> ref git allows beside the attempt branches", kind: "tag", ref: "ticfac/run-epic-2jn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := reportRepo(t, tc.kind, tc.ref)
			stateRoot := t.TempDir()
			t.Setenv("TICFAC_EXEC_STATE_DIR", stateRoot)
			archiveReport(t, stateRoot, "epic-2jn", "nwj", 1, reportBody)

			input := AttemptReports(repo, "epic-2jn")("nwj", 1)
			if input == nil {
				t.Fatal("the report is archived and the branch stands, and the reader answered nothing")
			}
			if input.Summary != want {
				t.Errorf("the summary is %q, want the report's first non-heading paragraph, one line", input.Summary)
			}
			if !input.DiffRead {
				t.Fatal("the attempt branch stands and its diff was not read")
			}
			if input.Files != 2 || input.Insertions != 10 || input.Deletions != 0 {
				t.Errorf("the diff stats are {%d files, +%d, -%d}, want {2, +10, -0}: two new files of five lines each",
					input.Files, input.Insertions, input.Deletions)
			}
		})
	}
}

// TestReportIsAlsoFoundUnderTheDefaultStateRoots: without the environment's
// override, the archived report is looked for under every executor's runs
// directory under ~/.ticfac/exec — the reader does not know which executor
// ran the attempt, so it walks them all.
func TestReportIsAlsoFoundUnderTheDefaultStateRoots(t *testing.T) {
	repo := reportRepo(t, "branch", "epic/2jn")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TICFAC_EXEC_STATE_DIR", "")
	archiveReport(t, filepath.Join(home, ".ticfac", "exec", "herdr"), "epic-2jn", "nwj", 1, reportBody)

	input := AttemptReports(repo, "epic-2jn")("nwj", 1)
	if input == nil {
		t.Fatal("the report is archived under an executor's default state root and the reader answered nothing")
	}
	if input.Summary == "" || !input.DiffRead {
		t.Errorf("the report read back as %+v, want its summary and its diff", *input)
	}
}

// TestReportIsNullWhenNothingIsArchived: nothing to read is the honest nil —
// no archived report and no branch to diff. A report whose branch is gone
// still answers its summary and says the diff was not read; an attempt
// record that names another tick is not this attempt's, whatever directory
// it shares with it.
func TestReportIsNullWhenNothingIsArchived(t *testing.T) {
	stateRoot := t.TempDir()
	t.Setenv("TICFAC_EXEC_STATE_DIR", stateRoot)
	repo := reportRepo(t, "branch", "epic/2jn")
	reader := AttemptReports(repo, "epic-2jn")

	// Nothing anywhere: the attempt was never dispatched.
	if input := reader("89m", 4); input != nil {
		t.Errorf("an attempt with nothing archived and no branch answered %+v, want nil", *input)
	}

	// The report survived, the branch was disposed: the summary answers and
	// the diff says it was not read — "branch gone" and "not looked" are
	// different claims, and the model owes the reader the second.
	gitIn(t, repo, "branch", "-q", "-D", "ticfac/run-epic-2jn/tick-nwj/attempt-1")
	archiveReport(t, stateRoot, "epic-2jn", "nwj", 1, reportBody)
	input := reader("nwj", 1)
	if input == nil {
		t.Fatal("the report is archived and the reader answered nothing")
	}
	if input.Summary == "" {
		t.Error("the archived report's summary did not answer")
	}
	if input.DiffRead {
		t.Error("the diff reads as read for a branch that is gone")
	}

	// A state directory whose attempt record names another tick is not this
	// attempt's (the cross-run discovery's own guard): never offered.
	dir := filepath.Join(stateRoot, "runs", "epic-2jn", "xbp", "2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attempt.json"), []byte(`{"tick_id": "89m"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(reportBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if input := reader("xbp", 2); input != nil {
		t.Errorf("another tick's same-named attempt directory answered %+v, want nil", *input)
	}
}

// TestReportCachesPerAttempt: a settled attempt's report is immutable, so
// the reader answers the second and every later redraw from the cache — no
// git process a frame, including for the attempts that answered nothing.
func TestReportCachesPerAttempt(t *testing.T) {
	repo := reportRepo(t, "branch", "epic/2jn")
	stateRoot := t.TempDir()
	t.Setenv("TICFAC_EXEC_STATE_DIR", stateRoot)
	archiveReport(t, stateRoot, "epic-2jn", "nwj", 1, reportBody)

	var calls int32
	counting := func(dir string, args ...string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return runGit(dir, args...)
	}
	reader := attemptReports(repo, "epic-2jn", counting)

	first := reader("nwj", 1)
	if first == nil {
		t.Fatal("the report is archived and the reader answered nothing")
	}
	afterFirst := atomic.LoadInt32(&calls)
	if afterFirst == 0 {
		t.Fatal("the first read spawned no git: the fixture measures the reader, not the cache")
	}
	second := reader("nwj", 1)
	if second != first {
		t.Errorf("the second read of one attempt answered a different result: %+v then %+v", first, second)
	}
	if got := atomic.LoadInt32(&calls); got != afterFirst {
		t.Errorf("the second read of one attempt ran git again (%d calls, were %d): a settled attempt's report is immutable, and a two-second redraw must not spawn git every frame",
			got, afterFirst)
	}

	// Another attempt is another read, whatever it answers — and an attempt
	// that answered nothing is cached as nothing, so the empty redraws cost
	// nothing either.
	if reader("nwj", 2) != nil {
		t.Error("an attempt with nothing archived answered a report")
	}
	afterSecond := atomic.LoadInt32(&calls)
	if afterSecond == afterFirst {
		t.Fatal("a different attempt did not read: the cache keyed the tick alone")
	}
	_ = reader("nwj", 2)
	if got := atomic.LoadInt32(&calls); got != afterSecond {
		t.Errorf("a second read of the same EMPTY answer ran git again (%d calls, were %d)", got, afterSecond)
	}
}

// TestReportSummaryIsBoundedToOneLineOfRunes: the summary is the report's
// first non-heading paragraph, flattened and bounded to 300 CHARACTERS — the
// bound the tick states — cut on a rune boundary, never mid-rune into
// invalid UTF-8: the summary rides the dashboard's JSON, and a byte-split
// rune renders as replacement garbage. The rule skips every heading — a
// heading is never the summary — while the branch's diff still answers.
func TestReportSummaryIsBoundedToOneLineOfRunes(t *testing.T) {
	repo := reportRepo(t, "branch", "epic/2jn")
	stateRoot := t.TempDir()
	t.Setenv("TICFAC_EXEC_STATE_DIR", stateRoot)

	// A first paragraph whose byte length crosses 300 inside a multi-byte
	// rune: 299 ASCII letters, then three-byte arrows.
	long := strings.Repeat("a", 299) + strings.Repeat("\u25b8", 8)
	archiveReport(t, stateRoot, "epic-2jn", "nwj", 1,
		"# RESULT-nwj\n\n"+long+"\n\n## What changed\n\n- a file\n\nSTATUS: DONE\n")
	input := AttemptReports(repo, "epic-2jn")("nwj", 1)
	if input == nil {
		t.Fatal("the report is archived and the branch stands, and the reader answered nothing")
	}
	if !utf8.ValidString(input.Summary) {
		t.Errorf("the summary is not valid UTF-8: the cut split a rune — %q", input.Summary)
	}
	if got := len([]rune(input.Summary)); got > 300 {
		t.Errorf("the summary is %d characters, want the tick's bound of 300", got)
	}
	if !input.DiffRead {
		t.Error("the branch stands and its diff was not read")
	}

	// A report that opens with headings and a list: the first-paragraph rule
	// skips every heading — a heading is never quoted — and the first
	// non-heading paragraph answers, however little it says.
	gitIn(t, repo, "branch", "ticfac/run-epic-2jn/tick-nwj/attempt-2",
		"ticfac/run-epic-2jn/tick-nwj/attempt-1")
	archiveReport(t, stateRoot, "epic-2jn", "nwj", 2,
		"# RESULT-nwj\n\n## What changed\n\n- a file\n\nSTATUS: DONE\n")
	input = AttemptReports(repo, "epic-2jn")("nwj", 2)
	if input == nil {
		t.Fatal("the branch stands and its diff was readable, and the reader answered nothing")
	}
	if input.Summary != "- a file" {
		t.Errorf("a report of headings and a list quotes %q, want the first non-heading paragraph: a heading is never the summary", input.Summary)
	}
	if !input.DiffRead {
		t.Error("the branch stands and its diff was not read")
	}
}

// TestReportsDecorateTheSettledTicks: the model carries the drill-in on
// every tick with a current attempt whose state is not dispatched — the
// settled and the reported ones, never the one still in flight — and leaves
// the report null wherever the reader answered nothing.
func TestReportsDecorateTheSettledTicks(t *testing.T) {
	src := runningEpicSources()
	src.Records.Checkpoint.Ticks = append(src.Records.Checkpoint.Ticks,
		runstate.TickState{TickID: "89m", State: "rejected", Attempt: 5},
		runstate.TickState{TickID: "152", State: "reported", Attempt: 7})
	src.Records.Attempts = append(src.Records.Attempts,
		attemptMarker(5, "89m", "2026-09-27T04:20:00Z", "strong", "m", "local-subprocess"),
		attemptMarker(7, "152", "2026-09-27T04:30:00Z", "strong", "m", "local-subprocess"))

	asked := map[string]bool{}
	src.Report = func(tickID string, attempt int) *ReportInput {
		asked[tickID] = true
		switch tickID {
		case "nwj":
			return &ReportInput{Summary: "the nwj report", Files: 1, Insertions: 2, Deletions: 3, DiffRead: true}
		case "152":
			return &ReportInput{Summary: "the 152 report"}
		}
		return nil
	}
	model := Build(src)

	tickByID := map[string]Tick{}
	for _, wave := range *model.Waves {
		for _, tick := range wave.Ticks {
			tickByID[tick.TickID] = tick
		}
	}
	if asked["6dh"] {
		t.Error("the in-flight attempt (state dispatched) was asked for a report: its worker has not written one")
	}
	if report := tickByID["nwj"]; report.Report == nil || report.Report.Summary == nil ||
		*report.Report.Summary != "the nwj report" || report.Report.Diff == nil ||
		*report.Report.Diff != (ReportDiff{Files: 1, Insertions: 2, Deletions: 3}) {
		t.Errorf("the closed tick's drill-in is %+v, want its report's summary and diff", report.Report)
	}
	if report := tickByID["89m"]; report.Report != nil {
		t.Errorf("the rejected tick's reader answered nothing and the model carries %+v, want null", report.Report)
	}
	if report := tickByID["152"]; report.Report == nil || report.Report.Summary == nil ||
		*report.Report.Summary != "the 152 report" || report.Report.Diff != nil {
		t.Errorf("the reported tick's drill-in is %+v, want its summary and the diff left null (not read)", report.Report)
	}
	if report := tickByID["6dh"]; report.Report != nil {
		t.Errorf("the in-flight tick carries a report %+v, want null", report.Report)
	}
}
