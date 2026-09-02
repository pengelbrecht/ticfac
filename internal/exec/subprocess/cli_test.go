package subprocess

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Unix command surface, exercised as a shell would: the built binary, one
// record in on stdin, one record out on stdout.
//
// It is run out of process on purpose. `start` re-invokes the executable it is
// running as to supervise the attempt, so an in-process test would be testing
// a supervisor that does not exist — and the binary being the supervisor is
// half of what makes `start` return immediately without abandoning the job.

type cliRun struct {
	stdout string
	stderr string
	code   int
}

func runCLI(t *testing.T, f *fixture, stdin string, args ...string) cliRun {
	t.Helper()
	full := append([]string{args[0], "--repo", f.Repo.Dir, "--state-dir", f.StateDir}, args[1:]...)
	cmd := exec.Command(executorBin, full...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), EnvRunnerArgv+"="+strings.Join(f.Executor.opts.RunnerArgv, "\n"))
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("%s %v: %v", executorBin, full, err)
		}
	}
	return cliRun{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// The four operations round-trip JSON on stdin and stdout, and each output is
// the record the contract says that operation returns.
func TestTheFourOperationsRoundTripJSONOnStdinAndStdout(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	spec := f.spec("run-cli/tick-c1/attempt-1", "c1")
	specJSON := mustMarshal(t, spec)

	started := runCLI(t, f, specJSON, "start")
	if started.code != ExitOK {
		t.Fatalf("start exited %d: %s", started.code, started.stderr)
	}
	handle, err := ParseJobHandle([]byte(started.stdout))
	if err != nil {
		t.Fatalf("start's stdout is not a JobHandle: %v\n%s", err, started.stdout)
	}
	if handle.JobID != spec.JobID || handle.Executor != ExecutorName {
		t.Errorf("handle %+v", handle)
	}
	f.handles = append(f.handles, handle)
	handleJSON := started.stdout

	inspected := runCLI(t, f, handleJSON, "inspect")
	if inspected.code != ExitOK {
		t.Fatalf("inspect exited %d: %s", inspected.code, inspected.stderr)
	}
	var status JobStatus
	if err := strictUnmarshal([]byte(inspected.stdout), &status); err != nil {
		t.Fatalf("inspect's stdout is not a JobStatus: %v\n%s", err, inspected.stdout)
	}
	if status.JobID != spec.JobID {
		t.Errorf("inspect answered about %s", status.JobID)
	}
	// The cursor comes back out and goes back in.
	if status.Cursor != nil {
		resumed := runCLI(t, f, handleJSON, "inspect", "--cursor", *status.Cursor)
		if resumed.code != ExitOK {
			t.Fatalf("inspect --cursor exited %d: %s", resumed.code, resumed.stderr)
		}
	}

	f.waitSettled(handle)

	collected := runCLI(t, f, handleJSON, "collect")
	if collected.code != ExitOK {
		t.Fatalf("collect exited %d: %s", collected.code, collected.stderr)
	}
	var result JobResult
	if err := strictUnmarshal([]byte(collected.stdout), &result); err != nil {
		t.Fatalf("collect's stdout is not a JobResult: %v\n%s", err, collected.stdout)
	}
	if result.Outcome != OutcomeSucceeded {
		t.Errorf("collect says %s: %s", result.Outcome, collected.stderr)
	}

	cancelled := runCLI(t, f, handleJSON, "cancel")
	if cancelled.code != ExitOK {
		t.Fatalf("cancel exited %d: %s", cancelled.code, cancelled.stderr)
	}
	var ack CancelAck
	if err := strictUnmarshal([]byte(cancelled.stdout), &ack); err != nil {
		t.Fatalf("cancel's stdout is not a CancelAck: %v\n%s", err, cancelled.stdout)
	}
	if !ack.CredentialsRevoked || ack.Order != OrderRevokeThenStop {
		t.Errorf("acknowledgement %+v", ack)
	}

	disposed := runCLI(t, f, handleJSON, "dispose")
	if disposed.code != ExitOK {
		t.Fatalf("dispose exited %d: %s", disposed.code, disposed.stderr)
	}
	if branches := runGit(t, f.Repo.Dir, "branch", "--list", "tick/*"); branches != "" {
		t.Errorf("dispose left %q", branches)
	}
}

// stdout carries the record and NOTHING else. A verdict's sentence, a boundary
// report and every diagnostic go to stderr, so `| jq` works on the output of
// every operation whatever happened to the job.
func TestStdoutCarriesTheRecordAndNothingElse(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "boundary"})
	spec := f.spec("run-cli/tick-c2/attempt-1", "c2")
	started := runCLI(t, f, mustMarshal(t, spec), "start")
	handle, err := ParseJobHandle([]byte(started.stdout))
	if err != nil {
		t.Fatal(err)
	}
	f.handles = append(f.handles, handle)
	f.waitSettled(handle)

	collected := runCLI(t, f, started.stdout, "collect")
	var record map[string]any
	if err := json.Unmarshal([]byte(collected.stdout), &record); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, collected.stdout)
	}
	if !strings.Contains(collected.stderr, "boundary violation") {
		t.Errorf("the boundary attempt was not reported on stderr:\n%s", collected.stderr)
	}
	if strings.Contains(collected.stdout, "boundary violation:") {
		t.Error("a diagnostic line was written to stdout beside the record")
	}
}

// A refusal has its own exit code: "this executor will not do that, and here
// is which rule" is a different answer from "something broke".
func TestARefusalHasItsOwnExitCode(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})
	if stop := runCLI(t, f, "", "stop", "--reason", "called off"); stop.code != ExitOK {
		t.Fatalf("stop exited %d: %s", stop.code, stop.stderr)
	}
	started := runCLI(t, f, mustMarshal(t, f.spec("run-cli/tick-c3/attempt-1", "c3")), "start")
	if started.code != ExitRefused {
		t.Fatalf("start under a stop exited %d, want %d\n%s", started.code, ExitRefused, started.stderr)
	}
	if !strings.Contains(started.stderr, RefusedStopped) {
		t.Errorf("the refusal does not name its reason: %s", started.stderr)
	}
	if strings.TrimSpace(started.stdout) != "" {
		t.Errorf("a refused start wrote a record to stdout: %s", started.stdout)
	}
}

// A malformed record is a usage error, and a record the contract refuses is
// refused here too rather than half-run.
func TestBadInputIsRefusedBeforeAnythingIsStarted(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "report"})

	if empty := runCLI(t, f, "", "start"); empty.code != ExitUsage {
		t.Errorf("start with no stdin exited %d, want %d", empty.code, ExitUsage)
	}
	if unknown := runCLI(t, f, "{}", "explode"); unknown.code != ExitUsage {
		t.Errorf("an unknown operation exited %d, want %d", unknown.code, ExitUsage)
	}

	// The negative the contract carries: a JobSpec naming a concrete backend.
	spec := map[string]any{}
	if err := json.Unmarshal([]byte(mustMarshal(t, f.spec("run-cli/tick-c4/attempt-1", "c4"))), &spec); err != nil {
		t.Fatal(err)
	}
	spec["backend"] = "container-shell"
	raw, _ := json.Marshal(spec)
	if bad := runCLI(t, f, string(raw), "start"); bad.code == ExitOK {
		t.Error("a JobSpec naming a concrete backend was accepted")
	}
	if worktreeCount(t, f.Repo.Dir) != 1 {
		t.Error("a refused start left a worktree behind")
	}
}

// The handle a shell wrote to a file is enough to address the job later —
// which is what "re-addressable" means, and the reason `start` prints one.
func TestAHandleReadFromAFileStillAddressesTheJob(t *testing.T) {
	f := newFixture(t, fixtureOptions{mode: "slow_report", sleep: "1"})
	started := runCLI(t, f, mustMarshal(t, f.spec("run-cli/tick-c5/attempt-1", "c5")), "start")
	path := filepath.Join(t.TempDir(), "handle.json")
	if err := os.WriteFile(path, []byte(started.stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	handle := readHandleFile(t, path)
	f.handles = append(f.handles, handle)

	waitFor(t, "the attempt to settle", 30*time.Second, func() bool { return f.store(handle).settled() })
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	collected := runCLI(t, f, string(raw), "collect")
	if collected.code != ExitOK {
		t.Fatalf("collect from a handle on disk exited %d: %s", collected.code, collected.stderr)
	}
}

func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + "\n"
}
