package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// stubSupervisorOnPath puts a ticfac-exec-subprocess of the test's own at the
// head of PATH, for a test that needs the supervisor to be FINDABLE but never
// starts it: reconcile.CheckExecutor and the local executor's construction
// only resolve the binary (beside this executable, then PATH), and refuse
// without it. Without this such a test asserts whether ticfac happens to be
// installed on the host running it — green on a developer's machine, whose
// ~/.local/bin carries the real binary, and red on CI, which has none (epic
// hn6's CI: TestTheFactoryHandsTheLocalSubprocessExecutorTheJoin and
// TestAPrintedExitEndsAtCobraNotFang). The stub refuses any invocation, so a
// test that does start a job through it fails loudly instead of passing on
// a supervisor that is not there; a test that starts one builds the real
// binary (see cli_test.go).
func stubSupervisorOnPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	stub := filepath.Join(bin, "ticfac-exec-subprocess")
	script := "#!/bin/sh\necho 'ticfac-exec-subprocess: test stub, never started' >&2\nexit 2\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
