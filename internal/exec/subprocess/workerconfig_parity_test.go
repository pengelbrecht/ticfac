package subprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// The metering join crosses a language boundary nothing imports across:
// worker.json's metering object is MARSHALLED by this package's writer
// (workerconfig.go, tick m1w) and READ by the harness
// (harness/src/local/gateway-metering.ts, tick lrd) — and the finding tick
// lrd absorbed was exactly a drift of this shape: a Go writer writing a join
// no harness code read, green because the only tests exercised the writer.
//
// The harness's own node suite pins the read half end to end (a metered
// local launch whose worker.json carries a join tags its calls, against a
// fake AI Gateway); this guard pins the direction only a Go test can see —
// that every field the writer marshals is a field the harness declares, so a
// renamed tag here can never write a join the harness silently drops. The
// subscription rung's parity guard (internal/profile) is the pattern.
func TestTheMeteringJoinsSpellingsMatchTheHarnessReader(t *testing.T) {
	t.Parallel()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := os.ReadFile(filepath.Join(root, "harness", "src", "local", "gateway-metering.ts"))
	if err != nil {
		t.Fatalf("read the harness's metering reader: %v — a join worker.json carries would be read by no one, the exact shape of the finding this tick absorbed", err)
	}
	config, err := os.ReadFile(filepath.Join(root, "harness", "src", "local", "worker-host.ts"))
	if err != nil {
		t.Fatalf("read the harness's worker config type: %v", err)
	}
	if !regexp.MustCompile(`readonly metering\??:`).Match(config) {
		t.Error("LocalWorkerConfig declares no metering field: the join this writer marshals would be read by no harness code, the exact finding tick lrd absorbed")
	}
	probe, err := json.Marshal(workerMetering{
		GatewayURL:        "https://gateway.ai.cloudflare.com/v1/acct/gw",
		RunID:             "run-lrd",
		Metadata:          `{"run_id":"run-lrd"}`,
		CredentialCommand: "cat \"$HOME/.ticfacrc\" | sed s/^/Bearer\\ /",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(probe, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		if !regexp.MustCompile("readonly " + regexp.QuoteMeta(name) + `\??:`).Match(reader) {
			t.Errorf("worker.json carries a metering field %q that harness/src/local/gateway-metering.ts does not declare: the harness would read the join without it, silently unmetered", name)
		}
	}
}
