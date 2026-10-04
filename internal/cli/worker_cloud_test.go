package cli

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The factory-hosted worker (tick y03), against a stand-in factory serving
// the operator's worker routes (cloudflare/src/sandbox-dispatch.ts
// operatorWorkerRoute, tested from its own side in cloudflare/test/
// worker-agent-door.test.ts) over a real HTTP server — and a real
// WebSocket, server side written here, so the client's handshake, framing,
// fragmentation and ping handling are what is tested.

const cloudWorkerRun = "run_5c7c16d1"

// factoryStandIn is the factory's operator routes for one hosted attempt.
type factoryStandIn struct {
	t        *testing.T
	attempt  int
	messages [][]byte // what the watch socket sends, one message each
	hosted   bool

	mu     sync.Mutex
	steers []map[string]any
	pongs  [][]byte
	auth   []string
}

func (f *factoryStandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	prefix := "/api/runs/" + cloudWorkerRun + "/workers/y03/"
	rest, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		http.Error(w, `{"error":"not_found","detail":"no such run in this factory"}`, http.StatusNotFound)
		return
	}
	if !f.hosted {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"not_hosted","detail":"run ` + cloudWorkerRun + `'s workers are not WorkerAgents"}`))
		return
	}
	switch rest {
	case "latest", "4":
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": cloudWorkerRun, "tick_id": "y03", "attempt": f.attempt,
			"state": map[string]any{"phase": "conversing"}})
	case "4/steer":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.steers = append(f.steers, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"submission":7}`))
	case "4/watch":
		f.serveWatch(w, r)
	default:
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}
}

// serveWatch is the server side of RFC 6455, as much as the watch needs: the
// handshake, then each message — the first after a ping, the second split in
// two fragments — then a close.
func (f *factoryStandIn) serveWatch(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, `{"error":"upgrade_required"}`, http.StatusUpgradeRequired)
		return
	}
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		f.t.Errorf("hijack: %v", err)
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + websocketGUID))
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	writeFrame := func(fin bool, opcode byte, payload []byte) {
		head := byte(opcode)
		if fin {
			head |= 0x80
		}
		frame := []byte{head}
		switch n := len(payload); {
		case n < 126:
			frame = append(frame, byte(n))
		case n <= 0xFFFF:
			frame = append(frame, 126, byte(n>>8), byte(n))
		default:
			frame = append(frame, 127)
			frame = binary.BigEndian.AppendUint64(frame, uint64(n))
		}
		_, _ = rw.Write(append(frame, payload...))
		_ = rw.Flush()
	}
	reader := bufio.NewReader(rw)
	writeFrame(true, wsPing, []byte("are you there"))
	// The client's pong, masked as a client's frame must be.
	client := &wsConn{r: reader}
	if fin, opcode, payload, err := client.readFrame(); err != nil || !fin || opcode != wsPong {
		f.t.Errorf("the client answered the ping with fin=%v opcode=%#x err=%v", fin, opcode, err)
	} else {
		f.mu.Lock()
		f.pongs = append(f.pongs, payload)
		f.mu.Unlock()
	}
	for i, message := range f.messages {
		if i == 1 && len(message) > 4 {
			writeFrame(false, wsText, message[:4])
			writeFrame(true, wsContinuation, message[4:])
			continue
		}
		writeFrame(true, wsText, message)
	}
	writeFrame(true, wsClose, []byte{0x03, 0xE8})
	_, _, _, _ = client.readFrame() // the client's close
}

func newFactoryStandIn(t *testing.T) (*factoryStandIn, *httptest.Server) {
	t.Helper()
	f := &factoryStandIn{t: t, attempt: 4, hosted: true}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	configureCloudFactory(t, server.URL)
	return f, server
}

// cloudMessages is the local fixture's stream as the WorkerAgent's socket
// sends it: its state first, the same events frames, then the settled
// state (the cloud socket has no end frame — the agent's phase is its end).
func cloudMessages(t *testing.T) [][]byte {
	t.Helper()
	messages := [][]byte{[]byte(`{"type":"state","state":{"phase":"conversing","exit_code":null,"harness":"pi-durable"}}`)}
	for _, line := range fixtureLines(t) {
		if bytes.Contains(line, []byte(`"type":"end"`)) {
			continue
		}
		messages = append(messages, line)
	}
	messages = append(messages,
		[]byte(`{"type":"log","text":"ticks-worker: pushed\n"}`),
		[]byte(`{"type":"state","state":{"phase":"settled","exit_code":0,"harness":"pi-durable"}}`))
	return messages
}

func TestTheWorkerWatchReadsAFactoryHostedWorkerOverItsWebSocket(t *testing.T) {
	fastWorkerClock(t)
	f, _ := newFactoryStandIn(t)
	f.messages = cloudMessages(t)

	var stdout, stderr bytes.Buffer
	out := &syncWriter{w: &stdout}
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"watch", cloudWorkerRun, "y03", "--state-root", t.TempDir(), "--repo", t.TempDir()}, out, &stderr)
	}()
	select {
	case code := <-done:
		if code != exitSuccess {
			t.Fatalf("the watch of a settled hosted attempt exited %d:\n%s%s", code, out.String(), stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the watch did not end on the agent's settled state:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{
		"∴ thinking  The tick wants a step file; a bash round writes it.",
		"» steer     Write the report next.",
		"✎ answer    watched and steered",
		"■ y03#4 · " + cloudWorkerRun + " settled — its conversation is over",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the cloud watch did not say %q:\n%s", want, text)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pongs) != 1 || string(f.pongs[0]) != "are you there" {
		t.Errorf("the ping was not answered with its payload: %q", f.pongs)
	}
	for _, auth := range f.auth {
		if auth != "Bearer tkf_test-token" {
			t.Errorf("a request went without the operator's token: %q", auth)
		}
	}
}

func TestSteerReachesAFactoryHostedWorker(t *testing.T) {
	f, _ := newFactoryStandIn(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"steer", cloudWorkerRun, "y03", "write", "the", "report", "--json",
		"--request-id", "op-2", "--state-root", t.TempDir(), "--repo", t.TempDir()}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("steer exited %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	var doc steerDoc
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("not one document: %v\n%s", err, stdout.String())
	}
	if doc.Host != "cloud" || doc.Attempt != 4 || doc.RunID != cloudWorkerRun || doc.State != agentStateDone {
		t.Errorf("the document: %+v", doc)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.steers) != 1 || f.steers[0]["text"] != "write the report" || f.steers[0]["request_id"] != "op-2" {
		t.Errorf("the factory received %v", f.steers)
	}
}

func TestAFactoryRunWhoseWorkersAreNotHostedIsSaidSo(t *testing.T) {
	f, _ := newFactoryStandIn(t)
	f.hosted = false
	var stdout, stderr bytes.Buffer
	code := Run([]string{"watch", cloudWorkerRun, "y03", "--state-root", t.TempDir(), "--repo", t.TempDir()}, &stdout, &stderr)
	if code != exitGeneric {
		t.Fatalf("exit %d, want %d", code, exitGeneric)
	}
	if !strings.Contains(stderr.String(), "not WorkerAgents") {
		t.Errorf("the refusal's reason was not said: %q", stderr.String())
	}
}

func TestTheWebSocketClientWritesMaskedFrames(t *testing.T) {
	var wire bytes.Buffer
	c := &wsConn{rwc: nopReadWriteCloser{&wire}}
	payload := bytes.Repeat([]byte("x"), 300)
	if err := c.writeFrame(wsText, payload); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte{}, wire.Bytes()...)
	if raw[1]&0x80 == 0 || raw[1]&0x7F != 126 {
		t.Errorf("a client frame must be masked and carry its 16-bit length: %08b", raw[1])
	}
	if bytes.Contains(raw, payload[:16]) {
		t.Errorf("the payload went out unmasked")
	}
	server := &wsConn{r: bufio.NewReader(&wire)}
	fin, opcode, got, err := server.readFrame()
	if err != nil || !fin || opcode != wsText || !bytes.Equal(got, payload) {
		t.Fatalf("round trip: fin=%v opcode=%#x len=%d err=%v", fin, opcode, len(got), err)
	}
}

type nopReadWriteCloser struct{ *bytes.Buffer }

func (nopReadWriteCloser) Close() error { return nil }

var _ io.ReadWriteCloser = nopReadWriteCloser{}
