package subprocess

import (
	"reflect"
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
//   - the block is a JSON array of CLOSED records: every field required,
//     empty included, and no field the record does not have;
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

func TestFindingsAsAnyCarriesTheRecordFieldForField(t *testing.T) {
	findings := []Finding{{Kind: FindingKindContract, Title: "t", Body: "b", Severity: FindingSeverityHigh, Target: "pengelbrecht/ticks"}}
	as := FindingsAsAny(findings)
	if len(as) != 1 {
		t.Fatalf("as %v", as)
	}
	want := map[string]any{"kind": "contract", "title": "t", "body": "b", "severity": "high", "target": "pengelbrecht/ticks"}
	if !reflect.DeepEqual(as[0], want) {
		t.Fatalf("as[0] %v, want %v", as[0], want)
	}
	if got := FindingsAsAny(nil); len(got) != 0 || got == nil {
		t.Fatalf("an empty list is stated as [], not nil: %v", got)
	}
}
