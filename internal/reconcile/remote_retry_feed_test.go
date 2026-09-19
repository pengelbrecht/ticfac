package reconcile

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// reset is the stderr run epic-ncv died on, host removed.
const reset = "git fetch: exit status 128: Connection reset by remote.invalid port 22\n" +
	"fatal: Could not read from remote repository."

// feedReader is a reconciler with nothing but a feed: enough to prove what a
// retry puts in front of a person, without standing up a run.
func feedReader(t *testing.T, retry runstate.RemoteRetry) (*Reconciler, func() []runfeed.Event) {
	t.Helper()
	repo := t.TempDir()
	at := time.Date(2026, 9, 18, 18, 21, 0, 0, time.UTC)
	r := &Reconciler{runID: "r-enj", now: func() time.Time { at = at.Add(time.Second); return at }}
	r.opts.Repo = repo
	r.opts.RunID = r.runID
	r.opts.RemoteRetry = retry
	r.feed = runfeed.Open(repo, r.runID)
	return r, func() []runfeed.Event {
		t.Helper()
		if err := r.FeedError(); err != nil {
			t.Fatalf("the feed could not be written: %v", err)
		}
		events, err := runfeed.Read(runfeed.Path(repo, r.runID))
		if err != nil {
			t.Fatalf("read the run feed: %v", err)
		}
		return events
	}
}

// TestARetryIsSaidOutLoudInTheRunsFeed.
//
// A silent retry is how a real outage looks healthy. The whole reason run
// epic-ncv needed a person is that nobody could tell a run waiting on a
// remote from a run that had stopped emitting, so a retry that fixed the
// death and said nothing would trade one invisible failure for another.
func TestARetryIsSaidOutLoudInTheRunsFeed(t *testing.T) {
	t.Parallel()
	r, feed := feedReader(t, runstate.RemoteRetry{
		Attempts: 4,
		Backoff:  2 * time.Second,
		Sleep:    func(time.Duration) {},
	})

	tries := 0
	err := r.remoteRetry().Do("git fetch", func() error {
		if tries++; tries == 1 {
			return errors.New(reset)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("one reset still killed the caller: %v", err)
	}

	events := feed()
	if len(events) != 1 {
		t.Fatalf("the feed carries %d lines, want the one retry: %+v", len(events), events)
	}
	line := events[0]
	if line.Stage != StageRemoteRetried {
		t.Errorf("the retry landed as stage %q, want %q", line.Stage, StageRemoteRetried)
	}
	// Run-level: which tick's record the fetch was reading is not what a
	// person needs told about the network.
	if line.TickID != nil {
		t.Errorf("the retry claims tick %q; a remote failure belongs to the run", *line.TickID)
	}
	for _, want := range []string{"git fetch", "attempt 1 of 4", "2s", "Connection reset"} {
		if !strings.Contains(line.Detail, want) {
			t.Errorf("the feed line does not say %q, so a reader cannot tell what happened: %q", want, line.Detail)
		}
	}
}

// TestARunThatGaveUpSaysHowManyTimesItTried.
//
// "The remote reset us" and "the remote reset us four times" are the same
// sentence about two very different remotes, and only the second tells an
// operator the run waited before it stopped.
func TestARunThatGaveUpSaysHowManyTimesItTried(t *testing.T) {
	t.Parallel()
	r, feed := feedReader(t, runstate.RemoteRetry{
		Attempts: 3,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) {},
	})

	err := r.remoteRetry().Do("git fetch", func() error { return errors.New(reset) })
	if err == nil {
		t.Fatal("a remote that never came back returned success")
	}
	if !strings.Contains(err.Error(), "3 times") {
		t.Errorf("the refusal handed back to the run does not name the attempts: %v", err)
	}

	events := feed()
	if len(events) != 3 {
		t.Fatalf("the feed carries %d lines, want two retries and one giving up: %+v", len(events), events)
	}
	if events[0].Stage != StageRemoteRetried || events[1].Stage != StageRemoteRetried {
		t.Errorf("the first two lines are %q and %q, want retries", events[0].Stage, events[1].Stage)
	}
	last := events[2]
	if last.Stage != StageRemoteExhausted {
		t.Errorf("the last line is %q, want %q", last.Stage, StageRemoteExhausted)
	}
	if !strings.Contains(last.Detail, "all 3 attempts") {
		t.Errorf("the line a person reads does not say how many times the run tried: %q", last.Detail)
	}
}

// TestATerminalRemoteFailureLeavesNoRetryInTheFeed.
//
// The feed is the second half of the classification: a run that stopped on a
// rejected key must not show a person a run that spent its bound waiting on
// the network, because that is where they would then go looking.
func TestATerminalRemoteFailureLeavesNoRetryInTheFeed(t *testing.T) {
	t.Parallel()
	r, _ := feedReader(t, runstate.RemoteRetry{
		Attempts: 5,
		Backoff:  time.Millisecond,
		Sleep:    func(time.Duration) { t.Error("the run waited on a key the remote rejected") },
	})

	tries := 0
	err := r.remoteRetry().Do("git push", func() error {
		tries++
		return errors.New("git push: exit status 128: git@remote.invalid: Permission denied (publickey).\n" +
			"fatal: Could not read from remote repository.")
	})
	if err == nil {
		t.Fatal("a rejected key returned success")
	}
	if tries != 1 {
		t.Errorf("the push ran %d times, want exactly one", tries)
	}
	// The feed touches no disk until the first append, so "no file" is the
	// honest answer here and the journal is read instead.
	if journal := r.Journal(); len(journal) != 0 {
		t.Errorf("the run recorded %+v, want nothing: no retry happened", journal)
	}
	if _, err := os.Stat(runfeed.Path(r.opts.Repo, r.runID)); !os.IsNotExist(err) {
		t.Errorf("a feed exists for a run that never retried anything: %v", err)
	}
}
