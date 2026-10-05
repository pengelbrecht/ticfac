package statusmodel

import (
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/reconcile"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// A run's pushes, its peak pushes in any sixty seconds and its GitHub errors
// by class are counted from the typed lines the queue and the runners wrote
// (tick rlp) — never from prose.
//
// short: counts a hand-made feed
func TestTheModelCountsPushesPeakAndGitHubErrorsByClass(t *testing.T) {
	t.Parallel()
	line := func(at time.Duration, stage string) runfeed.Event {
		return runfeed.NewEvent(testNow.Add(at), "epic-hn6", "", nil, stage, "prose the count never reads")
	}
	feed := []runfeed.Event{
		line(0, reconcile.StagePushed),
		line(10*time.Second, reconcile.StagePushed),
		line(20*time.Second, reconcile.StagePushed),
		line(59*time.Second, reconcile.StagePushed),
		line(70*time.Second, reconcile.StagePushed), // the first has left its minute
		line(5*time.Minute, reconcile.StagePushed),
		line(time.Minute, reconcile.StagePushQueued),
		line(time.Minute, reconcile.StageGitHubErrorPrefix+runstate.GitHubErrorRefUpdateFailed),
		line(time.Minute, reconcile.StageGitHubErrorPrefix+runstate.GitHubErrorRefUpdateFailed),
		line(time.Minute, reconcile.StageGitHubErrorPrefix+runstate.GitHubErrorCrossRepoRefused),
		line(time.Minute, reconcile.StageGitHubErrorPrefix+runstate.GitHubErrorNetwork),
		line(time.Minute, reconcile.StageRemoteRetried),
	}
	h := buildHealth(feed)
	if h.Pushes != 6 || h.PeakPushesPerMinute != 4 {
		t.Errorf("counted %d pushes peaking at %d a minute, want 6 peaking at 4", h.Pushes, h.PeakPushesPerMinute)
	}
	want := GitHubErrors{Network: 1, RefUpdateFailed: 2, CrossRepoRefused: 1}
	if h.GitHubErrors != want || h.GitHubErrors.Total() != 4 {
		t.Errorf("counted GitHub errors %+v, want %+v", h.GitHubErrors, want)
	}
	if h.RemoteRetries != 1 {
		t.Errorf("the push lines disturbed the retry count: %d", h.RemoteRetries)
	}
}
