package cli

// Tests for the activity line (tick 93n), both halves: the CLOUD half reads
// the factory's watch socket, the LOCAL half the worker's own watch door —
// folded the same way the drill-down view folds it, bounded to the first
// events frame and a timeout. fakeAttacher and workerStandIn stand in for
// the two real doors — a recorded or hand-built frame stream, never a real
// model call — so these tests are headless.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/statusmodel"
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
	m := workerSnapshot(context.Background(), src, time.Second)
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
	m := workerSnapshot(context.Background(), &fakeAttacher{block: true}, 30*time.Millisecond)
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
	m := workerSnapshot(context.Background(), &fakeAttacher{err: errWorkerRefused{err: context.Canceled}}, time.Second)
	if m != nil {
		t.Errorf("the socket could not be opened and the snapshot reads %+v, want nil", m)
	}
}

// TestCloudWorkerActivityRedactsCredentials: the last action is rendered on
// the dashboard and shipped off-host with the phone snapshot exactly like
// the local transcript reader's line (tick ghh) — a credential in the tool
// call's argument must not survive the cloud reader either, as a token and
// as a URL carrying one.
func TestCloudWorkerActivityRedactsCredentials(t *testing.T) {
	src := &fakeAttacher{frames: []workerview.Frame{
		eventsFrame(t, snapshotWithToolCall(1, "bash",
			`{"command":"GH_TOKEN=ghp_0123456789abcdefghijklmnopqrstuv go test ./internal/reconcile"}`, 1000)),
	}}
	input := activityFromSnapshot(workerSnapshot(context.Background(), src, time.Second))
	if input == nil {
		t.Fatal("the worker carried a tool call and its activity reads nil")
	}
	if strings.Contains(input.LastAction, "ghp_0123456789") {
		t.Errorf("the last action %q carries the token whole — the model ships off-host", input.LastAction)
	}
	if want := "bash: GH_TOKEN=<redacted> go test ./internal/reconcile"; input.LastAction != want {
		t.Errorf("the last action is %q, want %q", input.LastAction, want)
	}

	// A URL carrying its credential in userinfo and query must not survive
	// the line either: the same pattern set the local reader redacts with.
	src = &fakeAttacher{frames: []workerview.Frame{
		eventsFrame(t, snapshotWithToolCall(1, "curl",
			`{"url":"https://user:hunter2@example.com/pull/1?token=shh"}`, 1000)),
	}}
	input = activityFromSnapshot(workerSnapshot(context.Background(), src, time.Second))
	if input == nil {
		t.Fatal("the worker carried a tool call and its activity reads nil")
	}
	if strings.Contains(input.LastAction, "hunter2") || strings.Contains(input.LastAction, "token=shh") {
		t.Errorf("the last action %q carries the URL's credential — the model ships off-host", input.LastAction)
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
	// The stream's final message speaks ("watched and steered") without a
	// tool call of its own, but its stamp ties the last write's — the same
	// snapshot stamp every item of one model call carries — and a tie goes
	// to the tool call, the concrete act.
}

// TestActivityFromSnapshotPrefersTheNewestAct (tick 93n): the excerpt is
// the worker's newest act — its latest assistant sentence when the newest
// act was speaking, its last tool call otherwise — whichever the
// conversation stamps later.
func TestActivityFromSnapshotPrefersTheNewestAct(t *testing.T) {
	src := &fakeAttacher{frames: []workerview.Frame{
		eventsFrame(t,
			`{"type":"snapshot","entries":[{"id":1,"model":[{"role":"assistant","timestamp":1000,"content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"go build ./..."}}]}]},{"id":2,"model":[{"role":"assistant","timestamp":5000,"content":[{"type":"text","text":"Refactoring the reconciliation loop next"}]}]}],"tools":[],"inbox":[],"agent":{},"usage":{"models":{},"tools":{}}}`),
	}}
	input := activityFromSnapshot(workerSnapshot(context.Background(), src, time.Second))
	if input == nil {
		t.Fatal("the worker said something and its activity reads nil")
	}
	if input.LastAction != "Refactoring the reconciliation loop next" {
		t.Errorf("the last action is %q, want the newest act — the model's own sentence", input.LastAction)
	}
	if input.LastActionAt.UnixMilli() != 5000 {
		t.Errorf("the last action is stamped %v, want the sentence's own stamp", input.LastActionAt)
	}

	// And a credential the model echoed must not survive the sentence.
	src = &fakeAttacher{frames: []workerview.Frame{
		eventsFrame(t,
			`{"type":"snapshot","entries":[{"id":1,"model":[{"role":"assistant","timestamp":1000,"content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"echo done"}}]}]},{"id":2,"model":[{"role":"assistant","timestamp":9000,"content":[{"type":"text","text":"the token is ghp_0123456789abcdefghijklmnopqrstuv, rotate it"}]}]}],"tools":[],"inbox":[],"agent":{},"usage":{"models":{},"tools":{}}}`),
	}}
	input = activityFromSnapshot(workerSnapshot(context.Background(), src, time.Second))
	if input == nil {
		t.Fatal("the worker said something and its activity reads nil")
	}
	if strings.Contains(input.LastAction, "ghp_0123456789") {
		t.Errorf("the sentence %q carries the token whole — the model ships off-host", input.LastAction)
	}
	if want := "the token is <redacted>, rotate it"; input.LastAction != want {
		t.Errorf("the sentence is %q, want %q", input.LastAction, want)
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

// ---- the local half -------------------------------------------------------

// The local half reads the same conversation stream through the worker's own
// door: the same Unix socket the drill-down opens, found through a real
// attempt record under a real state root. workerStandIn (worker_watch_test.go)
// is the door.

// TestLocalWorkerActivityReadsTheWorkersWatchDoor (tick 93n): a local
// pi-durable worker's worktree names no session transcript — its
// conversation is its own storage — so the activity reader opens the
// worker's door and folds the SNAPSHOT frame: the worker's whole
// conversation so far, so its newest act is already in it. The stream is the
// real frame stream the harness's node suite captured from a live local
// worker, and the read takes the first events frame's own answer — the read
// is not a watch, it opens, takes the state of the conversation, closes.
func TestLocalWorkerActivityReadsTheWorkersWatchDoor(t *testing.T) {
	execDir := t.TempDir()
	root := filepath.Join(execDir, "runs")
	t.Setenv(statusmodel.EnvExecStateDir, execDir)
	w := newWorkerStandIn(t, root, "epic-2jn", "6dh", 3, "pi")
	w.frames = fixtureLines(t)
	defer w.listen()()

	input := localWorkerActivity(context.Background(), "epic-2jn", "6dh", 3)
	if input == nil {
		t.Fatal("the worker is listening and its activity reads nil")
	}
	if input.LastAction != "bash: echo working; sleep 2; echo stepped > step.txt" {
		t.Errorf("the last action is %q, want the snapshot's own newest tool call", input.LastAction)
	}
	if input.LastActionAt.IsZero() {
		t.Error("the last action carries no stamp; the dashboard cannot state its age")
	}
	if len(input.Events) == 0 {
		t.Error("the snapshot's settled items carry stamps and the window's events read empty")
	}
}

// TestLocalWorkerActivityPrefersTheNewestAct (tick 93n): the excerpt is the
// worker's newest act — its latest assistant sentence when the newest act
// was speaking, its last tool call otherwise. The snapshot carries both as
// the whole conversation so far; the newest stamp wins, and a tie goes to
// the tool call, the concrete action.
func TestLocalWorkerActivityPrefersTheNewestAct(t *testing.T) {
	execDir := t.TempDir()
	root := filepath.Join(execDir, "runs")
	t.Setenv(statusmodel.EnvExecStateDir, execDir)
	w := newWorkerStandIn(t, root, "epic-2jn", "6dh", 3, "pi")
	w.frames = [][]byte{[]byte(`{"type":"events","events":[{"type":"snapshot","entries":[` +
		`{"id":1,"model":[{"role":"assistant","timestamp":1000,"content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"go build ./..."}}]}]},` +
		`{"id":2,"model":[{"role":"assistant","timestamp":5000,"content":[{"type":"text","text":"Refactoring the reconciliation loop next"}]}]}],` +
		`"tools":[],"inbox":[],"agent":{},"usage":{"models":{},"tools":{}}}]}`)}
	defer w.listen()()

	input := localWorkerActivity(context.Background(), "epic-2jn", "6dh", 3)
	if input == nil {
		t.Fatal("the worker said something and its activity reads nil")
	}
	if input.LastAction != "Refactoring the reconciliation loop next" {
		t.Errorf("the last action is %q, want the newest act — the model's own sentence", input.LastAction)
	}

	// A tie goes to the tool call: a message that both spoke and called a
	// tool is described by the call.
	w2 := newWorkerStandIn(t, root, "epic-2jn", "6dh", 4, "pi")
	w2.frames = [][]byte{[]byte(`{"type":"events","events":[{"type":"snapshot","entries":[` +
		`{"id":1,"model":[{"role":"assistant","timestamp":9000,"content":[{"type":"text","text":"considering the design"},{"type":"toolCall","id":"c2","name":"bash","arguments":{"command":"go vet ./..."}}]}]}],` +
		`"tools":[],"inbox":[],"agent":{},"usage":{"models":{},"tools":{}}}]}`)}
	defer w2.listen()()
	if input := localWorkerActivity(context.Background(), "epic-2jn", "6dh", 4); input == nil || input.LastAction != "bash: go vet ./..." {
		t.Errorf("a tie reads %+v, want the tool call", input)
	}
}

// TestLocalWorkerActivityAnswersNilWithoutAWorkerToRead: a state root with
// no attempt record for the (run, tick, attempt), and a worker whose door is
// not listening right now (between one runner process and its next) — both
// answer nil, the honest not-measured, never a guessed line.
func TestLocalWorkerActivityAnswersNilWithoutAWorkerToRead(t *testing.T) {
	execDir := t.TempDir()
	root := filepath.Join(execDir, "runs")
	t.Setenv(statusmodel.EnvExecStateDir, execDir)
	if input := localWorkerActivity(context.Background(), "epic-2jn", "6dh", 3); input != nil {
		t.Errorf("no attempt record and the activity reads %+v, want nil", input)
	}

	newWorkerStandIn(t, root, "epic-2jn", "6dh", 3, "pi")
	// Not listening: the attempt record and its door exist, nothing stands
	// behind the door.
	if input := localWorkerActivity(context.Background(), "epic-2jn", "6dh", 3); input != nil {
		t.Errorf("nothing listens on the door and the activity reads %+v, want nil", input)
	}
}

// TestWorkerActivityRoutesByHost (tick 93n): the one gatherer dispatches on
// the client the caller names — nil is this machine's door, a client the
// factory's socket. A nil-client call against a state root with no worker
// costs no socket and answers nil.
func TestWorkerActivityRoutesByHost(t *testing.T) {
	t.Setenv(statusmodel.EnvExecStateDir, t.TempDir())
	if input := workerActivity(context.Background(), nil, "epic-2jn", "6dh", 3); input != nil {
		t.Errorf("a local dispatch with no worker reads %+v, want nil", input)
	}
	if input := workerActivity(context.Background(), nil, "epic-2jn", "6dh", 0); input != nil {
		t.Errorf("attempt 0 reads %+v, want nil", input)
	}
}
