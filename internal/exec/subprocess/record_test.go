package subprocess

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// The four operations against contracts/job-protocol.json's own documents.
//
// Two directions, and both are needed. The goldens go THROUGH these Go types
// and have to come back unchanged, which is what catches a field this package
// forgot or spells differently. The records the operations EMIT are validated
// against the bundle's schemas, which is what catches this executor inventing
// a shape the reconciler will not read.
//
// The bundle is never edited here and is read from the vendored copy, so a
// contract change upstream fails this package as well as internal/contracts.

type jobProtocolFixture struct {
	Records map[string]struct {
		SchemaID string          `json:"schema_id"`
		Schema   json.RawMessage `json:"schema"`
	} `json:"records"`
	Defs     map[string]json.RawMessage `json:"$defs"`
	Examples struct {
		Golden []struct {
			Name     string          `json:"name"`
			Record   string          `json:"record"`
			Document json.RawMessage `json:"document"`
		} `json:"golden"`
		Negative []struct {
			Name     string          `json:"name"`
			Record   string          `json:"record"`
			Why      string          `json:"why"`
			Document json.RawMessage `json:"document"`
		} `json:"negative"`
	} `json:"examples"`
}

func loadProtocol(t *testing.T) (jobProtocolFixture, map[string]*schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	dir, err := contracts.Dir()
	if err != nil {
		t.Fatalf("locate the vendored contract bundle: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "job-protocol.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture jobProtocolFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	defs, err := schema.ParseDefs(fixture.Defs)
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]*schema.Schema{}
	for name, record := range fixture.Records {
		parsed, err := schema.ParseSchema(record.Schema)
		if err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
		records[name] = parsed
	}
	return fixture, records, defs
}

// spokenRecords are the records this executor reads or writes, and the Go type
// each one decodes into. `evidence` is deliberately absent: it is the gate's
// record, persisted by whatever runs a check, and an executor that invented
// one would be fabricating provenance for work it did not evaluate.
var spokenRecords = map[string]func() any{
	"job_spec":    func() any { return new(JobSpec) },
	"job_handle":  func() any { return new(JobHandle) },
	"job_status":  func() any { return new(JobStatus) },
	"cancel_ack":  func() any { return new(CancelAck) },
	"job_result":  func() any { return new(JobResult) },
	"role_result": func() any { return new(RoleResult) },
}

// Every golden document of every record this executor speaks decodes into its
// Go type and re-encodes to the SAME document. A field this package does not
// know about disappears on the way through, which is exactly what this
// catches — and it is why the comparison is of decoded values rather than of
// bytes, so that key order and indentation are not mistaken for meaning.
func TestEveryGoldenDocumentRoundTripsThroughTheseTypes(t *testing.T) {
	fixture, _, _ := loadProtocol(t)

	seen := map[string]bool{}
	for _, golden := range fixture.Examples.Golden {
		newValue, spoken := spokenRecords[golden.Record]
		if !spoken {
			continue
		}
		seen[golden.Record] = true
		value := newValue()
		if err := json.Unmarshal(golden.Document, value); err != nil {
			t.Errorf("%s (%s) does not decode: %v", golden.Name, golden.Record, err)
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Errorf("%s (%s) does not re-encode: %v", golden.Name, golden.Record, err)
			continue
		}
		var before, after any
		if err := json.Unmarshal(golden.Document, &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Errorf("%s (%s) changed on the way through:\n  contract %s\n  this package %s",
				golden.Name, golden.Record, golden.Document, encoded)
		}
	}
	for record := range spokenRecords {
		if !seen[record] {
			t.Errorf("no golden document exercises %s; a type nothing has ever decoded is untested", record)
		}
	}
}

// The strict decode refuses every negative job_spec the contract carries. The
// executor validates by hand rather than by carrying a schema validator at run
// time, so this is what keeps the two from disagreeing about what a valid spec
// is: the bundle says these five documents are wrong, and Start must refuse
// all five.
func TestEveryNegativeJobSpecIsRefused(t *testing.T) {
	fixture, _, _ := loadProtocol(t)

	negatives := 0
	for _, bad := range fixture.Examples.Negative {
		if bad.Record != "job_spec" {
			continue
		}
		negatives++
		if _, err := ParseJobSpec(bad.Document); err == nil {
			t.Errorf("%s was accepted: %s", bad.Name, bad.Why)
		}
	}
	if negatives == 0 {
		t.Fatal("the contract carries no negative job_spec; nothing has watched this parser refuse")
	}
}

// Every golden job_spec is ACCEPTED by the same parser. A validator that
// refuses everything passes the test above and is useless.
func TestEveryGoldenJobSpecIsAccepted(t *testing.T) {
	fixture, _, _ := loadProtocol(t)

	accepted := 0
	for _, golden := range fixture.Examples.Golden {
		if golden.Record != "job_spec" {
			continue
		}
		if _, err := ParseJobSpec(golden.Document); err != nil {
			t.Errorf("%s is golden and this parser refuses it: %v", golden.Name, err)
			continue
		}
		accepted++
	}
	if accepted == 0 {
		t.Fatal("no golden job_spec was accepted")
	}
}

// The records the four operations EMIT, validated against the bundle's
// schemas. This is the acceptance criterion in one test: start returns a
// JobHandle, inspect a JobStatus (live and terminal), cancel a CancelAck and
// collect a JobResult, and each is a document the contract admits.
func TestTheRecordsTheFourOperationsEmitValidateAgainstTheContract(t *testing.T) {
	_, records, defs := loadProtocol(t)
	validate := func(name, record string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		if errs := schema.Validate(records[record], defs, document); len(errs) != 0 {
			t.Errorf("%s is not a valid %s:\n  %s\n%s", name, record, strings.Join(errs, "\n  "), raw)
		}
	}

	f := newFixture(t, fixtureOptions{mode: "hang"})
	spec := f.spec("run-17/tick-qqq/attempt-1", "qqq")
	validate("the spec this test starts from", "job_spec", spec)

	handle := f.Start(spec)
	validate("the handle start returned", "job_handle", handle)

	waitFor(t, "the runner to start", 20*time.Second, func() bool { return f.store(handle).runnerPID() > 0 })
	live := f.inspect(handle)
	if live.State != StateRunning {
		t.Fatalf("the attempt is %s, and this test needs a live one", live.State)
	}
	validate("a live status", "job_status", live)

	ack, err := f.Executor.Cancel(handle)
	if err != nil {
		t.Fatal(err)
	}
	validate("the cancellation acknowledgement", "cancel_ack", ack)

	terminal := f.inspect(handle)
	validate("a terminal status", "job_status", terminal)

	result, err := f.Executor.Collect(handle)
	if err != nil {
		t.Fatal(err)
	}
	validate("the collected result", "job_result", result)

	// And a succeeded result, which is the one that must carry a role_result.
	g := newFixture(t, fixtureOptions{mode: "report", name: "second"})
	done := g.Start(g.spec("run-18/tick-rrr/attempt-1", "rrr"))
	g.waitSettled(done)
	success, err := g.Executor.Collect(done)
	if err != nil {
		t.Fatal(err)
	}
	if success.Outcome != OutcomeSucceeded || success.RoleResult == nil {
		t.Fatalf("outcome %s with role_result %v", success.Outcome, success.RoleResult)
	}
	validate("a succeeded result", "job_result", success)
	validate("its role result", "role_result", success.RoleResult)
}

// The contract's local-subprocess handle golden is not decoration: it spells
// the three keys a local handle carries, and this executor's handle carries
// them under the same names.
func TestTheLocalSubprocessHandleGoldenIsThisExecutorsShape(t *testing.T) {
	fixture, _, _ := loadProtocol(t)

	var goldenHandle map[string]any
	for _, golden := range fixture.Examples.Golden {
		if golden.Record != "job_handle" {
			continue
		}
		var document struct {
			Executor string         `json:"executor"`
			Handle   map[string]any `json:"handle"`
		}
		if err := json.Unmarshal(golden.Document, &document); err != nil {
			t.Fatal(err)
		}
		if document.Executor == ExecutorName {
			goldenHandle = document.Handle
		}
	}
	if goldenHandle == nil {
		t.Fatalf("the contract carries no golden handle for the %s executor", ExecutorName)
	}

	ours := (&LocalHandle{PID: 1, Worktree: "/w", Branch: "b", State: "/s"}).asMap()
	for key := range goldenHandle {
		if _, ok := ours[key]; !ok {
			t.Errorf("the contract's local-subprocess handle carries %q and this executor's does not", key)
		}
	}
}
