package runfeed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Source half of the feed (tick k7p): one follow loop that both hosts
// read through. A local run's feed is a file; a cloud run publishes the SAME
// lines to a stream a laptop reads from the factory. One loop means one
// implementation of "follow a run" that cannot drift into two — the defect
// b9w and 5bp each had to settle — so the loop here is pinned against a
// source that is neither a file nor the factory: a scripted in-memory one.

// scriptSource is a Source whose bytes a test appends to — the same shape a
// live feed has: bytes that grow, lines that land whole.
type scriptSource struct {
	mu     sync.Mutex
	data   []byte
	err    error       // a terminal error to answer once, then clear
	calls  int         // how many reads the loop has made
	misses int         // how many leading reads answer nothing at all
	where  string      // what the source calls itself
	onRead func(n int) // lets a test observe the loop's read cadence
}

func (s *scriptSource) Where() string {
	if s.where != "" {
		return s.where
	}
	return "the scripted feed"
}

func (s *scriptSource) ReadAt(_ context.Context, cursor int64) ([]byte, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.onRead != nil {
		s.onRead(s.calls)
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return nil, 0, err
	}
	if s.calls <= s.misses {
		// Not there yet: no bytes, the cursor unchanged — the
		// run-has-not-written-its-first-line shape.
		return nil, cursor, nil
	}
	if int64(len(s.data)) < cursor {
		return nil, int64(len(s.data)), nil
	}
	return s.data[cursor:], int64(len(s.data)), nil
}

func (s *scriptSource) append(t *testing.T, event Event) {
	t.Helper()
	line, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append(s.data, append(line, '\n')...)
}

// TestFollowSourceDeliversEachLineAsItLands pins the loop's subscription
// property against a source that is not a file: every line the source grows
// by, exactly once, in order, from the cursor the subscriber named.
func TestFollowSourceDeliversEachLineAsItLands(t *testing.T) {
	source := &scriptSource{}
	attempt := 1
	source.append(t, NewEvent(time.Now(), "r-1", "a1", &attempt, "dispatched", "first"))

	ctx, cancel := context.WithCancel(context.Background())
	delivered := make(chan Event, 4)
	var err error
	done := make(chan struct{})
	go func() {
		err = FollowSource(ctx, source, time.Millisecond, 0, func(e Event) { delivered <- e })
		close(done)
	}()

	// The first standing line, then one that lands while the loop is open.
	first := <-delivered
	if first.Stage != "dispatched" || *first.TickID != "a1" {
		t.Fatalf("the first line was not delivered as it stands: %+v", first)
	}
	source.append(t, NewEvent(time.Now(), "r-1", "", nil, "run_finished", "completed"))
	second := <-delivered
	if second.Stage != "run_finished" || second.TickID != nil {
		t.Fatalf("the line that landed was not delivered: %+v", second)
	}
	cancel()
	<-done
	if err != nil {
		t.Fatalf("FollowSource: %v", err)
	}
}

// TestFollowSourceResumesFromTheCursor: a subscriber that names a cursor is
// shown only what lands after it — the from-now property, held by the loop
// for every source, not only the file.
func TestFollowSourceResumesFromTheCursor(t *testing.T) {
	source := &scriptSource{}
	attempt := 1
	source.append(t, NewEvent(time.Now(), "r-1", "a1", &attempt, "dispatched", "the standing line"))

	located, err := ParseLocated(source.data)
	if err != nil {
		t.Fatal(err)
	}
	cursor := located[len(located)-1].End

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen []Event
	var done = make(chan struct{})
	go func() {
		err = FollowSource(ctx, source, time.Millisecond, cursor, func(e Event) { seen = append(seen, e) })
		close(done)
	}()
	// The standing line is never delivered: it stands before the cursor.
	time.Sleep(50 * time.Millisecond)
	source.append(t, NewEvent(time.Now(), "r-1", "a1", &attempt, "collected", "the line that lands"))
	deadline := time.Now().Add(5 * time.Second)
	for len(seen) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(seen) == 0 {
		t.Fatal("the line after the cursor was never delivered")
	}
	if len(seen) != 1 || seen[0].Stage != "collected" {
		t.Fatalf("the follower was shown %d lines past its cursor, want exactly the one that landed: %+v", len(seen), seen)
	}
}

// TestFollowSourceRefusesAShrinkingFeed: append-only is the property a
// resumable cursor rests on, and a source whose standing size goes backwards
// must end the subscription rather than silently resetting.
func TestFollowSourceRefusesAShrinkingFeed(t *testing.T) {
	source := &scriptSource{}
	attempt := 1
	source.append(t, NewEvent(time.Now(), "r-1", "a1", &attempt, "dispatched", "a line"))
	err := FollowSource(context.Background(), source, time.Millisecond, int64(len(source.data))+10, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "shrank") {
		t.Fatalf("a shrinking feed must end the subscription with the shrink named, got %v", err)
	}
}

// TestFollowSourceEndsOnASourceError: an error from the source ends the
// stream rather than being swallowed — a follower that quietly kept going
// after the feed became unreadable would be one more watcher that reports
// nothing and looks alive.
func TestFollowSourceEndsOnASourceError(t *testing.T) {
	source := &scriptSource{err: errors.New("the stream is gone")}
	err := FollowSource(context.Background(), source, time.Millisecond, 0, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "the stream is gone") {
		t.Fatalf("a source error must end the subscription named, got %v", err)
	}
}

// TestFollowSourceRefusesAMalformedLine: the closed-schema strictness is the
// loop's, not the file's — whatever source the bytes come from, a line that
// is not a closed ticfac.run_event.v1 object ends the subscription with the
// complaint in the open.
func TestFollowSourceRefusesAMalformedLine(t *testing.T) {
	source := &scriptSource{}
	source.mu.Lock()
	source.data = append(source.data, []byte("not a line\n")...)
	source.mu.Unlock()
	err := FollowSource(context.Background(), source, time.Millisecond, 0, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "not a run event feed") || !strings.Contains(err.Error(), source.Where()) {
		t.Fatalf("a malformed line must end the subscription with the feed named, got %v", err)
	}
}

// TestFollowSourceWaitsForAFeedThatDoesNotExistYet: a run that has not
// written its first line is a run that has not started, and waiting for it
// is the subscription working, not failing.
func TestFollowSourceWaitsForAFeedThatDoesNotExistYet(t *testing.T) {
	source := &scriptSource{misses: 3}
	ctx, cancel := context.WithCancel(context.Background())
	var seen int
	var err error
	done := make(chan struct{})
	go func() {
		err = FollowSource(ctx, source, time.Millisecond, 0, func(Event) { seen++ })
		close(done)
	}()
	// The first three reads answer nothing; the loop must still be running
	// when the feed arrives.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		source.mu.Lock()
		calls := source.calls
		source.mu.Unlock()
		if calls >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	attempt := 1
	source.append(t, NewEvent(time.Now(), "r-1", "a1", &attempt, "dispatched", "arrived late"))
	select {
	case <-done:
		t.Fatalf("the subscription ended before the feed arrived: %v", err)
	default:
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		source.mu.Lock()
		written := seen
		source.mu.Unlock()
		if written == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if seen != 1 {
		t.Fatalf("the line was delivered %d times, want once", seen)
	}
	if err != nil {
		t.Fatalf("FollowSource: %v", err)
	}
}

// TestFollowSourceHonoursTheCadence: the follower's read cadence is an
// implementation detail of the follower, and it is the loop's caller that
// names it — a remote source asks less often than a local stat, and the loop
// must read at the cadence it was given, not at its own.
func TestFollowSourceHonoursTheCadence(t *testing.T) {
	source := &scriptSource{}
	var mu sync.Mutex
	stamps := []time.Time{}
	source.onRead = func(int) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = FollowSource(ctx, source, 40*time.Millisecond, 0, func(Event) {})
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(stamps)
		mu.Unlock()
		if count >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(stamps) < 4 {
		t.Fatalf("the loop read %d times, want at least 4 to measure a cadence", len(stamps))
	}
	// Three consecutive reads at a 40ms cadence must span at least 40ms; a
	// loop that polled at its own default (25ms) would be a follower that
	// hammers a remote read at file-stat speed.
	span := stamps[3].Sub(stamps[1])
	if span < 40*time.Millisecond {
		t.Fatalf("three reads at a 40ms cadence took %s — the loop read faster than its caller asked", span)
	}
}
