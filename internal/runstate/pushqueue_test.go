package runstate

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

// With the push queue's pacing turned on (TICFAC_PUSH_QUEUE=1; it is off by
// default), two runs on one host, writing their records to one origin at the
// same time, never push in the same pacing slot (tick rlp).
//
// This is the shape docs/analysis/github-failures.md measured: hn6 and 43y
// writing DIFFERENT integration branches of one repository, and 7 of the 8
// bare "(failed)" rejections landing within five seconds of the other run's
// push. Here each run is its own store in its own clone, on its own branch,
// and the origin is a real bare repository whose pre-receive hook is the fake
// remote: it counts every push it receives and records any push that arrives
// while another is still being received. The queue's pacing is scaled down
// (a 1.5s window instead of a minute) so the test runs in seconds; the
// arithmetic is the production arithmetic.
func TestTwoRunsOnOneOriginNeverPushInTheSamePacingSlot(t *testing.T) {
	o := newOrigin(t)
	if runtime.GOOS == "windows" {
		t.Skip("the counting hook is a /bin/sh script")
	}
	gitRun(t, o.bare, "branch", "epic/two", o.branch)

	// The fake remote: one line per received push, and a line in overlaps
	// for any push received while another was still being received.
	marks := t.TempDir()
	hook := "#!/bin/sh\n" +
		"cat >/dev/null\n" +
		"if ! mkdir \"" + filepath.Join(marks, "inflight") + "\" 2>/dev/null; then echo overlap >> \"" +
		filepath.Join(marks, "overlaps") + "\"; fi\n" +
		"echo push >> \"" + filepath.Join(marks, "count") + "\"\n" +
		"sleep 0.05\n" +
		"rmdir \"" + filepath.Join(marks, "inflight") + "\" 2>/dev/null\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(o.bare, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	policy := gitbin.PushPolicy{
		Pace:        true,
		PerWindow:   3,
		Window:      1500 * time.Millisecond,
		MinGap:      150 * time.Millisecond,
		MaxWait:     time.Minute,
		NoticeAfter: time.Hour,
		Dir:         t.TempDir(),
		PaceLocal:   true,
	}
	defer gitbin.SetPushPolicy(policy)()

	var mu sync.Mutex
	starts := map[string][]time.Time{}
	run := func(name, branch string) *Store {
		dir := filepath.Join(o.root, "run-"+name)
		gitRun(t, o.root, "clone", "--quiet", "--no-checkout", o.bare, dir)
		s, err := Open(Options{
			Repo:   dir,
			Remote: "origin",
			Branch: branch,
			RunID:  testRun,
			Now:    time.Now,
			RemoteRetry: RemoteRetry{Pushed: func(e gitbin.PushEvent) {
				if e.Kind != gitbin.PushDone {
					return
				}
				if e.Err != nil {
					t.Errorf("run %s: a paced push failed: %v", name, e.Err)
				}
				mu.Lock()
				defer mu.Unlock()
				starts[name] = append(starts[name], e.At)
			}},
		})
		if err != nil {
			t.Fatalf("open run %s: %v", name, err)
		}
		return s
	}
	runs := map[string]*Store{"hn6": run("hn6", o.branch), "43y": run("43y", "epic/two")}

	const writes = 4
	var wg sync.WaitGroup
	for name, s := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 1; i <= writes; i++ {
				if _, err := s.PutAttempt(testAttempt(i, "t"+name)); err != nil {
					t.Errorf("run %s, write %d: %v", name, i, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	var all []time.Time
	for name, at := range starts {
		if len(at) != writes {
			t.Errorf("run %s made %d paced pushes, want %d", name, len(at), writes)
		}
		all = append(all, at...)
	}
	raw, _ := os.ReadFile(filepath.Join(marks, "count"))
	if got := strings.Count(string(raw), "push"); got != len(all) {
		t.Errorf("the remote received %d pushes, the runs report %d paced: a push went around the queue", got,
			len(all))
	}
	if overlaps, err := os.ReadFile(filepath.Join(marks, "overlaps")); err == nil {
		t.Errorf("the remote received %d push(es) while another was in flight", strings.Count(string(overlaps), "overlap"))
	}
	// A push starts at its slot or after it — a sleeper wakes late, and a
	// push waits for the one in flight before it — so the start times are
	// read with a scheduler's slack. The slots themselves are held exactly
	// (gitbin's TestTwoRunsReservingAtOnceNeverShareASlot); what this test
	// adds is that real pushes from two runs follow them, and the remote's
	// own count above that none ever overlapped.
	const slack = 60 * time.Millisecond
	sort.Slice(all, func(i, j int) bool { return all[i].Before(all[j]) })
	for i := 1; i < len(all); i++ {
		if gap := all[i].Sub(all[i-1]); gap < policy.MinGap-slack {
			t.Errorf("pushes %d and %d started %s apart, inside one pacing slot (%s)", i-1, i, gap, policy.MinGap)
		}
	}
	for i := range all {
		n := 0
		for j := i; j < len(all) && all[j].Sub(all[i]) < policy.Window-slack; j++ {
			n++
		}
		if n > policy.PerWindow {
			t.Errorf("%d pushes reached the remote inside one %s window, want at most %d", n, policy.Window,
				policy.PerWindow)
		}
	}
}

// A push the remote took and did not apply — "(failed)", the class that
// tracked the other run's push — waits a jittered interval before its retry,
// so two runs that collided do not collide again on the same schedule; every
// failed attempt is counted by class.
//
// short: a fake op and a pinned jitter; no git
func TestARefUpdateFailedRetryWaitsAJitteredInterval(t *testing.T) {
	t.Parallel()
	failed := errors.New("git push origin x:refs/heads/epic/hn6: exit status 1: To github.com:o/r.git\n" +
		" ! [remote rejected] " + strings.Repeat("a", 40) + " -> epic/hn6 (failed)\nerror: failed to push some refs")
	var waits []time.Duration
	var jittered []time.Duration
	var classes []string
	calls := 0
	err := RemoteRetry{
		Attempts: 4,
		Backoff:  2 * time.Second,
		Sleep:    func(d time.Duration) { waits = append(waits, d) },
		Jitter: func(d time.Duration) time.Duration {
			jittered = append(jittered, d)
			return d / 3
		},
		Failed: func(f RemoteFailure) { classes = append(classes, f.Class) },
	}.Do("git push", func() error {
		calls++
		if calls < 3 {
			return failed
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the push did not survive two (failed) rejections: %v", err)
	}
	if want := []time.Duration{2 * time.Second, 4 * time.Second}; !equalDurations(jittered, want) {
		t.Errorf("the jitter was asked to spread %v, want the backoff %v", jittered, want)
	}
	if want := []time.Duration{2 * time.Second / 3, 4 * time.Second / 3}; !equalDurations(waits, want) {
		t.Errorf("the retries waited %v, want the jittered %v", waits, want)
	}
	if strings.Join(classes, ",") != GitHubErrorRefUpdateFailed+","+GitHubErrorRefUpdateFailed {
		t.Errorf("the failures were counted as %q", classes)
	}

	// A network failure keeps its deterministic backoff.
	waits = nil
	calls = 0
	_ = RemoteRetry{Attempts: 2, Backoff: time.Second, Sleep: func(d time.Duration) { waits = append(waits, d) },
		Jitter: func(time.Duration) time.Duration { t.Error("a network failure was jittered"); return 0 }}.
		Do("git fetch", func() error {
			calls++
			if calls == 1 {
				return errors.New("ssh: connect to host github.com port 22: Connection timed out")
			}
			return nil
		})
	if !equalDurations(waits, []time.Duration{time.Second}) {
		t.Errorf("a network retry waited %v, want 1s", waits)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// crossRepo403 is exactly what run_5c7c got pushing a finding routed to the
// ticks repository with an installation token minted for ticfac.
const crossRepo403 = "git push --quiet https://github.com/pengelbrecht/ticks.git abc:refs/heads/main: exit status 128: " +
	"remote: Permission to pengelbrecht/ticks.git denied to ticfac[bot].\n" +
	"fatal: unable to access 'https://github.com/pengelbrecht/ticks.git/': The requested URL returned error: 403"

// A credential refused by a repository other than the one it was minted for
// is refused at once, by name, with the repository in it — never retried as
// a blip (tick gy9). The same 403 from the run's OWN repository keeps the
// small auth bound, because those were blips (epic hn6, 2026-09-29).
//
// short: a fake op; no git
func TestACrossRepositoryRefusalIsNeverRetried(t *testing.T) {
	t.Parallel()
	calls := 0
	var classes []string
	err := RemoteRetry{
		Attempts:      4,
		Backoff:       time.Millisecond,
		Sleep:         func(time.Duration) { t.Error("a cross-repository refusal waited to be retried") },
		OwnRepository: func() string { return "pengelbrecht/ticfac" },
		Failed:        func(f RemoteFailure) { classes = append(classes, f.Class) },
	}.Do("git push", func() error { calls++; return errors.New(crossRepo403) })
	if calls != 1 {
		t.Errorf("the refused push was attempted %d times, want once", calls)
	}
	var cross *RemoteCrossRepoRefusedError
	if !errors.As(err, &cross) || cross.Repo != "pengelbrecht/ticks" || cross.Own != "pengelbrecht/ticfac" {
		t.Fatalf("the refusal came back as %v, want a RemoteCrossRepoRefusedError naming ticks and ticfac", err)
	}
	if !strings.HasPrefix(err.Error(), RemoteCrossRepoRefusedClass+": ") ||
		!strings.Contains(err.Error(), "pengelbrecht/ticks") {
		t.Errorf("the refusal does not lead with its class and name the repository: %v", err)
	}
	if ClassifyRemote(err) == RemoteTransient {
		t.Errorf("the refusal classifies as transient")
	}
	if strings.Join(classes, ",") != GitHubErrorCrossRepoRefused {
		t.Errorf("the refusal was counted as %q, want one %s", classes, GitHubErrorCrossRepoRefused)
	}

	// The run's own repository refusing it is the token blip it always was.
	calls = 0
	own := strings.ReplaceAll(crossRepo403, "ticks", "ticfac")
	err = RemoteRetry{
		Attempts: 4, Backoff: time.Millisecond, Sleep: func(time.Duration) {},
		OwnRepository: func() string { return "pengelbrecht/ticfac" },
	}.Do("git push", func() error {
		calls++
		if calls < 3 {
			return errors.New(own)
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("the own repository's 403 was not retried as a blip: %d calls, %v", calls, err)
	}

	// With the credential's repository unknown nothing is cross-repository.
	if _, cross := CrossRepoRefusal(errors.New(crossRepo403), ""); cross {
		t.Error("a refusal with no known own repository read as cross-repository")
	}
}

// short: string matching only
func TestGitHubErrorsAreCountedByClass(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		text string
		want string
	}{
		{" ! [remote rejected] abc -> epic/hn6 (failed)", GitHubErrorRefUpdateFailed},
		{"remote: fatal error in commit_refs\n ! [remote rejected] abc -> epic/2jn (failure)", GitHubErrorServerFault},
		{"fatal: unable to access 'https://github.com/o/r/': The requested URL returned error: 502", GitHubErrorServerFault},
		{"ssh: Could not resolve hostname github.com: nodename nor servname provided", GitHubErrorNetwork},
		{"Connection reset by 140.82.121.4 port 22", GitHubErrorNetwork},
		{"git@github.com: Permission denied (publickey).", GitHubErrorAuthRefused},
		{crossRepo403, GitHubErrorCrossRepoRefused},
		{" ! [rejected] abc -> epic/hn6 (stale info)", ""},
		{"fatal: couldn't find remote ref refs/heads/nope", ""},
	} {
		if got := GitHubErrorClass(errors.New(c.text), "pengelbrecht/ticfac"); got != c.want {
			t.Errorf("GitHubErrorClass(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
