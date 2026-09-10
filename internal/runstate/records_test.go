package runstate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The record types against the bundle's own documents.
//
// A Go struct is a second spelling of a schema, and two spellings of one shape
// is what bundle 1.2.0 shipped. So the golden documents are decoded into these
// types, re-encoded, and put back through the contract's schemas: a field these
// types cannot express, or one they invent, fails here.

func TestGoldenRecordsRoundTripThroughTheGoTypes(t *testing.T) {
	c := loadContract(t)
	schemas, defs := contractSchemas(t)

	cases := []struct {
		record string
		into   func() any
	}{
		{"checkpoint", func() any { return new(Checkpoint) }},
		{"attempt", func() any { return new(Attempt) }},
		{"decision", func() any { return new(Decision) }},
	}
	for _, tc := range cases {
		t.Run(tc.record, func(t *testing.T) {
			golden, ok := c.Golden[tc.record]
			if !ok {
				t.Fatalf("the contract has no golden %s", tc.record)
			}
			record := tc.into()
			if err := decodeRecord(golden, record); err != nil {
				t.Fatalf("the golden %s is refused by this package's reader: %v", tc.record, err)
			}
			if err := validateRecord(record); err != nil {
				t.Errorf("the golden %s is refused by this package's validation: %v", tc.record, err)
			}

			encoded, err := encodeRecord(record)
			if err != nil {
				t.Fatal(err)
			}
			if errs := validateDocument(t, schemas[tc.record], defs, encoded); len(errs) != 0 {
				t.Errorf("what this package would WRITE for the golden %s is refused by schemas.%s:\n  %s",
					tc.record, tc.record, strings.Join(errs, "\n  "))
			}
			if !sameDocument(t, golden, encoded) {
				t.Errorf("the golden %s does not survive this package's types:\n  golden:  %s\n  rewrote: %s",
					tc.record, golden, encoded)
			}
		})
	}
}

// The evidence record crosses the seam: this package places the file and
// contracts/job-protocol.json defines what is in it. So the golden evidence is
// round-tripped through the Go type and validated against THAT definition.
func TestGoldenEvidenceRoundTripsAndValidatesAgainstTheContractThatDefinesIt(t *testing.T) {
	c := loadContract(t)
	s, defs := evidenceSchema(t)

	if _, defined := c.Schemas["evidence"]; defined {
		t.Error("the run-state contract defines an evidence schema of its own; one record, one definition")
	}
	if c.References.Evidence.File != jobProtocolFile {
		t.Errorf("the evidence record is referenced from %q, want %s", c.References.Evidence.File, jobProtocolFile)
	}

	golden, ok := c.Golden["evidence"]
	if !ok {
		t.Fatal("the contract has no golden evidence")
	}
	var e Evidence
	if err := decodeRecord(golden, &e); err != nil {
		t.Fatalf("the golden evidence is refused by this package's reader: %v", err)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("the golden evidence is refused by this package's validation: %v", err)
	}
	// The filename and the record's own key name the same thing.
	if got := EvidencePath(e.Provenance.RunID, e.Key); got != RunDir("r-2f9c")+"/evidence/"+e.Key+".json" {
		t.Errorf("the golden evidence would be placed at %s", got)
	}

	encoded, err := encodeRecord(e)
	if err != nil {
		t.Fatal(err)
	}
	if errs := validateDocument(t, s, defs, encoded); len(errs) != 0 {
		t.Errorf("what this package would WRITE for the golden evidence is refused by %s:\n  %s",
			jobProtocolFile, strings.Join(errs, "\n  "))
	}
	if !sameDocument(t, golden, encoded) {
		t.Errorf("the golden evidence does not survive this package's types:\n  golden:  %s\n  rewrote: %s", golden, encoded)
	}
}

// Every document the contract calls invalid is refused HERE too — by the
// reader, by the validation, or by both. A writer that would happily persist a
// document the bundle refuses is a writer that puts the run's record beyond the
// reach of everything that reads it.
func TestEveryNegativeDocumentIsRefusedByThisPackage(t *testing.T) {
	c := loadContract(t)
	if len(c.Invalid) == 0 {
		t.Fatal("the contract carries no negative example")
	}

	for _, bad := range c.Invalid {
		t.Run(bad.Record+"/"+shortWhy(bad.Why), func(t *testing.T) {
			var record any
			switch bad.Record {
			case "checkpoint":
				record = new(Checkpoint)
			case "attempt":
				record = new(Attempt)
			case "decision":
				record = new(Decision)
			case "evidence":
				record = new(Evidence)
			default:
				t.Fatalf("negative example for unknown record %q", bad.Record)
			}
			if err := decodeRecord(bad.Document, record); err != nil {
				return // refused by the reader
			}
			if err := validateRecord(record); err == nil {
				t.Errorf("a document the contract calls invalid was accepted: %s", bad.Why)
			}
		})
	}
}

// The union: an evidence record carries inline output or artifact output, never
// both and never neither. An `anyOf` with two arms satisfied is a record two
// readers disagree about.
func TestEvidenceOutputIsAClosedUnion(t *testing.T) {
	inline := Output{Inline: &InlineOutput{Mode: "inline", MaxBytes: 4096}}
	raw, err := json.Marshal(inline)
	if err != nil {
		t.Fatal(err)
	}
	var back Output
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Inline == nil || back.Artifact != nil || back.Inline.Mode != "inline" {
		t.Errorf("an inline output did not survive the round trip: %+v", back)
	}

	for _, broken := range []Output{
		{},
		{Inline: &InlineOutput{Mode: "inline"}, Artifact: &ArtifactOutput{Mode: "artifact"}},
	} {
		if _, err := json.Marshal(broken); err == nil {
			t.Errorf("%+v encoded, and it is neither one arm of the union nor the other", broken)
		}
	}
	if err := json.Unmarshal([]byte(`{"mode":"whatever"}`), &back); err == nil {
		t.Error("an output with an unknown mode was accepted")
	}
}

// Provenance is required-and-nullable in every field, and the difference is the
// point: "this ran before integration" and "nobody recorded where it ran" are
// different claims.
func TestProvenanceRefusesAnOmittedFieldAndAcceptsANullOne(t *testing.T) {
	full := map[string]any{}
	for _, field := range provenanceFields {
		full[field] = nil
	}
	full["run_id"] = "r-2f9c"
	full["source_ref"] = "refs/heads/main"
	full["source_sha"] = "acb08b9"
	full["phase"] = "post-wave"

	raw, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var p Provenance
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("provenance stating every nullable field as null was refused: %v", err)
	}
	if p.IntegrationRef != nil {
		t.Error("a null integration_ref did not read as null")
	}

	delete(full, "integration_ref")
	raw, err = json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &p); err == nil {
		t.Error("provenance OMITTING integration_ref was accepted; omitted and null are different claims")
	}
}

// The closed vocabularies, as Go values. A state meaning "in flight" that no
// other reconciler can settle is the exact failure these enums prevent.
func TestClosedVocabulariesMatchTheContract(t *testing.T) {
	c := loadContract(t)

	var contract struct {
		Defs struct {
			Phase     struct{ Enum []string } `json:"phase"`
			Executor  struct{ Enum []string } `json:"executor"`
			Role      struct{ Enum []string } `json:"role"`
			TickState struct {
				Properties struct {
					State struct{ Enum []string } `json:"state"`
				} `json:"properties"`
			} `json:"tick_state"`
		} `json:"$defs"`
		Schemas struct {
			Checkpoint struct {
				Properties struct {
					State struct{ Enum []string } `json:"state"`
				} `json:"properties"`
			} `json:"checkpoint"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(readBundleFile(t, runStateFile), &contract); err != nil {
		t.Fatal(err)
	}

	compare := func(name string, mine, theirs []string) {
		if !reflect.DeepEqual(mine, theirs) {
			t.Errorf("%s here is %v and in the contract %v", name, mine, theirs)
		}
	}
	compare("the phase vocabulary", phaseNames(), contract.Defs.Phase.Enum)
	compare("the executor vocabulary", Executors, contract.Defs.Executor.Enum)
	compare("the role vocabulary", Roles, contract.Defs.Role.Enum)
	compare("the tick-state vocabulary", TickStates, contract.Defs.TickState.Properties.State.Enum)
	compare("the run-state vocabulary", stateNames(), contract.Schemas.Checkpoint.Properties.State.Enum)

	// Terminal is this package's reading of that vocabulary, and it decides
	// when the run tag is placed.
	terminal := []State{}
	for _, s := range States {
		if s.Terminal() {
			terminal = append(terminal, s)
		}
	}
	if !reflect.DeepEqual(terminal, []State{StateCompleted, StateFailed, StateCancelled}) {
		t.Errorf("the terminal states are %v", terminal)
	}

	// The envelope, on every committed record.
	schemas, _ := contractSchemas(t)
	for name, s := range schemas {
		for _, field := range []string{"schema_version", "provenance"} {
			if !contains(s.Required, field) {
				t.Errorf("schemas.%s does not require the envelope field %q", name, field)
			}
		}
	}
	_ = c
}

func validateRecord(record any) error {
	switch r := record.(type) {
	case *Checkpoint:
		return r.Validate()
	case *Attempt:
		return r.Validate()
	case *Decision:
		return r.Validate()
	case *Evidence:
		return r.Validate()
	}
	return nil
}

func sameDocument(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(left, right)
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// shortWhy is a subtest name, not a summary: the contract's `why` is a
// paragraph and the run log wants a handle.
func shortWhy(why string) string {
	if i := strings.IndexAny(why, "—,:"); i > 0 {
		why = why[:i]
	}
	if len(why) > 40 {
		why = why[:40]
	}
	return strings.ReplaceAll(strings.TrimSpace(why), " ", "-")
}
