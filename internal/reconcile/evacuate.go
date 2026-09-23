package reconcile

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The eviction flush (tick ppt).
//
// The platform's eviction is graceful and the runway is enormous: SIGTERM to
// the main process, up to fifteen minutes of wait for that process to exit,
// then SIGKILL. Fifteen minutes is far more than enough to commit and push;
// using thirty seconds of it converts most evictions from "lose a tick" into
// "lose nothing". Nothing used that window before — the run keeper pushes the
// run branch on a timer and the supervisor pushes each attempt's branch on
// its own, so durability depended on when the last timer fired, and a worker
// killed between fires lost everything its commits had not yet carried away.
//
// Evacuate is what `ticfac run-epic`'s SIGTERM handler calls before the
// process exits: commit what is in the worktrees, push, write the checkpoint,
// then exit — with a BOUND, because the window is not this flush's to spend.
// A hung push that ate the whole grace window would reach SIGKILL anyway and
// the flush would have bought nothing; the bound is what makes the flush an
// improvement rather than a risk. The window is for making the disk's loss
// survivable, not for finishing work: the attempts are not resumed here, no
// gate runs, nothing is collected. The resumed run re-derives everything from
// origin's markers, exactly as a crash would — the flush only arranges for
// there to be more of the work on origin when it does.

// DefaultEvacuationBudget is how much of the platform's grace window a SIGTERM
// flush may spend. Thirty seconds is the tick's own number: it converts most
// evictions — the ones with an attempt or two in flight and a reachable
// remote — into "lose nothing", while leaving fourteen and a half minutes of
// the window unspent for the SIGKILL that may still come.
const DefaultEvacuationBudget = 30 * time.Second

// Evacuate commits and pushes the run's in-flight work, then writes a final
// checkpoint, all inside budget. It is what a SIGTERM runs before the process
// exits; it is safe to call exactly once, from any goroutine, beside a run in
// flight, because it shares NOTHING with the running reconciler's mutable
// state — it reads only construction-time configuration and the durable
// records on disk, and it writes through its own run-state store, whose
// compare-and-swap is the guard against the run itself moving the branch
// underneath it.
//
// It answers with the account of what it did, one line per fact, in the
// order it did it — the last line is a summary, folded by the caller into the
// run's death record. Nothing here is an error out: an evacuation step that
// cannot be done is SAID and skipped, because the alternative — stopping the
// flush — is exactly how a hung push would eat the window the bound exists to
// protect. The process is exiting anyway; the account is what it says on the
// way out.
func (r *Reconciler) Evacuate(signal string, budget time.Duration) []string {
	if budget <= 0 {
		budget = DefaultEvacuationBudget
	}
	var lines []string
	say := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	say("stopping on %s with a %s budget to make the disk's loss survivable", signal, budget)
	started := time.Now()
	deadline := started.Add(budget)

	// The in-flight attempts: worktrees this container owns whose branches
	// exist nowhere else. A settled attempt already pushed before its
	// settlement was recorded, so only the live ones are worth the budget.
	attempts := r.evacuationAttempts()
	if len(attempts) == 0 {
		say("no in-flight attempts under the state root: nothing to flush but the checkpoint")
	}

	// Fair shares, computed as the flush goes: each attempt gets a slice of
	// what REMAINS, leaving one slice of the whole budget for the checkpoint,
	// so one attempt's hung push cannot starve the run's last durable fact.
	// A step that finishes early hands its unspent time to the next, which is
	// why the slice is recomputed per attempt rather than fixed up front.
	for i, attempt := range attempts {
		remaining := time.Until(deadline)
		// One slice for each attempt still to come, and one for the
		// checkpoint: the last durable fact is never the thing a hung
		// push starves.
		share := remaining / time.Duration(len(attempts)-i+1)
		stopAt := time.Now().Add(share)
		if remaining <= 0 {
			say("%s attempt %d: no budget left to flush its worktree", attempt.tickID, attempt.attempt)
			continue
		}
		r.evacuateAttempt(attempt, stopAt, say)
	}
	r.evacuationCheckpoint(signal, deadline, say)
	say("evacuation finished in %s", time.Since(started).Round(time.Millisecond))
	return lines
}

// evacuationAttempt is one in-flight attempt the flush found on disk: where
// its state lives and where its work is.
type evacuationAttempt struct {
	tickID  string
	attempt int
	state   string
	work    subprocess.AttemptWork
}

// evacuationAttempts walks this run's executor state for attempts whose
// worktree is still live: the attempt record exists, the runner has not
// settled, and the worktree it names is still on this disk. Only THIS run's
// subtree is walked — a state root shared by several runs belongs to several
// processes, and each one handles its own signal.
//
// Worktree directories are pruned from the walk, the way the harness's own
// state walk prunes them: an attempt's worktree is a full checkout, and
// walking it would read every file in the repository to find no
// attempt.json.
func (r *Reconciler) evacuationAttempts() []evacuationAttempt {
	root := filepath.Join(r.opts.ExecStateRoot, r.runID)
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.IsDir() {
			if entry.Name() == "attempt.json" {
				dirs = append(dirs, filepath.Dir(path))
			}
			return nil
		}
		switch entry.Name() {
		case "worktree", "locks":
			return fs.SkipDir
		}
		return nil
	})

	var out []evacuationAttempt
	for _, dir := range dirs {
		if subprocess.AttemptSettled(dir) {
			continue
		}
		work, err := subprocess.ReadAttemptWork(dir)
		if err != nil {
			// A record that cannot be read cannot be flushed, and a flush
			// that stopped for it would be a flush that stopped. Said and
			// skipped, like every other step.
			continue
		}
		if _, err := os.Stat(work.Worktree); err != nil {
			// The worktree is gone: the attempt's durability is not this
			// flush's to arrange, and pushing a branch whose worktree no
			// longer exists would be a claim about nothing.
			continue
		}
		if work.Remote == "" {
			work.Remote = r.opts.Remote
		}
		out = append(out, evacuationAttempt{tickID: work.TickID, attempt: work.Attempt, state: dir, work: work})
	}
	// Deterministic order, so the account reads the same whatever the
	// filesystem handed the walk.
	sort.Slice(out, func(i, j int) bool {
		if out[i].tickID != out[j].tickID {
			return out[i].tickID < out[j].tickID
		}
		return out[i].attempt < out[j].attempt
	})
	return out
}

// evacuateAttempt commits what is in one attempt's worktree and pushes the
// branch, bounded by stopAt.
//
// The commit is a SNAPSHOT, not a verdict: the runner may still be writing
// (it was never signalled), so what lands is whatever the worktree held at
// the moment of the `git add`. A half-written state committed to a scratch
// branch is worth more than a whole one that died on a disk nothing will read
// again — the branch is never merged unproven, and the resumed run's gate
// decides on it the way it decides on any attempt's work.
//
// The push is plain, never forced — the same contract the supervisor's own
// timer pushes under: this attempt is the only writer of its ref, and a
// non-fast-forward is something to fail on rather than overwrite.
func (r *Reconciler) evacuateAttempt(attempt evacuationAttempt, stopAt time.Time, say func(string, ...any)) {
	name := fmt.Sprintf("%s attempt %d", attempt.tickID, attempt.attempt)
	work := attempt.work

	dirty, err := r.evacGit(work.Worktree, stopAt, "status", "--porcelain", "-uall")
	if err != nil {
		say("%s: could not read its worktree (%s); pushing whatever is committed", name, firstLine(err.Error()))
	} else if dirty != "" {
		_, _ = r.evacGit(work.Worktree, stopAt, "add", "-A")
		_, commitErr := r.evacGit(work.Worktree, stopAt, "commit", "-q", "-m",
			"ticfac: evacuation snapshot of uncommitted work — the container was stopped mid-attempt")
		if commitErr != nil {
			// The runner may hold the index, or be committing itself; the
			// work it already committed is still worth pushing.
			say("%s: could not snapshot its uncommitted work (%s); pushing whatever is committed",
				name, firstLine(commitErr.Error()))
		} else {
			say("%s: snapshotted its uncommitted work", name)
		}
	}

	if work.Remote == "" {
		say("%s: its record names no remote to push to; the work stays on this disk", name)
		return
	}
	if _, err := r.evacGit(work.Worktree, stopAt, "push", work.Remote, "HEAD:refs/heads/"+work.Branch); err != nil {
		say("%s: could not push %s (%s)", name, work.Branch, firstLine(err.Error()))
		return
	}
	say("%s: pushed %s to %s", name, work.Branch, work.Remote)
}

// evacCheckpointTries bounds the final checkpoint's re-derivation loop: the
// run the flush is beside writes a checkpoint per stage transition, so a busy
// one can lose the compare-and-swap more than once — but a run that moves the
// branch three times inside one flush is a run the flush stops racing, on
// the same reasoning as the store's own maxContendedPushes.
const evacCheckpointTries = 3

// evacuationCheckpoint writes the run's last durable fact — the run stopped
// here, on this signal, and is resumable — through a run-state store of this
// flush's own, because the running reconciler's store is not safe to share
// between goroutines and the compare-and-swap needs no help besides that.
//
// The checkpoint is the run's POSITION, never permission to redo anything:
// the resumed run trusts the markers, the tracker and the evidence records on
// origin, exactly as a crash's resume does. What the checkpoint adds is the
// sentence a restart otherwise has to guess — the container was stopped
// mid-run — and, for a run that had not yet written a single checkpoint, the
// first one to read.
//
// A terminal checkpoint is left exactly alone: a run that finished or failed
// has nothing to resume, and "running" written over it would be a lie a
// restart believed. The ticks of the previous checkpoint are carried, for the
// same reason: a flush that dropped them would make the resumed run's own
// first checkpoint a downgrade of the truth.
func (r *Reconciler) evacuationCheckpoint(signal string, deadline time.Time, say func(string, ...any)) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		say("no budget left for the checkpoint: the durable markers on origin are the resume's authority")
		return
	}
	store, err := runstate.Open(runstate.Options{
		Repo: r.opts.Repo, Remote: r.opts.Remote, Branch: r.branch, RunID: r.runID, Now: time.Now,
	})
	if err != nil {
		say("could not open the run-state store for the final checkpoint: %s", firstLine(err.Error()))
		return
	}

	// The write runs in a goroutine the caller does not wait for past the
	// deadline: a store whose fetch or push hangs mid-flight is abandoned,
	// not joined — the process is exiting, and the bound is the whole point.
	// The account the goroutine produces is handed back whole over a channel,
	// because appending to the caller's slice from two goroutines is a race
	// the deadline branch would lose silently.
	done := make(chan []string, 1)
	go func() {
		var out []string
		note := func(format string, args ...any) {
			out = append(out, fmt.Sprintf(format, args...))
		}
		defer func() { done <- out }()

		if _, err := store.Fetch(); err != nil {
			note("could not read the run state for the final checkpoint: %s", firstLine(err.Error()))
			return
		}
		// The run being flushed is still running, and it writes checkpoints of
		// its own as its stages move. A write that lost the compare-and-swap to
		// one of those re-derives from what actually landed and tries again,
		// because the flush's sentence — the run stopped, on this signal — is
		// the one durable fact only this write carries. The tries are bounded
		// inside a goroutine the deadline already bounds from outside.
		for try := 1; ; try++ {
			previous, exists, err := store.Checkpoint()
			if err != nil {
				note("could not read the checkpoint for the final write: %s", firstLine(err.Error()))
				return
			}
			if exists && previous.State.Terminal() {
				note("the checkpoint is already terminal (%s): not rewritten", previous.State)
				return
			}

			var ticks []runstate.TickState
			provenance := runstate.Provenance{}
			if exists {
				ticks, provenance = previous.Ticks, previous.Provenance
			} else {
				// No checkpoint yet: the run was cut short before it wrote
				// even its admission. The provenance still has to name a
				// source — a record that does not proves nothing — so the base
				// this run was configured with is resolved, bounded, and a
				// base that does not resolve skips the write rather than
				// fabricating one.
				sha, err := r.evacGit(r.opts.Repo, deadline, "rev-parse", "--verify",
					"--end-of-options", r.opts.BaseRef+"^{commit}")
				if err != nil {
					note("no checkpoint to carry and the base %s would not resolve: no final checkpoint written (%s)",
						r.opts.BaseRef, firstLine(err.Error()))
					return
				}
				provenance = runstate.Provenance{
					RunID: r.runID, SourceRef: r.opts.BaseRef, SourceSHA: sha,
					IntegrationRef: runstate.Ptr(refFor(r.branch)), Phase: runstate.PhaseWorker,
				}
			}
			reason := fmt.Sprintf("stopped by %s: the container was evicted mid-run; the run is resumable", signal)
			outcome, err := store.PutCheckpoint(runstate.Checkpoint{
				RunID: r.runID, EpicID: r.opts.EpicID, State: runstate.StateRunning,
				Reason: reason, Ticks: ticks, Provenance: provenance,
			})
			if err != nil {
				note("the final checkpoint could not be written: %s", firstLine(err.Error()))
				return
			}
			if outcome.IsConflict() {
				if try < evacCheckpointTries {
					note("the run state moved under the flush while writing the final checkpoint (%s): re-deriving from what landed", outcome)
					if _, err := store.Fetch(); err != nil {
						note("could not re-read the run state after it moved: %s", firstLine(err.Error()))
						return
					}
					continue
				}
				note("the run state moved under the flush and stayed moving: the winner's record stands")
				return
			}
			if outcome == runstate.NoChange {
				note("the checkpoint already says this: nothing to write")
				return
			}
			note("checkpoint written: %s", reason)
			return
		}
	}()

	select {
	case out := <-done:
		for _, line := range out {
			say("%s", line)
		}
	case <-time.After(remaining):
		say("the checkpoint write did not finish inside the evacuation budget: the durable markers on origin are the resume's authority")
	}
}

// evacWaitDelay bounds how long an abandoned command's wait may be held
// open by output a grandchild still holds. The kill below takes the group,
// so the delay is only the backstop on the one case it does not cover — and
// it is seconds, not the gate's five, because the flush's whole budget is
// thirty of them.
const evacWaitDelay = time.Second

// evacGit runs one bounded git command and returns its trimmed stdout. The
// environment is the run's own stated one — no host hooks, no maintenance, no
// rerere, no prompts, and a bounded transport — because the flush is part of a
// run, and a host's git configuration reaching into it is the same defect here
// as anywhere else the run starts a git (tick 6na).
//
// The bound is the whole point of the flush, so it is enforced twice: the
// context kills the command at the deadline, and the command runs as a
// process GROUP leader so the kill takes git's own children too. A push is
// not one process — `git push` over a network transport spawns a remote
// helper that inherits the pipes — and killing only git leaves the helper
// holding the pipe Wait reads, which is how a "bounded" push outlives its
// bound (the gate learned this as tick 9pz's wait; the rule is its). WaitDelay
// is the last backstop, for the kill that cannot reach.
func (r *Reconciler) evacGit(dir string, deadline time.Time, args ...string) (string, error) {
	base := append([]string{
		"-c", "user.name=" + r.git.name,
		"-c", "user.email=" + r.git.email,
		"-c", "commit.gpgsign=false",
	}, append(append([]string{}, gitbin.NoAutoMaintenance...), gitbin.NoRerere...)...)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	cmd := osexec.CommandContext(ctx, gitbin.Path(), append(base, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(append(append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0"),
		"GIT_PAGER=cat"),
		"GIT_OPTIONAL_LOCKS=0"),
		runstate.TransportEnv()...)
	cmd.SysProcAttr = gateProcessGroup()
	cmd.Cancel = func() error { return killGateGroup(cmd.Process.Pid) }
	cmd.WaitDelay = evacWaitDelay
	var outBuf, errBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("did not finish inside its bound: %w", ctx.Err())
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), firstLine(errBuf.String()))
	}
	return strings.TrimSpace(outBuf.String()), nil
}
