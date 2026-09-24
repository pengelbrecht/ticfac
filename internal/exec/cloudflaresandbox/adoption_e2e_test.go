package cloudflaresandbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	shorttest "github.com/pengelbrecht/ticfac/internal/shorttest"
)

// Sandbox adoption, proved end to end through the REAL door — not a fixture
// (tick 6gr, absorbing finding dccd84cf): the worker from cloudflare/src,
// the deployed fetch handler's own routing, the gateway's own authorization
// over a real minted run credential, the real per-project dispatch lease in a
// real RunRoom, and the door's own adoption machinery — all of it running in
// real workerd through miniflare, on a real local HTTP port, with the real
// Go executor on the far side.
//
// The previous suite proved every one of these layers against a fixture
// (door_test.go's httptest fake) — and avx's finding stands: a fixture
// certifies the client's reading of the contract, never the contract. What
// only the real thing can prove, and what this test asserts:
//
//   - a dispatch lands: the real door boots (a fake) container through its
//     own green-start probe and confirmed-dispatch wait, and mints a handle
//     the real client parses;
//   - a RESTARTED orchestrator — a second incarnation holding NOTHING the
//     first held, not even the first's state directory, the shape a rebooted
//     container is — re-derives the attempt, asks the door by identity, and
//     ADOPTS the container still running the tick: same job id, same live
//     process, `adopted` true, and no rival container booted beside it;
//   - a door that cannot be REACHED is an error, never a `lost`: the
//     restarted incarnation's questions fail loudly while its container is
//     still running — the exact failure avx's rule ("unreachable is not
//     absent") exists to keep from writing an attempt off.
//
// The one substitution is the container itself, through the SANDBOXES seam —
// the same substitution the worker's own suite makes, by the same design (a
// Cloudflare Sandbox needs a Cloudflare account; the seam discriminates a
// test binding on purpose). Everything else on the worker side is the
// deployed code.
//
// End-to-end by the shorttest discipline: it builds a real subprocess and a
// real workerd runtime, so it runs in `make test`, never in the per-tick
// gate. It skips — naming its remedy — where the door's runtime is not
// installed: `node`, and `cloudflare/node_modules` (pnpm install in
// cloudflare/). A skip there is the honest answer on a host that cannot run
// the door at all; CI runs the full suite on a host that can.
func TestARestartedOrchestratorAdoptsTheRunningSandboxThroughTheRealDoor(t *testing.T) {
	shorttest.EndToEnd(t)
	door := newRealDoor(t)
	const tickID = "6gr"
	spec := door.newSpec(tickID)

	// ------------------------------------------------------ the dispatch ---
	// The first incarnation: the orchestrator that dispatched the attempt.
	first := door.newExecutor(t.TempDir())
	handle1, err := first.Start(spec)
	if err != nil {
		t.Fatalf("the first incarnation's Start: %v", err)
	}
	payload1, err := local(handle1)
	if err != nil {
		t.Fatalf("decode the first handle's payload: %v", err)
	}
	if payload1.ProcessID == nil || *payload1.ProcessID == "" {
		t.Fatal("the first handle names no process: the dispatch was confirmed to one")
	}
	firstRecord, err := newStore(first.stateDirFor(spec.JobID, first.opts.Attempt)).readAttempt()
	if err != nil {
		t.Fatalf("read the first incarnation's attempt record: %v", err)
	}
	if firstRecord.Adopted {
		t.Error("the first dispatch was recorded as adopted: nothing was running under this identity")
	}
	if firstRecord.Model != door.model {
		t.Errorf("the record booted on model %q, want %q", firstRecord.Model, door.model)
	}
	if firstRecord.Harness != door.harness {
		t.Errorf("the record booted on harness %q, want %q", firstRecord.Harness, door.harness)
	}
	if firstRecord.Prompt != door.prompt {
		t.Errorf("the record does not state the prompt the dispatch delivered (%d bytes, want %d)",
			len(firstRecord.Prompt), len(door.prompt))
	}

	// Inspect by identity, the way a live orchestrator watches its work.
	status1, err := first.Inspect(handle1, "")
	if err != nil {
		t.Fatalf("the first incarnation's Inspect: %v", err)
	}
	if status1.State != subprocess.StateRunning || status1.Terminal {
		t.Fatalf("the dispatched attempt reads %s (terminal %v), want running and not terminal",
			status1.State, status1.Terminal)
	}
	if status1.JobID != spec.JobID {
		t.Errorf("the status answers for %q, want %q", status1.JobID, spec.JobID)
	}
	door.assertOneLiveWorkProcess(t, "after the first dispatch")

	// ------------------------------------------------------ the restart ---
	// The second incarnation holds NOTHING the first held — not even its
	// state directory, which is the honest shape of a rebooted orchestrator
	// container: the previous process is gone, its container-local state went
	// with it, and the durable evidence is the run's own. It re-derives the
	// attempt and asks the door by identity, and the door must ADOPT the
	// container still running the tick rather than boot a rival beside it.
	second := door.newExecutor(t.TempDir())
	handle2, err := second.Start(spec)
	if err != nil {
		t.Fatalf("the restarted incarnation's Start: %v", err)
	}
	if handle2.JobID != handle1.JobID {
		t.Errorf("the second handle's job id is %q, want %q: the credential names the run and the identity is the same",
			handle2.JobID, handle1.JobID)
	}
	if handle2.Attempt != handle1.Attempt {
		t.Errorf("the second handle's attempt is %d, want %d", handle2.Attempt, handle1.Attempt)
	}
	payload2, err := local(handle2)
	if err != nil {
		t.Fatalf("decode the second handle's payload: %v", err)
	}
	if payload2.ProcessID == nil || payload1.ProcessID == nil || *payload2.ProcessID != *payload1.ProcessID {
		t.Errorf("the second handle addresses process %v, want %v: an adoption hands back the SAME live work process",
			payload2.ProcessID, payload1.ProcessID)
	}
	secondRecord, err := newStore(second.stateDirFor(spec.JobID, second.opts.Attempt)).readAttempt()
	if err != nil {
		t.Fatalf("read the restarted incarnation's attempt record: %v", err)
	}
	if !secondRecord.Adopted {
		t.Error("the restarted incarnation's dispatch was not recorded as adopted: the door should have found the tick's sandbox still running")
	}
	if !strings.Contains(secondRecord.Detail, "adopted") {
		t.Errorf("the adopted record's detail is %q, want it to say the container was adopted", secondRecord.Detail)
	}

	// The restarted incarnation can still watch the work it adopted.
	status2, err := second.Inspect(handle2, "")
	if err != nil {
		t.Fatalf("the restarted incarnation's Inspect: %v", err)
	}
	if status2.State != subprocess.StateRunning || status2.Terminal {
		t.Fatalf("the adopted attempt reads %s (terminal %v), want running and not terminal",
			status2.State, status2.Terminal)
	}
	// And the door booted no rival: still exactly one container, exactly one
	// live work process in it — the assertion the door's own answers cannot
	// express, which is what the harness's observation route exists for.
	door.assertOneLiveWorkProcess(t, "after the restart")

	// ------------------------------------------------ unreachable ≠ absent ---
	// The door goes away — the exact outage in which an attempt is at most
	// risk of being written off. Every question after it must fail LOUDLY: a
	// transport error naming the unreachable door, never a `lost` the caller
	// could mistake for "no sandbox", never a settled refusal, never a
	// half-held attempt record.
	door.shutdown(t)

	_, err = second.Inspect(handle2, "")
	if err == nil {
		t.Fatal("an Inspect against an unreachable door succeeded: something answered after shutdown")
	}
	assertUnreachable(t, err, "the restarted incarnation's Inspect")

	// And the redispatch question — the one a reconciler asks when it cannot
	// reach the door at all — is the same loud failure, from an incarnation
	// with no record of its own.
	third := door.newExecutor(t.TempDir())
	_, err = third.Start(spec)
	if err == nil {
		t.Fatal("a Start against an unreachable door succeeded: something answered after shutdown")
	}
	assertUnreachable(t, err, "a fresh incarnation's Start")
	// Nothing was half-held: a start that never reached the door records
	// NOTHING (the executor's own rule), so this attempt is free to retry the
	// moment the door is back rather than held for a person.
	if entries, readErr := os.ReadDir(third.root); readErr != nil || len(entries) != 0 {
		t.Errorf("the unreachable-door start left %d entries in its state root (err %v): a start that never "+
			"reached the door records nothing, so the retry is free", len(entries), readErr)
	}
}

// assertUnreachable states what avx's rule means at the call site: the error
// is the client's transport failure naming the door it cannot reach — never
// the door's own refusal (the door never answered), never anything a caller
// could read as a verdict on the attempt.
func assertUnreachable(t *testing.T, err error, where string) {
	t.Helper()
	if !strings.Contains(err.Error(), "could not be reached") {
		t.Errorf("%s against a shut door answered %q, want the transport error naming the unreachable door", where, err)
	}
	var refusal *doorError
	if errors.As(err, &refusal) {
		t.Errorf("%s against a shut door answered the door's own refusal (%d %s): the door never answered, and "+
			"a refusal here would be a verdict the outage invented", where, refusal.Status, refusal.Class)
	}
	var refused *subprocess.Refusal
	if errors.As(err, &refused) {
		t.Errorf("%s against a shut door answered a typed refusal (%s): an outage is not a verdict on the attempt",
			where, refused.Reason)
	}
}

// ---------------------------------------------------------------- harness ---

// realDoorReadyLine is the harness's own marker on stdout: the Go test reads
// lines until it sees it. Everything else the harness or the runtime prints
// goes to the harness's log buffer, surfaced only on failure.
const realDoorReadyLine = `{"ready":true`

// realDoorStartup bounds how long the test waits for the ready line — a
// condition wait on the harness's own stdout, not a sleep: esbuild bundling,
// workerd booting, sixteen migrations and one lease take a few seconds warm
// and this leaves an order of magnitude for a cold one.
const realDoorStartup = 3 * time.Minute

// realDoorHarnessPath is the door harness under cloudflare/, run by node.
var realDoorHarnessPath = filepath.Join("test", "go-door-harness", "run.mjs")

// realDoorWorkCommand is WORKER_COMMAND (cloudflare/src/worker-boot.ts): the
// command a worker container's WORK process runs. The door's own
// isWorkProcess matches it exactly, so the observation assertion does too.
const realDoorWorkCommand = "/usr/local/bin/ticks-worker"

// realDoor is the real factory door, in real workerd, on a real local port:
// one node process (cloudflare/test/go-door-harness/run.mjs) holding the
// Worker, its D1 index, its lease and its run credential for us.
type realDoor struct {
	t *testing.T

	url     string
	token   string
	runID   string
	epic    string
	baseSHA string
	project string
	model   string
	// The harness and the rendered role prompt the dispatch carries (tick 9iz):
	// what the executor sends over the door, and what the fake container's
	// work process must be booted on.
	harness string
	prompt  string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	logMu  sync.Mutex
	log    bytes.Buffer
}

// newRealDoor starts the harness and waits — on its ready line, never on a
// sleep — until the door is serving with the run seeded and the lease held.
// It skips, naming the remedy, on a host that cannot run the door at all.
func newRealDoor(t *testing.T) *realDoor {
	t.Helper()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("the real-door harness runs on node: %v", err)
	}
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	harnessDir := filepath.Join(root, "cloudflare")
	// The harness needs the workspace's own dependencies (miniflare,
	// esbuild, wrangler's SQL splitter, the pinned worker sources): they are
	// installed, not committed, so a checkout without them cannot run the
	// real door and says so rather than failing on a missing module.
	for _, marker := range []string{
		filepath.Join(harnessDir, "node_modules", "miniflare"),
		filepath.Join(harnessDir, "node_modules", "esbuild"),
	} {
		if _, err := os.Stat(marker); err != nil {
			t.Skipf("the real door needs %s: run pnpm install in cloudflare/ to run this end-to-end test", marker)
		}
	}

	door := &realDoor{t: t}
	door.cmd = exec.Command(node, realDoorHarnessPath)
	door.cmd.Dir = harnessDir
	stdout, err := door.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	door.stdout = bufio.NewReader(stdout)
	stderr, err := door.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	door.stdin, err = door.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := door.cmd.Start(); err != nil {
		t.Fatalf("start the door harness: %v", err)
	}
	t.Cleanup(func() { door.stop() })

	// Everything the harness prints beyond the ready line is diagnostic,
	// kept for the failure that might need it.
	go func() {
		_, _ = io.Copy(door.logWriter(), stderr)
	}()

	// The readiness wait: poll the harness's own stdout, with a watchdog
	// that kills the process if the door never announces itself.
	watchdog := time.AfterFunc(realDoorStartup, door.stop)
	defer watchdog.Stop()
	for {
		line, err := door.stdout.ReadString('\n')
		if err != nil {
			door.t.Fatalf("the door harness exited before announcing readiness: %s", door.harnessLog())
		}
		if strings.HasPrefix(strings.TrimSpace(line), realDoorReadyLine) {
			var ready struct {
				Ready   bool   `json:"ready"`
				URL     string `json:"url"`
				Observe string `json:"observe"`
				RunID   string `json:"run_id"`
				Project string `json:"project"`
				Epic    string `json:"epic"`
				BaseSHA string `json:"base_sha"`
				Token   string `json:"token"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &ready); err != nil {
				door.t.Fatalf("the door harness's ready line is not its documented JSON shape (%v): %s", err, strings.TrimSpace(line))
			}
			door.url = strings.TrimSuffix(ready.URL, "/")
			door.token = ready.Token
			door.runID = ready.RunID
			door.project = ready.Project
			door.epic = ready.Epic
			door.baseSHA = ready.BaseSHA
			door.model = "cloudflare-workers-ai/@cf/zai-org/glm-5.3"
			door.harness = "pi"
			door.prompt = "# implement-tick\n\nYou are implementing ONE unit of work from the ticks tracker, headless, in\n" +
				"an isolated git worktree that is yours alone. Nobody will answer a question.\n"
			return door
		}
		door.note(line)
	}
}

// newExecutor is one ORCHESTRATOR INCARNATION over this door: the real
// executor, pointed at the real factory URL with the run's own minted
// credential, with its private state rooted where the caller says — a fresh
// temp directory per incarnation, because a rebooted orchestrator container
// carries none of its predecessor's local state.
func (d *realDoor) newExecutor(stateDir string) *Executor {
	d.t.Helper()
	ex, err := New(Options{
		FactoryURL: d.url,
		Token:      d.token,
		EpicID:     d.epic,
		BaseRef:    "refs/heads/epic/" + d.epic,
		Title:      "Prove sandbox adoption end to end through the real Go executor and the real door",
		Model:      d.model,
		Harness:    d.harness,
		Prompt:     d.prompt,
		Attempt:    1,
		StateDir:   stateDir,
	})
	if err != nil {
		d.t.Fatalf("New (the orchestrator incarnation): %v", err)
	}
	return ex
}

// newSpec is the JobSpec the reconciler would build for this attempt: the
// job id in the door's own run/tick/attempt shape, the tick input, a write
// ref inside the grant's namespace — the same shape door_test.go's harness
// builds, over the real run the harness seeded.
func (d *realDoor) newSpec(tickID string) *subprocess.JobSpec {
	d.t.Helper()
	jobID := fmt.Sprintf("run-%s/tick-%s/attempt-%d", d.runID, tickID, 1)
	return &subprocess.JobSpec{
		SchemaVersion: subprocess.SchemaVersion,
		JobID:         jobID,
		Role:          "implement-tick",
		Source: subprocess.Source{
			Repository: "https://github.com/" + d.project,
			BaseSHA:    d.baseSHA,
			WriteRef:   "refs/heads/ticfac/" + jobID,
		},
		Capabilities:   subprocess.Capabilities{Persistence: "durable", Isolation: "process", Network: "restricted"},
		Inputs:         []subprocess.Input{{Kind: "tick", ID: tickID}, {Kind: "epic", ID: d.epic}},
		OutputSchema:   "ticfac.job-result.implement-tick.v1",
		ArtifactPrefix: "runs/" + jobID + "/",
		Credentials: subprocess.Credentials{
			Model: subprocess.ModelCredential{Shorthand: "issued-by-host"},
			Source: subprocess.SourceCredential{Grant: &subprocess.SourceGrant{
				Issuer: "host", Grade: "write", WriteRefPrefix: "refs/heads/ticfac/",
			}},
		},
		Limits: subprocess.Limits{WallSeconds: 300},
	}
}

// observe reads the harness's one observation route: what containers its
// fake binding was asked to boot, what processes they hold, and the boot
// environment the live work process was started with (tick 9iz). The door's
// own answers cannot express "no rival was booted" — this can.
func (d *realDoor) observe() (map[string]int, map[string]string, error) {
	response, err := http.Get(d.url + "/__door_harness/sandboxes")
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	var observation struct {
		Sandboxes []struct {
			Name      string `json:"name"`
			Processes []struct {
				ID       string            `json:"id"`
				Command  string            `json:"command"`
				State    string            `json:"state"`
				ExitCode *int              `json:"exit_code"`
				Env      map[string]string `json:"env"`
			} `json:"processes"`
		} `json:"sandboxes"`
	}
	if err := json.NewDecoder(response.Body).Decode(&observation); err != nil {
		return nil, nil, err
	}
	counts := map[string]int{}
	var workEnv map[string]string
	for _, sandbox := range observation.Sandboxes {
		counts["containers"]++
		for _, process := range sandbox.Processes {
			if process.Command == realDoorWorkCommand && process.State == "running" {
				counts["live work processes"]++
				workEnv = process.Env
			}
			if process.Command == realDoorWorkCommand && process.State != "running" {
				counts["dead work processes"]++
			}
		}
	}
	return counts, workEnv, nil
}

// assertOneLiveWorkProcess is the no-rival assertion: exactly one container
// was ever addressed, holding exactly one live work process — after the
// dispatch and again after the restart, the two moments at which a rival
// could have been booted beside the tick's sandbox.
func (d *realDoor) assertOneLiveWorkProcess(t *testing.T, when string) {
	t.Helper()
	counts, workEnv, err := d.observe()
	if err != nil {
		t.Fatalf("%s: read the harness's observation route: %v", when, err)
	}
	if counts["containers"] != 1 {
		t.Errorf("%s: the door addressed %d containers, want 1: a rival container was booted beside the tick's sandbox", when, counts["containers"])
	}
	if counts["live work processes"] != 1 {
		t.Errorf("%s: the containers hold %d live work processes, want 1: a rival work process was started beside the tick's", when, counts["live work processes"])
	}
	if counts["dead work processes"] != 0 {
		t.Errorf("%s: %d work processes ended unexpectedly", when, counts["dead work processes"])
	}
	// The acceptance clause, read against the REAL door (tick 9iz): the
	// worker boots on exactly the harness and the rendered prompt the
	// dispatch carried — both proven in the container's own environment,
	// not inferred from the door having accepted the request.
	if got := workEnv["TICKS_HARNESS"]; got != d.harness {
		t.Errorf("%s: the work process is bound to harness %q, want the dispatch's %q", when, got, d.harness)
	}
	if got := workEnv["TICKS_ROLE_PROMPT"]; got != d.prompt {
		t.Errorf("%s: the work process was given a %d-byte role prompt, want the dispatch's %d bytes", when, len(got), len(d.prompt))
	}
}

// shutdown asks the harness to stop listening and waits — on the process's
// own exit, never on a sleep — until the door is gone, so the unreachable
// leg proves exactly what it claims: a door that is not there.
func (d *realDoor) shutdown(t *testing.T) {
	t.Helper()
	if d.cmd.Process == nil {
		return
	}
	if d.stdin != nil {
		_, _ = d.stdin.Write([]byte("shutdown\n"))
		_ = d.stdin.Close()
		d.stdin = nil
	}
	waited := make(chan error, 1)
	go func() { waited <- d.cmd.Wait() }()
	select {
	case <-waited:
		return
	case <-time.After(30 * time.Second):
		d.stop()
		t.Fatal("the door harness did not exit within 30s of its shutdown")
	}
}

// stop is the cleanup path: kill the harness if it is still running, so a
// failing test never leaks a workerd on the test's port.
func (d *realDoor) stop() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Kill()
	_, _ = d.cmd.Process.Wait()
}

// logWriter is a synchronized writer over the harness's diagnostic log.
func (d *realDoor) logWriter() io.Writer {
	return &realDoorLog{door: d}
}

// note records a line the harness printed that was not the ready line.
func (d *realDoor) note(line string) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	d.log.WriteString(line)
}

// harnessLog is the diagnostics a failure should surface.
func (d *realDoor) harnessLog() string {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	return d.log.String()
}

// realDoorLog serializes writes into the door's diagnostic buffer.
type realDoorLog struct {
	door *realDoor
}

func (w *realDoorLog) Write(p []byte) (int, error) {
	w.door.logMu.Lock()
	defer w.door.logMu.Unlock()
	return w.door.log.Write(p)
}
