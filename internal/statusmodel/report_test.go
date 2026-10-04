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

			input := AttemptReports(repo)("epic-2jn", "nwj", 1)
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

	input := AttemptReports(repo)("epic-2jn", "nwj", 1)
	if input == nil {
		t.Fatal("the report is archived under an executor's default state root and the reader answered nothing")
	}
	if input.Summary == "" || !input.DiffRead {
		t.Errorf("the report read back as %+v, want its summary and its diff", *input)
	}
}

// TestReportIsKeyedByRun (tick ihw): the same (tick, attempt) in two runs
// names two dispatches, and a tick's row under `watch epic-<id>` may come
// from a run that is not the one the surface was opened on — so the reader
// is keyed by (run, tick, attempt): each run's attempt answers its own
// archived report and its own branch's diff, never the other run's
// same-numbered one.
func TestReportIsKeyedByRun(t *testing.T) {
	repo := reportRepoRuns(t)
	stateRoot := t.TempDir()
	t.Setenv("TICFAC_EXEC_STATE_DIR", stateRoot)
	archiveReport(t, stateRoot, "epic-aaa", "nwj", 1, "# RESULT-nwj\n\nrun aaa's report\n\nSTATUS: DONE\n")
	archiveReport(t, stateRoot, "epic-bbb", "nwj", 1, "# RESULT-nwj\n\nrun bbb's report\n\nSTATUS: DONE\n")

	reader := AttemptReports(repo)
	for _, tc := range []struct {
		run        string
		summary    string
		files      int
		insertions int
		deletions  int
	}{
		{run: "epic-aaa", summary: "run aaa's report", files: 1, insertions: 5, deletions: 0},
		{run: "epic-bbb", summary: "run bbb's report", files: 2, insertions: 10, deletions: 0},
	} {
		input := reader(tc.run, "nwj", 1)
		if input == nil {
			t.Fatalf("run %s's attempt is archived and its branch stands, and the reader answered nothing", tc.run)
		}
		if input.Summary != tc.summary {
			t.Errorf("run %s's summary is %q, want its own report's %q: the same attempt number in two runs names two dispatches",
				tc.run, input.Summary, tc.summary)
		}
		if !input.DiffRead || input.Files != tc.files || input.Insertions != tc.insertions || input.Deletions != tc.deletions {
			t.Errorf("run %s's diff is {%d files, +%d, −%d} (read %t), want its own branch's {%d, +%d, −%d}",
				tc.run, input.Files, input.Insertions, input.Deletions, input.DiffRead,
				tc.files, tc.insertions, tc.deletions)
		}
	}

	// A run that was never dispatched answers nothing — the reader says so
	// rather than borrowing another run's same-numbered attempt.
	if input := reader("epic-ccc", "nwj", 1); input != nil {
		t.Errorf("a run with no attempt of the tick answered %+v, want nil", *input)
	}
}

// reportRepoRuns is the shape two runs of one epic leave behind: each cut
// its own attempt branch of the same tick under its own run id, from its
// own integration branch spelling. Run aaa's attempt adds one file of five
// lines (+1/+5), run bbb's adds two (+2/+10).
func reportRepoRuns(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "config", "user.email", "fixture@example.com")
	gitIn(t, repo, "config", "user.name", "the fixture")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the base both runs branched from")
	for _, run := range []struct {
		id    string
		epics string
		files int
	}{{"epic-aaa", "aaa", 1}, {"epic-bbb", "bbb", 2}} {
		gitIn(t, repo, "branch", "epic/"+run.epics)
		gitIn(t, repo, "checkout", "-q", "-b",
			fmt.Sprintf("ticfac/run-%s/tick-nwj/attempt-1", run.id))
		for i := 1; i <= run.files; i++ {
			if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("%s-%d.txt", run.epics, i)),
				[]byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitIn(t, repo, "add", "-A")
		gitIn(t, repo, "commit", "-q", "-m", run.id+"'s attempt")
		gitIn(t, repo, "checkout", "-q", "main")
	}
	return repo
}

// TestReportDiffSurvivesTheSweep (tick ihw): the close sweeps the attempt
// branch the moment its work is merged (dispatch.go cleanUp → sweepTick),
// so a closed tick's drill-in would go quiet on the diff the moment it
// closes. The work is still readable from the merge commit that carried it
// in — in every message the reconciler mints: the plain merge, the
// resolve-conflict job's resolution, and the fold of a resolution onto a
// moved branch — and the counts are the same number the live branch read.
func TestReportDiffSurvivesTheSweep(t *testing.T) {
	const runID, tickID = "epic-2jn", "nwj"
	for _, tc := range []struct {
		name    string
		message string
	}{
		{name: "the plain merge",
			message: "Merge branch 'ticfac/run-epic-2jn/tick-nwj/attempt-1' into epic/2jn\n\n" +
				"ticfac run epic-2jn: tick nwj attempt 1"},
		{name: "the resolve-conflict job's minted merge",
			message: "Merge the resolve-conflict job's resolution of nwj into epic/2jn\n\n" +
				"ticfac run epic-2jn: tick nwj attempt 1 conflicted and was resolved by the resolve-conflict job"},
		{name: "a resolution folded onto a moved branch",
			message: "Merge the resolve-conflict job's resolution of nwj into epic/2jn\n\n" +
				"ticfac run epic-2jn: tick nwj attempt 1 was resolved against 1a2b3c4d; epic/2jn has moved to 5e6f7a8b since, and the resolution is merged onto it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := sweptRepo(t, runID, tickID, 1, tc.message)
			input := AttemptReports(repo)(runID, tickID, 1)
			if input == nil {
				t.Fatal("the attempt was merged and the reader answered nothing")
			}
			if !input.DiffRead {
				t.Fatal("the merged attempt's diff was not read from the merge commit that carries its work")
			}
			if input.Files != 2 || input.Insertions != 10 || input.Deletions != 0 {
				t.Errorf("the merged attempt's diff is {%d files, +%d, −%d}, want the live branch's {2, +10, −0}",
					input.Files, input.Insertions, input.Deletions)
			}
		})
	}
}

// sweptRepo is the repository a closed tick leaves behind: one attempt
// branch of two files of five lines, merged into the integration branch
// with the message the reconciler mints, then swept — the close deletes
// merged branches, so the merge commit is the only thing that still names
// the attempt's work.
func sweptRepo(t *testing.T, runID, tickID string, attempt int, message string) string {
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
	epicID := strings.TrimPrefix(runID, "epic-")
	gitIn(t, repo, "branch", "epic/"+epicID)
	branch := fmt.Sprintf("ticfac/run-%s/tick-%s/attempt-%d", runID, tickID, attempt)
	gitIn(t, repo, "checkout", "-q", "-b", branch)
	for _, file := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(repo, file), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the attempt's work")
	gitIn(t, repo, "checkout", "-q", "epic/"+epicID)
	gitIn(t, repo, "merge", "--no-ff", "-q", "--no-edit", "-m", message, branch)
	gitIn(t, repo, "branch", "-q", "-D", branch)
	return repo
}

// TestMergedDiffIsTheAttemptOwnNumber (tick ihw): the merge commit is found
// by the message line that names the attempt — never a substring, which
// would read attempt 1's number off attempt 10's merge and answer a closed
// tick with a later dispatch's work.
func TestMergedDiffIsTheAttemptOwnNumber(t *testing.T) {
	const runID, tickID = "epic-2jn", "nwj"
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "config", "user.email", "fixture@example.com")
	gitIn(t, repo, "config", "user.name", "the fixture")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "the base the run branched from")
	gitIn(t, repo, "branch", "epic/2jn")
	// Attempt 1 closed the tick early: two files. Attempt 10 is a later
	// dispatch of the same tick another run merged: one file.
	for _, attempt := range []struct {
		n     int
		files []string
	}{{1, []string{"a.txt", "b.txt"}}, {10, []string{"c.txt"}}} {
		branch := fmt.Sprintf("ticfac/run-%s/tick-%s/attempt-%d", runID, tickID, attempt.n)
		gitIn(t, repo, "checkout", "-q", "-b", branch)
		for _, file := range attempt.files {
			if err := os.WriteFile(filepath.Join(repo, file), []byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitIn(t, repo, "add", "-A")
		gitIn(t, repo, "commit", "-q", "-m", "attempt's work")
		gitIn(t, repo, "checkout", "-q", "epic/2jn")
		gitIn(t, repo, "merge", "--no-ff", "-q", "--no-edit", "-m",
			fmt.Sprintf("Merge branch '%s' into epic/2jn\n\nticfac run %s: tick %s attempt %d", branch, runID, tickID, attempt.n),
			branch)
		gitIn(t, repo, "branch", "-q", "-D", branch)
	}

	reader := AttemptReports(repo)
	if input := reader(runID, tickID, 1); input == nil || !input.DiffRead ||
		input.Files != 2 || input.Insertions != 10 {
		got := ReportInput{}
		if input != nil {
			got = *input
		}
		t.Errorf("attempt 1's merged diff is %+v, want its own {2 files, +10}: the number was read off attempt 10's merge",
			got)
	}
	if input := reader(runID, tickID, 10); input == nil || !input.DiffRead ||
		input.Files != 1 || input.Insertions != 5 {
		t.Errorf("attempt 10's merged diff is %+v, want its own {1 file, +5}", input)
	}
}

// TestReportIsAskedOfTheRunThatOwnsTheTick (tick ihw): a tick's row under
// `watch epic-<id>` may come from a run that is not the one the surface was
// opened on — the run that closed it — and its attempt number is that run's
// own per-run number. The reader is asked for (that run, that tick, that
// attempt), never for the current run's id with a prior run's number: the
// same number in two runs names two dispatches, and the wrong pairing reads
// another dispatch's report or none.
func TestReportIsAskedOfTheRunThatOwnsTheTick(t *testing.T) {
	src := failedNewestSources()
	asked := map[string]bool{}
	src.Report = func(runID, tickID string, attempt int) *ReportInput {
		asked[fmt.Sprintf("%s/%s#%d", runID, tickID, attempt)] = true
		if runID == "run_aaa" && tickID == "at1" && attempt == 1 {
			return &ReportInput{Summary: "the run that closed it"}
		}
		return nil
	}
	model := Build(src)

	// The rows the prior runs own, each with the owning run's own attempt
	// number — the exact triples the reader must be asked for.
	for _, want := range []string{
		"run_aaa/at1#1",
		"run_bbb/at2#2",
		"run_bbb/at3#12",
		"run_bbb/at5#3",
	} {
		if !asked[want] {
			t.Errorf("the reader was never asked for %s: the row's run and number were not paired", want)
		}
	}
	for key := range asked {
		if strings.HasPrefix(key, "run_ccc/") {
			t.Errorf("the reader was asked for the CURRENT run's %s: the row belongs to a prior run, and the current run's same-numbered dispatch is another worker's", key)
		}
	}
	if tick := tickOfModel(model, "at1"); tick == nil || tick.Report == nil ||
		tick.Report.Summary == nil || *tick.Report.Summary != "the run that closed it" {
		t.Errorf("the closed prior-run tick's drill-in did not carry its run's report: %+v", tick)
	}
}

// tickOfModel is one tick's row out of the built model, by id.
func tickOfModel(m Model, tickID string) *Tick {
	if m.Waves == nil {
		return nil
	}
	for wi := range *m.Waves {
		for ti := range (*m.Waves)[wi].Ticks {
			if (*m.Waves)[wi].Ticks[ti].TickID == tickID {
				return &(*m.Waves)[wi].Ticks[ti]
			}
		}
	}
	return nil
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
	reader := AttemptReports(repo)

	// Nothing anywhere: the attempt was never dispatched.
	if input := reader("epic-2jn", "89m", 4); input != nil {
		t.Errorf("an attempt with nothing archived and no branch answered %+v, want nil", *input)
	}

	// The report survived, the branch was disposed: the summary answers and
	// the diff says it was not read — "branch gone" and "not looked" are
	// different claims, and the model owes the reader the second.
	gitIn(t, repo, "branch", "-q", "-D", "ticfac/run-epic-2jn/tick-nwj/attempt-1")
	archiveReport(t, stateRoot, "epic-2jn", "nwj", 1, reportBody)
	input := reader("epic-2jn", "nwj", 1)
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
	if input := reader("epic-2jn", "xbp", 2); input != nil {
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
	reader := attemptReports(repo, counting)

	first := reader("epic-2jn", "nwj", 1)
	if first == nil {
		t.Fatal("the report is archived and the reader answered nothing")
	}
	afterFirst := atomic.LoadInt32(&calls)
	if afterFirst == 0 {
		t.Fatal("the first read spawned no git: the fixture measures the reader, not the cache")
	}
	second := reader("epic-2jn", "nwj", 1)
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
	if reader("epic-2jn", "nwj", 2) != nil {
		t.Error("an attempt with nothing archived answered a report")
	}
	afterSecond := atomic.LoadInt32(&calls)
	if afterSecond == afterFirst {
		t.Fatal("a different attempt did not read: the cache keyed the tick alone")
	}
	_ = reader("epic-2jn", "nwj", 2)
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
	input := AttemptReports(repo)("epic-2jn", "nwj", 1)
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
	input = AttemptReports(repo)("epic-2jn", "nwj", 2)
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
	src.Report = func(runID, tickID string, attempt int) *ReportInput {
		asked[fmt.Sprintf("%s/%s#%d", runID, tickID, attempt)] = true
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
