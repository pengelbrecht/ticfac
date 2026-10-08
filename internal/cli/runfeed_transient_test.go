package cli

// Tick vy8: an attached watch of a cloud run died on ONE bad read of the
// factory's run feed while the run kept going — twice on 2026-10-07:
//
//   - ~15:00 UTC, a local DNS outage: `ticfac watch: the factory's run feed
//     for run_69f8…: not a run event feed: invalid character 'i' looking for
//     beginning of value`;
//   - ~16:15 UTC, a factory exception: `factory returned HTTP 500: A Worker
//     script configured by the website owner threw an unhandled exception`.
//
// A read that fails to connect or to parse, or that the factory failed
// answering, is transient: the watch keeps what it has, says it is stale,
// backs off and asks again. Only a definitive answer (404 run unknown,
// 401/403) ends it.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// workerExceptionBody is the text workerd answers with, as a 500, for an
// exception a Worker's route let escape — the incident's own body.
const workerExceptionBody = "A Worker script configured by the website owner threw an unhandled exception while processing this request."

const transientRunID = "run_62c289d1478fae4b1d5c7a2e3f0a9b8c"

// fastFeedRetries makes the backoff milliseconds, not seconds.
func fastFeedRetries(t *testing.T) {
	t.Helper()
	base, max := feedRetryBase, feedRetryMax
	t.Cleanup(func() { feedRetryBase, feedRetryMax = base, max })
	feedRetryBase, feedRetryMax = 5*time.Millisecond, 20*time.Millisecond
}

// feedPage answers one request of the paged route over text, honouring the
// request's `from` cursor — the shape the factory serves.
func feedPage(request cloudFactoryRequest, text, state string) map[string]any {
	from := 0
	if raw := request.Query.Get("from"); raw != "" {
		from, _ = strconv.Atoi(raw)
	}
	if from > len(text) {
		from = len(text)
	}
	page := text[from:]
	return map[string]any{
		"run_id": transientRunID, "state": state, "text": page,
		"from": from, "bytes": len(page), "next": len(text), "total_bytes": len(text), "more": false,
	}
}

func transientFeedLines(t *testing.T) (dispatched, finished string) {
	attempt := 1
	at := "2026-10-07T16:15:00Z"
	stamp, _ := time.Parse(time.RFC3339, at)
	dispatched = feedLine(t, runfeed.NewEvent(stamp, transientRunID, "t1", &attempt, "dispatched", "t1 try 1 dispatched"))
	finished = feedLine(t, runfeed.NewEvent(stamp, transientRunID, "", nil, "run_finished", "completed: every tick closed"))
	return dispatched, finished
}

// TestWatchRetriesTheIncidentsBadReadsBeforeItsFirstFrame: the attach's very
// first read of the feed meets each of the incident's bodies in turn — the
// factory's 500, a body that is not JSON, a page whose line is not JSON —
// and the watch asks again through all of them and follows the run to its
// own last word, instead of exiting on the first.
//
// short: a fake factory in-process and millisecond backoffs.
func TestWatchRetriesTheIncidentsBadReadsBeforeItsFirstFrame(t *testing.T) {
	fastFeedRetries(t)
	dispatched, finished := transientFeedLines(t)
	text := dispatched + finished

	var mu sync.Mutex
	feedReads := 0
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/" + transientRunID:
			return 200, map[string]any{"run": map[string]any{"run_id": transientRunID, "epic": "ex6", "state": "completed"}}
		case "/api/runs/" + transientRunID + "/events":
			mu.Lock()
			feedReads++
			n := feedReads
			mu.Unlock()
			switch n {
			case 1:
				return 500, rawFactoryBody(workerExceptionBody)
			case 2:
				return 200, rawFactoryBody("internal error")
			case 3:
				page := feedPage(request, text, "completed")
				page["text"] = "illegible\n"
				page["bytes"] = len("illegible\n")
				page["next"] = len("illegible\n")
				page["total_bytes"] = len("illegible\n")
				return 200, page
			}
			return 200, feedPage(request, text, "completed")
		}
		return 404, map[string]any{"error": "not_found"}
	})

	var stdout, stderr syncBuffer
	code := Run([]string{"watch", "--repo", t.TempDir(), transientRunID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: the watch ended on a transient read; stderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_finished") {
		t.Errorf("the run's own last word never printed:\n%s", stdout.String())
	}
	errText := stderr.String()
	for _, want := range []string{
		"factory returned HTTP 500: " + workerExceptionBody,
		"invalid character 'i' looking for beginning of value",
		"asking the factory again",
	} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not say %q:\n%s", want, errText)
		}
	}
}

// TestWatchFollowRidesOutTransientReadsMidRun: the run is live, the watch is
// following it, and then the factory fails — a 500, then a page that does
// not parse. The follow keeps its cursor, warns, backs off, and delivers the
// run's last word when the factory answers again; it never re-delivers a
// line and never ends early.
//
// short: a fake factory in-process and millisecond backoffs.
func TestWatchFollowRidesOutTransientReadsMidRun(t *testing.T) {
	fastFeedRetries(t)
	cadence := defaultCloudFeedInterval
	t.Cleanup(func() { defaultCloudFeedInterval = cadence })
	defaultCloudFeedInterval = 10 * time.Millisecond
	dispatched, finished := transientFeedLines(t)

	var mu sync.Mutex
	feedReads := 0
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		switch request.Path {
		case "/api/runs/" + transientRunID:
			return 200, map[string]any{"run": map[string]any{"run_id": transientRunID, "epic": "ex6", "state": "running"}}
		case "/api/runs/" + transientRunID + "/events":
			mu.Lock()
			feedReads++
			n := feedReads
			mu.Unlock()
			switch {
			case n <= 2:
				// The standing read, then the follow's first read: the
				// dispatched line alone.
				return 200, feedPage(request, dispatched, "running")
			case n == 3:
				return 500, rawFactoryBody(workerExceptionBody)
			case n == 4:
				page := feedPage(request, dispatched+finished, "running")
				page["text"] = "invalid\n"
				page["bytes"] = len("invalid\n")
				page["next"] = len(dispatched) + len("invalid\n")
				return 200, page
			}
			return 200, feedPage(request, dispatched+finished, "completed")
		}
		return 404, map[string]any{"error": "not_found"}
	})

	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"watch", "--repo", t.TempDir(), "--interval", "10ms", transientRunID}, &stdout, &stderr)
	}()
	var code int
	select {
	case code = <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("the watch never returned; stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if code != 0 {
		t.Fatalf("exit %d: the follow ended on a transient read; stderr:\n%s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Count(out, "dispatched:") != 1 || !strings.Contains(out, "run_finished") {
		t.Errorf("the follow did not deliver each line exactly once:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "HTTP 500") || !strings.Contains(stderr.String(), "did not parse") {
		t.Errorf("the transient reads were not warned about:\n%s", stderr.String())
	}
}

// TestWatchEndsOnTheFactorysDefinitiveAnswer: a 404 (the run is unknown) and
// a 401 (the token is refused) are answers, not blips — the watch says them
// and exits at once rather than asking forever.
//
// short: a fake factory in-process.
func TestWatchEndsOnTheFactorysDefinitiveAnswer(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			fastFeedRetries(t)
			var mu sync.Mutex
			feedReads := 0
			cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
				switch request.Path {
				case "/api/runs/" + transientRunID:
					return 200, map[string]any{"run": map[string]any{"run_id": transientRunID, "epic": "ex6", "state": "running"}}
				case "/api/runs/" + transientRunID + "/events":
					mu.Lock()
					feedReads++
					mu.Unlock()
					return status, map[string]any{"error": "refused", "detail": "the factory's own answer"}
				}
				return 404, map[string]any{"error": "not_found"}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var stdout, stderr syncBuffer
			code := runContext(ctx, []string{"watch", "--repo", t.TempDir(), transientRunID}, &stdout, &stderr)
			if ctx.Err() != nil {
				t.Fatalf("the watch kept asking after a definitive %d", status)
			}
			if code == 0 {
				t.Fatalf("exit 0 on a definitive %d", status)
			}
			if !strings.Contains(stderr.String(), "the factory's own answer") {
				t.Errorf("stderr does not carry the factory's answer:\n%s", stderr.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if feedReads != 1 {
				t.Errorf("the feed was asked %d times after a definitive %d; once is the answer", feedReads, status)
			}
		})
	}
}

// TestWatchOnATerminalKeepsAStaleFrameThroughAFactory500: the live view had
// a good frame, the factory starts failing, and the frame stays on screen
// labelled stale — the dashboard's rule — until the factory answers again
// and the run's own end closes the watch.
//
// short: a fake factory and a fake terminal in-process.
func TestWatchOnATerminalKeepsAStaleFrameThroughAFactory500(t *testing.T) {
	fastFeedRetries(t)
	dispatched, finished := transientFeedLines(t)
	repo := t.TempDir()
	execTestCmd(t, repo, "git", "init", "--quiet", "-b", "main")

	var mu sync.Mutex
	failing := false
	state := "running"
	endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
		mu.Lock()
		fail, current := failing, state
		mu.Unlock()
		switch {
		case request.Path == "/api/runs":
			return 200, map[string]any{"runs": []any{map[string]any{"run_id": transientRunID, "epic": "ex6", "state": "running"}}}
		case request.Path == "/api/runs/"+transientRunID:
			if fail {
				return 500, rawFactoryBody(workerExceptionBody)
			}
			return 200, map[string]any{"run": map[string]any{"run_id": transientRunID, "epic": "ex6", "state": current}}
		case request.Path == "/api/runs/"+transientRunID+"/events":
			if fail {
				return 500, rawFactoryBody(workerExceptionBody)
			}
			text := dispatched
			if current == "completed" {
				text += finished
			}
			return 200, feedPage(request, text, current)
		}
		return 404, map[string]any{"error": "not_found"}
	})
	configureCloudFactory(t, endpoint)
	fakeTheTracker(t, &tk.Graph{Waves: []tk.GraphWave{{
		Wave:  1,
		Tasks: []tk.GraphTask{{ID: "t1", Title: "the one tick", Status: "open"}},
	}}})
	fakeTerminal(t)

	var stdout, stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- Run([]string{"watch", "--repo", repo, "--interval", "20ms", transientRunID}, &stdout, &stderr)
	}()
	watchWaitsFor(t, "the first frame", func() bool {
		return strings.Contains(stdout.String(), "the one tick")
	}, &stdout, &stderr)

	mu.Lock()
	failing = true
	mu.Unlock()
	watchWaitsFor(t, "the stale label", func() bool {
		return strings.Contains(stdout.String(), "stale: the run's host could not be read")
	}, &stdout, &stderr)

	mu.Lock()
	failing, state = false, "completed"
	mu.Unlock()
	var got int
	select {
	case got = <-code:
	case <-time.After(15 * time.Second):
		t.Fatalf("the watch never returned after the factory recovered; stderr:\n%s", stderr.String())
	}
	if got != 0 {
		t.Fatalf("exit %d for a run that ended clean after a factory 500; stderr:\n%s", got, stderr.String())
	}
}

func TestCloudAPIErrorTransient(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{500, workerExceptionBody, true},
		{502, "", true},
		{503, `{"error":"feed_read_failed","retryable":true}`, true},
		{503, `{"error":"feed_unavailable","detail":"no artifacts bucket"}`, false},
		{429, "", true},
		{408, "", true},
		{404, `{"error":"unknown_run"}`, false},
		{401, "", false},
		{403, "", false},
		{400, "", false},
	} {
		if got := (cloudAPIError{status: tc.status, body: []byte(tc.body)}).transient(); got != tc.want {
			t.Errorf("HTTP %d %q: transient = %v, want %v", tc.status, tc.body, got, tc.want)
		}
	}
	if feedReadTransient(errors.New("no factory is configured")) {
		t.Error("a missing configuration is not a blip: asking again asks the same question")
	}
	if !feedReadTransient(&url.Error{Op: "Get", URL: "https://factory.test", Err: errors.New("no such host")}) {
		t.Error("a DNS failure is a blip")
	}
	if feedReadTransient(context.Canceled) {
		t.Error("a cancelled context is the caller leaving, not a blip")
	}
}

func TestFeedRetryDelayBacksOffToItsBound(t *testing.T) {
	base, max := feedRetryBase, feedRetryMax
	t.Cleanup(func() { feedRetryBase, feedRetryMax = base, max })
	feedRetryBase, feedRetryMax = time.Second, 10*time.Second
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for failures, w := range want {
		if got := feedRetryDelay(failures); got != w {
			t.Errorf("feedRetryDelay(%d) = %s, want %s", failures, got, w)
		}
	}
}
