package reconcile

import (
	"fmt"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// The run's pushes and GitHub errors, told in its feed (tick rlp).
//
// Every push the run makes to a hosted repository goes through the host's
// push queue (gitbin.PushQueue), which records it and tells the run how it
// ended; when the operator has turned pacing on (TICFAC_PUSH_QUEUE=1, off by
// default) it also tells a wait long enough to notice and a wait past the
// queue's bound. Every remote failure GitHub handed back is counted by class,
// a failed push with the host's push log around it.
// docs/analysis/github-failures.md had to reconstruct all of this from
// `published` lines and stderr after the fact; with these lines `ticfac
// status` and the status model count it per run — the measurement that
// decides whether pacing is worth turning on, and what f61 (one push per
// step) bought.
//
// They are FEED lines and not journal events: they are exhaust about the
// transport, never a step of the reconcile, and a run's journal reads the
// same whichever host's queue it ran under.
const (
	// StagePushed is one push to a hosted repository, landed or not, with
	// the time it waited in the queue when the queue paces.
	StagePushed = "pushed"
	// StagePushQueued is a push about to wait in the queue long enough to
	// notice: a run that has gone quiet is waiting on the queue, not hung.
	StagePushQueued = "push_queued"
	// StagePushQueueOverdue is a push whose slot was past the queue's bound:
	// it waits the bound and pushes then. Its own line, because it means
	// more is pushing to the repository than the queue can pace.
	StagePushQueueOverdue = "push_queue_overdue"
	// StageGitHubErrorPrefix prefixes one line per failed remote attempt,
	// the class after it (runstate.GitHubErrorClasses):
	// github_error_ref_update_failed, github_error_network, ...
	StageGitHubErrorPrefix = "github_error_"
)

// pushTally is the run's own count, for the line's detail.
type pushTally struct {
	mu     sync.Mutex
	count  int
	recent []time.Time
}

// note counts one push at `at` and answers the incarnation's count and how
// many of its pushes fall in the trailing minute.
func (p *pushTally) note(at time.Time) (count, lastMinute int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	kept := p.recent[:0]
	for _, t := range p.recent {
		if at.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	p.recent = append(kept, at)
	return p.count, len(p.recent)
}

// pushNotify is what the run's git runners tell the queue to say.
func (r *Reconciler) pushNotify() gitbin.PushNotify {
	return func(e gitbin.PushEvent) {
		switch e.Kind {
		case gitbin.PushWaiting:
			r.feedOnly(StagePushQueued, "a push to %s waits %s in this host's push queue for the repository "+
				"(at most %s): other pushes to it hold the slots before this one", e.Repo, e.Wait.Round(time.Second),
				e.Bound)
		case gitbin.PushOverdue:
			r.feedOnly(StagePushQueueOverdue, "a push to %s would wait %s in this host's push queue, past its "+
				"bound of %s: it pushes at the bound, faster than the queue paces the repository — more is pushing "+
				"to it from this host than one queue can keep under GitHub's rate", e.Repo,
				e.Wait.Round(time.Second), e.Bound)
		case gitbin.PushDone:
			count, minute := r.pushes.note(e.At)
			outcome := "landed"
			if e.Err != nil {
				outcome = "failed"
				// The runner hands the failure to the retry bound next, which
				// classes it and says so (remoteFailed): the host's log around
				// THIS push goes on that line.
				r.pushMu.Lock()
				r.failedPush = &e
				r.pushMu.Unlock()
			}
			queued := ""
			if e.Wait >= time.Millisecond {
				queued = fmt.Sprintf(" after %s in the push queue", e.Wait.Round(time.Millisecond))
			}
			r.feedOnly(StagePushed, "push %d of this incarnation to %s %s%s; %d of this run's in its last minute",
				count, e.Repo, outcome, queued, minute)
		}
	}
}

// remoteFailed is the line for one failed remote attempt, by class. A failed
// PUSH carries what the host's push log says around it (tick rlp): the
// pushes to the repository from this host in its trailing minute, and
// whether another run's push started within five seconds — the two numbers
// that confirm or kill "(failed) is the rate" and "(failed) is the other run".
func (r *Reconciler) remoteFailed(f runstate.RemoteFailure) {
	r.pushMu.Lock()
	push := r.failedPush
	r.failedPush = nil
	r.pushMu.Unlock()
	around := ""
	if push != nil && push.Context != nil && f.What == "git push" {
		c := push.Context
		other := "no other run pushed to it within 5s"
		if c.Other != "" {
			when := "after"
			gap := c.OtherGap
			if gap < 0 {
				when, gap = "before", -gap
			}
			other = fmt.Sprintf("another run (%s) pushed to it %s %s", c.Other, gap.Round(time.Millisecond), when)
		}
		around = fmt.Sprintf(" [%s: %d push(es) from this host in the trailing 60s; %s]", push.Repo,
			c.TrailingMinute, other)
	}
	r.feedOnly(StageGitHubErrorPrefix+f.Class, "%s failed (%s)%s: %v", f.What, f.Class, around, f.Err)
}

// ownRepository is the repository the run's remote names, owner/name, read
// once: the repository a credential the run was handed is minted for (tick
// gy9). Empty when it cannot be read, which makes no refusal cross-repository.
func (r *Reconciler) ownRepository() string {
	r.ownRepoMu.Lock()
	defer r.ownRepoMu.Unlock()
	if r.ownRepo == "" && r.git != nil {
		if self, err := r.thisRepository(); err == nil {
			r.ownRepo = self
		}
	}
	return r.ownRepo
}

// feedOnly writes one line to the run's feed, and nothing to its journal.
func (r *Reconciler) feedOnly(stage, format string, args ...any) {
	if r.feedEnded.Load() {
		// run_finished is the feed's last line, whatever the run's last
		// pushes are (contracts/run-event-feed.json).
		return
	}
	r.feedMu.Lock()
	defer r.feedMu.Unlock()
	r.emit(Event{At: r.now(), Stage: stage, Detail: fmt.Sprintf(format, args...)})
}
