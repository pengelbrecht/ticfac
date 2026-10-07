package subprocess

import (
	"strings"
	"testing"
)

// A finding whose fix is an edit of a file the worker boundary refuses
// carries the exact change, for the run to apply (epic-v5t's yck).
//
// short: parses strings; no I/O.
func TestAFindingCarriesTheProtectedChangeThatFixesIt(t *testing.T) {
	t.Parallel()
	body := "# x\n\n```findings v2\n[{\"kind\":\"defect\",\"title\":\"Named cloud configs are missing\",\"severity\":\"high\"," +
		"\"protected_change\":{\"path\":\".tick/runners.cloud.toml\",\"append\":\"[configs.claude]\\nmodel = 1\"}}]\n```\n\nSTATUS: DONE\n"
	report := ParseReport(body)
	if report.FindingsProblem != "" {
		t.Fatalf("the block was refused: %s", report.FindingsProblem)
	}
	if len(report.Findings) != 1 || report.Findings[0].ProtectedChange == nil {
		t.Fatalf("the finding carries no protected change: %+v", report.Findings)
	}
	change := *report.Findings[0].ProtectedChange
	if change.Path != ".tick/runners.cloud.toml" || change.Append != "[configs.claude]\nmodel = 1" {
		t.Errorf("the finding's change is %+v", change)
	}
	if len(report.FindingsFolded) != 0 {
		t.Errorf("protected_change was folded as an unknown key: %v", report.FindingsFolded)
	}

	for name, bad := range map[string]string{
		"an unprotected path":   strings.Replace(body, ".tick/runners.cloud.toml", "src/a.go", 1),
		"a tracker record":      strings.Replace(body, ".tick/runners.cloud.toml", ".tick/issues/x.json", 1),
		"the run's own state":   strings.Replace(body, ".tick/runners.cloud.toml", ".ticfac/runs/r/x.json", 1),
		"both content and text": strings.Replace(body, `"append"`, `"content":"x","append"`, 1),
		"an unknown key":        strings.Replace(body, `"append"`, `"mode":"x","append"`, 1),
	} {
		if got := ParseReport(bad); !strings.Contains(got.FindingsProblem, "protected_change") {
			t.Errorf("%s read as %q / %+v", name, got.FindingsProblem, got.Findings)
		}
	}
}

// An append is idempotent against the file's exact bytes: a resumed run
// writes nothing twice.
//
// short: pure functions.
func TestAProtectedAppendIsAppliedOnce(t *testing.T) {
	t.Parallel()
	change := ProtectedChange{Path: ".tick/runners.cloud.toml", Append: "[configs.x]"}
	once := change.Applied("[roles]")
	if once != "[roles]\n[configs.x]\n" {
		t.Errorf("applied once: %q", once)
	}
	if twice := change.Applied(once); twice != once {
		t.Errorf("applied twice: %q", twice)
	}
	if got := (ProtectedChange{Path: ".tick/runners.cloud.toml", Content: "new"}).Applied("old"); got != "new" {
		t.Errorf("a replacement is %q", got)
	}
	text := "put it in .tick/runners.cloud.toml. Not .tick/issues/x.json or .tick/runners.toml"
	if got := ProtectedPathsIn(text, false); len(got) != 1 || got[0] != ".tick/runners.cloud.toml" {
		t.Errorf("ProtectedPathsIn(local) = %v, want runners.cloud.toml alone: a local worker writes runners.toml", got)
	}
	// The cloud's boundary refuses all of .tick/ (tick 9sy), runners.toml too.
	if got := ProtectedPathsIn(text, true); len(got) != 2 || got[1] != ".tick/runners.toml" {
		t.Errorf("ProtectedPathsIn(cloud) = %v, want runners.cloud.toml and runners.toml", got)
	}
}
