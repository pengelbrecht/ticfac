package subprocess

import (
	"path/filepath"
	"strings"
	"testing"
)

// The tracker-edit proposal (hn6 run_d51a, yjq). A tick whose deliverable is
// a tracker edit — yjq's was a one-field re-flow of hn6's acceptance_criteria —
// could never be done by a worker: the boundary forbids .tick/ writes and tk,
// rightly, so yjq answered BLOCKED with the exact text in prose, three times,
// and the run redispatched it up the ladder into the same wall. The worker
// now PROPOSES the edit in a typed block and the run applies it.

// The yjq record as it stood: six [A<n>] marks on one line, so Parse read
// one item and A2–A6 were unaddressable.
const yjqHeld = "[A1] watch renders the fixed layout; [A2] a held run shows the hold; [A3] enter shows attempts; " +
	"[A4] cost says not metered; [A5] status --json carries every field; [A6] Bombadil properties pass in CI"

const yjqReflowed = "[A1] watch renders the fixed layout;\n[A2] a held run shows the hold;\n[A3] enter shows attempts;\n" +
	"[A4] cost says not metered;\n[A5] status --json carries every field;\n[A6] Bombadil properties pass in CI"

func trackerEditsReport(block string) string {
	return "# yjq\n\nThe re-flow, proposed for the run to apply.\n\n```tracker-edits\n" + block + "\n```\n\n```findings v2\n[]\n```\n\nSTATUS: DONE\n"
}

// short: parses strings; no I/O.
func TestATrackerEditsBlockIsReadAsTypedEdits(t *testing.T) {
	t.Parallel()
	block := `[{"tick": "hn6", "field": "acceptance_criteria", "value": ` + jsonString(yjqReflowed) + `},
 {"tick": "yjq", "field": "notes", "value": "re-flowed hn6's acceptance one item per line"}]`
	report := ParseReport(trackerEditsReport(block))
	if report.TrackerEditsProblem != "" {
		t.Fatalf("a well-formed block was refused: %s", report.TrackerEditsProblem)
	}
	want := []TrackerEdit{
		{Tick: "hn6", Field: TrackerFieldAcceptance, Value: yjqReflowed},
		{Tick: "yjq", Field: TrackerFieldNotes, Value: "re-flowed hn6's acceptance one item per line"},
	}
	if len(report.TrackerEdits) != len(want) {
		t.Fatalf("read %d edit(s), want %d: %+v", len(report.TrackerEdits), len(want), report.TrackerEdits)
	}
	for i := range want {
		if report.TrackerEdits[i] != want[i] {
			t.Errorf("edit %d is %+v, want %+v", i, report.TrackerEdits[i], want[i])
		}
	}
	if problems := ReportRefusal("implement-tick", report); len(problems) != 0 {
		t.Errorf("collect refuses a report with a well-formed block: %v", problems)
	}

	// A version tag on the fence is read the same.
	tagged := strings.Replace(trackerEditsReport(block), "```tracker-edits\n", "```tracker-edits v1\n", 1)
	if got := ParseReport(tagged); got.TrackerEditsProblem != "" || len(got.TrackerEdits) != 2 {
		t.Errorf("a v1-tagged fence read as %+v / %q", got.TrackerEdits, got.TrackerEditsProblem)
	}

	// No block is no edits, and no problem.
	if got := ParseReport("# x\n\nSTATUS: DONE\n"); got.TrackerEdits != nil || got.TrackerEditsProblem != "" {
		t.Errorf("a report with no block read as %+v / %q", got.TrackerEdits, got.TrackerEditsProblem)
	}
}

// A proposal the run cannot apply as written is refused at the report, where
// the worker can still fix it — never folded, never guessed at: an edit is a
// WRITE, and a write half-understood is a write to the wrong place. Above all
// the fields that are the run's own (status, owner, parent, the graph's
// edges) are never a worker's to propose.
//
// short: parses strings; no I/O.
func TestATrackerEditTheRunCannotApplyIsRefusedAtTheReport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, block, want string
	}{
		{"status is the run's", `[{"tick":"yjq","field":"status","value":"closed"}]`, "the run's own"},
		{"owner is the run's", `[{"tick":"yjq","field":"owner","value":"me"}]`, "the run's own"},
		{"an unknown field", `[{"tick":"yjq","field":"colour","value":"red"}]`, "acceptance_criteria, description or notes"},
		{"an unknown key", `[{"tick":"yjq","field":"notes","value":"x","why":"y"}]`, `"why"`},
		{"no tick", `[{"field":"notes","value":"x"}]`, "tick"},
		{"a tick id that is a path", `[{"tick":"../hn6","field":"notes","value":"x"}]`, "tick id"},
		{"an empty value", `[{"tick":"yjq","field":"description","value":"  "}]`, "empty"},
		{"a control character", `[{"tick":"yjq","field":"notes","value":"a\u0000b"}]`, "control character"},
		{"not an array", `{"tick":"yjq","field":"notes","value":"x"}`, "JSON array"},
		{"an empty array", `[]`, "no edit"},
		{"an acceptance that does not parse", `[{"tick":"hn6","field":"acceptance_criteria","value":"[A01] leading zero"}]`, "acceptance"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := ParseReport(trackerEditsReport(tc.block))
			if report.TrackerEditsProblem == "" {
				t.Fatalf("the block was accepted: %+v", report.TrackerEdits)
			}
			if !strings.Contains(report.TrackerEditsProblem, tc.want) {
				t.Errorf("the refusal %q does not say %q", report.TrackerEditsProblem, tc.want)
			}
			if report.TrackerEdits != nil {
				t.Errorf("a refused block still carried edits: %+v", report.TrackerEdits)
			}
			// Collect cannot act on it: it is a fatal report problem, pushed
			// back in-session and retried like any report nobody can read.
			problems := ReportRefusal("implement-tick", report)
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), "tracker-edits block") {
				t.Errorf("collect does not refuse the report over it: %v", problems)
			}
		})
	}

	unclosed := "# x\n\n```tracker-edits\n[{\"tick\":\"yjq\",\"field\":\"notes\",\"value\":\"x\"}]\n\nSTATUS: DONE\n"
	if got := ParseReport(unclosed); !strings.Contains(got.TrackerEditsProblem, "never closes") {
		t.Errorf("an unclosed block read as %q", got.TrackerEditsProblem)
	}
}

// A finding whose whole fix is a tracker edit carries the exact change, and
// the run applies it rather than filing a tick a worker could never do.
//
// short: parses strings; no I/O.
func TestAFindingCarriesTheTrackerEditThatFixesIt(t *testing.T) {
	t.Parallel()
	body := "# x\n\n```findings v2\n[{\"kind\":\"defect\",\"title\":\"hn6's acceptance parses as one item\",\"severity\":\"medium\"," +
		"\"tracker_edit\":{\"tick\":\"hn6\",\"field\":\"acceptance_criteria\",\"value\":" + jsonString(yjqReflowed) + "}}]\n```\n\nSTATUS: DONE\n"
	report := ParseReport(body)
	if report.FindingsProblem != "" {
		t.Fatalf("the block was refused: %s", report.FindingsProblem)
	}
	if len(report.Findings) != 1 || report.Findings[0].TrackerEdit == nil {
		t.Fatalf("the finding carries no tracker edit: %+v", report.Findings)
	}
	edit := *report.Findings[0].TrackerEdit
	if edit.Tick != "hn6" || edit.Field != TrackerFieldAcceptance || edit.Value != yjqReflowed {
		t.Errorf("the finding's edit is %+v", edit)
	}
	if len(report.FindingsFolded) != 0 {
		t.Errorf("tracker_edit was folded as an unknown key: %v", report.FindingsFolded)
	}

	bad := strings.Replace(body, `"acceptance_criteria"`, `"status"`, 1)
	if got := ParseReport(bad); !strings.Contains(got.FindingsProblem, "tracker_edit") {
		t.Errorf("a finding proposing a status edit read as %q / %+v", got.FindingsProblem, got.Findings)
	}
}

// The checker knows the checkout: an edit naming a tick the tracker does not
// carry, or an acceptance edit that would drop an item the record marks, is
// pushed back in-session — and the yjq re-flow, which drops nothing, passes.
//
// short: one temporary directory, no processes.
func TestTheCheckerHoldsATrackerEditToTheCheckoutsRecords(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".tick", "issues", "hn6.json"),
		`{"id":"hn6","type":"epic","acceptance_criteria":`+jsonString(yjqHeld)+`}`)
	mustWrite(t, filepath.Join(repo, ".tick", "issues", "yjq.json"), `{"id":"yjq","type":"task","parent":"hn6"}`)
	ctx := LoadLintContext(repo, "implement-tick", "yjq")

	clean := trackerEditsReport(`[{"tick":"hn6","field":"acceptance_criteria","value":` + jsonString(yjqReflowed) + `}]`)
	if result := LintReport(clean, ctx); !result.Clean() {
		t.Fatalf("the yjq re-flow fails the check:\n%s", result.Text())
	}

	dropped := strings.Replace(yjqReflowed, "\n[A6] Bombadil properties pass in CI", "", 1)
	result := LintReport(trackerEditsReport(`[{"tick":"hn6","field":"acceptance_criteria","value":`+jsonString(dropped)+`}]`), ctx)
	if result.Clean() || !strings.Contains(result.Text(), "A6") {
		t.Errorf("an acceptance edit dropping A6 passes the check:\n%s", result.Text())
	}

	result = LintReport(trackerEditsReport(`[{"tick":"zzz","field":"notes","value":"x"}]`), ctx)
	if result.Clean() || !strings.Contains(result.Text(), "zzz") {
		t.Errorf("an edit of a tick the checkout does not carry passes the check:\n%s", result.Text())
	}
}
