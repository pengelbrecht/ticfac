package runfeed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// The vendored contract is this package's SECOND reader, read from contracts/
// the way the parity readers read it — never from a copy. The tests below
// validate the bytes this package WRITES against the schema vendored there,
// because a feed whose writer and contract disagree is a feed every subscriber
// parses differently, which is precisely the drift the bundle exists to
// catch.

const feedFile = "run-event-feed.json"

type feedContract struct {
	SchemaVersion int    `json:"schema_version"`
	Contract      string `json:"contract"`
	Layout        struct {
		Path      string `json:"path"`
		Committed bool   `json:"committed"`
	} `json:"layout"`
	Identity struct {
		EveryLineCarries []string `json:"every_line_carries"`
	} `json:"identity"`
	Subscribe struct {
		ALineMeans            string `json:"a_line_means"`
		CompletionIsDecidedBy string `json:"completion_is_decided_by"`
	} `json:"subscribe"`
	Golden  map[string]json.RawMessage `json:"golden"`
	Invalid []struct {
		Record              string          `json:"record"`
		Why                 string          `json:"why"`
		ExpectErrorContains string          `json:"expect_error_contains"`
		Document            json.RawMessage `json:"document"`
	} `json:"invalid"`
}

// lineSchema parses `records.feed_event.schema` out of the vendored fixture —
// the ONE definition of the line, so that a change to it is a change this
// package's tests see rather than drift past them.
func lineSchema(t *testing.T) (*schema.Schema, feedContract) {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, contracts.DirName, feedFile))
	if err != nil {
		t.Fatalf("read the vendored contract: %v", err)
	}
	var c feedContract
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("%s is not valid JSON: %v", feedFile, err)
	}
	var full struct {
		Records map[string]struct {
			SchemaID string          `json:"schema_id"`
			Schema   json.RawMessage `json:"schema"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatal(err)
	}
	record, ok := full.Records["feed_event"]
	if !ok {
		t.Fatalf("%s declares no feed_event record schema", feedFile)
	}
	if record.SchemaID != "ticfac.run_event.v1" {
		t.Errorf("the line schema id is %q, want ticfac.run_event.v1", record.SchemaID)
	}
	parsed, err := schema.ParseSchema(record.Schema)
	if err != nil {
		t.Fatalf("parse the feed_event schema: %v", err)
	}
	return parsed, c
}

func asDocument(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func goldenEvent(t *testing.T) Event {
	t.Helper()
	at := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	attempt := 1
	return NewEvent(at, "epic-av8", "u9l", &attempt, "dispatched", "attempt 1 started as job-7f2a")
}

func goldenRunLevel(t *testing.T) Event {
	t.Helper()
	at := time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC)
	return NewEvent(at, "epic-av8", "", nil, "run_finished", "completed: every tick closed behind the gate")
}

func TestTheContractAdmitsItsGoldenDocuments(t *testing.T) {
	lineSchema, c := lineSchema(t)
	if len(c.Golden) == 0 {
		t.Fatal("the fixture carries no golden documents to replay")
	}
	for name, document := range c.Golden {
		var raw any
		if err := json.Unmarshal(document, &raw); err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		if errs := schema.Validate(lineSchema, nil, raw); len(errs) > 0 {
			t.Errorf("golden %s does not validate: %v", name, errs)
		}
	}
	// The bytes THIS package writes for the same intent validate too, which
	// is what makes the fixture the second reader of the writer.
	for _, event := range []Event{goldenEvent(t), goldenRunLevel(t)} {
		if errs := schema.Validate(lineSchema, nil, asDocument(t, event)); len(errs) > 0 {
			t.Errorf("an event as written by this package does not validate: %v", errs)
		}
	}
}

func TestTheContractRefusesItsNegativeDocuments(t *testing.T) {
	lineSchema, c := lineSchema(t)
	if len(c.Invalid) == 0 {
		t.Fatal("the fixture carries no negative documents to replay")
	}
	for _, bad := range c.Invalid {
		var raw any
		if err := json.Unmarshal(bad.Document, &raw); err != nil {
			t.Fatalf("%s: %v", bad.Why, err)
		}
		errs := schema.Validate(lineSchema, nil, raw)
		found := false
		for _, e := range errs {
			if strings.Contains(e, bad.ExpectErrorContains) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected %q among %v", bad.Why, bad.ExpectErrorContains, errs)
		}
	}
}

func TestTheFeedIsAppendOnlyAndCarriesIdentityOnEveryLine(t *testing.T) {
	feed := Open(t.TempDir(), "r-1")
	attempt := 1
	if err := feed.Append(goldenEvent(t)); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(goldenRunLevel(t)); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(NewEvent(time.Now(), "r-1", "u9l", &attempt, "collected", "verdict done")); err != nil {
		t.Fatal(err)
	}

	events, err := Read(feed.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("Read returned %d events, want the 3 appended", len(events))
	}
	// Identity: a dispatch line names its tick and attempt; a run-level line
	// states both as null rather than omitting them.
	if events[0].TickID == nil || *events[0].TickID != "u9l" || events[0].Attempt == nil || *events[0].Attempt != 1 {
		t.Errorf("the dispatch line carries %v/%v, want tick u9l attempt 1", events[0].TickID, events[0].Attempt)
	}
	if events[1].TickID != nil || events[1].Attempt != nil {
		t.Errorf("the run-level line carries %v/%v, want null/null — omitted and null are different claims", events[1].TickID, events[1].Attempt)
	}
	if events[2].RunID != "r-1" {
		t.Errorf("a line carries run_id %q", events[2].RunID)
	}

	// Every written line validates against the contract's schema, which is
	// the only thing that makes the fixture the second reader of the writer.
	lineSchema, _ := lineSchema(t)
	for i, event := range events {
		if errs := schema.Validate(lineSchema, nil, asDocument(t, event)); len(errs) > 0 {
			t.Errorf("line %d as written does not validate against the contract: %v", i, errs)
		}
	}
}

func TestReadRefusesWhatTheSchemaDoesNotName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, EventsName)
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"at":"2026-09-14T12:00:00Z","run_id":"r-1","tick_id":null,"attempt":null,"stage":"held","detail":"a person must release","cost_usd":1.24}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("a line with a field the closed schema does not name was accepted")
	}

	// An attempt below the 1-based first is refused by the beyond-schema rule
	// the contract names in identity.attempt_is_1_based.
	bad := goldenEvent(t)
	zero := 0
	bad.Attempt = &zero
	if err := Open(dir, "r-1").Append(bad); err == nil {
		t.Fatal("an event claiming attempt 0 was appended")
	}
}

func TestFollowDeliversEachLineAsItLands(t *testing.T) {
	feed := Open(t.TempDir(), "r-1")

	// A subscriber may start BEFORE the run writes anything: following a run
	// that has not started is waiting for its first event, not an error.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var got []Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := Follow(ctx, feed.Path(), func(e Event) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}); err != nil {
			t.Errorf("Follow: %v", err)
		}
	}()

	if err := feed.Append(goldenEvent(t)); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(goldenRunLevel(t)); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	}, "the follower to see both appended lines")
	mu.Lock()
	if got[0].Stage != "dispatched" || got[1].Stage != "run_finished" {
		t.Errorf("the follower saw %s then %s — not the order the lines landed", got[0].Stage, got[1].Stage)
	}
	mu.Unlock()
	cancel()
	<-done
}

// TestFollowFromDoesNotReplayAnEarlierRunsTerminalEvent is the resumed-run
// case the from-now cursor exists for (ticfac tick 55i): the feed is
// append-only per RUN ID, so a run that was resumed appends to the same
// file a previous, failed incarnation already ended with run_finished. A
// subscriber that replays history sees that terminal line first and stops
// on it — reporting a failure that already happened and is no longer true.
// A follower started at the standing END of the feed sees only what the
// current incarnation writes.
func TestFollowFromDoesNotReplayAnEarlierRunsTerminalEvent(t *testing.T) {
	feed := Open(t.TempDir(), "r-1")

	// The first incarnation: dispatched, then it failed and said so.
	attempt := 1
	if err := feed.Append(goldenEvent(t)); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(NewEvent(time.Date(2026, 9, 14, 12, 41, 3, 0, time.UTC),
		"r-1", "", nil, "run_finished", "failed: attempt 1 of a1 is missing-result")); err != nil {
		t.Fatal(err)
	}
	cursor, err := End(feed.Path())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var got []Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := FollowFrom(ctx, feed.Path(), cursor, func(e Event) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		}); err != nil {
			t.Errorf("FollowFrom: %v", err)
		}
	}()

	// The resumed run: the same run id, the same file, new events — ending
	// in the CURRENT incarnation's terminal line.
	if err := feed.Append(NewEvent(time.Date(2026, 9, 14, 13, 2, 0, 0, time.UTC),
		"r-1", "a1", &attempt, "settled", "the rejected attempt was released by an operator")); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(NewEvent(time.Date(2026, 9, 14, 13, 9, 41, 0, time.UTC),
		"r-1", "", nil, "run_finished", "completed: every tick closed behind the gate")); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 2
	}, "the follower to see the resumed run's two lines")
	mu.Lock()
	defer mu.Unlock()
	terminals := 0
	for _, event := range got {
		if event.Stage == "run_finished" {
			terminals++
			if !strings.Contains(event.Detail, "completed") {
				t.Errorf("the follower saw %q — a terminal line of an earlier incarnation, not the current run's", event.Detail)
			}
		}
	}
	if terminals != 1 {
		t.Errorf("the follower saw %d terminal lines, want exactly the current run's one", terminals)
	}
	if got[0].Stage != "settled" {
		t.Errorf("the follower's first line was %s, not the first line the resumed run wrote", got[0].Stage)
	}
	cancel()
	<-done
}

func TestFollowRefusesAShrinkingFeed(t *testing.T) {
	feed := Open(t.TempDir(), "r-1")
	if err := feed.Append(goldenEvent(t)); err != nil {
		t.Fatal(err)
	}
	if err := feed.Append(goldenRunLevel(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Follow(ctx, feed.Path(), func(Event) {
		// After the existing lines land, shrink the file behind the
		// follower's back: an append-only feed never shrinks, and a follower
		// that reset its offset silently would miss the lines in between.
		if err := os.Truncate(feed.Path(), 10); err != nil {
			t.Errorf("truncate: %v", err)
		}
	})
	if err == nil {
		t.Fatal("a shrinking feed was followed without complaint")
	}
}

func TestTheFeedLivesWhereTheContractSays(t *testing.T) {
	_, c := lineSchema(t)
	if c.SchemaVersion != 1 || c.Contract != "ticfac.run_event_feed" {
		t.Fatalf("the fixture does not identify itself: %s v%d", c.Contract, c.SchemaVersion)
	}
	if c.Layout.Committed {
		t.Error("the feed is committed, says the contract; then it would be a record and not exhaust")
	}
	if c.Layout.Path != ".ticfac/logs/<run-id>/events.jsonl" {
		t.Errorf("the contract places the feed at %s", c.Layout.Path)
	}
	if got, want := Path("/repo", "r-1"), "/repo/.ticfac/logs/r-1/"+EventsName; got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
	// The identity the contract demands on every line is what the struct
	// carries, and the hint rule is stated in the words a dashboard must not
	// soften.
	for _, id := range c.Identity.EveryLineCarries {
		switch id {
		case "run_id", "tick_id", "attempt":
		default:
			t.Errorf("identity on every line names %q", id)
		}
	}
	if !strings.Contains(c.Subscribe.ALineMeans, "worth looking now") {
		t.Errorf("a_line_means softens the rule: %q", c.Subscribe.ALineMeans)
	}
	if !strings.Contains(c.Subscribe.CompletionIsDecidedBy, "durable evidence") {
		t.Errorf("completion_is_decided_by does not name durable evidence: %q", c.Subscribe.CompletionIsDecidedBy)
	}
}

func waitFor(t *testing.T, condition func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(followTick)
	}
	t.Fatalf("timed out waiting for %s", what)
}
