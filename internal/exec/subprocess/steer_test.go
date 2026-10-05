package subprocess

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shorttest "github.com/pengelbrecht/ticfac/internal/shorttest"
)

// The stuck nudge as a STEER (tick hpk): the Go half of the socket protocol
// the harness's steer server owns (harness/src/local/steer-socket.ts). The
// unit tests here drive a stand-in server — the protocol is pinned from all
// three sides (the harness's server, its own node-suite client, and this
// client), and the supervisor-level tests below drive the real supervisor
// against the same stand-in, so what is tested at each level is the level
// itself, not the other side of the socket.

// steerStandIn is a Unix socket server speaking the pinned protocol: one
// request line in, `reply` (or the protocol's error replies) out, then close.
type steerStandIn struct {
	listener net.Listener
	received chan steerRequest
}

// shortSocketDir is a directory a Unix socket can be BOUND in, whatever the
// test harness's own TMPDIR depth: the kernel bounds a socket path, and the
// per-suite temp this package's TestMain builds sits over that bound.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ticfac-steer-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startSteerStandIn listens on a socket at path and answers every request
// with `ok`. It records what it received, one buffer per request.
func startSteerStandIn(t *testing.T, path string, ok bool) *steerStandIn {
	t.Helper()
	s := &steerStandIn{received: make(chan steerRequest, 8)}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("the stand-in could not listen: %v", err)
	}
	s.listener = listener
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var request steerRequest
				if json.Unmarshal(line, &request) == nil {
					select {
					case s.received <- request:
					default:
					}
				}
				reply, _ := json.Marshal(steerReply{OK: ok, RequestID: request.RequestID})
				if !ok {
					reply, _ = json.Marshal(steerReply{OK: false, RequestID: request.RequestID, Error: "the stand-in refuses"})
				}
				_, _ = conn.Write(append(reply, '\n'))
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return s
}

// A delivered steer is one ack line for one request line, with the text and
// the idempotent request id both intact.
func TestSteerRunnerDeliversOneRequestAndReadsTheAck(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "steer.sock")
	standIn := startSteerStandIn(t, sock, true)

	if err := steerRunner(sock, "You appear stuck: commit and carry on.", "stuck-nudge-1"); err != nil {
		t.Fatalf("the steer was not delivered: %v", err)
	}
	select {
	case request := <-standIn.received:
		if request.Text != "You appear stuck: commit and carry on." {
			t.Errorf("the steered text arrived as %q", request.Text)
		}
		if request.RequestID != "stuck-nudge-1" {
			t.Errorf("the request id arrived as %q", request.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stand-in received no request")
	}
}

// A refused steer is an error the caller falls back from, carrying the
// harness's own reason.
func TestARefusedSteerIsAnErrorNamingWhy(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "steer.sock")
	startSteerStandIn(t, sock, false)
	err := steerRunner(sock, "steer me", "s-1")
	if err == nil || !strings.Contains(err.Error(), "the stand-in refuses") {
		t.Fatalf("refused steer err = %v, want the harness's own reason", err)
	}
}

// A socket nobody listens on is an error, not a hang: the whole exchange is
// bounded, and the watch falls back rather than waits.
func TestAMissingSocketRefusesFast(t *testing.T) {
	start := time.Now()
	if err := steerRunner(filepath.Join(t.TempDir(), "no.sock"), "steer me", "s-1"); err == nil {
		t.Fatal("a steer to a missing socket was delivered")
	}
	if taken := time.Since(start); taken > steerTimeout {
		t.Errorf("the missing socket took %s, over the bound", taken)
	}
}

// A reply that is not the pinned shape is an error: the protocol is a
// contract, and a drifted harness is a fallback now, not a silent nothing.
func TestAGarbageReplyIsAnError(t *testing.T) {
	sock := filepath.Join(shortSocketDir(t), "steer.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_, _ = conn.Write([]byte("this is not json\n"))
	}()
	if err := steerRunner(sock, "steer me", "s-1"); err == nil {
		t.Fatal("a garbage reply was accepted as a delivered steer")
	}
}

// The socket's path is short, stable for one attempt's state directory,
// and different for another — the kernel bounds a Unix socket path, and a
// real run's state directories sit deeper than that bound.
func TestTheSteerSockPathIsStableAndPerAttempt(t *testing.T) {
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))
	state := filepath.Join("x", "state")
	got := steerSockPath(state)
	if got != steerSockPath(state) {
		t.Errorf("steerSockPath is not stable for one attempt: %q", got)
	}
	if steerSockPath(filepath.Join("x", "other")) == got {
		t.Errorf("two attempts share one steer socket: %q", got)
	}
}

// The supervisor's stuck nudge on a DURABLE runner is a steer: the runner
// process LIVES through the nudge, the conversation takes the message, and
// only a second silence earns the stop. The stand-in socket is the harness's
// half; what this test exercises is the supervisor's ladder.
func TestTheStuckNudgeOnADurableRunnerIsASteer(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this one runs a real supervisor, a real runner process and a real socket")
	}
	shorttest.EndToEnd(t)
	dir := t.TempDir()
	fakeHarness(t, dir)

	// The stand-in steer socket listens BEFORE the runner starts, at the
	// path the executor names in the attempt record.
	t.Setenv("TICFAC_HARNESS_DIR", dir)
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))
	f := newFixture(t, fixtureOptions{runner: "pi", noFakeRunner: true, stuckAfter: 1200 * time.Millisecond})
	handle := f.Start(f.spec("run-hpk/tick-stk/attempt-1", "stk"))
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	if local.State == "" {
		t.Fatal("the handle names no state directory")
	}
	if _, err := os.Stat(filepath.Join(local.State, fileWorkerConfig)); err != nil {
		t.Fatalf("the durable runner's worker.json was not written beside the record: %v", err)
	}
	// The record names the socket the supervisor will dial; the stand-in
	// listens on exactly that path, and the dial is what proves the two
	// halves name it the same way.
	var record struct {
		SteerSock string `json:"steer_sock"`
	}
	raw, err := os.ReadFile(filepath.Join(local.State, "attempt.json"))
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode the attempt record: %v", err)
	}
	if record.SteerSock == "" {
		t.Fatal("the durable runner's attempt record names no steer socket")
	}
	standIn := startSteerStandIn(t, record.SteerSock, true)

	f.waitSettled(handle)
	status := f.inspect(handle)
	if status.State != StateFailed || !strings.Contains(lastObservationDetail(status), "stopped as stuck") {
		t.Fatalf("state %s, want failed as stuck after the steer changed nothing:\n%s", status.State,
			formatObservations(status.Observations))
	}
	var steered int
	var interrupts int
	for _, o := range status.Observations {
		switch {
		case IsStuckNudge(o):
			steered++
			if !strings.Contains(o.Detail, "steered in its own conversation") {
				t.Errorf("the stuck nudge does not say it was a steer: %s", o.Detail)
			}
		case o.Kind == ObsStarted && strings.Contains(o.Detail, "re-prompted as stuck"):
			interrupts++
		}
	}
	if steered != 1 || interrupts != 0 {
		t.Fatalf("steers %d, interrupt-restarts %d, want 1 and 0:\n%s", steered, interrupts,
			formatObservations(status.Observations))
	}
	// The steer the supervisor sent is the stuck prompt, after the current
	// tool round — the one message the ladder has.
	select {
	case request := <-standIn.received:
		if !strings.Contains(request.Text, "You appear stuck") {
			t.Errorf("the steered text is not the stuck prompt: %q", request.Text)
		}
		if request.RequestID != "stuck-nudge-1" {
			t.Errorf("the steer's request id is %q, want the ladder's first", request.RequestID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the supervisor never sent the steer")
	}
}

// A steer that cannot be delivered — the socket gone, the harness dead —
// falls back to the CLI runner's interrupt-and-re-prompt, and the ladder
// still ends the attempt rather than waiting on a door that never opens.
func TestAnUndeliverableSteerFallsBackToTheInterruptRePrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this one runs a real supervisor and a real runner process")
	}
	shorttest.EndToEnd(t)
	dir := t.TempDir()
	fakeHarness(t, dir)
	t.Setenv("TICFAC_HARNESS_DIR", dir)
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))
	f := newFixture(t, fixtureOptions{runner: "pi", noFakeRunner: true, stuckAfter: 1200 * time.Millisecond})
	handle := f.Start(f.spec("run-hpk/tick-stk/attempt-2", "stk"))
	// No stand-in socket: nothing listens, so the steer fails and the
	// supervisor must fall back.
	f.waitSettled(handle)
	status := f.inspect(handle)
	if status.State != StateFailed || !strings.Contains(lastObservationDetail(status), "stopped as stuck") {
		t.Fatalf("state %s, want failed as stuck:\n%s", status.State, formatObservations(status.Observations))
	}
	var interrupts int
	var steers int
	for _, o := range status.Observations {
		switch {
		case IsStuckNudge(o):
			if strings.Contains(o.Detail, "steered in its own conversation") {
				steers++
			}
		case o.Kind == ObsStarted && strings.Contains(o.Detail, "re-prompted as stuck"):
			interrupts++
		}
	}
	if steers != 0 || interrupts != 1 {
		t.Fatalf("steers %d, interrupt-restarts %d, want 0 and 1:\n%s", steers, interrupts,
			formatObservations(status.Observations))
	}
}

// THE TICK l6n LADDER: a durable runner stuck in a HUNG TOOL is steered AND
// has that tool interrupted, so the round ends, the steer is read, and the
// worker carries on — recovered, not stopped. Before the fix the steer was
// placed after a round that never ended, and one StuckAfter later the
// attempt was stopped as stuck.
//
// The stand-in harness runs its tool the way pi-durable's NodeExecutionEnv
// does — a shell spawned `detached`, leading its own process group — and
// does what the real harness does when that tool's round ends and a steer is
// waiting: it carries on, here by writing its report and finishing.
func TestAStuckSteerInterruptsAHungToolAndTheWorkerRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: this one runs a real supervisor, a real runner process and a real socket")
	}
	shorttest.EndToEnd(t)
	dir := t.TempDir()
	fakeHarnessWith(t, dir, hungToolHarnessEntry)
	t.Setenv("TICFAC_HARNESS_DIR", dir)
	t.Setenv("TICFAC_STEER_SOCK_DIR", shortSocketDir(t))
	f := newFixture(t, fixtureOptions{runner: "pi", noFakeRunner: true, stuckAfter: 1200 * time.Millisecond})
	handle := f.Start(f.spec("run-l6n/tick-hng/attempt-1", "hng"))
	local, err := handle.Local()
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		SteerSock string `json:"steer_sock"`
	}
	raw, err := os.ReadFile(filepath.Join(local.State, "attempt.json"))
	if err != nil {
		t.Fatalf("read the attempt record: %v", err)
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode the attempt record: %v", err)
	}
	standIn := startSteerStandIn(t, record.SteerSock, true)

	f.waitSettled(handle)
	status := f.inspect(handle)
	if status.State != StateSucceeded {
		if raw, err := os.ReadFile(filepath.Join(local.State, fileRunnerLog)); err == nil {
			t.Logf("the runner's log:\n%s", raw)
		}
		t.Fatalf("state %s, want succeeded: the steer should have reached the worker through its hung tool:\n%s",
			status.State, formatObservations(status.Observations))
	}
	var steered, interrupts, stops int
	for _, o := range status.Observations {
		switch {
		case IsStuckNudge(o):
			steered++
			if !strings.Contains(o.Detail, "steered in its own conversation") ||
				!strings.Contains(o.Detail, "hung tool") {
				t.Errorf("the stuck nudge does not say it steered and interrupted the hung tool: %s", o.Detail)
			}
		case IsStuckStop(o):
			stops++
		case o.Kind == ObsStarted && strings.Contains(o.Detail, "re-prompted as stuck"):
			interrupts++
		}
	}
	if steered != 1 || interrupts != 0 || stops != 0 {
		t.Fatalf("steers %d, interrupt-restarts %d, stuck stops %d; want 1, 0 and 0:\n%s", steered, interrupts, stops,
			formatObservations(status.Observations))
	}
	select {
	case request := <-standIn.received:
		if !strings.Contains(request.Text, "interrupted the command you were running") {
			t.Errorf("the steer does not tell the worker its command was interrupted: %q", request.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the supervisor never sent the steer")
	}
	// The harness itself saw its tool end by the signal pi-durable's own
	// abort sends — a round that ENDED, which is what lets the steer in.
	report, err := os.ReadFile(filepath.Join(local.Worktree, "runs", "run-l6n", "tool-exit.txt"))
	if err != nil || strings.TrimSpace(string(report)) != "SIGKILL" {
		t.Errorf("the harness saw its tool end as %q (%v), want SIGKILL", string(report), err)
	}
}

// hungToolHarnessEntry is a stand-in harness whose one tool hangs: a shell
// spawned detached, as pi-durable spawns every tool. When the tool ends —
// only an interrupt ends it — the round is over and the harness carries on:
// it records how the tool ended, writes its report and finishes.
const hungToolHarnessEntry = `
const { spawn } = process.getBuiltinModule("node:child_process");
const fs = process.getBuiltinModule("node:fs");
const path = process.getBuiltinModule("node:path");
const config = JSON.parse(fs.readFileSync(process.argv[process.argv.indexOf("--config") + 1], "utf8"));
console.log("fake pi-durable harness: running a tool that hangs");
const tool = spawn("sh", ["-c", "sleep 3600; echo never"], { detached: true, stdio: "ignore" });
tool.on("exit", (code, signal) => {
  fs.mkdirSync(path.join(config.worktree, "runs", "run-l6n"), { recursive: true });
  fs.writeFileSync(path.join(config.worktree, "runs", "run-l6n", "tool-exit.txt"), (signal || String(code)) + "\n");
  fs.mkdirSync(path.dirname(config.report), { recursive: true });
  fs.writeFileSync(config.report, "# hng\n\nThe hung tool was interrupted and the worker carried on.\n\nSTATUS: DONE\n");
  console.log("fake pi-durable harness: the tool ended by " + (signal || code) + "; the steer is read and the work goes on");
  process.exit(0);
});
setInterval(() => {}, 60000);
`

// fakeHarness writes the stand-in harness a `pi` runner runs from: the
// register shim the argv imports, and an entry that hangs — the runner this
// ladder steers is one that is alive, quiet and going nowhere.
func fakeHarness(t *testing.T, dir string) {
	t.Helper()
	fakeHarnessWith(t, dir, "console.log('fake pi-durable harness: hanging');\nsetInterval(() => {}, 60000);\n")
}

// fakeHarnessWith writes a stand-in harness whose entry is the given script.
func fakeHarnessWith(t *testing.T, dir, entry string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime", "register.mjs"), []byte("// a stand-in: the entry imports nothing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "local", "main.ts"), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
}
