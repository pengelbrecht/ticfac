package reconcile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runprogress"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The liveness record: what the run watched an attempt DO, kept where it can
// be read afterwards and not put on the feed (tick dh1).
//
// The defect this answers is an evidence defect, not a behaviour one. Two
// attempts of epic ncv were dispatched into the same wave against the same
// 3600s bound on 2026-09-18. 9fc produced 433 lines; ef7 produced an empty
// commit. Their observation logs are the same three lines — started,
// heartbeat at second one, exited at the wall clock — because everything
// between those lines is the AGENT's business and nothing the run observes.
// So "it is thinking hard" and "it wedged in its first minute" read
// identically, and tick ad4 — why did ef7 produce nothing in an hour — cannot
// be answered from what ticfac kept.
//
// The signal was already on disk. The attempt's worktree is registered in the
// repo the run works in, and the stall warning already walks it for its two
// gaps. A gap, though, is the wrong shape for this question: it says how long
// ago the newest thing happened, and for a checkout that has never been
// touched the newest thing happened AT DISPATCH — which is precisely the
// number a wedged agent and a thinking one both produce. A COUNT of the files
// written since dispatch separates them at the first probe: ef7's worktree
// carried zero for sixty minutes, and zero is not a gap, it is an answer.
//
// # SEEN and RECORDED are different surfaces, on purpose
//
// A line per attempt per poll on the run event feed would be, at the local
// executor's five-second cadence and a width of three, some two thousand
// lines an hour saying "still nothing". That is not observability; it is the
// thing that teaches a watcher to stop reading the feed, and the feed's own
// rule already says a line is a hint about WHEN TO LOOK. "Still nothing, as
// of four seconds ago" is never a moment to look.
//
// So the two surfaces carry different things:
//
//   - RECORDED, here, at `.ticfac/logs/<run-id>/liveness.jsonl`: every probe,
//     append-only, beside the feed and under the same `.ticfac/logs/` exhaust
//     entry the run-state contract already carries. Nobody is asked to read
//     it while the run goes. It is what ad4 needed and did not have — the
//     row-by-row account that says the worktree was empty from 16:51 onward,
//     rather than a silence that could have meant anything.
//   - SEEN, on the feed: only the moments a person can act on. The stall
//     warning, which now carries the count and so finally says WHICH kind of
//     quiet it found; and how long an attempt outlived its bound before it
//     settled, because an agent that takes ninety seconds to stop is an agent
//     whose grace period is theatre and nothing said so.
//
// The record is exhaust, like the feed: a run whose liveness record cannot be
// written is a run that is harder to investigate afterwards, never a run that
// must stop. The failure is collected and carried, the same way a feed
// failure is.

// LivenessName is the liveness record's file name, under the run's exhaust
// directory — beside events.jsonl, so one place holds everything a run left
// behind about itself.
const LivenessName = "liveness.jsonl"

// LivenessSchemaVersion is the row schema's version. A reader that meets a
// version it does not know refuses the row rather than guessing at it.
const LivenessSchemaVersion = 1

// Liveness row kinds. `probe` is one measurement of a live attempt; `stop` is
// the run's account of how an attempt that had passed its bound finally ended.
const (
	LivenessProbe = "probe"
	LivenessStop  = "stop"
)

// LivenessPath is where a run's liveness record lives:
// `<repo>/.ticfac/logs/<run-id>/liveness.jsonl`, the same `<run-id>` segment
// that names the feed.
func LivenessPath(repo, runID string) string {
	return filepath.Join(repo, runstate.Root, "logs", runID, LivenessName)
}

// LivenessRow is one line of the record. Every measurement is nullable for
// the same reason runprogress's are: an unmeasured fact is stated as null and
// never as a zero that reads like evidence.
type LivenessRow struct {
	SchemaVersion int    `json:"schema_version"`
	At            string `json:"at"`
	RunID         string `json:"run_id"`
	TickID        string `json:"tick_id"`
	Attempt       int    `json:"attempt"`
	Kind          string `json:"kind"`

	// ChangedFiles is the count this record exists for: files written under
	// the attempt's worktree since the run's first look at it, which is taken
	// when the attempt joins the window. Zero is the wedged answer; null is
	// "not measured" — a worktree the run could not calibrate against — and
	// the two are never the same claim.
	ChangedFiles *int `json:"changed_files"`
	// The gaps the stall warning reads, recorded at every probe rather than
	// only at the one poll that crosses a threshold.
	BranchIdle   *runprogress.Duration `json:"branch_idle"`
	WorktreeIdle *runprogress.Duration `json:"worktree_idle"`
	Idle         *runprogress.Duration `json:"idle"`

	// State is what the executor last said about the attempt, on a `stop`
	// row. WallOverran is how long the attempt outlived its own wall clock
	// before it settled — the run's own arithmetic, from the durable marker's
	// issue stamp, never from an executor's prose. Null on a probe row, and
	// null for an attempt whose bound never fired.
	State       string                `json:"state,omitempty"`
	WallOverran *runprogress.Duration `json:"wall_overran,omitempty"`
	// Detail is the executor's own last word, carried verbatim for the person
	// reading this afterwards and never matched: whether the interrupt was
	// honoured or the pane had to be closed is a sentence only the executor
	// can write, and reading a decision out of it would be the prose-matching
	// this codebase refuses everywhere else.
	Detail string `json:"detail,omitempty"`
}

// appendLiveness writes one row. The write is one O_APPEND write of a
// newline-terminated line, so the file is safe to follow while the run writes
// it — the same discipline the feed keeps.
func (r *Reconciler) appendLiveness(row LivenessRow) {
	row.SchemaVersion = LivenessSchemaVersion
	row.RunID = r.runID
	line, err := json.Marshal(row)
	if err != nil {
		if r.livenessErr == nil {
			r.livenessErr = fmt.Errorf("marshal the liveness row: %w", err)
		}
		return
	}
	path := LivenessPath(r.opts.Repo, r.runID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		if r.livenessErr == nil {
			r.livenessErr = fmt.Errorf("create the run's liveness directory: %w", err)
		}
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		if r.livenessErr == nil {
			r.livenessErr = fmt.Errorf("open the liveness record at %s: %w", path, err)
		}
		return
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil && r.livenessErr == nil {
		r.livenessErr = fmt.Errorf("append to the liveness record: %w", err)
	}
}

// LivenessError is the first error the liveness record produced, if it
// produced one. Exhaust, like the feed: collected and reported, never fatal.
func (r *Reconciler) LivenessError() error { return r.livenessErr }

// probeProgress takes ONE liveness measurement of a live attempt and records
// it.
//
// It is throttled independently of the poll, and that is deliberate. The poll
// cadence is the KEEPALIVE's — five seconds on both local executors, chosen
// so a stop lands within one poll of the bound — while this walks the
// attempt's whole worktree, which on a repository with a node_modules under
// it is tens of thousands of stats. Those are two different jobs with two
// different right answers, and tying the walk to the keepalive would make
// every future shortening of the poll interval silently more expensive. The
// probe interval is the one that must merely be short against the question it
// answers: "has this agent written anything since it started", where a minute
// of resolution is already far finer than the fifteen-minute stall threshold
// or the hour-long bound.
//
// A measurement that cannot be made writes no row. A run that could not read
// a worktree has nothing to say about it, and a row of nulls in an
// append-only record is worse than a gap in it: the gap is honest, and the
// row looks like evidence.
// calibrateProgress takes the run's first look at a newly held attempt's
// worktree and keeps what it found as the baseline every later count is
// measured against. It writes no liveness row: nothing has been observed
// about the attempt yet, and a row saying so would be a row of nulls.
//
// A worktree that cannot be read leaves the baseline zero, and the first
// probe that CAN read one sets it instead — weaker by exactly one probe, and
// honest: an executor whose attempts have no local worktree simply never gets
// a count, which is the same answer runprogress gives everywhere else.
func (r *Reconciler) calibrateProgress(fl *inflightAttempt) {
	if fl == nil || fl.marker.WriteRef == "" {
		return
	}
	gap, ok, err := runprogress.AttemptOf(r.opts.Repo, fl.marker.WriteRef, r.now())
	if err != nil || !ok || gap.WorktreeChangedAt == nil {
		return
	}
	fl.baseline = *gap.WorktreeChangedAt
}

func (r *Reconciler) probeProgress(fl *inflightAttempt) {
	if fl == nil || fl.marker.WriteRef == "" {
		return
	}
	now := r.now()
	if !fl.probedAt.IsZero() && now.Sub(fl.probedAt) < r.progressProbe {
		return
	}
	// The count is measured against the run's own first look at this
	// worktree, never against the dispatch stamp — see inflightAttempt's
	// baseline for the evidence that the stamp sits on the wrong side of the
	// checkout. Until that first look has been taken the gaps are still worth
	// recording and the count stays null: "nobody has counted yet" is a true
	// thing to write down and a zero would not be.
	gap, ok, err := runprogress.AttemptSince(r.opts.Repo, fl.marker.WriteRef, fl.baseline, now)
	if err != nil || !ok {
		return
	}
	fl.probedAt = now
	fl.progress = &gap
	if fl.baseline.IsZero() && gap.WorktreeChangedAt != nil {
		fl.baseline = *gap.WorktreeChangedAt
	}

	row := LivenessRow{
		At: now.UTC().Format(time.RFC3339Nano), TickID: fl.marker.TickID, Attempt: fl.marker.Attempt,
		Kind: LivenessProbe, ChangedFiles: gap.ChangedFiles,
		BranchIdle: gap.BranchIdle, WorktreeIdle: gap.WorktreeIdle,
	}
	if idle, ok := gap.Idle(); ok {
		d := runprogress.Duration(idle)
		row.Idle = &d
	}
	r.appendLiveness(row)
}

// recordStop is the run's own account of how an attempt that had passed its
// bound finally ended (tick dh1).
//
// Both ncv attempts were sent the wall-clock interrupt every five seconds for
// about ninety seconds, ignored it, and had to be stopped by closing the
// pane. Nothing in ticfac said so. The executor's observation stream
// distinguishes the two stops in prose — it is the only party that can, since
// it is the one delivering them — but the RUN can state the fact that makes
// the distinction matter without reading anybody's prose: how long the
// attempt outlived its own bound. An agent that settles a second after the
// interrupt honoured it; one that takes ninety seconds did not, and the grace
// period it spent is the theatre the tick names.
//
// So the run records its own arithmetic — settled-at minus the wall clock
// derived from the durable marker — and carries the executor's last sentence
// beside it, verbatim and never matched. An attempt whose bound never fired
// gets no row: there is no stop to account for.
func (r *Reconciler) recordStop(fl *inflightAttempt, state, detail string) *time.Duration {
	wallAt, ok := r.wallClockAt(fl.marker)
	if !ok {
		return nil
	}
	now := r.now()
	if !now.After(wallAt) {
		return nil
	}
	overran := now.Sub(wallAt)
	d := runprogress.Duration(overran)
	row := LivenessRow{
		At: now.UTC().Format(time.RFC3339Nano), TickID: fl.marker.TickID, Attempt: fl.marker.Attempt,
		Kind: LivenessStop, State: state, WallOverran: &d, Detail: detail,
	}
	if fl.progress != nil {
		row.ChangedFiles = fl.progress.ChangedFiles
	}
	r.appendLiveness(row)
	return &overran
}
