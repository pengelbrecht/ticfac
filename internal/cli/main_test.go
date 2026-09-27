package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// A test that claims a run writes a machine-local registration beside the
// run's pidfile (tick aj9), and this package's tests claim runs — the
// status, watch and overview fixtures claim r-status, r-1, r-sigterm,
// epic-run and more, from per-test temp checkouts. Without a redirect every
// one of those claims leaves a registration in the operator's real
// ~/.ticfac/registry, pointing at a temp dir that is gone by the time
// anyone reads it back — and read back they are: tick 9oo's cross-checkout
// refusal consults the registration on every claim, so the strays are not
// only litter, they are load-bearing for the next test's claim (tick eih).
//
// The registry is redirected for the WHOLE package here, the way
// internal/runlife's own TestMain does for the same reason: parallel tests
// cannot each hold the one environment variable that names where
// registrations live, so the redirect happens once, before any test runs.
// registry_redirect_test.go guards the day this file is lost.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ticfac-cli-registry-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv(runregistry.RegistryDirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
