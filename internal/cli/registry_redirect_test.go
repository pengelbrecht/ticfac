package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/runregistry"
)

// Tick eih: this package's tests claim runs — runlife.Claim from the status,
// watch and overview fixtures — and a claim writes a machine-local
// registration (tick aj9) into runregistry's directory, which unredirected
// is the operator's real ~/.ticfac/registry. Every gate and CI run left
// registrations there pointing at temp checkouts that are gone by the time
// anyone reads them back, and tick 9oo's cross-checkout refusal reads them
// back on the next claim, making the strays load-bearing. TestMain in
// main_test.go redirects the directory for the whole package before any
// test runs; this test is the guard that fails the package the day that
// redirect is lost, by proving a claim made here can reach ONLY the
// redirected directory.
func TestTheRegistryClaimsWriteToIsRedirectedAwayFromTheOperatorHome(t *testing.T) {
	t.Parallel()

	// The default directory, computed the way runregistry computes it
	// when nothing redirects it.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("this host cannot name the operator's home: %v", err)
	}
	operator := filepath.Join(home, ".ticfac", "registry")

	// The assert comes BEFORE the claim: with the redirect lost, failing
	// here leaves nothing written on the operator's machine.
	dir := runregistry.Dir()
	if dir == operator {
		t.Fatalf("runregistry.Dir() is the operator's own %s: this package's tests claim runs, and a claim writes a registration there — every gate run would leave registrations pointing at temp checkouts nobody can read back. TestMain must set %s before any test runs.",
			operator, runregistry.RegistryDirEnv)
	}

	// And the redirect actually carries a claim: a run claimed here is
	// registered in the redirected directory alone.
	runID := "r-cli-registry"
	repo := t.TempDir()
	life, err := runlife.Claim(repo, runID)
	if err != nil {
		t.Fatalf("claim a run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	if _, err := os.Stat(filepath.Join(dir, runID+".json")); err != nil {
		t.Errorf("the claim did not register in the redirected registry %s: %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(operator, runID+".json")); err == nil {
		t.Errorf("a claim in this test wrote into the operator's registry: %s",
			filepath.Join(operator, runID+".json"))
	}
}
