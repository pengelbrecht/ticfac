package reconcile

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/forge"
)

// suspendingForge is the rerunningForge whose CI answers can put the host to
// sleep: the first `pending` it answers arms a suspension, and the run's next
// wait spends it — the wall clock jumps while the wait asked for a poll.
type suspendingForge struct {
	*rerunningForge
	mu    sync.Mutex
	armed bool
}

func (f *suspendingForge) CI(ctx context.Context, pr forge.PullRequest) (forge.CIReport, error) {
	report, err := f.rerunningForge.CI(ctx, pr)
	if err == nil && report.State == forge.CIPending {
		f.mu.Lock()
		f.armed = true
		f.mu.Unlock()
	}
	return report, err
}

func (f *suspendingForge) take() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	armed := f.armed
	f.armed = false
	return armed
}

// epic-6in, 2026-09-28: the close-out re-ran its red go job once, logged
// "pending", and the host went to sleep. For twenty-six minutes the feed said
// nothing while the re-run went red again, and the run looked hung. The run
// must say the host was suspended when a wait took far more wall clock than it
// asked for, and the second red must still go to the repair job — not a hold,
// not a second re-run.
func TestACloseoutWaitThroughASuspendedHostSaysSoAndRepairsTheSecondRed(t *testing.T) {
	t.Parallel()
	pr := &suspendingForge{rerunningForge: &rerunningForge{attempts: map[int64]int{},
		fakeForge: &fakeForge{exists: true, pr: openPR(), ci: []forge.CIReport{
			{State: forge.CIRed, Failing: []string{"go"}, FailingRuns: []int64{42}},
			{State: forge.CIPending},
			{State: forge.CIRed, Failing: []string{"go"}, FailingRuns: []int64{42}},
			{State: forge.CIGreen},
		}}}}
	f := newFixture(t, fixtureOptions{pullRequests: pr})
	declareCloseoutRule(t, f.Repo)

	opts := f.options(f.Repo, fixtureOptions{pullRequests: pr})
	var mu sync.Mutex
	var offset time.Duration
	opts.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return time.Now().Add(offset)
	}
	opts.Sleep = func(time.Duration) {
		if pr.take() {
			mu.Lock()
			offset += 26 * time.Minute // the host slept through this poll
			mu.Unlock()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.RunProtected(context.Background())
	if err != nil {
		t.Fatalf("the run did not finish: %v", err)
	}

	var suspended string
	for _, e := range r.Journal() {
		if e.Stage == StageHostSuspended {
			suspended = e.Detail
		}
	}
	if suspended == "" {
		t.Fatalf("a wait that took 26m of wall clock is silent in the feed: nothing says the host was suspended, "+
			"so the silence reads as a hung run (stages %v)", r.Stages(""))
	}
	if !strings.Contains(suspended, "26m") {
		t.Errorf("the suspension line does not say how long the wait took: %s", suspended)
	}
	if fmt.Sprint(pr.reruns) != "[42]" {
		t.Errorf("re-ran %v, want workflow run 42 exactly once", pr.reruns)
	}
	if !anyTickHasStage(r, StageRepairDispatched) {
		t.Errorf("the red CI after the re-run was not answered with the repair job (%+v)", result.Failure)
	}
	if result.Failure != nil && result.Failure.Reason == RefusedCloseoutCI {
		t.Errorf("the second red held the close-out (%s) instead of going to the repair job", result.Failure.Reason)
	}
}

// short: pure arithmetic over a fake clock; no repository, no process.
func TestAWaitSleepsAgainstTheWallClockNotTheAwakeClock(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 28, 15, 45, 36, 0, time.UTC)

	// The host sleeps through the first slice: 17 minutes of wall clock pass
	// while the wait asked for 15 seconds. The five-minute poll is owed at
	// once, not after another 4m45s of awake time.
	wall := start
	var asked []time.Duration
	sleepByWallClock(5*time.Minute, func() time.Time { return wall }, func(d time.Duration) {
		asked = append(asked, d)
		if len(asked) == 1 {
			wall = wall.Add(17 * time.Minute)
			return
		}
		wall = wall.Add(d)
	})
	if len(asked) != 1 {
		t.Errorf("after the host woke 17m into a 5m wait, the wait slept %v more; it should have returned", asked[1:])
	}

	// A clock that tracks the sleeps ends the wait on time, in slices no
	// longer than wallSlice.
	wall, asked = start, nil
	sleepByWallClock(time.Minute, func() time.Time { return wall }, func(d time.Duration) {
		asked = append(asked, d)
		wall = wall.Add(d)
	})
	var total time.Duration
	for _, d := range asked {
		if d > wallSlice {
			t.Errorf("a slice of %s is longer than %s: a woken host would wait that long to poll", d, wallSlice)
		}
		total += d
	}
	if total != time.Minute {
		t.Errorf("a 1m wait slept %s in total", total)
	}

	// A clock that never moves (a test's fixed now) still ends the wait.
	asked = nil
	sleepByWallClock(time.Minute, func() time.Time { return start }, func(d time.Duration) { asked = append(asked, d) })
	total = 0
	for _, d := range asked {
		total += d
	}
	if total != time.Minute {
		t.Errorf("against a fixed clock a 1m wait asked for %s of sleep; it must end after 1m", total)
	}
}
