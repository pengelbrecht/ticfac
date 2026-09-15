package runfeed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Event is one line of the feed: `ticfac.run_event.v1`, closed. Run, tick and
// attempt identity is on every line — that is the point of the feed, a line a
// multi-run dashboard can place without asking anyone.
//
// TickID and Attempt are pointers, and carry no `omitempty`: a line that
// OMITS tick_id and one that states it as null are different claims, and only
// the second is a claim at all. Null is honest — a run-level event belongs to
// no tick, and an event before the tick's first dispatch is recorded belongs
// to no attempt.
type Event struct {
	SchemaVersion int     `json:"schema_version"`
	At            string  `json:"at"`
	RunID         string  `json:"run_id"`
	TickID        *string `json:"tick_id"`
	Attempt       *int    `json:"attempt"`
	Stage         string  `json:"stage"`
	Detail        string  `json:"detail"`
}

// NewEvent builds one line. A run-level event is an empty tick; an event that
// belongs to no attempt yet is a nil one. The time is stamped by the caller —
// the writer's clock is the only clock there is, and a feed that guessed at
// another's would be a feed that lies about when.
func NewEvent(at time.Time, runID, tickID string, attempt *int, stage, detail string) Event {
	var tick *string
	if tickID != "" {
		tick = &tickID
	}
	return Event{
		SchemaVersion: SchemaVersion,
		At:            at.UTC().Format(time.RFC3339Nano),
		RunID:         runID,
		TickID:        tick,
		Attempt:       attempt,
		Stage:         stage,
		Detail:        detail,
	}
}

// Validate applies what the schema subset cannot say, which the contract
// names rather than leaves to prose: `at` is RFC3339, and an attempt is
// 1-based — an integer attempt below 1 is an event claiming an attempt that
// was never dispatched.
func (e Event) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("run event schema_version is %d, want %d", e.SchemaVersion, SchemaVersion)
	}
	if _, err := time.Parse(time.RFC3339, e.At); err != nil {
		return fmt.Errorf("run event at %q is not RFC3339: %w", e.At, err)
	}
	if e.RunID == "" {
		return fmt.Errorf("run event carries no run_id: a line without run identity is a line no subscriber can place")
	}
	if e.Stage == "" {
		return fmt.Errorf("run event carries no stage: an event that names nothing is a line that says nothing")
	}
	if e.Attempt != nil && *e.Attempt < 1 {
		return fmt.Errorf("run event attempt is %d, below the 1-based first attempt", *e.Attempt)
	}
	if e.TickID != nil && *e.TickID == "" {
		return fmt.Errorf("run event tick_id is the empty string, not null: 'no tick' and 'the tick was not recorded' are different claims")
	}
	return nil
}

// Feed is the run's append-only feed, at Path(repo, runID). It touches no
// disk until the first append, because a reconciler constructed is not a run
// started, and a run refused at construction must leave no feed behind.
type Feed struct {
	path    string
	dirOnce bool
}

// Open prepares a run's feed. It makes no write: the feed comes to exist when
// the first event lands, not when the reconciler is built.
func Open(repo, runID string) *Feed {
	return &Feed{path: Path(repo, runID)}
}

// Append adds one line. The write is one O_APPEND write of a
// newline-terminated line, which is what makes the file safe to follow while
// the run writes: a follower either sees a whole line or none of it, and a
// line once written is never rewritten (contracts/run-event-feed.json
// `append_only`).
func (f *Feed) Append(e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal the run event: %w", err)
	}
	if !f.dirOnce {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return fmt.Errorf("create the run feed directory: %w", err)
		}
		f.dirOnce = true
	}
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open the run feed at %s: %w", f.path, err)
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append to the run feed: %w", err)
	}
	return nil
}

// Path is where this feed lives.
func (f *Feed) Path() string { return f.path }

// End is the feed's standing size in bytes: the cursor a subscriber that
// wants only what lands from NOW starts from. A feed that does not exist
// yet is the run-has-not-written case, and its cursor is zero — everything
// the run will write is still to come (ticfac tick 55i).
func End(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// Read returns every event in the feed, in the order it landed. A line that
// is not a closed `ticfac.run_event.v1` object is refused rather than
// skipped: a feed a reader silently forgives is a feed whose drift nobody
// can see.
func Read(path string) ([]Event, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the run feed at %s: %w", path, err)
	}
	events := []Event{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	for {
		var line Event
		err := decoder.Decode(&line)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s is not a run event feed: %w", path, err)
		}
		if err := line.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		events = append(events, line)
	}
	return events, nil
}

// followTick is the follower's own read cadence. It is an implementation
// detail of the FOLLOWER, not a guess about the work: the subscriber never
// names an interval and never decides anything on one (contracts/
// run-event-feed.json `subscribe`). The stdlib has no file-watch, so a
// follower reads again shortly; a real watch, where one exists, would only
// make this faster.
const followTick = 25 * time.Millisecond

// Follow subscribes to a run's events: every event already in the feed, then
// each new line as it lands, until the context is cancelled.
//
// This is the subscription the feed exists for — open once and follow, in
// place of a poll loop over the durable records with an interval somebody
// guessed. What it yields are hints, never verdicts: a subscriber that learns
// a run is finished from a line has skipped the one step that is never the
// feed's to take — looking at the evidence.
//
// Replaying the standing feed is a subscriber's CHOICE, and not always the
// right one: the feed is append-only per RUN ID, so a resumed run appends to
// a file an earlier, failed incarnation already ended with a terminal line,
// and a follower from offset zero replays that line first (ticfac tick 55i).
// A subscriber that wants only what the current incarnation writes starts
// at [End] instead — [FollowFrom] carries the cursor.
func Follow(ctx context.Context, path string, fn func(Event)) error {
	return FollowFrom(ctx, path, 0, fn)
}

// FollowFrom is [Follow] starting at a byte cursor: zero replays every
// standing line first (the contract's "follow the file from offset zero"),
// [End] subscribes from now, and a cursor a previous session remembered
// resumes without missing or replaying anything. The reader never names an
// interval and never decides anything on one — the subscriber's cadence is
// its own, and the cursor is the subscriber's, not the feed's.
func FollowFrom(ctx context.Context, path string, cursor int64, fn func(Event)) error {
	var offset int64 = cursor
	if offset < 0 {
		offset = 0
	}
	pending := []byte{}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		info, err := os.Stat(path)
		switch {
		case os.IsNotExist(err):
			// A run that has not written yet is a run that has not started;
			// waiting for the file is waiting for the first event.
		case err != nil:
			return fmt.Errorf("follow the run feed at %s: %w", path, err)
		default:
			if info.Size() < offset {
				return fmt.Errorf("the run feed at %s shrank from %d bytes to %d: the feed is append-only, and a follower that reset silently would miss the events in between", path, offset, info.Size())
			}
			if info.Size() > offset {
				file, err := os.Open(path)
				if err != nil {
					return fmt.Errorf("follow the run feed at %s: %w", path, err)
				}
				chunk := make([]byte, info.Size()-offset)
				if _, err := file.ReadAt(chunk, offset); err != nil {
					file.Close()
					return fmt.Errorf("follow the run feed at %s: %w", path, err)
				}
				file.Close()
				offset += int64(len(chunk))
				pending = append(pending, chunk...)
				for {
					index := bytes.IndexByte(pending, '\n')
					if index < 0 {
						break
					}
					line := pending[:index]
					pending = pending[index+1:]
					if len(strings.TrimSpace(string(line))) == 0 {
						continue
					}
					var event Event
					if err := unmarshalStrict(line, &event); err != nil {
						return fmt.Errorf("%s is not a run event feed: %w", path, err)
					}
					if err := event.Validate(); err != nil {
						return fmt.Errorf("%s: %w", path, err)
					}
					fn(event)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(followTick):
		}
	}
}

// unmarshalStrict decodes one line, refusing unknown fields — the closed
// schema, enforced on the follower the same way Read enforces it.
func unmarshalStrict(line []byte, event *Event) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(event); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("one object per line, got trailing content after the event")
	}
	return nil
}
