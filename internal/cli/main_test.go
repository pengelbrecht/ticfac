package cli

import (
	"os"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runregistry/registrytest"
)

// A test that claims a run writes a machine-local registration beside the
// run's pidfile (tick aj9), and this package's tests claim runs — the
// status, watch and overview fixtures claim r-status, r-1, r-sigterm,
// epic-run, epic-rmod and more, from per-test temp checkouts, and the
// evacuation fixture and `ticfac run` re-exec this binary as a whole
// run-epic. Without a redirect every one of those claims leaves a
// registration in the operator's real ~/.ticfac/registry, pointing at a
// temp dir that is gone by the time anyone reads it back — and read back
// they are: tick 9oo's cross-checkout refusal consults the registration on
// every claim, so the strays are not only litter, they are load-bearing for
// the next test's claim (tick eih).
//
// registrytest.GuardMain redirects the registry for the whole package the
// way internal/runlife's does (parallel tests cannot each hold the one
// environment variable that names where registrations live), and afterwards
// it scans the operator's real registry and fails the package if this test
// run wrote anything there (tick 7ag) — the redirect and the guard
// together, so the day the redirect is lost by ANY path, including a child
// spawned with an environment that drops it, the suite goes red instead of
// the operator's overview growing a phantom run. registry_redirect_test.go
// guards the redirect itself for in-process claims.
//
// TestMain also doubles this binary as the detached child `ticfac run`
// starts (tick 9sz): the production spawn execs os.Executable() with argv
// ["run-epic", ...], and under test that is THIS binary — the same re-exec
// trick the SIGTERM evacuation test uses (tick ppt), at the other end. A go
// test invocation never carries "run-epic" as its first argument, so this
// dispatch is invisible to the suite itself. Both children dispatch BEFORE
// the guard: each inherits the parent test process's environment, redirect
// included, so its claim registers where the parent's tests look — and the
// parent's guard, not the child's, is the one that scans for its leaks.
//
// The evacuation child especially cannot afford the guard (tick 8d6): it
// leaves by the SIGTERM handler's os.Exit, which skips GuardMain's cleanup,
// so the guard's ticfac-registry-guard-* root would survive in the very
// directory the test reads for the run's own temp trees — and the guard
// moves the child's TMPDIR inside that root, so the test's live-run check
// would pass on the guard's directory while the run's trees hide one level
// down. Dispatched here instead, the child's TMPDIR stays what the test
// pointed at a directory of its own, and every ticfac-* entry in it belongs
// to the run the test is making claims about.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "run-epic" {
		os.Exit(runDetachedChild(os.Args[2:]))
	}
	if os.Getenv(evacChildEnv) != "" {
		os.Exit(m.Run())
	}
	registrytest.GuardMain(m)
}
