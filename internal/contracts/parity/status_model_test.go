package parity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/schema"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
)

// contracts/status-model.json — EXECUTABLE.
//
// `ticfac.status.v1`, the one model every status surface renders (6dh: watch,
// the bare-ticfac overview, an agent checking in, i1r's phone page). It was
// born as a package-local fixture (internal/statusmodel/contract.json) with a
// note that said: the day a second reader exists it moves into the bundle. The
// factory's phone status page (cloudflare/src/status.ts, i1r) is that second
// reader, and the move happened when ticfac took the bundle over (tick 4i8).
//
// The fixture is executable three ways here:
//
//   - the schema admits its golden documents and refuses its negative ones
//     with the pinned refusals — the fixture is a reader of the shape, not a
//     decoration beside the Go type;
//   - the Go Model type ROUND-TRIPS each golden: what the type marshals is
//     what the fixture pins, field for field, so a field the fixture names
//     cannot be lost to an omitempty and a field the type grows that the
//     fixture does not know breaks the closed schema rather than a renderer;
//   - the boundary claims the fixture states in prose are asserted, because
//     the boundary is the fixture's whole reason to exist: the model reads
//     only what exists and takes no verdict from the feed.
//
// The BUILDER binding — a Model the builder produces out of Sources validates
// against this same schema — is asserted in internal/statusmodel, beside the
// builder it binds (contract_binding_test.go), reading this same file.

const statusModelFile = "status-model.json"

type statusModelContract struct {
	SchemaVersion int    `json:"schema_version"`
	Contract      string `json:"contract"`
	Tick          string `json:"tick"`
	Layout        struct {
		EmittedBy   string   `json:"emitted_by"`
		Cardinality string   `json:"cardinality"`
		Hosts       []string `json:"hosts"`
	} `json:"layout"`
	Boundary struct {
		ReadsOnlyExistingRecords bool `json:"reads_only_existing_records"`
		NewSourceOfTruth         bool `json:"new_source_of_truth"`
		VerdictsFromFeed         bool `json:"verdicts_from_feed"`
	} `json:"boundary"`
	Records map[string]struct {
		SchemaID string          `json:"schema_id"`
		Schema   json.RawMessage `json:"schema"`
	} `json:"records"`
	Defs    map[string]json.RawMessage `json:"$defs"`
	Golden  map[string]json.RawMessage `json:"golden"`
	Invalid []struct {
		Record              string          `json:"record"`
		Why                 string          `json:"why"`
		ExpectErrorContains string          `json:"expect_error_contains"`
		Document            json.RawMessage `json:"document"`
	} `json:"invalid"`
}

func loadStatusModelContract(t *testing.T) (statusModelContract, *schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	var fixture statusModelContract
	readContract(t, statusModelFile, &fixture)
	record, ok := fixture.Records["status_model"]
	if !ok {
		t.Fatalf("the status model contract declares no status_model record")
	}
	if record.SchemaID != statusmodel.SchemaID {
		t.Fatalf("the record names schema_id %q, want %q", record.SchemaID, statusmodel.SchemaID)
	}
	parsed, err := schema.ParseSchema(record.Schema)
	if err != nil {
		t.Fatalf("the status model schema does not parse: %v", err)
	}
	defs, err := schema.ParseDefs(fixture.Defs)
	if err != nil {
		t.Fatalf("the status model contract's $defs do not parse: %v", err)
	}
	return fixture, parsed, defs
}

// TestTheStatusModelContractAdmitsItsGoldens: every golden document
// validates, with no violation at all — a golden that "mostly" validates is a
// fixture that certifies a shape it does not hold.
func TestTheStatusModelContractAdmitsItsGoldens(t *testing.T) {
	fixture, record, defs := loadStatusModelContract(t)
	if len(fixture.Golden) == 0 {
		t.Fatal("the status model contract carries no golden document")
	}
	for name, raw := range fixture.Golden {
		document := decodeDocument(t, raw)
		if problems := schema.Validate(record, defs, document); len(problems) > 0 {
			t.Errorf("golden %s is refused by the schema it is the golden of:\n%s",
				name, strings.Join(problems, "\n"))
		}
	}
}

// TestTheStatusModelGoTypeRoundTripsEveryGolden: the Go Model must marshal
// back exactly what each golden states. A field the fixture pins that the
// type drops (a stray omitempty) or a field the type grows that the fixture
// does not know breaks here before it breaks a renderer.
func TestTheStatusModelGoTypeRoundTripsEveryGolden(t *testing.T) {
	fixture, _, _ := loadStatusModelContract(t)
	for name, raw := range fixture.Golden {
		var model statusmodel.Model
		if err := json.Unmarshal(raw, &model); err != nil {
			t.Errorf("golden %s does not decode into the Go Model: %v", name, err)
			continue
		}
		if model.SchemaVersion != statusmodel.SchemaVersion {
			t.Errorf("golden %s carries schema_version %d, want %d",
				name, model.SchemaVersion, statusmodel.SchemaVersion)
		}
		marshaled, err := json.Marshal(model)
		if err != nil {
			t.Errorf("the decoded golden %s does not re-marshal: %v", name, err)
			continue
		}
		if !sameJSON(t, raw, marshaled) {
			t.Errorf("the Go Model does not round-trip golden %s:\n got %s",
				name, marshaled)
		}
	}
}

// TestTheStatusModelContractRefusesItsNegatives: every negative document is
// refused with the pinned message — the same discipline the bundle's other
// fixtures hold, so a validator that drifted would name it here rather than
// half-refuse.
func TestTheStatusModelContractRefusesItsNegatives(t *testing.T) {
	fixture, record, defs := loadStatusModelContract(t)
	if len(fixture.Invalid) == 0 {
		t.Fatal("the status model contract carries no negative document")
	}
	for _, negative := range fixture.Invalid {
		document := decodeDocument(t, negative.Document)
		problems := schema.Validate(record, defs, document)
		if len(problems) == 0 {
			t.Errorf("negative %q was ADMITTED by the schema", negative.Why)
			continue
		}
		joined := strings.Join(problems, "; ")
		if !strings.Contains(joined, negative.ExpectErrorContains) {
			t.Errorf("negative %q was refused as:\n%s\nwant the pinned refusal:\n%s",
				negative.Why, joined, negative.ExpectErrorContains)
		}
	}
}

// TestTheStatusModelContractPinsTheBoundary: the claims the fixture states in
// prose are asserted, because the boundary is the fixture's whole reason to
// exist — the model reads only what exists and takes no verdict from the feed.
func TestTheStatusModelContractPinsTheBoundary(t *testing.T) {
	fixture, _, _ := loadStatusModelContract(t)
	if !fixture.Boundary.ReadsOnlyExistingRecords {
		t.Error("the contract does not pin that the model reads only existing records")
	}
	if fixture.Boundary.NewSourceOfTruth {
		t.Error("the contract claims the model adds a source of truth")
	}
	if fixture.Boundary.VerdictsFromFeed {
		t.Error("the contract claims the model takes verdicts from the feed")
	}
	if fixture.SchemaVersion != statusmodel.SchemaVersion {
		t.Errorf("the fixture's schema_version is %d, want %d",
			fixture.SchemaVersion, statusmodel.SchemaVersion)
	}
	if fixture.Layout.EmittedBy == "" || fixture.Layout.Cardinality == "" || len(fixture.Layout.Hosts) == 0 {
		t.Error("the contract does not say who emits it, how often, or who hosts it")
	}
}
