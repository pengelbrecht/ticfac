package cli

import (
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runregistry/registrytest"
)

// A test that claims a run writes a machine-local registration beside the
// run's pidfile (tick aj9), and this package's tests claim runs — the
// status, watch and overview fixtures claim r-status, r-1, epic-rmod and
// more, from per-test temp checkouts, and the evacuation fixture re-execs
// this binary as a whole run-epic. Without a redirect every one of those
// claims leaves a registration in the operator's real ~/.ticfac/registry,
// pointing at a temp dir that is gone by the time anyone reads it back —
// and read back they are: tick 9oo's cross-checkout refusal consults the
// registration on every claim, so the strays are not only litter, they are
// load-bearing for the next test's claim (tick eih).
//
// registrytest.GuardMain is the whole TestMain: it redirects the registry
// for the whole package the way internal/runlife's does (parallel tests
// cannot each hold the one environment variable that names where
// registrations live), and afterwards it scans the operator's real
// registry and fails the package if this test run wrote anything there
// (tick 7ag) — the redirect and the guard together, so the day the
// redirect is lost by ANY path, including a child spawned with an
// environment that drops it, the suite goes red instead of the operator's
// overview growing a phantom run. registry_redirect_test.go guards the
// redirect itself for in-process claims.
func TestMain(m *testing.M) {
	registrytest.GuardMain(m)
}
