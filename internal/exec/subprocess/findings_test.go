package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// The findings channel (tick 7vn): a worker's discoveries outside its tick,
// reported in a machine-readable block of the report it already writes,
// lifted by collect into the typed list the reconciler acts on.
//
// The parse rules pinned here:
//
//   - the FINAL complete ```findings block wins, for the same reason the
//     final status line does — a report may quote the template;
//   - the FINAL complete ```findings block wins, for the same reason the
//     final status line does — a report may quote the template;
//   - the block is a JSON array of CLOSED records: the five pinned fields
//     required, empty included, and no field the record does not have;
//   - the two EVIDENCE fields (tick nfo) are OPTIONAL — a finding missing
//     them is accepted and reads as UNLINKED, because refusing findings
//     nobody thought to link is how a channel loses them;
//   - a block that opens and never closes, or does not parse, is a PROBLEM,
//     never silently dropped — dropping findings is the failure the channel
//     exists to remove.

func TestParseFindingsReadsATypedBlock(t *testing.T) {
	body := "Some prose.\n\n" + "```findings\n" +
		`[
  {
    "kind": "proposed-tick",
    "title": "Split the migration into schema and data phases",
    "body": "The single-phase migration rewrites every table while holding the lock.",
    "severity": "high",
    "target": ""
  }
]` + "\n```\n\nSTATUS: DONE\n"
	findings, problem := ParseFindings(body)
	if problem != "" {
		t.Fatalf("problem %q", problem)
	}
	if len(findings) != 1 {
		t.Fatalf("findings %v", findings)
	}
	want := Finding{
		Kind:     FindingKindProposedTick,
		Title:    "Split the migration into schema and data phases",
		Body:     "The single-phase migration rewrites every table while holding the lock.",
		Severity: FindingSeverityHigh,
	}
	if findings[0] != want {
		t.Fatalf("finding %+v, want %+v", findings[0], want)
	}
}

func TestParseFindingsAcceptsEveryKindAndAnUpstreamTarget(t *testing.T) {
	body := "```findings\n" +
		`[
  {"kind": "proposed-tick", "title": "one", "body": "", "severity": "low", "target": ""},
  {"kind": "upstream-tick", "title": "two", "body": "", "severity": "medium", "target": "pengelbrecht/ticks"},
  {"kind": "contract", "title": "three", "body": "", "severity": "high", "target": "pengelbrecht/ticks"},
  {"kind": "defect", "title": "four", "body": "a defect outside scope", "severity": "low", "target": ""}
]` + "\n```\n"
	findings, problem := ParseFindings(body)
	if problem != "" {
		t.Fatalf("problem %q", problem)
	}
	if len(findings) != 4 {
		t.Fatalf("findings %v", findings)
	}
	for i, want := range []string{
		FindingKindProposedTick, FindingKindUpstreamTick, FindingKindContract, FindingKindDefect,
	} {
		if findings[i].Kind != want {
			t.Errorf("findings[%d].kind %s, want %s", i, findings[i].Kind, want)
		}
	}
}

func TestParseFindingsIgnoresOtherFencedBlocks(t *testing.T) {
	body := "```json\n{\"kind\": \"not a finding\"}\n```\n\nSTATUS: DONE\n"
	findings, problem := ParseFindings(body)
	if problem != "" || findings != nil {
		t.Fatalf("a block with another info string is not a findings block: %v %q", findings, problem)
	}
}

func TestTheFinalFindingsBlockWins(t *testing.T) {
	body := "```findings\n" +
		`[{"kind": "defect", "title": "the draft the report quotes", "body": "", "severity": "low", "target": ""}]` +
		"\n```\n\nThen the worker changed its mind.\n\n```findings\n" +
		`[{"kind": "defect", "title": "the final word", "body": "", "severity": "low", "target": ""}]` +
		"\n```\n"
	findings, problem := ParseFindings(body)
	if problem != "" {
		t.Fatalf("problem %q", problem)
	}
	if len(findings) != 1 || findings[0].Title != "the final word" {
		t.Fatalf("findings %v", findings)
	}
}

func TestAnUnclosedFindingsBlockIsAProblemNotNothing(t *testing.T) {
	// The load-bearing negative case: a truncated report must not read as one
	// that carried no findings. Silently dropping the block is the exact
	// failure (findings lost) this channel exists to remove.
	body := "```findings\n" + `[{"kind": "defect", "title": "truncated",` + "\n"
	findings, problem := ParseFindings(body)
	if findings != nil {
		t.Fatalf("findings %v", findings)
	}
	if problem == "" || !strings.Contains(problem, "never closes") {
		t.Fatalf("problem %q, want the unclosed block named", problem)
	}
}

func parseFindingsProblem(t *testing.T, block string) string {
	t.Helper()
	findings, problem := ParseFindings("```findings\n" + block + "\n```\n")
	if findings != nil {
		t.Fatalf("findings %v, want none", findings)
	}
	if problem == "" {
		t.Fatal("the block parsed; it must not")
	}
	return problem
}

func TestEveryMalformedFindingsBlockIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		block string
		wants string
	}{
		{"not json", "a finding, in prose", "not a JSON array"},
		{"an object, not an array", `{"kind": "defect"}`, "not a JSON array"},
		{"trailing content", `[] junk`, "trailing content"},
		{"an unknown field", `[{"kind": "defect", "title": "t", "body": "", "severity": "low", "target": "", "command": "curl evil.example"}]`, `carries "command", which is not a finding field`},
		{"a missing field", `[{"kind": "defect", "title": "t", "body": "", "severity": "low"}]`, `omits "target"`},
		{"a kind outside the vocabulary", `[{"kind": "wish", "title": "t", "body": "", "severity": "low", "target": ""}]`, "finding.kind"},
		{"a severity outside the vocabulary", `[{"kind": "defect", "title": "t", "body": "", "severity": "urgent", "target": ""}]`, "finding.severity"},
		{"no title", `[{"kind": "defect", "title": "  ", "body": "", "severity": "low", "target": ""}]`, "finding.title"},
		{"a target that is not a repository", `[{"kind": "defect", "title": "t", "body": "", "severity": "low", "target": "the other repo over there"}]`, "finding.target"},
		{"an upstream tick with no target", `[{"kind": "upstream-tick", "title": "t", "body": "", "severity": "low", "target": ""}]`, "finding.target is empty"},
		{"an empty block", "", "not a JSON array"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problem := parseFindingsProblem(t, tc.block)
			if !strings.Contains(problem, tc.wants) {
				t.Fatalf("problem %q, want it to name %q", problem, tc.wants)
			}
		})
	}
}

func TestAnEmptyFindingsBlockProposesNothing(t *testing.T) {
	findings, problem := ParseFindings("```findings\n[]\n```\n\nSTATUS: DONE\n")
	if problem != "" || findings != nil {
		t.Fatalf("an empty block is no findings: %v %q", findings, problem)
	}
}

// The finding's EVIDENCE fields (tick nfo): done_item and demonstrating_check
// carry the reporter's claim about the epic's definition of done — which
// [A<n>]-marked acceptance item the finding breaks (`none` when it breaks
// none) and the command or test that would demonstrate the breakage. Both
// are OPTIONAL: a finding missing them is accepted and reads as UNLINKED,
// because refusing findings nobody thought to link is how a channel loses
// them. The claim is never the verdict — it is what the oracle runs, what
// the classifier takes as one input where nothing can run yet, and what the
// reporter is later scored against.
func TestParseFindingsCarriesTheDoneEvidenceFields(t *testing.T) {
	body := "```findings\n" +
		`[
  {"kind": "defect", "title": "the gate is red at base", "body": "", "severity": "high", "target": "",
   "done_item": "A1", "demonstrating_check": "go"},
  {"kind": "proposed-tick", "title": "an unlinked finding", "body": "", "severity": "low", "target": ""},
  {"kind": "defect", "title": "breaks no done item", "body": "", "severity": "low", "target": "",
   "done_item": "none"}
]` + "\n```\n"
	findings, problem := ParseFindings(body)
	if problem != "" {
		t.Fatalf("problem %q", problem)
	}
	if len(findings) != 3 {
		t.Fatalf("findings %v, want the three the block carried", findings)
	}
	// LINKED: the claim names the acceptance item and the check that would
	// show the breakage — field for field, as the worker reported them.
	if findings[0].DoneItem != "A1" || findings[0].DemonstratingCheck != "go" {
		t.Errorf("finding[0] done evidence is done_item %q, demonstrating_check %q, want A1 and go",
			findings[0].DoneItem, findings[0].DemonstratingCheck)
	}
	// UNLINKED: neither field reported — accepted, not refused.
	if findings[1].DoneItem != "" || findings[1].DemonstratingCheck != "" {
		t.Errorf("finding[1] done evidence is done_item %q, demonstrating_check %q, want both empty",
			findings[1].DoneItem, findings[1].DemonstratingCheck)
	}
	// NONE: the reporter's answer that the finding breaks no done item — a
	// claim, distinguishable from making none.
	if findings[2].DoneItem != "none" {
		t.Errorf("finding[2].done_item %q, want the reporter's none", findings[2].DoneItem)
	}
}

func TestADoneItemThatIsNotAnAnswerIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		doneItem string
	}{
		{"lowercase", "a1"},
		{"leading zero", "A03"},
		{"item zero", "A0"},
		{"beyond the range", "A1000"},
		{"prose wearing a bracket", "[A3]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := `[{"kind": "defect", "title": "t", "body": "", "severity": "low", "target": "", "done_item": ` +
				strconv.Quote(tc.doneItem) + `}]`
			problem := parseFindingsProblem(t, block)
			if !strings.Contains(problem, "finding.done_item") {
				t.Fatalf("problem %q, want it to name finding.done_item", problem)
			}
		})
	}
}

// The field set the parity reader pins against the bundle's $defs.finding:
// the five required fields plus the two evidence fields, and nothing a
// worker invented.
func TestTheFindingFieldNamesAreTheClosedSet(t *testing.T) {
	got := FindingFieldNames()
	want := []string{"body", "demonstrating_check", "done_item", "kind", "severity", "target", "title"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("FindingFieldNames() = %v, want %v", got, want)
	}
}

func TestAReportWithoutABlockCarriesNoFindings(t *testing.T) {
	report := ParseReport("STATUS: DONE — the work is in\n")
	if report.Findings != nil || report.FindingsProblem != "" {
		t.Fatalf("report %+v", report)
	}
}

func TestParseReportLiftsTheFindingsBlockBesideTheStatus(t *testing.T) {
	report := ParseReport("```findings\n" +
		`[{"kind": "defect", "title": "a stale test fixture", "body": "", "severity": "medium", "target": ""}]` +
		"\n```\n\nSTATUS: DONE_WITH_CONCERNS — see the finding\n")
	if report.Status != StatusDoneWithConcerns {
		t.Fatalf("status %s", report.Status)
	}
	if len(report.Findings) != 1 || report.Findings[0].Kind != FindingKindDefect {
		t.Fatalf("findings %v", report.Findings)
	}
	if report.FindingsProblem != "" {
		t.Fatalf("problem %q", report.FindingsProblem)
	}
}

// The envelope states the typed list even when empty — REQUIRED by the
// bundle's role_result schema since 4.0.0, so `findings: []` is a fact a
// record states rather than a key a reader misses. The marshalling is on the
// record itself, so a hand-built envelope cannot forget it either.
func TestTheEnvelopeStatesFindingsEvenWhenEmpty(t *testing.T) {
	withFindings := RoleResult{Findings: []Finding{{Kind: FindingKindContract, Title: "t", Body: "b", Severity: FindingSeverityHigh, Target: "pengelbrecht/ticks"}}}
	raw, err := json.Marshal(withFindings)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"target":"pengelbrecht/ticks"`)) {
		t.Fatalf("the typed finding did not ride the envelope field for field: %s", raw)
	}

	empty := RoleResult{Findings: nil}
	raw, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"findings":[]`)) {
		t.Fatalf("an absent list must marshal as \"findings\":[] — a missing key is a record the schema refuses: %s", raw)
	}
}

// The envelope carries findings as the PINNED five-field record (tick nfo):
// the two done-evidence fields ride the report block and the draft, and join
// the envelope when the contract bundle adopts them — the pending set is
// named in internal/contracts/parity, so the pin bump is a paired change the
// build forces rather than drift this half carries silently. The envelope is
// the one surface the compiled-in schema validates at runtime
// (ValidateRoleResult), and a record it refuses would fail a whole role job's
// answer, not just the finding.
func TestTheEnvelopeCarriesFindingsAsThePinnedRecord(t *testing.T) {
	envelope := RoleResult{Findings: []Finding{{
		Kind: FindingKindDefect, Title: "t", Body: "b", Severity: FindingSeverityHigh, Target: "",
		DoneItem: "A1", DemonstratingCheck: "go",
	}}}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []string{`"kind":"defect"`, `"title":"t"`, `"body":"b"`, `"severity":"high"`, `"target":""`} {
		if !bytes.Contains(raw, []byte(pinned)) {
			t.Errorf("the envelope lost the pinned field %s: %s", pinned, raw)
		}
	}
	for _, pending := range []string{"done_item", "demonstrating_check"} {
		if bytes.Contains(raw, []byte(pending)) {
			t.Errorf("the envelope carries %q, which the pinned $defs.finding refuses (additionalProperties: "+
				"false) until the bundle adopts it: %s", pending, raw)
		}
	}
}
