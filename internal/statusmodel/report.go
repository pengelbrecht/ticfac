package statusmodel

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// The per-tick report drill-in (epic hn6, wave 2 — tick ltg): what a person
// reads when they press enter on a tick — the attempt report's own summary
// and the attempt branch's diff stats (hn6 rule 6). Everything is read from
// what exists: the summary from the archived report.md the collect phase
// made survive teardown (tick 35h), the diff from the repository the run
// works in. Where neither exists the report stays null — the honest "the
// report was not read", never a guess.
//
// The two halves are separated from the model by the Sources seam
// (Sources.Report), so a cloud run passes nil and its ticks state no report:
// a cloud run's attempt branches belong to the factory's containers, and
// its reports to their executor state, neither of which this machine reads.

// ReportInput is one (tick, attempt) report as its reader answers it: the
// STATUS-bearing summary the report opens with, the diff's three counts,
// and whether the diff was read at all — the fact that separates "no
// changes" from "not looked", which the model owes the reader.
type ReportInput struct {
	Summary    string
	Files      int
	Insertions int
	Deletions  int
	DiffRead   bool
}

// decorateReports lays the drill-in on every tick with a current attempt
// whose state is not dispatched — the settled and the reported ones, never
// the attempt still in flight, whose worker has written no report yet. A
// reader that answers nothing leaves the report null.
func decorateReports(src Sources, m *Model) {
	if src.Report == nil || m.Waves == nil {
		return
	}
	for wi := range *m.Waves {
		wave := &(*m.Waves)[wi]
		for ti := range wave.Ticks {
			tick := &wave.Ticks[ti]
			if tick.Attempt == nil || tick.State == tickDispatched {
				continue
			}
			if report := src.Report(tick.TickID, *tick.Attempt); report != nil {
				tick.Report = reportOf(report)
			}
		}
	}
}

// reportOf carries one reader's answer onto the tick's drill-in: the summary
// null when the report carried none, the diff null when it was not read —
// the two honest absences the contract states separately.
func reportOf(input *ReportInput) *TickReport {
	out := &TickReport{}
	if input.Summary != "" {
		summary := input.Summary
		out.Summary = &summary
	}
	if input.DiffRead {
		out.Diff = &ReportDiff{Files: input.Files, Insertions: input.Insertions, Deletions: input.Deletions}
	}
	return out
}

// AttemptReports is the production report reader for a LOCAL run: it answers
// a (tick, attempt) report from the run's own artifacts — the archived
// report.md beside the attempt record under the executor state root, and
// the attempt branch's diff against its merge base with the run branch, in
// the repository the run works in. Results are immutable per (tick, attempt)
// once the attempt settled, so they are cached: a two-second redraw must not
// spawn git every frame.
func AttemptReports(repo, runID string) func(tickID string, attempt int) *ReportInput {
	return attemptReports(repo, runID, runGit)
}

// gitCommand runs one git command in a directory and answers its stdout —
// the seam the cache test counts.
type gitCommand func(dir string, args ...string) (string, error)

// runGit runs one git command in a directory, through the git binary the
// repository's own runners use (internal/gitbin), with the transport bound
// every git here carries (gitbin.TransportEnv): a runner that takes its
// argv from its caller can be handed a command that reaches a remote, and a
// git that has gone silent is waited on forever without it.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command(gitbin.Path(), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), gitbin.TransportEnv()...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// attemptReports is AttemptReports over an injectable git: the reader is a
// closure over one cache, keyed by (tick, attempt) — an attempt's report is
// settled when the attempt is, and nil answers cache too (the attempt that
// has no report will not grow one).
func attemptReports(repo, runID string, git gitCommand) func(tickID string, attempt int) *ReportInput {
	var cache sync.Map
	return func(tickID string, attempt int) *ReportInput {
		key := fmt.Sprintf("%s#%d", tickID, attempt)
		if held, ok := cache.Load(key); ok {
			return held.(*ReportInput)
		}
		input := readAttemptReport(repo, runID, tickID, attempt, git)
		cache.Store(key, input)
		return input
	}
}

// readAttemptReport answers one attempt's report: the archived summary when
// one survived, the diff stats when the attempt branch and its merge base
// could both be resolved. Nil when neither half could be read — the reader
// says nothing rather than an empty shape that renders as a report.
func readAttemptReport(repo, runID, tickID string, attempt int, git gitCommand) *ReportInput {
	input := ReportInput{}
	read := false
	if summary, ok := archivedSummary(runID, tickID, attempt); ok {
		input.Summary, read = summary, true
	}
	if stats, ok := attemptDiff(repo, runID, tickID, attempt, git); ok {
		input.Files, input.Insertions, input.Deletions, input.DiffRead =
			stats.Files, stats.Insertions, stats.Deletions, true
		read = true
	}
	if !read {
		return nil
	}
	return &input
}

// ---- the archived report --------------------------------------------------

// EnvExecStateDir is the executor state root override the reconciler honors
// ($TICFAC_EXEC_STATE_DIR, the executor's own state directory with "runs"
// beneath it): the tests point it at a temp dir so nothing reads the host.
const EnvExecStateDir = "TICFAC_EXEC_STATE_DIR"

// archivedSummary reads one attempt's archived report.md and answers its
// opening paragraph. The report is located the way the reconciler's own
// prior-report walk locates it (internal/reconcile/dispatch.go,
// priorReports/findAttemptState): by walking the dispatch's state directory
// for the attempt record rather than recomputing any executor's internal
// naming — which is what keeps this reading herdr's nested layout and the
// subprocess' flat one with the same code — and an attempt record that does
// not name this tick is not this attempt's, however same-named its directory
// (the same guard the cross-run discovery holds).
func archivedSummary(runID, tickID string, attempt int) (string, bool) {
	for _, root := range reportStateRoots() {
		state, ok := findAttemptState(filepath.Join(root, runID, tickID, strconv.Itoa(attempt)))
		if !ok || !attemptNamesTick(state, tickID) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(state, subprocess.FileReportArchive))
		if err != nil {
			// Dispatched, but it left no report — settled with nothing said.
			continue
		}
		return reportSummary(string(raw)), true
	}
	return "", false
}

// reportStateRoots are the roots one run's executor state may live under:
// the override's own runs directory when the environment names one, else
// every executor's runs directory under ~/.ticfac/exec — the reader does not
// know which executor ran the attempt, so it walks them all.
func reportStateRoots() []string {
	if dir := os.Getenv(EnvExecStateDir); dir != "" {
		return []string{filepath.Join(dir, "runs")}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".ticfac", "exec", "*", "runs"))
	return matches
}

// findAttemptState locates the executor's own state directory for one
// dispatch: the reconciler gave the dispatch a directory of its own, so
// the walk for the attempt record is unambiguous. It is the reconciler's own
// walk (dispatch.go findAttemptState), mirrored here because it is not
// exported, and recomputing no executor's naming is what lets this read a
// state directory the executor nested its own way under the one it was
// given.
func findAttemptState(root string) (string, bool) {
	if root == "" {
		return "", false
	}
	found := ""
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if err == fs.ErrNotExist {
				return nil
			}
			return err
		}
		if !entry.IsDir() && entry.Name() == "attempt.json" {
			found = filepath.Dir(path)
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
}

// attemptNamesTick says whether the attempt record inside one state
// directory names this tick. The record is read loosely, the way the
// reconciler walks this seam: this is a reader of the executors' shared file
// name, not an importer of their shape.
func attemptNamesTick(state, tickID string) bool {
	raw, err := os.ReadFile(filepath.Join(state, "attempt.json"))
	if err != nil {
		return false
	}
	var record struct {
		TickID string `json:"tick_id"`
	}
	return json.Unmarshal(raw, &record) == nil && record.TickID == tickID
}

// reportSummary is the drill-in's one line: the report's first non-empty
// paragraph that is not a heading, flattened and bounded to 300 characters —
// what a person reads to decide whether to open the whole report.
func reportSummary(body string) string {
	var paragraph []string
	flush := func() string {
		defer func() { paragraph = nil }()
		if len(paragraph) == 0 {
			return ""
		}
		if strings.HasPrefix(paragraph[0], "#") {
			return ""
		}
		return oneLine(strings.Join(paragraph, " "), 300)
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if summary := flush(); summary != "" {
				return summary
			}
			continue
		}
		paragraph = append(paragraph, line)
	}
	return flush()
}

// ---- the diff -------------------------------------------------------------

// attemptDiff measures one attempt's work: the diff between the attempt
// branch and its merge base with the run ref, as three counts. The attempt
// branch is the write ref's own spelling,
// ticfac/run-<run>/tick-<tick>/attempt-<n> (attemptWriteRef in
// internal/reconcile). The run ref is resolved through reportMergeBases:
// the merge base is the attempt's own fork point whichever spelling the
// repository carries. Not ok when the branch is gone or no run ref shares
// history with it: the diff was not read, and the model says so rather than
// guessing a number against a base nobody resolved.
func attemptDiff(repo, runID, tickID string, attempt int, git gitCommand) (ReportDiff, bool) {
	if repo == "" {
		return ReportDiff{}, false
	}
	branch := fmt.Sprintf("ticfac/run-%s/tick-%s/attempt-%d", runID, tickID, attempt)
	if head, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil ||
		strings.TrimSpace(head) == "" {
		return ReportDiff{}, false // the branch is gone: its work was merged or disposed
	}
	for _, base := range reportMergeBases(runID) {
		mergeBase, err := git(repo, "merge-base", base, branch)
		if err != nil {
			continue
		}
		mergeBase = strings.TrimSpace(mergeBase)
		if mergeBase == "" {
			continue
		}
		out, err := git(repo, "diff", "--shortstat", mergeBase+".."+branch)
		if err != nil {
			continue
		}
		return parseShortstat(out), true
	}
	return ReportDiff{}, false
}

// reportMergeBases are the run-ref spellings an attempt's diff is based
// against, in the order they are tried: the run's own namespace ref,
// ticfac/run-<run-id> — which can only be the run TAG runstate places at
// terminal state (refs/tags/...), since a branch of that name is a
// file/directory conflict git refuses inside the attempt branches' own
// namespace (refs/heads/ticfac/run-<run>/...) — then the integration branch
// a local run derives from its run id, which is the branch every attempt is
// cut from while the run is live. A run that carried a custom integration
// branch names nothing either way, and its diffs read as not read — never a
// number against a base that was not resolved.
func reportMergeBases(runID string) []string {
	bases := []string{"ticfac/run-" + runID}
	if epicID, ok := strings.CutPrefix(runID, "epic-"); ok && epicID != "" {
		bases = append(bases, "epic/"+epicID)
	}
	return bases
}

// shortstatFiles, shortstatInsertions and shortstatDeletions read
// `git diff --shortstat`'s own line: " 2 files changed, 10 insertions(+),
// 0 deletions(-)", in every singular and plural it spells.
var (
	shortstatFiles      = regexp.MustCompile(`(\d+) files? changed`)
	shortstatInsertions = regexp.MustCompile(`(\d+) insertions?\(\+\)`)
	shortstatDeletions  = regexp.MustCompile(`(\d+) deletions?\(-\)`)
)

// parseShortstat reads the shortstat line into the three counts the model
// states. A part the line does not spell (a binary-only change carries no
// insertions) is zero: the line said so by saying nothing.
func parseShortstat(out string) ReportDiff {
	count := func(re *regexp.Regexp) int {
		m := re.FindStringSubmatch(out)
		if m == nil {
			return 0
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return ReportDiff{
		Files:      count(shortstatFiles),
		Insertions: count(shortstatInsertions),
		Deletions:  count(shortstatDeletions),
	}
}
