package parity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/schema"
)

// contracts/job-protocol.json — EXECUTABLE.
//
// The four-operation executor protocol ticfac is built around: JobSpec,
// JobHandle, JobStatus, the cancel acknowledgement, JobResult, the role-result
// envelope and the bundle's ONE evidence record. It is ticfac's contract,
// frozen in ticks in Phase 0 precisely because the code that implements it
// lands here — so this reader is the first implementation-side validator the
// file has ever had.
//
// Fully executable: the records are schemas in the strict subset, and the file
// carries thirteen golden documents each schema must admit and eighteen
// negative documents each must refuse, with the exact refusal pinned. That
// last part is what makes the negatives worth having — a negative asserting
// only "something failed" is satisfied by a validator that has quietly stopped
// checking the thing the case was written about.

const jobProtocolFile = "job-protocol.json"

type jobProtocol struct {
	SchemaVersion int      `json:"schema_version"`
	Contract      string   `json:"contract"`
	Why           []string `json:"why"`
	Versioning    struct {
		Rule             string `json:"rule"`
		AddingAField     string `json:"adding_a_field"`
		RecordsAreClosed string `json:"records_are_closed"`
		SchemaSubset     string `json:"schema_subset"`
	} `json:"versioning"`
	Operations []struct {
		Operation string `json:"operation"`
		Argv      string `json:"argv"`
		Input     string `json:"input"`
		Output    string `json:"output"`
		Rule      string `json:"rule"`
	} `json:"operations"`
	Credentials struct {
		Issuer     string `json:"issuer"`
		OwnedBy    string `json:"owned_by"`
		Revocation struct {
			OnCancel                     string `json:"on_cancel"`
			Order                        string `json:"order"`
			ReissueAfterCancel           string `json:"reissue_after_cancel"`
			RevokeBeforeStop             bool   `json:"revoke_before_stop"`
			RefuseIssueBeforeEveryBoot   bool   `json:"refuse_issue_before_every_boot"`
			CancelledHandleCannotReissue bool   `json:"cancelled_handle_cannot_reissue"`
		} `json:"revocation"`
	} `json:"credentials"`
	Rules struct {
		CompletionContract struct {
			DrivesCollection      []string `json:"drives_collection"`
			NeverDrivesCollection []string `json:"never_drives_collection"`
			Why                   string   `json:"why"`
		} `json:"completion_contract"`
		Disposal struct {
			AllowedOnlyAfter []string `json:"allowed_only_after"`
			Why              string   `json:"why"`
		} `json:"disposal"`
	} `json:"rules"`
	Records map[string]struct {
		SchemaID     string          `json:"schema_id"`
		Description  string          `json:"description"`
		ReferencedBy []string        `json:"referenced_by"`
		Schema       json.RawMessage `json:"schema"`
	} `json:"records"`
	Defs     map[string]json.RawMessage `json:"$defs"`
	Examples struct {
		Golden []struct {
			Name     string          `json:"name"`
			Record   string          `json:"record"`
			Source   string          `json:"source"`
			Document json.RawMessage `json:"document"`
		} `json:"golden"`
		Negative []struct {
			Name                string          `json:"name"`
			Record              string          `json:"record"`
			Why                 string          `json:"why"`
			ExpectErrorContains string          `json:"expect_error_contains"`
			Document            json.RawMessage `json:"document"`
		} `json:"negative"`
	} `json:"examples"`
}

// theSevenRecords is the protocol's record set, pinned here so that a record
// dropped or renamed upstream fails as a named absence rather than as a table
// that quietly got shorter.
var theSevenRecords = map[string]string{
	"job_spec":    "ticfac.job-spec.v1",
	"job_handle":  "ticfac.job-handle.v1",
	"job_status":  "ticfac.job-status.v1",
	"cancel_ack":  "ticfac.cancel-ack.v1",
	"job_result":  "ticfac.job-result.v1",
	"role_result": "ticfac.role-result.v1",
	"evidence":    "ticfac.evidence.v1",
}

func loadJobProtocol(t *testing.T) (jobProtocol, map[string]*schema.Schema, map[string]*schema.Schema) {
	t.Helper()
	var c jobProtocol
	readContract(t, jobProtocolFile, &c)

	defs := parseDefs(t, c.Defs)
	records := make(map[string]*schema.Schema, len(c.Records))
	for _, name := range sortedKeys(c.Records) {
		record := c.Records[name]
		if len(record.Schema) == 0 {
			t.Fatalf("record %s carries no schema", name)
		}
		records[name] = parseSchema(t, "record "+name, record.Schema)
	}
	return c, records, defs
}

func TestJobProtocolIdentityAndRecords(t *testing.T) {
	c, _, _ := loadJobProtocol(t)

	if c.SchemaVersion != 1 || c.Contract != "ticfac.job-protocol" {
		t.Errorf("the contract does not identify itself: %q v%d", c.Contract, c.SchemaVersion)
	}
	if c.Versioning.Rule == "" || c.Versioning.AddingAField == "" {
		t.Error("versioning must state the rule and what adding a field costs")
	}

	for name, want := range theSevenRecords {
		record, ok := c.Records[name]
		if !ok {
			t.Errorf("record %q is missing from the contract", name)
			continue
		}
		if record.SchemaID != want {
			t.Errorf("record %s: schema_id = %q, want %q", name, record.SchemaID, want)
		}
		if record.Description == "" {
			t.Errorf("record %s: no description", name)
		}
	}
	for name := range c.Records {
		if _, ok := theSevenRecords[name]; !ok {
			t.Errorf("record %q is in the contract and not in the set this reader knows; "+
				"a new record needs a reader before it needs a schema", name)
		}
	}
}

// start/inspect/cancel/collect, each wired to the record it takes and returns.
// The protocol collapses WorkerProvider, Worker, Workspace and AgentRunner into
// exactly these four (SPEC §4.3); a fifth, or one wired to the wrong record, is
// a different protocol and ticfac would be built against the wrong shape.
func TestTheFourOperations(t *testing.T) {
	c, _, _ := loadJobProtocol(t)

	want := []struct{ op, in, out string }{
		{"start", "job_spec", "job_handle"},
		{"inspect", "job_handle", "job_status"},
		{"cancel", "job_handle", "cancel_ack"},
		{"collect", "job_handle", "job_result"},
	}
	if len(c.Operations) != len(want) {
		t.Fatalf("got %d operations, want exactly %d", len(c.Operations), len(want))
	}
	for i, w := range want {
		got := c.Operations[i]
		if got.Operation != w.op || got.Input != w.in || got.Output != w.out {
			t.Errorf("operation %d is %s(%s) -> %s, want %s(%s) -> %s",
				i, got.Operation, got.Input, got.Output, w.op, w.in, w.out)
		}
		if got.Argv == "" || got.Rule == "" {
			t.Errorf("%s: an operation must name its argv and the rule a caller follows", w.op)
		}
	}
}

func TestEveryGoldenDocumentValidates(t *testing.T) {
	c, records, defs := loadJobProtocol(t)

	if len(c.Examples.Golden) == 0 {
		t.Fatal("no golden example: nothing has ever been admitted by these schemas")
	}
	covered := map[string]bool{}
	illustration := false
	for _, g := range c.Examples.Golden {
		s, ok := records[g.Record]
		if !ok {
			t.Errorf("%s: golden example for unknown record %q", g.Name, g.Record)
			continue
		}
		covered[g.Record] = true
		if errs := schema.Validate(s, defs, decodeDocument(t, g.Document)); len(errs) != 0 {
			t.Errorf("%s (%s) is golden and its own schema refuses it:\n  %s",
				g.Name, g.Record, strings.Join(errs, "\n  "))
		}
		if strings.Contains(g.Source, "SPEC §4.3") {
			illustration = true
		}
	}
	for name := range theSevenRecords {
		if !covered[name] {
			t.Errorf("record %s has no golden example; a schema nothing has ever admitted is untested", name)
		}
	}
	// The SPEC's printed JobSpec is the document a reader copies first, so it
	// is the one that must not quietly stop validating.
	if !illustration {
		t.Error("no golden example is SPEC §4.3's printed JobSpec")
	}
}

func TestEveryNegativeDocumentIsRefusedForItsOwnReason(t *testing.T) {
	c, records, defs := loadJobProtocol(t)

	if len(c.Examples.Negative) == 0 {
		t.Fatal("no negative example: these schemas have never been seen to refuse anything")
	}
	covered := map[string]bool{}
	for _, bad := range c.Examples.Negative {
		s, ok := records[bad.Record]
		if !ok {
			t.Errorf("%s: negative example for unknown record %q", bad.Name, bad.Record)
			continue
		}
		covered[bad.Record] = true
		if bad.ExpectErrorContains == "" {
			t.Errorf("%s: pins no expected refusal", bad.Name)
			continue
		}
		errs := schema.Validate(s, defs, decodeDocument(t, bad.Document))
		if len(errs) == 0 {
			t.Errorf("%s (%s) was accepted: %s", bad.Name, bad.Record, bad.Why)
			continue
		}
		if !strings.Contains(strings.Join(errs, "\n"), bad.ExpectErrorContains) {
			t.Errorf("%s (%s) refused with\n  %s\nthe contract expects\n  %s",
				bad.Name, bad.Record, strings.Join(errs, "\n  "), bad.ExpectErrorContains)
		}
	}
	for name := range theSevenRecords {
		if !covered[name] {
			t.Errorf("record %s has no negative example; nothing has watched its schema refuse", name)
		}
	}
}

// Records are CLOSED, which is the opposite of the tk manifest's rule and
// deliberately so: an executor record is exchanged between two components that
// ship together, and there a field one side invents and the other ignores IS
// the bug. The one exception is JobHandle.handle, which is executor-private.
func TestRecordsAreClosed(t *testing.T) {
	c, records, _ := loadJobProtocol(t)

	if c.Versioning.RecordsAreClosed == "" {
		t.Error("the contract does not state that its records are closed")
	}
	for _, name := range sortedKeys(records) {
		s := records[name]
		if s.AdditionalProperties == nil || *s.AdditionalProperties {
			// A record built entirely out of anyOf alternatives closes each
			// alternative rather than itself.
			if len(s.AnyOf) > 0 {
				for i, alt := range s.AnyOf {
					if alt.Ref == "" && (alt.AdditionalProperties == nil || *alt.AdditionalProperties) {
						t.Errorf("record %s: anyOf[%d] is open", name, i)
					}
				}
				continue
			}
			if s.Ref != "" {
				continue
			}
			t.Errorf("record %s is open; an added field would be a silent break rather than a version bump", name)
		}
	}
}

// The completion contract and the disposal rule, which are the two places
// SPEC §10 stops an executor from calling a job done. Terminal output is
// diagnostic material and is never a completion contract.
func TestCompletionAndDisposalRules(t *testing.T) {
	c, _, _ := loadJobProtocol(t)

	if len(c.Rules.CompletionContract.DrivesCollection) == 0 {
		t.Error("nothing drives collection, so anything could")
	}
	if len(c.Rules.CompletionContract.NeverDrivesCollection) == 0 {
		t.Fatal("the contract names nothing that never drives collection")
	}
	joined := strings.ToLower(strings.Join(c.Rules.CompletionContract.NeverDrivesCollection, " "))
	if !strings.Contains(joined, "terminal output") {
		t.Errorf("terminal output is no longer excluded from what drives collection: %v",
			c.Rules.CompletionContract.NeverDrivesCollection)
	}
	for _, drives := range c.Rules.CompletionContract.DrivesCollection {
		for _, never := range c.Rules.CompletionContract.NeverDrivesCollection {
			if drives == never {
				t.Errorf("%q both drives collection and never does", drives)
			}
		}
	}
	if len(c.Rules.Disposal.AllowedOnlyAfter) == 0 {
		t.Error("disposal is allowed after nothing in particular, which is disposal before persistence")
	}

	// Revoke-then-stop, the ordering Appendix A #1's second half is about, and
	// which credential-ownership.json pins from the other side.
	rev := c.Credentials.Revocation
	if rev.Order != "revoke-then-stop" || !rev.RevokeBeforeStop {
		t.Errorf("the revocation order is %q; a container torn down before its credential is revoked can spend on the way out", rev.Order)
	}
	if rev.ReissueAfterCancel != "refused" || !rev.CancelledHandleCannotReissue {
		t.Error("a cancelled handle that can reissue is a cancellation that did not cancel")
	}
	if c.Credentials.Issuer == "" || c.Credentials.OwnedBy == "" {
		t.Error("credentials are part of the protocol; the contract must say who issues them and who owns them")
	}
}

// The evidence record is the one this contract DEFINES and another names. The
// pointer is asserted here; run_state_test.go follows it from the other side.
func TestTheEvidenceRecordDeclaresItsOtherReader(t *testing.T) {
	c, _, _ := loadJobProtocol(t)

	evidence := c.Records["evidence"]
	if len(evidence.ReferencedBy) == 0 {
		t.Fatal("the evidence record does not say which other contract places it; " +
			"bundle 1.2.0 shipped two schemas for one file because neither said so")
	}
	found := false
	for _, ref := range evidence.ReferencedBy {
		if strings.HasSuffix(ref, runStateFile) {
			found = true
		}
	}
	if !found {
		t.Errorf("the evidence record is referenced by %v, which does not include %s",
			evidence.ReferencedBy, runStateFile)
	}
}
