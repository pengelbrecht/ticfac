package cli

// Tests for the cloud half of the activity line (tick 93n): the worker's
// watch socket, folded the same way the drill-down view folds it, bounded
// to the first events frame and a timeout. fakeAttacher stands in for the
// factory's WebSocket — a recorded or hand-built frame stream, never a real
// dial — so these tests are headless.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/workerview"
)

// fakeAttacher answers attach with a fixed frame stream, buffered whole so
// sending never blocks on a reader that stops early.
type fakeAttacher struct {
	frames []workerview.Frame
	err    error
	// block, when set, never sends and never closes: the timeout test's own
	// "the worker said nothing" shape.
	block bool
}

func (f *fakeAttacher) attach(ctx context.Context) (<-chan workerview.Frame, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(chan workerview.Frame, len(f.frames))
	if f.block {
		return out, nil
	}
	for _, fr := range f.frames {
		out <- fr
	}
	close(out)
	return out, nil
}

// eventsFrame builds one "events" frame carrying the given raw pi-durable
// events, the shape workerview.ParseFrame reads off the real socket.
func eventsFrame(t *testing.T, evs ...string) workerview.Frame {
	t.Helper()
	f := workerview.Frame{Type: workerview.FrameEvents}
	for _, e := range evs {
		if !json.Valid([]byte(e)) {
			t.Fatalf("invalid event JSON: %s", e)
		}
		f.Events = append(f.Events, json.RawMessage(e))
	}
	return f
}

// snapshotWithToolCall is one "snapshot" event whose only entry is an
// assistant message carrying one tool call — the minimal shape
// activityFromSnapshot reads a last action from.
func snapshotWithToolCall(id int, name, argsJSON string, at int64) string {
	return `{"type":"snapshot","entries":[{"id":` + itoa(id) + `,"model":[{"role":"assistant","timestamp":` + itoa64(at) +
		`,"content":[{"type":"toolCall","id":"c` + itoa(id) + `","name":"` + name + `","arguments":` + argsJSON + `}]}]}],` +
		`"tools":[],"inbox":[],"agent":{},"usage":{"models":{},"tools":{}}}`
}

func itoa(n int) string     { return itoa64(int64(n)) }
func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestCloudWorkerSnapshotStopsAtTheFirstEventsFrame: the snapshot read closes
// as soon as the first events frame lands — pi-durable's whole conversation
// so far — never waiting for whatever the worker does next. A second frame
// sitting unread in the channel must not change the answer.
func TestCloudWorkerSnapshotStopsAtTheFirstEventsFrame(t *testing.T) {
	src := &fakeAttacher{frames: []workerview.Frame{
		eventsFrame(t, snapshotWithToolCall(1, "bash", `{"command":"go test ./internal/reconcile"}`, 1000)),
		eventsFrame(t, snapshotWithToolCall(2, "bash", `{"command":"rm -rf /should-not-be-seen"}`, 2000)),
	}}
	m := cloudWorkerSnapshot(context.Background(), src, time.Second)
	if m == nil {
		t.Fatal("the snapshot read nothing")
	}
	if m.Heartbeat.LastTool == nil || m.Heartbeat.LastTool.Args != "go test ./internal/reconcile" {
		t.Errorf("the last tool reads %+v, want the FIRST frame's call, not the second's", m.Heartbeat.LastTool)
	}
}

// TestCloudWorkerSnapshotTimesOutWithoutAFrame: a worker whose socket opens
// but says nothing must not stall a status read — the read returns at the
// timeout, with an empty model, not forever.
func TestCloudWorkerSnapshotTimesOutWithoutAFrame(t *testing.T) {
	start := time.Now()
	m := cloudWorkerSnapshot(context.Background(), &fakeAttacher{block: true}, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("the read took %s, want it bounded near its 30ms timeout", elapsed)
	}
	if m == nil {
		t.Fatal("a timed-out read still answers an empty model, not nil")
	}
	if m.Heartbeat.LastTool != nil {
		t.Errorf("nothing was sent and the heartbeat still names a tool: %+v", m.Heartbeat.LastTool)
	}
}

// TestCloudWorkerSnapshotIsNilWhenTheSocketCannotBeOpened: a worker that
// refuses the watch (settled, gone, no such attempt) answers nil, never a
// read that blocks on a connection that was never made.
func TestCloudWorkerSnapshotIsNilWhenTheSocketCannotBeOpened(t *testing.T) {
	m := cloudWorkerSnapshot(context.Background(), &fakeAttacher{err: errWorkerRefused{err: context.Canceled}}, time.Second)
	if m != nil {
		t.Errorf("the socket could not be opened and the snapshot reads %+v, want nil", m)
	}
}

// TestCloudWorkerActivityRedactsCredentials: the last action is rendered on
// the dashboard and shipped off-host with the phone snapshot exactly like
// the local transcript reader's line (tick ghh) — a credential in the tool
// call's argument must not survive the cloud reader either.
func TestCloudWorkerActivityRedactsCredentials(t *testing.T) {
	src := &fakeAttacher{frames: []workerview.Frame{
		eventsFrame(t, snapshotWithToolCall(1, "bash",
			`{"command":"GH_TOKEN=ghp_0123456789abcdefghijklmnopqrstuv go test ./internal/reconcile"}`, 1000)),
	}}
	input := activityFromSnapshot(cloudWorkerSnapshot(context.Background(), src, time.Second))
	if input == nil {
		t.Fatal("the worker carried a tool call and its activity reads nil")
	}
	if strings.Contains(input.LastAction, "ghp_0123456789") {
		t.Errorf("the last action %q carries the token whole — the model ships off-host", input.LastAction)
	}
	if want := "bash: GH_TOKEN=<redacted> go test ./internal/reconcile"; input.LastAction != want {
		t.Errorf("the last action is %q, want %q", input.LastAction, want)
	}
}

// TestActivityFromSnapshotFoldsARecordedStream: the real frame stream the
// harness's node suite captured from a live local worker (the same fixture
// internal/workerview folds in its own tests) — proof the cloud reader reads
// the production shape, not a fixture invented for this package alone.
func TestActivityFromSnapshotFoldsARecordedStream(t *testing.T) {
	f, err := os.Open("../workerview/testdata/local-watch.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := workerview.New()
	if err := workerview.ReadFrames(f, func(fr workerview.Frame) error {
		return m.Apply(fr, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	input := activityFromSnapshot(m)
	if input == nil {
		t.Fatal("the recorded stream carries a tool call and the activity reads nil")
	}
	if input.LastAction != "write: RESULT-hpk.md" {
		t.Errorf("the last action is %q, want the stream's own last tool call", input.LastAction)
	}
	if len(input.Events) == 0 {
		t.Error("the recorded stream's settled items carry stamps and the sparkline events read empty")
	}
}

// TestActivityFromSnapshotIsNilWithNothingToSay: a model nothing ever
// folded into (an empty snapshot, or none at all) answers nil, the honest
// not-measured — never a zeroed shape a dashboard reads as "quiet since
// forever".
func TestActivityFromSnapshotIsNilWithNothingToSay(t *testing.T) {
	if input := activityFromSnapshot(nil); input != nil {
		t.Errorf("a nil model reads %+v, want nil", input)
	}
	if input := activityFromSnapshot(workerview.New()); input != nil {
		t.Errorf("an empty model reads %+v, want nil", input)
	}
}

// TestCloudWorkerActivityAnswersNilWithoutAClient: the reader is nil-safe
// at its own entry point too — no client, no attempt, nothing to dial.
func TestCloudWorkerActivityAnswersNilWithoutAClient(t *testing.T) {
	if input := cloudWorkerActivity(context.Background(), nil, "run_1", "t1", 1); input != nil {
		t.Errorf("no client reads %+v, want nil", input)
	}
}
