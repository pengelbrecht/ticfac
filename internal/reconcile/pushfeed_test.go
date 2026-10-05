package reconcile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// feedLines reads a feedReader reconciler's whole feed, the per-class lines
// included.
func feedLines(t *testing.T, r *Reconciler) []runfeed.Event {
	t.Helper()
	events, err := runfeed.Read(runfeed.Path(r.opts.Repo, r.runID))
	if err != nil {
		t.Fatalf("read the run feed: %v", err)
	}
	return events
}

// What the push queue tells a run lands in its feed as typed lines (tick
// rlp): every paced push, a wait long enough to notice, and a wait past the
// bound — so a run waiting on the queue reads as waiting on the queue, and
// its pushes can be counted per run.
//
// short: an in-memory feed and hand-made queue events; no git
func TestThePushQueueIsToldInTheRunsFeed(t *testing.T) {
	t.Parallel()
	r, _ := feedReader(t, runstate.RemoteRetry{})
	notify := r.remoteRetry().Pushed
	at := time.Date(2026, 10, 5, 10, 24, 20, 0, time.UTC)
	notify(gitbin.PushEvent{Kind: gitbin.PushWaiting, Repo: "github.com/o/r", At: at, Wait: 9 * time.Second,
		Bound: 2 * time.Minute})
	notify(gitbin.PushEvent{Kind: gitbin.PushDone, Repo: "github.com/o/r", At: at, Wait: 9 * time.Second})
	notify(gitbin.PushEvent{Kind: gitbin.PushOverdue, Repo: "github.com/o/r", At: at, Wait: 3 * time.Minute,
		Bound: 2 * time.Minute})
	notify(gitbin.PushEvent{Kind: gitbin.PushDone, Repo: "github.com/o/r", At: at.Add(time.Second),
		Err: errors.New("exit status 1")})

	events := feedLines(t, r)
	var stages []string
	for _, e := range events {
		stages = append(stages, e.Stage)
		if e.TickID != nil {
			t.Errorf("a push line claims tick %q; the queue belongs to the run", *e.TickID)
		}
	}
	want := []string{StagePushQueued, StagePushed, StagePushQueueOverdue, StagePushed}
	if strings.Join(stages, ",") != strings.Join(want, ",") {
		t.Fatalf("the feed carries %v, want %v", stages, want)
	}
	if !strings.Contains(events[0].Detail, "waits 9s") || !strings.Contains(events[0].Detail, "github.com/o/r") {
		t.Errorf("the queued line does not say how long, or for which repository: %q", events[0].Detail)
	}
	if !strings.Contains(events[2].Detail, "past its bound of 2m0s") {
		t.Errorf("the overdue line does not name the bound: %q", events[2].Detail)
	}
	if !strings.Contains(events[1].Detail, "after 9s in the push queue") {
		t.Errorf("the paced push line does not say how long it queued: %q", events[1].Detail)
	}
	if !strings.Contains(events[3].Detail, "push 2 ") || !strings.Contains(events[3].Detail, "failed") ||
		!strings.Contains(events[3].Detail, "2 of this run's in its last minute") ||
		strings.Contains(events[3].Detail, "queue") {
		t.Errorf("the unpaced second push line does not count it, say it failed, count its minute, or keeps "+
			"quiet about a queue it never waited in: %q", events[3].Detail)
	}
	if len(r.Journal()) != 0 {
		t.Errorf("the queue's lines reached the run's journal: %+v", r.Journal())
	}
}

// Every failed remote attempt is counted by class in the feed, beside the
// retry story the feed already told.
//
// short: the retry policy over a stubbed sleep and an in-memory feed
func TestGitHubErrorsAreCountedByClassInTheRunsFeed(t *testing.T) {
	t.Parallel()
	r, _ := feedReader(t, runstate.RemoteRetry{Attempts: 4, Backoff: time.Millisecond, Sleep: func(time.Duration) {},
		Jitter: func(d time.Duration) time.Duration { return d }})
	failed := " ! [remote rejected] " + strings.Repeat("a", 40) + " -> epic/hn6 (failed)"
	tries := 0
	if err := r.remoteRetry().Do("git push", func() error {
		if tries++; tries == 1 {
			return errors.New(failed)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var counted []string
	for _, e := range feedLines(t, r) {
		if strings.HasPrefix(e.Stage, StageGitHubErrorPrefix) {
			counted = append(counted, e.Stage)
		}
	}
	if strings.Join(counted, ",") != StageGitHubErrorPrefix+runstate.GitHubErrorRefUpdateFailed {
		t.Errorf("the failure was counted as %v, want one %s%s", counted, StageGitHubErrorPrefix,
			runstate.GitHubErrorRefUpdateFailed)
	}
}

// A failed push's GitHub error line carries what the host's push log says
// around it — the trailing minute's pushes and whether another run pushed
// within five seconds — so the data confirms or kills the hypothesis that
// "(failed)" is the rate or the other run (tick rlp).
//
// short: the retry policy over a stubbed sleep and an in-memory feed
func TestAFailedPushIsLoggedWithTheHostsPushesAroundIt(t *testing.T) {
	t.Parallel()
	r, _ := feedReader(t, runstate.RemoteRetry{Attempts: 2, Backoff: time.Millisecond, Sleep: func(time.Duration) {},
		Jitter: func(d time.Duration) time.Duration { return d }})
	retry := r.remoteRetry()
	failed := errors.New(" ! [remote rejected] " + strings.Repeat("a", 40) + " -> epic/43y (failed)")
	tries := 0
	if err := retry.Do("git push", func() error {
		tries++
		if tries == 1 {
			// What the runner's done hands the queue's notify, just before
			// the failure reaches the retry bound.
			retry.Pushed(gitbin.PushEvent{Kind: gitbin.PushDone, Repo: "github.com/o/r", At: time.Now(),
				Err: failed, Context: &gitbin.PushContext{TrailingMinute: 7, Other: "epic-hn6",
					OtherGap: -1400 * time.Millisecond}})
			return failed
		}
		retry.Pushed(gitbin.PushEvent{Kind: gitbin.PushDone, Repo: "github.com/o/r", At: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var line string
	for _, e := range feedLines(t, r) {
		if e.Stage == StageGitHubErrorPrefix+runstate.GitHubErrorRefUpdateFailed {
			line = e.Detail
		}
	}
	for _, want := range []string{"7 push(es) from this host in the trailing 60s",
		"another run (epic-hn6) pushed to it 1.4s before"} {
		if !strings.Contains(line, want) {
			t.Errorf("the error line does not carry %q: %q", want, line)
		}
	}
}

// run_finished stays the feed's last line whatever the run pushes after it.
//
// short: an in-memory feed
func TestNoPushLineFollowsRunFinished(t *testing.T) {
	t.Parallel()
	r, _ := feedReader(t, runstate.RemoteRetry{})
	r.record("", StageRunFinished, "done")
	r.remoteRetry().Pushed(gitbin.PushEvent{Kind: gitbin.PushDone, Repo: "github.com/o/r", At: time.Now()})
	events := feedLines(t, r)
	if last := events[len(events)-1]; last.Stage != StageRunFinished {
		t.Errorf("the feed's last line is %s, want run_finished", last.Stage)
	}
}

// A credential refused by a repository other than the run's own is a named
// stop the supervisor never resumes, and the run's own remote retry does not
// spend a single retry on it (tick gy9).
//
// short: the retry policy over a stubbed sleep and an in-memory feed
func TestACrossRepositoryRefusalIsANamedStopNeverAResume(t *testing.T) {
	t.Parallel()
	r, _ := feedReader(t, runstate.RemoteRetry{Attempts: 4, Backoff: time.Millisecond,
		Sleep: func(time.Duration) { t.Error("a cross-repository refusal waited for a retry") }})
	r.ownRepo = "pengelbrecht/ticfac"
	calls := 0
	err := r.remoteRetry().Do("git push", func() error {
		calls++
		return errors.New("remote: Permission to pengelbrecht/ticks.git denied to ticfac[bot].\n" +
			"fatal: unable to access 'https://github.com/pengelbrecht/ticks.git/': The requested URL returned error: 403")
	})
	if calls != 1 {
		t.Errorf("the refused push was attempted %d times, want once", calls)
	}
	if got := errorStopReason(err); got != StoppedRemoteCrossRepoRefused {
		t.Fatalf("the refusal stops as %q, want %q", got, StoppedRemoteCrossRepoRefused)
	}
	if resumesWithoutAPerson(StoppedRemoteCrossRepoRefused) {
		t.Error("a cross-repository refusal is resumed without a person: the next incarnation's token reaches no further")
	}
	if why := haltReason(supervisedStop{Reason: StoppedRemoteCrossRepoRefused}, supervisedStop{}, 0, 10); why == "" {
		t.Error("the supervisor would continue across a cross-repository refusal")
	}
	var counted []string
	for _, e := range feedLines(t, r) {
		counted = append(counted, e.Stage)
	}
	if strings.Join(counted, ",") != StageGitHubErrorPrefix+runstate.GitHubErrorCrossRepoRefused {
		t.Errorf("the feed carries %v, want only the cross-repository count — no retry line", counted)
	}
}

// 5. THE APP RUNG (tick gy9): a cloud run's only GitHub credential is an
// installation token minted for its own repository, so a finding routed to an
// allowlisted target is backlogged here WITHOUT a push to the target — the
// push run_5c7c made four times could only be refused.
//
// serial: t.Setenv of the token door's variable, which every other routed
// test reads to learn it is not on the App rung.
func TestOnTheAppRungARoutedFindingIsBackloggedWithoutAPushToTheTarget(t *testing.T) {
	t.Setenv(forge.FactoryTokenURLEnv, "https://factory.invalid/api/github/token")

	root := t.TempDir()
	target := newTargetTracker(t, root)
	marker := filepath.Join(root, "target-was-pushed")
	hook := "#!/bin/sh\ntouch \"" + marker + "\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(target, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t, root, "repo", routedGate(target))
	f := newFixture(t, fixtureOptions{mode: "finding_routed", repo: repo})
	routedEpic(t, f)

	r, result, err := f.run(f.Repo, fixtureOptions{mode: "finding_routed"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State != runstate.StateCompleted {
		t.Fatalf("the run ended %s (%+v): a routed finding must never hold a run", result.State, result.Failure)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the run pushed to the target with a token minted for its own repository")
	}
	finding, record := routedFinding(t, f, r)
	assertRuleDecided(t, finding, record)
	assertLocalTrackingTick(t, f, finding, record, "minted for")
}
