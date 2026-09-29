package feedrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// fakeFactory is the feed-relay door (cloudflare/src/feed-relay.ts) reduced to
// its contract: segments keyed by the offset they start at, a replay answered
// as stored, any other offset than the stream's end refused with the one
// expected, and a closed boot refused outright. The feed it holds is what the
// factory would serve at /api/runs/:id/events for this boot.
type fakeFactory struct {
	t *testing.T

	mu       sync.Mutex
	segments map[int64]string // start offset -> relayed text
	ends     map[int64]int64  // start offset -> end offset
	end      int64
	closed   bool
	token    string
	posts    int

	// failNext answers the next n POSTs with 503 and stores nothing.
	failNext int
	// loseNext stores the next n POSTs and then answers 503 — the POST
	// whose answer was lost, which a relay must re-send without duplicating.
	loseNext int
}

func newFakeFactory(t *testing.T) (*fakeFactory, *httptest.Server) {
	f := &fakeFactory{t: t, segments: map[int64]string{}, ends: map[int64]int64{}, token: "tkr_boot1"}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	return f, server
}

func (f *fakeFactory) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/feed" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"unauthorized"}`)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		if r.URL.Query().Get("stream") != "feed" {
			f.t.Errorf("the relay asked about stream %q, want feed", r.URL.Query().Get("stream"))
		}
		fmt.Fprintf(w, `{"run_id":"run_x","boot":1,"stream":"feed","end":%d}`, f.end)
		return
	}
	f.posts++
	var body struct {
		Stream string `json:"stream"`
		Offset int64  `json:"offset"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("the relay posted an unreadable body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Stream != "feed" || !strings.HasSuffix(body.Text, "\n") {
		f.t.Errorf("the relay posted stream %q with text %q: want whole lines of the feed stream", body.Stream, body.Text)
	}
	if f.failNext > 0 {
		f.failNext--
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"unavailable","detail":"redeploying"}`)
		return
	}
	if f.closed {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":"feed_closed","detail":"boot 1 has ended"}`)
		return
	}
	if end, ok := f.ends[body.Offset]; ok {
		fmt.Fprintf(w, `{"stored":false,"boot":1,"end":%d,"lines":0}`, end)
		return
	}
	if body.Offset != f.end {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, `{"error":"offset_mismatch","detail":"stands at %d","expected":%d}`, f.end, f.end)
		return
	}
	f.segments[body.Offset] = body.Text
	f.end = body.Offset + int64(len(body.Text))
	f.ends[body.Offset] = f.end
	if f.loseNext > 0 {
		f.loseNext--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"stored":true,"boot":1,"end":%d,"lines":1}`, f.end)
}

// feed is what the factory holds, concatenated in key order.
func (f *fakeFactory) feed() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for offset := int64(0); offset < f.end; {
		text, ok := f.segments[offset]
		if !ok {
			f.t.Fatalf("the factory's feed has a hole at byte %d", offset)
		}
		b.WriteString(text)
		offset = f.ends[offset]
	}
	return b.String()
}

// appendLine writes one feed line exactly as the reconciler does.
func appendLine(t *testing.T, feed *runfeed.Feed, stage, detail string) {
	t.Helper()
	attempt := 1
	event := runfeed.NewEvent(time.Now(), "run_x", "r5i", &attempt, stage, detail)
	if err := feed.Append(event); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the feed: %v", err)
	}
	return string(data)
}

func newRelay(t *testing.T, server *httptest.Server, path string, log io.Writer) *Relay {
	t.Helper()
	relay := New(server.URL, "tkr_boot1", path, log)
	relay.SetInterval(10 * time.Millisecond)
	return relay
}

// The hn6 finding: the reconciler's rich feed never left the container. A
// relay started beside the run carries every line to the factory as it is
// written, and Stop drains the lines written last — the ones an operator most
// wants — before the process exits.
func TestRelayCarriesTheReconcilersFeedToTheFactory(t *testing.T) {
	factory, server := newFakeFactory(t)
	repo := t.TempDir()
	feed := runfeed.Open(repo, "run_x")
	relay := newRelay(t, server, runfeed.Path(repo, "run_x"), io.Discard)
	relay.Start()

	for _, stage := range []string{"claimed", "dispatched", "gate_started", "gate_failed"} {
		appendLine(t, feed, stage, stage+" r5i")
	}
	deadline := time.Now().Add(5 * time.Second)
	for factory.feed() != fileText(t, feed.Path()) {
		if time.Now().After(deadline) {
			t.Fatalf("the factory never caught up with the feed:\nfactory %q\nfile    %q", factory.feed(), fileText(t, feed.Path()))
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The run's last words, written just before it exits.
	appendLine(t, feed, "repair_started", "repair 1 of r5i")
	appendLine(t, feed, "run_finished", "failed: gate red")
	relay.Stop(5 * time.Second)

	if got, want := factory.feed(), fileText(t, feed.Path()); got != want {
		t.Fatalf("after Stop the factory holds\n%q\nwant the whole feed\n%q", got, want)
	}
	located, err := runfeed.ParseLocated([]byte(factory.feed()))
	if err != nil {
		t.Fatalf("the relayed feed does not parse as a run feed: %v", err)
	}
	if len(located) != 6 {
		t.Fatalf("the factory holds %d lines, want 6", len(located))
	}
}

// A restarted relay — a fresh process in the same boot — asks where the
// factory's feed stands and resumes there, relaying nothing twice.
func TestRelayResumesWhereTheFactorysFeedStands(t *testing.T) {
	factory, server := newFakeFactory(t)
	repo := t.TempDir()
	feed := runfeed.Open(repo, "run_x")
	appendLine(t, feed, "claimed", "claimed r5i")
	appendLine(t, feed, "dispatched", "attempt 1")

	first := newRelay(t, server, feed.Path(), io.Discard)
	if !first.Flush(context.Background()) {
		t.Fatal("the first relay did not relay the feed to its end")
	}
	postsBefore := factory.posts

	appendLine(t, feed, "gate_started", "gate on r5i")
	second := newRelay(t, server, feed.Path(), io.Discard)
	if !second.Flush(context.Background()) {
		t.Fatal("the restarted relay did not relay the feed to its end")
	}
	if got, want := factory.feed(), fileText(t, feed.Path()); got != want {
		t.Fatalf("the factory holds\n%q\nwant\n%q", got, want)
	}
	if factory.posts != postsBefore+1 {
		t.Fatalf("the restarted relay posted %d batches, want exactly the one new one", factory.posts-postsBefore)
	}
}

// A POST that failed is re-sent, and a POST whose answer was lost after the
// factory stored it is re-sent at the same offset and answered as stored:
// the feed ends with every line exactly once.
func TestRelayRetriesWithoutDuplicatesOrHoles(t *testing.T) {
	factory, server := newFakeFactory(t)
	repo := t.TempDir()
	feed := runfeed.Open(repo, "run_x")
	var log bytes.Buffer
	relay := newRelay(t, server, feed.Path(), &log)

	appendLine(t, feed, "claimed", "claimed r5i")
	factory.failNext = 1
	if relay.Flush(context.Background()) {
		t.Fatal("a refused batch was reported as relayed")
	}
	if relay.Cursor() != 0 {
		t.Fatalf("the cursor moved to %d past a batch the factory refused", relay.Cursor())
	}

	factory.loseNext = 1
	appendLine(t, feed, "dispatched", "attempt 1")
	relay.Flush(context.Background()) // stored, answer lost
	appendLine(t, feed, "gate_started", "gate on r5i")
	if !relay.Flush(context.Background()) {
		t.Fatal("the relay did not recover")
	}
	if got, want := factory.feed(), fileText(t, feed.Path()); got != want {
		t.Fatalf("the factory holds\n%q\nwant every line exactly once\n%q", got, want)
	}
	if !strings.Contains(log.String(), "retrying") || !strings.Contains(log.String(), "relaying again") {
		t.Fatalf("the relay's log does not say it failed and recovered:\n%s", log.String())
	}
}

// A relay whose cursor disagrees with the factory (the factory's GET failed at
// start) is told the offset expected and converges on it.
func TestRelayFollowsTheOffsetTheFactoryExpects(t *testing.T) {
	factory, server := newFakeFactory(t)
	repo := t.TempDir()
	feed := runfeed.Open(repo, "run_x")
	appendLine(t, feed, "claimed", "claimed r5i")
	primer := newRelay(t, server, feed.Path(), io.Discard)
	primer.Flush(context.Background())

	appendLine(t, feed, "dispatched", "attempt 1")
	lost := newRelay(t, server, feed.Path(), io.Discard)
	// As if the GET had failed and the relay had lost its place: a cursor
	// the factory's feed does not stand at.
	lost.resumed = true
	lost.cursor = 5
	if !lost.Flush(context.Background()) {
		t.Fatal("the relay did not converge on the offset the factory expects")
	}
	if got, want := factory.feed(), fileText(t, feed.Path()); got != want {
		t.Fatalf("the factory holds\n%q\nwant\n%q", got, want)
	}
}

// A line the reconciler has not newline-terminated is still being written:
// it is never relayed half-way.
func TestRelayWaitsForAWholeLine(t *testing.T) {
	factory, server := newFakeFactory(t)
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"schema_version":1`), 0o644); err != nil {
		t.Fatal(err)
	}
	relay := newRelay(t, server, path, io.Discard)
	if !relay.Flush(context.Background()) || factory.posts != 0 {
		t.Fatalf("a partial line was relayed (%d posts)", factory.posts)
	}
}

// The boot's exit line stands (or its credential died): the factory takes no
// more of this boot's feed, and the relay stops asking rather than retrying
// forever.
func TestRelayStopsWhenTheFactoryClosesTheFeed(t *testing.T) {
	factory, server := newFakeFactory(t)
	repo := t.TempDir()
	feed := runfeed.Open(repo, "run_x")
	var log bytes.Buffer
	relay := newRelay(t, server, feed.Path(), &log)
	factory.closed = true
	appendLine(t, feed, "claimed", "claimed r5i")
	relay.Flush(context.Background())
	appendLine(t, feed, "dispatched", "attempt 1")
	relay.Flush(context.Background())
	if factory.posts != 1 {
		t.Fatalf("the relay posted %d times to a closed feed, want 1", factory.posts)
	}
	if !strings.Contains(log.String(), "relaying stopped") {
		t.Fatalf("the relay did not say it stopped:\n%s", log.String())
	}
}

// Only the booted run's orchestrator relays: a laptop that merely has a
// factory configured, or a process running a different run, relays nothing.
func TestFromEnvRelaysOnlyTheBootedRun(t *testing.T) {
	t.Setenv(factoryURLEnv, "https://factory.example.com")
	t.Setenv(factoryTokenEnv, "tkr_x")
	t.Setenv(runIDEnv, "run_x")
	if FromEnv(t.TempDir(), "run_x", io.Discard) == nil {
		t.Fatal("the booted run's orchestrator got no relay")
	}
	if FromEnv(t.TempDir(), "epic-hn6", io.Discard) != nil {
		t.Fatal("a different run relayed")
	}
	t.Setenv(runIDEnv, "")
	if FromEnv(t.TempDir(), "run_x", io.Discard) != nil {
		t.Fatal("a process with no booted run relayed")
	}
	// The nil relay is a valid no-op everywhere.
	var none *Relay
	none.Start()
	none.Stop(time.Second)
	if !none.Flush(context.Background()) || none.Cursor() != 0 {
		t.Fatal("the nil relay is not a no-op")
	}
}
