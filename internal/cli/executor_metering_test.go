package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/reconcile"
)

// The gateway metering join the herdr factory resolves (tick dm2): the run
// id the dispatch carries, the gateway ~/.ticfacrc names, and the
// documented-optional state that launches exactly as before.

// isolateHostCredentials points HOME at an empty directory and returns the
// ~/.ticfacrc path to write, so the host's own factory credentials never
// answer for the test.
func isolateHostCredentials(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".ticfacrc")
}

// short: a pure resolution over a temp HOME, no repository is built
func TestTheHerdrMeteringJoinNeedsBothHalvesOfTheHostCredential(t *testing.T) {
	rc := isolateHostCredentials(t)
	d := reconcile.Dispatch{RunID: "epic-hn6"}

	// Nothing configured: nil, never an error — the dispatch runs as before.
	if m := herdrMetering(d); m != nil {
		t.Fatalf("a host with no ~/.ticfacrc got metering %+v, want none: telemetry is optional and the dispatch must not change", *m)
	}

	// The gateway alone is half-set: the token is what authenticates the
	// route, and a join whose requests the gateway would refuse is a broken
	// dispatch, not a metered one.
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := herdrMetering(d); m != nil {
		t.Fatalf("a gateway with no token got metering %+v, want none: half-set is refused, not guessed around", *m)
	}

	// The token alone names a route nothing reads.
	if err := os.WriteFile(rc, []byte("factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := herdrMetering(d); m != nil {
		t.Fatalf("a token with no gateway got metering %+v, want none: half-set is refused, not guessed around", *m)
	}

	// A gateway outside Cloudflare: the requests would route, but the logs
	// API this repository reads would never answer, and routing without the
	// read is spend without the metering.
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://proxy.example.com/acct/gw\n"+
		"factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := herdrMetering(d); m != nil {
		t.Fatalf("a non-Cloudflare gateway got metering %+v, want none: no logs API, no join", *m)
	}

	// Both halves: the join, carrying the dispatch's own run id — the id the
	// status model's reader filters the gateway logs by.
	if err := os.WriteFile(rc, []byte("factory_gateway_url=https://gateway.ai.cloudflare.com/v1/acct/gw/\n"+
		"factory_cloudflare_api_token=cft_test_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := herdrMetering(d)
	if m == nil {
		t.Fatal("a fully configured host got no metering: the join is the tick's whole point")
	}
	if m.RunID != "epic-hn6" {
		t.Errorf("the join attributes to run id %q, want the dispatch's own", m.RunID)
	}
	// The resolved join must itself be a usable override: the executor
	// writes it into the attempt state dir.
	path, err := m.WriteExtension(t.TempDir())
	if err != nil {
		t.Fatalf("the resolved join does not write its override: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the override file the agent will load is missing: %v", err)
	}
	// And it keeps the model gate: a non-Workers-AI dispatch never loads it.
	var none *subprocess.GatewayMetering
	if none.Applies("opus") {
		t.Error("a nil join applies to a claude model: nothing is configured")
	}
}
