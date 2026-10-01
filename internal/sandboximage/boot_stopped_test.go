//go:build !windows

package sandboximage

import (
	"fmt"
	"strings"
	"testing"
)

// A worker that dies in its boot — before its harness starts — leaves the
// reason on origin (hn6 run_ee8e).
//
// 378's resolve job booted, checked out, made its worker branch, and then
// its one-token gateway probe got nothing back. The container exited 7 with
// its reason on stdout and pushed NOTHING, so the run's collect could only say
// "the container's branch carries no report and no commit beyond the base it
// was cut from: the push never landed". The log was the only place the reason
// lived, and a log is not the durable layer.
//
// Now a boot that stops leaves a marker beside its worker branch — never ON
// it: an empty worker branch is what the collect's verdicts (and the
// infrastructure class) are keyed on, and a marker that rode the worker branch
// would read as a report-only answer.

// short: one stub worker boot, local git; no container, no network.
func TestAWorkerThatStopsInItsBootLeavesTheReasonOnOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		code   int
		reason string
	}{
		{"the gateway refused the route", map[string]string{"TICKS_TEST_CURL_STATUS": "503"},
			ExitModel, "could not answer a one-token request"},
		{"the gateway never answered", map[string]string{"TICKS_TEST_CURL_STATUS": "000",
			EnvModelProbeBackoff: "0", EnvModelProbeTimeout: "1"},
			ExitGatewayUnavailable, "did not answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkerFixture(t)
			for k, v := range tc.env {
				f.env[k] = v
			}
			out, code := f.run()
			if code != tc.code {
				t.Fatalf("exit %d, want %d (the marker must not change the boot's exit)\n%s", code, tc.code, out)
			}
			if f.harnessRecord() != "" {
				t.Fatal("the harness ran on a boot that stopped")
			}
			worker := WorkerBranch(f.epic, f.tick)
			if ok, _ := f.remoteBranch(worker); ok {
				t.Errorf("the worker branch %s reached origin: the collect would read a boot failure as an answer", worker)
			}
			marker, ok := f.remoteFile(WorkerBootStoppedBranch(worker), WorkerBootStoppedFile(f.tick))
			if !ok {
				t.Fatalf("origin carries no %s on %s: the reason lives only in the container's log\n%s",
					WorkerBootStoppedFile(f.tick), WorkerBootStoppedBranch(worker), out)
			}
			mustContain(t, marker, fmt.Sprintf("exit: %d", tc.code), "the marker carries the exit code")
			mustContain(t, marker, "reason: ", "the marker carries the reason")
			mustContain(t, marker, tc.reason, "the reason is the boot's own")
			if strings.Contains(marker, "STATUS:") {
				t.Errorf("the marker carries a STATUS line, which a report reader would take as an answer:\n%s", marker)
			}
			if ok, n := f.remoteBranch(WorkerBootStoppedBranch(worker)); !ok || n != 1 {
				t.Errorf("the marker branch is %d commit(s) beyond the base, want 1", n)
			}
		})
	}
}

// A boot that reached its harness leaves no marker: the worker branch and its
// report are the account, as they always were.
//
// short: one stub worker run, local git; no container, no network.
func TestAWorkerThatReachedItsHarnessLeavesNoBootMarker(t *testing.T) {
	f := newWorkerFixture(t)
	out, code := f.run()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	worker := WorkerBranch(f.epic, f.tick)
	if ok, _ := f.remoteBranch(WorkerBootStoppedBranch(worker)); ok {
		t.Errorf("a worker that ran its harness left a boot marker")
	}
	f2 := newWorkerFixture(t)
	f2.env["TICKS_TEST_WORKER_EXIT"] = "1"
	f2.env["TICKS_TEST_WORKER_COMMIT"] = ""
	_, _ = f2.run()
	if ok, _ := f2.remoteBranch(WorkerBootStoppedBranch(worker)); ok {
		t.Errorf("a harness that FAILED left a boot marker: its exit is the agent's, not the boot's")
	}
}
