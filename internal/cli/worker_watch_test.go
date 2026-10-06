package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/workerview"
)

// The worker view's wiring (tick y03): which worker, how it is reached, and
// how the watch lives across the worker's own process boundaries. The frame
// and the model are pinned in internal/workerview; the socket protocol from
// its other side in the harness's node suite (harness/test/node/
// local-host.test.ts, which drives the real worker). Here the worker is a
// stand-in speaking the pinned protocol over a real Unix socket, found
// through a real attempt record under a state root — the same record and
// layout the subprocess executor and the run write.

// workerStandIn is a durable worker's door: a Unix socket answering steers
// and watches the way harness/src/local/steer-socket.ts does.
type workerStandIn struct {
	t        *testing.T
	sock     string
	stateDir string

	mu       sync.Mutex
	steers   []map[string]any
	refuse   string   // a non-empty refusal answers every steer with it
	frames   [][]byte // what a watch is sent, one line each
	listener net.Listener
}

// shortDir is a directory a Unix socket can be bound in: the kernel bounds
// a socket path (104 bytes on darwin), and a test's own temp dir is deeper.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ticfac-ww-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newWorkerStandIn records one attempt of `tick` under root the way the
// run lays it out — <root>/<run>/<tick>/<attempt>/<repo key>/<attempt key>/
// attempt.json — naming the socket the stand-in will listen on.
func newWorkerStandIn(t *testing.T, root, runID, tick string, attempt int, runner string) *workerStandIn {
	t.Helper()
	stateDir := filepath.Join(root, runID, tick, strconv.Itoa(attempt), "repokey0123456789", "attemptkey0123456")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := &workerStandIn{t: t, stateDir: stateDir}
	record := map[string]any{
		"job_id":  "run-" + runID + "/tick-" + tick + "/attempt-" + strconv.Itoa(attempt),
		"attempt": attempt, "tick_id": tick, "runner": runner,
	}
	if runner == "pi" {
		s.sock = filepath.Join(shortDir(t), "steer.sock")
		record["steer_sock"] = s.sock
	}
	raw, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(stateDir, "attempt.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

// listen starts answering on the socket; stop() closes it and removes the
// file, as a worker process's exit does.
func (s *workerStandIn) listen() (stop func()) {
	s.t.Helper()
	listener, err := net.Listen("unix", s.sock)
	if err != nil {
		s.t.Fatalf("the stand-in could not listen: %v", err)
	}
	s.listener = listener
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return func() {
		listener.Close()
		<-done
		_ = os.Remove(s.sock)
	}
}

func (s *workerStandIn) serve(conn net.Conn) {
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var request map[string]any
	if json.Unmarshal(line, &request) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if request["type"] == "watch" {
		for _, frame := range s.frames {
			_, _ = conn.Write(append(append([]byte{}, frame...), '\n'))
		}
		// A watch that ends with an end frame is a process exiting, as
		// the harness's is: its socket goes with it.
		if n := len(s.frames); n > 0 && bytes.Contains(s.frames[n-1], []byte(`"type":"end"`)) {
			s.listener.Close()
			_ = os.Remove(s.sock)
		}
		return
	}
	s.steers = append(s.steers, request)
	reply := map[string]any{"ok": s.refuse == "", "requestId": request["requestId"]}
	if s.refuse != "" {
		reply["error"] = s.refuse
	}
	raw, _ := json.Marshal(reply)
	_, _ = conn.Write(append(raw, '\n'))
}

// settle writes the supervisor's settle marker: the runner's last exit.
func (s *workerStandIn) settle() {
	if err := os.WriteFile(filepath.Join(s.stateDir, "runner.exit"), []byte("0\n"), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// fixtureLines is the real local stream internal/workerview pins.
func fixtureLines(t *testing.T) [][]byte {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal", "workerview", "testdata", "local-watch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		out = append(out, line)
	}
	return out
}

func TestSteerRoundTripsToALiveLocalWorker(t *testing.T) {
	root := t.TempDir()
	w := newWorkerStandIn(t, root, "epic-43y", "y03", 17, "pi")
	defer w.listen()()

	var stdout, stderr bytes.Buffer
	code := Run([]string{"steer", "epic-43y", "y03", "Write", "the", "report", "next.",
		"--state-root", root, "--request-id", "op-1", "--json", "--repo", t.TempDir()}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("steer exited %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	var doc steerDoc
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("not one document: %v\n%s", err, stdout.String())
	}
	if doc.Schema != "ticfac.steer.v1" || doc.State != agentStateDone || doc.Host != "local" ||
		doc.Attempt != 17 || doc.RunID != "epic-43y" || doc.RequestID != "op-1" {
		t.Errorf("the document: %+v", doc)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.steers) != 1 || w.steers[0]["text"] != "Write the report next." || w.steers[0]["requestId"] != "op-1" {
		t.Errorf("the worker received %v", w.steers)
	}
}

func TestARefusedSteerIsAFailureThatSaysWhy(t *testing.T) {
	root := t.TempDir()
	w := newWorkerStandIn(t, root, "epic-43y", "y03", 17, "pi")
	w.refuse = "the attempt is not conversing"
	defer w.listen()()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"steer", "epic-43y", "y03", "hello", "--state-root", root, "--repo", t.TempDir()}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("a refused steer exited %d, want %d", code, exitGeneric)
	}
	if !strings.Contains(stderr.String(), "the attempt is not conversing") {
		t.Errorf("the refusal's reason was not said: %q", stderr.String())
	}
}

func TestACLIRunnerHasNoConversationToWatchOrSteer(t *testing.T) {
	root := t.TempDir()
	newWorkerStandIn(t, root, "epic-43y", "y03", 3, "claude")
	for _, args := range [][]string{
		{"steer", "epic-43y", "y03", "hello"},
		{"watch", "epic-43y", "y03"},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(append(args, "--state-root", root, "--repo", t.TempDir()), &stdout, &stderr)
		if code != exitGeneric {
			t.Errorf("%s on a claude attempt exited %d, want %d", args[0], code, exitGeneric)
		}
		if !strings.Contains(stderr.String(), "only a pi-durable worker has a live conversation") {
			t.Errorf("%s did not say why: %q", args[0], stderr.String())
		}
	}
}

func TestSteerNeedsTextAndAWorker(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"steer", "epic-43y", "y03"}, &stdout, &stderr); code != exitUsage {
		t.Errorf("a steer with no text exited %d, want %d", code, exitUsage)
	}
	stdout.Reset()
	stderr.Reset()
	t.Setenv("HOME", t.TempDir()) // no factory configured either
	code := Run([]string{"steer", "epic-43y", "nope", "hi", "--state-root", t.TempDir(), "--repo", t.TempDir()}, &stdout, &stderr)
	if code != exitGeneric || !strings.Contains(stderr.String(), "no attempt of this tick was dispatched on this machine") {
		t.Errorf("a tick with no worker: exit %d, %q", code, stderr.String())
	}
}

// fastWorkerClock makes the watch's reaction time a test's, not a person's.
func fastWorkerClock(t *testing.T) {
	t.Helper()
	poll, redraw := workerPoll, workerRedraw
	workerPoll, workerRedraw = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { workerPoll, workerRedraw = poll, redraw })
}

func TestTheWorkerWatchStreamsPlainLinesAndEndsWhenTheAttemptSettles(t *testing.T) {
	fastWorkerClock(t)
	root := t.TempDir()
	w := newWorkerStandIn(t, root, "epic-43y", "hpk", 9, "pi")
	w.frames = fixtureLines(t)
	stop := w.listen()

	var stdout, stderr bytes.Buffer
	out := &syncWriter{w: &stdout}
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"watch", "epic-43y", "hpk", "--state-root", root, "--repo", t.TempDir()}, out, &stderr)
	}()
	// The stream's end frame says the process is exiting; it does, and the
	// supervisor records the runner's last exit.
	waitForText(t, out, "■ the worker process is exiting")
	stop()
	w.settle()
	select {
	case code := <-done:
		if code != exitSuccess {
			t.Fatalf("the watch of a settled attempt exited %d:\n%s%s", code, stdout.String(), stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the watch did not end when the attempt settled:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{
		"› input     do the job",
		"∴ thinking  The tick wants a step file; a bash round writes it.",
		"▸ bash      echo working; sleep 2; echo stepped > step.txt",
		"  ✓ bash    working",
		"» steer     Write the report next.",
		"▸ write     RESULT-hpk.md",
		"✎ answer    watched and steered",
		"■ hpk#9 · epic-43y settled — its conversation is over",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the plain watch did not say %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "» steer") != 1 {
		t.Errorf("an item printed twice:\n%s", text)
	}
}

func TestTheWorkerWatchFollowsTheWorkerAcrossARelaunch(t *testing.T) {
	fastWorkerClock(t)
	root := t.TempDir()
	w := newWorkerStandIn(t, root, "epic-43y", "hpk", 9, "pi")
	lines := fixtureLines(t)
	// The first process serves the snapshot and its first commits, then
	// exits; the relaunched one serves the whole conversation again (its
	// snapshot) and the rest.
	w.frames = append(append([][]byte{}, lines[:3]...), []byte(`{"type":"end","reason":"the worker process is exiting"}`))
	stop := w.listen()

	var stdout, stderr bytes.Buffer
	out := &syncWriter{w: &stdout}
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"watch", "epic-43y", "hpk", "--state-root", root, "--repo", t.TempDir()}, out, &stderr)
	}()
	waitForText(t, out, "■ the worker process is exiting")
	stop()
	waitForText(t, out, "… waiting for hpk#9 · epic-43y's next process")

	// The relaunch: a snapshot of the whole conversation so far, then on.
	w.mu.Lock()
	w.frames = lines[3:]
	snapshot := relaunchSnapshot(t, lines)
	w.frames = append([][]byte{snapshot}, w.frames...)
	w.mu.Unlock()
	stop = w.listen()
	waitForText(t, out, "✎ answer    watched and steered")
	stop()
	w.settle()
	select {
	case code := <-done:
		if code != exitSuccess {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the watch did not end:\n%s", out.String())
	}
	if n := strings.Count(out.String(), "› input     do the job"); n != 1 {
		t.Errorf("the relaunch's snapshot re-printed the conversation (%d inputs):\n%s", n, out.String())
	}
}

// relaunchSnapshot is a snapshot of the conversation as a relaunched
// process would state it: the fixture's own first snapshot, its entries
// extended with every entry settled in the first process's commits.
func relaunchSnapshot(t *testing.T, lines [][]byte) []byte {
	t.Helper()
	var first struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	snapshot := first.Events[0]
	entries := snapshot["entries"].([]any)
	for _, line := range lines[1:3] {
		var f struct {
			Events []map[string]any `json:"events"`
		}
		if err := json.Unmarshal(line, &f); err != nil {
			t.Fatal(err)
		}
		for _, e := range f.Events {
			if e["type"] == "message_end" {
				entries = append(entries, e["entry"])
			}
		}
	}
	snapshot["entries"] = entries
	snapshot["tools"] = []any{}
	raw, err := json.Marshal(map[string]any{"type": "events", "events": []any{snapshot}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestThePipedWorkerWatchWritesTheHeartbeat(t *testing.T) {
	m := workerview.New()
	frames := fixtureLines(t)
	now := time.Unix(1_800_000_000, 0)
	var buf bytes.Buffer
	out := &plainWorkerOutput{w: &buf, title: "hpk#9", heartbeat: time.Minute}
	for i, line := range frames[:3] {
		f, err := workerview.ParseFrame(line)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Apply(f, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		out.update(m, now.Add(time.Duration(i)*time.Second))
	}
	out.tick(m, now.Add(30*time.Second))
	if strings.Contains(buf.String(), "heartbeat") {
		t.Fatalf("a heartbeat before its interval:\n%s", buf.String())
	}
	out.tick(m, now.Add(61*time.Second))
	out.tick(m, now.Add(122*time.Second))
	beats := []string{}
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, "heartbeat") {
			beats = append(beats, line)
		}
	}
	if len(beats) != 2 {
		t.Fatalf("want two heartbeat lines a minute apart:\n%s", buf.String())
	}
	// The second line is a stuck worker's: no new commit since the first.
	if !strings.Contains(beats[0], "hpk#9 heartbeat: 2 commits, last 59s ago") ||
		!strings.Contains(beats[1], "2 commits (+0 in 1m01s), last 2m00s ago") {
		t.Errorf("the heartbeat lines:\n%s", strings.Join(beats, "\n"))
	}
}

func TestTheLiveWorkerWatchRedrawsInPlace(t *testing.T) {
	m := workerview.New()
	var buf bytes.Buffer
	out := &liveWorkerOutput{w: &buf, title: "hpk#9", width: 80, height: 30}
	now := time.Unix(1_800_000_000, 0)
	for i, line := range fixtureLines(t)[:2] {
		f, err := workerview.ParseFrame(line)
		if err != nil {
			t.Fatal(err)
		}
		_ = m.Apply(f, now)
		out.update(m, now.Add(time.Duration(i)*time.Second))
	}
	text := buf.String()
	if !strings.Contains(text, "\x1b[") || !strings.Contains(text, "\r\x1b[J") {
		t.Errorf("the second frame was not drawn over the first:\n%q", text)
	}
	if !strings.Contains(text, "⋯ bash") || !strings.Contains(text, "│ working") {
		t.Errorf("the running tool and its output are not in the frame:\n%s", text)
	}
	out.note("a line kept above the block")
	if !strings.Contains(buf.String(), "\x1b[La line kept above the block") {
		t.Errorf("a note is not inserted above the block:\n%q", buf.String())
	}
}

func TestAnInterruptedWorkerWatchExitsStillRunning(t *testing.T) {
	fastWorkerClock(t)
	root := t.TempDir()
	w := newWorkerStandIn(t, root, "epic-43y", "hpk", 9, "pi")
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	out := &syncWriter{w: &stdout}
	done := make(chan int, 1)
	go func() {
		fl := workerWatchFlags{attempt: new(int), heartbeat: new(time.Duration), stateRoot: &root}
		done <- workerWatchCommand(ctx, t.TempDir(), "epic-43y", "hpk", fl, out, &stderr)
	}()
	_ = w // no process listens: the watch waits for one
	waitForText(t, out, "waiting for")
	cancel()
	select {
	case code := <-done:
		if code != exitRunning {
			t.Errorf("an interrupted worker watch exited %d, want %d", code, exitRunning)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch did not stop on interrupt")
	}
	if !strings.Contains(stderr.String(), "the worker keeps going") {
		t.Errorf("the interrupt was not said: %q", stderr.String())
	}
}

// syncWriter serialises writes and reads of one buffer across goroutines.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.String()
}

type stringer interface{ String() string }

// waitForText waits for the watch to have said something — a condition
// polled briefly, never a blind sleep.
func waitForText(t *testing.T, out stringer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in:\n%s", want, out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
