// Package feedrelay carries a cloud run's event feed out of its orchestrator
// container and into the factory's run feed.
//
// A cloud run's orchestrator is `ticfac run-epic` inside a container, and the
// reconciler writes its journal to `.ticfac/logs/<run>/events.jsonl` INSIDE
// that container — a file nobody outside can read. The first cloud run of epic
// hn6 ran 1h45m, gated, tried three repairs and rebooted its orchestrator
// three times, and `ticfac events` / `ticfac status` (which read the
// factory's feed) showed exactly two lines: the Workflow's start and its
// finish. The operator saw nothing all night.
//
// The relay is a FOLLOWER of that file, exactly as the feed contract says a
// hosted mirror must be (contracts/run-event-feed.json `why_not_pushed`: a
// subscriber like any other, never the run pushing): it reads whole lines past
// its cursor, batches them, and POSTs them to the factory's feed-relay door
// (cloudflare/src/feed-relay.ts) on the run's own gateway token.
//
// DURABLE AND EXACTLY-ONCE. The door keys every batch by the byte offset of
// the file it starts at, and the credential names the boot. So a relay that
// starts — or restarts — asks the door where the stream stands and resumes
// there; a POST whose answer was lost is re-sent at the same offset and
// answered as already stored; and a relay that lost its place is told the
// offset the door expects. No line is relayed twice, and none is skipped.
//
// EXHAUST, NEVER AUTHORITY. Like the feed itself, the relay can never cost the
// run anything: no method returns an error the run must handle, a factory
// that cannot be reached is retried on the next tick, and a door that says
// the feed is closed (the boot's exit line stands, the credential is revoked)
// ends the relay quietly. A run nobody can watch is still a run.
package feedrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
	"github.com/pengelbrecht/ticfac/internal/runfeed"
)

// The door's path, as the Worker serves it (cloudflare/src/auth.ts
// FEED_RELAY_PATH).
const relayPath = "/api/feed"

// The stream this relay carries: the reconciler's own feed. The entrypoint
// relays its lifecycle lines as the "boot" stream itself (image/entrypoint.sh).
const streamFeed = "feed"

// The environment an orchestrator container is booted with: the factory, the
// run's own gateway credential, and the run's id. The id is what keeps a
// laptop that merely has a factory configured from relaying anything — only a
// process whose run IS the booted run relays.
const (
	factoryURLEnv   = "TICKS_FACTORY_URL"
	factoryTokenEnv = "TICKS_FACTORY_TOKEN"
	runIDEnv        = "TICKS_RUN_ID"
)

// Defaults: a relayed line reaches the factory within a couple of seconds of
// being written, in batches far below the door's cap.
const (
	DefaultInterval = 2 * time.Second
	DefaultMaxBatch = 128 * 1024
	httpTimeout     = 15 * time.Second
)

// Relay follows one feed file and relays it. A nil *Relay is valid and does
// nothing — FromEnv's answer for every run that is not a cloud orchestrator —
// so call sites start and stop it unconditionally.
type Relay struct {
	url      string
	token    string
	path     string
	log      io.Writer
	http     *http.Client
	interval time.Duration
	maxBatch int

	mu      sync.Mutex // serializes Flush: the loop and the final drain
	cursor  int64
	resumed bool
	closed  bool   // the door said this boot's feed takes no more lines
	failing string // the last failure said, so a retrying relay says it once

	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	done      chan struct{}
}

// FromEnv builds the relay for run runID's feed in repo, or nil when this
// process is not a cloud run's orchestrator.
func FromEnv(repo, runID string, log io.Writer) *Relay {
	factory := strings.TrimSpace(os.Getenv(factoryURLEnv))
	token := strings.TrimSpace(os.Getenv(factoryTokenEnv))
	booted := strings.TrimSpace(os.Getenv(runIDEnv))
	if factory == "" || token == "" || booted == "" || booted != runID {
		return nil
	}
	return New(factory, token, runfeed.Path(repo, runID), log)
}

// New builds a relay of the feed file at path to one factory.
func New(factoryURL, token, path string, log io.Writer) *Relay {
	if log == nil {
		log = io.Discard
	}
	return &Relay{
		url:      strings.TrimRight(strings.TrimSpace(factoryURL), "/"),
		token:    strings.TrimSpace(token),
		path:     path,
		log:      log,
		http:     httpnet.Client(httpTimeout),
		interval: DefaultInterval,
		maxBatch: DefaultMaxBatch,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// SetInterval and SetMaxBatch tune the cadence and the batch; tests use them.
func (r *Relay) SetInterval(d time.Duration) {
	if r != nil && d > 0 {
		r.interval = d
	}
}

func (r *Relay) SetMaxBatch(n int) {
	if r != nil && n > 0 {
		r.maxBatch = n
	}
}

// Start begins following the feed in the background. Idempotent.
func (r *Relay) Start() {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		fmt.Fprintf(r.log, "feed relay: relaying %s to the factory's run feed\n", r.path)
		go r.loop()
	})
}

// Stop drains whatever the feed holds — the run's last lines are the ones an
// operator most wants — and ends the relay, waiting at most timeout. Safe to
// call more than once and on a relay never started.
func (r *Relay) Stop(timeout time.Duration) {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	// A relay never started has no loop to wait for.
	r.startOnce.Do(func() { close(r.done) })
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		fmt.Fprintf(r.log, "feed relay: the final drain did not finish within %s; the feed may miss its last lines\n", timeout)
		return
	}
	// The loop has exited; one last drain under the caller's bound.
	r.Flush(ctx)
}

func (r *Relay) loop() {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 4*httpTimeout)
		r.Flush(ctx)
		cancel()
		select {
		case <-r.stop:
			return
		case <-ticker.C:
		}
	}
}

// relayAnswer is the door's reply, success and refusal alike.
type relayAnswer struct {
	Stored   *bool  `json:"stored"`
	End      *int64 `json:"end"`
	Expected *int64 `json:"expected"`
	Error    string `json:"error"`
	Detail   string `json:"detail"`
}

// Flush relays every whole line past the cursor, batch by batch, and reports
// whether the feed is now relayed to its end. It never returns an error: a
// failure is said once and retried on the next call.
func (r *Relay) Flush(ctx context.Context) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return true
	}
	if !r.resumed {
		if err := r.resume(ctx); err != nil {
			// Not fatal: the door refuses a batch at the wrong offset with
			// the offset it expects, so a relay that could not ask still
			// converges on the first POST.
			r.fail(fmt.Sprintf("could not ask the factory where the feed stands (%v)", err))
		}
		r.resumed = true
	}
	for mismatches := 0; ctx.Err() == nil; {
		batch, err := r.nextBatch()
		if err != nil {
			r.fail(err.Error())
			return false
		}
		if len(batch) == 0 {
			return true
		}
		answer, status, err := r.post(ctx, batch)
		if err != nil {
			r.fail(fmt.Sprintf("the factory could not be reached (%v); retrying", err))
			return false
		}
		switch {
		case status == http.StatusOK || status == http.StatusCreated:
			next := r.cursor + int64(len(batch))
			if answer.End != nil && *answer.End > r.cursor {
				// A replay is answered with where the STORED segment ends,
				// which is where the stream stands whatever this batch held.
				next = *answer.End
			}
			r.cursor = next
			r.recovered()
		case status == http.StatusConflict && answer.Error == "offset_mismatch" && answer.Expected != nil:
			mismatches++
			if mismatches > 3 {
				r.fail(fmt.Sprintf("the factory keeps expecting a different offset (%s)", answer.Detail))
				return false
			}
			r.cursor = *answer.Expected
		case status == http.StatusConflict || status == http.StatusUnauthorized || status == http.StatusForbidden:
			// The boot's feed is closed, or its credential is: nothing this
			// relay sends will ever be taken again. Said once, then quiet.
			r.closed = true
			fmt.Fprintf(r.log, "feed relay: the factory takes no more of this boot's feed (HTTP %d %s: %s); relaying stopped\n",
				status, answer.Error, answer.Detail)
			return true
		default:
			r.fail(fmt.Sprintf("the factory answered HTTP %d (%s: %s); retrying", status, answer.Error, answer.Detail))
			return false
		}
	}
	return false
}

// resume asks the door where this boot's feed stream stands.
func (r *Relay) resume(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		r.url+relayPath+"?stream="+url.QueryEscape(streamFeed), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var answer struct {
		End *int64 `json:"end"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.End == nil {
		return fmt.Errorf("an unreadable answer: %s", strings.TrimSpace(string(body)))
	}
	if *answer.End > r.cursor {
		fmt.Fprintf(r.log, "feed relay: resuming at byte %d, where the factory's feed stands\n", *answer.End)
		r.cursor = *answer.End
	}
	return nil
}

// nextBatch is the whole lines past the cursor, at most maxBatch bytes — or
// one whole line when a single line is larger. A line the reconciler has not
// newline-terminated yet is still being written, and waits.
func (r *Relay) nextBatch() ([]byte, error) {
	file, err := os.Open(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // the run has not written yet
	}
	if err != nil {
		return nil, fmt.Errorf("could not open the run feed: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("could not stat the run feed: %w", err)
	}
	if info.Size() <= r.cursor {
		return nil, nil
	}
	want := info.Size() - r.cursor
	if want > int64(r.maxBatch) {
		want = int64(r.maxBatch)
	}
	buf := make([]byte, want)
	n, err := file.ReadAt(buf, r.cursor)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("could not read the run feed: %w", err)
	}
	buf = buf[:n]
	if last := bytes.LastIndexByte(buf, '\n'); last >= 0 {
		return buf[:last+1], nil
	}
	if int64(n) < info.Size()-r.cursor {
		// One line longer than a batch: send it whole rather than never.
		rest := make([]byte, info.Size()-r.cursor)
		m, err := file.ReadAt(rest, r.cursor)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("could not read the run feed: %w", err)
		}
		if i := bytes.IndexByte(rest[:m], '\n'); i >= 0 {
			return rest[:i+1], nil
		}
	}
	return nil, nil
}

func (r *Relay) post(ctx context.Context, batch []byte) (relayAnswer, int, error) {
	body, err := json.Marshal(struct {
		Stream string `json:"stream"`
		Offset int64  `json:"offset"`
		Text   string `json:"text"`
	}{streamFeed, r.cursor, string(batch)})
	if err != nil {
		return relayAnswer{}, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+relayPath, bytes.NewReader(body))
	if err != nil {
		return relayAnswer{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return relayAnswer{}, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var answer relayAnswer
	_ = json.Unmarshal(raw, &answer)
	if answer.Error == "" && answer.Detail == "" && resp.StatusCode >= 300 {
		answer.Detail = strings.TrimSpace(string(raw))
	}
	return answer, resp.StatusCode, nil
}

// fail says a failure once per distinct message, so a relay retrying against
// a factory that is down does not fill the run's log.
func (r *Relay) fail(message string) {
	if message == r.failing {
		return
	}
	r.failing = message
	fmt.Fprintf(r.log, "feed relay: %s\n", message)
}

func (r *Relay) recovered() {
	if r.failing != "" {
		fmt.Fprintf(r.log, "feed relay: relaying again (relayed to byte %d)\n", r.cursor)
		r.failing = ""
	}
}

// Cursor is how far the feed has been relayed, in bytes of the file.
func (r *Relay) Cursor() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cursor
}
