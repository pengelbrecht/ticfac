package runstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// The vendored contract, read by this package's tests exactly as the parity
// readers read it: from contracts/, never from a copy. Two copies of a fixture
// is the one arrangement guaranteed to defeat it.
//
// internal/contracts/parity already replays these sequences over an in-memory
// fake. This package replays them over REAL git, against a temporary bare
// origin, because the fake is a model of git and a model is not the thing.

const (
	runStateFile    = "ticfac-run-state.json"
	jobProtocolFile = "job-protocol.json"
)

type casStep struct {
	Actor           string         `json:"actor"`
	Op              string         `json:"op"`
	Path            string         `json:"path"`
	Content         map[string]any `json:"content"`
	Expect          string         `json:"expect"`
	EffectPermitted *bool          `json:"effect_permitted"`
}

type casSequence struct {
	ID    string    `json:"id"`
	Why   string    `json:"why"`
	Steps []casStep `json:"steps"`
	Final struct {
		OriginWrites int                       `json:"origin_writes"`
		Files        map[string]map[string]any `json:"files"`
	} `json:"final"`
}

type runStateContract struct {
	Layout struct {
		Root    string `json:"root"`
		RunDir  string `json:"run_dir"`
		Entries []struct {
			Path      string `json:"path"`
			Record    string `json:"record"`
			Committed bool   `json:"committed"`
			CAS       string `json:"cas"`
		} `json:"entries"`
	} `json:"layout"`
	Persistence struct {
		DurableMeans string `json:"durable_means"`
		CheckpointOn string `json:"checkpoint_on"`
		Tag          struct {
			Pattern  string `json:"pattern"`
			PlacedAt string `json:"placed_at"`
		} `json:"tag"`
	} `json:"persistence"`
	Gitignore struct {
		Target          string   `json:"target"`
		BeginMarker     string   `json:"begin_marker"`
		EndMarker       string   `json:"end_marker"`
		Fragment        []string `json:"fragment"`
		IgnoredExamples []string `json:"ignored_examples"`
		TrackedExamples []string `json:"tracked_examples"`
	} `json:"gitignore"`
	References struct {
		Evidence struct {
			SchemaID string `json:"schema_id"`
			File     string `json:"file"`
		} `json:"evidence"`
	} `json:"references"`
	Defs    map[string]json.RawMessage `json:"$defs"`
	Schemas map[string]json.RawMessage `json:"schemas"`
	Golden  map[string]json.RawMessage `json:"golden"`
	Invalid []struct {
		Record              string          `json:"record"`
		ValidatedAgainst    string          `json:"validated_against"`
		Why                 string          `json:"why"`
		ExpectErrorContains string          `json:"expect_error_contains"`
		Document            json.RawMessage `json:"document"`
	} `json:"invalid"`
	CAS struct {
		Modes []struct {
			Mode       string   `json:"mode"`
			OnConflict string   `json:"on_conflict"`
			Records    []string `json:"records"`
		} `json:"modes"`
		Mechanisms map[string]string `json:"mechanisms"`
		Fake       struct {
			Ops []struct {
				Op       string   `json:"op"`
				Outcomes []string `json:"outcomes"`
			} `json:"ops"`
		} `json:"fake"`
		Sequences []casSequence `json:"sequences"`
	} `json:"cas"`
}

func loadContract(t *testing.T) runStateContract {
	t.Helper()
	var c runStateContract
	if err := json.Unmarshal(readBundleFile(t, runStateFile), &c); err != nil {
		t.Fatalf("parse %s: %v", runStateFile, err)
	}
	return c
}

func readBundleFile(t *testing.T, name string) []byte {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatalf("locate the vendored contract bundle: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}

// contractSchemas parses the three record schemas and the shared $defs out of
// the run-state contract, through the strict subset.
func contractSchemas(t *testing.T) (map[string]*schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	c := loadContract(t)
	defs, err := schema.ParseDefs(c.Defs)
	if err != nil {
		t.Fatalf("%v", err)
	}
	schemas := map[string]*schema.Schema{}
	for name, raw := range c.Schemas {
		s, err := schema.ParseSchema(raw)
		if err != nil {
			t.Fatalf("schemas.%s: %v", name, err)
		}
		schemas[name] = s
	}
	return schemas, defs
}

// evidenceSchema is read from the contract that DEFINES the evidence record.
// This package places the file; job-protocol.json owns what is in it, so the
// documents this package writes are checked against that definition and never
// against a second schema kept here.
func evidenceSchema(t *testing.T) (*schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	var jp struct {
		Records map[string]struct {
			SchemaID string          `json:"schema_id"`
			Schema   json.RawMessage `json:"schema"`
		} `json:"records"`
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(readBundleFile(t, jobProtocolFile), &jp); err != nil {
		t.Fatalf("parse %s: %v", jobProtocolFile, err)
	}
	record, ok := jp.Records["evidence"]
	if !ok {
		t.Fatalf("%s no longer defines the evidence record", jobProtocolFile)
	}
	if record.SchemaID != "ticfac.evidence.v1" {
		t.Fatalf("the evidence record's schema_id moved to %q", record.SchemaID)
	}
	defs, err := schema.ParseDefs(jp.Defs)
	if err != nil {
		t.Fatalf("%v", err)
	}
	s, err := schema.ParseSchema(record.Schema)
	if err != nil {
		t.Fatalf("records.evidence: %v", err)
	}
	return s, defs
}

// validateDocument puts one encoded record through a bundle schema. The store
// writes bytes, so the bytes are what is validated — not a struct a test
// rebuilt by hand.
func validateDocument(t *testing.T, s *schema.Schema, defs map[string]*schema.Schema, encoded []byte) []string {
	t.Helper()
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode document: %v", err)
	}
	return schema.Validate(s, defs, document)
}

// The layout, the tag pattern and the CAS mode per record are contract text
// this package restates in Go. Restating is fine; drifting is not.
func TestThePathHelpersAreTheContractsLayout(t *testing.T) {
	c := loadContract(t)

	if Root != c.Layout.Root {
		t.Errorf("Root = %q, the contract says %q", Root, c.Layout.Root)
	}
	const runID, key = "r-2f9c", "gate-integrated-go-test"
	want := map[string]string{
		CheckpointPath(runID):    "sha_guarded_update",
		AttemptPath(runID, 1):    "create_if_absent",
		DecisionPath(runID, 1):   "create_if_absent",
		EvidencePath(runID, key): "create_if_absent",
	}
	got := map[string]string{}
	for _, e := range c.Layout.Entries {
		if !e.Committed {
			continue
		}
		path := e.Path
		for placeholder, value := range map[string]string{"<run-id>": runID, "<n>": "1", "<key>": key} {
			path = strings.ReplaceAll(path, placeholder, value)
		}
		got[path] = e.CAS
	}
	if len(got) != len(want) {
		t.Errorf("the contract commits %d records and this package places %d", len(got), len(want))
	}
	for path, mode := range want {
		actual, ok := got[path]
		if !ok {
			t.Errorf("%s is not a path the contract's layout describes", path)
			continue
		}
		if actual != mode {
			t.Errorf("%s is written %s here and %s in the contract", path, mode, actual)
		}
	}

	if pattern := strings.ReplaceAll(c.Persistence.Tag.Pattern, "<run-id>", runID); TagName(runID) != pattern {
		t.Errorf("TagName = %q, the contract's pattern gives %q", TagName(runID), pattern)
	}
	if c.Persistence.Tag.PlacedAt != "terminal state" {
		t.Errorf("the tag is placed at %q; this store places it on a terminal checkpoint", c.Persistence.Tag.PlacedAt)
	}
	if c.Persistence.CheckpointOn != "state change" {
		t.Errorf("checkpoint_on = %q; PutCheckpoint writes on a state change", c.Persistence.CheckpointOn)
	}
}

// The outcome vocabulary is the fake's, because a second implementation that
// renames the outcomes cannot be compared with the first.
func TestOutcomesAreTheContractsVocabulary(t *testing.T) {
	c := loadContract(t)

	mine := map[string]bool{
		string(Fetched): true, string(NoChange): true, string(LocalOnly): true,
		string(Created): true, string(Updated): true,
		string(ConflictExists): true, string(ConflictStaleSHA): true, string(ConflictMissingBase): true,
	}
	for _, op := range c.CAS.Fake.Ops {
		for _, outcome := range op.Outcomes {
			if !mine[outcome] {
				t.Errorf("op %q declares outcome %q, which this package cannot return", op.Op, outcome)
			}
			// A refusal and a permission are opposites: an outcome that is
			// both is a guard that decides nothing.
			if Outcome(outcome).IsConflict() == Outcome(outcome).EffectPermitted() {
				t.Errorf("outcome %q is both a conflict and effect-permitting", outcome)
			}
		}
	}
	for _, mode := range c.CAS.Modes {
		if !mine[mode.OnConflict] {
			t.Errorf("cas mode %q refuses with %q, which this package cannot return", mode.Mode, mode.OnConflict)
		}
		if !Outcome(mode.OnConflict).IsConflict() {
			t.Errorf("%q is the refusal of cas mode %q and this package does not read it as one",
				mode.OnConflict, mode.Mode)
		}
	}
	if got := c.CAS.Mechanisms["local_host"]; got != "git push --force-with-lease" {
		t.Errorf("the local host's mechanism is %q; this store pushes with --force-with-lease", got)
	}
}
