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
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The per-tick report drill-in (epic hn6, wave 2 — tick ltg): what a person
// reads when they press enter on a tick — the attempt report's own summary
// and the attempt's diff stats (hn6 rule 6). Everything is read from
// what exists: the summary from the archived report.md the collect phase
// made survive teardown (tick 35h), the diff from the repository the run
// works in — from the attempt branch while it stands, and, once the close
// swept it (tick ihw), from the merge commit that carried its work into
// the integration branch. Where neither exists the report stays null — the
// honest "the report was not read", never a guess.
//
// The reader is keyed by (run, tick, attempt) (tick ihw): a tick's row
// under `watch epic-<id>` may come from a run that is not the one the
// surface was opened on, and its attempt number is that run's own per-run
// number — the same number in two runs names two dispatches, so the run id
// rides with the tick and the attempt, never assumed to be the current
// run's.
//
// The two halves are separated from the model by the Sources seam
// (Sources.Report), so a cloud run passes nil and its ticks state no report:
// a cloud run's attempt branches belong to the factory's containers, and
// its reports to their executor state, neither of which this machine reads.

// ReportInput is one (run, tick, attempt) report as its reader answers it:
// the STATUS-bearing summary the report opens with, the diff's three counts,
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
// reader that answers nothing leaves the report null. Each ask is keyed by
// the run whose row the tick reads (mergedRuns.ownerRunID): the attempt
// number a prior run's row carries is that run's own, and pairing it with
// the current run's id would read another dispatch's report — or none.
func decorateReports(src Sources, merged *mergedRuns, m *Model) {
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
			if report := src.Report(merged.ownerRunID(tick.TickID, m.RunID), tick.TickID, *tick.Attempt); report != nil {
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
// a (run, tick, attempt) report from the run's own artifacts — the archived
// report.md beside the attempt record under the executor state root, and
// the attempt's diff against its merge base with the run branch, in the
// repository the run works in — from the attempt branch while it stands,
// and from the merge commit once the close swept it. Results are immutable
// per (run, tick, attempt) once the attempt settled, so they are cached: a
// two-second redraw must not spawn git every frame.
func AttemptReports(repo string) func(runID, tickID string, attempt int) *ReportInput {
	return attemptReports(repo, runGit)
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
	// It is handed its argv, so it goes through the push queue like every
	// such runner; for anything but a push the queue costs nothing (tick rlp).
	done := gitbin.PushQueue(dir, args, nil)
	out, err := cmd.Output()
	done(err)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// attemptReports is AttemptReports over an injectable git: the reader is a
// closure over one cache, keyed by (run, tick, attempt) — an attempt's
// report is settled when the attempt is, and nil answers cache too (the
// attempt that has no report will not grow one).
func attemptReports(repo string, git gitCommand) func(runID, tickID string, attempt int) *ReportInput {
	var cache sync.Map
	return func(runID, tickID string, attempt int) *ReportInput {
		key := fmt.Sprintf("%s/%s#%d", runID, tickID, attempt)
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

// archivedSummary reads one attempt's archived report.md, under the RUN
// the attempt belonged to, and answers its opening paragraph. The report
// is located the way the reconciler's own
// prior-report walk locates it (internal/reconcile/dispatch.go,
// priorReports/findAttemptState): by walking the dispatch's state directory
// for the attempt record rather than recomputing any executor's internal
// naming — which is what keeps this reading herdr's nested layout and the
// subprocess' flat one with the same code — and an attempt record that does
// not name this tick is not this attempt's, however same-named its directory
// (the same guard the cross-run discovery holds).
func archivedSummary(runID, tickID string, attempt int) (string, bool) {
	if runID == "" {
		return "", false // a run id nobody recorded is not any run's by default
	}
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
// internal/reconcile), keyed by the run that dispatched the attempt. The
// run ref is resolved through reportMergeBases: the merge base is the
// attempt's own fork point whichever spelling the repository carries. The
// branch is gone when its work was merged and swept (the close's own half,
// sweepTick) or disposed: a MERGED attempt's work is still readable from
// the merge commit, a disposed one's is not — and the diff says it was not
// read rather than guessing a number against a base nobody resolved.
func attemptDiff(repo, runID, tickID string, attempt int, git gitCommand) (ReportDiff, bool) {
	if repo == "" || runID == "" {
		return ReportDiff{}, false
	}
	branch := fmt.Sprintf("ticfac/run-%s/tick-%s/attempt-%d", runID, tickID, attempt)
	if head, err := git(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil ||
		strings.TrimSpace(head) == "" {
		// The branch is gone: its work was merged and swept, or disposed.
		return mergedAttemptDiff(repo, runID, tickID, attempt, git)
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

// mergedAttemptDiff measures a MERGED attempt's work from the merge commit
// that carried it into the integration branch — the shape every closed
// tick's attempt is in after the close swept its branch (internal/reconcile,
// cleanUp → sweepTick). The reconciler names the attempt in the merge
// commit's message in every spelling it mints — the plain merge
// ("ticfac run <run>: tick <tick> attempt <n>"), the resolve-conflict job's
// minted resolution and the fold of a resolution onto a moved branch (the
// same words with the resolution's story after them) — so the search is for
// a message LINE that names the attempt, never a substring: a substring
// would read attempt 1's number off attempt 10's merge and answer a closed
// tick with a later dispatch's work. The diff is the merge's own two sides
// — its first parent (the integration head at merge time) against its
// second (the attempt's head), measured from their merge base — the same
// number the live branch read. Not ok when no merge of THIS attempt is
// reachable from any base the run names: the diff was not read.
func mergedAttemptDiff(repo, runID, tickID string, attempt int, git gitCommand) (ReportDiff, bool) {
	if tickID == "" || attempt < 1 {
		return ReportDiff{}, false
	}
	needle := reconcile.AttemptMergeNeedle(runID, tickID, attempt)
	for _, base := range reportMergeBases(runID) {
		out, err := git(repo, "log", base, "--merges", "--fixed-strings",
			"--grep="+needle, "--format=%H%x1f%B%x1e")
		if err != nil {
			continue
		}
		sha, ok := mergeNamingAttempt(out, needle)
		if !ok {
			continue
		}
		stats, err := git(repo, "diff", "--shortstat", sha+"^1..."+sha+"^2")
		if err != nil {
			continue
		}
		return parseShortstat(stats), true
	}
	return ReportDiff{}, false
}

// mergeNamingAttempt finds the newest merge commit whose message names the
// attempt in one line: the line is the needle whole (the plain merge ends
// its message there) or the needle followed by a space (the resolve
// spellings carry the resolution's story after it). A line that merely
// starts with the needle and goes on to another digit — attempt 10 beside
// attempt 1 — names another dispatch, never this one.
func mergeNamingAttempt(out, needle string) (string, bool) {
	for _, record := range strings.Split(out, "\x1e") {
		sha, body, ok := strings.Cut(record, "\x1f")
		if !ok {
			continue
		}
		sha = strings.TrimSpace(sha)
		if sha == "" {
			continue
		}
		for _, line := range strings.Split(body, "\n") {
			if line == needle || strings.HasPrefix(line, needle+" ") {
				return sha, true
			}
		}
	}
	return "", false
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
