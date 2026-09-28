package statusmodel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contracts/status-model.json — the bundle fixture for `ticfac.status.v1`.
//
// 6dh cut this fixture as a package-local file with one reader; the factory's
// phone status page (i1r, cloudflare/src/status.ts) became the second reader,
// and the fixture moved into the contract bundle when ticfac took it over
// (tick 4i8). The SHAPE half of its execution lives beside the bundle now
// (internal/contracts/parity/status_model_test.go): goldens admitted,
// negatives refused with the pinned messages, the Go Model round-tripped.
//
// What could not move is here, because it binds the BUILDER: a Model this
// package's Build produces out of Sources must validate against the same
// schema the fixture pins — the assembled answer and the pinned shape are one
// claim, not two documents that happen to agree today.

// bundleFixture reads the status model contract from the bundle.
func bundleFixture(t *testing.T) (*schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatalf("locate the contract bundle: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "status-model.json"))
	if err != nil {
		t.Fatalf("read the status model contract: %v", err)
	}
	var fixture struct {
		Records map[string]struct {
			SchemaID string          `json:"schema_id"`
			Schema   json.RawMessage `json:"schema"`
		} `json:"records"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the status model contract does not parse: %v", err)
	}
	record, ok := fixture.Records["status_model"]
	if !ok {
		t.Fatal("the status model contract declares no status_model record")
	}
	if record.SchemaID != SchemaID {
		t.Fatalf("the record names schema_id %q, want %q", record.SchemaID, SchemaID)
	}
	parsed, err := schema.ParseSchema(record.Schema)
	if err != nil {
		t.Fatalf("the status model schema does not parse: %v", err)
	}
	defs, err := schema.ParseDefs(fixture.Defs)
	if err != nil {
		t.Fatalf("the status model contract's $defs do not parse: %v", err)
	}
	return parsed, defs
}

// TestTheContractBindsTheBuilder: a Model the builder produces out of Sources
// marshals and validates against the contract's own schema. The Sources are
// the build_test fixture's running epic.
func TestTheContractBindsTheBuilder(t *testing.T) {
	record, defs := bundleFixture(t)
	model := Build(runningEpicSources())
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("the built model does not marshal: %v", err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if problems := schema.Validate(record, defs, document); len(problems) > 0 {
		t.Errorf("a model the builder produced is refused by the contract:\n%s\nmodel:\n%s",
			strings.Join(problems, "\n"), raw)
	}
}
