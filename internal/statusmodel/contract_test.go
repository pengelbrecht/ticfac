package statusmodel

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contract.json — EXECUTABLE (contracts/README.md's rule: a copied JSON file
// without an executable check is not a contract).
//
// Three things are executed here rather than described:
//
//   - the schema (`ticfac.status.v1`) admits its golden documents and
//     refuses its negative ones, with the pinned refusal — the fixture is a
//     reader of the shape and not a decoration beside the Go type;
//   - the Go Model type ROUND-TRIPS each golden: what the type marshals is
//     what the fixture pins, field for field, so a field the fixture names
//     cannot be lost to an omitempty and a field the type grows that the
//     fixture does not know breaks the closed schema rather than a renderer;
//   - a Model the BUILDER produces out of Sources validates against the
//     schema, so the assembled answer and the pinned shape are the same
//     claim, not two documents that happen to agree today.
//
// The fixture lives in this package, not the cross-language bundle, because
// only Go reads it today; the day a second reader exists it moves to the
// bundle, and this test moves with it.

const contractFile = "contract.json"

type contractFixture struct {
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

func loadContract(t *testing.T) (contractFixture, *schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	raw, err := os.ReadFile(contractFile)
	if err != nil {
		t.Fatalf("read the status model contract: %v", err)
	}
	var fixture contractFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("the status model contract does not parse: %v", err)
	}
	record, ok := fixture.Records["status_model"]
	if !ok {
		t.Fatalf("the status model contract declares no status_model record")
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
	return fixture, parsed, defs
}

// TestTheContractAdmitsItsGoldens: every golden document validates, with no
// violation at all — a golden that "mostly" validates is a fixture that
// certifies a shape it does not hold.
func TestTheContractAdmitsItsGoldens(t *testing.T) {
	t.Parallel()
	fixture, record, defs := loadContract(t)
	if len(fixture.Golden) == 0 {
		t.Fatal("the status model contract carries no golden document")
	}
	for name, raw := range fixture.Golden {
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Errorf("golden %s does not parse: %v", name, err)
			continue
		}
		if problems := schema.Validate(record, defs, document); len(problems) > 0 {
			t.Errorf("golden %s is refused by the schema it is the golden of:\n%s",
				name, strings.Join(problems, "\n"))
		}
	}
}

// TestTheGoTypeRoundTripsEveryGolden: the Go Model must marshal back exactly
// what each golden states. A field the fixture pins that the type drops (a
// stray omitempty) or a field the type grows that the fixture does not know
// breaks here before it breaks a renderer.
func TestTheGoTypeRoundTripsEveryGolden(t *testing.T) {
	t.Parallel()
	fixture, _, _ := loadContract(t)
	for name, raw := range fixture.Golden {
		var model Model
		if err := json.Unmarshal(raw, &model); err != nil {
			t.Errorf("golden %s does not decode into the Go Model: %v", name, err)
			continue
		}
		if model.SchemaVersion != SchemaVersion {
			t.Errorf("golden %s carries schema_version %d, want %d", name, model.SchemaVersion, SchemaVersion)
		}
		marshaled, err := json.Marshal(model)
		if err != nil {
			t.Errorf("the decoded golden %s does not re-marshal: %v", name, err)
			continue
		}
		var before, after map[string]any
		if err := json.Unmarshal(raw, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(marshaled, &after); err != nil {
			t.Fatal(err)
		}
		if !sameJSON(before, after) {
			t.Errorf("the Go Model does not round-trip golden %s:\n got %s", name,
				indent(marshaled))
		}
	}
}

// TestTheContractRefusesItsNegatives: every negative document is refused with
// the pinned message — the same discipline the bundle's fixtures hold, so a
// validator that drifted would name it here rather than half-refuse.
func TestTheContractRefusesItsNegatives(t *testing.T) {
	t.Parallel()
	fixture, record, defs := loadContract(t)
	if len(fixture.Invalid) == 0 {
		t.Fatal("the status model contract carries no negative document")
	}
	for _, negative := range fixture.Invalid {
		var document any
		if err := json.Unmarshal(negative.Document, &document); err != nil {
			t.Errorf("negative %q does not parse: %v", negative.Why, err)
			continue
		}
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

// TestTheContractBindsTheBuilder: a Model the builder produces out of Sources
// marshals and validates against the contract's own schema — the assembled
// answer and the pinned shape are one claim, not two documents that happen
// to agree. The Sources are the build_test fixture's running epic.
func TestTheContractBindsTheBuilder(t *testing.T) {
	t.Parallel()
	_, record, defs := loadContract(t)
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
			strings.Join(problems, "\n"), indent(raw))
	}
}

// TestTheContractPinsTheBoundary: the claims the fixture states in prose are
// asserted, because the boundary is the fixture's whole reason to exist —
// the model reads only what exists and takes no verdict from the feed.
func TestTheContractPinsTheBoundary(t *testing.T) {
	t.Parallel()
	fixture, _, _ := loadContract(t)
	if !fixture.Boundary.ReadsOnlyExistingRecords {
		t.Error("the contract does not pin that the model reads only existing records")
	}
	if fixture.Boundary.NewSourceOfTruth {
		t.Error("the contract claims the model adds a source of truth")
	}
	if fixture.Boundary.VerdictsFromFeed {
		t.Error("the contract claims the model takes verdicts from the feed")
	}
	if fixture.SchemaVersion != SchemaVersion {
		t.Errorf("the fixture's schema_version is %d, want %d", fixture.SchemaVersion, SchemaVersion)
	}
}

// sameJSON compares two decoded documents deeply, without relying on map
// ordering.
func sameJSON(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, ok := right[key]
			if !ok || !sameJSON(value, other) {
				return false
			}
		}
		return true
	case []any:
		right, ok := b.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for i := range left {
			if !sameJSON(left[i], right[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

func indent(raw []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}
