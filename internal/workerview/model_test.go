package workerview

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// fixtureFrames is the real stream the harness's node suite captured from a
// live local worker (harness/test/node/local-host.test.ts, the watch test,
// with TICFAC_CAPTURE_WATCH): a watcher that attached mid-bash, an operator
// steer sent while the bash ran, and the conversation to its end. It is the
// contract between the halves: when the pinned pi-durable's event shape
// moves, regenerating this file is what shows the reader the new shape.
func fixtureFrames(t *testing.T) []Frame {
	t.Helper()
	f, err := os.Open("testdata/local-watch.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var frames []Frame
	if err := ReadFrames(f, func(fr Frame) error {
		frames = append(frames, fr)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return frames
}

// fold applies frames to a fresh model, each commit a second after the last.
func fold(t *testing.T, frames []Frame, start time.Time) *Model {
	t.Helper()
	m := New()
	for i, f := range frames {
		if err := m.Apply(f, start.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	return m
}

func kinds(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		label := string(it.Kind)
		if it.Tool != "" {
			label += ":" + it.Tool
		}
		out = append(out, label)
	}
	return out
}

func TestTheModelFoldsARealLocalWorkerStream(t *testing.T) {
	frames := fixtureFrames(t)
	if len(frames) < 3 || frames[0].Type != FrameEvents || frames[len(frames)-1].Type != FrameEnd {
		t.Fatalf("the fixture is not a whole watch: %d frames", len(frames))
	}
	m := fold(t, frames, time.Unix(1_800_000_000, 0))

	want := []string{
		"input",
		"thinking", "tool_call:bash", "tool_result:bash",
		"steer",
		"thinking", "tool_call:write", "tool_result:write",
		"thinking", "text",
	}
	if got := kinds(m.Items); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the conversation folded to\n  %v\nwant\n  %v", got, want)
	}
	// The snapshot's settled call carries its argument summary; the result
	// carries the tool's output; the steer is labelled a steer although its
	// submission named its entry AFTER the entry landed.
	if m.Items[2].Args != "echo working; sleep 2; echo stepped > step.txt" {
		t.Errorf("the bash call's args: %q", m.Items[2].Args)
	}
	if m.Items[3].Text != "working\n" || m.Items[3].IsError {
		t.Errorf("the bash result: %+v", m.Items[3])
	}
	if m.Items[4].Text != "Write the report next." {
		t.Errorf("the steer: %+v", m.Items[4])
	}
	if m.Items[6].Args != "RESULT-hpk.md" {
		t.Errorf("the write call's args: %q", m.Items[6].Args)
	}
	if m.Items[9].Text != "watched and steered" {
		t.Errorf("the answer: %q", m.Items[9].Text)
	}

	// The heartbeat is the commit sequence: every events frame after the
	// snapshot is one commit.
	h := m.Heartbeat
	if h.Commits != len(frames)-2 {
		t.Errorf("commits: %d, want %d (every events frame after the snapshot)", h.Commits, len(frames)-2)
	}
	if h.ModelCalls != 3 || h.ToolCalls != 2 {
		t.Errorf("model calls %d, tool calls %d; want 3 and 2", h.ModelCalls, h.ToolCalls)
	}
	if h.LastTool == nil || h.LastTool.Tool != "write" {
		t.Errorf("the last tool: %+v", h.LastTool)
	}
	if h.InputTokens == 0 || h.OutputTokens == 0 {
		t.Errorf("the usage was not read: %+v", h)
	}
	if m.ModelName != "faux/faux-1" {
		t.Errorf("the model: %q", m.ModelName)
	}
	if m.Live || len(m.Running) != 0 || m.PartialItems() != nil || m.Queued != 0 {
		t.Errorf("a settled conversation still reads live: live=%v running=%v queued=%d", m.Live, m.Running, m.Queued)
	}
	if m.Ended != "the worker process is exiting" {
		t.Errorf("the end: %q", m.Ended)
	}
}

func TestAWatcherAttachedMidToolSeesTheToolRunningAndItsOutput(t *testing.T) {
	frames := fixtureFrames(t)
	m := New()
	at := time.Unix(1_800_000_000, 0)
	if err := m.Apply(frames[0], at); err != nil {
		t.Fatal(err)
	}
	// The snapshot alone: no commit yet, the bash running with the
	// arguments its settled call carries, the run live.
	if m.Heartbeat.Commits != 0 {
		t.Errorf("the snapshot counted as a commit")
	}
	if len(m.Running) != 1 || m.Running[0].Name != "bash" || !strings.HasPrefix(m.Running[0].Args, "echo working") {
		t.Fatalf("running after the snapshot: %+v", m.Running)
	}
	if !m.Live || Doing(m) != "running bash" {
		t.Errorf("live=%v doing=%q", m.Live, Doing(m))
	}
	// The next commit is the tool's live output.
	if err := m.Apply(frames[1], at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if m.Running[0].Output != "working\n" {
		t.Errorf("the live output: %q", m.Running[0].Output)
	}
	if m.Heartbeat.Commits != 1 || !m.Heartbeat.LastCommit.Equal(at.Add(time.Second)) {
		t.Errorf("heartbeat after one commit: %+v", m.Heartbeat)
	}
	// The steer is queued before it is placed.
	if err := m.Apply(frames[2], at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if m.Queued != 1 {
		t.Errorf("queued: %d, want the steer waiting for the tool round", m.Queued)
	}
}

func events(t *testing.T, evs ...string) Frame {
	t.Helper()
	f := Frame{Type: FrameEvents}
	for _, e := range evs {
		if !json.Valid([]byte(e)) {
			t.Fatalf("invalid event JSON: %s", e)
		}
		f.Events = append(f.Events, json.RawMessage(e))
	}
	return f
}

func TestTheModelStreamsTheMessageTheModelIsWriting(t *testing.T) {
	m := New()
	at := time.Unix(1_800_000_000, 0)
	steps := []Frame{
		events(t, `{"type":"snapshot","entries":[],"tools":[],"inbox":[],"run":{"inputs":[1]},"agent":{},"usage":{"models":{},"tools":{}}}`),
		events(t, `{"type":"message_start","message":{"role":"assistant","content":[]}}`),
		events(t, `{"type":"message_update","usage":{},"changes":[{"type":"thinking_start","contentIndex":0,"block":{"type":"thinking","thinking":""}},{"type":"thinking_delta","contentIndex":0,"delta":"Read the "}]}`),
		events(t, `{"type":"message_update","usage":{},"changes":[{"type":"thinking_delta","contentIndex":0,"delta":"watch command first."}]}`),
	}
	for i, f := range steps {
		if err := m.Apply(f, at.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	partial := m.PartialItems()
	if len(partial) != 1 || partial[0].Kind != KindThinking || partial[0].Text != "Read the watch command first." {
		t.Fatalf("the streaming thinking: %+v", partial)
	}
	if Doing(m) != "thinking" {
		t.Errorf("doing: %q", Doing(m))
	}

	// A tool call streams its arguments as JSON text before they are whole.
	if err := m.Apply(events(t,
		`{"type":"message_update","usage":{},"changes":[{"type":"toolcall_start","contentIndex":1,"block":{"type":"toolCall","id":"c1","name":"bash","arguments":{}}},{"type":"toolcall_delta","contentIndex":1,"path":[],"delta":"{\"command\":\"go te"}]}`,
	), at.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	partial = m.PartialItems()
	if len(partial) != 2 || partial[1].Tool != "bash" || partial[1].Args != `{"command":"go te` {
		t.Fatalf("the streaming call: %+v", partial)
	}
	if Doing(m) != "calling bash" {
		t.Errorf("doing: %q", Doing(m))
	}

	// The settled message replaces the partial.
	if err := m.Apply(events(t,
		`{"type":"message_end","entry":{"id":5,"kind":"pi.assistant","model":[{"role":"assistant","content":[{"type":"thinking","thinking":"Read the watch command first."},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"go test ./internal/cli/"}}],"timestamp":1800000011000,"stopReason":"toolUse"}]}}`,
		`{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":"go test ./internal/cli/"}}`,
	), at.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if m.PartialItems() != nil {
		t.Errorf("the partial outlived its message_end: %+v", m.PartialItems())
	}
	if got := kinds(m.Items); strings.Join(got, " ") != "thinking tool_call:bash" {
		t.Errorf("settled: %v", got)
	}
	if len(m.Running) != 1 || m.Running[0].Args != "go test ./internal/cli/" {
		t.Errorf("running: %+v", m.Running)
	}

	// Live output trims its front and appends, as pi-durable bounds it.
	for _, e := range []string{
		`{"type":"tool_execution_update","toolCallId":"c1","toolName":"bash","output":{"set":"=== RUN TestA\n"}}`,
		`{"type":"tool_execution_update","toolCallId":"c1","toolName":"bash","output":{"trimStart":4,"append":"--- PASS: TestA\n"}}`,
	} {
		if err := m.Apply(events(t, e), at.Add(12*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if m.Running[0].Output != "RUN TestA\n--- PASS: TestA\n" {
		t.Errorf("the trimmed output: %q", m.Running[0].Output)
	}
}

func TestAnOverflowSnapshotIsACommitAndDoesNotDuplicate(t *testing.T) {
	frames := fixtureFrames(t)
	m := fold(t, frames[:4], time.Unix(1_800_000_000, 0))
	before := len(m.Items)
	commits := m.Heartbeat.Commits
	// pi-durable answers a watcher that fell behind with one snapshot in
	// place of the batches it dropped: the same conversation, re-stated.
	if err := m.Apply(frames[0], time.Unix(1_800_000_100, 0)); err != nil {
		t.Fatal(err)
	}
	if m.Heartbeat.Commits != commits+1 {
		t.Errorf("an overflow snapshot is a commit: %d -> %d", commits, m.Heartbeat.Commits)
	}
	if len(m.Items) > before {
		t.Errorf("the re-stated snapshot duplicated items: %d -> %d", before, len(m.Items))
	}
}

func TestRetriesAndFailuresAreSaid(t *testing.T) {
	m := New()
	at := time.Unix(1_800_000_000, 0)
	for _, e := range []string{
		`{"type":"snapshot","entries":[],"tools":[],"inbox":[],"agent":{},"usage":{"models":{},"tools":{}}}`,
		`{"type":"auto_retry_start","attempt":2,"at":1800000005000,"errorMessage":"429 rate limited"}`,
	} {
		if err := m.Apply(events(t, e), at); err != nil {
			t.Fatal(err)
		}
	}
	if Doing(m) != "retrying the model call" || m.Retry != "429 rate limited" {
		t.Errorf("doing %q, retry %q", Doing(m), m.Retry)
	}
	if err := m.Apply(events(t,
		`{"type":"auto_retry_end","attempt":2}`,
		`{"type":"task_failed","taskId":9,"kind":"pi.tool","message":"the env is gone"}`,
	), at); err != nil {
		t.Fatal(err)
	}
	if m.Retry != "" {
		t.Errorf("the retry outlived its end")
	}
	last := m.Items[len(m.Items)-1]
	if last.Kind != KindNote || !last.IsError || last.Text != "pi.tool failed: the env is gone" {
		t.Errorf("the failure note: %+v", last)
	}
}

func TestAFrameThatIsNotAFrameIsAnError(t *testing.T) {
	if _, err := ParseFrame([]byte(`{"ok":false,"error":"this worker serves no watch"}`)); err == nil ||
		!strings.Contains(err.Error(), "this worker serves no watch") {
		t.Errorf("a refusal line: %v", err)
	}
	if _, err := ParseFrame([]byte(`not json`)); err == nil {
		t.Errorf("a non-JSON line parsed")
	}
	if _, err := ParseFrame([]byte(`{"events":[]}`)); err == nil {
		t.Errorf("a typeless frame parsed")
	}
	m := New()
	if err := m.Apply(Frame{Type: FrameEvents, Events: []json.RawMessage{json.RawMessage(`[1]`)}}, time.Now()); err == nil {
		t.Errorf("an event that is not an object folded")
	}
}

func TestTheCloudSocketsStateAndLogFramesFold(t *testing.T) {
	m := New()
	exit := 0
	if err := m.Apply(Frame{Type: FrameState, State: &AttemptState{Phase: "booting", Harness: "pi-durable"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if Doing(m) != "booting" {
		t.Errorf("doing: %q", Doing(m))
	}
	if err := m.Apply(Frame{Type: FrameLog, Text: "ticks-worker: cloned\nticks-worker: setup ok\n"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(m.Log) != 2 || m.Log[1] != "ticks-worker: setup ok" {
		t.Errorf("log: %v", m.Log)
	}
	if err := m.Apply(Frame{Type: FrameState, State: &AttemptState{Phase: "settled", ExitCode: &exit}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if Doing(m) != "settled (exit 0)" {
		t.Errorf("doing: %q", Doing(m))
	}
}
