package cli

// A LONG cloud run's feed, read a page at a time (run_5c7c16d1, epic hn6).
//
// That run went ~5h with ~16 dispatches and many findings, and its
// orchestrator's feed is relayed to the factory every two seconds — so the
// feed is thousands of R2 segments. The events route read every one of them on
// every request and never answered inside the client's deadline: `ticfac
// events` and `ticfac status` failed on it every time, and then `events` said
// "the run has not written an event — which is what a run that has not started
// looks like" about a run five hours in, because the source swallowed the
// failed read as "no change". Two defects, pinned here:
//
//   - the client reads the feed in PAGES, by byte cursor (`from`), and a
//     `--tail` asks the factory for the end of the feed rather than walking
//     all of it;
//   - a read that FAILED is said to have failed, never answered as a feed
//     with no line in it.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

const pagedRunID = "run_5c7c16d199414da2a8a40d97b524e987"

// largeFeed is a long run's feed: n events, each one line, each line carrying
// an em dash so a byte count that is not UTF-8 shows.
func largeFeed(t *testing.T, n int) (string, []string) {
	t.Helper()
	var lines []string
	start := time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		attempt := 1
		lines = append(lines, feedLine(t, runfeed.NewEvent(start.Add(time.Duration(i)*time.Second), pagedRunID,
			fmt.Sprintf("t%d", i%16), &attempt, "dispatched", fmt.Sprintf("event %d — of a long run", i))))
	}
	return strings.Join(lines, ""), lines
}

// pagedFeedFactory serves a feed the way the paged route does: `from` is a
// byte cursor, `tail` the last N bytes rounded back to a line boundary,
// `limit` the page's byte budget (each line a segment, at least one served).
// It counts the bytes it served, so a test can tell a tail from a walk.
type pagedFeedFactory struct {
	text  string
	limit int

	mu     sync.Mutex
	served int
	pages  int
}

func (f *pagedFeedFactory) handle(t *testing.T, request cloudFactoryRequest) (int, any) {
	switch request.Path {
	case "/api/runs/" + pagedRunID:
		return 200, map[string]any{"run": map[string]any{"run_id": pagedRunID, "state": "running", "epic": "hn6"}}
	case "/api/runs/" + pagedRunID + "/events":
	default:
		t.Fatalf("unexpected factory request %s", request.Path)
		return 500, nil
	}
	total := len(f.text)
	limit := f.limit
	if raw := request.Query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 400, map[string]any{"error": "bad_request", "detail": "limit"}
		}
		if n < limit {
			limit = n
		}
	}
	from := 0
	if raw := request.Query.Get("from"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 400, map[string]any{"error": "bad_request", "detail": "from"}
		}
		from = min(n, total)
	}
	if raw := request.Query.Get("tail"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 400, map[string]any{"error": "bad_request", "detail": "tail"}
		}
		wanted := max(total-n, 0)
		from = strings.LastIndex(f.text[:wanted], "\n") + 1
		if wanted == 0 {
			from = 0
		}
	}
	end := from
	for end < total && (end == from || end-from < limit) {
		next := strings.IndexByte(f.text[end:], '\n')
		end += next + 1
	}
	page := f.text[from:end]
	f.mu.Lock()
	f.served += len(page)
	f.pages++
	f.mu.Unlock()
	return 200, map[string]any{
		"run_id": pagedRunID, "state": "running", "text": page,
		"from": from, "bytes": len(page), "next": end, "total_bytes": total, "more": end < total,
	}
}

func (f *pagedFeedFactory) stats() (served, pages int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served, f.pages
}

// THE WHOLE FEED, PAGED: `events` walks the pages by cursor and prints every
// line once, in order — no line twice, no hole.
func TestEventsWalksALongCloudFeedPageByPage(t *testing.T) {
	text, lines := largeFeed(t, 3000)
	factory := &pagedFeedFactory{text: text, limit: 16 * 1024}
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) { return factory.handle(t, request) })

	var stdout, stderr syncBuffer
	code := Run([]string{"events", "--repo", t.TempDir(), pagedRunID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	got := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(got) != len(lines) {
		t.Fatalf("printed %d lines of a %d-line feed", len(got), len(lines))
	}
	if got[0]+"\n" != lines[0] || got[len(got)-1]+"\n" != lines[len(lines)-1] {
		t.Errorf("the walked feed is not the feed: first %q, last %q", got[0], got[len(got)-1])
	}
	if _, pages := factory.stats(); pages < 2 {
		t.Errorf("the feed was read in %d page(s): a long feed is read a page at a time", pages)
	}
	if strings.Contains(stderr.String(), "standing size") {
		t.Errorf("a paged read warned it was bounded: %q", stderr.String())
	}
}

// THE TAIL asks the factory for the END of the feed: the last N events,
// printed in order, without walking the hours before them.
func TestEventsTailReadsOnlyTheEndOfALongCloudFeed(t *testing.T) {
	text, lines := largeFeed(t, 3000)
	factory := &pagedFeedFactory{text: text, limit: 64 * 1024}
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) { return factory.handle(t, request) })

	var stdout, stderr syncBuffer
	code := Run([]string{"events", "--repo", t.TempDir(), "--tail", "5", pagedRunID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	got := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(got) != 5 {
		t.Fatalf("--tail 5 printed %d lines: %q", len(got), stdout.String())
	}
	for i, line := range got {
		if want := lines[len(lines)-5+i]; line+"\n" != want {
			t.Errorf("tail line %d is %q, want %q", i, line, want)
		}
	}
	if served, _ := factory.stats(); served >= len(text)/4 {
		t.Errorf("--tail 5 read %d of the feed's %d bytes: a tail must not walk the whole feed", served, len(text))
	}
}

// FROM A CURSOR: `--from` starts the read at a byte cursor a previous read
// ended at, and --json states the cursor the next read starts from.
func TestEventsFromACursorPrintsOnlyWhatFollowsIt(t *testing.T) {
	text, lines := largeFeed(t, 200)
	factory := &pagedFeedFactory{text: text, limit: 4 * 1024}
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) { return factory.handle(t, request) })

	cursor := len(strings.Join(lines[:190], ""))
	var stdout, stderr syncBuffer
	code := Run([]string{"events", "--repo", t.TempDir(), "--from", strconv.Itoa(cursor), "--json", pagedRunID},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var doc struct {
		Events []runfeed.Event `json:"events"`
		Next   int64           `json:"next_cursor"`
	}
	if err := decodeCloudJSON([]byte(stdout.String()), &doc); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	if len(doc.Events) != 10 {
		t.Fatalf("--from the 190th line's end printed %d events, want the last 10", len(doc.Events))
	}
	if doc.Events[0].Detail != "event 191 — of a long run" {
		t.Errorf("the first event from the cursor is %q", doc.Events[0].Detail)
	}
	if doc.Next != int64(len(text)) {
		t.Errorf("next_cursor is %d, want the feed's end %d", doc.Next, len(text))
	}
}

// A READ THAT FAILED IS SAID TO HAVE FAILED. The factory timing out is not a
// run with no events — saying so about a run five hours in is the lie this
// pins.
func TestEventsSaysTheReadFailedWhenTheFactoryTimesOut(t *testing.T) {
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		return 200, map[string]any{"run": map[string]any{"run_id": pagedRunID, "state": "running"}}
	})
	// The transport fails for the feed alone, as the deadline did.
	inner := cloudHTTPClient.Transport
	cloudHTTPClient = &http.Client{Transport: cloudRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			return nil, errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
		}
		return inner.RoundTrip(r)
	})}

	var stdout, stderr syncBuffer
	code := Run([]string{"events", "--repo", t.TempDir(), pagedRunID}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("a feed the factory could not serve was answered as read: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "has not written an event") || strings.Contains(stderr.String(), "has not started") {
		t.Errorf("a failed read was answered as a run with no events: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not be read") || !strings.Contains(stderr.String(), "deadline exceeded") {
		t.Errorf("stderr %q does not say the read failed, and why", stderr.String())
	}
}

// The same lie in `status` for a cloud run: a feed that could not be read is
// named as that, never left out as though the run had said nothing.
func TestCloudStatusSaysTheFeedReadFailed(t *testing.T) {
	cloudFeedFactory(t, func(request cloudFactoryRequest) (int, any) {
		return 200, map[string]any{"run": map[string]any{"run_id": pagedRunID, "state": "running"}}
	})
	inner := cloudHTTPClient.Transport
	cloudHTTPClient = &http.Client{Transport: cloudRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			return nil, errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
		}
		return inner.RoundTrip(r)
	})}

	var stdout, stderr syncBuffer
	Run([]string{"status", "--repo", t.TempDir(), pagedRunID}, &stdout, &stderr)
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "could not be read") {
		t.Errorf("status does not say the run's feed could not be read: %q", out)
	}
	if strings.Contains(out, "has not written an event") {
		t.Errorf("status answered a failed read as a run with no events: %q", out)
	}
}
