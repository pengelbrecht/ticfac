// Package runprogress is the run's account of whether its attempts are
// getting anywhere — the question liveness does not answer (tick 7zs).
//
// During the Phase 3 run (epic-9pd, tick 0z0 attempt 1) the run correctly knew
// the worker was alive for all 55 minutes; it had no way to know the worker
// had produced nothing durable for 40 of them. The feed was silent because
// nothing happened that the run observes; `ticfac status` reported alive,
// which was true. A person caught it by reading the pane.
//
// The simplest signal that is honest about what it measures: how long since
// an attempt's branch last moved, and how long since its worktree last
// changed. Neither is a verdict and neither may stop or reject anything — a
// worker thinking hard legitimately commits nothing for a while. They are a
// reason to look.
//
// Both facts are read from the repo the run works in, without asking the
// executor: every attempt of this run's executors leaves its worktree
// registered there, checked out on the one branch the attempt's write grade
// allows — `refs/heads/ticfac/run-<run-id>/tick-<tick>/attempt-<n>`, the
// ref vocabulary the dispatch builds (job-protocol.json's write_ref_prefix).
// The branch tip's committer date is "the branch last moved"; the newest
// file mtime under the worktree — .git skipped — is "the worktree last
// changed".
package runprogress

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// attemptRefPrefix is the namespace every attempt's write ref lives under.
const attemptRefPrefix = "refs/heads/ticfac/"

// RefPrefix is the ref namespace ONE RUN's attempts write — bounded per run,
// the prefix a run's write grade allows (job-protocol.json's
// `write_ref_prefix`). It is the spelling the dispatch builds its write refs
// from, stated here once so the reader that enumerates attempt refs and the
// writer that mints them cannot drift apart silently: reconcile pins the two
// spellings to each other in a test.
func RefPrefix(runID string) string {
	return attemptRefPrefix + "run-" + runID + "/"
}

// ParseAttempt reads one attempt's identity out of its write ref: which run,
// which tick, which attempt. A ref that is not one run's attempt vocabulary
// answers false rather than a guess.
func ParseAttempt(ref string) (runID, tickID string, attempt int, ok bool) {
	rest, ok := strings.CutPrefix(ref, attemptRefPrefix+"run-")
	if !ok {
		return "", "", 0, false
	}
	runAndTick, tail, ok := strings.Cut(rest, "/tick-")
	if !ok || runAndTick == "" {
		return "", "", 0, false
	}
	tick, attemptTail, ok := strings.Cut(tail, "/attempt-")
	if !ok || tick == "" {
		return "", "", 0, false
	}
	// The number is the whole tail: "attempt-12x" is not attempt 12, and a
	// ref that fails to round-trip through Atoi is not an attempt ref.
	n, err := strconv.Atoi(attemptTail)
	if err != nil || n < 1 || strconv.Itoa(n) != attemptTail {
		return "", "", 0, false
	}
	return runAndTick, tick, n, true
}

// Duration is a time.Duration that marshals as its own spelling, because a
// gap is for reading: "40m0s" answers a watcher; 2400000000000 answers
// nobody. The JSON carries the same string the text surfaces print.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON is MarshalJSON's other half, so a status read back from its
// own JSON — a dashboard, a test — sees the same number the feed printed.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("runprogress: %q is not a duration a gap was measured in: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) String() string { return time.Duration(d).String() }

// Round is time.Duration's own, so a feed line and a threshold comparison
// spell the same number the same way.
func (d Duration) Round(m time.Duration) Duration { return Duration(time.Duration(d).Round(m)) }

// Attempt is one in-flight attempt's account of itself: who it is, where its
// work stands, and how long since that work last moved. Every measurement is
// nullable — an unmeasurable fact is stated as null, never guessed at, the
// same honesty the run's own records keep.
type Attempt struct {
	TickID  string `json:"tick_id"`
	Attempt int    `json:"attempt"`
	Branch  string `json:"branch"`
	// Worktree is the path git's registration names, which is the path the
	// executor created — empty when the attempt has no standing registration
	// (a substrate that is not local, or a worktree whose directory is gone).
	Worktree string `json:"worktree"`

	// BranchMovedAt is the committer date of the branch tip — when the attempt
	// last moved its branch, which before the first commit is the BASE
	// commit's date, somebody else's work. Null when the ref cannot be read.
	BranchMovedAt *time.Time `json:"branch_moved_at"`
	// WorktreeChangedAt is the mtime of the newest file under the worktree
	// (.git skipped) — when the attempt last changed anything in it. Null
	// when the worktree is not there to be read.
	WorktreeChangedAt *time.Time `json:"worktree_changed_at"`

	// The same two facts as gaps, measured against the `now` the caller
	// stamped the probe with, so a reader never has to do clock arithmetic.
	BranchIdle   *Duration `json:"branch_idle"`
	WorktreeIdle *Duration `json:"worktree_idle"`
}

// Idle is the gap: how long since the attempt last produced anything
// durable, of either kind — the newer of the two facts, because a commit and
// a file change are both progress and the recent one is what keeps the
// attempt honest.
//
// The branch fact alone is deliberately NOT a gap: before an attempt's first
// commit the tip is the base commit, and "nothing durable for three hours"
// said about an attempt twenty minutes old is a number measuring somebody
// else's work. Where the worktree cannot be read — a substrate that is not
// local, a teardown that removed it — this answers false rather than guess,
// and the branch age stays what it is: a reported fact, not a claimed gap.
func (a Attempt) Idle() (time.Duration, bool) {
	switch {
	case a.BranchIdle != nil && a.WorktreeIdle != nil:
		branch, work := time.Duration(*a.BranchIdle), time.Duration(*a.WorktreeIdle)
		if branch < work {
			return branch, true
		}
		return work, true
	case a.WorktreeIdle != nil:
		return time.Duration(*a.WorktreeIdle), true
	}
	return 0, false
}

// Standing measures every attempt of one run whose worktree still stands in
// the repo — the registrations git keeps are the honest census: the run's own
// teardown is what removes an attempt's worktree, so one that stands is an
// attempt nobody has collected, whether the run is driving it or died beside
// it. Sorted by tick and attempt, so a watcher's two reads differ only in the
// numbers.
func Standing(repo, runID string, now time.Time) ([]Attempt, error) {
	regs, err := worktrees(repo)
	if err != nil {
		return nil, err
	}
	prefix := RefPrefix(runID)
	// A census that succeeded owes an EMPTY answer, not a nil one: null and
	// [] are different claims to the status surface that reads this — "could
	// not be measured" and "no attempt stands".
	out := []Attempt{}
	for _, reg := range regs {
		if !strings.HasPrefix(reg.branch, prefix) {
			continue
		}
		refRun, tick, attempt, ok := ParseAttempt(reg.branch)
		if !ok || refRun != runID {
			continue
		}
		a, _, err := measure(repo, reg, tick, attempt, now)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TickID != out[j].TickID {
			return out[i].TickID < out[j].TickID
		}
		return out[i].Attempt < out[j].Attempt
	})
	return out, nil
}

// AttemptOf measures the attempt one write ref names, locating its worktree
// through the repo's registrations — executor-neutral, from the same facts
// `ticfac status` reads. The ok answer is false only when the ref names no
// attempt of this vocabulary or no branch the repo carries.
func AttemptOf(repo, writeRef string, now time.Time) (Attempt, bool, error) {
	_, tick, attempt, ok := ParseAttempt(writeRef)
	if !ok {
		return Attempt{}, false, nil
	}
	regs, err := worktrees(repo)
	if err != nil {
		return Attempt{}, false, err
	}
	for _, reg := range regs {
		if reg.branch != writeRef {
			continue
		}
		return measure(repo, reg, tick, attempt, now)
	}
	// No registration: a branch with no standing worktree still has a
	// measurable branch fact, and that is the honest half-answer.
	if at, ok := branchMovedAt(repo, writeRef); ok {
		return filled(writeRef, "", tick, attempt, now, at, time.Time{}), true, nil
	}
	return Attempt{TickID: tick, Attempt: attempt, Branch: writeRef}, false, nil
}

// filled assembles one Attempt from its measured facts. A zero time is no
// fact: it stays null rather than being reported as the epoch.
func filled(branch, worktree, tick string, attempt int, now, movedAt, changedAt time.Time) Attempt {
	a := Attempt{TickID: tick, Attempt: attempt, Branch: branch, Worktree: worktree}
	if !movedAt.IsZero() {
		moved := movedAt
		idle := Duration(now.Sub(moved))
		a.BranchMovedAt, a.BranchIdle = &moved, &idle
	}
	if !changedAt.IsZero() {
		changed := changedAt
		idle := Duration(now.Sub(changed))
		a.WorktreeChangedAt, a.WorktreeIdle = &changed, &idle
	}
	return a
}

// measure reads one registration's two facts. A registration whose directory
// is gone keeps its branch fact and loses its worktree fact, which is the
// teardown shape a killed run leaves behind.
func measure(repo string, reg registration, tick string, attempt int, now time.Time) (Attempt, bool, error) {
	movedAt, _ := branchMovedAt(repo, reg.branch)
	changedAt, _ := worktreeChangedAt(reg.worktree)
	a := filled(reg.branch, reg.worktree, tick, attempt, now, movedAt, changedAt)
	// An attempt with neither fact measurable is not one this repo can say
	// anything about.
	if movedAt.IsZero() && changedAt.IsZero() {
		return Attempt{TickID: tick, Attempt: attempt, Branch: reg.branch, Worktree: reg.worktree}, false, nil
	}
	return a, true, nil
}

// registration is one entry of git's worktree census: the directory, and the
// branch it has checked out. A detached or bare worktree carries no branch
// and is no attempt's.
type registration struct {
	worktree string
	branch   string
}

// worktrees is the repo's worktree census, from `git worktree list
// --porcelain` — one call, no locks, safe to run beside the run it observes.
func worktrees(repo string) ([]registration, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var regs []registration
	var reg *registration
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
			reg = nil
		case strings.HasPrefix(line, "worktree "):
			regs = append(regs, registration{worktree: strings.TrimPrefix(line, "worktree ")})
			reg = &regs[len(regs)-1]
		case strings.HasPrefix(line, "branch ") && reg != nil:
			reg.branch = strings.TrimPrefix(line, "branch ")
		}
	}
	return regs, nil
}

// branchMovedAt is the branch tip's committer date: the moment the branch
// last moved. Reading it from the ref (never a rev-parse of a shorthand the
// checkout might re-interpret) keeps the measurement a fact about the ref.
func branchMovedAt(repo, ref string) (time.Time, bool) {
	out, err := git(repo, "log", "-1", "--format=%cI", ref)
	if err != nil {
		return time.Time{}, false
	}
	at, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(out))
	if parseErr != nil {
		return time.Time{}, false
	}
	return at, true
}

// worktreeChangedAt is the newest file mtime under the worktree, .git
// skipped: when the attempt last changed anything in it. Files, not
// directories — a change is a file written, edited or removed, and the git
// plumbing's bookkeeping (the .git name, wherever it appears: a file in a
// linked worktree, a directory anywhere else) is not work. An unreadable or
// empty worktree answers false rather than a guess.
func worktreeChangedAt(dir string) (time.Time, bool) {
	if dir == "" {
		return time.Time{}, false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return time.Time{}, false
	}
	var newest time.Time
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A file the worker removed mid-walk is a change; a walk that
			// cannot read a path keeps going, and the answer is the newest
			// mtime the walk could see.
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil || newest.IsZero() {
		return time.Time{}, false
	}
	return newest, true
}

// git runs one command in the repo and hands back its stdout, with the
// terminal prompt disabled: a measurement that stopped to answer a credential
// prompt would be a watcher that hangs beside the thing it watches.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s (in %s): %w: %s",
			strings.Join(args, " "), dir, err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}
